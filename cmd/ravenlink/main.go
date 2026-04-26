// Package main is the RavenLink entry point — wires all subsystems together.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/autostart"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/collect"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/config"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/dashboard"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/lifecycle"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/limelight"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntlogger"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/obsclient"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/paths"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/statemachine"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/tray"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/typeconv"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/uploader"
)

// openLogFile creates/appends to the OS-standard log file. Returns
// (nil, err) if the log path can't be computed — the caller should
// fall back to stdout-only logging.
func openLogFile() (*os.File, error) {
	p, err := paths.LogPath()
	if err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
}

// logFilePath returns the log file path for display purposes.
func logFilePath() string {
	p, err := paths.LogPath()
	if err != nil {
		return "(unknown)"
	}
	return p
}

// writeTemplateConfig writes a first-run example config.yaml to path.
func writeTemplateConfig(path string) error {
	tmpl := `# RavenLink first-run config template.
# Edit this file (at minimum, set team) then relaunch RavenLink.
# Enable ravenbrain and/or ravenscope below to activate uploads.

bridge:
  team: 0                       # REQUIRED: your FRC team number
  nt_host: ""                   # empty = derive 10.TE.AM.2 from team. "localhost" for WPILib sim.
  obs_host: localhost
  obs_port: 4455
  obs_password: ""
  stop_delay: 10
  poll_interval: 0.05
  log_level: INFO
  record_trigger: fms           # fms | auto | any — when to run OBS
  collect_trigger: fms          # fms | auto | any — when to log/upload NT data
  auto_teleop_gap: 5
  nt_disconnect_grace: 15
  launch_on_login: true

telemetry:
  nt_paths:
    - /FMSInfo/
    - /.schema/
    - /SmartDashboard/
    - /Shuffleboard/
  data_dir: ./data
  retention_days: 30

# RavenBrain (team-hosted server, username/password → JWT).
# Active only when enabled=true AND url is non-empty.
ravenbrain:
  enabled: false
  url: ""                       # https://ravenbrain.team1310.ca (or leave empty to disable)
  username: telemetry-agent
  password: ""
  batch_size: 50
  upload_interval: 10

# RavenScope (Cloudflare Worker, bearer API key — no /login).
# Independent of ravenbrain; either, both, or neither can run.
# http://localhost:* is accepted for local wrangler dev; otherwise https:// required.
ravenscope:
  enabled: true
  url: "https://ravenscope.team1310.ca"
  api_key: ""                   # rsk_live_…
  batch_size: 50
  upload_interval: 10

dashboard:
  enabled: true
  port: 8080

limelight:
  enabled: true                 # poll Limelight /results endpoint for uptime
  last_octets: [11]             # 10.TE.AM.<octet> for each camera
  poll_interval: 2.0            # seconds between polls per camera
  timeout_ms: 1000              # per-request HTTP timeout
`
	return os.WriteFile(path, []byte(tmpl), 0o600)
}

const Banner = `
╔══════════════════════════════════════╗
║        RavenLink v0.1.0             ║
║   FRC Robot Data Bridge for 1310    ║
╚══════════════════════════════════════╝
`

func main() {
	// Resolve and chdir to the app directory so config.yaml and data/
	// paths work regardless of whether we were launched from a terminal
	// (cwd=project) or from Finder/Explorer (cwd=/ or $HOME).
	appDir, err := paths.AppDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve app directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.Chdir(appDir); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to chdir to %s: %v\n", appDir, err)
		os.Exit(1)
	}

	// Set up a log file in the OS-standard location so logs are
	// available even when launched detached (no terminal attached).
	logFile, logErr := openLogFile()
	defer func() {
		if logFile != nil {
			_ = logFile.Close()
		}
	}()

	// Load config from the (now current) app directory.
	cfgPath := "config.yaml"
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		// First-run bootstrap: write a template config and exit with a
		// helpful message. Users can edit the template and re-launch.
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			if writeErr := writeTemplateConfig(cfgPath); writeErr == nil {
				msg := fmt.Sprintf(
					"First run detected.\n"+
						"A template config was written to:\n  %s\n"+
						"Edit it (set team, and optionally configure ravenbrain / ravenscope upload targets) and relaunch.\n"+
						"Logs: %s\n",
					filepath.Join(appDir, cfgPath),
					logFilePath(),
				)
				fmt.Fprint(os.Stderr, msg)
				if logFile != nil {
					_, _ = logFile.WriteString(msg)
				}
				os.Exit(2)
			}
		}
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		fmt.Fprintln(os.Stderr, "Using defaults. Create config.yaml or pass flags.")
		cfg = config.DefaultConfig()
	}

	// Apply CLI flag overrides
	config.ParseFlags(cfg)

	// First-run / unconfigured: if team is still 0, don't exit. Instead,
	// start the dashboard (which always works) and auto-open the browser
	// so the user can fill in config via the web UI. The main loop will
	// wait until config is saved + reloaded with a valid team, then
	// proceed to start NT/OBS/logger/uploader components.
	firstRun := cfg.Bridge.Team == 0

	// Set up logging — tee to stdout AND the log file so Finder/Explorer
	// launches still have persistent diagnostic output.
	level := slog.LevelInfo
	switch cfg.Bridge.LogLevel {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARNING":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}
	var logWriter io.Writer = os.Stdout
	if logFile != nil {
		logWriter = io.MultiWriter(os.Stdout, logFile)
	}
	dashHandler := &dashLogHandler{
		inner: slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: level}),
	}
	slog.SetDefault(slog.New(dashHandler))
	if logErr != nil {
		slog.Warn("could not open log file", "err", logErr)
	}
	slog.Info("resolved app directory", "path", appDir)
	slog.Info("log file", "path", logFilePath())

	fmt.Print(Banner)
	if firstRun {
		slog.Warn("first run: no team configured — dashboard will open for initial setup",
			"config", filepath.Join(appDir, cfgPath),
		)
	} else {
		slog.Info("Starting RavenLink",
			"team", cfg.Bridge.Team,
			"robot_ip", cfg.RobotIP(),
			"obs", fmt.Sprintf("%s:%d", cfg.Bridge.OBSHost, cfg.Bridge.OBSPort),
			"record_trigger", cfg.Bridge.RecordTrigger,
			"data_dir", cfg.Telemetry.DataDir,
		)
	}

	// Auto-start registration
	autostart.Sync(cfg.Bridge.LaunchOnLogin)

	// Shared status
	st := status.New()

	// Wire slog → dashboard log buffer now that the status struct exists.
	dashHandler.hook = func(msg string) {
		st.Update(func(s *status.Status) { s.AddLog(msg) })
	}

	// Runtime pause flag for NT data collection + RavenBrain upload.
	// Shared between main loop (drives session lifecycle), uploader
	// (gates HTTP transmission), and dashboard (Pause/Resume buttons).
	collectState := collect.NewState()

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

	// restartRequested is set to true by the dashboard "restart" or
	// "save" handler. When the main goroutine reaches the end of its
	// shutdown sequence, it re-execs self if this is true.
	var restartRequested bool

	var wg sync.WaitGroup
	var (
		nt    *ntclient.Client
		obs   *obsclient.Client
		ntLog *ntlogger.Logger
		up    *uploader.Uploader
		sm    *statemachine.Machine
		// collectSM drives NT logger session lifecycle + uploader gating.
		// It's a second statemachine.Machine running in parallel with sm,
		// configured with its own trigger mode so data collection can be
		// scoped differently from OBS recording.
		collectSM *statemachine.Machine
		fmsCh     chan ntclient.TopicValue
	)

	// ============================================================
	// FULL STARTUP (only when we have a valid team number)
	// First-run mode skips all subsystems and starts only the
	// dashboard + tray + browser so the user can configure via UI.
	// ============================================================
	if !firstRun {
		// NT4 client. Use the explicit host override when set (sim /
		// bring-up scenarios) so we connect to localhost:5810 or a
		// specified host instead of the team-derived 10.TE.AM.2.
		nt = ntclient.New("ravenlink", 1024)
		if cfg.Bridge.NTHost != "" {
			slog.Info("ntclient: using NT host override", "host", cfg.Bridge.NTHost)
			nt.ConnectAddress(cfg.Bridge.NTHost, 5810, cfg.Telemetry.NTPaths)
		} else {
			nt.Connect(cfg.Bridge.Team, 5810, cfg.Telemetry.NTPaths)
		}
		defer nt.Close()

		// OBS client
		obs = obsclient.New(cfg.Bridge.OBSHost, cfg.Bridge.OBSPort, cfg.Bridge.OBSPassword)
		if err := obs.Connect(); err != nil {
			slog.Warn("OBS connection failed — will retry", "err", err)
		}
		obs.StartHealthCheck(ctx)
		defer obs.Close()

		// State machine (OBS recording)
		sm = statemachine.NewMachine(
			statemachine.WithStopDelay(cfg.Bridge.StopDelay),
			statemachine.WithAutoTeleopGap(cfg.Bridge.AutoTeleopGap),
			statemachine.WithNTDisconnectGrace(cfg.Bridge.NTDisconnectGrace),
			statemachine.WithRecordTrigger(cfg.Bridge.RecordTrigger),
		)

		// Second state machine for NT data collection. Reuses all of the
		// same gap/stop-delay/NT-grace handling as OBS recording, but with
		// an independent trigger so you can (e.g.) collect only during
		// FMS matches while leaving OBS on "any".
		collectSM = statemachine.NewMachine(
			statemachine.WithStopDelay(cfg.Bridge.StopDelay),
			statemachine.WithAutoTeleopGap(cfg.Bridge.AutoTeleopGap),
			statemachine.WithNTDisconnectGrace(cfg.Bridge.NTDisconnectGrace),
			statemachine.WithRecordTrigger(cfg.Bridge.CollectTrigger),
		)

		// Limelight monitor — optional. When enabled, emits uptime and
		// reachability values as TopicValues that ride the same logger
		// pipeline as NetworkTables data.
		var llValues <-chan ntclient.TopicValue
		if cfg.Limelight.Enabled {
			lm := limelight.New(
				cfg.Bridge.Team,
				cfg.Limelight.LastOctets,
				time.Duration(cfg.Limelight.PollInterval*float64(time.Second)),
				time.Duration(cfg.Limelight.TimeoutMS)*time.Millisecond,
				32,
			)
			llValues = lm.Values()
			wg.Add(1)
			go func() {
				defer wg.Done()
				lm.Run(ctx)
			}()
		}

		// NT logger — fans out the single NT values channel, and merges
		// Limelight monitor output into the same logger input channel.
		logCh := make(chan ntclient.TopicValue, 512)
		fmsCh = make(chan ntclient.TopicValue, 64)
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
					select {
					case logCh <- v:
					default:
					}
					if v.Name == statemachine.FMSControlDataKey {
						select {
						case fmsCh <- v:
						default:
						}
					}
				case v, ok := <-llValues:
					// When Limelight monitor is disabled, llValues is
					// nil and this case never fires.
					if !ok {
						llValues = nil
						continue
					}
					select {
					case logCh <- v:
					default:
					}
				}
			}
		}()

		ntLog = ntlogger.New(logCh, cfg.Telemetry.DataDir, cfg.Bridge.Team)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ntLog.Run(ctx)
		}()

		// Uploader + targets. A target runs when its section is Enabled
		// AND its URL is non-empty; anything else (disabled, unset URL,
		// or failed construction) silently drops the target. An empty
		// targets slice is a valid state — the uploader goroutine exits
		// immediately and files stay in pending/ (local-only mode).
		targets := buildUploadTargets(cfg)
		up = uploader.New(
			cfg.Telemetry.DataDir,
			targets,
			func() string { return ntLog.Stats().ActiveSessionID },
		)
		up.SetPauseFn(collectState.Paused)
		wg.Add(1)
		go func() {
			defer wg.Done()
			up.Run(ctx)
		}()
	}

	// ============================================================
	// ALWAYS START: dashboard + tray + browser
	// ============================================================

	// Dashboard
	var dash *dashboard.Server
	if cfg.Dashboard.Enabled {
		dash = dashboard.New(cfg, cfgPath, cfg.Telemetry.DataDir, st, func() {
			slog.Info("Config reloaded from dashboard")
			autostart.Sync(cfg.Bridge.LaunchOnLogin)
		})
		dash.SetCollectState(collectState)
		if ntLog != nil {
			dash.SetActiveSessionFn(func() string { return ntLog.Stats().ActiveSessionID })
		}
		// Wire lifecycle hooks: dashboard Save/Restart/Shutdown buttons.
		// Save triggers a restart so new config is applied fresh without
		// hot-reload complexity.
		dash.SetLifecycleHooks(
			func() { // shutdown
				slog.Info("shutdown hook invoked from dashboard")
				cancel()
				select {
				case quitCh <- struct{}{}:
				default:
				}
			},
			func() { // restart
				slog.Info("restart hook invoked from dashboard")
				restartRequested = true
				cancel()
				select {
				case quitCh <- struct{}{}:
				default:
				}
			},
		)
		wg.Add(1)
		go func() {
			defer wg.Done()
			dash.Start(ctx, cfg.Dashboard.Port)
		}()
	}

	// Tray icon (runs on main goroutine on macOS)
	dashboardURL := fmt.Sprintf("http://localhost:%d", cfg.Dashboard.Port)
	trayIcon := tray.New(dashboardURL, quitCh)

	// Main loop goroutine — runs state machine + status updates.
	// Launched AFTER dash and trayIcon exist so it can pass real
	// (non-nil) values into runMainLoop, which dereferences them on
	// every status tick.
	if !firstRun {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runMainLoop(ctx, cfg, sm, collectSM, collectState, nt, obs, ntLog, up, st, dash, trayIcon, fmsCh)
		}()
	}

	// Auto-open the browser to the dashboard on startup. This gives
	// the user a visible confirmation that RavenLink launched, and
	// doubles as the first-run setup flow when team==0.
	//
	// Suppressed in --minimized mode (used by autostart on login) so
	// the user doesn't get a surprise browser window on every reboot.
	// First-run is an exception: if we genuinely have no config yet,
	// we still open the wizard so the user can complete setup.
	if cfg.Dashboard.Enabled && (!cfg.Minimized || firstRun) {
		go func() {
			// Give the HTTP listener a moment to bind.
			time.Sleep(300 * time.Millisecond)
			if firstRun {
				slog.Info("first run — opening browser to config wizard")
				lifecycle.OpenBrowser(dashboardURL + "#config")
			} else {
				slog.Info("opening browser to dashboard", "url", dashboardURL)
				lifecycle.OpenBrowser(dashboardURL)
			}
		}()
	} else if cfg.Minimized {
		slog.Info("started --minimized; skipping browser auto-open")
	}

	// Wait for shutdown signal
	go func() {
		select {
		case <-sigCh:
			slog.Info("Received signal, shutting down")
		case <-quitCh:
			slog.Info("Quit requested, shutting down")
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

	// Wait for all goroutines to exit cleanly. At this point:
	//   - The NT client has stopped
	//   - The logger has flushed its bufio.Writer, written a session_end
	//     marker, and closed its JSONL file (via the defer in Run())
	//   - The uploader goroutine has exited its ticker loop
	// The most recent session file is now sitting in data/pending/ with no
	// active writer, eligible for upload.
	wg.Wait()

	// Phase 2: drain any pending files with a bounded deadline. Skip
	// this in first-run mode since there's no uploader to drain.
	if up != nil {
		const drainDeadline = 30 * time.Second
		slog.Info("draining pending uploads before exit", "deadline", drainDeadline)
		drainCtx, drainCancel := context.WithTimeout(context.Background(), drainDeadline)
		up.DrainPending(drainCtx, "")
		drainCancel()
		up.PruneUploaded(cfg.Telemetry.RetentionDays)
	}

	// Self-restart if requested via dashboard. This happens AFTER the
	// full shutdown + drain sequence, so all pending data is flushed
	// and the replacement process starts cleanly. On Unix this replaces
	// the current process in place (syscall.Exec); on Windows it
	// spawns a new process and exits.
	if restartRequested {
		if logFile != nil {
			_ = logFile.Close()
		}
		if err := lifecycle.RestartSelf(); err != nil {
			slog.Error("self-restart failed", "err", err)
			os.Exit(1)
		}
		// unreachable on Unix (exec replaces process)
	}

	slog.Info("Goodbye!")
}

// runMainLoop is the coordinator: reads FMS state, runs state machine, executes OBS actions.
func runMainLoop(
	ctx context.Context,
	cfg *config.Config,
	sm *statemachine.Machine,
	collectSM *statemachine.Machine,
	collectState *collect.State,
	nt *ntclient.Client,
	obs *obsclient.Client,
	ntLog *ntlogger.Logger,
	up *uploader.Uploader,
	st *status.Status,
	dash *dashboard.Server,
	trayIcon *tray.Tray,
	fmsCh <-chan ntclient.TopicValue,
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
			ntConnected := nt.Connected()

			// Update state machine
			if !ntConnected {
				fms = statemachine.FMSStateDisconnected()
			}

			// Failsafe: a paused session always auto-resumes when we see
			// a live FMS match starting up. Collection data for real
			// matches is too important to rely on the user remembering
			// to unpause.
			if collectState.Paused() && fms.Enabled && fms.FMSAttached {
				slog.Info("collection auto-resumed: FMS match detected")
				collectState.Resume()
			}

			// Drive NT logger session lifecycle from the collection
			// state machine. Its StartRecord/StopRecord actions become
			// StartSession/EndSession on the ntLogger.
			if collectState.Paused() {
				// Force the collect machine and the logger back to idle
				// while paused. Values continue flowing through the
				// channel (so NT stays responsive) but ntLogger drops
				// them because its file handle is nil.
				if collectSM.State != statemachine.Idle {
					collectSM.Reset()
					ntLog.EndSession()
				}
			} else {
				for _, action := range collectSM.Update(fms) {
					switch action {
					case statemachine.StartRecord:
						slog.Info("collection started", "trigger", cfg.Bridge.CollectTrigger)
						ntLog.StartSession()
					case statemachine.StopRecord:
						slog.Info("collection stopped")
						ntLog.EndSession()
					}
				}
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
			targetSnaps := make([]status.UploadTargetStatus, 0, len(up.Targets()))
			totalPending := 0
			for _, tgt := range up.Targets() {
				snap := tgt.Snapshot()
				targetSnaps = append(targetSnaps, status.UploadTargetStatus{
					Name:               snap.Name,
					Enabled:            snap.Enabled,
					Reachable:          snap.Reachable,
					FilesPending:       snap.FilesPending,
					FilesUploaded:      snap.FilesUploaded,
					CurrentlyUploading: snap.CurrentlyUploading,
					LastResult:         snap.LastResult,
				})
				totalPending += snap.FilesPending
			}
			st.Update(func(s *status.Status) {
				s.NTConnected = nt.Connected()
				s.OBSConnected = obs.IsConnected()
				s.MatchState = stateName(sm.State)
				s.ActiveSessionFile = stats.ActiveSessionID
				s.EntriesWritten = stats.EntriesWritten
				s.UploadTargets = targetSnaps
				s.OBSRecording = sm.State == statemachine.RecordingAuto || sm.State == statemachine.RecordingTeleop
				s.CollectTrigger = cfg.Bridge.CollectTrigger
				s.CollectPaused = collectState.Paused()
				s.CollectActive = !collectState.Paused() &&
					(collectSM.State == statemachine.RecordingAuto ||
						collectSM.State == statemachine.RecordingTeleop)
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
				"pending", totalPending,
			)
		}
	}
}

// dashLogHandler wraps an slog.Handler and copies every log record into
// a callback. This lets us tee slog output into the dashboard's
// RecentLogs ring buffer without touching the logging hot path.
type dashLogHandler struct {
	inner slog.Handler
	hook  func(string) // set after status is created
}

func (h *dashLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *dashLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.hook != nil {
		// Format: "LEVEL message key=val key=val ..."
		var b strings.Builder
		b.WriteString(r.Level.String())
		b.WriteByte(' ')
		b.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			b.WriteByte(' ')
			b.WriteString(a.Key)
			b.WriteByte('=')
			b.WriteString(a.Value.String())
			return true
		})
		h.hook(b.String())
	}
	return h.inner.Handle(ctx, r)
}

func (h *dashLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &dashLogHandler{inner: h.inner.WithAttrs(attrs), hook: h.hook}
}

func (h *dashLogHandler) WithGroup(name string) slog.Handler {
	return &dashLogHandler{inner: h.inner.WithGroup(name), hook: h.hook}
}

// buildUploadTargets instantiates one uploader.Target per enabled
// config section (ravenbrain + ravenscope). A target is only built when
// its Enabled flag is true AND its URL is non-empty. Any plaintext http://
// URL is logged as insecure and skipped — credentials must not traverse
// a plaintext connection. Returns an empty slice when nothing should
// upload; the caller treats that as local-only mode.
func buildUploadTargets(cfg *config.Config) []*uploader.Target {
	var targets []*uploader.Target

	if cfg.RavenBrain.Enabled && cfg.RavenBrain.URL != "" {
		if !uploader.IsSecureURL(cfg.RavenBrain.URL) {
			slog.Warn("!!! INSECURE ravenbrain.url: credentials will NOT be sent — use https:// or http://localhost",
				"url", cfg.RavenBrain.URL)
		}
		auth := uploader.NewAuth(cfg.RavenBrain.URL, cfg.RavenBrain.Username, cfg.RavenBrain.Password)
		t, err := uploader.NewTarget(
			"ravenbrain", auth,
			cfg.RavenBrain.BatchSize,
			time.Duration(cfg.RavenBrain.UploadInterval*float64(time.Second)),
		)
		if err != nil {
			slog.Error("uploader: failed to build ravenbrain target", "err", err)
		} else {
			targets = append(targets, t)
		}
	}

	if cfg.RavenScope.Enabled && cfg.RavenScope.URL != "" {
		if !uploader.IsSecureURL(cfg.RavenScope.URL) {
			slog.Warn("!!! INSECURE ravenscope.url: api key will NOT be sent — use https:// or http://localhost",
				"url", cfg.RavenScope.URL)
		}
		auth := uploader.NewAuthWithKey(cfg.RavenScope.URL, cfg.RavenScope.APIKey)
		t, err := uploader.NewTarget(
			"ravenscope", auth,
			cfg.RavenScope.BatchSize,
			time.Duration(cfg.RavenScope.UploadInterval*float64(time.Second)),
		)
		if err != nil {
			slog.Error("uploader: failed to build ravenscope target", "err", err)
		} else {
			targets = append(targets, t)
		}
	}

	return targets
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

