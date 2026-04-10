// Package ntlogger reads TopicValue updates from an NT4 client channel
// and writes them to JSONL session files for later upload.
package ntlogger

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/statemachine"
)

// Logger reads from a TopicValue channel and writes JSONL session files
// into dataDir/pending/.
type Logger struct {
	valuesCh   <-chan ntclient.TopicValue
	dataDir    string
	team       int
	robotIP    string
	pendingDir string

	file *os.File

	// EntriesWritten is the number of data entries written in the current session.
	EntriesWritten int
	// ActiveSessionID is the hex ID of the current open session, or empty.
	ActiveSessionID string
}

// New creates a Logger that reads from valuesCh and writes JSONL files
// into dataDir/pending/. The team number is recorded in session metadata.
func New(valuesCh <-chan ntclient.TopicValue, dataDir string, team int) *Logger {
	te := team / 100
	am := team % 100

	pendingDir := filepath.Join(dataDir, "pending")
	if err := os.MkdirAll(pendingDir, 0o755); err != nil {
		slog.Error("ntlogger: failed to create pending dir", "path", pendingDir, "err", err)
	}

	return &Logger{
		valuesCh:   valuesCh,
		dataDir:    dataDir,
		team:       team,
		robotIP:    fmt.Sprintf("10.%d.%d.2", te, am),
		pendingDir: pendingDir,
	}
}

// Run is the main loop. It reads TopicValue updates from the channel and
// writes them to the current session file. It returns when ctx is cancelled
// or the channel is closed.
func (l *Logger) Run(ctx context.Context) {
	slog.Info("ntlogger: running", "dataDir", l.dataDir, "team", l.team)
	defer l.EndSession()

	for {
		select {
		case <-ctx.Done():
			slog.Info("ntlogger: shutting down")
			return

		case tv, ok := <-l.valuesCh:
			if !ok {
				slog.Info("ntlogger: values channel closed")
				return
			}
			l.handleValue(tv)
		}
	}
}

// handleValue writes a single TopicValue to the current JSONL file.
func (l *Logger) handleValue(tv ntclient.TopicValue) {
	if l.file == nil {
		return
	}

	entry := map[string]any{
		"ts":        unixNow(),
		"server_ts": tv.ServerTimeMicros,
		"key":       tv.Name,
		"type":      tv.Type,
		"value":     coerceValue(tv.Type, tv.Value),
	}

	l.writeLine(entry)
	l.EntriesWritten++
}

// StartSession closes any active session and opens a new JSONL file in
// the pending directory with a session_start header line.
func (l *Logger) StartSession() {
	if l.file != nil {
		l.EndSession()
	}

	sessionID := randomHex8()
	ts := time.Now().UTC().Format("20060102T150405Z")
	filename := fmt.Sprintf("%s_%s.jsonl", ts, sessionID)
	fpath := filepath.Join(l.pendingDir, filename)

	f, err := os.Create(fpath)
	if err != nil {
		slog.Error("ntlogger: failed to create session file", "path", fpath, "err", err)
		return
	}

	l.file = f
	l.ActiveSessionID = sessionID
	l.EntriesWritten = 0

	l.writeLine(map[string]any{
		"type":       "session_start",
		"ts":         unixNow(),
		"team":       l.team,
		"robot_ip":   l.robotIP,
		"session_id": sessionID,
	})

	slog.Info("ntlogger: session started", "file", filename, "sessionID", sessionID)
}

// EndSession writes a session_end line and closes the current file.
// It is safe to call when no session is active.
func (l *Logger) EndSession() {
	if l.file == nil {
		return
	}

	sessionID := l.ActiveSessionID

	l.writeLine(map[string]any{
		"type":            "session_end",
		"ts":              unixNow(),
		"session_id":      sessionID,
		"entries_written": l.EntriesWritten,
	})

	if err := l.file.Close(); err != nil {
		slog.Warn("ntlogger: error closing session file", "err", err)
	}
	l.file = nil

	slog.Info("ntlogger: session ended", "sessionID", sessionID, "entries", l.EntriesWritten)
	l.ActiveSessionID = ""
}

// RecordMatchEvent writes a match_start or match_end marker line with
// FMS metadata into the current session file.
func (l *Logger) RecordMatchEvent(eventType string, fms statemachine.FMSState) {
	entry := map[string]any{
		"type": eventType,
		"ts":   unixNow(),
	}

	if eventType == "match_start" {
		// Include FMS info fields in match_start markers. These will be
		// populated by the caller from the latest NT snapshot if available.
		// We record the FMS state flags directly since we don't have access
		// to individual FMSInfo table entries like the Python version does
		// via ntcore — the Go NT4 client delivers values through a channel.
		entry["fms_enabled"] = fms.Enabled
		entry["fms_auto"] = fms.AutoMode
		entry["fms_test"] = fms.TestMode
		entry["fms_estop"] = fms.EStop
		entry["fms_attached"] = fms.FMSAttached
		entry["ds_attached"] = fms.DSAttached
	}

	entry["fms_state"] = fms.String()

	l.writeLine(entry)
	slog.Info("ntlogger: match event recorded", "type", eventType)
}

// writeLine serialises obj as compact JSON and writes it as one line to
// the session file, followed by a newline. The file is flushed (synced)
// after every write.
func (l *Logger) writeLine(obj map[string]any) {
	if l.file == nil {
		return
	}

	data, err := json.Marshal(obj)
	if err != nil {
		slog.Warn("ntlogger: json marshal error", "err", err)
		return
	}

	data = append(data, '\n')
	if _, err := l.file.Write(data); err != nil {
		slog.Warn("ntlogger: write error", "err", err)
		return
	}
	// Best-effort sync so data is on disk promptly.
	_ = l.file.Sync()
}

// coerceValue converts a TopicValue's value to a JSON-safe representation.
// Raw byte slices are base64-encoded; everything else passes through.
func coerceValue(typeName string, v any) any {
	if typeName == "raw" {
		switch b := v.(type) {
		case []byte:
			return base64.StdEncoding.EncodeToString(b)
		default:
			return fmt.Sprintf("%v", v)
		}
	}
	return v
}

// unixNow returns the current time as a Unix timestamp with sub-second
// precision (float64).
func unixNow() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// randomHex8 returns 8 hex characters (4 random bytes).
func randomHex8() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use timestamp nanos.
		return fmt.Sprintf("%08x", time.Now().UnixNano()&0xFFFFFFFF)
	}
	return hex.EncodeToString(b)
}
