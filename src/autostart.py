"""Manage launch-on-login registration for Windows and macOS."""

import logging
import platform
import sys
from pathlib import Path

log = logging.getLogger(__name__)

_APP_NAME = "RavenLink"
_PLIST_LABEL = "ca.team1310.ravenlink"


class AutoStart:
    """Register/unregister the bridge to launch on user login."""

    @staticmethod
    def enable() -> bool:
        exe = _get_exe_command()
        system = platform.system()
        try:
            if system == "Windows":
                return _windows_enable(exe)
            elif system == "Darwin":
                return _macos_enable(exe)
            else:
                log.warning("Auto-start not supported on %s", system)
                return False
        except Exception as e:
            log.warning("Failed to enable auto-start: %s", e)
            return False

    @staticmethod
    def disable() -> bool:
        system = platform.system()
        try:
            if system == "Windows":
                return _windows_disable()
            elif system == "Darwin":
                return _macos_disable()
            else:
                return False
        except Exception as e:
            log.warning("Failed to disable auto-start: %s", e)
            return False

    @staticmethod
    def is_enabled() -> bool:
        system = platform.system()
        try:
            if system == "Windows":
                return _windows_is_enabled()
            elif system == "Darwin":
                return _macos_is_enabled()
            else:
                return False
        except Exception:
            return False

    @staticmethod
    def sync(should_be_enabled: bool) -> None:
        current = AutoStart.is_enabled()
        if should_be_enabled and not current:
            if AutoStart.enable():
                log.info("Auto-start registered")
        elif not should_be_enabled and current:
            if AutoStart.disable():
                log.info("Auto-start unregistered")


def _get_exe_command() -> str:
    if getattr(sys, "frozen", False):
        return f'"{sys.executable}" --minimized'
    else:
        return f'"{sys.executable}" -m src.main --minimized'


# --- Windows (Registry) ---

def _windows_enable(exe: str) -> bool:
    import winreg
    key = winreg.OpenKey(
        winreg.HKEY_CURRENT_USER,
        r"Software\Microsoft\Windows\CurrentVersion\Run",
        0, winreg.KEY_SET_VALUE,
    )
    winreg.SetValueEx(key, _APP_NAME, 0, winreg.REG_SZ, exe)
    winreg.CloseKey(key)
    return True


def _windows_disable() -> bool:
    import winreg
    try:
        key = winreg.OpenKey(
            winreg.HKEY_CURRENT_USER,
            r"Software\Microsoft\Windows\CurrentVersion\Run",
            0, winreg.KEY_SET_VALUE,
        )
        winreg.DeleteValue(key, _APP_NAME)
        winreg.CloseKey(key)
    except FileNotFoundError:
        pass
    return True


def _windows_is_enabled() -> bool:
    import winreg
    try:
        key = winreg.OpenKey(
            winreg.HKEY_CURRENT_USER,
            r"Software\Microsoft\Windows\CurrentVersion\Run",
            0, winreg.KEY_READ,
        )
        winreg.QueryValueEx(key, _APP_NAME)
        winreg.CloseKey(key)
        return True
    except FileNotFoundError:
        return False


# --- macOS (LaunchAgent) ---

def _plist_path() -> Path:
    return Path.home() / "Library" / "LaunchAgents" / f"{_PLIST_LABEL}.plist"


def _macos_enable(exe: str) -> bool:
    plist = _plist_path()
    plist.parent.mkdir(parents=True, exist_ok=True)

    parts = exe.split()
    program_args = "\n".join(f"        <string>{p.strip('\"')}</string>" for p in parts)

    plist.write_text(f"""<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{_PLIST_LABEL}</string>
    <key>ProgramArguments</key>
    <array>
{program_args}
    </array>
    <key>RunAtLoad</key>
    <true/>
</dict>
</plist>
""")
    return True


def _macos_disable() -> bool:
    plist = _plist_path()
    if plist.exists():
        plist.unlink()
    return True


def _macos_is_enabled() -> bool:
    return _plist_path().exists()
