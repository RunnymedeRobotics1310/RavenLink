"""Pure-logic state machine for FRC match recording control."""

import logging
from enum import Enum, auto
from typing import Callable, Optional

from .nt_client import FMSState

log = logging.getLogger(__name__)


class State(Enum):
    IDLE = auto()
    RECORDING_AUTO = auto()
    RECORDING_TELEOP = auto()
    STOP_PENDING = auto()


class Action(Enum):
    START_RECORD = auto()
    STOP_RECORD = auto()


class MatchStateMachine:
    """Determines recording actions from FMS state transitions.

    Pure logic — no OBS or NT calls. All time comes from an injectable clock.
    """

    def __init__(
        self,
        stop_delay: float = 10.0,
        auto_teleop_gap: float = 5.0,
        nt_disconnect_grace: float = 15.0,
        clock: Optional[Callable[[], float]] = None,
    ) -> None:
        self._stop_delay = stop_delay
        self._auto_teleop_gap = auto_teleop_gap
        self._nt_disconnect_grace = nt_disconnect_grace
        self._clock = clock or self._default_clock

        self.state = State.IDLE
        self._disabled_at: Optional[float] = None
        self._stop_pending_at: Optional[float] = None
        self._nt_disconnected_at: Optional[float] = None
        self._was_enabled = False

    @staticmethod
    def _default_clock() -> float:
        import time
        return time.monotonic()

    def _now(self) -> float:
        return self._clock()

    def _reset(self) -> None:
        self.state = State.IDLE
        self._disabled_at = None
        self._stop_pending_at = None
        self._nt_disconnected_at = None
        self._was_enabled = False

    def update(self, fms: FMSState) -> list[Action]:
        """Process one FMS state tick. Returns actions to execute."""
        now = self._now()
        actions: list[Action] = []

        # --- Handle NT disconnect (raw == -1) ---
        if fms.raw < 0:
            if self.state != State.IDLE:
                if self._nt_disconnected_at is None:
                    self._nt_disconnected_at = now
                    log.warning("NT disconnected while in %s — grace period started", self.state.name)
                elif now - self._nt_disconnected_at > self._nt_disconnect_grace:
                    log.warning("NT disconnect grace period expired — stopping recording")
                    actions.append(Action.STOP_RECORD)
                    self._reset()
            return actions

        # NT is connected — clear disconnect timer
        self._nt_disconnected_at = None

        # --- Handle FMS detach while recording ---
        if not fms.fms_attached and self.state != State.IDLE:
            if self.state != State.STOP_PENDING:
                log.warning("FMS detached while in %s — stopping recording after 3s grace", self.state.name)
                self.state = State.STOP_PENDING
                self._stop_pending_at = now - self._stop_delay + 3.0  # expires in 3s
            # Fall through to STOP_PENDING handler to check the timer

        # --- Handle E-stop ---
        if fms.estop and self.state in (State.RECORDING_AUTO, State.RECORDING_TELEOP):
            log.warning("E-STOP detected — transitioning to STOP_PENDING")
            self.state = State.STOP_PENDING
            self._stop_pending_at = now
            self._disabled_at = now

        # --- State transitions ---
        if self.state == State.IDLE:
            # IDLE → RECORDING_AUTO: FMS attached + enabled
            if fms.fms_attached and fms.enabled:
                log.info("Match start detected (FMS attached + enabled) — starting recording")
                self.state = State.RECORDING_AUTO
                self._was_enabled = True
                self._disabled_at = None
                actions.append(Action.START_RECORD)

        elif self.state == State.RECORDING_AUTO:
            if fms.enabled:
                self._was_enabled = True
                self._disabled_at = None
                # Check transition to teleop: enabled + not auto + not test
                if not fms.auto_mode and not fms.test_mode:
                    log.info("Auto → Teleop transition detected")
                    self.state = State.RECORDING_TELEOP
            else:
                # Disabled during auto — start gap timer
                if self._disabled_at is None:
                    self._disabled_at = now
                    log.debug("Disabled during auto — gap timer started")
                elif now - self._disabled_at > self._auto_teleop_gap:
                    # Gap too long — this isn't an auto→teleop transition
                    log.info("Auto phase disabled gap exceeded %.1fs — stopping", self._auto_teleop_gap)
                    self.state = State.STOP_PENDING
                    self._stop_pending_at = now

        elif self.state == State.RECORDING_TELEOP:
            if fms.enabled:
                self._was_enabled = True
                self._disabled_at = None
            else:
                # Disabled during teleop — match end
                if self._disabled_at is None:
                    self._disabled_at = now
                    log.info("Teleop disabled — match end, entering STOP_PENDING")
                    self.state = State.STOP_PENDING
                    self._stop_pending_at = now

        elif self.state == State.STOP_PENDING:
            # Re-enable cancels the stop
            if fms.enabled and fms.fms_attached:
                log.info("Re-enabled during STOP_PENDING — resuming recording")
                if fms.auto_mode:
                    self.state = State.RECORDING_AUTO
                else:
                    self.state = State.RECORDING_TELEOP
                self._stop_pending_at = None
                self._disabled_at = None
            elif self._stop_pending_at is not None and now - self._stop_pending_at >= self._stop_delay:
                log.info("Stop delay (%.1fs) elapsed — stopping recording", self._stop_delay)
                actions.append(Action.STOP_RECORD)
                self._reset()

        return actions
