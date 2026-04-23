# RavenLink

FRC robot data bridge for Team 1310. A single native binary that captures NetworkTables telemetry, controls OBS Studio recording, and forwards data to RavenBrain and/or RavenScope — the two upload destinations run independently, so you can send to one, both, or neither (local-only mode).

## What It Does

Runs on the Driver Station laptop and:

- **Captures all NetworkTables data** — subscribes to configurable path prefixes, logs every value change to JSONL files with timestamps
- **Auto starts/stops OBS recording** — based on FMS match state (or manual/practice mode)
- **Monitors Limelight uptime** — polls each camera's `/results` endpoint so you can distinguish "Limelight rebooted mid-match" from "we lost network to the Limelight" in post-match review
- **Store-and-forward upload** — data saved locally first, then fanned out to every enabled destination (RavenBrain JWT, RavenScope bearer API key) with idempotent per-target retry. A file only moves out of `data/pending/` once every enabled target has accepted it; a down target doesn't hold up a healthy one.
- **Web dashboard** at `http://localhost:8080` — live status (one row per upload target), log viewer, session browser, config editor, restart/shutdown buttons
- **WPILog export** — convert any session to `.wpilog` and open directly in AdvantageScope from the dashboard
- **Menu bar / system tray icon** — click for connection status, "Open Dashboard", "Quit". Menu shows one row per subsystem (NT, OBS) and one row per enabled upload target (RavenBrain, RavenScope) with per-target pending counts.
- **Simulator friendly** — set `bridge.nt_host: localhost` to point at a WPILib simulator instead of the robot, and RavenLink treats `http://localhost` as a secure target so a local wrangler dev Worker "just works" for RavenScope testing.
- **Auto-opens the dashboard** in your browser on launch (unless started in `--minimized` mode by autostart)
- **First-run wizard** — ships with no team configured; on first launch the dashboard opens a config form, and saving restarts RavenLink with the new values automatically
- **Launch on login** — registers itself so it starts when you boot the DS laptop

Built in Go. Single ~14 MB static binary. No runtime dependencies.

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

RavenLink searches for `config.yaml` in this order at startup:

1. `$RAVENLINK_HOME/config.yaml` (env override for CI / custom installs)
2. `./config.yaml` in the current working directory (terminal launches)
3. OS-standard app directory:
   - **macOS**: `~/Library/Application Support/RavenLink/config.yaml`
   - **Windows**: `%APPDATA%\RavenLink\config.yaml`
   - **Linux**: `$XDG_CONFIG_HOME/ravenlink/config.yaml` or `~/.config/ravenlink/config.yaml`

On first launch with no config found, RavenLink writes a template to the OS-standard app directory, logs the path, and exits with a helpful error. Edit the template and relaunch.

Logs are always written to the OS-standard location regardless of how you launch:
- **macOS**: `~/Library/Logs/RavenLink/ravenlink.log`
- **Windows**: `%LOCALAPPDATA%\RavenLink\ravenlink.log`
- **Linux**: `~/.cache/ravenlink/ravenlink.log`

When launched from a terminal, logs also go to stdout. When launched from Finder / Explorer / a `.app` bundle, check the log file.

Example config (same format as `config.yaml.example`):

```yaml
bridge:
  team: 1310
  nt_host: ""                      # empty = derive 10.TE.AM.2 from team. Set "localhost" for WPILib sim.
  obs_host: localhost
  obs_port: 4455
  obs_password: ""
  stop_delay: 10
  poll_interval: 0.05
  log_level: INFO
  record_trigger: fms      # fms | auto | any — when to run OBS
  collect_trigger: fms     # fms | auto | any — when to log/upload NT data
  auto_teleop_gap: 5
  nt_disconnect_grace: 15
  launch_on_login: true

telemetry:
  nt_paths:
    - /.schema/
    - /SmartDashboard/
    - /Shuffleboard/
  data_dir: ./data
  retention_days: 30

# RavenBrain (legacy /login JWT). Enable + set URL to activate.
ravenbrain:
  enabled: true
  url: ""                          # empty = disabled regardless of enabled flag
  username: telemetry-agent
  password: ""
  batch_size: 50
  upload_interval: 10

# RavenScope (bearer API key). Enable + set URL + api_key to activate.
# Independent of ravenbrain — both can run in parallel, either alone, or neither.
ravenscope:
  enabled: false
  url: ""                          # e.g. https://scope.your-domain.workers.dev or http://localhost:8787 for sim
  api_key: ""                      # rsk_live_… bearer token
  batch_size: 50
  upload_interval: 10

dashboard:
  enabled: true
  port: 8080

limelight:
  enabled: true
  last_octets: [11, 12]            # 10.TE.AM.<octet> for each camera
  poll_interval: 2.0               # seconds between polls
  timeout_ms: 1000                 # per-request HTTP timeout
```

### Upload Targets

RavenLink ships to two independent destinations and each is its own config section. A target is "active" when `enabled: true` AND its `url` is non-empty. The dashboard rejects `enabled: true` with an empty URL at save time.

| Target | Auth | Section | Works for |
|---|---|---|---|
| RavenBrain | Username/password → JWT (cached, auto-renewed 5 min before expiry) | `ravenbrain` | Team-hosted RavenBrain server |
| RavenScope | Bearer API key (no `/login`, no cache) | `ravenscope` | Cloudflare Worker RavenScope instance |

Both can run in parallel. Per-file completion is tracked on disk via `<base>.jsonl.<target>.done` sidecar markers; a file moves from `data/pending/` to `data/uploaded/` only after every enabled target has marked it done. A target that's temporarily unreachable backs off independently — the other target keeps delivering.

Zero enabled targets = local-only mode: files stay in `data/pending/` and no network traffic happens.

### Simulator / Local Dev

For a WPILib simulator instead of a real robot:

```yaml
bridge:
  nt_host: localhost             # overrides the 10.TE.AM.2 derivation
```

Or pass `--nt-host localhost` on the command line.

For a local RavenScope Worker (e.g., `wrangler dev`):

```yaml
ravenscope:
  enabled: true
  url: http://localhost:8787
  api_key: rsk_live_…
```

`http://` is normally refused to protect credentials, but loopback hosts (`localhost`, `127.x.x.x`, `::1`, `*.localhost`) are treated as secure — same rule browsers use for "secure contexts".

Any setting can also be overridden by CLI flag — run `ravenlink --help` for the full list.

### Trigger Modes

Both `record_trigger` (OBS recording) and `collect_trigger` (NT data logging + upload) support the same three modes. They can be set independently — e.g., collect only during FMS matches while leaving OBS on "any".

| Mode | Trigger | Use case |
|------|---------|----------|
| `fms` | FMS attached + enabled | Competition matches (default) |
| `auto` | Auto mode + enabled | DS Practice button, manual auto enables |
| `any` | Any robot enable | Any enable triggers recording/collection |

All three modes use the same stop logic: robot disable → auto-teleop gap tolerance → `stop_delay` → stop.

## Limelight Uptime Monitor

RavenLink polls each configured Limelight camera's `/results` HTTP endpoint once per second and records two synthetic topics into the same session JSONL file as NetworkTables data:

- `/RavenLink/Limelight/<octet>/uptime_ms` — the Limelight's reported time since boot, in milliseconds (the `ts` field from `/results`)
- `/RavenLink/Limelight/<octet>/reachable` — `true` if the poll succeeded within the timeout, `false` otherwise

The point is to separate two failure modes that look identical from the robot code's perspective but have very different root causes:

| What happened | How it shows up in the data |
|---|---|
| **Limelight rebooted** (power glitch, firmware crash, brownout, manual reset) | `uptime_ms` drops to a small value between adjacent samples while `reachable` stays `true` the whole time |
| **Network outage** (Ethernet unplugged, radio dropout, switch died, Limelight powered off) | `reachable` flips to `false` for a stretch; no `uptime_ms` updates during that stretch |

In AdvantageScope, plot `uptime_ms` as a line chart — in a healthy session it grows monotonically, with clean downward resets at reboots. Plot `reachable` as a digital/boolean signal — every dip to `false` marks a network outage you can correlate against the match timeline.

On a failed poll (timeout, connection refused, HTTP non-2xx, malformed JSON, or missing `ts` field) the monitor emits only `reachable=false` — silence would be ambiguous (is the Limelight down, or is RavenLink itself down?), so the explicit `false` entry disambiguates.

### Configuration

```yaml
limelight:
  enabled: true            # set false to disable entirely (zero runtime cost)
  last_octets: [11, 12]    # 10.TE.AM.<octet>:5807 for each camera
  poll_interval: 2.0       # seconds between polls per camera
  timeout_ms: 1000         # HTTP request timeout (longer tolerates a busy pipeline)
```

IPs are derived from the team number: for team 1310, `last_octets: [11, 12]` polls `http://10.13.10.11:5807/results` and `http://10.13.10.12:5807/results`. Add or remove octets to match your actual installation.

Config is editable in the dashboard (`http://localhost:8080` → Config tab → limelight section). Changes require a restart, which the dashboard triggers automatically on Save.

### What It Doesn't Do

- Does not monitor Limelight pipeline output, vision targets, or pose data. If the robot publishes those over NetworkTables (which it usually does), they're already captured by the NT subscription.
- Does not raise real-time alerts. The goal is durable post-match analysis, not live paging.
- Does not back off for unreachable Limelights. Every tick polls every configured IP regardless of prior state, so you get a consistent sample cadence even across long outages.

## Building

Requires Go 1.22+.

### macOS (build as a .app bundle)

```bash
./scripts/build-macos.sh arm64   # or amd64 or universal
open dist/RavenLink.app           # registers with Window Server
```

**Important:** On macOS, running the raw Go binary **will not show the menu bar icon**. The process needs to be a `.app` bundle with `LSUIElement=true` in Info.plist so macOS treats it as a menu-bar-only accessory app (no Dock icon, no ⌘-Tab entry — just the menu bar icon). The `build-macos.sh` script handles this.

For development, you can still run the binary directly (`./ravenlink --team 1310`) — everything works except the menu bar icon.

### Linux

```bash
go build -o ravenlink ./cmd/ravenlink
./ravenlink --team 1310
```

### Windows

Unlike macOS, `fyne.io/systray` is **pure Go on Windows** (it uses `syscall` + `golang.org/x/sys/windows`, no CGo). This makes Windows cross-compilation trivial:

**Option A — Cross-compile from macOS/Linux (recommended for dev)**

No C toolchain needed. From any platform:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -ldflags "-H=windowsgui" -o ravenlink.exe ./cmd/ravenlink
```

The `-H=windowsgui` linker flag suppresses the console window so only the tray icon is visible when the user launches the exe. Copy `ravenlink.exe` to the DS laptop and run.

**Option B — Cross-compile with CGo via Zig (fallback)**

If you ever re-enable a CGo dependency on Windows, install [Zig](https://ziglang.org/download/) (`brew install zig` on macOS), which ships with a Windows C cross-compiler:

```bash
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
  CC="zig cc -target x86_64-windows-gnu" \
  go build -ldflags "-H=windowsgui" -o ravenlink.exe ./cmd/ravenlink
```

**Option C — Build natively on Windows**

Install Go, then:

```powershell
$env:CGO_ENABLED = "0"
go build -ldflags "-H=windowsgui" -o ravenlink.exe ./cmd/ravenlink
```

(If CGo is needed, also install a C toolchain: MSYS2 / MinGW-w64 / TDM-GCC, and set `$env:CGO_ENABLED = "1"`.)

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

Completed JSONL files in `data/pending/` are fanned out to every enabled upload target. Each target runs in its own goroutine on its own interval and has its own HTTP client, backoff timer, and status counters.

Per-target upload flow (identical protocol for RavenBrain and RavenScope):

1. Authenticate — RavenBrain: `POST /login` → JWT (cached, auto-renewed 5 min before expiry). RavenScope: `Authorization: Bearer <api_key>` directly, no `/login`.
2. `POST /api/telemetry/session` (idempotent upsert — returns existing session if present)
3. `GET /api/telemetry/session/{id}` → server's `uploadedCount` for resumption
4. `POST /api/telemetry/session/{id}/data` in batches, skipping the prefix the server already has
5. `POST /api/telemetry/session/{id}/complete` (idempotent on both servers)
6. Write `<base>.jsonl.<target>.done` sidecar marker

A file moves from `data/pending/` to `data/uploaded/` only after **every currently enabled target** has its marker. Targets that were enabled previously but are now disabled don't block the move — the uploader only checks markers for the active set. Zero targets enabled = local-only mode, files stay in `data/pending/`.

On 401: invalidate auth, retry once. On network failure: per-target exponential backoff (5s → 60s). A slow or down target does not delay uploads to healthy targets. Server-side `uploadedCount` guarantees each target's re-attempts are idempotent — no duplicates, even across process restarts.

### Auth Modes

Each upload target owns exactly one auth shape:

1. **Legacy username/password (RavenBrain `ravenbrain` section).** Set `ravenbrain.username` and `ravenbrain.password`. RavenLink calls `POST /login` to exchange them for a short-lived JWT and caches it (auto-renewed 5 minutes before expiry). 401 triggers an invalidate-and-retry.
2. **API key bearer token (RavenScope `ravenscope` section).** Set `ravenscope.api_key` to an `rsk_live_…` token. RavenLink sends `Authorization: Bearer <api_key>` directly on every request — no `/login`, no cache, no renewal. The key itself is the credential.

Both modes refuse to send credentials over plaintext HTTP **except** to loopback hosts (`localhost`, `127.x.x.x`, `::1`, `*.localhost`) so a local wrangler dev Worker or WPILib sim works out of the box. Anything non-loopback must be `https://`.

Config via CLI:

```bash
./ravenlink \
  --ravenbrain-url https://brain.team1310.ca \
  --ravenscope-url https://scope.your-domain.workers.dev \
  --ravenscope-api-key rsk_live_… \
  --ravenscope-enabled
```

(Each target can be enabled/disabled independently; `--no-ravenbrain` turns the legacy target off.)

## Web Dashboard

`http://localhost:8080` when the bridge is running:

- **Status** — live connection status (NT, OBS, plus one row per enabled upload target), match state, telemetry stats, collection state, per-target upload progress
- **Logs** — recent slog output (auto-scrolling)
- **Sessions** — browse all recorded session files (pending + uploaded), see match IDs for FMS matches, export to `.wpilog`, or open directly in AdvantageScope
- **Config** — edit all settings, save to `config.yaml`, hot-reload for supported fields

The Sessions tab auto-refreshes via SSE when file counts change. WPILog files saved via "Open" are stored in `data/wpilog/` for quick re-opening.

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
- Uploader walks `data/pending/` sequentially and ships every file to every enabled target as fast as possible, ignoring the normal upload interval and per-target backoff
- A file that gets its markers for all enabled targets moves to `data/uploaded/` immediately
- If all files finalize before the 30-second deadline, the process exits cleanly
- If the deadline hits (slow WiFi, a target is down), files that aren't fully marked stay in `data/pending/` with their partial markers. Next startup resumes — healthy targets skip files they already marked; the unhealthy target retries only what it owes.

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

**Limelight `reachable=false` but I can ping it**
- RavenLink polls from the machine running it (the DS laptop). Make sure that laptop is on the robot subnet — a laptop on the venue WiFi can't reach `10.TE.AM.11`.
- If you see sporadic `reachable=false` blips, a complex pipeline may be exceeding the 1000 ms timeout. Raise `limelight.timeout_ms` further or check the Limelight's CPU load.
- The log shows `limelight: camera went unreachable reason=…` on the first failure transition — the `reason` field is the specific error (e.g. `dial tcp 10.13.10.11:5807: connect: connection refused`, `http 404`, `decode json: …`). Sustained failures are silent on purpose; only transitions are logged.
- Verify the Limelight's REST server is enabled (it is by default; some reimaging workflows disable it).
- Check the last-octet list actually matches your installation. If you only have one camera at `.11`, set `last_octets: [11]`.

**Data not uploading**
- Check the dashboard Connections card. "Upload targets: None configured" means no target is both `enabled: true` AND has a non-empty `url`. Fix one or both sections.
- Check `*.url` is either `https://` or `http://` to a loopback host (localhost, 127.x.x.x, ::1). Non-loopback `http://` is refused with a `WARN` log.
- RavenBrain: verify `username` and `password` for the `telemetry-agent` service account. Repeated 401s → password changed on the server.
- RavenScope: verify `api_key` is a valid `rsk_live_…` token and not expired/revoked.
- Each target's dashboard row shows its own last error (HTTP status, connection error, auth error) — the other target may be fine while this one is failing.
- If a file seems stuck in `data/pending/` and only one target is configured: check for a stray `.done` marker from a previously-enabled target. Startup sweeps orphan markers, but a marker written while that target was enabled persists; once its target is disabled, the finalize sweep moves the file on the next tick.

**`rbping` diagnostic**
- `rbping --target ravenbrain` — runs `/api/ping` → `/login` → `/api/validate` against the RavenBrain URL from config.
- `rbping --target ravenscope` — runs `/api/health` → authenticated probe against the RavenScope URL. A 404 on the probe path is the expected success signal for auth.

**Menu bar / system tray icon missing**
- **macOS**: running the raw binary doesn't register with the Window Server. Build with `./scripts/build-macos.sh` and launch with `open dist/RavenLink.app`. The `.app` bundle sets `LSUIElement=true`, which makes RavenLink a menu-bar-only accessory (no Dock icon, no ⌘-Tab entry).
- **Windows**: the icon is probably hidden in the tray overflow area. Click the `^` arrow in the system tray to see it, then drag-and-drop it to the always-visible area. Windows hides new tray icons by default.
- **Linux**: requires a system tray implementation (most desktop environments have one; GNOME needs an extension).
- Check logs for `tray: onReady fired` — if present, the tray IS installed; if missing, the tray goroutine didn't start.

## Project Layout

```
cmd/ravenlink/main.go         # Entry point + coordinator
cmd/iconbuilder/              # Generates .iconset → .icns for the .app bundle
internal/
├── assets/                   # Embedded team logo PNG
├── autostart/                # Launch-on-login (build-tagged per OS)
├── collect/                  # Runtime pause flag for NT data collection
├── config/                   # YAML config, CLI flags, save-and-restart
├── dashboard/                # Embedded HTTP dashboard + static UI + session list + WPILog export
├── lifecycle/                # Self-restart (exec/spawn), OpenBrowser, OpenFile
├── limelight/                # HTTP poller for Limelight /results (uptime + reachability)
├── ntclient/                 # NT4 WebSocket+MessagePack client
├── ntlogger/                 # JSONL writing, session lifecycle, match markers
├── obsclient/                # OBS WebSocket (via goobs library)
├── paths/                    # OS-standard config + log file paths
├── statemachine/             # Pure-logic state machine (53 tests)
├── status/                   # Thread-safe shared state
├── tray/                     # Menu bar / system tray icon (fyne.io/systray)
├── typeconv/                 # NT value type coercion helpers
├── uploader/                 # Store-and-forward upload + JWT auth
└── wpilog/                   # WPILog v1.0 encoder (JSONL → .wpilog for AdvantageScope, 22 tests)
third_party/
└── systray/                  # Vendored fyne.io/systray (one-line patch)
```

## Dependencies

| Library | Purpose | CGo |
|---------|---------|-----|
| `github.com/coder/websocket` | WebSocket for NT4 client | No |
| `github.com/vmihailenco/msgpack/v5` | NT4 binary frame decoding | No |
| `github.com/andreykaipov/goobs` | OBS WebSocket v5 (code-generated) | No |
| `fyne.io/systray` | Cross-platform system tray | **macOS only** (uses Cocoa); pure Go on Windows/Linux |
| `gopkg.in/yaml.v3` | Config file parsing | No |
| `golang.org/x/sys/windows/registry` | Windows launch-on-login (build-tagged) | No |

Everything else (HTTP server/client, JSON, embed, JWT decode, file I/O) is Go stdlib.

`fyne.io/systray` is vendored into `third_party/systray/` via a `replace` directive in `go.mod`. The only patch is a one-line fix in `systray_darwin.m` that positions the popup menu at `(0, 0)` instead of `(0, button.height + 6)` — the upstream coordinate places the menu above the top of the screen, which forces macOS to clamp it and show a scroll arrow that hides the first menu item.

## License

MIT
