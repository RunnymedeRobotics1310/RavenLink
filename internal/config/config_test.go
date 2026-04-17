package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// writeFile is a tiny helper that writes content to a path and fails the
// test on error.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// TestRoundTrip — SaveConfig followed by LoadConfig returns an equal Config.
// ---------------------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	orig := DefaultConfig()
	orig.Bridge.Team = 4646
	orig.Bridge.OBSHost = "testhost"
	orig.Bridge.OBSPort = 9999
	orig.Bridge.OBSPassword = "secret"
	orig.Bridge.StopDelay = 7.5
	orig.Bridge.LogLevel = "DEBUG"
	orig.Telemetry.DataDir = "/tmp/nope"
	orig.Telemetry.RetentionDays = 7
	orig.RavenBrain.URL = "https://brain.example"
	orig.RavenBrain.Username = "tester"
	orig.RavenBrain.Password = "pw"
	orig.Dashboard.Enabled = false
	orig.Dashboard.Port = 9090

	if err := orig.SaveConfig(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !reflect.DeepEqual(orig, loaded) {
		t.Errorf("round trip mismatch:\n got:  %+v\n want: %+v", loaded, orig)
	}
}

// ---------------------------------------------------------------------------
// TestDefaultMerge — loading a partial YAML leaves defaults for missing
// fields.
// ---------------------------------------------------------------------------

func TestDefaultMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.yaml")

	// Only set a couple of bridge fields. Everything else should come from
	// DefaultConfig.
	writeFile(t, path, `
bridge:
  team: 254
  obs_host: roborio
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Bridge.Team != 254 {
		t.Errorf("Team: got %d, want 254", cfg.Bridge.Team)
	}
	if cfg.Bridge.OBSHost != "roborio" {
		t.Errorf("OBSHost: got %q, want %q", cfg.Bridge.OBSHost, "roborio")
	}
	// Default OBSPort is 4455.
	if cfg.Bridge.OBSPort != 4455 {
		t.Errorf("OBSPort: got %d, want default 4455", cfg.Bridge.OBSPort)
	}
	// Default log level.
	if cfg.Bridge.LogLevel != "INFO" {
		t.Errorf("LogLevel: got %q, want default %q", cfg.Bridge.LogLevel, "INFO")
	}
	// Default telemetry NT paths.
	if len(cfg.Telemetry.NTPaths) == 0 {
		t.Error("Telemetry.NTPaths: got empty, want defaults")
	}
	// Default RavenBrain batch size.
	if cfg.RavenBrain.BatchSize != 50 {
		t.Errorf("BatchSize: got %d, want default 50", cfg.RavenBrain.BatchSize)
	}
	if !cfg.Dashboard.Enabled {
		t.Error("Dashboard.Enabled: got false, want default true")
	}
	// Limelight section was omitted; defaults should be applied.
	if !cfg.Limelight.Enabled {
		t.Error("Limelight.Enabled: got false, want default true")
	}
	if len(cfg.Limelight.LastOctets) != 2 || cfg.Limelight.LastOctets[0] != 11 || cfg.Limelight.LastOctets[1] != 12 {
		t.Errorf("Limelight.LastOctets: got %v, want default [11 12]", cfg.Limelight.LastOctets)
	}
	if cfg.Limelight.PollInterval != 1.0 {
		t.Errorf("Limelight.PollInterval: got %v, want default 1.0", cfg.Limelight.PollInterval)
	}
	if cfg.Limelight.TimeoutMS != 200 {
		t.Errorf("Limelight.TimeoutMS: got %d, want default 200", cfg.Limelight.TimeoutMS)
	}
}

// ---------------------------------------------------------------------------
// TestLimelightRoundTrip — a YAML file with an explicit limelight section
// should round-trip all four fields faithfully.
// ---------------------------------------------------------------------------

func TestLimelightRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ll.yaml")
	writeFile(t, path, `
bridge:
  team: 1310
limelight:
  enabled: false
  last_octets: [11, 12, 13]
  poll_interval: 2.5
  timeout_ms: 500
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Limelight.Enabled {
		t.Error("Enabled: want false")
	}
	if got := cfg.Limelight.LastOctets; len(got) != 3 || got[2] != 13 {
		t.Errorf("LastOctets: got %v, want [11 12 13]", got)
	}
	if cfg.Limelight.PollInterval != 2.5 {
		t.Errorf("PollInterval: got %v, want 2.5", cfg.Limelight.PollInterval)
	}
	if cfg.Limelight.TimeoutMS != 500 {
		t.Errorf("TimeoutMS: got %d, want 500", cfg.Limelight.TimeoutMS)
	}
}

// TestLimelightEmptyOctets — an explicit empty last_octets list is
// honored as "no cameras to poll", not treated as a missing-section
// signal. This distinguishes "I disabled via empty list" from "the
// section wasn't in my config file at all".
func TestLimelightEmptyOctets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ll-empty.yaml")
	writeFile(t, path, `
bridge:
  team: 1310
limelight:
  enabled: true
  last_octets: []
  poll_interval: 1.0
  timeout_ms: 200
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Limelight.LastOctets == nil {
		t.Error("LastOctets: nil (should be empty but non-nil slice after explicit [])")
	}
	if len(cfg.Limelight.LastOctets) != 0 {
		t.Errorf("LastOctets: got %v, want empty", cfg.Limelight.LastOctets)
	}
	// PollInterval and TimeoutMS were explicitly set, so backfill must
	// NOT override them back to defaults.
	if cfg.Limelight.PollInterval != 1.0 || cfg.Limelight.TimeoutMS != 200 {
		t.Errorf("defaults should be preserved for explicit set: got pi=%v tmo=%d",
			cfg.Limelight.PollInterval, cfg.Limelight.TimeoutMS)
	}
}

// ---------------------------------------------------------------------------
// TestSavePermissions — the saved config file has mode 0600.
// ---------------------------------------------------------------------------

func TestSavePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not meaningful on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	if err := cfg.SaveConfig(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode: got %o, want 0600", mode)
	}
}

// ---------------------------------------------------------------------------
// TestAtomicWriteBackup — saving over an existing file creates a .bak.
// ---------------------------------------------------------------------------

func TestAtomicWriteBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// First save.
	first := DefaultConfig()
	first.Bridge.Team = 1111
	if err := first.SaveConfig(path); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// After the first save there should be no .bak yet (nothing to back up).
	bakPath := path + ".bak"
	if _, err := os.Stat(bakPath); !os.IsNotExist(err) {
		t.Errorf("unexpected .bak after first save: err=%v", err)
	}

	// Second save — the previous file should now be backed up.
	second := DefaultConfig()
	second.Bridge.Team = 2222
	if err := second.SaveConfig(path); err != nil {
		t.Fatalf("second save: %v", err)
	}

	bakInfo, err := os.Stat(bakPath)
	if err != nil {
		t.Fatalf("stat .bak: %v", err)
	}
	if bakInfo.Size() == 0 {
		t.Error(".bak is empty")
	}

	// The main file should reflect the newer config.
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Bridge.Team != 2222 {
		t.Errorf("main file Team: got %d, want 2222", loaded.Bridge.Team)
	}

	// The .bak should reflect the older config.
	bakLoaded, err := LoadConfig(bakPath)
	if err != nil {
		t.Fatalf("load .bak: %v", err)
	}
	if bakLoaded.Bridge.Team != 1111 {
		t.Errorf(".bak Team: got %d, want 1111", bakLoaded.Bridge.Team)
	}
}

// ---------------------------------------------------------------------------
// TestLoadMissingFile — LoadConfig on a nonexistent path returns an error.
// ---------------------------------------------------------------------------

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadConfig(filepath.Join(dir, "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestLoadMalformedYAML — LoadConfig on garbage YAML returns an error.
// ---------------------------------------------------------------------------

func TestLoadMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	// Unclosed map + bad indentation.
	writeFile(t, path, "bridge:\n  team: [this is\nnot yaml")

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestRobotIP — FRC 10.TE.AM.2 convention.
// ---------------------------------------------------------------------------

func TestRobotIP(t *testing.T) {
	cases := []struct {
		team int
		want string
	}{
		{1310, "10.13.10.2"},
		{254, "10.2.54.2"},
		{9999, "10.99.99.2"},
		{1, "10.0.1.2"},
		{100, "10.1.0.2"},
	}
	for _, c := range cases {
		c := c
		t.Run("", func(t *testing.T) {
			cfg := &Config{Bridge: BridgeConfig{Team: c.team}}
			if got := cfg.RobotIP(); got != c.want {
				t.Errorf("team %d: got %q, want %q", c.team, got, c.want)
			}
		})
	}
}
