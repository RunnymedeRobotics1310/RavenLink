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
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	initialBackoff = 5 * time.Second
	maxBackoff     = 60 * time.Second
)

// Uploader scans the pending/ directory for completed JSONL session files
// and uploads them to a RavenBrain server using the store-and-forward
// protocol with server-side progress tracking.
type Uploader struct {
	dataDir        string
	auth           *Auth
	batchSize      int
	uploadInterval time.Duration

	// httpClient is reused across all HTTP requests so connections can
	// be kept alive. It is safe for concurrent use.
	httpClient *http.Client

	// activeSessionFn returns the ID of the session file currently being
	// written (if any). Files containing this ID are skipped so that a
	// mid-match file is not uploaded before it is complete. May be nil.
	activeSessionFn func() string

	pendingDir  string
	uploadedDir string

	lastUploadTime time.Time
	backoff        time.Duration
	backoffUntil   time.Time

	// FilesPending is the count of JSONL files awaiting upload.
	FilesPending int
	// FilesUploaded is the total number of files successfully uploaded.
	FilesUploaded int
	// CurrentlyUploading is true while an upload is in progress.
	CurrentlyUploading bool
	// LastUploadResult describes the outcome of the most recent upload attempt.
	LastUploadResult string
}

// New creates an Uploader. dataDir must contain pending/ and uploaded/
// subdirectories (they are created if missing). activeSessionFn, if
// non-nil, is called by the upload loop to determine which pending file
// is currently being written (so it can be skipped); pass nil in tests
// or when no session is ever active.
func New(
	dataDir string,
	auth *Auth,
	batchSize int,
	uploadInterval time.Duration,
	activeSessionFn func() string,
) *Uploader {
	pendingDir := filepath.Join(dataDir, "pending")
	uploadedDir := filepath.Join(dataDir, "uploaded")

	for _, d := range []string{pendingDir, uploadedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			slog.Error("uploader: failed to create directory", "path", d, "err", err)
		}
	}

	return &Uploader{
		dataDir:         dataDir,
		auth:            auth,
		batchSize:       batchSize,
		uploadInterval:  uploadInterval,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		activeSessionFn: activeSessionFn,
		pendingDir:      pendingDir,
		uploadedDir:     uploadedDir,
	}
}

// currentActiveSessionID returns the currently-active session ID via
// the configured callback, or "" if no callback was provided.
func (u *Uploader) currentActiveSessionID() string {
	if u.activeSessionFn == nil {
		return ""
	}
	return u.activeSessionFn()
}

// Run starts the upload loop, checking for pending files on each tick.
// It runs in its own goroutine so slow HTTP calls do not block the main
// state-machine loop. It returns when ctx is cancelled.
func (u *Uploader) Run(ctx context.Context) {
	slog.Info("uploader: running", "dataDir", u.dataDir, "interval", u.uploadInterval)

	ticker := time.NewTicker(u.uploadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("uploader: shutting down")
			return
		case <-ticker.C:
			u.MaybeUpload(u.currentActiveSessionID())
		}
	}
}

// MaybeUpload checks for pending files and uploads the oldest one,
// skipping any file whose name contains activeSessionID (the currently
// recording session). It respects the upload interval and backoff timers.
func (u *Uploader) MaybeUpload(activeSessionID string) {
	if !u.auth.IsConfigured() {
		return
	}

	now := time.Now()

	if now.Before(u.backoffUntil) {
		return
	}

	pending := u.getPendingFiles(activeSessionID)
	u.FilesPending = len(pending)

	if len(pending) == 0 {
		return
	}

	fpath := pending[0]
	u.CurrentlyUploading = true
	defer func() { u.CurrentlyUploading = false }()

	ok, err := u.uploadFile(fpath)
	if err != nil {
		slog.Warn("uploader: upload failed", "file", filepath.Base(fpath), "err", err)
		u.LastUploadResult = fmt.Sprintf("ERROR: %v", err)
		u.applyBackoff()
		return
	}
	if ok {
		u.moveToUploaded(fpath)
		u.FilesUploaded++
		u.FilesPending = max(0, u.FilesPending-1)
		u.LastUploadResult = fmt.Sprintf("OK: %s", filepath.Base(fpath))
		u.backoff = 0
		slog.Info("uploader: uploaded", "file", filepath.Base(fpath))
	} else {
		u.applyBackoff()
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

// getPendingFiles returns JSONL files in pending/ sorted oldest-first,
// excluding any file whose name contains activeSessionID.
func (u *Uploader) getPendingFiles(activeSessionID string) []string {
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

// uploadFile uploads a single JSONL file to RavenBrain using the
// session protocol: POST /session -> GET /session/{id} -> POST /data
// (batched) -> POST /complete. Returns (true, nil) on success.
func (u *Uploader) uploadFile(fpath string) (bool, error) {
	data, err := os.ReadFile(fpath)
	if err != nil {
		return false, fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		slog.Warn("uploader: empty JSONL file", "file", filepath.Base(fpath))
		return true, nil
	}

	// Parse session_start line.
	sessionMeta := parseSessionStart(lines)
	if sessionMeta == nil {
		slog.Warn("uploader: no session_start found, skipping", "file", filepath.Base(fpath))
		return true, nil
	}

	sessionID, _ := sessionMeta["session_id"].(string)
	if sessionID == "" {
		slog.Warn("uploader: session_start has no session_id, skipping", "file", filepath.Base(fpath))
		return true, nil
	}

	// Step 1: Create or resume session (idempotent).
	teamNum, _ := sessionMeta["team"].(float64)
	robotIP, _ := sessionMeta["robot_ip"].(string)
	startedAt := sessionMeta["ts"]

	ok, err := u.postJSON("/api/telemetry/session", map[string]any{
		"sessionId":  sessionID,
		"teamNumber": teamNum,
		"robotIp":    robotIP,
		"startedAt":  startedAt,
	})
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	// Step 2: Ask server how many entries it already has.
	serverCount, err := u.getUploadedCount(sessionID)
	if err != nil {
		return false, err
	}

	// Collect all data entries (skip session_start).
	var entries []json.RawMessage
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var peek map[string]any
		if err := json.Unmarshal([]byte(line), &peek); err != nil {
			slog.Warn("uploader: skipping malformed JSONL line", "file", filepath.Base(fpath))
			continue
		}
		if peek["type"] == "session_start" {
			continue
		}
		entries = append(entries, json.RawMessage(line))
	}

	// Step 3: Skip entries the server already has, upload remaining in batches.
	remaining := entries
	if serverCount > 0 && serverCount < len(entries) {
		remaining = entries[serverCount:]
	} else if serverCount >= len(entries) {
		remaining = nil
	}

	if len(remaining) == 0 {
		slog.Info("uploader: server already has all entries",
			"sessionID", sessionID, "count", serverCount)
	} else {
		slog.Info("uploader: uploading remaining entries",
			"sessionID", sessionID,
			"serverHas", serverCount,
			"total", len(entries),
			"remaining", len(remaining),
		)
		for i := 0; i < len(remaining); i += u.batchSize {
			end := i + u.batchSize
			if end > len(remaining) {
				end = len(remaining)
			}
			batch := remaining[i:end]

			ok, err := u.postJSON(
				fmt.Sprintf("/api/telemetry/session/%s/data", sessionID),
				batch,
			)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
	}

	// Step 4: Complete the session.
	endedAt := findLastTimestamp(lines)

	ok, err = u.postJSON(
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

// findLastTimestamp returns the "ts" field from the last non-empty JSONL line.
func findLastTimestamp(lines []string) any {
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
			return ts
		}
	}
	return ""
}

// getUploadedCount asks the server how many entries it already has for a session.
func (u *Uploader) getUploadedCount(sessionID string) (int, error) {
	result, err := u.getJSON(fmt.Sprintf("/api/telemetry/session/%s", sessionID))
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, fmt.Errorf("nil response for session %s", sessionID)
	}

	count, _ := result["uploadedCount"].(float64)
	return int(count), nil
}

// postJSON sends a POST request with a JSON body to the RavenBrain server.
// It retries once on 401 (after invalidating auth). Returns (true, nil)
// on a 2xx response.
func (u *Uploader) postJSON(path string, payload any) (bool, error) {
	for attempt := 0; attempt < 2; attempt++ {
		authHeader, err := u.auth.GetAuthHeader()
		if err != nil {
			u.LastUploadResult = fmt.Sprintf("Auth error: %v", err)
			return false, fmt.Errorf("auth: %w", err)
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return false, fmt.Errorf("marshal payload: %w", err)
		}

		url := u.auth.BaseURL() + path
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return false, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader)

		resp, err := u.httpClient.Do(req)
		if err != nil {
			u.LastUploadResult = fmt.Sprintf("Connection error: %v", err)
			return false, fmt.Errorf("POST %s: %w", path, err)
		}
		// Drain + close so the underlying connection can be reused
		// by http.Client's keep-alive pool.
		statusCode := resp.StatusCode
		statusText := resp.Status
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if statusCode == http.StatusUnauthorized && attempt == 0 {
			slog.Info("uploader: got 401, re-authenticating", "path", path)
			u.auth.Invalidate()
			continue
		}

		if statusCode < 200 || statusCode >= 300 {
			u.LastUploadResult = fmt.Sprintf("HTTP %d: %s", statusCode, statusText)
			slog.Warn("uploader: server returned error",
				"path", path, "status", statusCode)
			return false, nil
		}

		return true, nil
	}
	return false, errors.New("postJSON: retry budget exhausted")
}

// getJSON sends a GET request and parses the JSON response body.
// It retries once on 401.
func (u *Uploader) getJSON(path string) (map[string]any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		authHeader, err := u.auth.GetAuthHeader()
		if err != nil {
			u.LastUploadResult = fmt.Sprintf("Auth error: %v", err)
			return nil, fmt.Errorf("auth: %w", err)
		}

		url := u.auth.BaseURL() + path
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", authHeader)

		resp, err := u.httpClient.Do(req)
		if err != nil {
			u.LastUploadResult = fmt.Sprintf("GET error: %v", err)
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}

		// Read, drain, and close the body in one shot so the connection
		// can be reused by http.Client's keep-alive pool.
		body, readErr := io.ReadAll(resp.Body)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			slog.Info("uploader: got 401 on GET, re-authenticating", "path", path)
			u.auth.Invalidate()
			continue
		}

		if readErr != nil {
			return nil, fmt.Errorf("read response body: %w", readErr)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			u.LastUploadResult = fmt.Sprintf("GET HTTP %d: %s", resp.StatusCode, resp.Status)
			slog.Warn("uploader: GET returned error",
				"path", path, "status", resp.StatusCode)
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

// moveToUploaded moves a file from pending/ to uploaded/.
func (u *Uploader) moveToUploaded(fpath string) {
	dest := filepath.Join(u.uploadedDir, filepath.Base(fpath))
	if err := os.Rename(fpath, dest); err != nil {
		slog.Warn("uploader: failed to move file to uploaded", "file", filepath.Base(fpath), "err", err)
	}
}

// applyBackoff doubles the current backoff (starting from initialBackoff)
// up to maxBackoff.
func (u *Uploader) applyBackoff() {
	if u.backoff == 0 {
		u.backoff = initialBackoff
	} else {
		u.backoff = min(u.backoff*2, maxBackoff)
	}
	u.backoffUntil = time.Now().Add(u.backoff)
	slog.Info("uploader: backing off", "duration", u.backoff)
}
