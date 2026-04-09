# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

FRC OBS Recording Bridge — a Python app that auto-starts/stops OBS Studio recording based on FRC match state. Runs on the Driver Station laptop, reads FMS state from the robot via NetworkTables (NT4), and controls OBS via its WebSocket API. Designed for FRC team 1310.

## Commands

```bash
# Install dependencies
pip install -r requirements.txt

# Run from source
python -m src.main --team 1310

# Run tests
pytest

# Run a single test class or test
pytest tests/test_state_machine.py::TestFullMatchLifecycle
pytest tests/test_state_machine.py::TestFullMatchLifecycle::test_normal_match

# Build single-file Windows exe
pip install pyinstaller
pyinstaller build.spec
# Output: dist/frc-obs-bridge.exe
```

## Architecture

The app is a polling loop with four components:

- **`src/config.py`** — `Config` dataclass + `load_config()` that merges CLI args over `config.ini` defaults. Team number is the only required field; everything else has sensible defaults.
- **`src/nt_client.py`** — Wraps `pyntcore` (NT4 client). Connects to the robot at `10.TE.AM.2:5810`, subscribes to `FMSInfo/FMSControlData`. Returns `FMSState` — a dataclass that decodes the 6-bit FMS bitmask (enabled, auto, test, estop, fms_attached, ds_attached). `raw == -1` means disconnected.
- **`src/obs_client.py`** — Wraps `obsws-python` `ReqClient`. Handles connection/reconnect, start/stop recording with retry. Tolerates "already recording" and "not active" as success.
- **`src/state_machine.py`** — Pure-logic state machine (`MatchStateMachine`) with no I/O. States: `IDLE → RECORDING_AUTO → RECORDING_TELEOP → STOP_PENDING → IDLE`. Takes `FMSState` in, returns `list[Action]` out. Injectable clock for testing.
- **`src/main.py`** — Wires everything together in a `while` loop: polls NT, feeds state machine, executes actions on OBS. Handles SIGINT/SIGTERM for clean shutdown.

### Key design decision

The state machine is intentionally side-effect-free. It doesn't call OBS or NT directly — it returns `Action.START_RECORD` / `Action.STOP_RECORD` and the main loop executes them. This makes it fully testable with a `FakeClock` and synthetic `FMSState` values.

### FMS bitmask layout

```
bit 0 (0x01): enabled
bit 1 (0x02): auto mode
bit 2 (0x04): test mode
bit 3 (0x08): e-stop
bit 4 (0x10): FMS attached
bit 5 (0x20): DS attached
```

Recording only starts when FMS is attached + robot is enabled (competition/practice matches on the field). Home practice without FMS intentionally does not trigger recording.

### State transitions to know

- The brief disabled gap between auto and teleop (up to `auto_teleop_gap` seconds, default 5) is tolerated so recording isn't split.
- FMS detach during recording triggers a shortened 3-second stop grace (not the full `stop_delay`).
- NT disconnect starts a separate grace period (`nt_disconnect_grace`, default 15s) before stopping.
- Re-enabling during `STOP_PENDING` cancels the stop and resumes recording.

## Testing

Tests use `FakeClock` (injectable into `MatchStateMachine`) and the `make_fms()` helper to construct `FMSState` from flags. No real OBS or NT connections are needed. Tests cover: full match lifecycle, auto-teleop gap tolerance, stop delay, FMS detach, re-enable during stop, multiple sequential matches, NT disconnect grace, e-stop, and bitmask parsing.
