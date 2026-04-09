"""Configuration for frc-obs-bridge — CLI args + config.ini support."""

import argparse
import configparser
import os
from dataclasses import dataclass
from pathlib import Path

CONFIG_FILE = "config.ini"


@dataclass
class Config:
    team: int
    obs_host: str = "localhost"
    obs_port: int = 4455
    obs_password: str = ""
    stop_delay: float = 10.0
    poll_interval: float = 1.0
    log_level: str = "INFO"
    auto_teleop_gap: float = 5.0
    nt_disconnect_grace: float = 15.0

    @property
    def robot_ip(self) -> str:
        te = self.team // 100
        am = self.team % 100
        return f"10.{te}.{am}.2"


def load_config() -> Config:
    """Load config from CLI args, falling back to config.ini for defaults."""
    # First pass: read config.ini if it exists
    ini_defaults: dict[str, str] = {}
    config_path = Path(CONFIG_FILE)
    if config_path.exists():
        cp = configparser.ConfigParser()
        cp.read(config_path)
        if cp.has_section("bridge"):
            ini_defaults = dict(cp["bridge"])

    parser = argparse.ArgumentParser(
        description="FRC OBS Recording Bridge — auto start/stop OBS recording from FMS match state"
    )
    parser.add_argument(
        "--team",
        type=int,
        default=ini_defaults.get("team"),
        help="Team number (required). Used to derive robot IP 10.TE.AM.2",
    )
    parser.add_argument(
        "--obs-host",
        default=ini_defaults.get("obs_host", "localhost"),
        help="OBS WebSocket host (default: localhost)",
    )
    parser.add_argument(
        "--obs-port",
        type=int,
        default=int(ini_defaults.get("obs_port", "4455")),
        help="OBS WebSocket port (default: 4455)",
    )
    parser.add_argument(
        "--obs-password",
        default=ini_defaults.get("obs_password", ""),
        help="OBS WebSocket password (default: empty)",
    )
    parser.add_argument(
        "--stop-delay",
        type=float,
        default=float(ini_defaults.get("stop_delay", "10")),
        help="Seconds to wait after match end before stopping recording (default: 10)",
    )
    parser.add_argument(
        "--poll-interval",
        type=float,
        default=float(ini_defaults.get("poll_interval", "1.0")),
        help="Poll interval in seconds (default: 1.0)",
    )
    parser.add_argument(
        "--log-level",
        default=ini_defaults.get("log_level", "INFO"),
        choices=["DEBUG", "INFO", "WARNING", "ERROR"],
        help="Log level (default: INFO)",
    )
    parser.add_argument(
        "--auto-teleop-gap",
        type=float,
        default=float(ini_defaults.get("auto_teleop_gap", "5")),
        help="Max seconds of disabled between auto and teleop before stopping (default: 5)",
    )
    parser.add_argument(
        "--nt-disconnect-grace",
        type=float,
        default=float(ini_defaults.get("nt_disconnect_grace", "15")),
        help="Grace period in seconds before treating NT disconnect as match over (default: 15)",
    )

    args = parser.parse_args()

    if args.team is None:
        parser.error("--team is required (or set team= in config.ini)")

    return Config(
        team=args.team,
        obs_host=args.obs_host,
        obs_port=args.obs_port,
        obs_password=args.obs_password,
        stop_delay=args.stop_delay,
        poll_interval=args.poll_interval,
        log_level=args.log_level,
        auto_teleop_gap=args.auto_teleop_gap,
        nt_disconnect_grace=args.nt_disconnect_grace,
    )
