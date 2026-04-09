# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

FRC OBS Bridge + NT Data Collector — a Python app for the Driver Station laptop that:
1. Auto-starts/stops OBS Studio recording based on FRC match state
2. Subscribes to configurable NetworkTables paths and logs all value changes to JSONL files
3. Uploads telemetry data to RavenBrain via a store-and-forward system
4. Provides a web dashboard at localhost:8080 for status monitoring and config editing
5. Runs as a system tray icon with at-a-glance status

Designed for FRC team 1310. The RavenBrain server-side API lives in `~/src/1310/RavenBrain`.

## Commands

```bash
# Install dependencies
pip install -r requirements.txt

# Run from source
python -m src.main --team 1310

# Run minimized (system tray only, no console)
python -m src.main --team 1310 --minimized

# Run tests
pytest

# Run a single test class or test
pytest tests/test_state_machine.py::TestFullMatchLifecycle
pytest tests/test_uploader.py::TestUploaderFileManagement

# Build single-file Windows exe
pip install pyinstaller
pyinstaller build.spec
# Output: dist/frc-obs-bridge.exe
```

## Architecture

The app is a 50ms polling loop with these components:

### Core (existing)
- **`src/config.py`** — `Config` dataclass with `[bridge]`, `[telemetry]`, `[ravenbrain]`, `[dashboard]` sections. Merges CLI args over `config.ini`. Supports hot-reload via `reload_from_ini()` and `save_to_ini()` for the web dashboard config editor.
- **`src/nt_client.py`** — Wraps `pyntcore` NT4 client. Connects to `10.TE.AM.2:5810`, subscribes to `FMSInfo/FMSControlData`. Returns `FMSState` dataclass. Exposes `.instance` property for NTLogger to share the connection.
- **`src/obs_client.py`** — Wraps `obsws-python` ReqClient. Start/stop recording with reconnect and retry.
- **`src/state_machine.py`** — Pure-logic state machine (no I/O). States: `IDLE → RECORDING_AUTO → RECORDING_TELEOP → STOP_PENDING → IDLE`. Injectable clock for testing.

### NT Data Collection (new)
- **`src/nt_logger.py`** — `NTLogger` uses `ntcore.MultiSubscriber` + `NetworkTableListenerPoller` to subscribe to configurable path prefixes (`sendAll=True, keepDuplicates=True, pollStorage=500`). Writes all value changes to JSONL files in `data_dir/pending/`. Manages session lifecycle (start/end on NT connect/disconnect). Emits `match_start`/`match_end` markers with FMS metadata. `"/FMSInfo/"` is always subscribed. Supports `update_paths()` for hot-reload.
- **`src/uploader.py`** — `Uploader` scans `pending/` for completed JSONL files and uploads to RavenBrain in batches. 3-phase upload: create session → post data batches → complete session. Progress tracking via `.progress` files for crash recovery. Exponential backoff on failure. Does nothing if `ravenbrain_url` is empty.

### Dashboard & UI (new)
- **`src/web_dashboard.py`** — Flask web server at `localhost:8080`. Status page with live updates (1s poll), log viewer, and config editor with hot-reload. Runs on daemon thread.
- **`src/tray_icon.py`** — `pystray` system tray icon. Green/yellow/red status, tooltip summary, right-click menu with "Open Dashboard" and "Quit".
- **`src/bridge_status.py`** — Shared `BridgeStatus` dataclass updated by all components each loop iteration.
- **`src/autostart.py`** — Manages launch-on-login via Windows Registry Run key or macOS LaunchAgent.

### Main Loop (`src/main.py`)
Wires all components: polls NT → feeds state machine → emits match markers to NTLogger → executes OBS actions → drains NT event queue → runs upload check → updates dashboard status. Handles config hot-reload, SIGINT/SIGTERM shutdown.

### Key design decisions

1. **State machine is side-effect-free** — returns `Action` enums, main loop dispatches them.
2. **Match markers decouple from OBS** — `match_start` fires at RECORDING_AUTO entry, `match_end` fires at STOP_PENDING entry (actual disable time, not 10s later).
3. **NTLogger uses NetworkTableListenerPoller** — no threading needed, drains event queue from main loop.
4. **Store-and-forward** — always write JSONL locally first, upload when connectivity available.
5. **JSONL format** — one file per session, append-only, crash-safe (only last line can be corrupt).

### FMS bitmask layout

```
bit 0 (0x01): enabled
bit 1 (0x02): auto mode
bit 2 (0x04): test mode
bit 3 (0x08): e-stop
bit 4 (0x10): FMS attached
bit 5 (0x20): DS attached
```

### State transitions to know

- Auto-teleop disabled gap (up to `auto_teleop_gap` seconds, default 5) is tolerated.
- FMS detach → 3-second stop grace (not full `stop_delay`).
- NT disconnect → separate grace period (`nt_disconnect_grace`, default 15s).
- Re-enabling during `STOP_PENDING` cancels the stop.

## Testing

- **State machine tests** — `FakeClock` + `make_fms()` helper. No real OBS or NT.
- **NTLogger tests** — Mock ntcore module, test JSONL output, session lifecycle, match markers.
- **Uploader tests** — Mock HTTP, test file management, batch upload, progress tracking, backoff.
- **RavenBrain tests** — Micronaut integration test with Testcontainers MySQL.

## Config Sections

| Section | Key fields | Hot-reloadable |
|---------|-----------|----------------|
| `[bridge]` | team, obs_host/port/password, stop_delay, poll_interval, log_level, launch_on_login | log_level, stop_delay, poll_interval, auto_teleop_gap, nt_disconnect_grace, launch_on_login |
| `[telemetry]` | nt_paths, data_dir, retention_days | nt_paths, retention_days |
| `[ravenbrain]` | url, api_key, batch_size, upload_interval | batch_size, upload_interval |
| `[dashboard]` | enabled, port | — (restart required) |
