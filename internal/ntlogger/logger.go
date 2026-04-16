// Package ntlogger reads TopicValue updates from an NT4 client channel
// and writes them to JSONL session files for later upload.
//
// The Logger is structured as an actor: a single goroutine (Run) owns all
// mutable state (the open file, buffered writer, counters) and receives
// commands (StartSession, EndSession, RecordMatchEvent) via an internal
// command channel. Values come in through the ntclient.TopicValue channel.
// External callers never touch internal state directly; they push commands
// or read a snapshot via Stats().
package ntlogger

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/statemachine"
)

// syncInterval is how often the buffered writer is fsynced while a session
// is open. Individual writes are only Flush()'d to the OS; Sync() is
// reserved for boundaries and this ticker.
const syncInterval = 2 * time.Second

// commandBuffer is the capacity of the internal command channel. Commands
// are rare (session start/end, match events) so a small buffer is plenty.
const commandBuffer = 8

// Stats is a point-in-time snapshot of logger counters, safe to share
// across goroutines.
type Stats struct {
	EntriesWritten  int
	ActiveSessionID string
}

// commandKind identifies the type of command sent to the actor loop.
type commandKind int

const (
	cmdStartSession commandKind = iota
	cmdEndSession
	cmdRecordMatchEvent
)

// command is a message sent from external callers to the actor loop.
type command struct {
	kind      commandKind
	eventType string                 // for cmdRecordMatchEvent
	fms       statemachine.FMSState  // for cmdRecordMatchEvent
}

// Logger reads from a TopicValue channel and writes JSONL session files
// into dataDir/pending/. All mutable state is owned by the Run goroutine;
// use StartSession/EndSession/RecordMatchEvent to mutate and Stats() to
// read.
type Logger struct {
	valuesCh   <-chan ntclient.TopicValue
	dataDir    string
	team       int
	robotIP    string
	pendingDir string

	cmdCh chan command

	// Actor-owned state. Only the Run goroutine touches these.
	file          *os.File
	buf           *bufio.Writer
	entriesInFile int // local mirror for writing the session_end trailer

	// latestValues holds the most recent value for every topic seen
	// since the NT connection was established. When a new session
	// starts, all buffered values are replayed into the file so that
	// one-shot topics (/.schema/*, /FMSInfo/MatchNumber, etc.) that
	// were published before the collect trigger fired are captured.
	latestValues map[string]ntclient.TopicValue

	// Cross-goroutine counters. entriesWritten is an atomic so rate readers
	// never race with the writer. activeSessionID is guarded by sidMu.
	entriesWritten atomic.Int64

	sidMu           sync.RWMutex
	activeSessionID string
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
		valuesCh:     valuesCh,
		dataDir:      dataDir,
		team:         team,
		robotIP:      fmt.Sprintf("10.%d.%d.2", te, am),
		pendingDir:   pendingDir,
		cmdCh:        make(chan command, commandBuffer),
		latestValues: make(map[string]ntclient.TopicValue),
	}
}

// Run is the actor loop. It owns all mutable state and services values,
// commands, the periodic sync ticker, and context cancellation. It returns
// when ctx is cancelled or the values channel is closed.
func (l *Logger) Run(ctx context.Context) {
	slog.Info("ntlogger: running", "dataDir", l.dataDir, "team", l.team)

	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	defer l.endSessionLocked()

	for {
		select {
		case <-ctx.Done():
			slog.Info("ntlogger: shutting down")
			return

		case cmd := <-l.cmdCh:
			l.handleCommand(cmd)

		case tv, ok := <-l.valuesCh:
			if !ok {
				slog.Info("ntlogger: values channel closed")
				return
			}
			l.handleValue(tv)

		case <-ticker.C:
			l.periodicSync()
		}
	}
}

// StartSession asks the actor to close any open session and open a new
// JSONL file. Non-blocking: if the command buffer is full the command is
// dropped with a warning (this should be exceedingly rare).
func (l *Logger) StartSession() {
	l.sendCommand(command{kind: cmdStartSession})
}

// EndSession asks the actor to flush, fsync, and close the current
// session file. Safe to call even if no session is active.
func (l *Logger) EndSession() {
	l.sendCommand(command{kind: cmdEndSession})
}

// RecordMatchEvent asks the actor to write a match marker into the
// current session file. eventType is typically "match_start" or "match_end".
func (l *Logger) RecordMatchEvent(eventType string, fms statemachine.FMSState) {
	l.sendCommand(command{
		kind:      cmdRecordMatchEvent,
		eventType: eventType,
		fms:       fms,
	})
}

// Stats returns a snapshot of logger counters. Safe to call from any
// goroutine.
func (l *Logger) Stats() Stats {
	l.sidMu.RLock()
	sid := l.activeSessionID
	l.sidMu.RUnlock()
	return Stats{
		EntriesWritten:  int(l.entriesWritten.Load()),
		ActiveSessionID: sid,
	}
}

// sendCommand pushes a command onto the actor channel without blocking.
// Commands that can't be delivered are logged; losing a session lifecycle
// command would be bad but buffer=8 plus rare senders makes this unlikely.
func (l *Logger) sendCommand(c command) {
	select {
	case l.cmdCh <- c:
	default:
		slog.Warn("ntlogger: command channel full, dropping command", "kind", c.kind)
	}
}

// handleCommand dispatches a command inside the actor goroutine.
func (l *Logger) handleCommand(c command) {
	switch c.kind {
	case cmdStartSession:
		l.startSessionLocked()
	case cmdEndSession:
		l.endSessionLocked()
	case cmdRecordMatchEvent:
		l.recordMatchEventLocked(c.eventType, c.fms)
	}
}

// handleValue buffers the latest value for each topic (so one-shot
// topics can be replayed at session start) and writes it to the
// current JSONL file if one is open.
// Called only from the Run goroutine.
func (l *Logger) handleValue(tv ntclient.TopicValue) {
	// Always buffer the latest value regardless of session state.
	l.latestValues[tv.Name] = tv

	if l.file == nil {
		return
	}

	l.writeDataEntry(tv)
}

// writeDataEntry writes a single TopicValue as a JSONL data line.
func (l *Logger) writeDataEntry(tv ntclient.TopicValue) {
	entry := map[string]any{
		"ts":        unixNow(),
		"server_ts": tv.ServerTimeMicros,
		"key":       tv.Name,
		"type":      tv.Type,
		"value":     coerceValue(tv.Type, tv.Value),
	}

	l.writeLine(entry)
	l.entriesInFile++
	l.entriesWritten.Add(1)
}

// startSessionLocked is the actor-side implementation of StartSession.
// Called only from the Run goroutine.
func (l *Logger) startSessionLocked() {
	if l.file != nil {
		l.endSessionLocked()
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
	l.buf = bufio.NewWriterSize(f, 64*1024)
	l.entriesInFile = 0
	l.entriesWritten.Store(0)
	l.setActiveSessionID(sessionID)

	l.writeLine(map[string]any{
		"type":       "session_start",
		"ts":         unixNow(),
		"team":       l.team,
		"robot_ip":   l.robotIP,
		"session_id": sessionID,
	})

	// Replay buffered values so the session file contains a complete
	// state snapshot. This captures one-shot topics (/.schema/*,
	// /FMSInfo/MatchNumber, etc.) that were published before the
	// collect trigger fired.
	replayed := 0
	for _, tv := range l.latestValues {
		l.writeDataEntry(tv)
		replayed++
	}
	if replayed > 0 {
		slog.Info("ntlogger: replayed buffered values into new session", "count", replayed)
	}

	// session_start is a boundary — make sure it hits disk.
	l.flushAndSync()

	slog.Info("ntlogger: session started", "file", filename, "sessionID", sessionID)
}

// endSessionLocked is the actor-side implementation of EndSession.
// Called only from the Run goroutine (and from Run's defer on shutdown).
func (l *Logger) endSessionLocked() {
	if l.file == nil {
		return
	}

	sessionID := l.currentSessionID()

	l.writeLine(map[string]any{
		"type":            "session_end",
		"ts":              unixNow(),
		"session_id":      sessionID,
		"entries_written": l.entriesInFile,
	})

	// Flush the buffered writer, fsync, then close.
	if l.buf != nil {
		if err := l.buf.Flush(); err != nil {
			slog.Warn("ntlogger: buffer flush error on EndSession", "err", err)
		}
		l.buf = nil
	}
	if err := l.file.Sync(); err != nil {
		slog.Warn("ntlogger: fsync error on EndSession", "err", err)
	}
	if err := l.file.Close(); err != nil {
		slog.Warn("ntlogger: error closing session file", "err", err)
	}
	l.file = nil

	slog.Info("ntlogger: session ended", "sessionID", sessionID, "entries", l.entriesInFile)
	l.setActiveSessionID("")
}

// recordMatchEventLocked writes a match marker into the current session
// file. Called only from the Run goroutine.
func (l *Logger) recordMatchEventLocked(eventType string, fms statemachine.FMSState) {
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
	// Match boundaries are important — make sure they hit disk.
	l.flushAndSync()
	slog.Info("ntlogger: match event recorded", "type", eventType)
}

// periodicSync is called on the sync ticker. It flushes the buffer to the
// OS and fsyncs the file if a session is open.
func (l *Logger) periodicSync() {
	if l.file == nil {
		return
	}
	l.flushAndSync()
}

// flushAndSync flushes the bufio.Writer and fsyncs the underlying file.
// Only call when l.file != nil.
func (l *Logger) flushAndSync() {
	if l.buf != nil {
		if err := l.buf.Flush(); err != nil {
			slog.Warn("ntlogger: buffer flush error", "err", err)
			return
		}
	}
	if err := l.file.Sync(); err != nil {
		slog.Warn("ntlogger: fsync error", "err", err)
	}
}

// writeLine serialises obj as compact JSON and writes it as one line to
// the session file's buffered writer, followed by a newline. This flushes
// only the bufio.Writer (cheap); fsync happens on the ticker or at
// boundaries.
func (l *Logger) writeLine(obj map[string]any) {
	if l.buf == nil {
		return
	}

	data, err := json.Marshal(obj)
	if err != nil {
		slog.Warn("ntlogger: json marshal error", "err", err)
		return
	}

	data = append(data, '\n')
	if _, err := l.buf.Write(data); err != nil {
		slog.Warn("ntlogger: write error", "err", err)
		return
	}
}

// setActiveSessionID updates the shared session ID under the RWMutex.
// Called only from the Run goroutine.
func (l *Logger) setActiveSessionID(id string) {
	l.sidMu.Lock()
	l.activeSessionID = id
	l.sidMu.Unlock()
}

// currentSessionID returns the session ID without locking — the actor
// goroutine is the sole writer so it can read its own value directly.
// We still go through the mutex to satisfy the race detector, because
// other goroutines may be reading at the same time.
func (l *Logger) currentSessionID() string {
	l.sidMu.RLock()
	defer l.sidMu.RUnlock()
	return l.activeSessionID
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
