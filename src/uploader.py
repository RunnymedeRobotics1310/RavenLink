"""Store-and-forward uploader for JSONL telemetry files to RavenBrain."""

import json
import logging
import shutil
import time
from pathlib import Path

import urllib.request
import urllib.error

log = logging.getLogger(__name__)

MAX_BACKOFF = 60.0
INITIAL_BACKOFF = 5.0


class Uploader:
    """Scans pending/ for completed JSONL files and uploads them to RavenBrain."""

    def __init__(
        self,
        data_dir: Path,
        ravenbrain_url: str,
        api_key: str,
        batch_size: int = 500,
        upload_interval: float = 10.0,
    ) -> None:
        self._data_dir = data_dir
        self._url = ravenbrain_url.rstrip("/") if ravenbrain_url else ""
        self._api_key = api_key
        self._batch_size = batch_size
        self._upload_interval = upload_interval

        self._last_upload_time = 0.0
        self._backoff = 0.0
        self._backoff_until = 0.0

        # Public status attributes
        self.files_pending: int = 0
        self.files_uploaded: int = 0
        self.currently_uploading: bool = False
        self.last_upload_result: str = ""

        # Ensure subdirectories exist
        self._pending_dir = data_dir / "pending"
        self._uploaded_dir = data_dir / "uploaded"
        self._failed_dir = data_dir / "failed"
        for d in (self._pending_dir, self._uploaded_dir, self._failed_dir):
            d.mkdir(parents=True, exist_ok=True)

    def maybe_upload(self, active_session_id: str | None = None) -> None:
        """Called from the main loop every cycle. Only does work every upload_interval seconds."""
        if not self._url:
            return

        now = time.monotonic()

        if now - self._last_upload_time < self._upload_interval:
            return

        if now < self._backoff_until:
            return

        self._last_upload_time = now

        pending = self._get_pending_files(active_session_id)
        self.files_pending = len(pending)

        if not pending:
            return

        filepath = pending[0]
        self.currently_uploading = True
        try:
            success = self._upload_file(filepath)
            if success:
                self._move_to_uploaded(filepath)
                self.files_uploaded += 1
                self.files_pending = max(0, self.files_pending - 1)
                self.last_upload_result = f"OK: {filepath.name}"
                self._backoff = 0.0
                log.info("Uploaded %s", filepath.name)
            else:
                self._apply_backoff()
        except Exception as e:
            log.warning("Upload failed for %s: %s", filepath.name, e)
            self.last_upload_result = f"ERROR: {e}"
            self._apply_backoff()
        finally:
            self.currently_uploading = False

    def prune_uploaded(self, retention_days: int) -> None:
        """Delete files from uploaded/ older than retention_days."""
        if retention_days <= 0:
            return
        cutoff = time.time() - (retention_days * 86400)
        for f in self._uploaded_dir.glob("*.jsonl"):
            if f.stat().st_mtime < cutoff:
                log.info("Pruning old upload: %s", f.name)
                f.unlink()

    def _get_pending_files(self, active_session_id: str | None) -> list[Path]:
        """Return pending JSONL files sorted oldest-first, excluding the active session."""
        files = sorted(self._pending_dir.glob("*.jsonl"), key=lambda f: f.stat().st_mtime)
        if active_session_id:
            files = [f for f in files if active_session_id not in f.name]
        return files

    def _upload_file(self, filepath: Path) -> bool:
        """Upload a single JSONL file to RavenBrain. Returns True on success."""
        lines = filepath.read_text().splitlines()
        if not lines:
            log.warning("Empty JSONL file: %s", filepath.name)
            return True

        # Parse session_start line
        session_meta = self._parse_session_start(lines)
        if session_meta is None:
            log.warning("No session_start found in %s — skipping", filepath.name)
            return True

        session_id = session_meta["session_id"]

        # Step 1: Create or get session (idempotent — safe to call on every retry)
        ok = self._post_json(
            "/api/telemetry/session",
            {
                "sessionId": session_id,
                "teamNumber": session_meta.get("team", 0),
                "robotIp": session_meta.get("robot_ip", ""),
                "startedAt": session_meta.get("ts", ""),
            },
        )
        if not ok:
            return False

        # Step 2: Ask server how many entries it already has
        server_count = self._get_uploaded_count(session_id)
        if server_count is None:
            return False  # network error, retry later

        # Collect all data entries (skip session_start)
        entries: list[dict] = []
        for line in lines:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                log.warning("Skipping malformed JSONL line in %s: %.80s", filepath.name, line)
                continue
            if entry.get("type") == "session_start":
                continue
            entries.append(entry)

        # Step 3: Skip entries the server already has, upload remaining
        remaining = entries[server_count:]
        if not remaining:
            log.info("Server already has all %d entries for %s", server_count, session_id)
        else:
            log.info(
                "Server has %d/%d entries for %s, uploading %d remaining",
                server_count, len(entries), session_id, len(remaining),
            )
            for i in range(0, len(remaining), self._batch_size):
                batch = remaining[i : i + self._batch_size]
                ok = self._post_json(
                    f"/api/telemetry/session/{session_id}/data",
                    batch,
                )
                if not ok:
                    return False

        # Step 4: Complete the session
        ended_at = ""
        for line in reversed(lines):
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
                ts = entry.get("ts", "")
                if ts:
                    ended_at = ts
                    break
            except json.JSONDecodeError:
                continue

        ok = self._post_json(
            f"/api/telemetry/session/{session_id}/complete",
            {"endedAt": ended_at, "entryCount": len(entries)},
        )
        if not ok:
            return False

        return True

    def _parse_session_start(self, lines: list[str]) -> dict | None:
        """Find and parse the session_start line from a JSONL file."""
        for line in lines:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
                if entry.get("type") == "session_start":
                    return entry
            except json.JSONDecodeError:
                continue
        return None

    def _get_uploaded_count(self, session_id: str) -> int | None:
        """Query server for how many entries it has for this session. Returns None on error."""
        result = self._get_json(f"/api/telemetry/session/{session_id}")
        if result is None:
            return None
        return result.get("uploadedCount", 0)

    def _post_json(self, path: str, payload: dict | list) -> bool:
        """POST JSON to RavenBrain. Returns True on success (2xx)."""
        url = self._url + path
        data = json.dumps(payload).encode("utf-8")

        req = urllib.request.Request(
            url,
            data=data,
            method="POST",
            headers={
                "Content-Type": "application/json",
                "X-Telemetry-Key": self._api_key,
            },
        )

        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                if resp.status < 200 or resp.status >= 300:
                    log.warning("RavenBrain returned %d for %s", resp.status, path)
                    return False
                return True
        except urllib.error.HTTPError as e:
            log.warning("RavenBrain HTTP %d for %s: %s", e.code, path, e.reason)
            self.last_upload_result = f"HTTP {e.code}: {e.reason}"
            return False
        except (urllib.error.URLError, OSError) as e:
            log.warning("RavenBrain connection error for %s: %s", path, e)
            self.last_upload_result = f"Connection error: {e}"
            return False

    def _get_json(self, path: str) -> dict | None:
        """GET JSON from RavenBrain. Returns parsed dict on success, None on failure."""
        url = self._url + path
        req = urllib.request.Request(
            url,
            method="GET",
            headers={"X-Telemetry-Key": self._api_key},
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except (urllib.error.HTTPError, urllib.error.URLError, OSError) as e:
            log.warning("RavenBrain GET error for %s: %s", path, e)
            self.last_upload_result = f"GET error: {e}"
            return None

    def _move_to_uploaded(self, filepath: Path) -> None:
        dest = self._uploaded_dir / filepath.name
        shutil.move(str(filepath), str(dest))

    def _apply_backoff(self) -> None:
        if self._backoff == 0.0:
            self._backoff = INITIAL_BACKOFF
        else:
            self._backoff = min(self._backoff * 2, MAX_BACKOFF)
        self._backoff_until = time.monotonic() + self._backoff
        log.info("Upload backoff: %.0fs", self._backoff)
