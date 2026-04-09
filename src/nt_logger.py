"""NetworkTables data logger — subscribes to NT path prefixes and writes value changes to JSONL."""

import base64
import json
import logging
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import IO

import ntcore

log = logging.getLogger(__name__)

# NT type enum → human-readable name
_NT_TYPE_NAMES: dict[int, str] = {
    ntcore.NetworkTableType.kBoolean: "boolean",
    ntcore.NetworkTableType.kDouble: "double",
    ntcore.NetworkTableType.kString: "string",
    ntcore.NetworkTableType.kRaw: "raw",
    ntcore.NetworkTableType.kBooleanArray: "boolean[]",
    ntcore.NetworkTableType.kDoubleArray: "double[]",
    ntcore.NetworkTableType.kStringArray: "string[]",
    ntcore.NetworkTableType.kInteger: "int",
    ntcore.NetworkTableType.kFloat: "float",
    ntcore.NetworkTableType.kIntegerArray: "int[]",
    ntcore.NetworkTableType.kFloatArray: "float[]",
}


def _type_name(nt_type: int) -> str:
    return _NT_TYPE_NAMES.get(nt_type, f"unknown({nt_type})")


def _coerce_value(nt_type: int, raw_value):
    """Convert NT value to a JSON-safe Python value."""
    if nt_type == ntcore.NetworkTableType.kRaw:
        if isinstance(raw_value, (bytes, bytearray)):
            return base64.b64encode(raw_value).decode("ascii")
        return str(raw_value)
    return raw_value


class NTLogger:
    """Logs NT value changes to JSONL files in data_dir/pending/."""

    def __init__(
        self,
        inst: ntcore.NetworkTableInstance,
        paths: list[str],
        data_dir: Path,
        team: int,
    ) -> None:
        self._inst = inst
        self._paths = list(paths)
        self._data_dir = data_dir
        self._team = team

        te = team // 100
        am = team % 100
        self._robot_ip = f"10.{te}.{am}.2"

        self._pending_dir = data_dir / "pending"
        self._pending_dir.mkdir(parents=True, exist_ok=True)

        # Topic handle → topic name cache
        self._topic_cache: dict[int, str] = {}

        # Public status attributes
        self.entries_written: int = 0
        self.active_session_id: str | None = None

        # Session file handle
        self._file: IO[str] | None = None

        # Create subscriber + poller
        self._sub: ntcore.MultiSubscriber | None = None
        self._poller: ntcore.NetworkTableListenerPoller | None = None
        self._listener_handle = None
        self._setup_subscriber()

        log.info("NTLogger initialized — paths=%s, data_dir=%s", self._paths, self._data_dir)

    def _effective_prefixes(self) -> list[str]:
        prefixes = list(self._paths)
        if "/FMSInfo/" not in prefixes:
            prefixes.append("/FMSInfo/")
        return prefixes

    def _setup_subscriber(self) -> None:
        prefixes = self._effective_prefixes()
        options = ntcore.PubSubOptions(sendAll=True, keepDuplicates=True, pollStorage=500)
        self._sub = ntcore.MultiSubscriber(self._inst, prefixes, options)

        self._poller = ntcore.NetworkTableListenerPoller(self._inst)
        self._listener_handle = self._poller.addListener(
            self._sub,
            ntcore.EventFlags.kValueAll | ntcore.EventFlags.kConnection,
        )
        log.debug("Subscriber created for prefixes: %s", prefixes)

    def _teardown_subscriber(self) -> None:
        if self._poller is not None:
            if self._listener_handle is not None:
                self._poller.removeListener(self._listener_handle)
                self._listener_handle = None
            self._poller.close()
            self._poller = None
        if self._sub is not None:
            self._sub.close()
            self._sub = None

    def _resolve_topic_name(self, topic_handle: int) -> str:
        name = self._topic_cache.get(topic_handle)
        if name is None:
            topic = ntcore.Topic(self._inst, topic_handle)
            name = topic.getName()
            self._topic_cache[topic_handle] = name
        return name

    def _write_line(self, obj: dict) -> None:
        if self._file is not None:
            self._file.write(json.dumps(obj, separators=(",", ":")) + "\n")
            self._file.flush()

    # ── Session management ──────────────────────────────────────────────

    def start_session(self) -> None:
        if self._file is not None:
            self.end_session()

        session_id = uuid.uuid4().hex[:8]
        ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        filename = f"{ts}_{session_id}.jsonl"
        filepath = self._pending_dir / filename

        self._file = open(filepath, "w")
        self.active_session_id = session_id
        self.entries_written = 0

        self._write_line({
            "type": "session_start",
            "ts": time.time(),
            "team": self._team,
            "robot_ip": self._robot_ip,
            "session_id": session_id,
        })

        log.info("Session started: %s", filename)

    def end_session(self) -> None:
        if self._file is None:
            return

        session_id = self.active_session_id
        self._write_line({
            "type": "session_end",
            "ts": time.time(),
            "session_id": session_id,
            "entries_written": self.entries_written,
        })

        self._file.close()
        self._file = None
        log.info("Session ended: %s (%d entries)", session_id, self.entries_written)
        self.active_session_id = None

    # ── Match events ────────────────────────────────────────────────────

    def record_match_event(self, event_type: str, fms_state) -> None:
        entry: dict = {
            "type": event_type,
            "ts": time.time(),
        }

        if event_type == "match_start":
            fms_table = self._inst.getTable("FMSInfo")
            entry["match_number"] = fms_table.getEntry("MatchNumber").getInteger(-1)
            entry["event_name"] = fms_table.getEntry("EventName").getString("")
            entry["match_type"] = fms_table.getEntry("MatchType").getInteger(-1)
            entry["is_red_alliance"] = fms_table.getEntry("IsRedAlliance").getBoolean(False)
            entry["station_number"] = fms_table.getEntry("StationNumber").getInteger(-1)

        if fms_state is not None:
            entry["fms_state"] = str(fms_state)

        self._write_line(entry)
        log.info("Match event recorded: %s", event_type)

    # ── Polling ─────────────────────────────────────────────────────────

    def poll(self) -> None:
        if self._poller is None:
            return

        events = self._poller.readQueue()
        for event in events:
            flags = event.flags

            if flags & ntcore.EventFlags.kConnection:
                if event.data is not None and event.data.connected:
                    log.info("NT connected — starting logging session")
                    self.start_session()
                else:
                    log.info("NT disconnected — ending logging session")
                    self.end_session()
                continue

            if flags & ntcore.EventFlags.kValueAll:
                if self._file is None:
                    continue

                value_data = event.data
                topic_handle = value_data.topic
                topic_name = self._resolve_topic_name(topic_handle)
                nt_type = value_data.value.type()
                raw_value = value_data.value.value()

                self._write_line({
                    "ts": time.time(),
                    "server_ts": value_data.value.server_time(),
                    "key": topic_name,
                    "type": _type_name(nt_type),
                    "value": _coerce_value(nt_type, raw_value),
                })
                self.entries_written += 1

    # ── Hot-reload ──────────────────────────────────────────────────────

    def update_paths(self, new_paths: list[str]) -> None:
        if new_paths == self._paths:
            return

        log.info("Updating NT subscription paths: %s → %s", self._paths, new_paths)
        self._paths = list(new_paths)
        self._topic_cache.clear()
        self._teardown_subscriber()
        self._setup_subscriber()

    # ── Cleanup ─────────────────────────────────────────────────────────

    def close(self) -> None:
        self.end_session()
        self._teardown_subscriber()
        log.info("NTLogger closed")
