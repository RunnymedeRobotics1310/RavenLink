"""FRC OBS Recording Bridge — entry point and main loop."""

import logging
import signal
import sys
import time

from .autostart import AutoStart
from .bridge_status import BridgeStatus
from .config import Config, load_config
from .nt_client import NTClient
from .nt_logger import NTLogger
from .obs_client import OBSClient
from .state_machine import Action, MatchStateMachine, State
from .uploader import Uploader

log = logging.getLogger("frc_obs_bridge")

BANNER = r"""
╔══════════════════════════════════════╗
║   FRC OBS Recording Bridge v2.0.0   ║
║   + NT Data Collection & Upload     ║
╚══════════════════════════════════════╝
"""


class StatusLogHandler(logging.Handler):
    """Captures log messages into BridgeStatus for the dashboard."""

    def __init__(self, status: BridgeStatus) -> None:
        super().__init__()
        self._status = status

    def emit(self, record: logging.LogRecord) -> None:
        try:
            msg = self.format(record)
            self._status.add_log(msg)
        except Exception:
            pass


def setup_logging(level: str, status: BridgeStatus) -> None:
    formatter = logging.Formatter(
        "%(asctime)s [%(levelname)-7s] %(name)s: %(message)s",
        datefmt="%H:%M:%S",
    )
    console = logging.StreamHandler()
    console.setFormatter(formatter)

    status_handler = StatusLogHandler(status)
    status_handler.setFormatter(formatter)

    root = logging.getLogger()
    root.setLevel(getattr(logging, level))
    root.addHandler(console)
    root.addHandler(status_handler)


def run(config: Config) -> None:
    status = BridgeStatus()
    setup_logging(config.log_level, status)

    print(BANNER)
    log.info("Team: %d | Robot IP: %s", config.team, config.robot_ip)
    log.info("OBS: %s:%d | Stop delay: %.1fs | Poll: %.2fs",
             config.obs_host, config.obs_port, config.stop_delay, config.poll_interval)
    log.info("NT paths: %s", ", ".join(config.nt_paths))
    log.info("Data dir: %s", config.data_dir)
    if config.ravenbrain_url:
        log.info("RavenBrain: %s", config.ravenbrain_url)
    else:
        log.info("RavenBrain: disabled (local-only mode)")

    # Auto-start registration
    AutoStart.sync(config.launch_on_login)

    # Core components
    nt = NTClient(config.team)
    obs = OBSClient(config.obs_host, config.obs_port, config.obs_password)
    sm = MatchStateMachine(
        stop_delay=config.stop_delay,
        auto_teleop_gap=config.auto_teleop_gap,
        nt_disconnect_grace=config.nt_disconnect_grace,
    )

    # NT data logging
    nt_logger = NTLogger(nt.instance, config.nt_paths, config.data_dir, config.team)

    # Store-and-forward uploader
    uploader = Uploader(
        data_dir=config.data_dir,
        ravenbrain_url=config.ravenbrain_url,
        api_key=config.ravenbrain_api_key,
        batch_size=config.ravenbrain_batch_size,
        upload_interval=config.ravenbrain_upload_interval,
    )

    # Web dashboard + tray icon
    dashboard = None
    tray = None

    shutdown = False

    def handle_signal(sig, frame):
        nonlocal shutdown
        shutdown = True

    def handle_tray_quit():
        nonlocal shutdown
        shutdown = True

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    if config.dashboard_enabled:
        from .web_dashboard import WebDashboard
        dashboard = WebDashboard(config, port=config.dashboard_port)
        dashboard.start()

    from .tray_icon import TrayIcon
    tray = TrayIcon(dashboard_url=f"http://localhost:{config.dashboard_port}")
    tray.start(shutdown_callback=handle_tray_quit)

    last_status_log = 0.0
    status_interval = 5.0
    prev_state = State.IDLE
    last_entries = 0
    last_rate_time = time.monotonic()

    log.info("Bridge running — press Ctrl+C to stop")

    try:
        while not shutdown:
            fms_state = nt.get_fms_state()
            actions = sm.update(fms_state)

            # Match markers — fire at state transitions, not OBS actions
            if sm.state == State.RECORDING_AUTO and prev_state == State.IDLE:
                nt_logger.record_match_event("match_start", fms_state)
            if sm.state == State.STOP_PENDING and prev_state in (State.RECORDING_AUTO, State.RECORDING_TELEOP):
                nt_logger.record_match_event("match_end", fms_state)
            prev_state = sm.state

            # OBS actions
            for action in actions:
                if action == Action.START_RECORD:
                    if not obs.start_recording():
                        log.error("Failed to start OBS recording!")
                elif action == Action.STOP_RECORD:
                    if not obs.stop_recording():
                        log.error("Failed to stop OBS recording!")

            # NT data logging — drain event queue every cycle
            nt_logger.poll()

            # Upload (respects its own interval internally)
            uploader.maybe_upload(active_session_id=nt_logger.active_session_id)

            # Hot-reload config if changed via dashboard
            if config.consume_changed():
                log.info("Config reloaded")
                nt_logger.update_paths(config.nt_paths)
                uploader._upload_interval = config.ravenbrain_upload_interval
                uploader._batch_size = config.ravenbrain_batch_size
                AutoStart.sync(config.launch_on_login)
                logging.getLogger().setLevel(getattr(logging, config.log_level))

            # Update bridge status
            now = time.monotonic()
            dt = now - last_rate_time
            if dt >= 1.0:
                status.entries_per_second = (nt_logger.entries_written - last_entries) / dt
                last_entries = nt_logger.entries_written
                last_rate_time = now

            status.nt_connected = nt.connected
            status.obs_connected = obs.is_connected() if now - last_status_log >= status_interval else status.obs_connected
            status.match_state = sm.state.name
            status.active_session_file = nt_logger.active_session_id or ""
            status.entries_written = nt_logger.entries_written
            status.files_pending = uploader.files_pending
            status.files_uploaded = uploader.files_uploaded
            status.last_upload_result = uploader.last_upload_result
            status.currently_uploading = uploader.currently_uploading
            status.obs_recording = sm.state in (State.RECORDING_AUTO, State.RECORDING_TELEOP)

            if dashboard:
                dashboard.update_status(status)
            if tray:
                tray.update_status(status)

            # Periodic status logging
            if now - last_status_log >= status_interval:
                log.info(
                    "Status: NT=%s | FMS=%s | State=%s | OBS=%s | Entries=%d (%.1f/s) | Pending=%d",
                    "connected" if nt.connected else "DISCONNECTED",
                    fms_state,
                    sm.state.name,
                    "connected" if status.obs_connected else "DISCONNECTED",
                    nt_logger.entries_written,
                    status.entries_per_second,
                    uploader.files_pending,
                )
                last_status_log = now

            time.sleep(config.poll_interval)

    finally:
        log.info("Shutting down...")
        # Stop recording if active
        if sm.state in (State.RECORDING_AUTO, State.RECORDING_TELEOP, State.STOP_PENDING):
            log.info("Stopping active recording before exit")
            obs.stop_recording()

        # Prune old uploads
        uploader.prune_uploaded(config.retention_days)

        # Close all components
        nt_logger.close()
        obs.close()
        nt.close()
        if tray:
            tray.stop()
        log.info("Goodbye!")


def main() -> None:
    config = load_config()
    run(config)


if __name__ == "__main__":
    main()
