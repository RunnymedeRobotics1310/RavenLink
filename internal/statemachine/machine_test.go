package statemachine

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// FakeClock is an injectable clock for testing time-dependent state machine logic.
type FakeClock struct {
	Time float64
}

func NewFakeClock(start float64) *FakeClock {
	return &FakeClock{Time: start}
}

func (c *FakeClock) Now() float64 {
	return c.Time
}

func (c *FakeClock) Advance(seconds float64) {
	c.Time += seconds
}

// makeFMS constructs an FMSState from individual flag booleans.
func makeFMS(enabled, auto, test, estop, fms, ds bool) FMSState {
	raw := 0
	if enabled {
		raw |= 0x01
	}
	if auto {
		raw |= 0x02
	}
	if test {
		raw |= 0x04
	}
	if estop {
		raw |= 0x08
	}
	if fms {
		raw |= 0x10
	}
	if ds {
		raw |= 0x20
	}
	return FMSStateFromRaw(raw)
}

// assertState is a test helper that fails with a clear message.
func assertState(t *testing.T, m *Machine, want State) {
	t.Helper()
	if m.State != want {
		t.Fatalf("state: got %d, want %d", m.State, want)
	}
}

// assertActions is a test helper that compares action slices.
func assertActions(t *testing.T, got []Action, want ...Action) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("actions: got %v (len %d), want %v (len %d)", got, len(got), want, len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("actions[%d]: got %d, want %d", i, got[i], want[i])
		}
	}
}

func assertNoActions(t *testing.T, got []Action) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("expected no actions, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// TestFullMatchLifecycle
// ---------------------------------------------------------------------------

func TestFullMatchLifecycle(t *testing.T) {
	t.Run("normal_match", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(10.0), WithAutoTeleopGap(5.0), WithClock(clock.Now))

		assertState(t, sm, Idle)

		// 1. Auto start — FMS attached + enabled + auto
		actions := sm.Update(makeFMS(true, true, false, false, true, true))
		assertActions(t, actions, StartRecord)
		assertState(t, sm, RecordingAuto)

		// 2. Stay in auto for a while
		clock.Advance(15.0)
		actions = sm.Update(makeFMS(true, true, false, false, true, true))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingAuto)

		// 3. Auto ends — brief disabled gap
		clock.Advance(0.1)
		actions = sm.Update(makeFMS(false, false, false, false, true, true))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingAuto) // still in auto, gap tolerance

		// 4. Teleop starts — enabled, no auto
		clock.Advance(1.0)
		actions = sm.Update(makeFMS(true, false, false, false, true, true))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingTeleop)

		// 5. Teleop runs for a while
		clock.Advance(120.0)
		actions = sm.Update(makeFMS(true, false, false, false, true, true))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingTeleop)

		// 6. Match ends — disabled
		clock.Advance(0.1)
		actions = sm.Update(makeFMS(false, false, false, false, true, true))
		assertNoActions(t, actions)
		assertState(t, sm, StopPending)

		// 7. Wait for stop delay
		clock.Advance(9.0)
		actions = sm.Update(makeFMS(false, false, false, false, true, true))
		assertNoActions(t, actions)
		assertState(t, sm, StopPending)

		clock.Advance(2.0)
		actions = sm.Update(makeFMS(false, false, false, false, true, true))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})
}

// ---------------------------------------------------------------------------
// TestAutoTeleopGap
// ---------------------------------------------------------------------------

func TestAutoTeleopGap(t *testing.T) {
	t.Run("short_gap_tolerated", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithAutoTeleopGap(5.0), WithClock(clock.Now))

		// Start auto
		sm.Update(makeFMS(true, true, false, false, true, false))
		assertState(t, sm, RecordingAuto)

		// Disabled for 2 seconds
		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		assertState(t, sm, RecordingAuto)

		clock.Advance(2.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		assertState(t, sm, RecordingAuto) // still tolerating

		// Teleop starts
		clock.Advance(0.5)
		actions := sm.Update(makeFMS(true, false, false, false, true, false))
		assertState(t, sm, RecordingTeleop)
		assertNoActions(t, actions)
	})

	t.Run("long_gap_triggers_stop", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithAutoTeleopGap(5.0), WithStopDelay(10.0), WithClock(clock.Now))

		sm.Update(makeFMS(true, true, false, false, true, false))
		assertState(t, sm, RecordingAuto)

		// Disabled for longer than gap tolerance
		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(6.0)
		sm.Update(makeFMS(false, false, false, false, true, false))

		assertState(t, sm, StopPending)
	})
}

// ---------------------------------------------------------------------------
// TestStopDelay
// ---------------------------------------------------------------------------

func TestStopDelay(t *testing.T) {
	t.Run("stop_fires_after_delay", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(10.0), WithClock(clock.Now))

		// Get to teleop
		sm.Update(makeFMS(true, true, false, false, true, false))
		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(1.0)
		sm.Update(makeFMS(true, false, false, false, true, false)) // teleop
		assertState(t, sm, RecordingTeleop)

		// Match end
		clock.Advance(120.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		assertState(t, sm, StopPending)

		// Not yet
		clock.Advance(5.0)
		actions := sm.Update(makeFMS(false, false, false, false, true, false))
		assertNoActions(t, actions)
		assertState(t, sm, StopPending)

		// Now
		clock.Advance(6.0)
		actions = sm.Update(makeFMS(false, false, false, false, true, false))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})

	t.Run("custom_stop_delay", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(3.0), WithClock(clock.Now))

		sm.Update(makeFMS(true, true, false, false, true, false))
		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(1.0)
		sm.Update(makeFMS(true, false, false, false, true, false)) // teleop
		clock.Advance(120.0)
		sm.Update(makeFMS(false, false, false, false, true, false))

		clock.Advance(3.5)
		actions := sm.Update(makeFMS(false, false, false, false, true, false))
		assertActions(t, actions, StopRecord)
	})
}

// ---------------------------------------------------------------------------
// TestFMSDetach
// ---------------------------------------------------------------------------

func TestFMSDetach(t *testing.T) {
	t.Run("fms_detach_during_recording", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(10.0), WithClock(clock.Now))

		sm.Update(makeFMS(true, true, false, false, true, false))
		assertState(t, sm, RecordingAuto)

		// FMS detaches
		clock.Advance(5.0)
		sm.Update(makeFMS(false, false, false, false, false, false))
		assertState(t, sm, StopPending)

		// 3-second grace period (not full 10s)
		clock.Advance(4.0)
		actions := sm.Update(makeFMS(false, false, false, false, false, false))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})
}

// ---------------------------------------------------------------------------
// TestReEnableDuringStopPending
// ---------------------------------------------------------------------------

func TestReEnableDuringStopPending(t *testing.T) {
	t.Run("reenable_cancels_stop", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(10.0), WithClock(clock.Now))

		// Get to StopPending
		sm.Update(makeFMS(true, true, false, false, true, false))
		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(1.0)
		sm.Update(makeFMS(true, false, false, false, true, false)) // teleop
		clock.Advance(120.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		assertState(t, sm, StopPending)

		// Re-enable during stop pending
		clock.Advance(3.0)
		actions := sm.Update(makeFMS(true, false, false, false, true, false))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingTeleop)

		// Now it should NOT stop at the old time
		clock.Advance(15.0)
		actions = sm.Update(makeFMS(true, false, false, false, true, false))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingTeleop)
	})
}

// ---------------------------------------------------------------------------
// TestMultipleMatches
// ---------------------------------------------------------------------------

func TestMultipleMatches(t *testing.T) {
	t.Run("two_matches_in_sequence", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(5.0), WithAutoTeleopGap(5.0), WithClock(clock.Now))

		// --- Match 1 ---
		sm.Update(makeFMS(true, true, false, false, true, false))
		assertState(t, sm, RecordingAuto)

		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(1.0)
		sm.Update(makeFMS(true, false, false, false, true, false))
		assertState(t, sm, RecordingTeleop)

		clock.Advance(120.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		assertState(t, sm, StopPending)

		clock.Advance(6.0)
		actions := sm.Update(makeFMS(false, false, false, false, true, false))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)

		// --- Match 2 ---
		clock.Advance(60.0)
		actions = sm.Update(makeFMS(true, true, false, false, true, false))
		assertActions(t, actions, StartRecord)
		assertState(t, sm, RecordingAuto)

		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(1.0)
		sm.Update(makeFMS(true, false, false, false, true, false))
		assertState(t, sm, RecordingTeleop)

		clock.Advance(120.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(6.0)
		actions = sm.Update(makeFMS(false, false, false, false, true, false))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})
}

// ---------------------------------------------------------------------------
// TestNTDisconnect
// ---------------------------------------------------------------------------

func TestNTDisconnect(t *testing.T) {
	t.Run("nt_disconnect_grace_period", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithNTDisconnectGrace(15.0), WithClock(clock.Now))

		sm.Update(makeFMS(true, true, false, false, true, false))
		assertState(t, sm, RecordingAuto)

		// NT disconnects
		clock.Advance(5.0)
		actions := sm.Update(FMSStateDisconnected())
		assertNoActions(t, actions)
		assertState(t, sm, RecordingAuto)

		// Still within grace period
		clock.Advance(10.0)
		actions = sm.Update(FMSStateDisconnected())
		assertNoActions(t, actions)

		// Grace period expired
		clock.Advance(6.0)
		actions = sm.Update(FMSStateDisconnected())
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})

	t.Run("nt_reconnect_within_grace", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithNTDisconnectGrace(15.0), WithClock(clock.Now))

		sm.Update(makeFMS(true, true, false, false, true, false))

		// Disconnect
		clock.Advance(5.0)
		sm.Update(FMSStateDisconnected())

		// Reconnect within grace
		clock.Advance(5.0)
		actions := sm.Update(makeFMS(true, true, false, false, true, false))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingAuto)
	})
}

// ---------------------------------------------------------------------------
// TestEStop
// ---------------------------------------------------------------------------

func TestEStop(t *testing.T) {
	t.Run("estop_during_auto", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(10.0), WithClock(clock.Now))

		sm.Update(makeFMS(true, true, false, false, true, false))
		assertState(t, sm, RecordingAuto)

		// E-stop
		clock.Advance(5.0)
		sm.Update(makeFMS(false, false, false, true, true, false))
		assertState(t, sm, StopPending)

		// Wait for stop
		clock.Advance(11.0)
		actions := sm.Update(makeFMS(false, false, false, true, true, false))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})

	t.Run("estop_during_teleop", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithStopDelay(10.0), WithClock(clock.Now))

		sm.Update(makeFMS(true, true, false, false, true, false))
		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, true, false))
		clock.Advance(1.0)
		sm.Update(makeFMS(true, false, false, false, true, false))
		assertState(t, sm, RecordingTeleop)

		// E-stop
		clock.Advance(30.0)
		sm.Update(makeFMS(false, false, false, true, true, false))
		assertState(t, sm, StopPending)
	})
}

// ---------------------------------------------------------------------------
// TestIdleIgnoresNonFMS
// ---------------------------------------------------------------------------

func TestIdleIgnoresNonFMS(t *testing.T) {
	t.Run("enabled_without_fms_stays_idle", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithClock(clock.Now))

		actions := sm.Update(makeFMS(true, true, false, false, false, true))
		assertNoActions(t, actions)
		assertState(t, sm, Idle)
	})

	t.Run("fms_without_enabled_stays_idle", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithClock(clock.Now))

		actions := sm.Update(makeFMS(false, false, false, false, true, true))
		assertNoActions(t, actions)
		assertState(t, sm, Idle)
	})
}

// ---------------------------------------------------------------------------
// TestRecordTriggerAuto
// ---------------------------------------------------------------------------

func TestRecordTriggerAuto(t *testing.T) {
	t.Run("auto_enabled_without_fms_starts_recording", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithRecordTrigger("auto"), WithClock(clock.Now))

		actions := sm.Update(makeFMS(true, true, false, false, false, true))
		assertActions(t, actions, StartRecord)
		assertState(t, sm, RecordingAuto)
	})

	t.Run("teleop_enabled_without_fms_stays_idle", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithRecordTrigger("auto"), WithClock(clock.Now))

		actions := sm.Update(makeFMS(true, false, false, false, false, true))
		assertNoActions(t, actions)
		assertState(t, sm, Idle)
	})

	t.Run("ds_practice_full_lifecycle", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(
			WithRecordTrigger("auto"),
			WithStopDelay(10.0),
			WithAutoTeleopGap(5.0),
			WithClock(clock.Now),
		)

		// Auto start (no FMS)
		actions := sm.Update(makeFMS(true, true, false, false, false, true))
		assertActions(t, actions, StartRecord)
		assertState(t, sm, RecordingAuto)

		// Auto runs
		clock.Advance(15.0)
		sm.Update(makeFMS(true, true, false, false, false, true))

		// Brief disable between auto and teleop
		clock.Advance(0.1)
		sm.Update(makeFMS(false, false, false, false, false, true))
		assertState(t, sm, RecordingAuto) // gap tolerated

		// Teleop starts
		clock.Advance(1.0)
		sm.Update(makeFMS(true, false, false, false, false, true))
		assertState(t, sm, RecordingTeleop)

		// Teleop ends — disabled
		clock.Advance(120.0)
		sm.Update(makeFMS(false, false, false, false, false, true))
		assertState(t, sm, StopPending)

		// Stop delay
		clock.Advance(11.0)
		actions = sm.Update(makeFMS(false, false, false, false, false, true))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})

	t.Run("fms_detach_does_not_fire_in_auto_mode", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithRecordTrigger("auto"), WithClock(clock.Now))

		// Start in auto mode (no FMS)
		sm.Update(makeFMS(true, true, false, false, false, true))
		assertState(t, sm, RecordingAuto)

		// FMS is never attached, so "detach" should not trigger stop
		clock.Advance(5.0)
		sm.Update(makeFMS(true, true, false, false, false, true))
		assertState(t, sm, RecordingAuto) // still recording, not stopped
	})

	t.Run("reenable_during_stop_pending", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(
			WithRecordTrigger("auto"),
			WithStopDelay(10.0),
			WithClock(clock.Now),
		)

		// Start and get to StopPending
		sm.Update(makeFMS(true, true, false, false, false, true))
		clock.Advance(15.0)
		sm.Update(makeFMS(false, false, false, false, false, true))
		clock.Advance(6.0)
		sm.Update(makeFMS(false, false, false, false, false, true))
		assertState(t, sm, StopPending)

		// Re-enable in auto mode cancels stop
		clock.Advance(1.0)
		actions := sm.Update(makeFMS(true, true, false, false, false, true))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingAuto)
	})
}

// ---------------------------------------------------------------------------
// TestRecordTriggerAny
// ---------------------------------------------------------------------------

func TestRecordTriggerAny(t *testing.T) {
	t.Run("teleop_enabled_without_fms_starts_recording", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithRecordTrigger("any"), WithClock(clock.Now))

		actions := sm.Update(makeFMS(true, false, false, false, false, true))
		assertActions(t, actions, StartRecord)
		assertState(t, sm, RecordingAuto)
	})

	t.Run("disable_triggers_stop", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(
			WithRecordTrigger("any"),
			WithStopDelay(5.0),
			WithAutoTeleopGap(5.0),
			WithClock(clock.Now),
		)

		sm.Update(makeFMS(true, false, false, false, false, true))
		assertState(t, sm, RecordingAuto)

		// Disable — enters gap timer, then StopPending
		clock.Advance(10.0)
		sm.Update(makeFMS(false, false, false, false, false, true))
		clock.Advance(6.0)
		sm.Update(makeFMS(false, false, false, false, false, true))
		assertState(t, sm, StopPending)

		clock.Advance(6.0)
		actions := sm.Update(makeFMS(false, false, false, false, false, true))
		assertActions(t, actions, StopRecord)
		assertState(t, sm, Idle)
	})

	t.Run("reenable_during_stop_pending", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(
			WithRecordTrigger("any"),
			WithStopDelay(10.0),
			WithAutoTeleopGap(3.0),
			WithClock(clock.Now),
		)

		sm.Update(makeFMS(true, false, false, false, false, true))
		clock.Advance(10.0)
		sm.Update(makeFMS(false, false, false, false, false, true))
		clock.Advance(4.0)
		sm.Update(makeFMS(false, false, false, false, false, true))
		assertState(t, sm, StopPending)

		// Re-enable cancels stop
		actions := sm.Update(makeFMS(true, false, false, false, false, true))
		assertNoActions(t, actions)
		assertState(t, sm, RecordingTeleop)
	})
}

// ---------------------------------------------------------------------------
// TestRecordTriggerFmsDefault
// ---------------------------------------------------------------------------

func TestRecordTriggerFmsDefault(t *testing.T) {
	t.Run("enabled_without_fms_stays_idle", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithRecordTrigger("fms"), WithClock(clock.Now))

		actions := sm.Update(makeFMS(true, true, false, false, false, true))
		assertNoActions(t, actions)
		assertState(t, sm, Idle)
	})

	t.Run("fms_attached_starts_recording", func(t *testing.T) {
		clock := NewFakeClock(0.0)
		sm := NewMachine(WithRecordTrigger("fms"), WithClock(clock.Now))

		actions := sm.Update(makeFMS(true, true, false, false, true, true))
		assertActions(t, actions, StartRecord)
		assertState(t, sm, RecordingAuto)
	})
}

// ---------------------------------------------------------------------------
// TestFMSStateParsing — bitmask tests
// ---------------------------------------------------------------------------

func TestFMSStateParsing(t *testing.T) {
	t.Run("all_zeros", func(t *testing.T) {
		s := FMSStateFromRaw(0x00)
		if s.Enabled {
			t.Fatal("expected Enabled=false")
		}
		if s.AutoMode {
			t.Fatal("expected AutoMode=false")
		}
		if s.TestMode {
			t.Fatal("expected TestMode=false")
		}
		if s.EStop {
			t.Fatal("expected EStop=false")
		}
		if s.FMSAttached {
			t.Fatal("expected FMSAttached=false")
		}
		if s.DSAttached {
			t.Fatal("expected DSAttached=false")
		}
		if s.Raw != 0x00 {
			t.Fatalf("expected Raw=0x00, got 0x%02x", s.Raw)
		}
	})

	t.Run("enabled_bit", func(t *testing.T) {
		s := FMSStateFromRaw(0x01)
		if !s.Enabled {
			t.Fatal("expected Enabled=true")
		}
		if s.AutoMode {
			t.Fatal("expected AutoMode=false")
		}
	})

	t.Run("auto_mode_bit", func(t *testing.T) {
		s := FMSStateFromRaw(0x02)
		if s.Enabled {
			t.Fatal("expected Enabled=false")
		}
		if !s.AutoMode {
			t.Fatal("expected AutoMode=true")
		}
	})

	t.Run("test_mode_bit", func(t *testing.T) {
		s := FMSStateFromRaw(0x04)
		if !s.TestMode {
			t.Fatal("expected TestMode=true")
		}
	})

	t.Run("estop_bit", func(t *testing.T) {
		s := FMSStateFromRaw(0x08)
		if !s.EStop {
			t.Fatal("expected EStop=true")
		}
	})

	t.Run("fms_attached_bit", func(t *testing.T) {
		s := FMSStateFromRaw(0x10)
		if !s.FMSAttached {
			t.Fatal("expected FMSAttached=true")
		}
	})

	t.Run("ds_attached_bit", func(t *testing.T) {
		s := FMSStateFromRaw(0x20)
		if !s.DSAttached {
			t.Fatal("expected DSAttached=true")
		}
	})

	t.Run("auto_enabled_fms_attached", func(t *testing.T) {
		// 0x13 = enabled + auto + FMS attached
		s := FMSStateFromRaw(0x13)
		if !s.Enabled {
			t.Fatal("expected Enabled=true")
		}
		if !s.AutoMode {
			t.Fatal("expected AutoMode=true")
		}
		if s.TestMode {
			t.Fatal("expected TestMode=false")
		}
		if s.EStop {
			t.Fatal("expected EStop=false")
		}
		if !s.FMSAttached {
			t.Fatal("expected FMSAttached=true")
		}
		if s.DSAttached {
			t.Fatal("expected DSAttached=false")
		}
	})

	t.Run("teleop_enabled_fms_attached", func(t *testing.T) {
		// 0x11 = enabled + FMS attached (teleop)
		s := FMSStateFromRaw(0x11)
		if !s.Enabled {
			t.Fatal("expected Enabled=true")
		}
		if s.AutoMode {
			t.Fatal("expected AutoMode=false")
		}
		if s.TestMode {
			t.Fatal("expected TestMode=false")
		}
		if !s.FMSAttached {
			t.Fatal("expected FMSAttached=true")
		}
	})

	t.Run("disabled_fms_attached", func(t *testing.T) {
		// 0x10 = disabled + FMS attached
		s := FMSStateFromRaw(0x10)
		if s.Enabled {
			t.Fatal("expected Enabled=false")
		}
		if !s.FMSAttached {
			t.Fatal("expected FMSAttached=true")
		}
	})

	t.Run("all_bits_set", func(t *testing.T) {
		// 0x3F = all 6 bits set
		s := FMSStateFromRaw(0x3F)
		if !s.Enabled {
			t.Fatal("expected Enabled=true")
		}
		if !s.AutoMode {
			t.Fatal("expected AutoMode=true")
		}
		if !s.TestMode {
			t.Fatal("expected TestMode=true")
		}
		if !s.EStop {
			t.Fatal("expected EStop=true")
		}
		if !s.FMSAttached {
			t.Fatal("expected FMSAttached=true")
		}
		if !s.DSAttached {
			t.Fatal("expected DSAttached=true")
		}
	})

	t.Run("disconnected_state", func(t *testing.T) {
		s := FMSStateDisconnected()
		if s.Enabled {
			t.Fatal("expected Enabled=false")
		}
		if s.FMSAttached {
			t.Fatal("expected FMSAttached=false")
		}
		if s.Raw != -1 {
			t.Fatalf("expected Raw=-1, got %d", s.Raw)
		}
	})

	t.Run("auto_start_match", func(t *testing.T) {
		// Typical auto start: enabled + auto + FMS + DS = 0x33
		s := FMSStateFromRaw(0x33)
		if !s.Enabled {
			t.Fatal("expected Enabled=true")
		}
		if !s.AutoMode {
			t.Fatal("expected AutoMode=true")
		}
		if !s.FMSAttached {
			t.Fatal("expected FMSAttached=true")
		}
		if !s.DSAttached {
			t.Fatal("expected DSAttached=true")
		}
		if s.TestMode {
			t.Fatal("expected TestMode=false")
		}
		if s.EStop {
			t.Fatal("expected EStop=false")
		}
	})

	t.Run("teleop_with_ds", func(t *testing.T) {
		// Typical teleop: enabled + FMS + DS = 0x31
		s := FMSStateFromRaw(0x31)
		if !s.Enabled {
			t.Fatal("expected Enabled=true")
		}
		if s.AutoMode {
			t.Fatal("expected AutoMode=false")
		}
		if !s.FMSAttached {
			t.Fatal("expected FMSAttached=true")
		}
		if !s.DSAttached {
			t.Fatal("expected DSAttached=true")
		}
	})

	t.Run("str_representation", func(t *testing.T) {
		s := FMSStateFromRaw(0x13)
		text := s.String()
		if !strings.Contains(text, "ENABLED") {
			t.Fatalf("expected ENABLED in %q", text)
		}
		if !strings.Contains(text, "AUTO") {
			t.Fatalf("expected AUTO in %q", text)
		}
		if !strings.Contains(text, "FMS") {
			t.Fatalf("expected FMS in %q", text)
		}
	})

	t.Run("disconnected_str", func(t *testing.T) {
		s := FMSStateDisconnected()
		text := s.String()
		if !strings.Contains(text, "DISCONNECTED") {
			t.Fatalf("expected DISCONNECTED in %q", text)
		}
	})
}
