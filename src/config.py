"""Configuration for RavenLink — CLI args + config.ini support."""

import argparse
import configparser
import threading
from dataclasses import dataclass, field
from pathlib import Path

CONFIG_FILE = "config.ini"


@dataclass
class Config:
    team: int
    obs_host: str = "localhost"
    obs_port: int = 4455
    obs_password: str = ""
    stop_delay: float = 10.0
    poll_interval: float = 0.05
    log_level: str = "INFO"
    auto_teleop_gap: float = 5.0
    nt_disconnect_grace: float = 15.0
    record_trigger: str = "fms"
    launch_on_login: bool = True

    # Telemetry
    nt_paths: list[str] = field(default_factory=lambda: ["/SmartDashboard/", "/Shuffleboard/"])
    data_dir: Path = field(default_factory=lambda: Path("./data"))
    retention_days: int = 30

    # RavenBrain
    ravenbrain_url: str = ""
    ravenbrain_username: str = ""
    ravenbrain_password: str = ""
    ravenbrain_batch_size: int = 500
    ravenbrain_upload_interval: float = 10.0

    # Dashboard
    dashboard_enabled: bool = True
    dashboard_port: int = 8080

    # Runtime flag for hot-reload signaling
    _config_changed: bool = field(default=False, repr=False)
    _lock: threading.Lock = field(default_factory=threading.Lock, repr=False)

    @property
    def robot_ip(self) -> str:
        te = self.team // 100
        am = self.team % 100
        return f"10.{te}.{am}.2"

    def mark_changed(self) -> None:
        with self._lock:
            self._config_changed = True

    def consume_changed(self) -> bool:
        with self._lock:
            changed = self._config_changed
            self._config_changed = False
            return changed

    def reload_from_ini(self) -> list[str]:
        """Re-read config.ini and update hot-reloadable fields. Returns list of changed field names."""
        config_path = Path(CONFIG_FILE)
        if not config_path.exists():
            return []

        cp = configparser.ConfigParser()
        cp.read(config_path)

        changed: list[str] = []

        if cp.has_section("bridge"):
            bridge = cp["bridge"]
            _reload_float(self, bridge, "stop_delay", changed)
            _reload_float(self, bridge, "poll_interval", changed)
            _reload_float(self, bridge, "auto_teleop_gap", changed)
            _reload_float(self, bridge, "nt_disconnect_grace", changed)
            _reload_str(self, bridge, "log_level", changed)
            _reload_str(self, bridge, "record_trigger", changed)
            _reload_bool(self, bridge, "launch_on_login", changed)

        if cp.has_section("telemetry"):
            tel = cp["telemetry"]
            if "nt_paths" in tel:
                new_paths = [p.strip() for p in tel["nt_paths"].split(",") if p.strip()]
                if new_paths != self.nt_paths:
                    self.nt_paths = new_paths
                    changed.append("nt_paths")
            _reload_int(self, tel, "retention_days", changed)

        if cp.has_section("ravenbrain"):
            rb = cp["ravenbrain"]
            _reload_int(self, rb, "batch_size", changed, attr="ravenbrain_batch_size")
            _reload_float(self, rb, "upload_interval", changed, attr="ravenbrain_upload_interval")

        return changed

    def save_to_ini(self) -> None:
        """Write current config to config.ini."""
        cp = configparser.ConfigParser()

        cp["bridge"] = {
            "team": str(self.team),
            "obs_host": self.obs_host,
            "obs_port": str(self.obs_port),
            "obs_password": self.obs_password,
            "stop_delay": str(self.stop_delay),
            "poll_interval": str(self.poll_interval),
            "log_level": self.log_level,
            "auto_teleop_gap": str(self.auto_teleop_gap),
            "nt_disconnect_grace": str(self.nt_disconnect_grace),
            "record_trigger": self.record_trigger,
            "launch_on_login": str(self.launch_on_login).lower(),
        }
        cp["telemetry"] = {
            "nt_paths": ", ".join(self.nt_paths),
            "data_dir": str(self.data_dir),
            "retention_days": str(self.retention_days),
        }
        cp["ravenbrain"] = {
            "url": self.ravenbrain_url,
            "username": self.ravenbrain_username,
            "password": self.ravenbrain_password,
            "batch_size": str(self.ravenbrain_batch_size),
            "upload_interval": str(self.ravenbrain_upload_interval),
        }
        cp["dashboard"] = {
            "enabled": str(self.dashboard_enabled).lower(),
            "port": str(self.dashboard_port),
        }

        with open(CONFIG_FILE, "w") as f:
            cp.write(f)


def _reload_float(cfg: Config, section: dict, key: str, changed: list[str], attr: str | None = None) -> None:
    attr = attr or key
    if key in section:
        new_val = float(section[key])
        if getattr(cfg, attr) != new_val:
            setattr(cfg, attr, new_val)
            changed.append(attr)


def _reload_int(cfg: Config, section: dict, key: str, changed: list[str], attr: str | None = None) -> None:
    attr = attr or key
    if key in section:
        new_val = int(section[key])
        if getattr(cfg, attr) != new_val:
            setattr(cfg, attr, new_val)
            changed.append(attr)


def _reload_str(cfg: Config, section: dict, key: str, changed: list[str], attr: str | None = None) -> None:
    attr = attr or key
    if key in section:
        new_val = section[key]
        if getattr(cfg, attr) != new_val:
            setattr(cfg, attr, new_val)
            changed.append(attr)


def _reload_bool(cfg: Config, section: dict, key: str, changed: list[str], attr: str | None = None) -> None:
    attr = attr or key
    if key in section:
        new_val = section[key].lower() in ("true", "1", "yes")
        if getattr(cfg, attr) != new_val:
            setattr(cfg, attr, new_val)
            changed.append(attr)


def load_config() -> Config:
    """Load config from CLI args, falling back to config.ini for defaults."""
    ini_defaults: dict[str, str] = {}
    ini_telemetry: dict[str, str] = {}
    ini_ravenbrain: dict[str, str] = {}
    ini_dashboard: dict[str, str] = {}

    config_path = Path(CONFIG_FILE)
    if config_path.exists():
        cp = configparser.ConfigParser()
        cp.read(config_path)
        if cp.has_section("bridge"):
            ini_defaults = dict(cp["bridge"])
        if cp.has_section("telemetry"):
            ini_telemetry = dict(cp["telemetry"])
        if cp.has_section("ravenbrain"):
            ini_ravenbrain = dict(cp["ravenbrain"])
        if cp.has_section("dashboard"):
            ini_dashboard = dict(cp["dashboard"])

    parser = argparse.ArgumentParser(
        description="RavenLink — FRC robot data bridge: OBS recording, NT telemetry, RavenBrain upload"
    )
    parser.add_argument(
        "--team", type=int,
        default=ini_defaults.get("team"),
        help="Team number (required). Used to derive robot IP 10.TE.AM.2",
    )
    parser.add_argument(
        "--obs-host",
        default=ini_defaults.get("obs_host", "localhost"),
        help="OBS WebSocket host (default: localhost)",
    )
    parser.add_argument(
        "--obs-port", type=int,
        default=int(ini_defaults.get("obs_port", "4455")),
        help="OBS WebSocket port (default: 4455)",
    )
    parser.add_argument(
        "--obs-password",
        default=ini_defaults.get("obs_password", ""),
        help="OBS WebSocket password (default: empty)",
    )
    parser.add_argument(
        "--stop-delay", type=float,
        default=float(ini_defaults.get("stop_delay", "10")),
        help="Seconds to wait after match end before stopping recording (default: 10)",
    )
    parser.add_argument(
        "--poll-interval", type=float,
        default=float(ini_defaults.get("poll_interval", "0.05")),
        help="Poll interval in seconds (default: 0.05)",
    )
    parser.add_argument(
        "--log-level",
        default=ini_defaults.get("log_level", "INFO"),
        choices=["DEBUG", "INFO", "WARNING", "ERROR"],
        help="Log level (default: INFO)",
    )
    parser.add_argument(
        "--auto-teleop-gap", type=float,
        default=float(ini_defaults.get("auto_teleop_gap", "5")),
        help="Max seconds of disabled between auto and teleop before stopping (default: 5)",
    )
    parser.add_argument(
        "--nt-disconnect-grace", type=float,
        default=float(ini_defaults.get("nt_disconnect_grace", "15")),
        help="Grace period in seconds before treating NT disconnect as match over (default: 15)",
    )
    parser.add_argument(
        "--record-trigger",
        default=ini_defaults.get("record_trigger", "fms"),
        choices=["fms", "auto", "any"],
        help="When to start recording: fms (competition only), auto (auto mode enable), any (any enable) (default: fms)",
    )
    parser.add_argument(
        "--no-launch-on-login", action="store_true",
        default=False,
        help="Disable launch-on-login registration",
    )

    # Telemetry args
    parser.add_argument(
        "--nt-paths",
        default=ini_telemetry.get("nt_paths", "/SmartDashboard/, /Shuffleboard/"),
        help="NT path prefixes to subscribe to, comma-separated (default: /SmartDashboard/, /Shuffleboard/)",
    )
    parser.add_argument(
        "--data-dir",
        default=ini_telemetry.get("data_dir", "./data"),
        help="Local data directory for JSONL files (default: ./data)",
    )

    # RavenBrain args
    parser.add_argument(
        "--ravenbrain-url",
        default=ini_ravenbrain.get("url", ""),
        help="RavenBrain server URL (default: empty, local-only mode)",
    )
    parser.add_argument(
        "--ravenbrain-username",
        default=ini_ravenbrain.get("username", ""),
        help="RavenBrain service account username (default: empty)",
    )
    parser.add_argument(
        "--ravenbrain-password",
        default=ini_ravenbrain.get("password", ""),
        help="RavenBrain service account password (default: empty)",
    )

    # Dashboard args
    parser.add_argument(
        "--minimized", action="store_true",
        default=False,
        help="Start minimized to system tray (no console window)",
    )

    args = parser.parse_args()

    if args.team is None:
        parser.error("--team is required (or set team= in config.ini)")

    launch_on_login_ini = ini_defaults.get("launch_on_login", "true").lower() in ("true", "1", "yes")
    nt_paths = [p.strip() for p in args.nt_paths.split(",") if p.strip()]

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
        record_trigger=args.record_trigger,
        launch_on_login=launch_on_login_ini and not args.no_launch_on_login,
        nt_paths=nt_paths,
        data_dir=Path(args.data_dir),
        retention_days=int(ini_telemetry.get("retention_days", "30")),
        ravenbrain_url=args.ravenbrain_url,
        ravenbrain_username=args.ravenbrain_username,
        ravenbrain_password=args.ravenbrain_password,
        ravenbrain_batch_size=int(ini_ravenbrain.get("batch_size", "500")),
        ravenbrain_upload_interval=float(ini_ravenbrain.get("upload_interval", "10")),
        dashboard_enabled=ini_dashboard.get("enabled", "true").lower() in ("true", "1", "yes"),
        dashboard_port=int(ini_dashboard.get("port", "8080")),
    )
