"""Tests for the match state machine — all with mocked clocks, no real OBS/NT."""

import sys
import os

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from src.nt_client import FMSState
from src.state_machine import Action, MatchStateMachine, State


def make_fms(enabled=False, auto=False, test=False, estop=False, fms=False, ds=False) -> FMSState:
    """Helper to construct FMSState from flags."""
    raw = 0
    if enabled:
        raw |= 0x01
    if auto:
        raw |= 0x02
    if test:
        raw |= 0x04
    if estop:
        raw |= 0x08
    if fms:
        raw |= 0x10
    if ds:
        raw |= 0x20
    return FMSState.from_raw(raw)


class FakeClock:
    """Injectable clock for testing time-dependent state machine logic."""

    def __init__(self, start: float = 0.0):
        self.time = start

    def __call__(self) -> float:
        return self.time

    def advance(self, seconds: float):
        self.time += seconds


class TestFullMatchLifecycle:
    """Test a complete match: IDLE → auto → teleop → end → IDLE."""

    def test_normal_match(self):
        clock = FakeClock(0.0)
        sm = MatchStateMachine(stop_delay=10.0, auto_teleop_gap=5.0, clock=clock)

        assert sm.state == State.IDLE

        # 1. Auto start — FMS attached + enabled + auto
        actions = sm.update(make_fms(enabled=True, auto=True, fms=True, ds=True))
        assert actions == [Action.START_RECORD]
        assert sm.state == State.RECORDING_AUTO

        # 2. Stay in auto for a while
        clock.advance(15.0)
        actions = sm.update(make_fms(enabled=True, auto=True, fms=True, ds=True))
        assert actions == []
        assert sm.state == State.RECORDING_AUTO

        # 3. Auto ends — brief disabled gap
        clock.advance(0.1)
        actions = sm.update(make_fms(enabled=False, auto=False, fms=True, ds=True))
        assert actions == []
        assert sm.state == State.RECORDING_AUTO  # still in auto, gap tolerance

        # 4. Teleop starts — enabled, no auto
        clock.advance(1.0)
        actions = sm.update(make_fms(enabled=True, auto=False, fms=True, ds=True))
        assert actions == []
        assert sm.state == State.RECORDING_TELEOP

        # 5. Teleop runs for a while
        clock.advance(120.0)
        actions = sm.update(make_fms(enabled=True, auto=False, fms=True, ds=True))
        assert actions == []
        assert sm.state == State.RECORDING_TELEOP

        # 6. Match ends — disabled
        clock.advance(0.1)
        actions = sm.update(make_fms(enabled=False, auto=False, fms=True, ds=True))
        assert actions == []
        assert sm.state == State.STOP_PENDING

        # 7. Wait for stop delay
        clock.advance(9.0)
        actions = sm.update(make_fms(enabled=False, auto=False, fms=True, ds=True))
        assert actions == []
        assert sm.state == State.STOP_PENDING

        clock.advance(2.0)
        actions = sm.update(make_fms(enabled=False, auto=False, fms=True, ds=True))
        assert actions == [Action.STOP_RECORD]
        assert sm.state == State.IDLE


class TestAutoTeleopGap:
    """Test that the brief disabled gap between auto and teleop is tolerated."""

    def test_short_gap_tolerated(self):
        clock = FakeClock()
        sm = MatchStateMachine(auto_teleop_gap=5.0, clock=clock)

        # Start auto
        sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert sm.state == State.RECORDING_AUTO

        # Disabled for 2 seconds
        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        assert sm.state == State.RECORDING_AUTO

        clock.advance(2.0)
        sm.update(make_fms(enabled=False, fms=True))
        assert sm.state == State.RECORDING_AUTO  # still tolerating

        # Teleop starts
        clock.advance(0.5)
        actions = sm.update(make_fms(enabled=True, fms=True))
        assert sm.state == State.RECORDING_TELEOP
        assert actions == []

    def test_long_gap_triggers_stop(self):
        clock = FakeClock()
        sm = MatchStateMachine(auto_teleop_gap=5.0, stop_delay=10.0, clock=clock)

        sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert sm.state == State.RECORDING_AUTO

        # Disabled for longer than gap tolerance
        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(6.0)
        actions = sm.update(make_fms(enabled=False, fms=True))

        assert sm.state == State.STOP_PENDING


class TestStopDelay:
    """Test stop delay countdown."""

    def test_stop_fires_after_delay(self):
        clock = FakeClock()
        sm = MatchStateMachine(stop_delay=10.0, clock=clock)

        # Get to teleop
        sm.update(make_fms(enabled=True, auto=True, fms=True))
        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(1.0)
        sm.update(make_fms(enabled=True, fms=True))  # teleop
        assert sm.state == State.RECORDING_TELEOP

        # Match end
        clock.advance(120.0)
        sm.update(make_fms(enabled=False, fms=True))
        assert sm.state == State.STOP_PENDING

        # Not yet
        clock.advance(5.0)
        actions = sm.update(make_fms(enabled=False, fms=True))
        assert actions == []
        assert sm.state == State.STOP_PENDING

        # Now
        clock.advance(6.0)
        actions = sm.update(make_fms(enabled=False, fms=True))
        assert actions == [Action.STOP_RECORD]
        assert sm.state == State.IDLE

    def test_custom_stop_delay(self):
        clock = FakeClock()
        sm = MatchStateMachine(stop_delay=3.0, clock=clock)

        sm.update(make_fms(enabled=True, auto=True, fms=True))
        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(1.0)
        sm.update(make_fms(enabled=True, fms=True))  # teleop
        clock.advance(120.0)
        sm.update(make_fms(enabled=False, fms=True))

        clock.advance(3.5)
        actions = sm.update(make_fms(enabled=False, fms=True))
        assert actions == [Action.STOP_RECORD]


class TestFMSDetach:
    """Test FMS detach while recording triggers stop."""

    def test_fms_detach_during_recording(self):
        clock = FakeClock()
        sm = MatchStateMachine(stop_delay=10.0, clock=clock)

        sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert sm.state == State.RECORDING_AUTO

        # FMS detaches
        clock.advance(5.0)
        sm.update(make_fms(enabled=False, fms=False))
        assert sm.state == State.STOP_PENDING

        # 3-second grace period (not full 10s)
        clock.advance(4.0)
        actions = sm.update(make_fms(enabled=False, fms=False))
        assert actions == [Action.STOP_RECORD]
        assert sm.state == State.IDLE


class TestReEnableDuringStopPending:
    """Test that re-enabling during STOP_PENDING cancels the stop."""

    def test_reenable_cancels_stop(self):
        clock = FakeClock()
        sm = MatchStateMachine(stop_delay=10.0, clock=clock)

        # Get to STOP_PENDING
        sm.update(make_fms(enabled=True, auto=True, fms=True))
        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(1.0)
        sm.update(make_fms(enabled=True, fms=True))  # teleop
        clock.advance(120.0)
        sm.update(make_fms(enabled=False, fms=True))
        assert sm.state == State.STOP_PENDING

        # Re-enable during stop pending
        clock.advance(3.0)
        actions = sm.update(make_fms(enabled=True, fms=True))
        assert actions == []
        assert sm.state == State.RECORDING_TELEOP

        # Now it should NOT stop at the old time
        clock.advance(15.0)
        actions = sm.update(make_fms(enabled=True, fms=True))
        assert actions == []
        assert sm.state == State.RECORDING_TELEOP


class TestMultipleMatches:
    """Test that state machine resets properly between matches."""

    def test_two_matches_in_sequence(self):
        clock = FakeClock()
        sm = MatchStateMachine(stop_delay=5.0, auto_teleop_gap=5.0, clock=clock)

        # --- Match 1 ---
        sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert sm.state == State.RECORDING_AUTO

        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(1.0)
        sm.update(make_fms(enabled=True, fms=True))
        assert sm.state == State.RECORDING_TELEOP

        clock.advance(120.0)
        sm.update(make_fms(enabled=False, fms=True))
        assert sm.state == State.STOP_PENDING

        clock.advance(6.0)
        actions = sm.update(make_fms(enabled=False, fms=True))
        assert actions == [Action.STOP_RECORD]
        assert sm.state == State.IDLE

        # --- Match 2 ---
        clock.advance(60.0)
        actions = sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert actions == [Action.START_RECORD]
        assert sm.state == State.RECORDING_AUTO

        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(1.0)
        sm.update(make_fms(enabled=True, fms=True))
        assert sm.state == State.RECORDING_TELEOP

        clock.advance(120.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(6.0)
        actions = sm.update(make_fms(enabled=False, fms=True))
        assert actions == [Action.STOP_RECORD]
        assert sm.state == State.IDLE


class TestNTDisconnect:
    """Test NT disconnect grace period."""

    def test_nt_disconnect_grace_period(self):
        clock = FakeClock()
        sm = MatchStateMachine(nt_disconnect_grace=15.0, clock=clock)

        sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert sm.state == State.RECORDING_AUTO

        # NT disconnects
        clock.advance(5.0)
        actions = sm.update(FMSState.disconnected())
        assert actions == []
        assert sm.state == State.RECORDING_AUTO

        # Still within grace period
        clock.advance(10.0)
        actions = sm.update(FMSState.disconnected())
        assert actions == []

        # Grace period expired
        clock.advance(6.0)
        actions = sm.update(FMSState.disconnected())
        assert actions == [Action.STOP_RECORD]
        assert sm.state == State.IDLE

    def test_nt_reconnect_within_grace(self):
        clock = FakeClock()
        sm = MatchStateMachine(nt_disconnect_grace=15.0, clock=clock)

        sm.update(make_fms(enabled=True, auto=True, fms=True))

        # Disconnect
        clock.advance(5.0)
        sm.update(FMSState.disconnected())

        # Reconnect within grace
        clock.advance(5.0)
        actions = sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert actions == []
        assert sm.state == State.RECORDING_AUTO


class TestEStop:
    """Test E-stop behavior."""

    def test_estop_during_auto(self):
        clock = FakeClock()
        sm = MatchStateMachine(stop_delay=10.0, clock=clock)

        sm.update(make_fms(enabled=True, auto=True, fms=True))
        assert sm.state == State.RECORDING_AUTO

        # E-stop
        clock.advance(5.0)
        sm.update(make_fms(enabled=False, estop=True, fms=True))
        assert sm.state == State.STOP_PENDING

        # Wait for stop
        clock.advance(11.0)
        actions = sm.update(make_fms(enabled=False, estop=True, fms=True))
        assert actions == [Action.STOP_RECORD]
        assert sm.state == State.IDLE

    def test_estop_during_teleop(self):
        clock = FakeClock()
        sm = MatchStateMachine(stop_delay=10.0, clock=clock)

        sm.update(make_fms(enabled=True, auto=True, fms=True))
        clock.advance(15.0)
        sm.update(make_fms(enabled=False, fms=True))
        clock.advance(1.0)
        sm.update(make_fms(enabled=True, fms=True))
        assert sm.state == State.RECORDING_TELEOP

        # E-stop
        clock.advance(30.0)
        sm.update(make_fms(enabled=False, estop=True, fms=True))
        assert sm.state == State.STOP_PENDING


class TestIdleIgnoresNonFMS:
    """Test that IDLE state ignores non-FMS enable events."""

    def test_enabled_without_fms_stays_idle(self):
        clock = FakeClock()
        sm = MatchStateMachine(clock=clock)

        actions = sm.update(make_fms(enabled=True, auto=True, fms=False, ds=True))
        assert actions == []
        assert sm.state == State.IDLE

    def test_fms_without_enabled_stays_idle(self):
        clock = FakeClock()
        sm = MatchStateMachine(clock=clock)

        actions = sm.update(make_fms(enabled=False, fms=True, ds=True))
        assert actions == []
        assert sm.state == State.IDLE
