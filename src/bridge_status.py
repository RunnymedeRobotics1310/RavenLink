"""Shared status dataclass updated by all bridge components."""

from dataclasses import dataclass, field
from typing import Optional


@dataclass
class BridgeStatus:
    # Connections
    nt_connected: bool = False
    obs_connected: bool = False
    ravenbrain_reachable: bool = False

    # State machine
    match_state: str = "IDLE"

    # Telemetry
    active_session_file: str = ""
    entries_written: int = 0
    entries_per_second: float = 0.0
    subscribed_topics: int = 0

    # Upload
    files_pending: int = 0
    files_uploaded: int = 0
    last_upload_result: str = ""
    currently_uploading: bool = False

    # OBS
    obs_recording: bool = False

    # Log buffer
    recent_logs: list[str] = field(default_factory=list)
    _max_logs: int = field(default=100, repr=False)

    def add_log(self, message: str) -> None:
        self.recent_logs.append(message)
        if len(self.recent_logs) > self._max_logs:
            self.recent_logs = self.recent_logs[-self._max_logs:]

    def to_dict(self) -> dict:
        return {
            "nt_connected": self.nt_connected,
            "obs_connected": self.obs_connected,
            "ravenbrain_reachable": self.ravenbrain_reachable,
            "match_state": self.match_state,
            "active_session_file": self.active_session_file,
            "entries_written": self.entries_written,
            "entries_per_second": self.entries_per_second,
            "subscribed_topics": self.subscribed_topics,
            "files_pending": self.files_pending,
            "files_uploaded": self.files_uploaded,
            "last_upload_result": self.last_upload_result,
            "currently_uploading": self.currently_uploading,
            "obs_recording": self.obs_recording,
            "recent_logs": self.recent_logs,
        }
