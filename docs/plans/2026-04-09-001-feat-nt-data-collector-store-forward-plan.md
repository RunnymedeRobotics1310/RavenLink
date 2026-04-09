---
title: "feat: Add NetworkTables data collection with store-and-forward upload to RavenBrain"
type: feat
status: active
date: 2026-04-09
---

# feat: Add NetworkTables Data Collection with Store-and-Forward Upload to RavenBrain

## Overview

Expand AutoOBS from an OBS-only recording bridge into a comprehensive NetworkTables data collector. The bridge will subscribe to configurable NT paths, log all value changes with timestamps to local JSONL files, annotate match boundaries, and upload data to RavenBrain via a new batch telemetry API. OBS recording control remains as a parallel feature.

This spans two repositories:
- **AutoOBS** (Python) — NT data collection, local storage, upload client
- **RavenBrain** (Java/Micronaut) — new telemetry API, database storage, match association

## Problem Statement / Motivation

Currently, the team has no way to capture robot telemetry data during matches or practice. All NT data (sensor values, PID outputs, mechanism states, autonomous paths) is ephemeral — visible on dashboards in real time but lost when the robot disconnects. This data is invaluable for debugging, performance analysis, and strategy refinement.

The Driver Station laptop already runs AutoOBS and has an NT4 connection to the robot. Adding data capture here is the natural place — no additional hardware or network infrastructure needed. During competition matches, there is no internet access, so data must be stored locally and forwarded to RavenBrain when connectivity is available.

## Proposed Solution

### High-Level Data Flow

```
┌──────────┐       NT4 (5810)       ┌──────────────────────────────────────┐
│  Robot   │ ◄─────────────────────►│  DS Laptop (AutoOBS)                │
│  (rio)   │   configurable paths   │                                     │
└──────────┘                        │  ┌────────────────────────────────┐  │
                                    │  │ NTLogger                      │  │
                                    │  │  subscribes to NT paths       │  │
                                    │  │  writes JSONL to data_dir/    │  │
                                    │  └──────────┬─────────────────────┘  │
                                    │             │                        │
                                    │  ┌──────────▼─────────────────────┐  │
                                    │  │ Uploader                      │  │
                                    │  │  reads completed JSONL files  │  │
                                    │  │  POSTs batches to RavenBrain  │──┼──► RavenBrain
                                    │  │  moves uploaded files to      │  │    POST /api/telemetry
                                    │  │  archive/                     │  │
                                    │  └────────────────────────────────┘  │
                                    │                                      │
                                    │  ┌────────────────────────────────┐  │
                                    │  │ OBSClient (existing)          │  │
                                    │  │  start/stop recording         │──┼──► OBS Studio
                                    │  └────────────────────────────────┘  │
                                    └──────────────────────────────────────┘
```

### JSONL File Format

One file per "session" (continuous NT connectivity period). Filename: `{timestamp}_{session_id}.jsonl`

```jsonl
{"ts": 1712678400.000, "type": "session_start", "team": 1310, "robot_ip": "10.13.10.2", "session_id": "abc123"}
{"ts": 1712678400.123, "server_ts": 5200123456, "key": "/FMSInfo/FMSControlData", "type": "int", "value": 51}
{"ts": 1712678400.124, "server_ts": 5200124000, "key": "/FMSInfo/MatchNumber", "type": "int", "value": 12}
{"ts": 1712678400.125, "server_ts": 5200125000, "key": "/FMSInfo/EventName", "type": "string", "value": "ONTO"}
{"ts": 1712678400.126, "server_ts": 5200126000, "key": "/FMSInfo/MatchType", "type": "int", "value": 2}
{"ts": 1712678400.200, "server_ts": 5200200000, "key": "/SmartDashboard/DriveSpeed", "type": "double", "value": 3.14}
{"ts": 1712678400.201, "type": "match_start", "fms_raw": 51, "match_number": 12, "event_name": "ONTO", "match_type": 2, "is_red": true, "station": 1}
{"ts": 1712678415.300, "server_ts": 5215300000, "key": "/SmartDashboard/DriveSpeed", "type": "double", "value": 2.80}
{"ts": 1712678550.100, "type": "match_end", "fms_raw": 16}
{"ts": 1712678600.000, "type": "session_end"}
```

Key design choices:
- `ts` is Python `time.time()` (Unix epoch seconds with fractional precision) — the wall-clock reference
- `server_ts` (optional, on data entries) is the NT4 server timestamp in microseconds (robot FPGA clock) — useful for correlating with robot-side logs
- `type` field for data entries maps to NT4 type names (`boolean`, `int`, `double`, `float`, `string`, `boolean[]`, `double[]`, `float[]`, `int[]`, `string[]`, `raw`). Binary `raw` values are base64-encoded.
- `match_start` and `match_end` are synthetic markers emitted by the state machine. **`match_start`** fires when `Action.START_RECORD` is emitted (FMS attached + enabled). **`match_end`** fires when the robot disables (entering `STOP_PENDING`), NOT after the 10-second stop delay — this gives the actual match end time.
- Match markers include all available FMS metadata captured from `/FMSInfo/*`: `MatchNumber`, `EventName`, `MatchType`, `IsRedAlliance`, `StationNumber`, `ReplayNumber`, `GameSpecificMessage` (read from their respective NT topics at marker time)
- FMS metadata keys (`/FMSInfo/`) are always subscribed regardless of config, since RavenBrain needs them for match association
- Malformed JSONL lines (from mid-write crash) are skipped by the uploader with a warning — only the last line can be corrupt in append-only writes

### Local File Management

```
data_dir/
├── pending/          # Active and ready-to-upload files
│   ├── 2026-04-09T14-30-00_abc123.jsonl   # completed session, awaiting upload
│   └── 2026-04-09T15-00-00_def456.jsonl   # currently being written (active)
├── uploaded/         # Successfully uploaded files (kept for local reference)
│   └── 2026-04-09T13-00-00_xyz789.jsonl
└── failed/           # Files that failed upload after max retries
```

- Active file has an exclusive write lock (or is tracked by session ID in memory)
- On session end (NT disconnect or clean shutdown), the file is closed and becomes eligible for upload
- Uploader scans `pending/` for non-active files, uploads them, moves to `uploaded/`
- `uploaded/` files can be pruned after N days (configurable, default 30)

---

## Technical Approach

### Phase 1: AutoOBS — NT Data Collection & Local Storage

#### 1a. New module: `src/nt_logger.py`

Subscribes to configurable NT paths and writes value changes to JSONL.

```python
class NTLogger:
    """Subscribes to NT topics and logs value changes to JSONL files."""

    def __init__(self, inst: ntcore.NetworkTableInstance, paths: list[str], data_dir: Path):
        ...

    def start_session(self) -> None:
        """Open a new JSONL file, write session_start marker."""

    def poll(self) -> None:
        """Read queued value updates from NT subscribers, write to file."""

    def record_match_event(self, event_type: str, fms_raw: int) -> None:
        """Write a match_start or match_end marker."""

    def end_session(self) -> None:
        """Write session_end marker, close file, make available for upload."""
```

**NT subscription strategy using `MultiSubscriber` + `NetworkTableListenerPoller`:**

pyntcore's `MultiSubscriber` accepts an array of path prefixes and subscribes to every topic matching any prefix. Combined with `NetworkTableListenerPoller`, this provides a non-threaded event queue that fits perfectly into the existing synchronous main loop.

```python
import ntcore

# Subscribe to all topics under these prefixes
self._multi_sub = ntcore.MultiSubscriber(
    inst,
    paths,  # e.g. ["/SmartDashboard/", "/Shuffleboard/", "/FMSInfo/"]
    ntcore.PubSubOptions(
        sendAll=True,         # capture ALL value changes, not just latest
        keepDuplicates=True,  # preserve duplicate values
        pollStorage=500,      # buffer up to 100 values between readQueue() calls
    ),
)

# Create a poller — queues events internally, no background thread
self._poller = ntcore.NetworkTableListenerPoller(inst)
self._poller.addListener(
    self._multi_sub,
    ntcore.EventFlags.kValueAll | ntcore.EventFlags.kConnection,
)
```

In `poll()`, drain the queue and write each event:
```python
def poll(self) -> None:
    for event in self._poller.readQueue():
        if event.is_(ntcore.EventFlags.kConnected):
            self.start_session()
        elif event.is_(ntcore.EventFlags.kDisconnected):
            self.end_session()
        elif event.is_(ntcore.EventFlags.kValueAll):
            value = event.data.value
            topic_name = self._topic_name(event.data.topic)
            self._write_entry(topic_name, value)
```

Key details:
- Path prefixes must end with `/` to match table boundaries (e.g., `"/SmartDashboard/"` not `"/SmartDashboard"`)
- `"/FMSInfo/"` is always prepended to the path list regardless of user config
- `sendAll=True` ensures every value change is captured, not just the latest
- `pollStorage=500` prevents buffer overflow between poll cycles (a robot publishing 20 keys at 50Hz = 1000 events/second)
- `value.value()` auto-extracts the Python value regardless of NT type
- Server timestamps available via `value.server_time()` (microseconds, robot FPGA clock) — captured alongside wall-clock `time.time()` for both reference frames
- The `MultiSubscriber` object must be kept alive as an instance variable; garbage collection ends the subscription
- No threading needed — `readQueue()` is called from the main loop

#### 1b. New module: `src/uploader.py`

Background uploader that sends completed JSONL files to RavenBrain.

```python
class Uploader:
    """Store-and-forward uploader for JSONL telemetry files."""

    def __init__(self, data_dir: Path, ravenbrain_url: str, api_key: str):
        ...

    def upload_pending(self) -> None:
        """Scan pending/, upload completed files in order, move to uploaded/."""

    def _upload_file(self, path: Path) -> bool:
        """Read JSONL file, POST in batches to RavenBrain, return success."""
```

- Reads JSONL file, sends in batches (e.g., 500 entries per POST)
- Uses a simple pre-shared API key for auth (avoids JWT login complexity for machine-to-machine)
- Retries with exponential backoff on failure
- Tracks upload progress per file (in case of partial upload, resume from last batch)
- Runs on a separate thread or as part of the main loop (check every N seconds)

#### 1c. Modified: `src/config.py`

Add new configuration fields:

```ini
[bridge]
team = 1310
poll_interval = 0.05
# Launch automatically when the user logs in (default: true)
launch_on_login = true
# ... existing fields ...

[telemetry]
# NT path prefixes to subscribe to (comma-separated)
# Must end with / to match table boundaries
# /FMSInfo/ is always subscribed regardless of this setting
nt_paths = /SmartDashboard/, /Shuffleboard/, /Advantage/
# Local data directory
data_dir = ./data
# Days to keep uploaded files before pruning
retention_days = 30

[ravenbrain]
# RavenBrain server URL (leave empty to disable upload, local-only mode)
url = https://ravenbrain.team1310.ca
# Pre-shared API key for telemetry upload
api_key =
# Upload batch size (entries per POST)
batch_size = 500
# Upload check interval (seconds)
upload_interval = 10
```

CLI args for new fields: `--nt-paths`, `--data-dir`, `--ravenbrain-url`, `--ravenbrain-api-key`, `--no-launch-on-login`

Note: `poll_interval` default changes from 1.0s to 0.05s (50ms) to keep up with NT data flow. The existing OBS and state machine logic is unaffected — they already work at any poll rate.

#### 1d. Modified: `src/main.py`

Wire NTLogger and Uploader into the main loop alongside OBS.

**Important: poll interval adjustment.** The existing default `poll_interval` is 1.0s, which is fine for OBS control but too slow for draining NT value-change events. With `sendAll=True` and robots publishing at 50Hz, the `pollStorage` buffer (100 entries) can overflow between 1-second polls. The poll interval should be reduced to 0.05s (50ms / 20Hz) to keep up with NT data flow. The OBS and upload logic can run on a slower cadence within the same loop.

```python
def run(config: Config) -> None:
    nt = NTClient(config.team)
    obs = OBSClient(...)
    sm = MatchStateMachine(...)
    logger = NTLogger(nt.instance, config.nt_paths, config.data_dir)
    uploader = Uploader(config.data_dir, config.ravenbrain_url, config.ravenbrain_api_key)

    while not shutdown:
        fms_state = nt.get_fms_state()
        actions = sm.update(fms_state)

        # Match markers — match_end fires at disable (STOP_PENDING entry), not after delay
        if sm.state == State.RECORDING_AUTO and prev_state == State.IDLE:
            logger.record_match_event("match_start", fms_state)
        if sm.state == State.STOP_PENDING and prev_state in (State.RECORDING_AUTO, State.RECORDING_TELEOP):
            logger.record_match_event("match_end", fms_state)

        # Existing OBS logic
        for action in actions:
            if action == Action.START_RECORD:
                obs.start_recording()
            elif action == Action.STOP_RECORD:
                obs.stop_recording()

        # NT data logging — drain event queue every cycle
        logger.poll()

        # Periodic upload attempt (every upload_interval seconds, not every cycle)
        uploader.maybe_upload()

        sleep(config.poll_interval)  # 0.05s default for telemetry
```

Note: match markers are decoupled from OBS actions. `match_start` fires when entering `RECORDING_AUTO`, `match_end` fires when entering `STOP_PENDING` (the instant the robot disables, not 10 seconds later when OBS stops). This gives accurate match boundary timestamps.

#### 1e. Modified: `src/nt_client.py`

Expose the underlying `NetworkTableInstance` so `NTLogger` can create its own subscribers on the same connection:

```python
class NTClient:
    @property
    def instance(self) -> ntcore.NetworkTableInstance:
        return self._inst
```

#### 1f. Session lifecycle

- **Session starts** when NT connects (robot comes online)
- **Session ends** when NT disconnects, bridge shuts down, or optionally after a configurable idle timeout
- Match markers are embedded within sessions — a session can span multiple matches
- If NT reconnects after a disconnect, a new session (new file) is started

### Phase 2: RavenBrain — Telemetry API

#### 2a. New package: `ca.team1310.ravenbrain.telemetry`

Following RavenBrain's existing package conventions (record DAOs, service layer, API controller).

**Database tables (Flyway migration V24):**

```sql
CREATE TABLE RB_TELEMETRY_SESSION (
    id              BIGINT PRIMARY KEY AUTO_INCREMENT,
    session_id      VARCHAR(64)  NOT NULL UNIQUE,
    team_number     INT          NOT NULL,
    robot_ip        VARCHAR(32)  NOT NULL,
    started_at      TIMESTAMP(3) NOT NULL,
    ended_at        TIMESTAMP(3) NULL,
    entry_count     INT          NOT NULL DEFAULT 0,
    created_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE RB_TELEMETRY_ENTRY (
    id              BIGINT PRIMARY KEY AUTO_INCREMENT,
    session_id      BIGINT       NOT NULL,
    ts              TIMESTAMP(3) NOT NULL,
    entry_type      VARCHAR(32)  NOT NULL,  -- 'data', 'match_start', 'match_end', 'session_start', 'session_end'
    nt_key          VARCHAR(255) NULL,       -- NT path, null for markers
    nt_type         VARCHAR(32)  NULL,       -- NT data type
    nt_value        TEXT         NULL,       -- JSON-encoded value (handles all types)
    fms_raw         INT          NULL,       -- for match markers
    INDEX idx_session (session_id),
    INDEX idx_ts (ts),
    INDEX idx_key (nt_key),
    FOREIGN KEY (session_id) REFERENCES RB_TELEMETRY_SESSION(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

**Why two tables:** Sessions provide grouping and metadata. Entries are the raw data points. This allows querying by session, by time range, or by NT key. Match boundaries are in the entry stream, so RavenBrain can reconstruct match associations by scanning for `match_start`/`match_end` markers and correlating with FMSInfo data within the same session.

#### 2b. API endpoints

```
POST /api/telemetry/session
  Body: { "sessionId": "abc123", "teamNumber": 1310, "robotIp": "10.13.10.2", "startedAt": "..." }
  Response: { "id": 42 }
  Auth: API key (new ROLE_TELEMETRY or pre-shared key header)

POST /api/telemetry/session/{sessionId}/data
  Body: [ { "ts": "...", "entryType": "data", "ntKey": "/SmartDashboard/X", "ntType": "double", "ntValue": "3.14" }, ... ]
  Response: { "accepted": 500, "errors": 0 }
  Auth: same

POST /api/telemetry/session/{sessionId}/complete
  Body: { "endedAt": "...", "entryCount": 15000 }
  Response: { "status": "ok" }
  Auth: same
```

**Auth approach:** Add a pre-shared API key header (`X-Telemetry-Key`) rather than JWT, since this is machine-to-machine from the DS laptop. The key is configured in RavenBrain's `application.yml` under `raven-eye.telemetry.api-key`. This follows the pattern of other machine secrets in the config (`frc-api.key`, `nexus-api.key`).

#### 2c. Java records and service

```
TelemetrySession.java     — @MappedEntity("RB_TELEMETRY_SESSION") record
TelemetryEntry.java        — @MappedEntity("RB_TELEMETRY_ENTRY") record
TelemetrySessionRepository.java — Micronaut Data JDBC repository
TelemetryEntryRepository.java   — Micronaut Data JDBC repository (with raw SQL for bulk insert)
TelemetryService.java      — Business logic, bulk write operations
TelemetryApi.java           — REST controller with 3 endpoints above
```

The bulk insert for telemetry entries should use raw JDBC (similar to `ConfigSyncService`) for performance, since individual Micronaut Data saves would be too slow for hundreds/thousands of entries.

### Phase 3: System Tray + Web Dashboard

The bridge gets a local web-based status dashboard and a Windows system tray icon for at-a-glance status.

#### 3a. New module: `src/web_dashboard.py`

A lightweight local web server (Flask or FastAPI) serving a single-page status dashboard at `http://localhost:8080`.

**Dashboard shows:**
- **Connection status:** NT4 connected/disconnected, robot IP, OBS connected/disconnected
- **Match state:** Current state machine state (IDLE, RECORDING_AUTO, RECORDING_TELEOP, STOP_PENDING)
- **Telemetry stats:** Active session file, entries written, data rate (entries/sec), subscribed topic count
- **Upload status:** Files pending, currently uploading, last upload result, RavenBrain connectivity
- **Recent log output:** Last ~100 log lines, auto-scrolling
- **Config editor:** Edit all configuration values and save to `config.ini`

**Implementation:**
```python
class WebDashboard:
    """Local web server for status monitoring."""

    def __init__(self, host: str = "localhost", port: int = 8080):
        ...

    def start(self) -> None:
        """Start web server on a background thread."""

    def update_status(self, status: BridgeStatus) -> None:
        """Update the status data served to the dashboard."""
```

- Runs on a daemon thread so it doesn't block the main loop
- Status data is updated each main loop iteration via a shared `BridgeStatus` dataclass
- The dashboard page polls `/api/status` via JavaScript every 1 second for live updates
- Single HTML page with inline CSS/JS — no build step, no npm, no external assets
- Served from a Python string template or a single `dashboard.html` bundled as package data

**Config editor API endpoints:**

```
GET  /api/config          → returns current config as JSON (all sections)
POST /api/config          → accepts partial config update, writes to config.ini
POST /api/config/reload   → re-reads config.ini and applies changes that can be hot-reloaded
```

**Config editor UI:**
- Form-based editor organized by section (`[bridge]`, `[telemetry]`, `[ravenbrain]`, `[dashboard]`)
- Each field shows its current value, type, and description
- "Save" button writes changes to `config.ini` via `POST /api/config`
- Fields that require a restart (e.g., `team`, `obs_port`) are marked with a restart icon
- Fields that can be hot-reloaded (e.g., `nt_paths`, `log_level`, `upload_interval`, `batch_size`) take effect immediately after save + reload
- Validation before save: team number is an integer, ports are valid, paths end with `/`, etc.
- The API key and password fields are masked in the UI (show `••••••`, only overwritten if user types a new value)

**Hot-reload support in `Config`:**
- Add a `reload()` method to re-read `config.ini` and update mutable fields
- The main loop checks a `config_changed` flag set by the web API and calls `reload()` when needed
- Immutable fields (team number, OBS host/port) require a restart — the dashboard shows a "restart required" banner when these are changed
- Mutable fields: `log_level`, `nt_paths`, `poll_interval`, `stop_delay`, `auto_teleop_gap`, `nt_disconnect_grace`, `upload_interval`, `batch_size`, `retention_days`
- When `nt_paths` changes, the NTLogger tears down the old `MultiSubscriber` and creates a new one with the updated prefixes

#### 3b. New module: `src/tray_icon.py`

A Windows system tray icon using `pystray` (cross-platform, works on Windows/macOS/Linux).

**Tray icon shows:**
- **Icon color/state:** Green = all connected & healthy, Yellow = partially connected or uploading, Red = NT disconnected or errors
- **Tooltip:** One-line status summary (e.g., "Recording Match 12 | 5,230 entries | OBS ✓")
- **Right-click menu:**
  - "Open Dashboard" → opens `http://localhost:8080` in default browser
  - "Status: IDLE / RECORDING / etc." (informational, not clickable)
  - "NT: Connected / Disconnected"
  - "OBS: Connected / Disconnected"
  - Separator
  - "Quit"

```python
class TrayIcon:
    """System tray icon for at-a-glance status."""

    def __init__(self, dashboard_url: str = "http://localhost:8080"):
        ...

    def start(self) -> None:
        """Start tray icon on a background thread."""

    def update_status(self, status: BridgeStatus) -> None:
        """Update icon color and tooltip text."""
```

- Uses `pystray` + `Pillow` for icon generation (colored circle icons)
- Runs on its own thread (required by pystray on Windows)
- "Open Dashboard" launches `webbrowser.open(dashboard_url)`

#### 3c. New: `src/bridge_status.py`

Shared status dataclass that all components update:

```python
@dataclass
class BridgeStatus:
    # Connections
    nt_connected: bool = False
    obs_connected: bool = False
    ravenbrain_reachable: bool = False

    # State machine
    match_state: str = "IDLE"

    # Telemetry
    active_session_file: str = ""
    entries_written: int = 0
    entries_per_second: float = 0.0
    subscribed_topics: int = 0

    # Upload
    files_pending: int = 0
    files_uploaded: int = 0
    last_upload_result: str = ""
    currently_uploading: bool = False

    # OBS
    obs_recording: bool = False
```

### Phase 4: Integration & Polish

#### 4a. Upload progress tracking

A small `.progress` file alongside each JSONL file tracks how many lines have been uploaded:
```
uploaded_lines=3500
total_lines=15000
```

This allows resuming partial uploads after a crash or restart.

#### 4b. Startup recovery

On startup, `Uploader` scans `pending/` for:
- Files with no active writer → eligible for upload
- Files with `.progress` → resume from last position
- Starts uploading oldest files first (chronological order)

#### 4c. Data pruning

A periodic task (or on startup) deletes files from `uploaded/` older than `retention_days`.

#### 4d. Launch on login

New module `src/autostart.py` manages automatic launch on user login. Enabled by default via `launch_on_login = true` in config.

**Windows (primary target):** Uses the Windows Registry `Run` key:
```
HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Run
```
- Key name: `FRC-OBS-Bridge`
- Value: path to the `.exe` (or `pythonw -m src.main --team XXXX` when running from source)
- Uses `winreg` (Python stdlib, Windows-only) to add/remove the entry
- The `--minimized` flag is appended so the bridge starts in the system tray without a console window

**macOS (dev machines):** Creates a Launch Agent plist at `~/Library/LaunchAgents/ca.team1310.frc-obs-bridge.plist`

```python
class AutoStart:
    """Manage launch-on-login registration."""

    @staticmethod
    def enable(exe_path: str) -> None:
        """Register the bridge to launch on login."""

    @staticmethod
    def disable() -> None:
        """Remove the bridge from login launch."""

    @staticmethod
    def is_enabled() -> bool:
        """Check if launch-on-login is currently registered."""
```

**Lifecycle:**
- On startup, if `launch_on_login = true` and not already registered → register
- On startup, if `launch_on_login = false` and currently registered → unregister
- When `launch_on_login` is toggled in the web dashboard config editor → immediately register/unregister
- The config editor shows this as a toggle switch with immediate feedback ("Will launch on next login" / "Will not launch on login")
- PyInstaller `.exe` path is auto-detected via `sys.executable`; from-source path uses the Python interpreter + module path

---

## Alternative Approaches Considered

### SQLite instead of JSONL
- **Pro:** Queryable locally, handles concurrent access well
- **Con:** Adds a dependency, harder to inspect manually, harder to stream to RavenBrain
- **Decision:** JSONL chosen — simpler, human-readable, append-only write pattern is ideal, and the upload-then-archive lifecycle is clean with files

### WebSocket streaming to RavenBrain
- **Pro:** Real-time data flow, lower latency
- **Con:** Requires persistent connection (unavailable during matches), adds complexity to both sides, doesn't solve the offline problem
- **Decision:** Store-and-forward via REST is simpler and handles the offline-first constraint naturally

### Single table in RavenBrain (no sessions)
- **Pro:** Simpler schema
- **Con:** Harder to manage uploads (no idempotency anchor), harder to query by recording session
- **Decision:** Two tables — sessions provide upload coordination and natural grouping

### JWT auth for telemetry upload
- **Pro:** Consistent with existing RavenBrain auth
- **Con:** Requires login flow, token refresh, adds complexity to the Python uploader for no real benefit (this isn't a user, it's a machine)
- **Decision:** Pre-shared API key via header — simple, secure enough for this use case

---

## System-Wide Impact

### Interaction Graph

AutoOBS main loop → NTLogger.poll() → writes JSONL file  
AutoOBS main loop → StateMachine.update() → NTLogger.record_match_event() → writes match markers  
AutoOBS main loop → Uploader.upload_pending() → reads JSONL → HTTP POST → RavenBrain TelemetryApi → TelemetryService → MySQL bulk insert  

### Error Propagation

- NT subscription failures: logged, retried on next poll cycle. Missing data is acceptable (not every value change is critical).
- JSONL write failures: if disk full or permissions error, log error, skip write. The bridge should not crash.
- Upload failures: logged, file stays in `pending/`, retried on next upload cycle with backoff.
- RavenBrain ingestion errors: partial batch accepted, error entries reported in response. Uploader logs and skips errored entries.

### State Lifecycle Risks

- **Partial upload + crash:** Progress file tracks uploaded lines. On restart, resume from last position. If progress file is missing, re-upload entire file (RavenBrain should handle duplicate session_id gracefully).
- **Active file during crash:** File is valid JSONL up to last complete line. Missing `session_end` marker is handled — RavenBrain treats sessions without `session_end` as incomplete but still ingests the data.
- **Disk space:** JSONL files can grow large during long practice sessions. Consider a max file size config that triggers file rotation within a session.

### API Surface Parity

- The new `/api/telemetry/*` endpoints are machine-only (not used by RavenEye frontend currently).
- No existing endpoints are modified.
- Config sync (`/api/config-sync`) does NOT need to sync telemetry data — it's one-directional from bridge to server.

---

## Acceptance Criteria

### Functional Requirements

- [ ] AutoOBS subscribes to configured NT paths and logs all value changes to JSONL files in `data_dir/pending/`
- [ ] JSONL entries include timestamp, NT key, type, and value
- [ ] FMSInfo paths are always subscribed regardless of user config
- [ ] Match start/end markers are embedded in the JSONL stream when state machine transitions
- [ ] Session start/end markers bracket each connectivity period
- [ ] Completed JSONL files are uploaded to RavenBrain via `POST /api/telemetry/*`
- [ ] Upload is resilient to network failures — retries with backoff, resumes partial uploads
- [ ] On startup, pending un-uploaded files from previous runs are discovered and uploaded
- [ ] RavenBrain stores telemetry sessions and entries in MySQL
- [ ] RavenBrain can accept large batches (500+ entries per POST) efficiently via bulk insert
- [ ] OBS recording control continues to work as before, unaffected by new features
- [ ] Bridge works correctly with no RavenBrain URL configured (telemetry is local-only)
- [ ] Web dashboard at `localhost:8080` shows live connection status, match state, telemetry stats, upload progress, and recent logs
- [ ] System tray icon shows green/yellow/red status, tooltip summary, and right-click menu with "Open Dashboard" and "Quit"
- [ ] Dashboard auto-updates via polling without page refresh
- [ ] Config editor in dashboard allows editing all settings and saving to `config.ini`
- [ ] Hot-reloadable settings (log level, NT paths, upload interval, etc.) take effect without restart
- [ ] Settings requiring restart (team, OBS port) show a "restart required" indicator after change
- [ ] Sensitive fields (API key, OBS password) are masked in the config editor UI
- [ ] Bridge registers itself to launch on login by default (Windows Registry Run key)
- [ ] `launch_on_login` toggle in config editor immediately registers/unregisters auto-start
- [ ] Bridge starts minimized to system tray when launched on login (`--minimized` flag)

### Non-Functional Requirements

- [ ] NT data logging does not slow the main loop below the configured poll interval
- [ ] JSONL writes are append-only and crash-safe (each line is a complete JSON object)
- [ ] Upload runs on a background thread or interleaved with main loop without blocking OBS/state machine
- [ ] RavenBrain telemetry bulk insert handles 500 entries/POST without timeout

### Testing Requirements

- [ ] Unit tests for NTLogger with mock NT instance (value change → JSONL line)
- [ ] Unit tests for Uploader with mock HTTP (file discovery, batch upload, progress tracking, retry)
- [ ] Unit tests for JSONL parsing (RavenBrain side, if any processing is done)
- [ ] Integration test for RavenBrain telemetry API (create session, post data, complete session)
- [ ] Existing state machine and OBS tests continue to pass unchanged

---

## Implementation Phases

### Phase 1: AutoOBS Core (Python)
**Files to create:**
- `src/nt_logger.py` — NT subscription + JSONL writing
- `src/uploader.py` — Store-and-forward upload client

**Files to modify:**
- `src/config.py` — New `[telemetry]` and `[ravenbrain]` config sections
- `src/nt_client.py` — Expose NT instance property
- `src/main.py` — Wire NTLogger and Uploader into main loop
- `requirements.txt` — Add `requests` (for HTTP upload)
- `pyproject.toml` — Add `requests` to dependencies
- `config.ini.example` — Add new config sections

**New tests:**
- `tests/test_nt_logger.py`
- `tests/test_uploader.py`

### Phase 2: RavenBrain API (Java)
**Files to create:**
- `src/main/java/ca/team1310/ravenbrain/telemetry/TelemetrySession.java`
- `src/main/java/ca/team1310/ravenbrain/telemetry/TelemetryEntry.java`
- `src/main/java/ca/team1310/ravenbrain/telemetry/TelemetrySessionRepository.java`
- `src/main/java/ca/team1310/ravenbrain/telemetry/TelemetryEntryRepository.java`
- `src/main/java/ca/team1310/ravenbrain/telemetry/TelemetryService.java`
- `src/main/java/ca/team1310/ravenbrain/telemetry/TelemetryApi.java`
- `src/main/java/ca/team1310/ravenbrain/telemetry/TelemetryApiKeyFilter.java` — HTTP filter for API key auth
- `src/main/resources/db/migration/V24__telemetry.sql`

**Files to modify:**
- `src/main/resources/application.yml` — Add `raven-eye.telemetry.api-key` config

**New tests:**
- `src/test/java/ca/team1310/ravenbrain/telemetry/TelemetryApiTest.java`

### Phase 3: System Tray + Web Dashboard (Python)
**Files to create:**
- `src/web_dashboard.py` — Local web server + status API + HTML dashboard
- `src/tray_icon.py` — System tray icon with status colors and menu
- `src/bridge_status.py` — Shared status dataclass
- `src/static/dashboard.html` — Single-page dashboard (or inline in web_dashboard.py)

**Files to modify:**
- `src/main.py` — Create BridgeStatus, wire dashboard + tray icon, update status each loop
- `src/config.py` — Add `[dashboard]` section (port, enabled flag)
- `requirements.txt` — Add `flask` (or `fastapi` + `uvicorn`), `pystray`, `Pillow`
- `pyproject.toml` — Add dashboard dependencies
- `config.ini.example` — Add dashboard config

**New tests:**
- `tests/test_bridge_status.py`

### Phase 4: Integration & Polish
**Files to create:**
- `src/autostart.py` — Launch-on-login registration (Windows Registry / macOS LaunchAgent)

**Tasks:**
- Wire `autostart.py` into startup (register/unregister based on config) and config editor (toggle callback)
- Add `--minimized` CLI flag to start in tray without console window
- End-to-end testing with real or simulated NT data
- Update AutoOBS `README.md` with new configuration and dashboard usage
- Update AutoOBS `CLAUDE.md` with new architecture
- Update `build.spec` for PyInstaller (add `requests`, `flask`, `pystray`, `Pillow` hidden imports)

---

## Dependencies & Prerequisites

- **pyntcore** — Already used. `MultiSubscriber` and `NetworkTableListenerPoller` confirmed available in current pyntcore (verified via WPILib docs and robotpy API reference)
- **requests** — New Python dependency for HTTP upload to RavenBrain
- **flask** (or fastapi+uvicorn) — Local web dashboard server
- **pystray** — Cross-platform system tray icon
- **Pillow** — Icon image generation (required by pystray)
- **RavenBrain deployment** — Telemetry API must be deployed before AutoOBS can upload (but AutoOBS works fine in local-only mode without it)
- **API key distribution** — Need to generate and configure the telemetry API key on both sides

## Risk Analysis & Mitigation

| Risk | Impact | Mitigation |
|------|--------|------------|
| High NT data volume fills disk | Bridge stops logging | Max file size rotation, retention policy, disk space monitoring |
| NT poller buffer overflow at high data rates | Missed value changes | Set `pollStorage=500`, poll at 50ms intervals, warn if queue fills |
| Large JSONL uploads timeout | Data not delivered | Batch size limiting, chunked upload with progress tracking |
| Clock drift between robot and DS | Timestamps inconsistent | Use DS laptop clock (time.time()) consistently, not NT server time |
| RavenBrain API key leaked | Unauthorized telemetry data | Key rotation support, rate limiting on the endpoint |

## Sources & References

### Internal References
- AutoOBS state machine: `src/state_machine.py` — match boundary detection
- AutoOBS NT client: `src/nt_client.py` — existing NT4 connection pattern
- RavenBrain batch API pattern: `ca.team1310.ravenbrain.eventlog.EventApi` — batch POST with per-record results
- RavenBrain robot alerts: `ca.team1310.ravenbrain.robotalert.RobotAlertApi` — similar batch POST pattern
- RavenBrain bulk JDBC: `ca.team1310.ravenbrain.sync.ConfigSyncService` — raw JDBC for bulk writes
- RavenBrain auth config: `src/main/resources/application.yml` — API key pattern from frc-api/nexus-api

### External References
- [WPILib Publishing and Subscribing to a Topic](https://docs.wpilib.org/en/stable/docs/software/networktables/publish-and-subscribe.html)
- [WPILib Listening for Changes](https://docs.wpilib.org/en/stable/docs/software/networktables/listening-for-change.html)
- [NT4 Protocol Specification](https://github.com/wpilibsuite/allwpilib/blob/main/ntcore/doc/networktables4.adoc)
- [RobotPy MultiSubscriber API](https://robotpy.readthedocs.io/projects/robotpy/en/2025.1.1.0/ntcore/MultiSubscriber.html)
- [pyntcore NetworkTableInstance API](https://robotpy.readthedocs.io/projects/pyntcore/en/latest/ntcore/NetworkTableInstance.html)
- Micronaut Data JDBC: Database repository patterns
- Micronaut Security: HTTP filter-based auth
