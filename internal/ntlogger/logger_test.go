package ntlogger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/statemachine"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testRig wraps a Logger + the plumbing needed to drive it from tests.
type testRig struct {
	t        *testing.T
	dataDir  string
	valuesCh chan ntclient.TopicValue
	logger   *Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// newTestRig builds a rig with a tempdir, a values channel, and a running
// actor goroutine. Cleanup is registered via t.Cleanup.
func newTestRig(t *testing.T, team int) *testRig {
	t.Helper()
	dataDir := t.TempDir()
	valuesCh := make(chan ntclient.TopicValue, 16)
	logger := New(valuesCh, dataDir, team)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		logger.Run(ctx)
	}()

	rig := &testRig{
		t:        t,
		dataDir:  dataDir,
		valuesCh: valuesCh,
		logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
		done:     done,
	}
	t.Cleanup(rig.shutdown)
	return rig
}

// shutdown cancels the actor and waits for it to exit.
func (r *testRig) shutdown() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.t.Error("logger did not shut down within 5s")
	}
}

// waitForSession polls Stats() until the active session ID is non-empty,
// failing the test after a short timeout.
func (r *testRig) waitForSession() string {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sid := r.logger.Stats().ActiveSessionID
		if sid != "" {
			return sid
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatal("timed out waiting for session to start")
	return ""
}

// waitForNoSession polls Stats() until ActiveSessionID is empty.
func (r *testRig) waitForNoSession() {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.logger.Stats().ActiveSessionID == "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatal("timed out waiting for session to end")
}

// waitForEntries polls until EntriesWritten >= n.
func (r *testRig) waitForEntries(n int) {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.logger.Stats().EntriesWritten >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for entries>=%d, have %d", n, r.logger.Stats().EntriesWritten)
}

// readPendingFile returns the contents of the single pending JSONL file.
// Fails if there are zero or multiple files.
func (r *testRig) readPendingFile() []string {
	r.t.Helper()
	pendingDir := filepath.Join(r.dataDir, "pending")
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		r.t.Fatalf("read pending dir: %v", err)
	}
	var jsonl []os.DirEntry
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			jsonl = append(jsonl, e)
		}
	}
	if len(jsonl) == 0 {
		r.t.Fatal("no jsonl files in pending/")
	}
	if len(jsonl) != 1 {
		r.t.Fatalf("expected 1 jsonl file, got %d", len(jsonl))
	}
	data, err := os.ReadFile(filepath.Join(pendingDir, jsonl[0].Name()))
	if err != nil {
		r.t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return lines
}

// findLine returns the first JSONL line whose "type" field matches, or
// fails the test.
func findLine(t *testing.T, lines []string, typ string) map[string]any {
	t.Helper()
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["type"] == typ {
			return entry
		}
	}
	t.Fatalf("no line with type=%q in %d lines", typ, len(lines))
	return nil
}

// ---------------------------------------------------------------------------
// TestSessionLifecycle — StartSession writes a session_start marker,
// EndSession writes a session_end marker and closes the file.
// ---------------------------------------------------------------------------

func TestSessionLifecycle(t *testing.T) {
	rig := newTestRig(t, 1310)

	rig.logger.StartSession()
	sid := rig.waitForSession()
	if sid == "" {
		t.Fatal("empty session ID")
	}

	rig.logger.EndSession()
	rig.waitForNoSession()

	lines := rig.readPendingFile()
	if len(lines) < 2 {
		t.Fatalf("expected >=2 lines, got %d", len(lines))
	}

	start := findLine(t, lines, "session_start")
	if start["session_id"] != sid {
		t.Errorf("session_start session_id: got %v, want %q", start["session_id"], sid)
	}
	if got := start["team"]; got != float64(1310) {
		t.Errorf("session_start team: got %v, want 1310", got)
	}
	if got := start["robot_ip"]; got != "10.13.10.2" {
		t.Errorf("session_start robot_ip: got %v, want 10.13.10.2", got)
	}

	end := findLine(t, lines, "session_end")
	if end["session_id"] != sid {
		t.Errorf("session_end session_id: got %v, want %q", end["session_id"], sid)
	}
}

// ---------------------------------------------------------------------------
// TestValueWriting — values pushed through the channel land as data lines.
// ---------------------------------------------------------------------------

func TestValueWriting(t *testing.T) {
	rig := newTestRig(t, 1310)

	rig.logger.StartSession()
	rig.waitForSession()

	rig.valuesCh <- ntclient.TopicValue{
		Name:             "/SmartDashboard/foo",
		Type:             "double",
		Value:            42.0,
		ServerTimeMicros: 123456,
	}
	rig.valuesCh <- ntclient.TopicValue{
		Name:             "/SmartDashboard/bar",
		Type:             "string",
		Value:            "hello",
		ServerTimeMicros: 234567,
	}

	rig.waitForEntries(2)
	rig.logger.EndSession()
	rig.waitForNoSession()

	lines := rig.readPendingFile()

	// Pick out non-session-marker lines.
	var dataLines []map[string]any
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["type"] != "session_start" && entry["type"] != "session_end" {
			dataLines = append(dataLines, entry)
		}
	}
	if len(dataLines) != 2 {
		t.Fatalf("expected 2 data lines, got %d", len(dataLines))
	}
	if dataLines[0]["key"] != "/SmartDashboard/foo" {
		t.Errorf("first key: got %v", dataLines[0]["key"])
	}
	if dataLines[0]["type"] != "double" {
		t.Errorf("first type: got %v", dataLines[0]["type"])
	}
	if dataLines[0]["value"] != 42.0 {
		t.Errorf("first value: got %v", dataLines[0]["value"])
	}
	if dataLines[1]["value"] != "hello" {
		t.Errorf("second value: got %v", dataLines[1]["value"])
	}

	if stats := rig.logger.Stats(); stats.EntriesWritten < 2 {
		t.Errorf("EntriesWritten: got %d, want >=2", stats.EntriesWritten)
	}
}

// ---------------------------------------------------------------------------
// TestMatchEvent — RecordMatchEvent appends a marker line.
// ---------------------------------------------------------------------------

func TestMatchEvent(t *testing.T) {
	rig := newTestRig(t, 1310)
	rig.logger.StartSession()
	rig.waitForSession()

	fms := statemachine.FMSStateFromRaw(0x33) // enabled+auto+FMS+DS
	rig.logger.RecordMatchEvent("match_start", fms)

	rig.logger.EndSession()
	rig.waitForNoSession()

	lines := rig.readPendingFile()
	entry := findLine(t, lines, "match_start")
	if entry["fms_enabled"] != true {
		t.Errorf("fms_enabled: got %v, want true", entry["fms_enabled"])
	}
	if entry["fms_auto"] != true {
		t.Errorf("fms_auto: got %v, want true", entry["fms_auto"])
	}
	if entry["fms_attached"] != true {
		t.Errorf("fms_attached: got %v, want true", entry["fms_attached"])
	}
	if _, ok := entry["fms_state"].(string); !ok {
		t.Errorf("fms_state: got %T, want string", entry["fms_state"])
	}
}

// ---------------------------------------------------------------------------
// TestNoSessionDropsValues — values received while no session is active are
// silently dropped; no file is created.
// ---------------------------------------------------------------------------

func TestNoSessionDropsValues(t *testing.T) {
	rig := newTestRig(t, 1310)

	// No StartSession — push values anyway.
	for i := 0; i < 5; i++ {
		rig.valuesCh <- ntclient.TopicValue{
			Name:  "/x",
			Type:  "int",
			Value: int64(i),
		}
	}

	// Give the actor a beat to drain the channel.
	time.Sleep(50 * time.Millisecond)

	// Pending directory should contain no jsonl files.
	pendingDir := filepath.Join(rig.dataDir, "pending")
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		t.Fatalf("read pending dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			t.Errorf("unexpected jsonl file while idle: %s", e.Name())
		}
	}
	if sid := rig.logger.Stats().ActiveSessionID; sid != "" {
		t.Errorf("ActiveSessionID: got %q, want empty", sid)
	}
}

// ---------------------------------------------------------------------------
// TestConcurrentCommands — hammer the public API from multiple goroutines.
// Under -race this catches any shared-state violations.
// ---------------------------------------------------------------------------

func TestConcurrentCommands(t *testing.T) {
	rig := newTestRig(t, 1310)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Starter/ender loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				rig.logger.StartSession()
				time.Sleep(time.Millisecond)
				rig.logger.EndSession()
			}
		}
	}()

	// Match event loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		fms := statemachine.FMSStateFromRaw(0x11)
		for {
			select {
			case <-stop:
				return
			default:
				rig.logger.RecordMatchEvent("match_start", fms)
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Stats reader.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = rig.logger.Stats()
			}
		}
	}()

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()

	// End cleanly before shutdown so the actor flushes any open file.
	rig.logger.EndSession()
}
