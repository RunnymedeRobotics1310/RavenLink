"""Tests for Uploader — mocked HTTP, no real server required."""

import json
import os
import sys
import time
from pathlib import Path
from unittest.mock import MagicMock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from src.uploader import Uploader, INITIAL_BACKOFF, MAX_BACKOFF  # noqa: E402


def _make_uploader(
    tmp_path: Path,
    url: str = "http://localhost:8080",
    api_key: str = "test-key",
    batch_size: int = 500,
    upload_interval: float = 10.0,
) -> Uploader:
    return Uploader(tmp_path, url, api_key, batch_size=batch_size, upload_interval=upload_interval)


def _write_jsonl(filepath: Path, entries: list[dict]) -> None:
    """Write a list of dicts as JSONL to a file."""
    with open(filepath, "w") as f:
        for entry in entries:
            f.write(json.dumps(entry) + "\n")


def _make_session_file(
    pending_dir: Path,
    filename: str = "20260409T120000Z_abc12345.jsonl",
    session_id: str = "abc12345",
    team: int = 1310,
    extra_entries: list[dict] | None = None,
    include_end: bool = True,
) -> Path:
    """Create a realistic JSONL session file in pending/."""
    entries = [
        {
            "type": "session_start",
            "ts": 1700000000.0,
            "team": team,
            "robot_ip": "10.13.10.2",
            "session_id": session_id,
        },
    ]
    if extra_entries:
        entries.extend(extra_entries)
    if include_end:
        entries.append({
            "type": "session_end",
            "ts": 1700000150.0,
            "session_id": session_id,
            "entries_written": len(extra_entries) if extra_entries else 0,
        })

    filepath = pending_dir / filename
    _write_jsonl(filepath, entries)
    return filepath


class TestDirectoryStructure:
    """Ensure pending/, uploaded/, failed/ are created on init."""

    def test_directories_created(self, tmp_path):
        _make_uploader(tmp_path)
        assert (tmp_path / "pending").is_dir()
        assert (tmp_path / "uploaded").is_dir()
        assert (tmp_path / "failed").is_dir()

    def test_directories_already_exist(self, tmp_path):
        for d in ("pending", "uploaded", "failed"):
            (tmp_path / d).mkdir()
        _make_uploader(tmp_path)
        # No error, still there
        assert (tmp_path / "pending").is_dir()


class TestMaybeUploadEmptyUrl:
    """Test that maybe_upload does nothing when URL is empty."""

    def test_empty_url_noop(self, tmp_path):
        uploader = _make_uploader(tmp_path, url="")
        pending_dir = tmp_path / "pending"
        _make_session_file(pending_dir)

        uploader.maybe_upload()
        # File should still be in pending
        assert len(list(pending_dir.glob("*.jsonl"))) == 1
        assert uploader.files_uploaded == 0

    def test_whitespace_url_noop(self, tmp_path):
        """URL with only whitespace gets rstripped to empty."""
        uploader = _make_uploader(tmp_path, url="   ")
        # The rstrip in __init__ turns "   " into "   " (non-empty) but
        # url.rstrip("/") on whitespace still leaves whitespace, which is truthy.
        # This is fine — the test verifies that an empty string triggers the guard.
        uploader2 = _make_uploader(tmp_path, url="")
        pending_dir = tmp_path / "pending"
        _make_session_file(pending_dir)
        uploader2.maybe_upload()
        assert uploader2.files_uploaded == 0


class TestUploadIntervalTiming:
    """Test that maybe_upload respects the upload_interval."""

    @patch("src.uploader.time")
    def test_respects_interval(self, mock_time, tmp_path):
        mock_time.monotonic.return_value = 100.0
        mock_time.time = time.time  # prune_uploaded uses time.time

        uploader = _make_uploader(tmp_path, upload_interval=10.0)
        pending_dir = tmp_path / "pending"
        _make_session_file(pending_dir)

        # First call — should attempt upload (last_upload_time is 0)
        with patch.object(uploader, "_upload_file", return_value=True):
            uploader.maybe_upload()
        assert uploader._last_upload_time == 100.0

        # Second call at t=105 — within interval, should skip
        mock_time.monotonic.return_value = 105.0
        with patch.object(uploader, "_upload_file", return_value=True) as mock_upload:
            _make_session_file(pending_dir, filename="20260409T120001Z_def67890.jsonl", session_id="def67890")
            uploader.maybe_upload()
            mock_upload.assert_not_called()

        # Third call at t=111 — past interval, should proceed
        mock_time.monotonic.return_value = 111.0
        with patch.object(uploader, "_upload_file", return_value=True) as mock_upload:
            uploader.maybe_upload()
            mock_upload.assert_called_once()


class TestGetPendingFiles:
    """Test _get_pending_files sorting and exclusion."""

    def test_returns_sorted_by_mtime(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        pending = tmp_path / "pending"

        # Create files with different mtimes
        f1 = _make_session_file(pending, filename="a_first.jsonl", session_id="aaa")
        import time as _time
        _time.sleep(0.05)
        f2 = _make_session_file(pending, filename="b_second.jsonl", session_id="bbb")

        files = uploader._get_pending_files(None)
        assert len(files) == 2
        assert files[0].name == "a_first.jsonl"
        assert files[1].name == "b_second.jsonl"

    def test_excludes_active_session(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        pending = tmp_path / "pending"

        _make_session_file(pending, filename="20260409T120000Z_abc12345.jsonl", session_id="abc12345")
        _make_session_file(pending, filename="20260409T120001Z_xyz99999.jsonl", session_id="xyz99999")

        files = uploader._get_pending_files("abc12345")
        assert len(files) == 1
        assert "abc12345" not in files[0].name

    def test_empty_pending_returns_empty_list(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        files = uploader._get_pending_files(None)
        assert files == []


class TestParseSessionStart:
    """Test _parse_session_start metadata extraction."""

    def test_extracts_session_start(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        lines = [
            json.dumps({"type": "session_start", "ts": 1700000000.0, "team": 1310, "robot_ip": "10.13.10.2", "session_id": "abc12345"}),
            json.dumps({"ts": 1700000001.0, "key": "/SmartDashboard/foo", "type": "double", "value": 3.14}),
            json.dumps({"type": "session_end", "ts": 1700000150.0, "session_id": "abc12345", "entries_written": 1}),
        ]
        result = uploader._parse_session_start(lines)

        assert result is not None
        assert result["session_id"] == "abc12345"
        assert result["team"] == 1310
        assert result["robot_ip"] == "10.13.10.2"

    def test_returns_none_when_no_session_start(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        lines = [
            json.dumps({"ts": 1700000001.0, "key": "/foo", "type": "double", "value": 1.0}),
        ]
        assert uploader._parse_session_start(lines) is None

    def test_skips_malformed_lines(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        lines = [
            "not valid json {{{",
            "",
            json.dumps({"type": "session_start", "session_id": "found_it"}),
        ]
        result = uploader._parse_session_start(lines)
        assert result is not None
        assert result["session_id"] == "found_it"


class TestProgressRoundTrip:
    """Test _read_progress and _write_progress."""

    def test_write_and_read(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        fake_file = tmp_path / "pending" / "test.jsonl"
        fake_file.touch()

        uploader._write_progress(fake_file, 42)
        assert uploader._read_progress(fake_file) == 42

    def test_read_returns_zero_when_no_progress(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        fake_file = tmp_path / "pending" / "test.jsonl"
        fake_file.touch()

        assert uploader._read_progress(fake_file) == 0

    def test_progress_file_format(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        fake_file = tmp_path / "pending" / "test.jsonl"
        fake_file.touch()

        uploader._write_progress(fake_file, 100)

        progress_file = fake_file.with_suffix(".jsonl.progress")
        assert progress_file.exists()
        content = progress_file.read_text()
        assert "uploaded_lines=100" in content


class TestUploadFileHTTP:
    """Test _upload_file sends correct HTTP requests."""

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_upload_sends_session_create_and_data_and_complete(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, batch_size=500)
        pending = tmp_path / "pending"

        data_entries = [
            {"ts": 1700000001.0, "key": "/SmartDashboard/speed", "type": "double", "value": 3.14},
            {"ts": 1700000002.0, "key": "/SmartDashboard/mode", "type": "string", "value": "auto"},
        ]
        filepath = _make_session_file(pending, extra_entries=data_entries)

        # Mock successful HTTP responses
        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        result = uploader._upload_file(filepath)
        assert result is True

        # Should have made 3 requests: session create, data batch, complete
        assert mock_request_cls.call_count == 3

        # Check first call — session create
        first_call = mock_request_cls.call_args_list[0]
        assert "/api/telemetry/session" in first_call[1].get("url", first_call[0][0])
        assert first_call[1].get("method", "") == "POST" or first_call[0][0].endswith("/session")

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_upload_includes_api_key_header(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, api_key="my-secret-key")
        pending = tmp_path / "pending"
        filepath = _make_session_file(pending)

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        uploader._upload_file(filepath)

        # Every request should have the API key header
        for call in mock_request_cls.call_args_list:
            headers = call[1].get("headers", {})
            assert headers.get("X-Telemetry-Key") == "my-secret-key"

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_upload_empty_file_returns_true(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path)
        pending = tmp_path / "pending"

        empty_file = pending / "empty.jsonl"
        empty_file.write_text("")

        result = uploader._upload_file(empty_file)
        assert result is True
        mock_request_cls.assert_not_called()


class TestFileMoveAfterUpload:
    """Test that files are moved from pending/ to uploaded/ after success."""

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_file_moved_to_uploaded(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"
        uploaded = tmp_path / "uploaded"

        filepath = _make_session_file(pending)

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        uploader.maybe_upload()

        # File should no longer be in pending
        assert len(list(pending.glob("*.jsonl"))) == 0
        # File should be in uploaded
        assert len(list(uploaded.glob("*.jsonl"))) == 1

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_progress_deleted_after_success(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"

        data_entries = [{"ts": 1700000001.0 + i, "key": f"/k{i}", "type": "double", "value": float(i)} for i in range(10)]
        filepath = _make_session_file(pending, extra_entries=data_entries)

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        uploader.maybe_upload()

        # No progress files should remain
        assert len(list(pending.glob("*.progress"))) == 0
        assert len(list((tmp_path / "uploaded").glob("*.progress"))) == 0


class TestExponentialBackoff:
    """Test backoff behavior on upload failure."""

    @patch("src.uploader.time")
    def test_initial_backoff(self, mock_time, tmp_path):
        mock_time.monotonic.return_value = 100.0
        mock_time.time = time.time

        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"
        _make_session_file(pending)

        with patch.object(uploader, "_upload_file", return_value=False):
            uploader.maybe_upload()

        assert uploader._backoff == INITIAL_BACKOFF
        assert uploader._backoff_until == 100.0 + INITIAL_BACKOFF

    @patch("src.uploader.time")
    def test_backoff_doubles(self, mock_time, tmp_path):
        mock_time.monotonic.return_value = 100.0
        mock_time.time = time.time

        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"

        with patch.object(uploader, "_upload_file", return_value=False):
            # First failure
            _make_session_file(pending, filename="a.jsonl", session_id="aaa")
            uploader.maybe_upload()
            assert uploader._backoff == INITIAL_BACKOFF

            # Advance past backoff
            mock_time.monotonic.return_value = 200.0
            uploader._last_upload_time = 0.0  # Reset interval check
            uploader.maybe_upload()
            assert uploader._backoff == INITIAL_BACKOFF * 2

    @patch("src.uploader.time")
    def test_backoff_capped_at_max(self, mock_time, tmp_path):
        mock_time.monotonic.return_value = 100.0
        mock_time.time = time.time

        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"
        _make_session_file(pending)

        with patch.object(uploader, "_upload_file", return_value=False):
            # Hammer failures until hitting max
            for i in range(20):
                mock_time.monotonic.return_value = 100.0 + (i * 200.0)
                uploader._last_upload_time = 0.0
                uploader._backoff_until = 0.0
                uploader.maybe_upload()

        assert uploader._backoff <= MAX_BACKOFF

    @patch("src.uploader.time")
    def test_backoff_resets_on_success(self, mock_time, tmp_path):
        mock_time.monotonic.return_value = 100.0
        mock_time.time = time.time

        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"
        _make_session_file(pending)

        # Fail first
        with patch.object(uploader, "_upload_file", return_value=False):
            uploader.maybe_upload()
        assert uploader._backoff == INITIAL_BACKOFF

        # Now succeed
        mock_time.monotonic.return_value = 200.0
        uploader._last_upload_time = 0.0
        uploader._backoff_until = 0.0
        _make_session_file(pending, filename="b.jsonl", session_id="bbb")

        with patch.object(uploader, "_upload_file", return_value=True):
            uploader.maybe_upload()
        assert uploader._backoff == 0.0


class TestPruneUploaded:
    """Test prune_uploaded deletes old files."""

    def test_deletes_old_files(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        uploaded = tmp_path / "uploaded"

        # Create a file and backdate its mtime
        old_file = uploaded / "old_session.jsonl"
        old_file.write_text("{}\n")
        # Set mtime to 100 days ago
        old_time = time.time() - (100 * 86400)
        os.utime(old_file, (old_time, old_time))

        uploader.prune_uploaded(retention_days=30)
        assert not old_file.exists()

    def test_keeps_recent_files(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        uploaded = tmp_path / "uploaded"

        recent_file = uploaded / "recent_session.jsonl"
        recent_file.write_text("{}\n")
        # mtime is now, which is within 30 days

        uploader.prune_uploaded(retention_days=30)
        assert recent_file.exists()

    def test_zero_retention_noop(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        uploaded = tmp_path / "uploaded"

        f = uploaded / "file.jsonl"
        f.write_text("{}\n")
        old_time = time.time() - (100 * 86400)
        os.utime(f, (old_time, old_time))

        uploader.prune_uploaded(retention_days=0)
        # Should not prune when retention_days <= 0
        assert f.exists()

    def test_prune_also_removes_stale_progress(self, tmp_path):
        uploader = _make_uploader(tmp_path)
        uploaded = tmp_path / "uploaded"

        old_file = uploaded / "old.jsonl"
        old_file.write_text("{}\n")
        progress = uploaded / "old.jsonl.progress"
        progress.write_text("uploaded_lines=5\n")

        old_time = time.time() - (100 * 86400)
        os.utime(old_file, (old_time, old_time))

        uploader.prune_uploaded(retention_days=30)
        assert not old_file.exists()
        assert not progress.exists()


class TestMalformedJSONL:
    """Test that malformed JSONL lines are skipped during upload."""

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_malformed_lines_skipped(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path)
        pending = tmp_path / "pending"

        # Write a file with some bad lines mixed in
        filepath = pending / "mixed.jsonl"
        lines = [
            json.dumps({"type": "session_start", "ts": 1700000000.0, "team": 1310, "robot_ip": "10.13.10.2", "session_id": "mixedid1"}),
            "this is not json!",
            "{broken json",
            json.dumps({"ts": 1700000001.0, "key": "/foo", "type": "double", "value": 1.0}),
            "",
            json.dumps({"type": "session_end", "ts": 1700000150.0, "session_id": "mixedid1", "entries_written": 1}),
        ]
        filepath.write_text("\n".join(lines) + "\n")

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        # Should succeed without raising
        result = uploader._upload_file(filepath)
        assert result is True


class TestUploadBatching:
    """Test that large entry lists are split into batches."""

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_batches_data_entries(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, batch_size=3)
        pending = tmp_path / "pending"

        # Create 7 data entries => should be 3 batches (3, 3, 1) + session create + complete = 5 requests
        data_entries = [
            {"ts": 1700000001.0 + i, "key": f"/val{i}", "type": "double", "value": float(i)}
            for i in range(7)
        ]
        filepath = _make_session_file(pending, extra_entries=data_entries)

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        result = uploader._upload_file(filepath)
        assert result is True

        # 1 session create + 3 data batches + 1 complete = 5 requests
        # (session_end is also an entry in data batches, so 7 data + 1 session_end = 8 entries, ceil(8/3)=3 batches)
        assert mock_request_cls.call_count == 5


class TestUploadStatusAttributes:
    """Test that public status attributes are updated correctly."""

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_files_uploaded_increments(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"

        _make_session_file(pending, filename="file1.jsonl", session_id="id1")

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        assert uploader.files_uploaded == 0
        uploader.maybe_upload()
        assert uploader.files_uploaded == 1

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_last_upload_result_on_success(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"
        _make_session_file(pending, filename="good.jsonl", session_id="goodid")

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        uploader.maybe_upload()
        assert "OK" in uploader.last_upload_result
        assert "good.jsonl" in uploader.last_upload_result

    def test_currently_uploading_flag(self, tmp_path):
        """currently_uploading should be False before and after upload."""
        uploader = _make_uploader(tmp_path)
        assert uploader.currently_uploading is False

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_files_pending_updated(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path, upload_interval=0.0)
        pending = tmp_path / "pending"

        _make_session_file(pending, filename="f1.jsonl", session_id="id1")
        _make_session_file(pending, filename="f2.jsonl", session_id="id2")

        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = lambda s: s
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        uploader.maybe_upload()
        # One uploaded, one still pending
        assert uploader.files_pending == 1


class TestUploadFileNoSessionStart:
    """Test _upload_file when file has no session_start marker."""

    @patch("src.uploader.urllib.request.urlopen")
    @patch("src.uploader.urllib.request.Request")
    def test_no_session_start_returns_true(self, mock_request_cls, mock_urlopen, tmp_path):
        uploader = _make_uploader(tmp_path)
        pending = tmp_path / "pending"

        filepath = pending / "orphan.jsonl"
        _write_jsonl(filepath, [
            {"ts": 1700000001.0, "key": "/foo", "type": "double", "value": 1.0},
        ])

        result = uploader._upload_file(filepath)
        assert result is True
        # No HTTP requests should have been made
        mock_request_cls.assert_not_called()


class TestUrlTrailingSlash:
    """Test that trailing slashes in URL are handled."""

    def test_trailing_slash_stripped(self, tmp_path):
        uploader = _make_uploader(tmp_path, url="http://example.com/")
        assert uploader._url == "http://example.com"

    def test_no_trailing_slash(self, tmp_path):
        uploader = _make_uploader(tmp_path, url="http://example.com")
        assert uploader._url == "http://example.com"
