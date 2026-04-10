// Package main is the RavenLink entry point — wires all subsystems together.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/autostart"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/config"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/dashboard"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntlogger"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/obsclient"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/statemachine"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/tray"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/typeconv"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/uploader"
)

const Banner = `
╔══════════════════════════════════════╗
║        RavenLink v0.1.0             ║
║   FRC Robot Data Bridge for 1310    ║
╚══════════════════════════════════════╝
`

func main() {
	// Load config
	cfgPath := "config.yaml"
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		fmt.Fprintln(os.Stderr, "Using defaults. Create config.yaml or pass flags.")
		cfg = config.DefaultConfig()
	}

	// Apply CLI flag overrides
	config.ParseFlags(cfg)

	// Require team number
	if cfg.Bridge.Team == 0 {
		fmt.Fprintln(os.Stderr, "Error: team number is required (set in config.yaml or pass --team)")
		os.Exit(1)
	}

	// Set up logging
	level := slog.LevelInfo
	switch cfg.Bridge.LogLevel {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARNING":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	fmt.Print(Banner)
	slog.Info("Starting RavenLink",
		"team", cfg.Bridge.Team,
		"robot_ip", cfg.RobotIP(),
		"obs", fmt.Sprintf("%s:%d", cfg.Bridge.OBSHost, cfg.Bridge.OBSPort),
		"record_trigger", cfg.Bridge.RecordTrigger,
		"data_dir", cfg.Telemetry.DataDir,
	)

	// Auto-start registration
	autostart.Sync(cfg.Bridge.LaunchOnLogin)

	// Shared status
	st := status.New()

	// Create data directory
	if err := os.MkdirAll(cfg.Telemetry.DataDir, 0o755); err != nil {
		slog.Error("Failed to create data dir", "err", err)
		os.Exit(1)
	}

	// Context and shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	quitCh := make(chan struct{}, 1)

	var wg sync.WaitGroup

	// NT4 client
	nt := ntclient.New("ravenlink", 1024)
	nt.Connect(cfg.Bridge.Team, 5810, cfg.Telemetry.NTPaths)
	defer nt.Close()

	// OBS client
	obs := obsclient.New(cfg.Bridge.OBSHost, cfg.Bridge.OBSPort, cfg.Bridge.OBSPassword)
	if err := obs.Connect(); err != nil {
		slog.Warn("OBS connection failed — will retry", "err", err)
	}
	// Start the background health check. It pings OBS every 5 seconds with
	// a per-call timeout and refreshes the cached IsConnected state; it
	// also handles background reconnect attempts. The goroutine exits on
	// ctx cancellation, so Close() below will not block.
	obs.StartHealthCheck(ctx)
	defer obs.Close()

	// State machine
	sm := statemachine.NewMachine(
		statemachine.WithStopDelay(cfg.Bridge.StopDelay),
		statemachine.WithAutoTeleopGap(cfg.Bridge.AutoTeleopGap),
		statemachine.WithNTDisconnectGrace(cfg.Bridge.NTDisconnectGrace),
		statemachine.WithRecordTrigger(cfg.Bridge.RecordTrigger),
	)

	// NT logger — fans out the single NT values channel
	// We need to tee the channel so both state machine and logger see updates.
	logCh := make(chan ntclient.TopicValue, 512)
	fmsCh := make(chan ntclient.TopicValue, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(logCh)
		defer close(fmsCh)
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-nt.Values():
				if !ok {
					return
				}
				// Send to logger (drop if full)
				select {
				case logCh <- v:
				default:
				}
				// Send to state machine FMS watcher (drop if full)
				if v.Name == statemachine.FMSControlDataKey {
					select {
					case fmsCh <- v:
					default:
					}
				}
			}
		}
	}()

	ntLog := ntlogger.New(logCh, cfg.Telemetry.DataDir, cfg.Bridge.Team)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ntLog.Run(ctx)
	}()

	// Uploader + auth
	if cfg.RavenBrain.URL != "" && !strings.HasPrefix(strings.ToLower(cfg.RavenBrain.URL), "https://") {
		slog.Warn("!!! INSECURE ravenbrain_url: credentials will NOT be sent — configure https:// to enable upload",
			"ravenbrain_url", cfg.RavenBrain.URL,
		)
	}
	auth := uploader.NewAuth(cfg.RavenBrain.URL, cfg.RavenBrain.Username, cfg.RavenBrain.Password)
	// The uploader runs in its own goroutine so its (potentially slow)
	// HTTP calls never block the main state-machine loop. It consults
	// ntLog.Stats().ActiveSessionID to avoid uploading the session file
	// that is currently being written.
	up := uploader.New(
		cfg.Telemetry.DataDir,
		auth,
		cfg.RavenBrain.BatchSize,
		time.Duration(cfg.RavenBrain.UploadInterval*float64(time.Second)),
		func() string { return ntLog.Stats().ActiveSessionID },
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		up.Run(ctx)
	}()

	// Dashboard
	var dash *dashboard.Server
	if cfg.Dashboard.Enabled {
		dash = dashboard.New(cfg, cfgPath, st, func() {
			slog.Info("Config reloaded from dashboard")
			autostart.Sync(cfg.Bridge.LaunchOnLogin)
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			dash.Start(ctx, cfg.Dashboard.Port)
		}()
	}

	// Tray icon (runs on main goroutine on macOS)
	dashboardURL := fmt.Sprintf("http://localhost:%d", cfg.Dashboard.Port)
	trayIcon := tray.New(dashboardURL, quitCh)

	// Main loop goroutine — runs state machine + upload + status updates
	wg.Add(1)
	go func() {
		defer wg.Done()
		runMainLoop(ctx, cfg, sm, nt, obs, ntLog, up, st, dash, trayIcon, fmsCh, auth)
	}()

	// Wait for shutdown signal
	go func() {
		select {
		case <-sigCh:
			slog.Info("Received signal, shutting down")
		case <-quitCh:
			slog.Info("Quit requested from tray, shutting down")
		}
		cancel()
		if trayIcon != nil {
			trayIcon.Stop()
		}
	}()

	// Tray must run on main goroutine (required by macOS)
	// On Windows/Linux it could run in a goroutine, but we unify for simplicity.
	if runtime.GOOS == "darwin" {
		trayIcon.Start() // blocks until Stop() is called
	} else {
		go trayIcon.Start()
	}

	// Wait for all goroutines
	wg.Wait()

	// Final cleanup
	up.PruneUploaded(cfg.Telemetry.RetentionDays)
	slog.Info("Goodbye!")
}

// runMainLoop is the coordinator: reads FMS state, runs state machine, executes OBS actions.
func runMainLoop(
	ctx context.Context,
	cfg *config.Config,
	sm *statemachine.Machine,
	nt *ntclient.Client,
	obs *obsclient.Client,
	ntLog *ntlogger.Logger,
	up *uploader.Uploader,
	st *status.Status,
	dash *dashboard.Server,
	trayIcon *tray.Tray,
	fmsCh <-chan ntclient.TopicValue,
	auth *uploader.Auth,
) {
	pollInterval := time.Duration(cfg.Bridge.PollInterval * float64(time.Second))
	if pollInterval < 10*time.Millisecond {
		pollInterval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	statusTicker := time.NewTicker(5 * time.Second)
	defer statusTicker.Stop()

	// Uploads are driven by uploader.Run in its own goroutine (wired in
	// main) so slow HTTP calls never block this state-machine loop.

	rateTicker := time.NewTicker(1 * time.Second)
	defer rateTicker.Stop()

	// Latest FMS state
	var fms statemachine.FMSState = statemachine.FMSStateDisconnected()
	prevState := statemachine.Idle
	lastEntries := 0
	// Track NT connectivity transitions so we can drive session lifecycle.
	// Start as "not yet connected" so the first observed true flips to
	// StartSession.
	prevNTConnected := false

	for {
		select {
		case <-ctx.Done():
			// Stop recording on shutdown if active
			if sm.State == statemachine.RecordingAuto ||
				sm.State == statemachine.RecordingTeleop ||
				sm.State == statemachine.StopPending {
				slog.Info("Stopping active recording before exit")
				obs.StopRecording()
			}
			return

		case v, ok := <-fmsCh:
			if !ok {
				return
			}
			// Parse FMS state from topic value
			if raw, ok := typeconv.ToInt(v.Value); ok {
				fms = statemachine.FMSStateFromRaw(raw)
			}

		case <-ticker.C:
			// Drive logger session lifecycle from NT connectivity edges.
			ntConnected := nt.Connected()
			if ntConnected && !prevNTConnected {
				slog.Info("NT connected — starting telemetry session")
				ntLog.StartSession()
			} else if !ntConnected && prevNTConnected {
				slog.Info("NT disconnected — ending telemetry session")
				ntLog.EndSession()
			}
			prevNTConnected = ntConnected

			// Update state machine
			if !ntConnected {
				fms = statemachine.FMSStateDisconnected()
			}
			actions := sm.Update(fms)

			// Match markers on state transitions
			if sm.State == statemachine.RecordingAuto && prevState == statemachine.Idle {
				ntLog.RecordMatchEvent("match_start", fms)
			}
			if sm.State == statemachine.StopPending &&
				(prevState == statemachine.RecordingAuto || prevState == statemachine.RecordingTeleop) {
				ntLog.RecordMatchEvent("match_end", fms)
			}
			prevState = sm.State

			// Execute OBS actions
			for _, action := range actions {
				switch action {
				case statemachine.StartRecord:
					if !obs.StartRecording() {
						slog.Error("Failed to start OBS recording")
					}
				case statemachine.StopRecord:
					if !obs.StopRecording() {
						slog.Error("Failed to stop OBS recording")
					}
				}
			}

		case <-rateTicker.C:
			// Update entries-per-second gauge
			entries := ntLog.Stats().EntriesWritten
			rate := float64(entries - lastEntries)
			lastEntries = entries
			st.Update(func(s *status.Status) {
				s.EntriesPerSecond = rate
			})

		case <-statusTicker.C:
			// Periodic full status refresh
			stats := ntLog.Stats()
			st.Update(func(s *status.Status) {
				s.NTConnected = nt.Connected()
				s.OBSConnected = obs.IsConnected()
				s.RavenBrainReachable = auth.IsConfigured()
				s.MatchState = stateName(sm.State)
				s.ActiveSessionFile = stats.ActiveSessionID
				s.EntriesWritten = stats.EntriesWritten
				s.FilesPending = up.FilesPending
				s.FilesUploaded = up.FilesUploaded
				s.LastUploadResult = up.LastUploadResult
				s.CurrentlyUploading = up.CurrentlyUploading
				s.OBSRecording = sm.State == statemachine.RecordingAuto || sm.State == statemachine.RecordingTeleop
			})
			if dash != nil {
				dash.UpdateStatus(st)
			}
			trayIcon.UpdateStatus(st)

			slog.Info("Status",
				"nt", nt.Connected(),
				"fms", fmt.Sprintf("%v", fms),
				"state", stateName(sm.State),
				"entries", stats.EntriesWritten,
				"pending", up.FilesPending,
			)
		}
	}
}

func stateName(s statemachine.State) string {
	switch s {
	case statemachine.Idle:
		return "IDLE"
	case statemachine.RecordingAuto:
		return "RECORDING_AUTO"
	case statemachine.RecordingTeleop:
		return "RECORDING_TELEOP"
	case statemachine.StopPending:
		return "STOP_PENDING"
	}
	return "UNKNOWN"
}

