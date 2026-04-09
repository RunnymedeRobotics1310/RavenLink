"""System tray icon for at-a-glance bridge status."""

import logging
import threading
import webbrowser
from typing import Optional

log = logging.getLogger(__name__)

try:
    import pystray
    from PIL import Image, ImageDraw
    _AVAILABLE = True
except ImportError:
    _AVAILABLE = False
    log.debug("pystray/Pillow not available — tray icon disabled")


def _make_icon(color: str) -> "Image.Image":
    size = 64
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)
    colors = {
        "green": (34, 197, 94),
        "yellow": (234, 179, 8),
        "red": (239, 68, 68),
        "gray": (156, 163, 175),
    }
    rgb = colors.get(color, colors["gray"])
    draw.ellipse([4, 4, size - 4, size - 4], fill=rgb)
    return img


class TrayIcon:
    """System tray icon with status colors and a right-click menu."""

    def __init__(self, dashboard_url: str = "http://localhost:8080") -> None:
        self._dashboard_url = dashboard_url
        self._icon: Optional["pystray.Icon"] = None
        self._thread: Optional[threading.Thread] = None
        self._tooltip = "FRC OBS Bridge"
        self._color = "gray"
        self._status_text = "IDLE"
        self._nt_text = "NT: Unknown"
        self._obs_text = "OBS: Unknown"
        self._shutdown_callback: Optional[callable] = None

    def start(self, shutdown_callback: Optional[callable] = None) -> None:
        if not _AVAILABLE:
            log.warning("Cannot start tray icon — pystray/Pillow not installed")
            return
        self._shutdown_callback = shutdown_callback
        self._thread = threading.Thread(target=self._run, daemon=True, name="tray-icon")
        self._thread.start()

    def _run(self) -> None:
        self._icon = pystray.Icon(
            name="frc-obs-bridge",
            icon=_make_icon(self._color),
            title=self._tooltip,
            menu=pystray.Menu(
                pystray.MenuItem("Open Dashboard", self._open_dashboard, default=True),
                pystray.Menu.SEPARATOR,
                pystray.MenuItem(lambda _: self._status_text, None, enabled=False),
                pystray.MenuItem(lambda _: self._nt_text, None, enabled=False),
                pystray.MenuItem(lambda _: self._obs_text, None, enabled=False),
                pystray.Menu.SEPARATOR,
                pystray.MenuItem("Quit", self._quit),
            ),
        )
        self._icon.run()

    def update_status(self, status) -> None:
        if not _AVAILABLE or self._icon is None:
            return

        if status.nt_connected and status.obs_connected:
            new_color = "green"
        elif status.nt_connected or status.obs_connected:
            new_color = "yellow"
        else:
            new_color = "red"

        parts = [status.match_state]
        if status.entries_written > 0:
            parts.append(f"{status.entries_written:,} entries")
        if status.obs_recording:
            parts.append("REC")
        self._tooltip = f"FRC Bridge: {' | '.join(parts)}"
        self._status_text = f"State: {status.match_state}"
        self._nt_text = f"NT: {'Connected' if status.nt_connected else 'Disconnected'}"
        self._obs_text = f"OBS: {'Connected' if status.obs_connected else 'Disconnected'}"

        if new_color != self._color:
            self._color = new_color
            self._icon.icon = _make_icon(new_color)

        self._icon.title = self._tooltip

    def _open_dashboard(self, icon, item) -> None:
        webbrowser.open(self._dashboard_url)

    def _quit(self, icon, item) -> None:
        if self._shutdown_callback:
            self._shutdown_callback()
        if self._icon:
            self._icon.stop()

    def stop(self) -> None:
        if self._icon:
            self._icon.stop()
