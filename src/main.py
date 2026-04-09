"""FRC OBS Recording Bridge — entry point and main loop."""

import logging
import signal
import sys
import time

from .config import Config, load_config
from .nt_client import NTClient
from .obs_client import OBSClient
from .state_machine import Action, MatchStateMachine, State

log = logging.getLogger("frc_obs_bridge")

BANNER = r"""
╔══════════════════════════════════════╗
║   FRC OBS Recording Bridge v1.0.0   ║
╚══════════════════════════════════════╝
"""


def setup_logging(level: str) -> None:
    logging.basicConfig(
        level=getattr(logging, level),
        format="%(asctime)s [%(levelname)-7s] %(name)s: %(message)s",
        datefmt="%H:%M:%S",
    )


def run(config: Config) -> None:
    setup_logging(config.log_level)

    print(BANNER)
    log.info("Team: %d | Robot IP: %s", config.team, config.robot_ip)
    log.info("OBS: %s:%d | Stop delay: %.1fs | Poll: %.2fs",
             config.obs_host, config.obs_port, config.stop_delay, config.poll_interval)

    nt = NTClient(config.team)
    obs = OBSClient(config.obs_host, config.obs_port, config.obs_password)
    sm = MatchStateMachine(
        stop_delay=config.stop_delay,
        auto_teleop_gap=config.auto_teleop_gap,
        nt_disconnect_grace=config.nt_disconnect_grace,
    )

    shutdown = False

    def handle_signal(sig, frame):
        nonlocal shutdown
        shutdown = True

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    last_status_log = 0.0
    status_interval = 5.0

    log.info("Bridge running — press Ctrl+C to stop")

    try:
        while not shutdown:
            fms_state = nt.get_fms_state()
            actions = sm.update(fms_state)

            for action in actions:
                if action == Action.START_RECORD:
                    if not obs.start_recording():
                        log.error("Failed to start OBS recording!")
                elif action == Action.STOP_RECORD:
                    if not obs.stop_recording():
                        log.error("Failed to stop OBS recording!")

            # Periodic status logging
            now = time.monotonic()
            if now - last_status_log >= status_interval:
                obs_connected = obs.is_connected()
                log.info(
                    "Status: NT=%s | FMS=%s | State=%s | OBS=%s",
                    "connected" if nt.connected else "DISCONNECTED",
                    fms_state,
                    sm.state.name,
                    "connected" if obs_connected else "DISCONNECTED",
                )
                last_status_log = now

            time.sleep(config.poll_interval)

    finally:
        log.info("Shutting down...")
        # If we're recording, stop before exit
        if sm.state in (State.RECORDING_AUTO, State.RECORDING_TELEOP, State.STOP_PENDING):
            log.info("Stopping active recording before exit")
            obs.stop_recording()
        obs.close()
        nt.close()
        log.info("Goodbye!")


def main() -> None:
    config = load_config()
    run(config)


if __name__ == "__main__":
    main()
