# RavenLink

FRC robot data bridge for Team 1310. A single native binary that captures NetworkTables telemetry, controls OBS Studio recording, and forwards data to RavenBrain.

## What It Does

Runs on the Driver Station laptop and:

- **Captures all NetworkTables data** — subscribes to configurable path prefixes, logs every value change to JSONL files with timestamps
- **Auto starts/stops OBS recording** — based on FMS match state (or manual/practice mode)
- **Store-and-forward upload** — data saved locally first, uploaded to RavenBrain when internet is available (with idempotent retry and JWT auth)
- **Web dashboard** at `http://localhost:8080` — live status, log viewer, config editor
- **System tray icon** — green/yellow/red status at a glance
- **Launch on login** — starts automatically when you boot the DS laptop

Built in Go. Single 12 MB static binary. No runtime dependencies.

## Prerequisites

- **OBS Studio 28+** with WebSocket server enabled (Tools → WebSocket Server Settings)
- **Windows 10/11** or **macOS** (Linux works for development)

That's it. No Python, no .NET, no JVM. Just the binary.

## Quick Start

```bash
# Download or build the binary (see below)
./ravenlink --team 1310
```

Open `http://localhost:8080` in your browser for the dashboard.

## Configuration

RavenLink reads `config.yaml` from the current directory on startup. Copy `config.yaml.example`:

```yaml
bridge:
  team: 1310
  obs_host: localhost
  obs_port: 4455
  obs_password: ""
  stop_delay: 10
  poll_interval: 0.05
  log_level: INFO
  record_trigger: fms      # fms | auto | any
  auto_teleop_gap: 5
  nt_disconnect_grace: 15
  launch_on_login: true

telemetry:
  nt_paths:
    - /SmartDashboard/
    - /Shuffleboard/
  data_dir: ./data
  retention_days: 30

ravenbrain:
  url: ""                          # empty = local-only mode (no upload)
  username: telemetry-agent
  password: ""
  batch_size: 500
  upload_interval: 10

dashboard:
  enabled: true
  port: 8080
```

Any setting can also be overridden by CLI flag — run `ravenlink --help` for the full list.

### Record Trigger Modes

| Mode | Trigger | Use case |
|------|---------|----------|
| `fms` | FMS attached + enabled | Competition matches (default) |
| `auto` | Auto mode + enabled | DS Practice button, manual auto enables |
| `any` | Any robot enable | Any enable triggers recording |

All three modes use the same stop logic: robot disable → auto-teleop gap tolerance → `stop_delay` → OBS stop.

## Building

Requires Go 1.22+.

### macOS (build as a .app bundle)

```bash
./scripts/build-macos.sh arm64   # or amd64 or universal
open dist/RavenLink.app           # registers with Window Server
```

**Important:** On macOS, running the raw Go binary **will not show the menu bar icon**. The process needs to be a `.app` bundle with `LSUIElement=true` in Info.plist so macOS treats it as a menu-bar-only accessory app. The `build-macos.sh` script handles this.

For development, you can still run the binary directly (`./ravenlink --team 1310`) — everything works except the tray icon.

### Linux

```bash
go build -o ravenlink ./cmd/ravenlink
./ravenlink --team 1310
```

### Windows

```powershell
# Build natively on Windows
$env:CGO_ENABLED = 1
go build -ldflags "-H=windowsgui" -o ravenlink.exe ./cmd/ravenlink
```

Cross-compile from macOS is possible but awkward — `fyne.io/systray` requires CGo which needs a Windows C cross-compiler (zig or MinGW). For releases, build natively on each platform via GitHub Actions.

## Deploying on Windows

1. Copy `ravenlink.exe` and `config.yaml` to a permanent folder (e.g., `C:\FRC\RavenLink\`)
2. Run it once:
   ```
   C:\FRC\RavenLink\ravenlink.exe --team 1310
   ```
3. The bridge will:
   - Register itself to launch on login (Windows Registry `HKCU\...\Run`)
   - Start the web dashboard at `http://localhost:8080`
   - Show a system tray icon
   - Begin capturing NT data when the robot connects

### Competition Day Checklist

1. Turn on DS laptop — RavenLink starts automatically (system tray icon)
2. Open OBS Studio — ensure WebSocket server is enabled
3. Verify via the dashboard:
   - NT: Connected (when robot is on)
   - OBS: Connected
4. The bridge handles everything else — recording, logging, forwarding

## How It Works

### NT Data Collection

RavenLink speaks NT4 natively over WebSocket + MessagePack. It subscribes to the configured path prefixes (always including `/FMSInfo/`) and receives every value change through a Go channel. Each change is written as a JSON line to a session file in `data/pending/`.

Session files are named `{ISO-timestamp}_{hex8}.jsonl`. Match start/end markers with FMS metadata are embedded in the data stream.

### Match State Machine

```
IDLE → RECORDING_AUTO → RECORDING_TELEOP → STOP_PENDING → IDLE
```

- **IDLE → RECORDING_AUTO**: trigger condition met (per `record_trigger`)
- **RECORDING_AUTO → RECORDING_TELEOP**: auto mode ends, teleop starts (brief disable gap tolerated)
- **RECORDING_TELEOP → STOP_PENDING**: robot disabled
- **STOP_PENDING → IDLE**: after `stop_delay`, OBS recording stops

The state machine is pure logic with an injectable clock — 53 unit tests cover every transition.

### Store & Forward

Completed JSONL files in `data/pending/` are uploaded to RavenBrain:

1. `POST /login` → get JWT (cached, auto-renewed 5 min before expiry)
2. `POST /api/telemetry/session` (idempotent — returns existing session if present)
3. `GET /api/telemetry/session/{id}` (check server's `uploadedCount`)
4. `POST /api/telemetry/session/{id}/data` (batches of 500, skips already-uploaded entries)
5. `POST /api/telemetry/session/{id}/complete`
6. File moves to `data/uploaded/` (pruned after `retention_days`)

On 401: invalidate token, retry once. On network failure: exponential backoff (5s → 60s).

## Web Dashboard

`http://localhost:8080` when the bridge is running:

- **Status** — live connection status, match state, telemetry stats, upload progress
- **Logs** — recent log output (auto-scrolling)
- **Config** — edit all settings, save to `config.yaml`, hot-reload for supported fields

## Shutting Down Gracefully

RavenLink supports three shutdown paths. All three trigger a graceful drain:

1. **Ctrl-C** in the terminal (SIGINT)
2. **System tray → Quit** menu item
3. **`kill <pid>`** or **`Stop-Process -Id <pid>`** (SIGTERM on Unix; Windows sends the tray a close signal)

On any of these, RavenLink performs a two-phase shutdown:

**Phase 1 — stop data collection** *(instant)*
- Main context cancels → all goroutines exit cleanly
- NT4 client disconnects
- Logger flushes its bufio buffer, writes a `session_end` marker with entry count, fsyncs, and closes the active JSONL file
- OBS recording is stopped if currently active

**Phase 2 — drain pending uploads** *(up to 30 seconds)*
- Uploader walks `data/pending/` and uploads every file (including the just-closed session) as fast as possible, ignoring the normal upload interval and backoff
- If all files upload before the 30-second deadline, the process exits cleanly
- If the deadline hits (slow WiFi, RavenBrain down), remaining files stay in `data/pending/` and are retried on the next startup

**Tolerance of ungraceful termination** — `SIGKILL`, power loss, crash:

- The JSONL file may be missing its `session_end` marker — this is **fine**. `session_end` is just another entry in the data stream; the upload protocol doesn't require it.
- Data buffered in the `bufio.Writer` (up to a few KB) is lost — but the periodic sync ticker flushes to disk every 2 seconds, so the loss is bounded.
- On next startup, the uploader finds the unfinished file in `data/pending/` and uploads it via the normal flow. The server tracks `uploadedCount` per session transactionally, so the upload is idempotent and resumable — no duplicate entries.
- `POST /api/telemetry/session/{id}/complete` uses the **last timestamp in the file** as `endedAt`, which still gives RavenBrain a reasonable session boundary even without the explicit marker.

## Troubleshooting

**OBS not detected**
- Ensure OBS is running with WebSocket server enabled
- Check the port matches (`--obs-port` / `obs.port`)
- If you set a password in OBS, set `obs_password` in config

**NetworkTables not connecting**
- Verify team number is correct
- Ensure DS laptop can reach the robot at `10.TE.AM.2`
- Check firewall allows outbound connections to port 5810

**Recording doesn't start**
- Check `record_trigger` — `fms` (default) requires FMS attached
- For home practice, use `record_trigger: auto` (DS Practice button) or `any`
- In `auto` mode, plain teleop enable won't trigger — must enter auto mode

**Data not uploading**
- Check `ravenbrain.url` is set in config
- Verify `username` and `password` for the `telemetry-agent` service account
- Check dashboard upload status for error messages
- Repeated 401s → password may have changed on the server

**System tray icon missing**
- **macOS**: running the raw binary doesn't register with the Window Server. Build with `./scripts/build-macos.sh` and launch with `open dist/RavenLink.app`. The `.app` bundle includes `LSUIElement=true` which tells macOS to show the icon in the menu bar.
- **Windows**: the icon is probably hidden in the tray overflow area. Click the `^` arrow in the system tray to see it, then drag-and-drop it to the always-visible area. Windows hides new tray icons by default.
- **Linux**: requires a system tray implementation (most desktop environments have one; GNOME needs an extension).
- Check logs for `tray: onReady fired` — if present, the tray IS installed; if missing, the tray goroutine didn't start (check CGo was enabled during build).

## Project Layout

```
cmd/ravenlink/main.go         # Entry point + coordinator
internal/
├── config/                   # YAML config, CLI flags, hot-reload
├── statemachine/             # Pure-logic state machine (53 tests)
├── ntclient/                 # NT4 WebSocket+MessagePack client
├── ntlogger/                 # JSONL writing, session lifecycle, match markers
├── uploader/                 # Store-and-forward upload + JWT auth
├── obsclient/                # OBS WebSocket (via goobs library)
├── dashboard/                # Embedded HTTP dashboard
├── tray/                     # System tray icon (fyne.io/systray)
├── autostart/                # Launch-on-login (build-tagged per OS)
└── status/                   # Thread-safe shared state
```

## Dependencies

| Library | Purpose |
|---------|---------|
| `github.com/coder/websocket` | WebSocket for NT4 client |
| `github.com/vmihailenco/msgpack/v5` | NT4 binary frame decoding |
| `github.com/andreykaipov/goobs` | OBS WebSocket v5 (code-generated) |
| `fyne.io/systray` | Cross-platform system tray (CGo) |
| `gopkg.in/yaml.v3` | Config file parsing |

Everything else (HTTP server/client, JSON, embed, JWT decode, file I/O) is Go stdlib.

## License

MIT
