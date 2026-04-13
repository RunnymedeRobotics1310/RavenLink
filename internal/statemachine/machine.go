package statemachine

import "time"

// State represents the current state of the match recording state machine.
type State int

const (
	Idle          State = iota // Not recording
	RecordingAuto              // Recording the autonomous period
	RecordingTeleop            // Recording the teleoperated period
	StopPending                // Waiting for stop delay to elapse
)

// Action is a command the state machine emits for the caller to execute.
type Action int

const (
	StartRecord Action = iota
	StopRecord
)

// Clock is an injectable time source. It returns the current time as seconds
// (monotonic). The default uses time.Now().
type Clock func() float64

func defaultClock() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// Machine determines recording actions from FMS state transitions.
//
// It is pure logic — no OBS or NetworkTables calls. All time comes from an
// injectable Clock.
type Machine struct {
	State State

	stopDelay          float64
	autoTeleopGap      float64
	ntDisconnectGrace  float64
	recordTrigger      string
	clock              Clock

	disabledAt        *float64
	stopPendingAt     *float64
	ntDisconnectedAt  *float64
	wasEnabled        bool
}

// Option configures a Machine.
type Option func(*Machine)

// WithStopDelay sets how long (seconds) after a match ends before the
// recording is stopped. Default 10.
func WithStopDelay(d float64) Option {
	return func(m *Machine) { m.stopDelay = d }
}

// WithAutoTeleopGap sets the maximum tolerated disabled gap (seconds) between
// the autonomous and teleoperated periods. Default 5.
func WithAutoTeleopGap(g float64) Option {
	return func(m *Machine) { m.autoTeleopGap = g }
}

// WithNTDisconnectGrace sets how long (seconds) a NetworkTables disconnect is
// tolerated before the recording is stopped. Default 15.
func WithNTDisconnectGrace(g float64) Option {
	return func(m *Machine) { m.ntDisconnectGrace = g }
}

// WithRecordTrigger sets the trigger mode: "fms" (default), "auto", or "any".
//   - "fms":  recording starts when enabled && FMS attached
//   - "auto": recording starts when enabled && auto mode
//   - "any":  recording starts when enabled (regardless of FMS/auto)
func WithRecordTrigger(t string) Option {
	return func(m *Machine) { m.recordTrigger = t }
}

// WithClock injects a custom time source (useful for testing).
func WithClock(c Clock) Option {
	return func(m *Machine) { m.clock = c }
}

// NewMachine creates a new state machine with the given options.
func NewMachine(opts ...Option) *Machine {
	m := &Machine{
		State:             Idle,
		stopDelay:         10.0,
		autoTeleopGap:     5.0,
		ntDisconnectGrace: 15.0,
		recordTrigger:     "fms",
		clock:             defaultClock,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func (m *Machine) now() float64 {
	return m.clock()
}

// shouldStartRecording decides whether the given FMS state should trigger a
// new recording, according to the configured record trigger mode.
func (m *Machine) shouldStartRecording(fms FMSState) bool {
	if !fms.Enabled {
		return false
	}
	switch m.recordTrigger {
	case "fms":
		return fms.FMSAttached
	case "auto":
		return fms.AutoMode
	default: // "any"
		return true
	}
}

// Reset returns the machine to Idle and clears every internal timer. Use
// this when an external signal (e.g. a user pause) forces the machine to
// abandon its current session regardless of FMS state.
func (m *Machine) Reset() { m.reset() }

func (m *Machine) reset() {
	m.State = Idle
	m.disabledAt = nil
	m.stopPendingAt = nil
	m.ntDisconnectedAt = nil
	m.wasEnabled = false
}

func ptrFloat64(v float64) *float64 {
	return &v
}

// Update processes one FMS state tick and returns any actions that should be
// executed (start or stop recording).
func (m *Machine) Update(fms FMSState) []Action {
	now := m.now()
	var actions []Action

	// --- Handle NT disconnect (raw == -1) ---
	if fms.Raw < 0 {
		if m.State != Idle {
			if m.ntDisconnectedAt == nil {
				m.ntDisconnectedAt = ptrFloat64(now)
			} else if now-*m.ntDisconnectedAt > m.ntDisconnectGrace {
				actions = append(actions, StopRecord)
				m.reset()
			}
		}
		return actions
	}

	// NT is connected — clear disconnect timer.
	m.ntDisconnectedAt = nil

	// --- Handle FMS detach while recording (only in fms trigger mode) ---
	if m.recordTrigger == "fms" && !fms.FMSAttached && m.State != Idle {
		if m.State != StopPending {
			m.State = StopPending
			m.stopPendingAt = ptrFloat64(now - m.stopDelay + 3.0) // expires in 3s
		}
		// Fall through to StopPending handler to check the timer.
	}

	// --- Handle E-stop ---
	if fms.EStop && (m.State == RecordingAuto || m.State == RecordingTeleop) {
		m.State = StopPending
		m.stopPendingAt = ptrFloat64(now)
		m.disabledAt = ptrFloat64(now)
	}

	// --- State transitions ---
	switch m.State {
	case Idle:
		if m.shouldStartRecording(fms) {
			m.State = RecordingAuto
			m.wasEnabled = true
			m.disabledAt = nil
			actions = append(actions, StartRecord)
		}

	case RecordingAuto:
		if fms.Enabled {
			m.wasEnabled = true
			m.disabledAt = nil
			// Check transition to teleop: enabled + not auto + not test.
			if !fms.AutoMode && !fms.TestMode {
				m.State = RecordingTeleop
			}
		} else {
			// Disabled during auto — start gap timer.
			if m.disabledAt == nil {
				m.disabledAt = ptrFloat64(now)
			} else if now-*m.disabledAt > m.autoTeleopGap {
				// Gap too long — not an auto-to-teleop transition.
				m.State = StopPending
				m.stopPendingAt = ptrFloat64(now)
			}
		}

	case RecordingTeleop:
		if fms.Enabled {
			m.wasEnabled = true
			m.disabledAt = nil
		} else {
			// Disabled during teleop — match end.
			if m.disabledAt == nil {
				m.disabledAt = ptrFloat64(now)
				m.State = StopPending
				m.stopPendingAt = ptrFloat64(now)
			}
		}

	case StopPending:
		// Re-enable cancels the stop.
		if m.shouldStartRecording(fms) {
			if fms.AutoMode {
				m.State = RecordingAuto
			} else {
				m.State = RecordingTeleop
			}
			m.stopPendingAt = nil
			m.disabledAt = nil
		} else if m.stopPendingAt != nil && now-*m.stopPendingAt >= m.stopDelay {
			actions = append(actions, StopRecord)
			m.reset()
		}
	}

	return actions
}
