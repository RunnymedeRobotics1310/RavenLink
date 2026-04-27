// Package config handles YAML configuration loading, CLI flag overrides,
// default values, and config persistence for RavenLink.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for RavenLink.
type Config struct {
	Bridge     BridgeConfig     `yaml:"bridge"`
	Telemetry  TelemetryConfig  `yaml:"telemetry"`
	OBS        OBSConfig        `yaml:"obs"`
	RavenBrain RavenBrainConfig `yaml:"ravenbrain"`
	RavenScope RavenScopeConfig `yaml:"ravenscope"`
	Dashboard  DashboardConfig  `yaml:"dashboard"`
	Limelight  LimelightConfig  `yaml:"limelight"`

	// Minimized is a runtime-only flag (not persisted to YAML) set by
	// --minimized on the command line. When true, auto-launched
	// RavenLink should skip opening the browser on startup.
	Minimized bool `yaml:"-"`
}

// BridgeConfig holds settings for the NT bridge core.
type BridgeConfig struct {
	Team              int     `yaml:"team"`
	StopDelay         float64 `yaml:"stop_delay"`
	PollInterval      float64 `yaml:"poll_interval"`
	LogLevel          string  `yaml:"log_level"`
	RecordTrigger     string  `yaml:"record_trigger"`
	CollectTrigger    string  `yaml:"collect_trigger"`
	LaunchOnLogin     bool    `yaml:"launch_on_login"`
	AutoTeleopGap     float64 `yaml:"auto_teleop_gap"`
	NTDisconnectGrace float64 `yaml:"nt_disconnect_grace"`

	// NTHost, when non-empty, overrides the team-derived 10.TE.AM.2
	// robot address. Intended for connecting to a local WPILib
	// simulator (nt_host: localhost) or any non-RoboRIO NT4 server.
	// When set, it also becomes the robot_ip field logged in session
	// metadata so uploads reflect where the data actually came from.
	NTHost string `yaml:"nt_host"`
}

// TelemetryConfig holds settings for NT telemetry logging.
type TelemetryConfig struct {
	NTPaths       []string `yaml:"nt_paths"`
	DataDir       string   `yaml:"data_dir"`
	RetentionDays int      `yaml:"retention_days"`
}

// OBSConfig holds OBS Studio WebSocket settings. When Enabled is false,
// RavenLink does not connect to OBS at all and the state machine's
// StartRecord/StopRecord actions are no-ops. Use this on Driver Station
// laptops that aren't running OBS so the dashboard doesn't perpetually
// show "OBS: Disconnected".
type OBSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Password string `yaml:"password"`
}

// RavenBrainConfig holds settings for the RavenBrain upload target.
// Auth is legacy username/password via POST /login; the uploader caches
// the returned JWT and renews 5 minutes before expiry.
//
// A target is considered active when Enabled is true AND URL is non-empty.
// Enabled=true with an empty URL is a save-time validation error.
type RavenBrainConfig struct {
	Enabled        bool    `yaml:"enabled"`
	URL            string  `yaml:"url"`
	Username       string  `yaml:"username"`
	Password       string  `yaml:"password"`
	BatchSize      int     `yaml:"batch_size"`
	UploadInterval float64 `yaml:"upload_interval"`
}

// RavenScopeConfig holds settings for the RavenScope upload target.
// Auth is a bearer API key sent verbatim on every request — no /login
// round-trip, no client-side renewal.
//
// Same activation rule as RavenBrainConfig: Enabled AND URL non-empty.
type RavenScopeConfig struct {
	Enabled        bool    `yaml:"enabled"`
	URL            string  `yaml:"url"`
	APIKey         string  `yaml:"api_key"`
	BatchSize      int     `yaml:"batch_size"`
	UploadInterval float64 `yaml:"upload_interval"`
}

// DashboardConfig holds settings for the local status dashboard.
type DashboardConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}

// LimelightConfig holds settings for the Limelight uptime monitor.
// Each configured last-octet is polled at 10.TE.AM.<octet>:5807/results
// every PollInterval seconds with a TimeoutMS per-request deadline.
type LimelightConfig struct {
	Enabled      bool    `yaml:"enabled"`
	LastOctets   []int   `yaml:"last_octets"`
	PollInterval float64 `yaml:"poll_interval"`
	TimeoutMS    int     `yaml:"timeout_ms"`
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Bridge: BridgeConfig{
			Team:              1310,
			StopDelay:         10,
			PollInterval:      0.05,
			LogLevel:          "INFO",
			RecordTrigger:     "fms",
			CollectTrigger:    "fms",
			LaunchOnLogin:     true,
			AutoTeleopGap:     5,
			NTDisconnectGrace: 15,
		},
		Telemetry: TelemetryConfig{
			NTPaths:       []string{"/FMSInfo/", "/SmartDashboard/", "/Shuffleboard/"},
			DataDir:       "./data",
			RetentionDays: 30,
		},
		OBS: OBSConfig{
			Enabled:  false,
			Host:     "localhost",
			Port:     4455,
			Password: "",
		},
		RavenBrain: RavenBrainConfig{
			Enabled:        false,
			URL:            "",
			Username:       "telemetry-agent",
			Password:       "",
			BatchSize:      50,
			UploadInterval: 10,
		},
		RavenScope: RavenScopeConfig{
			Enabled:        true,
			URL:            "https://ravenscope.team1310.ca",
			APIKey:         "",
			BatchSize:      50,
			UploadInterval: 10,
		},
		Dashboard: DashboardConfig{
			Enabled: true,
			Port:    8080,
		},
		Limelight: LimelightConfig{
			Enabled:      true,
			LastOctets:   []int{11},
			PollInterval: 2.0,
			TimeoutMS:    1000,
		},
	}
}

// LoadConfig reads a YAML config file at path and returns a Config.
// Fields absent from the YAML keep the values from DefaultConfig().
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	return cfg, nil
}

// SaveConfig writes the config to the given YAML file path atomically
// with mode 0600. If the file already exists, a .bak copy of the previous
// version is kept alongside the new file. The write sequence is:
//
//  1. marshal YAML
//  2. write to <path>.tmp (0600) in the same directory
//  3. fsync the temp file
//  4. copy existing <path> to <path>.bak (if present)
//  5. rename <path>.tmp -> <path>
//  6. chmod <path> to 0600 (in case umask widened perms)
func (c *Config) SaveConfig(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config to YAML: %w", err)
	}

	dir := filepath.Dir(path)
	tmpPath := path + ".tmp"

	// Create temp file with 0600 perms in the same directory so the
	// subsequent rename is atomic (same filesystem).
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating temp config file: %w", err)
	}
	// Ensure we clean up the temp file on failure.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing temp config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsync temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing temp config file: %w", err)
	}

	// Keep a backup of the existing file, if any.
	if prev, err := os.ReadFile(path); err == nil {
		bakPath := path + ".bak"
		if err := os.WriteFile(bakPath, prev, 0o600); err != nil {
			// Backup failure is non-fatal but we log via returned error chain.
			// Still proceed with the atomic rename so the primary write lands.
			_ = err
		}
	} else if !os.IsNotExist(err) {
		// If we can't read the existing file for a reason other than
		// "doesn't exist", that's surprising but not fatal.
		_ = err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("renaming temp config file: %w", err)
	}

	// Enforce 0600 even if umask or prior perms were looser.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod config file to 0600: %w", err)
	}

	// Best-effort: ensure directory entry is flushed.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

// RobotIP returns the NT4 server host RavenLink should connect to. If
// Bridge.NTHost is set, it wins. Otherwise the address is derived from
// the team number using the FRC convention: 10.TE.AM.2 (e.g., team
// 1310 -> 10.13.10.2).
func (c *Config) RobotIP() string {
	if c.Bridge.NTHost != "" {
		return c.Bridge.NTHost
	}
	te := c.Bridge.Team / 100
	am := c.Bridge.Team % 100
	return fmt.Sprintf("10.%d.%d.2", te, am)
}

// ParseFlags applies CLI flag overrides to an existing Config.
// Flags that are not explicitly set on the command line leave the
// corresponding config value unchanged.
func ParseFlags(cfg *Config) {
	fs := flag.NewFlagSet("ravenlink", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "RavenLink — FRC robot data bridge: OBS recording, NT telemetry, cloud upload\n\n")
		fmt.Fprintf(fs.Output(), "Usage:\n")
		fs.PrintDefaults()
	}

	// Bridge flags
	team := fs.Int("team", cfg.Bridge.Team, "Team number (used to derive robot IP 10.TE.AM.2)")
	obsEnabled := fs.Bool("obs-enabled", cfg.OBS.Enabled, "Enable the OBS recording integration")
	noOBS := fs.Bool("no-obs", false, "Disable the OBS recording integration")
	obsHost := fs.String("obs-host", cfg.OBS.Host, "OBS WebSocket host")
	obsPort := fs.Int("obs-port", cfg.OBS.Port, "OBS WebSocket port")
	obsPassword := fs.String("obs-password", cfg.OBS.Password, "OBS WebSocket password")
	stopDelay := fs.Float64("stop-delay", cfg.Bridge.StopDelay, "Seconds after match end before stopping recording")
	pollInterval := fs.Float64("poll-interval", cfg.Bridge.PollInterval, "Poll interval in seconds")
	logLevel := fs.String("log-level", cfg.Bridge.LogLevel, "Log level (DEBUG, INFO, WARNING, ERROR)")
	recordTrigger := fs.String("record-trigger", cfg.Bridge.RecordTrigger, "When to record: fms, auto, any")
	collectTrigger := fs.String("collect-trigger", cfg.Bridge.CollectTrigger, "When to collect NT data + upload: fms, auto, any")
	noLaunchOnLogin := fs.Bool("no-launch-on-login", false, "Disable launch-on-login registration")
	autoTeleopGap := fs.Float64("auto-teleop-gap", cfg.Bridge.AutoTeleopGap, "Max seconds of disabled between auto and teleop before stopping")
	ntDisconnectGrace := fs.Float64("nt-disconnect-grace", cfg.Bridge.NTDisconnectGrace, "Grace period (seconds) before treating NT disconnect as match over")
	ntHost := fs.String("nt-host", cfg.Bridge.NTHost, "NT4 server host override (empty = derive 10.TE.AM.2 from team). Use 'localhost' for WPILib sim.")

	// Telemetry flags
	dataDir := fs.String("data-dir", cfg.Telemetry.DataDir, "Local data directory for JSONL files")
	retentionDays := fs.Int("retention-days", cfg.Telemetry.RetentionDays, "Days to retain local telemetry files")

	// RavenBrain flags
	ravenbrainURL := fs.String("ravenbrain-url", cfg.RavenBrain.URL, "RavenBrain server URL (empty or --no-ravenbrain to disable)")
	ravenbrainUsername := fs.String("ravenbrain-username", cfg.RavenBrain.Username, "RavenBrain service account username")
	ravenbrainPassword := fs.String("ravenbrain-password", cfg.RavenBrain.Password, "RavenBrain service account password")
	ravenbrainBatchSize := fs.Int("ravenbrain-batch-size", cfg.RavenBrain.BatchSize, "RavenBrain upload batch size")
	ravenbrainUploadInterval := fs.Float64("ravenbrain-upload-interval", cfg.RavenBrain.UploadInterval, "RavenBrain upload interval in seconds")
	noRavenBrain := fs.Bool("no-ravenbrain", false, "Disable the RavenBrain upload target")

	// RavenScope flags
	ravenscopeURL := fs.String("ravenscope-url", cfg.RavenScope.URL, "RavenScope server URL")
	ravenscopeAPIKey := fs.String("ravenscope-api-key", cfg.RavenScope.APIKey, "RavenScope bearer API key")
	ravenscopeBatchSize := fs.Int("ravenscope-batch-size", cfg.RavenScope.BatchSize, "RavenScope upload batch size")
	ravenscopeUploadInterval := fs.Float64("ravenscope-upload-interval", cfg.RavenScope.UploadInterval, "RavenScope upload interval in seconds")
	ravenscopeEnabled := fs.Bool("ravenscope-enabled", cfg.RavenScope.Enabled, "Enable the RavenScope upload target")

	// Dashboard flags
	dashboardPort := fs.Int("dashboard-port", cfg.Dashboard.Port, "Dashboard HTTP port")
	noDashboard := fs.Bool("no-dashboard", false, "Disable the status dashboard")

	// Lifecycle flags
	// --minimized is passed by the autostart registration (LaunchAgent
	// on macOS, Run key on Windows) so the auto-launched app knows to
	// skip the browser auto-open. Without this flag registered, flag
	// parsing would fail with "flag provided but not defined" and
	// autostart would be broken.
	minimized := fs.Bool("minimized", false, "Start without opening the browser (used by autostart)")

	// Config file flag (informational — caller is responsible for loading)
	_ = fs.String("config", "config.yaml", "Path to YAML config file")

	_ = fs.Parse(os.Args[1:])

	// Apply overrides.
	cfg.Bridge.Team = *team
	cfg.OBS.Enabled = *obsEnabled
	if *noOBS {
		cfg.OBS.Enabled = false
	}
	cfg.OBS.Host = *obsHost
	cfg.OBS.Port = *obsPort
	cfg.OBS.Password = *obsPassword
	cfg.Bridge.StopDelay = *stopDelay
	cfg.Bridge.PollInterval = *pollInterval
	cfg.Bridge.LogLevel = *logLevel
	cfg.Bridge.RecordTrigger = *recordTrigger
	cfg.Bridge.CollectTrigger = *collectTrigger
	cfg.Bridge.AutoTeleopGap = *autoTeleopGap
	cfg.Bridge.NTDisconnectGrace = *ntDisconnectGrace
	cfg.Bridge.NTHost = *ntHost

	if *noLaunchOnLogin {
		cfg.Bridge.LaunchOnLogin = false
	}

	cfg.Telemetry.DataDir = *dataDir
	cfg.Telemetry.RetentionDays = *retentionDays

	cfg.RavenBrain.URL = *ravenbrainURL
	cfg.RavenBrain.Username = *ravenbrainUsername
	cfg.RavenBrain.Password = *ravenbrainPassword
	cfg.RavenBrain.BatchSize = *ravenbrainBatchSize
	cfg.RavenBrain.UploadInterval = *ravenbrainUploadInterval
	if *noRavenBrain {
		cfg.RavenBrain.Enabled = false
	}

	cfg.RavenScope.URL = *ravenscopeURL
	cfg.RavenScope.APIKey = *ravenscopeAPIKey
	cfg.RavenScope.BatchSize = *ravenscopeBatchSize
	cfg.RavenScope.UploadInterval = *ravenscopeUploadInterval
	cfg.RavenScope.Enabled = *ravenscopeEnabled

	cfg.Dashboard.Port = *dashboardPort
	if *noDashboard {
		cfg.Dashboard.Enabled = false
	}

	cfg.Minimized = *minimized
}
