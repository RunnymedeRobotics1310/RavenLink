"""NetworkTables client for reading FMS match state."""

import logging
from dataclasses import dataclass

import ntcore

log = logging.getLogger(__name__)


@dataclass
class FMSState:
    enabled: bool
    auto_mode: bool
    test_mode: bool
    estop: bool
    fms_attached: bool
    ds_attached: bool
    raw: int

    @staticmethod
    def from_raw(value: int) -> "FMSState":
        return FMSState(
            enabled=bool(value & 0x01),
            auto_mode=bool(value & 0x02),
            test_mode=bool(value & 0x04),
            estop=bool(value & 0x08),
            fms_attached=bool(value & 0x10),
            ds_attached=bool(value & 0x20),
            raw=value,
        )

    @staticmethod
    def disconnected() -> "FMSState":
        return FMSState(
            enabled=False,
            auto_mode=False,
            test_mode=False,
            estop=False,
            fms_attached=False,
            ds_attached=False,
            raw=-1,
        )

    def __str__(self) -> str:
        flags = []
        if self.enabled:
            flags.append("ENABLED")
        if self.auto_mode:
            flags.append("AUTO")
        if self.test_mode:
            flags.append("TEST")
        if self.estop:
            flags.append("ESTOP")
        if self.fms_attached:
            flags.append("FMS")
        if self.ds_attached:
            flags.append("DS")
        return f"FMSState({' | '.join(flags) if flags else 'NONE'}, raw=0x{self.raw:02x})" if self.raw >= 0 else "FMSState(DISCONNECTED)"


class NTClient:
    """NetworkTables client that reads FMSControlData from the robot."""

    def __init__(self, team: int) -> None:
        self._team = team
        te = team // 100
        am = team % 100
        self._robot_ip = f"10.{te}.{am}.2"

        self._inst = ntcore.NetworkTableInstance.create()
        self._inst.startClient4("frc-obs-bridge")
        self._inst.setServer(self._robot_ip, 5810)

        table = self._inst.getTable("FMSInfo")
        self._control_data_sub = table.getIntegerTopic("FMSControlData").subscribe(-1)

        log.info("NT4 client started — connecting to %s:5810 (team %d)", self._robot_ip, team)

    @property
    def connected(self) -> bool:
        return self._inst.isConnected()

    def get_fms_state(self) -> FMSState:
        if not self.connected:
            return FMSState.disconnected()

        raw = self._control_data_sub.get()
        if raw < 0:
            return FMSState.disconnected()

        return FMSState.from_raw(raw)

    def close(self) -> None:
        log.info("Shutting down NT client")
        self._inst.stopClient()
        ntcore.NetworkTableInstance.destroy(self._inst)
