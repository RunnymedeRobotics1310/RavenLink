package uploader

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	initialBackoff = 5 * time.Second
	maxBackoff     = 60 * time.Second

	markerExt    = ".done"
	markerPrefix = ".jsonl." // marker filename is <base>.jsonl.<target>.done
)

// Uploader is the multi-target coordinator. It scans the pending/
// directory for completed JSONL session files and fans each one out to
// every configured Target. A file moves to uploaded/ only after every
// target has acknowledged it; per-target progress is tracked on disk via
// sidecar `<base>.jsonl.<target>.done` marker files so the semantics
// survive process restarts.
type Uploader struct {
	dataDir     string
	pendingDir  string
	uploadedDir string

	targets []*Target

	activeSessionFn func() string
	pausedFn        func() bool

	// finalizeMu serializes the "are all markers present → move file"
	// decision across concurrent target goroutines. Without it, two
	// targets finishing the same file at the same instant could both
	// conclude they were the last to mark and race on the rename.
	finalizeMu sync.Mutex
}

// New constructs an Uploader with the given set of targets. Pass an
// empty slice (or nil) to run in local-only mode: no upload goroutines
// will start, files stay in pending/ indefinitely, and DrainPending is
// a no-op. pending/ and uploaded/ directories are created under dataDir
// if they don't already exist. On construction, orphan `.done` markers
// in pending/ (markers without a corresponding .jsonl base file) are
// deleted as a best-effort sweep.
func New(
	dataDir string,
	targets []*Target,
	activeSessionFn func() string,
) *Uploader {
	pendingDir := filepath.Join(dataDir, "pending")
	uploadedDir := filepath.Join(dataDir, "uploaded")

	for _, d := range []string{pendingDir, uploadedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			slog.Error("uploader: failed to create directory", "path", d, "err", err)
		}
	}

	u := &Uploader{
		dataDir:         dataDir,
		pendingDir:      pendingDir,
		uploadedDir:     uploadedDir,
		targets:         targets,
		activeSessionFn: activeSessionFn,
	}
	u.sweepOrphanMarkers()
	// Handle startup case where a previously-enabled target's marker is
	// already present for every currently-enabled target — e.g., user
	// disabled RavenScope between sessions and RavenBrain's marker was
	// already on disk. Without this sweep, such files would wait for a
	// target upload tick that never has work to do.
	u.finalizeAnyReady()
	return u
}

// Targets returns the configured targets. Callers (main.go status
// fan-in) read via each target's Snapshot method.
func (u *Uploader) Targets() []*Target { return u.targets }

// SetPauseFn registers a predicate that gates all scheduled uploads.
// DrainPending is NOT gated — shutdown drain must flush files that were
// written before the pause.
func (u *Uploader) SetPauseFn(fn func() bool) { u.pausedFn = fn }

func (u *Uploader) currentActiveSessionID() string {
	if u.activeSessionFn == nil {
		return ""
	}
	return u.activeSessionFn()
}

// Run starts one goroutine per target, each with its own ticker keyed
// to the target's configured upload interval. Returns when ctx is
// cancelled. Local-only mode (no targets) returns immediately.
func (u *Uploader) Run(ctx context.Context) {
	if len(u.targets) == 0 {
		slog.Info("uploader: local-only mode (no upload targets configured)")
		return
	}
	slog.Info("uploader: running", "dataDir", u.dataDir, "targets", targetNames(u.targets))

	var wg sync.WaitGroup
	for _, t := range u.targets {
		wg.Add(1)
		go func(t *Target) {
			defer wg.Done()
			u.runTargetLoop(ctx, t)
		}(t)
	}
	wg.Wait()
	slog.Info("uploader: shutting down")
}

func (u *Uploader) runTargetLoop(ctx context.Context, t *Target) {
	ticker := time.NewTicker(t.Interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.maybeUploadForTarget(t, u.currentActiveSessionID())
		}
	}
}

// maybeUploadForTarget runs one upload attempt for a single target.
// Layout: finalize-ready sweep → pause check → list owing files → pick
// oldest → if in backoff skip (but still update pending count) →
// attempt one file. Each tick processes at most one file per target so
// logs stay readable.
func (u *Uploader) maybeUploadForTarget(t *Target, activeSessionID string) {
	if !t.IsConfigured() {
		t.setReachable(false)
		return
	}
	if u.pausedFn != nil && u.pausedFn() {
		return
	}

	// Cheap sweep for files whose markers are all present but weren't
	// finalized (e.g., another target ran into an error between marker
	// write and finalize). Guarded by finalizeMu internally.
	u.finalizeAnyReady()

	owing := u.filesOwingTarget(t, activeSessionID)
	t.setFilesPending(len(owing))

	if len(owing) == 0 {
		t.setReachable(t.ping())
		return
	}
	if t.inBackoff() {
		return
	}

	fpath := owing[0]
	t.setCurrentlyUploading(true)
	defer t.setCurrentlyUploading(false)

	ok, err := t.uploadFile(fpath)
	if err != nil {
		t.setReachable(false)
		slog.Warn("uploader: upload failed", "target", t.name, "file", shortName(fpath), "err", err)
		t.setLastResult("ERROR: " + err.Error())
		t.applyBackoff()
		return
	}
	t.setReachable(true)
	if !ok {
		t.applyBackoff()
		return
	}

	if err := writeMarker(fpath, t.name); err != nil {
		// A marker write failure means we'd re-upload this file to this
		// target on the next tick. The server-side uploadedCount makes
		// that cheap, so treat this like an upload failure for backoff
		// purposes and move on — do NOT try to finalize.
		slog.Warn("uploader: marker write failed", "target", t.name, "file", shortName(fpath), "err", err)
		t.setLastResult("Marker write error: " + err.Error())
		t.applyBackoff()
		return
	}

	t.incFilesUploaded()
	t.clearBackoff()
	t.setLastResult("OK: " + shortName(fpath))
	slog.Info("uploader: uploaded", "target", t.name, "file", shortName(fpath))

	u.maybeFinalize(fpath)

	// Refresh pending count for this target after the finalize attempt.
	t.setFilesPending(len(u.filesOwingTarget(t, activeSessionID)))
}

// maybeFinalize moves fpath to uploaded/ if every currently enabled
// target has a `.done` marker for it. Runs under finalizeMu so
// concurrent target goroutines don't race on the rename.
//
// Zero-target mode (no upload destinations configured) leaves files in
// place — we don't want to silently evacuate pending/ into uploaded/
// when the user has explicitly disabled uploads. This matches the
// existing local-only behavior when ravenbrain.url was empty.
func (u *Uploader) maybeFinalize(fpath string) {
	if len(u.targets) == 0 {
		return
	}
	u.finalizeMu.Lock()
	defer u.finalizeMu.Unlock()
	u.finalizeLocked(fpath)
}

// finalizeAnyReady scans pending/ for any file whose markers cover every
// currently enabled target and moves the matching files to uploaded/.
// Used at startup and at the top of each tick to handle files that
// became finalizable because the enabled set changed (e.g., a target
// was disabled via config edit + restart) or because a target crashed
// between writeMarker and maybeFinalize.
func (u *Uploader) finalizeAnyReady() {
	if len(u.targets) == 0 {
		return
	}
	u.finalizeMu.Lock()
	defer u.finalizeMu.Unlock()

	for _, fpath := range u.listPendingJSONL("") {
		u.finalizeLocked(fpath)
	}
}

// finalizeLocked is the inner finalize step. Callers must hold
// finalizeMu. Skips when targets is empty.
func (u *Uploader) finalizeLocked(fpath string) {
	if len(u.targets) == 0 {
		return
	}
	if _, err := os.Stat(fpath); err != nil {
		return
	}
	for _, t := range u.targets {
		if !hasMarker(fpath, t.name) {
			return
		}
	}
	dest := filepath.Join(u.uploadedDir, filepath.Base(fpath))
	if err := os.Rename(fpath, dest); err != nil {
		slog.Warn("uploader: finalize rename failed", "file", shortName(fpath), "err", err)
		return
	}
	cleanupAllMarkers(fpath)
	slog.Info("uploader: finalized", "file", shortName(fpath))
}

// DrainPending synchronously uploads every pending file to every
// configured target, ignoring backoff and tick intervals. Stops when
// ctx is cancelled or when no target has remaining work. Each iteration
// processes one (target, file) pair; a failed upload on one target
// does not prevent others from continuing.
//
// Intended for shutdown: after the main loop has stopped and the logger
// has closed its final session file, call DrainPending with a deadline
// to flush everything RavenBrain/RavenScope are expecting before exit.
func (u *Uploader) DrainPending(ctx context.Context, activeSessionID string) {
	if len(u.targets) == 0 {
		slog.Info("uploader: drain skipped (no targets configured)")
		return
	}
	for _, t := range u.targets {
		if t.IsConfigured() {
			t.clearBackoff()
		}
	}

	for {
		if ctx.Err() != nil {
			slog.Warn("uploader: drain deadline reached, giving up", "err", ctx.Err())
			return
		}

		progressed := false
		for _, t := range u.targets {
			if !t.IsConfigured() {
				continue
			}
			owing := u.filesOwingTarget(t, activeSessionID)
			if len(owing) == 0 {
				continue
			}
			fpath := owing[0]
			slog.Info("uploader: draining", "target", t.name, "file", shortName(fpath), "remaining", len(owing))
			t.setCurrentlyUploading(true)
			ok, err := t.uploadFile(fpath)
			t.setCurrentlyUploading(false)
			if err != nil {
				slog.Warn("uploader: drain upload failed — remaining files will be retried on next startup",
					"target", t.name, "file", shortName(fpath), "err", err)
				continue
			}
			if !ok {
				slog.Warn("uploader: drain upload returned not-ok",
					"target", t.name, "file", shortName(fpath))
				continue
			}
			if err := writeMarker(fpath, t.name); err != nil {
				slog.Warn("uploader: drain marker write failed",
					"target", t.name, "file", shortName(fpath), "err", err)
				continue
			}
			t.incFilesUploaded()
			progressed = true
			u.maybeFinalize(fpath)
		}
		if !progressed {
			slog.Info("uploader: drain complete")
			return
		}
	}
}

// PruneUploaded deletes files from uploaded/ that are older than
// retentionDays. If retentionDays <= 0, no pruning is done.
func (u *Uploader) PruneUploaded(retentionDays int) {
	if retentionDays <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	entries, err := os.ReadDir(u.uploadedDir)
	if err != nil {
		slog.Warn("uploader: failed to read uploaded dir", "err", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			fpath := filepath.Join(u.uploadedDir, e.Name())
			slog.Info("uploader: pruning old upload", "file", e.Name())
			_ = os.Remove(fpath)
		}
	}
}

// filesOwingTarget returns the pending JSONL files that the target has
// not yet marked done, sorted oldest-first. Files whose name contains
// activeSessionID (the actively-recording session) are skipped.
func (u *Uploader) filesOwingTarget(t *Target, activeSessionID string) []string {
	all := u.listPendingJSONL(activeSessionID)
	owing := all[:0]
	for _, fpath := range all {
		if !hasMarker(fpath, t.name) {
			owing = append(owing, fpath)
		}
	}
	return owing
}

// listPendingJSONL returns every JSONL file in pending/, excluding the
// active session and sorted oldest-first. Marker files are ignored
// because the HasSuffix(".jsonl") filter rejects them.
func (u *Uploader) listPendingJSONL(activeSessionID string) []string {
	entries, err := os.ReadDir(u.pendingDir)
	if err != nil {
		slog.Warn("uploader: failed to read pending dir", "err", err)
		return nil
	}

	type fileEntry struct {
		path    string
		modTime time.Time
	}
	var files []fileEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if activeSessionID != "" && strings.Contains(e.Name(), activeSessionID) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			path:    filepath.Join(u.pendingDir, e.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})
	result := make([]string, len(files))
	for i, f := range files {
		result[i] = f.path
	}
	return result
}

// sweepOrphanMarkers removes `.done` marker files in pending/ whose
// corresponding .jsonl base file no longer exists. These can arise if
// the process crashes between the base-file rename (into uploaded/) and
// the marker cleanup; the previous file lifecycle has already
// completed, so the markers are safe to discard.
func (u *Uploader) sweepOrphanMarkers() {
	entries, err := os.ReadDir(u.pendingDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, markerExt) {
			continue
		}
		base := strings.TrimSuffix(name, markerExt)
		// base looks like "<id>.jsonl.<targetname>"; strip ".<targetname>"
		// to get the original JSONL filename.
		dot := strings.LastIndex(base, ".")
		if dot <= 0 {
			continue
		}
		jsonlName := base[:dot]
		if !strings.HasSuffix(jsonlName, ".jsonl") {
			continue
		}
		jsonlPath := filepath.Join(u.pendingDir, jsonlName)
		if _, err := os.Stat(jsonlPath); err != nil {
			markerPath := filepath.Join(u.pendingDir, name)
			if rmErr := os.Remove(markerPath); rmErr == nil {
				slog.Info("uploader: swept orphan marker", "file", name)
			}
		}
	}
}

// markerPathFor returns the sidecar marker path for (file, target).
func markerPathFor(fpath, targetName string) string {
	return fpath + "." + targetName + markerExt
}

func hasMarker(fpath, targetName string) bool {
	_, err := os.Stat(markerPathFor(fpath, targetName))
	return err == nil
}

func writeMarker(fpath, targetName string) error {
	return os.WriteFile(markerPathFor(fpath, targetName), nil, 0o600)
}

// cleanupAllMarkers removes every `<fpath>.*.done` marker. Used when a
// base file is moved to uploaded/ — all markers associated with that
// file are no longer needed, including any stale ones from targets
// that are no longer enabled.
func cleanupAllMarkers(fpath string) {
	dir := filepath.Dir(fpath)
	base := filepath.Base(fpath) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, base) || !strings.HasSuffix(name, markerExt) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

func targetNames(ts []*Target) []string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.name
	}
	return names
}

// ---------------------------------------------------------------------------
// Helpers shared with target.go: session_start parsing, JSONL→API entry
// translation, timestamp conversion.
// ---------------------------------------------------------------------------

// parseSessionStart finds the first session_start line and parses it.
func parseSessionStart(lines []string) map[string]any {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["type"] == "session_start" {
			return entry
		}
	}
	return nil
}

// telemetryEntryRequest is the server's expected schema for a single entry in
// POST /api/telemetry/session/{sessionId}/data. Field names and types must
// match RavenBrain's TelemetryApi.TelemetryEntryRequest record.
type telemetryEntryRequest struct {
	Ts        string `json:"ts"`                // ISO-8601
	EntryType string `json:"entryType"`         // "data", "match_start", etc.
	NtKey     string `json:"ntKey,omitempty"`   // NT topic path (data entries only)
	NtType    string `json:"ntType,omitempty"`  // NT type name (data entries only)
	NtValue   string `json:"ntValue,omitempty"` // JSON-encoded value string
	FmsRaw    *int   `json:"fmsRaw,omitempty"`  // raw FMS bitmask (markers only)
}

// jsonlToServerEntry converts a parsed JSONL line from the local log format
// into the server's API schema. Returns false if the line is unrecognized.
func jsonlToServerEntry(raw map[string]any) (telemetryEntryRequest, bool) {
	entry := telemetryEntryRequest{
		Ts: convertLocalTimestamp(raw["ts"]),
	}

	typeField, _ := raw["type"].(string)
	if typeField == "" {
		return entry, false
	}

	if _, hasKey := raw["key"]; hasKey {
		entry.EntryType = "data"
		if k, ok := raw["key"].(string); ok {
			entry.NtKey = k
		}
		entry.NtType = typeField
		if v, ok := raw["value"]; ok {
			if b, err := json.Marshal(v); err == nil {
				entry.NtValue = string(b)
			}
		}
	} else {
		entry.EntryType = typeField
		if fms, ok := raw["fms_raw"]; ok {
			if n, ok := fms.(float64); ok {
				v := int(n)
				entry.FmsRaw = &v
			}
		}
	}

	return entry, true
}

// convertLocalTimestamp converts a local JSONL timestamp (Unix seconds with
// fractional component, as emitted by the Go logger via time.Now().UnixMicro)
// into an ISO-8601 string suitable for Java Instant parsing.
func convertLocalTimestamp(ts any) string {
	switch v := ts.(type) {
	case float64:
		secs := int64(v)
		nsecs := int64((v - float64(secs)) * 1e9)
		return time.Unix(secs, nsecs).UTC().Format(time.RFC3339Nano)
	case int64:
		return time.Unix(v, 0).UTC().Format(time.RFC3339Nano)
	case string:
		return v
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// findLastTimestamp returns the "ts" field from the last non-empty JSONL line
// converted to an ISO-8601 string for the server's completeSession endpoint.
func findLastTimestamp(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if ts, ok := entry["ts"]; ok {
			return convertLocalTimestamp(ts)
		}
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}
