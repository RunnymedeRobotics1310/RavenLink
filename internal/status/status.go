// Package status provides a thread-safe shared status struct updated
// by all RavenLink bridge components and served to the dashboard API.
package status

import (
	"encoding/json"
	"sync"
)

const maxLogs = 100

// Status holds the live state of every RavenLink subsystem.
// All fields are protected by an embedded sync.RWMutex; callers must
// use the Update and Snapshot methods (or lock manually) to access them.
type Status struct {
	mu sync.RWMutex

	// Connections
	NTConnected  bool `json:"nt_connected"`
	OBSConnected bool `json:"obs_connected"`

	// State machine
	MatchState string `json:"match_state"`

	// Telemetry
	ActiveSessionFile string  `json:"active_session_file"`
	EntriesWritten    int     `json:"entries_written"`
	EntriesPerSecond  float64 `json:"entries_per_second"`
	SubscribedTopics  int     `json:"subscribed_topics"`

	// Upload — one entry per configured destination (ravenbrain, ravenscope).
	// Empty slice = local-only mode (no destinations). Always non-nil so
	// the dashboard JS can iterate safely.
	UploadTargets []UploadTargetStatus `json:"upload_targets"`

	// OBS
	OBSRecording bool `json:"obs_recording"`

	// Collection (NT data retrieval + upload)
	CollectTrigger string `json:"collect_trigger"`
	CollectPaused  bool   `json:"collect_paused"`
	CollectActive  bool   `json:"collect_active"`

	// Log buffer
	RecentLogs []string `json:"recent_logs"`
}

// UploadTargetStatus is the per-destination slice entry in Status.UploadTargets.
// One row per configured-and-enabled upload target. Field names are the
// dashboard JSON contract; the embedded index.html iterates this array
// to render status rows.
type UploadTargetStatus struct {
	Name               string `json:"name"`
	Enabled            bool   `json:"enabled"`
	Reachable          bool   `json:"reachable"`
	FilesPending       int    `json:"files_pending"`
	FilesUploaded      int    `json:"files_uploaded"`
	CurrentlyUploading bool   `json:"currently_uploading"`
	LastResult         string `json:"last_result"`
}

// New returns a Status initialised with idle defaults.
func New() *Status {
	return &Status{
		MatchState:    "IDLE",
		RecentLogs:    make([]string, 0, maxLogs),
		UploadTargets: []UploadTargetStatus{},
	}
}

// Update acquires a write lock and calls fn, allowing the caller to
// mutate any Status fields safely.
//
//	s.Update(func(st *Status) {
//	    st.NTConnected = true
//	    st.EntriesWritten += n
//	})
func (s *Status) Update(fn func(*Status)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

// Snapshot acquires a read lock and calls fn, allowing the caller to
// read Status fields safely.
func (s *Status) Snapshot(fn func(*Status)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s)
}

// AddLog appends a log message to the ring buffer, trimming to the
// most recent maxLogs entries. Must be called under a write lock
// (e.g., inside an Update callback).
func (s *Status) AddLog(message string) {
	s.RecentLogs = append(s.RecentLogs, message)
	if len(s.RecentLogs) > maxLogs {
		// Drop the oldest entries.
		s.RecentLogs = s.RecentLogs[len(s.RecentLogs)-maxLogs:]
	}
}

// ToJSON marshals the current status to JSON, acquiring a read lock
// for the duration of the marshal.
func (s *Status) ToJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return json.Marshal(s)
}
