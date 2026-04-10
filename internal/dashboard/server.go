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
	"strconv"
	"strings"
	"sync"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/config"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
)

//go:embed static/*
var staticFS embed.FS

// Server is the embedded HTTP dashboard.
type Server struct {
	mu         sync.RWMutex
	status     *status.Status
	cfg        *config.Config
	cfgPath    string
	reloadHook func() // optional callback after config reload
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

// UpdateStatus replaces the status pointer the dashboard serves.
func (s *Server) UpdateStatus(st *status.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = st
}

// Start begins serving HTTP on the given port. It blocks until ctx is
// cancelled, then shuts down gracefully.
func (s *Server) Start(ctx context.Context, port int) {
	mux := http.NewServeMux()

	// Serve the embedded static files. The embed root is "static/*",
	// so we strip the prefix to serve index.html at "/".
	staticContent, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServer(http.FS(staticContent)))

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleConfigGet)
	mux.HandleFunc("POST /api/config", s.handleConfigPost)
	mux.HandleFunc("POST /api/config/reload", s.handleConfigReload)

	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down dashboard server")
		_ = srv.Close()
	}()

	slog.Info("dashboard started", "addr", fmt.Sprintf("http://localhost:%d", port))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("dashboard server error", "err", err)
	}
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

func (s *Server) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	flat := map[string]any{
		"team":                      cfg.Bridge.Team,
		"obs_host":                  cfg.Bridge.OBSHost,
		"obs_port":                  cfg.Bridge.OBSPort,
		"obs_password":              cfg.Bridge.OBSPassword,
		"stop_delay":                cfg.Bridge.StopDelay,
		"poll_interval":             cfg.Bridge.PollInterval,
		"log_level":                 cfg.Bridge.LogLevel,
		"auto_teleop_gap":           cfg.Bridge.AutoTeleopGap,
		"nt_disconnect_grace":       cfg.Bridge.NTDisconnectGrace,
		"record_trigger":            cfg.Bridge.RecordTrigger,
		"launch_on_login":           cfg.Bridge.LaunchOnLogin,
		"nt_paths":                  strings.Join(cfg.Telemetry.NTPaths, ", "),
		"data_dir":                  cfg.Telemetry.DataDir,
		"retention_days":            cfg.Telemetry.RetentionDays,
		"ravenbrain_url":            cfg.RavenBrain.URL,
		"ravenbrain_username":       cfg.RavenBrain.Username,
		"ravenbrain_password":       cfg.RavenBrain.Password,
		"ravenbrain_batch_size":     cfg.RavenBrain.BatchSize,
		"ravenbrain_upload_interval": cfg.RavenBrain.UploadInterval,
		"dashboard_enabled":         cfg.Dashboard.Enabled,
		"dashboard_port":            cfg.Dashboard.Port,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(flat)
}

func (s *Server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
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
			cfg.Bridge.OBSPassword = val
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
			cfg.RavenBrain.Password = val
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
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
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

// ---------- Helpers ----------

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
