package uploader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// telemetryFake is an in-memory fake of the RavenBrain/RavenScope HTTP API.
// It counts POST /session and /complete calls (for idempotency assertions),
// tracks per-session uploadedCount (for resumption), and can be configured
// to fail with 503 on /data to simulate a broken target.
type telemetryFake struct {
	srv *httptest.Server

	mu              sync.Mutex
	sessionCreate   int
	dataBatches     int
	completeCount   int
	uploaded        map[string]int // sessionID -> server-side uploadedCount
	fail503OnData   atomic.Bool
}

func newTelemetryFake(t *testing.T) *telemetryFake {
	t.Helper()
	f := &telemetryFake{uploaded: map[string]int{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/telemetry/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		f.sessionCreate++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/telemetry/session/", func(w http.ResponseWriter, r *http.Request) {
		// Routes:
		//   GET  /api/telemetry/session/{id}
		//   POST /api/telemetry/session/{id}/data
		//   POST /api/telemetry/session/{id}/complete
		path := strings.TrimPrefix(r.URL.Path, "/api/telemetry/session/")
		parts := strings.Split(path, "/")
		sessionID := parts[0]

		switch {
		case r.Method == http.MethodGet && len(parts) == 1:
			f.mu.Lock()
			count := f.uploaded[sessionID]
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"uploadedCount": count})
		case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "data":
			if f.fail503OnData.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var batch []map[string]any
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.uploaded[sessionID] += len(batch)
			f.dataBatches++
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"count": len(batch)})
		case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "complete":
			f.mu.Lock()
			f.completeCount++
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// HEAD / — used by Target.ping.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// snapshotCounts returns a deep-copy of call counters under lock.
func (f *telemetryFake) snapshotCounts() (sess, data, comp int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionCreate, f.dataBatches, f.completeCount
}

// makeTestPendingFile writes a JSONL file with a session_start line plus
// numEntries data lines, and returns its path. The file is placed in
// dir/pending/<sessionID>.jsonl.
func makeTestPendingFile(t *testing.T, dir, sessionID string, numEntries int) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0o755); err != nil {
		t.Fatalf("mkdir pending: %v", err)
	}
	fpath := filepath.Join(dir, "pending", sessionID+".jsonl")
	f, err := os.Create(fpath)
	if err != nil {
		t.Fatalf("create %s: %v", fpath, err)
	}
	defer f.Close()
	ts := 1712678400.0
	_, _ = fmt.Fprintf(f,
		`{"ts":%f,"type":"session_start","session_id":%q,"team":1310,"robot_ip":"10.13.10.2"}`+"\n",
		ts, sessionID)
	for i := 0; i < numEntries; i++ {
		_, _ = fmt.Fprintf(f,
			`{"ts":%f,"type":"double","key":"/Foo","value":%f}`+"\n",
			ts+float64(i+1)*0.1, float64(i))
	}
	return fpath
}

// newBearerTarget builds a *Target that uses bearer-key auth against the
// given fake server. The name becomes the marker-file infix.
func newBearerTarget(t *testing.T, name string, fs *telemetryFake, interval time.Duration) *Target {
	t.Helper()
	trustTelemetryFake(t, fs)
	auth := NewAuthWithKey(fs.srv.URL, "test-key-"+name)
	tgt, err := NewTarget(name, auth, 50, interval)
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	return tgt
}

// trustTelemetryFake installs the fake's TLS root into the default
// transport so Target.http can reach the self-signed cert.
func trustTelemetryFake(t *testing.T, fs *telemetryFake) {
	t.Helper()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("DefaultTransport is %T", http.DefaultTransport)
	}
	prior := tr.TLSClientConfig
	tr.TLSClientConfig = fs.srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	t.Cleanup(func() { tr.TLSClientConfig = prior })
}

// ---------------------------------------------------------------------------
// TestUploader_TwoTargets_Happy — both targets enabled and reachable.
// ---------------------------------------------------------------------------

func TestUploader_TwoTargets_Happy(t *testing.T) {
	dir := t.TempDir()
	// Use independent fakes so each target sees a fresh uploadedCount
	// and posts its own /data batches — matches production where
	// RavenBrain and RavenScope are distinct servers.
	fsBrain := newTelemetryFake(t)
	fsScope := newTelemetryFake(t)

	tBrain := newBearerTarget(t, "ravenbrain", fsBrain, time.Hour)
	tScope := newBearerTarget(t, "ravenscope", fsScope, time.Hour)

	makeTestPendingFile(t, dir, "sess-happy", 3)

	u := New(dir, []*Target{tBrain, tScope}, nil)

	u.maybeUploadForTarget(tBrain, "")
	u.maybeUploadForTarget(tScope, "")

	if _, err := os.Stat(filepath.Join(dir, "pending", "sess-happy.jsonl")); !os.IsNotExist(err) {
		t.Errorf("expected file gone from pending/, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "uploaded", "sess-happy.jsonl")); err != nil {
		t.Errorf("expected file in uploaded/, got err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "pending", "*.done"))
	if len(matches) != 0 {
		t.Errorf("markers not cleaned: %v", matches)
	}
	for _, tc := range []struct {
		name string
		fs   *telemetryFake
	}{{"brain", fsBrain}, {"scope", fsScope}} {
		sess, data, comp := tc.fs.snapshotCounts()
		if sess != 1 || data != 1 || comp != 1 {
			t.Errorf("%s fake counts: session=%d data=%d complete=%d, want 1/1/1",
				tc.name, sess, data, comp)
		}
	}
}

// ---------------------------------------------------------------------------
// TestUploader_OneTarget_Happy — only RavenScope enabled; file moves with
// only one marker.
// ---------------------------------------------------------------------------

func TestUploader_OneTarget_Happy(t *testing.T) {
	dir := t.TempDir()
	fs := newTelemetryFake(t)
	tScope := newBearerTarget(t, "ravenscope", fs, time.Hour)
	makeTestPendingFile(t, dir, "sess-one", 2)

	u := New(dir, []*Target{tScope}, nil)
	u.maybeUploadForTarget(tScope, "")

	if _, err := os.Stat(filepath.Join(dir, "uploaded", "sess-one.jsonl")); err != nil {
		t.Errorf("expected file in uploaded/, got err=%v", err)
	}
	// There should be no ravenbrain marker since that target wasn't enabled.
	if _, err := os.Stat(filepath.Join(dir, "pending", "sess-one.jsonl.ravenbrain.done")); err == nil {
		t.Error("did not expect ravenbrain marker")
	}
}

// ---------------------------------------------------------------------------
// TestUploader_NoTargets_LocalOnly — zero targets leaves the file alone.
// ---------------------------------------------------------------------------

func TestUploader_NoTargets_LocalOnly(t *testing.T) {
	dir := t.TempDir()
	makeTestPendingFile(t, dir, "sess-local", 1)

	u := New(dir, nil, nil)
	// Run with no-op ctx would just return; assert that finalizeAnyReady
	// during New() did not move the file.
	if _, err := os.Stat(filepath.Join(dir, "pending", "sess-local.jsonl")); err != nil {
		t.Errorf("local-only mode should leave file in pending/, got err=%v", err)
	}
	_ = u
}

// ---------------------------------------------------------------------------
// TestUploader_OneTargetDown — one target 503s; the other completes
// normally; file stays in pending with only the healthy marker; failing
// target's backoff is armed.
// ---------------------------------------------------------------------------

func TestUploader_OneTargetDown(t *testing.T) {
	dir := t.TempDir()
	fsHealthy := newTelemetryFake(t)
	fsBroken := newTelemetryFake(t)
	fsBroken.fail503OnData.Store(true)

	tBrain := newBearerTarget(t, "ravenbrain", fsBroken, time.Hour)
	tScope := newBearerTarget(t, "ravenscope", fsHealthy, time.Hour)

	makeTestPendingFile(t, dir, "sess-split", 2)

	u := New(dir, []*Target{tBrain, tScope}, nil)
	u.maybeUploadForTarget(tBrain, "")  // fails on /data → 503 → backoff
	u.maybeUploadForTarget(tScope, "")  // succeeds → marker written

	if _, err := os.Stat(filepath.Join(dir, "pending", "sess-split.jsonl")); err != nil {
		t.Errorf("file should remain in pending/ when not all markers present; err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "sess-split.jsonl.ravenscope.done")); err != nil {
		t.Errorf("scope marker missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "sess-split.jsonl.ravenbrain.done")); err == nil {
		t.Error("brain marker should NOT exist after 503")
	}
	if !tBrain.inBackoff() {
		t.Error("failing target should be in backoff")
	}
	if tScope.inBackoff() {
		t.Error("healthy target should NOT be in backoff")
	}
	// Now heal the broken target and run again — file should finalize.
	fsBroken.fail503OnData.Store(false)
	tBrain.clearBackoff()
	u.maybeUploadForTarget(tBrain, "")

	if _, err := os.Stat(filepath.Join(dir, "uploaded", "sess-split.jsonl")); err != nil {
		t.Errorf("expected file in uploaded/ after recovery; err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// TestUploader_DisableWaitingTarget — simulate a restart where the user
// disabled RavenBrain. Pre-existing .ravenbrain.done marker + only
// RavenScope in the targets slice. The file should move on startup
// because the enabled set (just ravenscope) has its marker.
// ---------------------------------------------------------------------------

func TestUploader_DisableWaitingTarget(t *testing.T) {
	dir := t.TempDir()
	fs := newTelemetryFake(t)
	tScope := newBearerTarget(t, "ravenscope", fs, time.Hour)

	fpath := makeTestPendingFile(t, dir, "sess-disable", 1)

	// Pretend ravenscope uploaded this file in a previous run.
	if err := writeMarker(fpath, "ravenscope"); err != nil {
		t.Fatalf("pre-mark: %v", err)
	}

	// Construct uploader with ravenscope still enabled (but no need to
	// re-upload — marker already exists). The startup finalizeAnyReady
	// sweep should move the file.
	_ = New(dir, []*Target{tScope}, nil)

	if _, err := os.Stat(filepath.Join(dir, "pending", "sess-disable.jsonl")); !os.IsNotExist(err) {
		t.Errorf("file should have been finalized on startup; err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "uploaded", "sess-disable.jsonl")); err != nil {
		t.Errorf("expected file in uploaded/ after startup sweep; err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// TestUploader_RestartPartialUpload — a `.ravenbrain.done` marker
// pre-exists; only ravenscope needs to upload. After ravenscope runs,
// file finalizes. Fake server should only see one POST /session.
// ---------------------------------------------------------------------------

func TestUploader_RestartPartialUpload(t *testing.T) {
	dir := t.TempDir()
	fsScope := newTelemetryFake(t)
	fsBrain := newTelemetryFake(t)

	tBrain := newBearerTarget(t, "ravenbrain", fsBrain, time.Hour)
	tScope := newBearerTarget(t, "ravenscope", fsScope, time.Hour)

	fpath := makeTestPendingFile(t, dir, "sess-resume", 2)
	// Pretend ravenbrain had already completed this file.
	if err := writeMarker(fpath, "ravenbrain"); err != nil {
		t.Fatalf("pre-mark: %v", err)
	}

	u := New(dir, []*Target{tBrain, tScope}, nil)
	// Run both targets; brain should skip (already marked), scope should upload.
	u.maybeUploadForTarget(tBrain, "")
	u.maybeUploadForTarget(tScope, "")

	if _, err := os.Stat(filepath.Join(dir, "uploaded", "sess-resume.jsonl")); err != nil {
		t.Errorf("expected file in uploaded/; err=%v", err)
	}
	sessBrain, dataBrain, _ := fsBrain.snapshotCounts()
	if sessBrain != 0 || dataBrain != 0 {
		t.Errorf("brain fake should have zero traffic (already marked); session=%d data=%d",
			sessBrain, dataBrain)
	}
	sessScope, dataScope, _ := fsScope.snapshotCounts()
	if sessScope != 1 || dataScope != 1 {
		t.Errorf("scope fake should have 1 session + 1 data; got session=%d data=%d",
			sessScope, dataScope)
	}
}

// ---------------------------------------------------------------------------
// TestUploader_OrphanMarkerSweep — a stray `.done` marker without its
// base file is removed on New().
// ---------------------------------------------------------------------------

func TestUploader_OrphanMarkerSweep(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "pending", "nothing.jsonl.ravenbrain.done")
	if err := os.WriteFile(orphan, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = New(dir, nil, nil)
	if _, err := os.Stat(orphan); err == nil {
		t.Error("orphan marker was not swept")
	}
}

// ---------------------------------------------------------------------------
// TestUploader_ConcurrentFinalize — two targets finishing the same file
// concurrently should finalize exactly once, with no leftover markers.
// ---------------------------------------------------------------------------

func TestUploader_ConcurrentFinalize(t *testing.T) {
	dir := t.TempDir()
	fs := newTelemetryFake(t)
	tBrain := newBearerTarget(t, "ravenbrain", fs, time.Hour)
	tScope := newBearerTarget(t, "ravenscope", fs, time.Hour)

	makeTestPendingFile(t, dir, "sess-race", 2)

	u := New(dir, []*Target{tBrain, tScope}, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); u.maybeUploadForTarget(tBrain, "") }()
	go func() { defer wg.Done(); u.maybeUploadForTarget(tScope, "") }()
	wg.Wait()

	if _, err := os.Stat(filepath.Join(dir, "uploaded", "sess-race.jsonl")); err != nil {
		t.Errorf("expected file in uploaded/; err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "pending", "*.done"))
	if len(matches) != 0 {
		t.Errorf("leftover markers: %v", matches)
	}
}

// ---------------------------------------------------------------------------
// TestUploader_DrainPending_BothTargets — synchronous shutdown flush.
// ---------------------------------------------------------------------------

func TestUploader_DrainPending_BothTargets(t *testing.T) {
	dir := t.TempDir()
	fs := newTelemetryFake(t)
	tBrain := newBearerTarget(t, "ravenbrain", fs, time.Hour)
	tScope := newBearerTarget(t, "ravenscope", fs, time.Hour)

	makeTestPendingFile(t, dir, "sess-drain-a", 1)
	makeTestPendingFile(t, dir, "sess-drain-b", 1)

	u := New(dir, []*Target{tBrain, tScope}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u.DrainPending(ctx, "")

	for _, name := range []string{"sess-drain-a", "sess-drain-b"} {
		if _, err := os.Stat(filepath.Join(dir, "uploaded", name+".jsonl")); err != nil {
			t.Errorf("drain: expected %s in uploaded/; err=%v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// TestUploader_ActiveSessionSkipped — a file whose name contains the
// active session ID is NOT touched by any target.
// ---------------------------------------------------------------------------

func TestUploader_ActiveSessionSkipped(t *testing.T) {
	dir := t.TempDir()
	fs := newTelemetryFake(t)
	tScope := newBearerTarget(t, "ravenscope", fs, time.Hour)

	makeTestPendingFile(t, dir, "active-session", 1)

	u := New(dir, []*Target{tScope}, nil)
	u.maybeUploadForTarget(tScope, "active-session")

	if _, err := os.Stat(filepath.Join(dir, "pending", "active-session.jsonl")); err != nil {
		t.Errorf("active session file should stay in pending/; err=%v", err)
	}
	sess, data, _ := fs.snapshotCounts()
	if sess != 0 || data != 0 {
		t.Errorf("active session should produce no traffic; got session=%d data=%d", sess, data)
	}
}
