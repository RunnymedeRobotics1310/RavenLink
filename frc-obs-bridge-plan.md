# FRC OBS Recording Bridge — Claude Code Execution Plan

## Overview

Build a Python application that runs on the FRC Driver Station laptop and automatically starts/stops OBS recording based on FMS match state. It bridges WPILib NetworkTables (match state source) to OBS Studio's WebSocket API (recording control).

**Target platform:** Windows 10/11 (Driver Station laptop)
**Language:** Python 3.11+
**Packaging:** PyInstaller single-file `.exe` for competition-day deployment

---

## Architecture

```
┌──────────┐       NT4 (5810)       ┌───────────┐
│  Robot    │ ◄────────────────────► │  DS Laptop │
│ (rio)     │   FMSInfo/FMSControlData │           │
└──────────┘                        │  ┌───────────────────┐
                                    │  │ frc-obs-bridge.py  │
                                    │  │  - NT4 client      │
                                    │  │  - State machine    │
                                    │  │  - OBS WS client    │
                                    │  └────────┬──────────┘
                                    │           │ WebSocket :4455
                                    │  ┌────────▼──────────┐
                                    │  │   OBS Studio       │
                                    │  │  (recording)       │
                                    │  └───────────────────┘
                                    └───────────┘
```

---

## Dependencies

```
pyntcore          # WPILib NT4 client (from robotpy-cscore or robotpy meta-package)
obsws-python      # OBS WebSocket v5 client (OBS 28+)
```

Dev/build dependencies:
```
pyinstaller       # Package to .exe
```

---

## FMSControlData Bitmask Reference

The robot publishes `/FMSInfo/FMSControlData` as an integer. Bit layout from WPILib `DriverStation.java`:

| Bit | Mask   | Meaning         |
|-----|--------|-----------------|
| 0   | 0x01   | Enabled         |
| 1   | 0x02   | Auto mode       |
| 2   | 0x04   | Test mode       |
| 3   | 0x08   | Emergency Stop  |
| 4   | 0x10   | FMS Attached    |
| 5   | 0x20   | DS Attached     |

Key derived states:
- **Match start (auto):** `(value & 0x11) == 0x11` and `(value & 0x02) == 0x02` → enabled + FMS attached + auto
- **Teleop:** `(value & 0x11) == 0x11` and `(value & 0x02) == 0x00` and `(value & 0x04) == 0x00` → enabled + FMS attached + not auto + not test
- **Match end (disabled after teleop):** `(value & 0x01) == 0x00` after having been in teleop

---

## State Machine

```
                 ┌─────────┐
                 │  IDLE   │◄──────────────────────┐
                 └────┬────┘                        │
                      │ FMS attached                │ stop_delay elapsed
                      │ + enabled                   │ OR FMS detached
                      ▼                             │
              ┌───────────────┐                     │
              │  RECORDING    │                     │
              │  (auto phase) │                     │
              └───────┬───────┘                     │
                      │ disabled briefly            │
                      │ (auto→teleop gap)           │
                      ▼                             │
              ┌───────────────┐                     │
              │  RECORDING    │                     │
              │  (teleop)     │                     │
              └───────┬───────┘                     │
                      │ disabled                    │
                      │ (match end)                 │
                      ▼                             │
              ┌───────────────┐                     │
              │  STOP_PENDING │─────────────────────┘
              │  (10s delay)  │
              └───────────────┘
```

### State transitions — detailed rules

1. **IDLE → RECORDING:** `FMS_attached` AND `enabled` transitions to true. Call `obs.start_record()`. Set `phase = AUTO`.
2. **RECORDING (auto) → RECORDING (teleop):** `enabled` AND NOT `auto_mode` AND NOT `test_mode`. Set `phase = TELEOP`. No OBS action.
3. **RECORDING → RECORDING (handle auto↔teleop gap):** If `disabled` but `phase == AUTO` and duration of disable < 5 seconds, remain in RECORDING. This covers the brief disabled window between auto and teleop.
4. **RECORDING (teleop) → STOP_PENDING:** `disabled` (bit 0 cleared). Start a 10-second countdown.
5. **STOP_PENDING → IDLE:** After 10-second delay, call `obs.stop_record()`. Reset all state.
6. **STOP_PENDING → RECORDING:** If `enabled` fires again during the 10s window (e.g., false trigger), cancel the stop and resume.
7. **Any state → IDLE (fallback):** If FMS detached (`bit 4` cleared) while recording, stop recording immediately with a 3-second grace period.

---

## Implementation Tasks

### Task 1: Project scaffolding

Create the project structure:

```
frc-obs-bridge/
├── README.md
├── requirements.txt
├── pyproject.toml          # optional, for modern packaging
├── src/
│   ├── __init__.py
│   ├── main.py             # Entry point, arg parsing, orchestration
│   ├── nt_client.py        # NetworkTables connection and FMS state reading
│   ├── obs_client.py       # OBS WebSocket connection and record control
│   ├── state_machine.py    # Match state machine logic
│   └── config.py           # Configuration (team number, OBS host/port/password, timings)
├── tests/
│   ├── test_state_machine.py
│   └── test_bitmask.py
└── build.spec              # PyInstaller spec for single-file exe
```

`config.py` should support:
- `--team` (required): Team number, used to derive robot IP `10.TE.AM.2`
- `--obs-host` (default: `localhost`)
- `--obs-port` (default: `4455`)
- `--obs-password` (default: empty string)
- `--stop-delay` (default: `10` seconds)
- `--poll-interval` (default: `0.1` seconds / 100ms)
- `--log-level` (default: `INFO`)

Use `argparse` for CLI args. Also support a `config.ini` file for persistent settings so the user doesn't have to pass args every time.

### Task 2: NetworkTables client (`nt_client.py`)

Implement a class `NTClient` that:

1. Creates an `ntcore.NetworkTableInstance` (use `.getDefault()` or `.create()`)
2. Starts NT4 client mode: `inst.startClient4("frc-obs-bridge")`
3. Sets server to `10.{team//100}.{team%100}.2` port 5810
4. Subscribes to `/FMSInfo/FMSControlData` (integer topic)
5. Exposes a method `get_fms_state() -> FMSState` that returns a dataclass:

```python
@dataclass
class FMSState:
    enabled: bool
    auto_mode: bool
    test_mode: bool
    estop: bool
    fms_attached: bool
    ds_attached: bool
    raw: int
```

6. Exposes a `connected` property that checks if NT is connected (use `inst.isConnected()`)
7. Handles disconnection gracefully — `get_fms_state()` returns a "disconnected" state with all False

### Task 3: OBS WebSocket client (`obs_client.py`)

Implement a class `OBSClient` that:

1. Wraps `obsws_python.ReqClient`
2. Connects to OBS on init (host, port, password from config)
3. Exposes methods:
   - `start_recording()` — calls `client.start_record()`, logs, returns success bool
   - `stop_recording()` — calls `client.stop_record()`, logs, returns success bool
   - `is_recording() -> bool` — calls `client.get_record_status()` to check
   - `is_connected() -> bool`
4. All methods wrapped in try/except — OBS might not be running, WebSocket might drop
5. Auto-reconnect logic: if a call fails with connection error, attempt reconnect once, then retry
6. Log all OBS actions clearly (timestamps, success/failure)

### Task 4: State machine (`state_machine.py`)

Implement `MatchStateMachine`:

1. States: `IDLE`, `RECORDING_AUTO`, `RECORDING_TELEOP`, `STOP_PENDING`
2. `update(fms_state: FMSState) -> list[Action]` — pure function that takes current FMS state, returns list of actions (`START_RECORD`, `STOP_RECORD`, or empty)
3. Internal tracking:
   - `state`: current state
   - `phase_entered_at`: timestamp of last state change
   - `disabled_at`: timestamp when robot went disabled (for gap detection)
   - `stop_pending_at`: timestamp when stop was scheduled
4. Implement all transitions from the state machine diagram above
5. The auto→teleop disabled gap tolerance should be configurable (default 5 seconds)
6. **This must be purely logic — no OBS or NT calls.** This makes it testable.

### Task 5: Main loop (`main.py`)

1. Parse config (CLI args + optional config.ini)
2. Initialize `NTClient`, `OBSClient`, `MatchStateMachine`
3. Print startup banner with config summary
4. Run poll loop at `poll_interval`:
   ```
   while True:
       fms_state = nt_client.get_fms_state()
       actions = state_machine.update(fms_state)
       for action in actions:
           if action == START_RECORD:
               obs_client.start_recording()
           elif action == STOP_RECORD:
               obs_client.stop_recording()
       log_status_periodically(fms_state, state_machine.state)
       sleep(poll_interval)
   ```
5. Status logging: every 5 seconds, log current state (NT connected, FMS state, SM state, OBS recording)
6. Graceful shutdown on Ctrl+C: if currently recording, stop recording before exit
7. Handle OBS not running at startup — warn but don't crash, keep retrying connection

### Task 6: Tests

**`test_bitmask.py`:**
- Test `FMSState` parsing from known integer values
- Test all individual bits
- Test common combinations: `0x13` (enabled + auto + FMS), `0x11` (enabled + teleop + FMS), `0x10` (disabled + FMS), `0x00` (disconnected)

**`test_state_machine.py`:**
- Test full match lifecycle: IDLE → auto start → auto/teleop gap → teleop → match end → IDLE
- Test the auto→teleop disabled gap is tolerated (no false stop)
- Test stop delay countdown
- Test FMS detach emergency stop
- Test re-enable during STOP_PENDING cancels the stop
- Test multiple matches in sequence (state resets properly)
- All tests use mocked timestamps (inject a clock function)

### Task 7: PyInstaller packaging

Create `build.spec` or a build script:

```bash
pyinstaller --onefile --name frc-obs-bridge --console src/main.py
```

- `--console` because we want to see logs in a terminal window
- Document in README: how to build, how to run the exe, how to set up OBS WebSocket

### Task 8: README.md

Write a practical README covering:

1. What this does (one paragraph)
2. Prerequisites: OBS 28+ with WebSocket enabled, Python 3.11+ (for dev), or just the .exe
3. OBS setup: enable WebSocket server, set password
4. Usage: `frc-obs-bridge.exe --team 1234` (and all other flags)
5. Config file option: `config.ini` example
6. How it works (brief state machine description)
7. Troubleshooting: common issues (OBS not detected, NT not connecting, firewall)
8. Building from source

---

## Edge Cases to Handle

- **OBS not running at startup:** Warn, keep polling for OBS connection, start recording when OBS becomes available mid-match if state machine says we should be recording
- **Robot reboots mid-match:** NT disconnects briefly. Don't stop recording if FMS was attached. Use a grace period (15s?) before treating NT disconnect as match over.
- **Practice matches vs qual matches:** Both have FMS attached. This is fine — record all of them.
- **DS laptop loses connection to robot:** Similar to robot reboot. Grace period.
- **Multiple matches without restart:** State machine must cleanly reset to IDLE after each match.
- **E-stop:** If e-stop bit is set, treat as match end (with delay).

---

## Acceptance Criteria

1. Running `python src/main.py --team XXXX` connects to NT and OBS, prints status
2. When FMS match starts (auto enable), OBS recording starts within 500ms
3. Auto→teleop transition does NOT interrupt recording
4. When match ends (teleop disable), OBS recording stops after configurable delay
5. All state transitions are logged with timestamps
6. `test_state_machine.py` passes with 100% of lifecycle scenarios
7. PyInstaller produces a working single-file `.exe`
8. Ctrl+C cleanly stops recording if active and exits

---

## Execution Notes for Claude Code

- Start with Tasks 1-4 (scaffolding, NT client, OBS client, state machine) — these are independent
- Task 5 (main loop) integrates them
- Task 6 (tests) can be written alongside Tasks 2-4
- Task 7-8 (packaging, docs) are last
- Use `pyntcore` — this is the correct package name on PyPI for the NT4 client
- Use `obsws-python` — this is the v5 WebSocket client (NOT `obs-websocket-py` which is v4)
- The state machine being pure logic with no side effects is a hard requirement — it must be testable without NT or OBS connections
