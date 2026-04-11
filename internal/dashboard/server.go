// Package dashboard provides an embedded web dashboard for monitoring
// RavenLink status and editing configuration via a browser.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/assets"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/config"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
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
	"nt_paths",
	"data_dir",
	"retention_days",
	"ravenbrain_url",
	"ravenbrain_username",
	"ravenbrain_password",
	"ravenbrain_batch_size",
	"ravenbrain_upload_interval",
	"dashboard_enabled",
	"dashboard_port",
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
}

// New creates a new dashboard server.
// cfgPath is the path to the YAML config file for save/reload.
// reloadHook is called (if non-nil) after a config reload via the API.
func New(cfg *config.Config, cfgPath string, st *status.Status, reloadHook func()) *Server {
	return &Server{
		status:     st,
		cfg:        cfg,
		cfgPath:    cfgPath,
		reloadHook: reloadHook,
	}
}

// SetLifecycleHooks wires callbacks for the Shutdown and Restart endpoints.
// If either is nil, the corresponding endpoint returns 501 Not Implemented.
func (s *Server) SetLifecycleHooks(shutdown, restart func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownHook = shutdown
	s.restartHook = restart
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
		"launch_on_login":            cfg.Bridge.LaunchOnLogin,
		"nt_paths":                   strings.Join(cfg.Telemetry.NTPaths, ", "),
		"data_dir":                   cfg.Telemetry.DataDir,
		"retention_days":             cfg.Telemetry.RetentionDays,
		"ravenbrain_url":             cfg.RavenBrain.URL,
		"ravenbrain_username":        cfg.RavenBrain.Username,
		"ravenbrain_password":        rbPwd,
		"ravenbrain_batch_size":      cfg.RavenBrain.BatchSize,
		"ravenbrain_upload_interval": cfg.RavenBrain.UploadInterval,
		"dashboard_enabled":          cfg.Dashboard.Enabled,
		"dashboard_port":             cfg.Dashboard.Port,
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
		case "dashboard_enabled":
			cfg.Dashboard.Enabled = toBool(val)
		case "dashboard_port":
			cfg.Dashboard.Port = toInt(val, cfg.Dashboard.Port)
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
	if v, ok := data["team"]; ok {
		n, err := strconv.Atoi(fmt.Sprintf("%v", v))
		if err != nil || n < 1 || n > 9999 {
			return fmt.Errorf("team must be between 1 and 9999")
		}
	}
	return nil
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
