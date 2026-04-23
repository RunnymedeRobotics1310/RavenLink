// Package dashboard provides an embedded web dashboard for monitoring
// RavenLink status and editing configuration via a browser.
package dashboard

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/assets"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/lifecycle"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/collect"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/config"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/wpilog"
)

//go:embed static/*
var staticFS embed.FS

// maskedPassword is the sentinel returned in place of sensitive values in
// GET /api/config. When received on POST, it means "leave unchanged".
const maskedPassword = "***"

// restartRequiredFields lists config keys whose changes only take effect
// after a process restart. log_level and launch_on_login are the only
// fields that actually hot-reload.
var restartRequiredFields = []string{
	"team",
	"obs_host",
	"obs_port",
	"obs_password",
	"stop_delay",
	"poll_interval",
	"auto_teleop_gap",
	"nt_disconnect_grace",
	"record_trigger",
	"collect_trigger",
	"nt_paths",
	"data_dir",
	"retention_days",
	"ravenbrain_enabled",
	"ravenbrain_url",
	"ravenbrain_username",
	"ravenbrain_password",
	"ravenbrain_batch_size",
	"ravenbrain_upload_interval",
	"ravenscope_enabled",
	"ravenscope_url",
	"ravenscope_api_key",
	"ravenscope_batch_size",
	"ravenscope_upload_interval",
	"dashboard_enabled",
	"dashboard_port",
	"limelight_enabled",
	"limelight_last_octets",
	"limelight_poll_interval",
	"limelight_timeout_ms",
}

// Server is the embedded HTTP dashboard.
type Server struct {
	mu           sync.RWMutex
	status       *status.Status
	cfg          *config.Config
	cfgPath      string
	port         int
	reloadHook   func() // optional callback after config reload
	shutdownHook func() // optional callback when /api/shutdown is hit
	restartHook  func() // optional callback when /api/restart is hit
	collect      *collect.State // optional: wired by SetCollectState
	dataDir      string         // captured at construction; immutable
	activeIDFn   func() string  // returns active session ID; nil = none
}

// New creates a new dashboard server.
// cfgPath is the path to the YAML config file for save/reload.
// dataDir is captured at construction time so config edits don't change
// the directory the running process is actually writing to.
// reloadHook is called (if non-nil) after a config reload via the API.
func New(cfg *config.Config, cfgPath, dataDir string, st *status.Status, reloadHook func()) *Server {
	return &Server{
		status:     st,
		cfg:        cfg,
		cfgPath:    cfgPath,
		dataDir:    dataDir,
		reloadHook: reloadHook,
	}
}

// SetActiveSessionFn registers a function that returns the session ID
// currently being written by the logger. Used by the sessions API to
// exclude the active file from export.
func (s *Server) SetActiveSessionFn(fn func() string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeIDFn = fn
}

// SetLifecycleHooks wires callbacks for the Shutdown and Restart endpoints.
// If either is nil, the corresponding endpoint returns 501 Not Implemented.
func (s *Server) SetLifecycleHooks(shutdown, restart func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownHook = shutdown
	s.restartHook = restart
}

// SetCollectState wires the pause flag used by /api/collect/pause and
// /api/collect/resume. If not set, those endpoints return 501.
func (s *Server) SetCollectState(c *collect.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collect = c
}

// UpdateStatus replaces the status pointer the dashboard serves.
func (s *Server) UpdateStatus(st *status.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = st
}

// Start begins serving HTTP on the given port, bound to loopback only
// (127.0.0.1). It blocks until ctx is cancelled, then shuts down
// gracefully.
func (s *Server) Start(ctx context.Context, port int) {
	s.mu.Lock()
	s.port = port
	s.mu.Unlock()

	mux := http.NewServeMux()

	// Serve the embedded static files. The embed root is "static/*",
	// so we strip the prefix to serve index.html at "/".
	staticContent, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServer(http.FS(staticContent)))

	mux.HandleFunc("GET /logo.png", s.handleLogo)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("POST /api/config", s.requireSameOrigin(s.handleConfigPost))
	mux.HandleFunc("POST /api/config/reload", s.requireSameOrigin(s.handleConfigReload))
	mux.HandleFunc("POST /api/shutdown", s.requireSameOrigin(s.handleShutdown))
	mux.HandleFunc("POST /api/restart", s.requireSameOrigin(s.handleRestart))
	mux.HandleFunc("POST /api/collect/pause", s.requireSameOrigin(s.handleCollectPause))
	mux.HandleFunc("POST /api/collect/resume", s.requireSameOrigin(s.handleCollectResume))
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/sessions/{id}/wpilog", s.handleSessionWPILog)
	mux.HandleFunc("POST /api/sessions/{id}/open", s.requireSameOrigin(s.handleSessionOpen))

	// Bind only to loopback — this dashboard is for the local user,
	// not a service to expose on the LAN.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down dashboard server")
		// Graceful shutdown: allow in-flight requests to finish
		// responding (most importantly the one that triggered the
		// shutdown — the browser needs to receive the 200 before
		// the TCP connection closes, or fetch() throws TypeError).
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("dashboard started", "addr", fmt.Sprintf("http://127.0.0.1:%d", port))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("dashboard server error", "err", err)
	}
}

// ---------- Middleware ----------

// requireSameOrigin wraps a handler with Origin + Host header validation
// to provide CSRF protection for state-changing POST endpoints. We reject
// any request whose Origin (if present) or Host does not match
// localhost/127.0.0.1 on our listening port.
func (s *Server) requireSameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		port := s.port
		s.mu.RUnlock()

		if !hostAllowed(r.Host, port) {
			slog.Warn("dashboard: rejecting request with unexpected Host header",
				"host", r.Host, "path", r.URL.Path)
			writeJSONError(w, http.StatusForbidden, "bad host")
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" {
			if !originAllowed(origin, port) {
				slog.Warn("dashboard: rejecting request with unexpected Origin header",
					"origin", origin, "path", r.URL.Path)
				writeJSONError(w, http.StatusForbidden, "bad origin")
				return
			}
		}

		next(w, r)
	}
}

func hostAllowed(host string, port int) bool {
	expected1 := fmt.Sprintf("localhost:%d", port)
	expected2 := fmt.Sprintf("127.0.0.1:%d", port)
	return host == expected1 || host == expected2
}

func originAllowed(origin string, port int) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Host
	expected1 := fmt.Sprintf("localhost:%d", port)
	expected2 := fmt.Sprintf("127.0.0.1:%d", port)
	return host == expected1 || host == expected2
}

// ---------- Handlers ----------

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	st := s.status
	s.mu.RUnlock()

	data, err := st.ToJSON()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// handleLogo serves the embedded team logo so the dashboard header
// can reference it as <img src="/logo.png">.
func (s *Server) handleLogo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(assets.Team1310Logo)
}

// handleEvents is a Server-Sent Events stream that pushes the full
// status JSON once per second for the lifetime of the connection.
// Clients (the dashboard) use EventSource to consume it; the stream
// stays open across many ticks so the browser doesn't re-open a new
// HTTP request every second.
//
// A single ticker (not change-notification) keeps the implementation
// simple — the status fields are cheap to marshal and the dashboard
// has no strict real-time requirements.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering if any

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Send an immediate snapshot so the UI doesn't sit empty for the
	// first tick.
	s.writeStatusEvent(w, flusher)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !s.writeStatusEvent(w, flusher) {
				return
			}
		}
	}
}

// writeStatusEvent marshals the current status and writes one SSE
// "data:" frame. Returns false if the write failed (client gone), so
// the caller can unwind the handler.
func (s *Server) writeStatusEvent(w http.ResponseWriter, flusher http.Flusher) bool {
	s.mu.RLock()
	st := s.status
	s.mu.RUnlock()

	data, err := st.ToJSON()
	if err != nil {
		return true // keep the stream alive across one bad marshal
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (s *Server) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	// Mask sensitive fields so passwords are never sent to the browser.
	obsPwd := ""
	if cfg.Bridge.OBSPassword != "" {
		obsPwd = maskedPassword
	}
	rbPwd := ""
	if cfg.RavenBrain.Password != "" {
		rbPwd = maskedPassword
	}
	rsAPIKey := ""
	if cfg.RavenScope.APIKey != "" {
		rsAPIKey = maskedPassword
	}

	flat := map[string]any{
		"team":                       cfg.Bridge.Team,
		"obs_host":                   cfg.Bridge.OBSHost,
		"obs_port":                   cfg.Bridge.OBSPort,
		"obs_password":               obsPwd,
		"stop_delay":                 cfg.Bridge.StopDelay,
		"poll_interval":              cfg.Bridge.PollInterval,
		"log_level":                  cfg.Bridge.LogLevel,
		"auto_teleop_gap":            cfg.Bridge.AutoTeleopGap,
		"nt_disconnect_grace":        cfg.Bridge.NTDisconnectGrace,
		"record_trigger":             cfg.Bridge.RecordTrigger,
		"collect_trigger":            cfg.Bridge.CollectTrigger,
		"launch_on_login":            cfg.Bridge.LaunchOnLogin,
		"nt_paths":                   strings.Join(cfg.Telemetry.NTPaths, ", "),
		"data_dir":                   cfg.Telemetry.DataDir,
		"retention_days":             cfg.Telemetry.RetentionDays,
		"ravenbrain_enabled":         cfg.RavenBrain.Enabled,
		"ravenbrain_url":             cfg.RavenBrain.URL,
		"ravenbrain_username":        cfg.RavenBrain.Username,
		"ravenbrain_password":        rbPwd,
		"ravenbrain_batch_size":      cfg.RavenBrain.BatchSize,
		"ravenbrain_upload_interval": cfg.RavenBrain.UploadInterval,
		"ravenscope_enabled":         cfg.RavenScope.Enabled,
		"ravenscope_url":             cfg.RavenScope.URL,
		"ravenscope_api_key":         rsAPIKey,
		"ravenscope_batch_size":      cfg.RavenScope.BatchSize,
		"ravenscope_upload_interval": cfg.RavenScope.UploadInterval,
		"dashboard_enabled":          cfg.Dashboard.Enabled,
		"dashboard_port":             cfg.Dashboard.Port,
		"limelight_enabled":          cfg.Limelight.Enabled,
		"limelight_last_octets":      joinInts(cfg.Limelight.LastOctets, ", "),
		"limelight_poll_interval":    cfg.Limelight.PollInterval,
		"limelight_timeout_ms":       cfg.Limelight.TimeoutMS,
		"restart_required":           restartRequiredFields,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(flat)
}

func (s *Server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}

	// Validate everything up front, before mutating the live config.
	if err := validateConfigPost(data); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	cfg := s.cfg
	for key, raw := range data {
		val := fmt.Sprintf("%v", raw)
		switch key {
		case "team":
			cfg.Bridge.Team = toInt(val, cfg.Bridge.Team)
		case "obs_host":
			cfg.Bridge.OBSHost = val
		case "obs_port":
			cfg.Bridge.OBSPort = toInt(val, cfg.Bridge.OBSPort)
		case "obs_password":
			// Treat masked value as "leave unchanged".
			if val != maskedPassword {
				cfg.Bridge.OBSPassword = val
			}
		case "stop_delay":
			cfg.Bridge.StopDelay = toFloat(val, cfg.Bridge.StopDelay)
		case "poll_interval":
			cfg.Bridge.PollInterval = toFloat(val, cfg.Bridge.PollInterval)
		case "log_level":
			cfg.Bridge.LogLevel = val
		case "auto_teleop_gap":
			cfg.Bridge.AutoTeleopGap = toFloat(val, cfg.Bridge.AutoTeleopGap)
		case "nt_disconnect_grace":
			cfg.Bridge.NTDisconnectGrace = toFloat(val, cfg.Bridge.NTDisconnectGrace)
		case "record_trigger":
			cfg.Bridge.RecordTrigger = val
		case "collect_trigger":
			cfg.Bridge.CollectTrigger = val
		case "launch_on_login":
			cfg.Bridge.LaunchOnLogin = toBool(val)
		case "nt_paths":
			parts := strings.Split(val, ",")
			paths := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					paths = append(paths, p)
				}
			}
			cfg.Telemetry.NTPaths = paths
		case "data_dir":
			cfg.Telemetry.DataDir = val
		case "retention_days":
			cfg.Telemetry.RetentionDays = toInt(val, cfg.Telemetry.RetentionDays)
		case "ravenbrain_enabled":
			cfg.RavenBrain.Enabled = toBool(val)
		case "ravenbrain_url":
			cfg.RavenBrain.URL = val
		case "ravenbrain_username":
			cfg.RavenBrain.Username = val
		case "ravenbrain_password":
			if val != maskedPassword {
				cfg.RavenBrain.Password = val
			}
		case "ravenbrain_batch_size":
			cfg.RavenBrain.BatchSize = toInt(val, cfg.RavenBrain.BatchSize)
		case "ravenbrain_upload_interval":
			cfg.RavenBrain.UploadInterval = toFloat(val, cfg.RavenBrain.UploadInterval)
		case "ravenscope_enabled":
			cfg.RavenScope.Enabled = toBool(val)
		case "ravenscope_url":
			cfg.RavenScope.URL = val
		case "ravenscope_api_key":
			if val != maskedPassword {
				cfg.RavenScope.APIKey = val
			}
		case "ravenscope_batch_size":
			cfg.RavenScope.BatchSize = toInt(val, cfg.RavenScope.BatchSize)
		case "ravenscope_upload_interval":
			cfg.RavenScope.UploadInterval = toFloat(val, cfg.RavenScope.UploadInterval)
		case "dashboard_enabled":
			cfg.Dashboard.Enabled = toBool(val)
		case "dashboard_port":
			cfg.Dashboard.Port = toInt(val, cfg.Dashboard.Port)
		case "limelight_enabled":
			cfg.Limelight.Enabled = toBool(val)
		case "limelight_last_octets":
			parts := strings.Split(val, ",")
			octets := make([]int, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				n, err := strconv.Atoi(p)
				if err != nil {
					continue // validateConfigPost rejects bad input upstream
				}
				octets = append(octets, n)
			}
			cfg.Limelight.LastOctets = octets
		case "limelight_poll_interval":
			cfg.Limelight.PollInterval = toFloat(val, cfg.Limelight.PollInterval)
		case "limelight_timeout_ms":
			cfg.Limelight.TimeoutMS = toInt(val, cfg.Limelight.TimeoutMS)
		}
	}
	s.mu.Unlock()

	if err := cfg.SaveConfig(s.cfgPath); err != nil {
		slog.Error("failed to save config", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"saved"}`))
}

func (s *Server) handleConfigReload(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	newCfg, err := config.LoadConfig(s.cfgPath)
	if err != nil {
		s.mu.Unlock()
		slog.Error("failed to reload config", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "reload failed: "+err.Error())
		return
	}
	*s.cfg = *newCfg
	s.mu.Unlock()

	if s.reloadHook != nil {
		s.reloadHook()
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"reloaded"}`))
}

// handleShutdown triggers a graceful shutdown. Responds 200 immediately,
// then fires the hook in a goroutine so the client receives the response
// before the process begins tearing down.
func (s *Server) handleShutdown(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	hook := s.shutdownHook
	s.mu.RUnlock()
	if hook == nil {
		writeJSONError(w, http.StatusNotImplemented, "shutdown not wired")
		return
	}
	slog.Info("shutdown requested via dashboard")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	_, _ = w.Write([]byte(`{"status":"shutting down"}`))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		// Give the response time to flush out to the client through
		// the kernel TCP stack before we begin tearing down the
		// server. Without this delay fetch() throws TypeError because
		// the socket closes mid-read.
		time.Sleep(500 * time.Millisecond)
		hook()
	}()
}

// handleRestart triggers a graceful restart. Same pattern as shutdown:
// respond 200, flush, then invoke the hook in a goroutine.
func (s *Server) handleRestart(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	hook := s.restartHook
	s.mu.RUnlock()
	if hook == nil {
		writeJSONError(w, http.StatusNotImplemented, "restart not wired")
		return
	}
	slog.Info("restart requested via dashboard")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	_, _ = w.Write([]byte(`{"status":"restarting"}`))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		hook()
	}()
}

// handleCollectPause flips the shared collect.State to paused. The main
// loop observes this flag each tick and ends the active NT logger
// session; the uploader observes it at every MaybeUpload call and skips
// network I/O. The flag is NOT persisted — on restart, collection is
// always enabled.
func (s *Server) handleCollectPause(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	c := s.collect
	s.mu.RUnlock()
	if c == nil {
		writeJSONError(w, http.StatusNotImplemented, "collect state not wired")
		return
	}
	c.Pause()
	slog.Info("collection paused via dashboard")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"paused"}`))
}

// handleCollectResume clears the pause flag so the next main-loop tick
// resumes NT logging and upload.
func (s *Server) handleCollectResume(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	c := s.collect
	s.mu.RUnlock()
	if c == nil {
		writeJSONError(w, http.StatusNotImplemented, "collect state not wired")
		return
	}
	c.Resume()
	slog.Info("collection resumed via dashboard")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"resumed"}`))
}

// ---------- Session Handlers ----------

// sessionInfo is the JSON shape returned by GET /api/sessions.
type sessionInfo struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	Date      string `json:"date"`
	Entries   int    `json:"entries"`
	SizeBytes int64  `json:"size_bytes"`
	Status    string `json:"status"` // "pending", "uploaded", "recording"
	Active    bool   `json:"active"`
	Match     string `json:"match,omitempty"` // e.g. "Q42", "E3" — empty for practice
}

// handleSessions returns metadata for all session files in both
// pending/ and uploaded/ directories.
func (s *Server) handleSessions(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	activeIDFn := s.activeIDFn
	dataDir := s.dataDir
	team := s.cfg.Bridge.Team
	s.mu.RUnlock()

	activeID := ""
	if activeIDFn != nil {
		activeID = activeIDFn()
	}

	sessions := []sessionInfo{}
	for _, sub := range []struct {
		dir    string
		status string
	}{
		{filepath.Join(dataDir, "pending"), "pending"},
		{filepath.Join(dataDir, "uploaded"), "uploaded"},
	} {
		entries, err := os.ReadDir(sub.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			sid := wpilog.ExtractSessionID(e.Name())
			if sid == "" {
				continue
			}

			info, err := e.Info()
			if err != nil {
				continue
			}

			active := sid == activeID && activeID != ""
			st := sub.status
			if active {
				st = "recording"
			}

			fpath := filepath.Join(sub.dir, e.Name())
			entryCount := readEntryCount(fpath)
			match := readMatchID(fpath)

			sessions = append(sessions, sessionInfo{
				ID:        sid,
				Filename:  e.Name(),
				Date:      wpilog.ExtractDate(e.Name()),
				Entries:   entryCount,
				SizeBytes: info.Size(),
				Status:    st,
				Active:    active,
				Match:     match,
			})
		}
	}

	// Sort newest-first by filename (which starts with a UTC timestamp).
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Filename > sessions[j].Filename
	})

	_ = team // available for future use
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

// handleSessionWPILog converts a session JSONL file to WPILog and
// serves it as a binary download.
func (s *Server) handleSessionWPILog(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if sid == "" {
		writeJSONError(w, http.StatusBadRequest, "missing session id")
		return
	}

	s.mu.RLock()
	activeIDFn := s.activeIDFn
	dataDir := s.dataDir
	team := s.cfg.Bridge.Team
	s.mu.RUnlock()

	// Refuse to export the active session.
	if activeIDFn != nil && activeIDFn() == sid {
		writeJSONError(w, http.StatusConflict, "session is currently being recorded")
		return
	}

	path, err := findSessionFile(dataDir, sid)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}

	jsonlData, err := os.ReadFile(path)
	if err != nil {
		slog.Error("failed to read session file", "path", path, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to read session file")
		return
	}

	wpilogData, err := wpilog.Convert(jsonlData, team, sid)
	if err != nil {
		slog.Error("failed to convert to WPILog", "session", sid, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "WPILog conversion failed: "+err.Error())
		return
	}

	// Derive download filename from the JSONL filename.
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	dlName := base + ".wpilog"

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, dlName))
	w.Header().Set("Content-Length", strconv.Itoa(len(wpilogData)))
	_, _ = w.Write(wpilogData)
}

// handleSessionOpen converts a session to WPILog, saves it to
// data/wpilog/, and opens it with the OS default handler (typically
// AdvantageScope for .wpilog files).
func (s *Server) handleSessionOpen(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("id")
	if sid == "" {
		writeJSONError(w, http.StatusBadRequest, "missing session id")
		return
	}

	s.mu.RLock()
	activeIDFn := s.activeIDFn
	dataDir := s.dataDir
	team := s.cfg.Bridge.Team
	s.mu.RUnlock()

	if activeIDFn != nil && activeIDFn() == sid {
		writeJSONError(w, http.StatusConflict, "session is currently being recorded")
		return
	}

	path, err := findSessionFile(dataDir, sid)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}

	jsonlData, err := os.ReadFile(path)
	if err != nil {
		slog.Error("failed to read session file", "path", path, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to read session file")
		return
	}

	wpilogData, err := wpilog.Convert(jsonlData, team, sid)
	if err != nil {
		slog.Error("failed to convert to WPILog", "session", sid, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "WPILog conversion failed: "+err.Error())
		return
	}

	// Save to data/wpilog/ so the file persists after the browser tab closes.
	wpilogDir := filepath.Join(dataDir, "wpilog")
	if err := os.MkdirAll(wpilogDir, 0o755); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create wpilog directory")
		return
	}
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	outPath := filepath.Join(wpilogDir, base+".wpilog")
	if err := os.WriteFile(outPath, wpilogData, 0o644); err != nil {
		slog.Error("failed to write WPILog file", "path", outPath, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to write WPILog file")
		return
	}

	// Open with the OS default handler for .wpilog (AdvantageScope).
	lifecycle.OpenFile(outPath)

	slog.Info("opened session in AdvantageScope", "session", sid, "path", outPath)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"opened"}`))
}

// findSessionFile searches pending/ and uploaded/ for a JSONL file
// whose name contains the given session ID.
func findSessionFile(dataDir, sessionID string) (string, error) {
	for _, sub := range []string{"pending", "uploaded"} {
		dir := filepath.Join(dataDir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if wpilog.ExtractSessionID(e.Name()) == sessionID {
				return filepath.Join(dir, e.Name()), nil
			}
		}
	}
	return "", fmt.Errorf("session %q not found", sessionID)
}

// readEntryCount reads the last line of a JSONL file and extracts
// the entries_written field from the session_end record. Returns -1
// if the file can't be read or has no session_end.
func readEntryCount(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()

	// Read last non-empty line by scanning the whole file. For files
	// up to a few MB this is fast enough; the OS page cache will have
	// the data hot after the first request.
	var lastLine string
	scanner := bufio.NewScanner(f)
	// Increase buffer for long lines (some JSONL entries can be large).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lastLine = line
		}
	}

	if lastLine == "" {
		return -1
	}
	var entry struct {
		Type           string `json:"type"`
		EntriesWritten int    `json:"entries_written"`
	}
	if err := json.Unmarshal([]byte(lastLine), &entry); err != nil {
		return -1
	}
	if entry.Type != "session_end" {
		return -1
	}
	return entry.EntriesWritten
}

// matchTypePrefix maps the FMS MatchType integer to a display prefix.
// 0=None, 1=Practice, 2=Qualification, 3=Elimination.
var matchTypePrefix = map[int]string{
	1: "P",
	2: "Q",
	3: "E",
}

// readMatchID scans a JSONL file for /FMSInfo/MatchType and
// /FMSInfo/MatchNumber data entries and returns a display string like
// "Q42" or "E3". Returns "" if no FMS match info is found (practice
// sessions without FMS have MatchType=0 and MatchNumber=0).
func readMatchID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var matchType int
	var matchNumber int
	found := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() && found < 2 {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Quick filter before full JSON parse.
		if !strings.Contains(line, "FMSInfo/Match") {
			continue
		}
		var entry struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		switch entry.Key {
		case "/FMSInfo/MatchType":
			if n, ok := entry.Value.(float64); ok {
				matchType = int(n)
				found++
			}
		case "/FMSInfo/MatchNumber":
			if n, ok := entry.Value.(float64); ok {
				matchNumber = int(n)
				found++
			}
		}
	}

	if matchNumber <= 0 {
		return ""
	}
	prefix, ok := matchTypePrefix[matchType]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s%d", prefix, matchNumber)
}

// ---------- Validation ----------

// validateConfigPost checks every known field present in data against the
// allowed range / whitelist. Unknown keys are ignored (forward-compat).
// Returns a human-readable error on the first failure.
func validateConfigPost(data map[string]any) error {
	if v, ok := data["data_dir"]; ok {
		s := fmt.Sprintf("%v", v)
		// Reject any path that contains a ".." traversal component.
		if strings.Contains(filepath.ToSlash(s), "..") {
			return fmt.Errorf("data_dir must not contain '..'")
		}
	}
	if v, ok := data["dashboard_port"]; ok {
		n, err := strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("dashboard_port must be between 1 and 65535")
		}
	}
	if v, ok := data["obs_port"]; ok {
		n, err := strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("obs_port must be between 1 and 65535")
		}
	}
	if v, ok := data["retention_days"]; ok {
		n, err := strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil || n < 1 || n > 365 {
			return fmt.Errorf("retention_days must be between 1 and 365")
		}
	}
	if v, ok := data["log_level"]; ok {
		s := fmt.Sprintf("%v", v)
		switch s {
		case "DEBUG", "INFO", "WARNING", "ERROR":
		default:
			return fmt.Errorf("log_level must be one of DEBUG, INFO, WARNING, ERROR")
		}
	}
	if v, ok := data["record_trigger"]; ok {
		s := fmt.Sprintf("%v", v)
		switch s {
		case "fms", "auto", "any":
		default:
			return fmt.Errorf("record_trigger must be one of fms, auto, any")
		}
	}
	if v, ok := data["collect_trigger"]; ok {
		s := fmt.Sprintf("%v", v)
		switch s {
		case "fms", "auto", "any":
		default:
			return fmt.Errorf("collect_trigger must be one of fms, auto, any")
		}
	}
	if v, ok := data["team"]; ok {
		n, err := strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil || n < 1 || n > 9999 {
			return fmt.Errorf("team must be between 1 and 9999")
		}
	}
	if v, ok := data["limelight_poll_interval"]; ok {
		f, err := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
		if err != nil || f < 0.1 || f > 60 {
			return fmt.Errorf("limelight_poll_interval must be between 0.1 and 60 seconds")
		}
	}
	if v, ok := data["limelight_timeout_ms"]; ok {
		n, err := strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil || n < 10 || n > 10000 {
			return fmt.Errorf("limelight_timeout_ms must be between 10 and 10000")
		}
	}
	// Upload target sanity: enabled=true with an empty URL is a
	// silent-no-op trap. Reject it at save time so the operator sees the
	// problem instead of wondering why files sit in pending/ forever.
	// Only runs when both keys are present in the same POST — an
	// isolated toggle of ravenbrain_enabled trusts the stored URL.
	if err := validateTargetEnabledURL(data, "ravenbrain"); err != nil {
		return err
	}
	if err := validateTargetEnabledURL(data, "ravenscope"); err != nil {
		return err
	}

	if v, ok := data["limelight_last_octets"]; ok {
		s := strings.TrimSpace(fmt.Sprintf("%v", v))
		if s != "" {
			for _, p := range strings.Split(s, ",") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				n, err := strconv.Atoi(p)
				if err != nil || n < 1 || n > 254 {
					return fmt.Errorf("limelight_last_octets entries must be integers between 1 and 254 (got %q)", p)
				}
			}
		}
	}
	return nil
}

// validateTargetEnabledURL enforces "enabled=true requires URL". Only
// flags when both keys are present in the same POST; when only the
// enabled key is sent, the operator is toggling against the stored URL
// and we accept it (the stored URL may be non-empty).
func validateTargetEnabledURL(data map[string]any, prefix string) error {
	enabledRaw, hasEnabled := data[prefix+"_enabled"]
	urlRaw, hasURL := data[prefix+"_url"]
	if !hasEnabled || !hasURL {
		return nil
	}
	enabled := toBool(fmt.Sprintf("%v", enabledRaw))
	url := strings.TrimSpace(fmt.Sprintf("%v", urlRaw))
	if enabled && url == "" {
		return fmt.Errorf("%s_url must be non-empty when %s_enabled is true", prefix, prefix)
	}
	return nil
}

// joinInts formats a slice of ints as a comma-separated string for the
// config GET response. Used for last_octets rendering in the dashboard.
func joinInts(xs []int, sep string) string {
	parts := make([]string, len(xs))
	for i, n := range xs {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, sep)
}

// ---------- Helpers ----------

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func toInt(s string, fallback int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return v
}

func toFloat(s string, fallback float64) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fallback
	}
	return v
}

func toBool(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "1" || s == "yes"
}
