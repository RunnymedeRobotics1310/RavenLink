// Package config handles YAML configuration loading, CLI flag overrides,
// default values, and config persistence for RavenLink.
package config

import (
	"flag"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for RavenLink.
type Config struct {
	Bridge     BridgeConfig     `yaml:"bridge"`
	Telemetry  TelemetryConfig  `yaml:"telemetry"`
	RavenBrain RavenBrainConfig `yaml:"ravenbrain"`
	Dashboard  DashboardConfig  `yaml:"dashboard"`
}

// BridgeConfig holds settings for the OBS/NT bridge core.
type BridgeConfig struct {
	Team              int     `yaml:"team"`
	OBSHost           string  `yaml:"obs_host"`
	OBSPort           int     `yaml:"obs_port"`
	OBSPassword       string  `yaml:"obs_password"`
	StopDelay         float64 `yaml:"stop_delay"`
	PollInterval      float64 `yaml:"poll_interval"`
	LogLevel          string  `yaml:"log_level"`
	RecordTrigger     string  `yaml:"record_trigger"`
	LaunchOnLogin     bool    `yaml:"launch_on_login"`
	AutoTeleopGap     float64 `yaml:"auto_teleop_gap"`
	NTDisconnectGrace float64 `yaml:"nt_disconnect_grace"`
}

// TelemetryConfig holds settings for NT telemetry logging.
type TelemetryConfig struct {
	NTPaths       []string `yaml:"nt_paths"`
	DataDir       string   `yaml:"data_dir"`
	RetentionDays int      `yaml:"retention_days"`
}

// RavenBrainConfig holds settings for RavenBrain cloud upload.
type RavenBrainConfig struct {
	URL            string  `yaml:"url"`
	Username       string  `yaml:"username"`
	Password       string  `yaml:"password"`
	BatchSize      int     `yaml:"batch_size"`
	UploadInterval float64 `yaml:"upload_interval"`
}

// DashboardConfig holds settings for the local status dashboard.
type DashboardConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Bridge: BridgeConfig{
			Team:              1310,
			OBSHost:           "localhost",
			OBSPort:           4455,
			OBSPassword:       "",
			StopDelay:         10,
			PollInterval:      0.05,
			LogLevel:          "INFO",
			RecordTrigger:     "fms",
			LaunchOnLogin:     true,
			AutoTeleopGap:     5,
			NTDisconnectGrace: 15,
		},
		Telemetry: TelemetryConfig{
			NTPaths:       []string{"/SmartDashboard/", "/Shuffleboard/"},
			DataDir:       "./data",
			RetentionDays: 30,
		},
		RavenBrain: RavenBrainConfig{
			URL:            "",
			Username:       "telemetry-agent",
			Password:       "",
			BatchSize:      500,
			UploadInterval: 10,
		},
		Dashboard: DashboardConfig{
			Enabled: true,
			Port:    8080,
		},
	}
}

// LoadConfig reads a YAML config file at path and returns a Config.
// Missing fields are filled from DefaultConfig.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config YAML: %w", err)
	}

	return cfg, nil
}

// SaveConfig writes the config to the given YAML file path.
func (c *Config) SaveConfig(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config to YAML: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}

// RobotIP derives the robot IP address from the team number using the
// FRC convention: 10.TE.AM.2 (e.g., team 1310 -> 10.13.10.2).
func (c *Config) RobotIP() string {
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
		fmt.Fprintf(fs.Output(), "RavenLink — FRC robot data bridge: OBS recording, NT telemetry, RavenBrain upload\n\n")
		fmt.Fprintf(fs.Output(), "Usage:\n")
		fs.PrintDefaults()
	}

	// Bridge flags
	team := fs.Int("team", cfg.Bridge.Team, "Team number (used to derive robot IP 10.TE.AM.2)")
	obsHost := fs.String("obs-host", cfg.Bridge.OBSHost, "OBS WebSocket host")
	obsPort := fs.Int("obs-port", cfg.Bridge.OBSPort, "OBS WebSocket port")
	obsPassword := fs.String("obs-password", cfg.Bridge.OBSPassword, "OBS WebSocket password")
	stopDelay := fs.Float64("stop-delay", cfg.Bridge.StopDelay, "Seconds after match end before stopping recording")
	pollInterval := fs.Float64("poll-interval", cfg.Bridge.PollInterval, "Poll interval in seconds")
	logLevel := fs.String("log-level", cfg.Bridge.LogLevel, "Log level (DEBUG, INFO, WARNING, ERROR)")
	recordTrigger := fs.String("record-trigger", cfg.Bridge.RecordTrigger, "When to record: fms, auto, any")
	noLaunchOnLogin := fs.Bool("no-launch-on-login", false, "Disable launch-on-login registration")
	autoTeleopGap := fs.Float64("auto-teleop-gap", cfg.Bridge.AutoTeleopGap, "Max seconds of disabled between auto and teleop before stopping")
	ntDisconnectGrace := fs.Float64("nt-disconnect-grace", cfg.Bridge.NTDisconnectGrace, "Grace period (seconds) before treating NT disconnect as match over")

	// Telemetry flags
	dataDir := fs.String("data-dir", cfg.Telemetry.DataDir, "Local data directory for JSONL files")
	retentionDays := fs.Int("retention-days", cfg.Telemetry.RetentionDays, "Days to retain local telemetry files")

	// RavenBrain flags
	ravenbrainURL := fs.String("ravenbrain-url", cfg.RavenBrain.URL, "RavenBrain server URL (empty = local-only mode)")
	ravenbrainUsername := fs.String("ravenbrain-username", cfg.RavenBrain.Username, "RavenBrain service account username")
	ravenbrainPassword := fs.String("ravenbrain-password", cfg.RavenBrain.Password, "RavenBrain service account password")
	batchSize := fs.Int("ravenbrain-batch-size", cfg.RavenBrain.BatchSize, "RavenBrain upload batch size")
	uploadInterval := fs.Float64("ravenbrain-upload-interval", cfg.RavenBrain.UploadInterval, "RavenBrain upload interval in seconds")

	// Dashboard flags
	dashboardPort := fs.Int("dashboard-port", cfg.Dashboard.Port, "Dashboard HTTP port")
	noDashboard := fs.Bool("no-dashboard", false, "Disable the status dashboard")

	// Config file flag (informational — caller is responsible for loading)
	_ = fs.String("config", "config.yaml", "Path to YAML config file")

	_ = fs.Parse(os.Args[1:])

	// Apply overrides — every flag is applied since defaults already come
	// from the loaded config.
	cfg.Bridge.Team = *team
	cfg.Bridge.OBSHost = *obsHost
	cfg.Bridge.OBSPort = *obsPort
	cfg.Bridge.OBSPassword = *obsPassword
	cfg.Bridge.StopDelay = *stopDelay
	cfg.Bridge.PollInterval = *pollInterval
	cfg.Bridge.LogLevel = *logLevel
	cfg.Bridge.RecordTrigger = *recordTrigger
	cfg.Bridge.AutoTeleopGap = *autoTeleopGap
	cfg.Bridge.NTDisconnectGrace = *ntDisconnectGrace

	if *noLaunchOnLogin {
		cfg.Bridge.LaunchOnLogin = false
	}

	cfg.Telemetry.DataDir = *dataDir
	cfg.Telemetry.RetentionDays = *retentionDays

	cfg.RavenBrain.URL = *ravenbrainURL
	cfg.RavenBrain.Username = *ravenbrainUsername
	cfg.RavenBrain.Password = *ravenbrainPassword
	cfg.RavenBrain.BatchSize = *batchSize
	cfg.RavenBrain.UploadInterval = *uploadInterval

	cfg.Dashboard.Port = *dashboardPort
	if *noDashboard {
		cfg.Dashboard.Enabled = false
	}
}
