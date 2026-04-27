package uploader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Target is one upload destination. The coordinator in uploader.go holds a
// slice of targets and dispatches each pending file to every enabled
// target. Each Target owns its own auth, HTTP client, batch size, tick
// interval, backoff state, and live status counters.
//
// Concurrency: the status counters, the last-result string, and the
// backoff fields are all guarded by Target.mu. Read access goes through
// Snapshot, which copies the fields under the read lock so callers
// (status fan-in in main.go) never observe torn state.
type Target struct {
	name      string
	auth      *Auth
	batchSize int
	interval  time.Duration

	httpClient *http.Client

	mu sync.RWMutex
	// Status counters (under mu).
	reachable          bool
	filesPending       int
	filesUploaded      int
	currentlyUploading bool
	lastResult         string
	// Backoff (under mu). Until a future call to GetAuthHeader or a
	// successful upload, this target skips its upload attempts.
	backoff      time.Duration
	backoffUntil time.Time
}

// TargetStatus is an immutable snapshot of a Target's observable state,
// suitable for serialization into the dashboard's /api/status JSON or
// for the tray menu. Callers hold no lock on the originating Target.
type TargetStatus struct {
	Name               string
	Enabled            bool
	Reachable          bool
	FilesPending       int
	FilesUploaded      int
	CurrentlyUploading bool
	LastResult         string
}

// NewTarget constructs a Target. name should be the short, lowercase
// identifier used in marker filenames ("ravenbrain", "ravenscope"). The
// caller supplies a pre-configured *Auth (legacy or bearer) and the
// per-target batch/interval pulled from config. Returns an error if name
// is empty or interval is non-positive.
func NewTarget(name string, auth *Auth, batchSize int, interval time.Duration) (*Target, error) {
	if name == "" {
		return nil, errors.New("target name must be non-empty")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("target %q: interval must be positive (got %v)", name, interval)
	}
	if batchSize <= 0 {
		batchSize = 50
	}
	return &Target{
		name:       name,
		auth:       auth,
		batchSize:  batchSize,
		interval:   interval,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Name returns the short target identifier.
func (t *Target) Name() string { return t.name }

// Interval returns the per-target upload tick interval.
func (t *Target) Interval() time.Duration { return t.interval }

// Auth exposes the target's auth handle for diagnostic callers (rbping).
// Do not mutate the returned *Auth.
func (t *Target) Auth() *Auth { return t.auth }

// Snapshot copies the target's observable status under a read lock.
// Enabled is always true — every Target in the uploader's slice is by
// definition currently enabled. The field exists on TargetStatus so the
// dashboard UI can render "disabled" rows for known-but-inactive targets
// driven from config without needing a separate shape.
func (t *Target) Snapshot() TargetStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TargetStatus{
		Name:               t.name,
		Enabled:            true,
		Reachable:          t.reachable,
		FilesPending:       t.filesPending,
		FilesUploaded:      t.filesUploaded,
		CurrentlyUploading: t.currentlyUploading,
		LastResult:         t.lastResult,
	}
}

// IsConfigured reports whether the target's auth can produce a valid
// header. Used by the coordinator to skip idle work for targets that
// aren't yet credentialed.
func (t *Target) IsConfigured() bool {
	return t.auth != nil && t.auth.IsConfigured()
}

// backoffActive reports whether the target is still inside a backoff
// window. Callers must hold t.mu (or call inBackoff which acquires it).
func (t *Target) backoffActive(now time.Time) bool {
	return now.Before(t.backoffUntil)
}

func (t *Target) inBackoff() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.backoffActive(time.Now())
}

// applyBackoff doubles the current backoff window from initialBackoff
// up to maxBackoff.
func (t *Target) applyBackoff() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.backoff == 0 {
		t.backoff = initialBackoff
	} else {
		t.backoff = min(t.backoff*2, maxBackoff)
	}
	t.backoffUntil = time.Now().Add(t.backoff)
	slog.Info("uploader: backing off", "target", t.name, "duration", t.backoff)
}

func (t *Target) clearBackoff() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.backoff = 0
	t.backoffUntil = time.Time{}
}

// setReachable updates the reachability field under the write lock.
func (t *Target) setReachable(r bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reachable = r
}

func (t *Target) setLastResult(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastResult = s
}

func (t *Target) setFilesPending(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.filesPending = n
}

func (t *Target) setCurrentlyUploading(u bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentlyUploading = u
}

func (t *Target) incFilesUploaded() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.filesUploaded++
}

// ping issues a GET against the target's base URL. Any HTTP response
// counts as reachable; only connection/timeout/TLS errors return false.
//
// GET (rather than HEAD) is used because some edges — notably
// Cloudflare Workers without an explicit HEAD branch — handle HEAD
// inconsistently, and a body-discarding GET is just as cheap for a
// liveness probe. Transport errors are logged at DEBUG so an operator
// running RavenLink with `--log-level=DEBUG` can see why a target is
// being marked Disconnected.
func (t *Target) ping() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.auth.BaseURL(), nil)
	if err != nil {
		slog.Debug("uploader: ping: bad request", "target", t.name, "err", err)
		return false
	}
	resp, err := t.httpClient.Do(req)
	if err != nil {
		slog.Debug("uploader: ping: transport error", "target", t.name, "url", t.auth.BaseURL(), "err", err)
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return true
}

// uploadFile runs the session-upload protocol for a single JSONL file
// against this target. Returns (true, nil) on complete success.
// Mirrors the single-target flow from the pre-refactor uploader: POST
// /session (upsert) → GET /session/{id} for uploadedCount → POST batched
// /data for the delta → POST /complete. Server-side uploadedCount
// guarantees per-target idempotency across retries.
func (t *Target) uploadFile(fpath string) (bool, error) {
	data, err := os.ReadFile(fpath)
	if err != nil {
		return false, fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		slog.Warn("uploader: empty JSONL file", "target", t.name, "file", shortName(fpath))
		return true, nil
	}

	meta := parseSessionStart(lines)
	if meta == nil {
		slog.Warn("uploader: no session_start found, skipping", "target", t.name, "file", shortName(fpath))
		return true, nil
	}
	sessionID, _ := meta["session_id"].(string)
	if sessionID == "" {
		slog.Warn("uploader: session_start has no session_id, skipping", "target", t.name, "file", shortName(fpath))
		return true, nil
	}

	teamNum, _ := meta["team"].(float64)
	robotIP, _ := meta["robot_ip"].(string)
	startedAt := convertLocalTimestamp(meta["ts"])

	ok, err := t.postJSON("/api/telemetry/session", map[string]any{
		"sessionId":  sessionID,
		"teamNumber": int(teamNum),
		"robotIp":    robotIP,
		"startedAt":  startedAt,
	})
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	serverCount, err := t.getUploadedCount(sessionID)
	if err != nil {
		return false, err
	}

	var entries []telemetryEntryRequest
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Warn("uploader: skipping malformed JSONL line", "target", t.name, "file", shortName(fpath))
			continue
		}
		if raw["type"] == "session_start" {
			continue
		}
		entry, ok := jsonlToServerEntry(raw)
		if !ok {
			slog.Warn("uploader: skipping unrecognized JSONL line", "target", t.name, "file", shortName(fpath))
			continue
		}
		entries = append(entries, entry)
	}

	remaining := entries
	if serverCount > 0 && serverCount < len(entries) {
		remaining = entries[serverCount:]
	} else if serverCount >= len(entries) {
		remaining = nil
	}

	if len(remaining) == 0 {
		slog.Info("uploader: server already has all entries", "target", t.name, "sessionID", sessionID, "count", serverCount)
	} else {
		slog.Info("uploader: uploading remaining entries",
			"target", t.name, "sessionID", sessionID,
			"serverHas", serverCount, "total", len(entries), "remaining", len(remaining))
		for i := 0; i < len(remaining); i += t.batchSize {
			end := i + t.batchSize
			if end > len(remaining) {
				end = len(remaining)
			}
			batch := remaining[i:end]
			ok, err := t.postJSON(fmt.Sprintf("/api/telemetry/session/%s/data", sessionID), batch)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
	}

	endedAt := findLastTimestamp(lines)
	ok, err = t.postJSON(
		fmt.Sprintf("/api/telemetry/session/%s/complete", sessionID),
		map[string]any{
			"endedAt":    endedAt,
			"entryCount": len(entries),
		},
	)
	if err != nil {
		return false, err
	}
	return ok, nil
}

func (t *Target) getUploadedCount(sessionID string) (int, error) {
	result, err := t.getJSON(fmt.Sprintf("/api/telemetry/session/%s", sessionID))
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, fmt.Errorf("nil response for session %s", sessionID)
	}
	count, _ := result["uploadedCount"].(float64)
	return int(count), nil
}

func (t *Target) postJSON(path string, payload any) (bool, error) {
	for attempt := 0; attempt < 2; attempt++ {
		authHeader, err := t.auth.GetAuthHeader()
		if err != nil {
			t.setLastResult(fmt.Sprintf("Auth error: %v", err))
			return false, fmt.Errorf("auth: %w", err)
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return false, fmt.Errorf("marshal payload: %w", err)
		}

		url := t.auth.BaseURL() + path
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return false, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)

		resp, err := t.httpClient.Do(req)
		if err != nil {
			t.setLastResult(fmt.Sprintf("Connection error: %v", err))
			return false, fmt.Errorf("POST %s: %w", path, err)
		}
		statusCode := resp.StatusCode
		statusText := resp.Status
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if statusCode == http.StatusUnauthorized && attempt == 0 {
			slog.Info("uploader: got 401, re-authenticating", "target", t.name, "path", path)
			t.auth.Invalidate()
			continue
		}
		if statusCode < 200 || statusCode >= 300 {
			t.setLastResult(fmt.Sprintf("HTTP %d: %s", statusCode, statusText))
			slog.Warn("uploader: server returned error", "target", t.name, "path", path, "status", statusCode)
			return false, nil
		}
		return true, nil
	}
	return false, errors.New("postJSON: retry budget exhausted")
}

func (t *Target) getJSON(path string) (map[string]any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		authHeader, err := t.auth.GetAuthHeader()
		if err != nil {
			t.setLastResult(fmt.Sprintf("Auth error: %v", err))
			return nil, fmt.Errorf("auth: %w", err)
		}

		url := t.auth.BaseURL() + path
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", authHeader)

		resp, err := t.httpClient.Do(req)
		if err != nil {
			t.setLastResult(fmt.Sprintf("GET error: %v", err))
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}

		body, readErr := io.ReadAll(resp.Body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			slog.Info("uploader: got 401 on GET, re-authenticating", "target", t.name, "path", path)
			t.auth.Invalidate()
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("read response body: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			t.setLastResult(fmt.Sprintf("GET HTTP %d: %s", resp.StatusCode, resp.Status))
			slog.Warn("uploader: GET returned error", "target", t.name, "path", path, "status", resp.StatusCode)
			return nil, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
		}

		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}
		return result, nil
	}
	return nil, errors.New("getJSON: retry budget exhausted")
}

func shortName(fpath string) string {
	for i := len(fpath) - 1; i >= 0; i-- {
		if fpath[i] == '/' {
			return fpath[i+1:]
		}
	}
	return fpath
}
