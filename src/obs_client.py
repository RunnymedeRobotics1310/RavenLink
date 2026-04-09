"""OBS WebSocket client for controlling recording."""

import logging
from typing import Optional

import obsws_python as obsws

log = logging.getLogger(__name__)


class OBSClient:
    """Wraps obsws-python ReqClient with reconnect logic and error handling."""

    def __init__(self, host: str = "localhost", port: int = 4455, password: str = "") -> None:
        self._host = host
        self._port = port
        self._password = password
        self._client: Optional[obsws.ReqClient] = None
        self._connect()

    def _connect(self) -> bool:
        try:
            self._client = obsws.ReqClient(
                host=self._host, port=self._port, password=self._password, timeout=3
            )
            log.info("Connected to OBS WebSocket at %s:%d", self._host, self._port)
            return True
        except Exception as e:
            log.warning("Could not connect to OBS: %s", e)
            self._client = None
            return False

    def _reconnect_and_retry(self, method_name: str):
        """Attempt one reconnect, return the client if successful."""
        log.info("Attempting OBS reconnect before retrying %s...", method_name)
        if self._connect():
            return self._client
        return None

    def is_connected(self) -> bool:
        if self._client is None:
            return False
        try:
            self._client.get_version()
            return True
        except Exception:
            self._client = None
            return False

    def start_recording(self) -> bool:
        for attempt in range(2):
            if self._client is None:
                if attempt == 0:
                    self._reconnect_and_retry("start_recording")
                    continue
                return False
            try:
                self._client.start_record()
                log.info(">>> OBS recording STARTED")
                return True
            except Exception as e:
                err = str(e)
                # Already recording is fine
                if "already" in err.lower() or "outputactive" in err.lower():
                    log.info("OBS was already recording")
                    return True
                log.warning("start_record failed (attempt %d): %s", attempt + 1, e)
                self._client = None
                if attempt == 0:
                    self._reconnect_and_retry("start_recording")
        return False

    def stop_recording(self) -> bool:
        for attempt in range(2):
            if self._client is None:
                if attempt == 0:
                    self._reconnect_and_retry("stop_recording")
                    continue
                return False
            try:
                self._client.stop_record()
                log.info(">>> OBS recording STOPPED")
                return True
            except Exception as e:
                err = str(e)
                if "not active" in err.lower() or "outputnotactive" in err.lower():
                    log.info("OBS was not recording")
                    return True
                log.warning("stop_record failed (attempt %d): %s", attempt + 1, e)
                self._client = None
                if attempt == 0:
                    self._reconnect_and_retry("stop_recording")
        return False

    def is_recording(self) -> bool:
        if self._client is None:
            return False
        try:
            resp = self._client.get_record_status()
            return resp.output_active
        except Exception:
            self._client = None
            return False

    def close(self) -> None:
        if self._client is not None:
            try:
                self._client.disconnect()
            except Exception:
                pass
            self._client = None
        log.info("OBS client closed")
