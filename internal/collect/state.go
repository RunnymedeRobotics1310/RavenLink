// Package collect holds the runtime pause flag for NT data collection
// and RavenBrain upload. This is separate from config because pause is a
// per-run toggle: it does not persist across restart, and the FMS-match
// failsafe may clear it automatically mid-session.
package collect

import "sync/atomic"

// State carries the live pause flag. Shared between the main loop (which
// drives session lifecycle), the uploader (which gates transmission), and
// the dashboard (which exposes Pause/Resume endpoints). All methods are
// safe for concurrent use.
type State struct {
	paused atomic.Bool
}

// NewState returns a State with collection initially enabled (not paused).
func NewState() *State { return &State{} }

// Paused reports whether collection is currently paused.
func (s *State) Paused() bool { return s.paused.Load() }

// Pause disables collection and upload until Resume is called (or the
// FMS-match failsafe auto-resumes).
func (s *State) Pause() { s.paused.Store(true) }

// Resume re-enables collection.
func (s *State) Resume() { s.paused.Store(false) }
