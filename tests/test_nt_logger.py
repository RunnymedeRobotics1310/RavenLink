"""Tests for NTLogger — mocked ntcore, no real NetworkTables required."""

import json
import sys
import os
import time
from pathlib import Path
from unittest.mock import MagicMock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

# ── Mock ntcore before importing NTLogger ──────────────────────────────
# Build a fake ntcore module with the constants and classes NTLogger references.

_mock_ntcore = MagicMock()

# NetworkTableType enum values
_mock_ntcore.NetworkTableType.kBoolean = 1
_mock_ntcore.NetworkTableType.kDouble = 2
_mock_ntcore.NetworkTableType.kString = 4
_mock_ntcore.NetworkTableType.kRaw = 8
_mock_ntcore.NetworkTableType.kBooleanArray = 16
_mock_ntcore.NetworkTableType.kDoubleArray = 32
_mock_ntcore.NetworkTableType.kStringArray = 64
_mock_ntcore.NetworkTableType.kInteger = 128
_mock_ntcore.NetworkTableType.kFloat = 256
_mock_ntcore.NetworkTableType.kIntegerArray = 512
_mock_ntcore.NetworkTableType.kFloatArray = 1024

# EventFlags
_mock_ntcore.EventFlags.kValueAll = 0x0F
_mock_ntcore.EventFlags.kConnection = 0x10

# Classes that NTLogger instantiates
_mock_ntcore.MultiSubscriber = MagicMock
_mock_ntcore.NetworkTableListenerPoller = MagicMock
_mock_ntcore.NetworkTableInstance = MagicMock
_mock_ntcore.PubSubOptions = MagicMock
_mock_ntcore.Topic = MagicMock

sys.modules["ntcore"] = _mock_ntcore

from src.nt_logger import NTLogger, _type_name, _coerce_value  # noqa: E402


def _read_jsonl(filepath: Path) -> list[dict]:
    """Read a JSONL file and return a list of parsed dicts."""
    lines = filepath.read_text().splitlines()
    entries = []
    for line in lines:
        line = line.strip()
        if line:
            entries.append(json.loads(line))
    return entries


def _make_logger(tmp_path: Path, paths: list[str] | None = None, team: int = 1310) -> NTLogger:
    """Create an NTLogger with a mocked NT instance, writing to tmp_path."""
    inst = MagicMock()
    if paths is None:
        paths = ["/SmartDashboard/"]
    return NTLogger(inst, paths, tmp_path, team)


class TestPendingDirectoryCreation:
    """Ensure the pending/ directory is created on init."""

    def test_pending_dir_created(self, tmp_path):
        _make_logger(tmp_path)
        assert (tmp_path / "pending").is_dir()

    def test_pending_dir_already_exists(self, tmp_path):
        (tmp_path / "pending").mkdir()
        _make_logger(tmp_path)
        assert (tmp_path / "pending").is_dir()


class TestSessionLifecycle:
    """Test start_session / end_session markers and state."""

    def test_start_session_creates_file(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        assert len(files) == 1

    def test_start_session_writes_session_start_marker(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        entries = _read_jsonl(files[0])
        assert entries[0]["type"] == "session_start"

    def test_session_start_contains_required_fields(self, tmp_path):
        logger = _make_logger(tmp_path, team=1310)
        logger.start_session()
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        start_entry = _read_jsonl(files[0])[0]

        assert "ts" in start_entry
        assert start_entry["team"] == 1310
        assert start_entry["robot_ip"] == "10.13.10.2"
        assert "session_id" in start_entry
        assert isinstance(start_entry["ts"], float)

    def test_end_session_writes_session_end_marker(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        entries = _read_jsonl(files[0])

        end_entry = entries[-1]
        assert end_entry["type"] == "session_end"
        assert "ts" in end_entry
        assert "entries_written" in end_entry

    def test_session_end_has_matching_session_id(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        entries = _read_jsonl(files[0])

        start_id = entries[0]["session_id"]
        end_id = entries[-1]["session_id"]
        assert start_id == end_id

    def test_end_session_noop_when_no_active_session(self, tmp_path):
        logger = _make_logger(tmp_path)
        # Should not raise
        logger.end_session()
        files = list((tmp_path / "pending").glob("*.jsonl"))
        assert len(files) == 0

    def test_start_session_ends_previous_session(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        first_id = logger.active_session_id
        logger.start_session()
        second_id = logger.active_session_id

        assert first_id != second_id

        # Should have two files
        files = list((tmp_path / "pending").glob("*.jsonl"))
        assert len(files) == 2


class TestActiveSessionId:
    """Test that active_session_id is set and cleared correctly."""

    def test_active_session_id_set_on_start(self, tmp_path):
        logger = _make_logger(tmp_path)
        assert logger.active_session_id is None

        logger.start_session()
        assert logger.active_session_id is not None
        assert len(logger.active_session_id) == 8

    def test_active_session_id_cleared_on_end(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.end_session()
        assert logger.active_session_id is None

    def test_session_ids_are_unique(self, tmp_path):
        logger = _make_logger(tmp_path)
        ids = set()
        for _ in range(10):
            logger.start_session()
            ids.add(logger.active_session_id)
            logger.end_session()
        assert len(ids) == 10


class TestEntriesWrittenCounter:
    """Test that entries_written increments correctly."""

    def test_entries_written_starts_at_zero(self, tmp_path):
        logger = _make_logger(tmp_path)
        assert logger.entries_written == 0

    def test_entries_written_resets_on_new_session(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        # Manually increment to simulate poll() writing entries
        logger.entries_written = 5
        logger.end_session()

        logger.start_session()
        assert logger.entries_written == 0

    def test_entries_written_appears_in_session_end(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.entries_written = 42
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        entries = _read_jsonl(files[0])
        end_entry = entries[-1]
        assert end_entry["entries_written"] == 42


class TestMatchEvents:
    """Test record_match_event for match_start and match_end markers."""

    def test_match_start_event(self, tmp_path):
        inst = MagicMock()
        # Set up getTable("FMSInfo").getEntry(...) chain
        fms_table = MagicMock()
        fms_table.getEntry("MatchNumber").getInteger.return_value = 7
        fms_table.getEntry("EventName").getString.return_value = "CAFR"
        fms_table.getEntry("MatchType").getInteger.return_value = 2
        fms_table.getEntry("IsRedAlliance").getBoolean.return_value = True
        fms_table.getEntry("StationNumber").getInteger.return_value = 1
        inst.getTable.return_value = fms_table

        logger = NTLogger(inst, ["/SmartDashboard/"], tmp_path, 1310)
        logger.start_session()

        fms_state = MagicMock()
        fms_state.__str__ = lambda self: "AUTO_ENABLED"
        logger.record_match_event("match_start", fms_state)
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        entries = _read_jsonl(files[0])

        match_entry = entries[1]  # After session_start
        assert match_entry["type"] == "match_start"
        assert match_entry["match_number"] == 7
        assert match_entry["event_name"] == "CAFR"
        assert match_entry["match_type"] == 2
        assert match_entry["is_red_alliance"] is True
        assert match_entry["station_number"] == 1
        assert match_entry["fms_state"] == "AUTO_ENABLED"
        assert "ts" in match_entry

    def test_match_end_event(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()

        fms_state = MagicMock()
        fms_state.__str__ = lambda self: "DISABLED"
        logger.record_match_event("match_end", fms_state)
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        entries = _read_jsonl(files[0])

        match_entry = entries[1]
        assert match_entry["type"] == "match_end"
        assert match_entry["fms_state"] == "DISABLED"
        assert "ts" in match_entry
        # match_end should NOT have match_number etc. — only match_start does
        assert "match_number" not in match_entry

    def test_match_event_with_none_fms_state(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.record_match_event("match_end", None)
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        entries = _read_jsonl(files[0])

        match_entry = entries[1]
        assert "fms_state" not in match_entry

    def test_match_event_without_active_session_is_silent(self, tmp_path):
        """record_match_event with no open session writes nothing (no crash)."""
        logger = _make_logger(tmp_path)
        # No start_session — _file is None, so _write_line is a no-op
        logger.record_match_event("match_start", None)
        files = list((tmp_path / "pending").glob("*.jsonl"))
        assert len(files) == 0


class TestJSONLFormat:
    """Test that JSONL entries use compact separators and one-per-line format."""

    def test_compact_json_separators(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        raw = files[0].read_text()
        lines = [l for l in raw.splitlines() if l.strip()]

        for line in lines:
            # Compact separators: no space after , or :
            parsed = json.loads(line)
            re_encoded = json.dumps(parsed, separators=(",", ":"))
            assert line == re_encoded

    def test_each_entry_is_single_line(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        logger.record_match_event("match_start", MagicMock())
        logger.record_match_event("match_end", MagicMock())
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        raw = files[0].read_text()
        lines = [l for l in raw.splitlines() if l.strip()]

        # session_start + match_start + match_end + session_end = 4 lines
        assert len(lines) == 4

        for line in lines:
            # Each line must be valid JSON
            json.loads(line)


class TestFMSInfoPrefix:
    """Test that /FMSInfo/ is always in the prefix list."""

    def test_fms_info_added_when_missing(self, tmp_path):
        logger = _make_logger(tmp_path, paths=["/SmartDashboard/"])
        prefixes = logger._effective_prefixes()
        assert "/FMSInfo/" in prefixes

    def test_fms_info_not_duplicated(self, tmp_path):
        logger = _make_logger(tmp_path, paths=["/SmartDashboard/", "/FMSInfo/"])
        prefixes = logger._effective_prefixes()
        assert prefixes.count("/FMSInfo/") == 1

    def test_fms_info_only(self, tmp_path):
        logger = _make_logger(tmp_path, paths=["/FMSInfo/"])
        prefixes = logger._effective_prefixes()
        assert prefixes == ["/FMSInfo/"]


class TestUpdatePaths:
    """Test hot-reload of subscription prefixes."""

    def test_update_paths_changes_prefixes(self, tmp_path):
        logger = _make_logger(tmp_path, paths=["/SmartDashboard/"])
        logger.update_paths(["/SmartDashboard/", "/Custom/"])

        assert logger._paths == ["/SmartDashboard/", "/Custom/"]
        assert "/FMSInfo/" in logger._effective_prefixes()

    def test_update_paths_noop_if_same(self, tmp_path):
        logger = _make_logger(tmp_path, paths=["/SmartDashboard/"])
        # Spy on _teardown_subscriber
        logger._teardown_subscriber = MagicMock()
        logger._setup_subscriber = MagicMock()

        logger.update_paths(["/SmartDashboard/"])

        logger._teardown_subscriber.assert_not_called()
        logger._setup_subscriber.assert_not_called()

    def test_update_paths_clears_topic_cache(self, tmp_path):
        logger = _make_logger(tmp_path, paths=["/SmartDashboard/"])
        logger._topic_cache[42] = "/SmartDashboard/foo"

        logger.update_paths(["/Custom/"])
        assert len(logger._topic_cache) == 0


class TestSessionFilenaming:
    """Test that session files are named with timestamp and session ID."""

    def test_filename_format(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        session_id = logger.active_session_id
        logger.end_session()

        files = list((tmp_path / "pending").glob("*.jsonl"))
        assert len(files) == 1

        name = files[0].stem  # e.g. "20260409T120000Z_ab12cd34"
        parts = name.split("_")
        assert len(parts) == 2

        timestamp_part = parts[0]
        id_part = parts[1]

        # Timestamp should end with Z (UTC)
        assert timestamp_part.endswith("Z")
        # Session ID should match
        assert id_part == session_id

    def test_robot_ip_calculation(self, tmp_path):
        """Test team number to IP conversion."""
        logger = _make_logger(tmp_path, team=254)
        assert logger._robot_ip == "10.2.54.2"

        logger2 = _make_logger(tmp_path, team=1310)
        assert logger2._robot_ip == "10.13.10.2"

        logger3 = _make_logger(tmp_path, team=9999)
        assert logger3._robot_ip == "10.99.99.2"


class TestTypeName:
    """Test the _type_name helper."""

    def test_known_types(self):
        assert _type_name(_mock_ntcore.NetworkTableType.kBoolean) == "boolean"
        assert _type_name(_mock_ntcore.NetworkTableType.kDouble) == "double"
        assert _type_name(_mock_ntcore.NetworkTableType.kString) == "string"
        assert _type_name(_mock_ntcore.NetworkTableType.kRaw) == "raw"
        assert _type_name(_mock_ntcore.NetworkTableType.kInteger) == "int"
        assert _type_name(_mock_ntcore.NetworkTableType.kFloat) == "float"
        assert _type_name(_mock_ntcore.NetworkTableType.kBooleanArray) == "boolean[]"
        assert _type_name(_mock_ntcore.NetworkTableType.kDoubleArray) == "double[]"
        assert _type_name(_mock_ntcore.NetworkTableType.kStringArray) == "string[]"
        assert _type_name(_mock_ntcore.NetworkTableType.kIntegerArray) == "int[]"
        assert _type_name(_mock_ntcore.NetworkTableType.kFloatArray) == "float[]"

    def test_unknown_type(self):
        result = _type_name(99999)
        assert "unknown" in result
        assert "99999" in result


class TestCoerceValue:
    """Test the _coerce_value helper."""

    def test_raw_bytes_base64(self):
        result = _coerce_value(_mock_ntcore.NetworkTableType.kRaw, b"\x00\x01\x02")
        assert result == "AAEC"  # base64

    def test_raw_bytearray_base64(self):
        result = _coerce_value(_mock_ntcore.NetworkTableType.kRaw, bytearray(b"\xff"))
        assert result == "/w=="

    def test_raw_non_bytes_str(self):
        result = _coerce_value(_mock_ntcore.NetworkTableType.kRaw, 42)
        assert result == "42"

    def test_non_raw_passthrough(self):
        assert _coerce_value(_mock_ntcore.NetworkTableType.kDouble, 3.14) == 3.14
        assert _coerce_value(_mock_ntcore.NetworkTableType.kString, "hello") == "hello"
        assert _coerce_value(_mock_ntcore.NetworkTableType.kBoolean, True) is True


class TestClose:
    """Test cleanup via close()."""

    def test_close_ends_session_and_tears_down(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.start_session()
        assert logger.active_session_id is not None

        logger.close()
        assert logger.active_session_id is None

        # File should be written and closed
        files = list((tmp_path / "pending").glob("*.jsonl"))
        assert len(files) == 1
        entries = _read_jsonl(files[0])
        assert entries[-1]["type"] == "session_end"

    def test_close_without_session_is_safe(self, tmp_path):
        logger = _make_logger(tmp_path)
        logger.close()  # Should not raise
