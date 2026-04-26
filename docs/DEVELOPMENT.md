# RavenLink Development & Building

Audience: contributors building RavenLink from source, packaging
custom binaries, or debugging an installed instance in depth.

End users running a packaged binary should start with the
[README](../README.md).

---

## Repository layout

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

---

## Building

Requires Go 1.22+.

### macOS — `.app` bundle (recommended)

```bash
./scripts/build-macos.sh arm64        # or amd64 or universal
open dist/RavenLink.app                # registers with Window Server
```

> **Important:** On macOS, running the raw Go binary **will not show
> the menu bar icon**. The process needs to be a `.app` bundle with
> `LSUIElement=true` in `Info.plist` so macOS treats it as a
> menu-bar-only accessory app (no Dock icon, no ⌘-Tab entry — just the
> menu bar icon). The `build-macos.sh` script handles this.

For development, you can still run the binary directly
(`./ravenlink --team 1310`) — everything works except the menu bar
icon.

### Linux

```bash
go build -o ravenlink ./cmd/ravenlink
./ravenlink --team 1310
```

### Windows

`fyne.io/systray` is **pure Go on Windows** (it uses `syscall` +
`golang.org/x/sys/windows`, no CGo). This makes Windows
cross-compilation trivial.

**Option A — Cross-compile from macOS/Linux (recommended for dev)**

No C toolchain needed. From any platform:

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -ldflags "-H=windowsgui" -o ravenlink.exe ./cmd/ravenlink
```

The `-H=windowsgui` linker flag suppresses the console window so
only the tray icon is visible when the user launches the exe. Copy
`ravenlink.exe` to the DS laptop and run.

**Option B — Cross-compile with CGo via Zig (fallback)**

If you ever re-enable a CGo dependency on Windows, install
[Zig](https://ziglang.org/download/) (`brew install zig` on macOS),
which ships with a Windows C cross-compiler:

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

(If CGo is needed, also install a C toolchain: MSYS2 / MinGW-w64 /
TDM-GCC, and set `$env:CGO_ENABLED = "1"`.)

### Deploying on Windows

1. Copy `ravenlink.exe` and `config.yaml` to a permanent folder
   (e.g., `C:\FRC\RavenLink\`).
2. Run it once:
   ```
   C:\FRC\RavenLink\ravenlink.exe --team 1310
   ```
3. The bridge will:
   - Register itself to launch on login
     (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`).
   - Start the web dashboard at `http://localhost:8080`.
   - Show a system tray icon.
   - Begin capturing NT data when the robot connects.

### Competition-day checklist

1. Turn on the DS laptop — RavenLink starts automatically (system
   tray icon).
2. Open OBS Studio — ensure WebSocket server is enabled.
3. Verify via the dashboard:
   - **NT**: Connected (when robot is on)
   - **OBS**: Connected
4. The bridge handles everything else — recording, logging,
   forwarding.

---

## Configuration reference

Full `config.yaml` example (also at `config.yaml.example` in the
repo root):

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
  record_trigger: fms              # fms | auto | any — when to run OBS
  collect_trigger: fms             # fms | auto | any — when to log/upload NT data
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
  enabled: false
  url: ""                          # empty = disabled regardless of enabled flag
  username: telemetry-agent
  password: ""
  batch_size: 50
  upload_interval: 10

# RavenScope (bearer API key). Default URL targets the public hosted
# instance at ravenscope.team1310.ca. Override `url` to point at your
# own RavenScope deployment.
ravenscope:
  enabled: true
  url: https://ravenscope.team1310.ca
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

### Auth modes

Each upload target owns exactly one auth shape:

1. **API key bearer token (RavenScope `ravenscope` section).** Set
   `ravenscope.api_key` to an `rsk_live_…` token. RavenLink sends
   `Authorization: Bearer <api_key>` directly on every request — no
   `/login`, no cache, no renewal. The key itself is the credential.
2. **Legacy username/password (RavenBrain `ravenbrain` section).**
   Set `ravenbrain.username` and `ravenbrain.password`. RavenLink
   calls `POST /login` to exchange them for a short-lived JWT and
   caches it (auto-renewed 5 minutes before expiry). 401 triggers
   an invalidate-and-retry.

Both modes refuse to send credentials over plaintext HTTP **except**
to loopback hosts (`localhost`, `127.x.x.x`, `::1`, `*.localhost`)
so a local dev server or WPILib sim works out of the box. Anything
non-loopback must be `https://`.

### Simulator / local dev

For a WPILib simulator instead of a real robot:

```yaml
bridge:
  nt_host: localhost             # overrides the 10.TE.AM.2 derivation
```

Or pass `--nt-host localhost` on the command line.

For a local RavenScope worker (e.g., `wrangler dev`):

```yaml
ravenscope:
  enabled: true
  url: http://localhost:8787
  api_key: rsk_live_…
```

Loopback hosts (`localhost`, `127.x.x.x`, `::1`, `*.localhost`) are
treated as secure — same rule browsers use for "secure contexts".

### CLI overrides

Any config setting can also be overridden by CLI flag:

```bash
./ravenlink \
  --ravenscope-url https://ravenscope.team1310.ca \
  --ravenscope-api-key rsk_live_… \
  --ravenscope-enabled \
  --ravenbrain-url https://ravenbrain.team1310.ca
```

Run `ravenlink --help` for the full list.

---

## How it works (deep dive)

### Store-and-forward upload protocol

Per-target upload flow (identical for RavenBrain and RavenScope):

1. **Authenticate** — RavenBrain: `POST /login` → JWT (cached,
   auto-renewed 5 min before expiry). RavenScope:
   `Authorization: Bearer <api_key>` directly, no `/login`.
2. **`POST /api/telemetry/session`** (idempotent upsert — returns
   existing session if present).
3. **`GET /api/telemetry/session/{id}`** → server's `uploadedCount`
   for resumption.
4. **`POST /api/telemetry/session/{id}/data`** in batches, skipping
   the prefix the server already has.
5. **`POST /api/telemetry/session/{id}/complete`** (idempotent on
   both servers).
6. **Write `<base>.jsonl.<target>.done` sidecar marker.**

A file moves from `data/pending/` to `data/uploaded/` only after
**every currently enabled target** has its marker. Targets that
were enabled previously but are now disabled don't block the move —
the uploader only checks markers for the active set. Zero targets
enabled = local-only mode, files stay in `data/pending/`.

On 401: invalidate auth, retry once. On network failure: per-target
exponential backoff (5s → 60s). A slow or down target does not
delay uploads to healthy targets. Server-side `uploadedCount`
guarantees each target's re-attempts are idempotent — no
duplicates, even across process restarts.

### State machine

The match state machine is pure logic with an injectable clock; 53
unit tests cover every transition. See `internal/statemachine/` for
the full state graph and trigger logic.

### Web dashboard

`http://localhost:8080` when the bridge is running:

- **Status** — live connection status (NT, OBS, plus one row per
  enabled upload target), match state, telemetry stats, collection
  state, per-target upload progress.
- **Logs** — recent slog output (auto-scrolling).
- **Sessions** — browse all recorded session files (pending +
  uploaded), see match IDs for FMS matches, export to `.wpilog`, or
  open directly in AdvantageScope.
- **Config** — edit all settings, save to `config.yaml`, hot-reload
  for supported fields.

The Sessions tab auto-refreshes via SSE when file counts change.
WPILog files saved via "Open" are stored in `data/wpilog/` for
quick re-opening.

### Graceful shutdown

RavenLink supports three shutdown paths. All three trigger a
graceful drain:

1. **Ctrl-C** in the terminal (SIGINT)
2. **System tray → Quit** menu item
3. **`kill <pid>`** or **`Stop-Process -Id <pid>`** (SIGTERM on
   Unix; Windows sends the tray a close signal)

On any of these, RavenLink performs a two-phase shutdown:

**Phase 1 — stop data collection** *(instant)*
- Main context cancels → all goroutines exit cleanly.
- NT4 client disconnects.
- Logger flushes its bufio buffer, writes a `session_end` marker
  with entry count, fsyncs, and closes the active JSONL file.
- OBS recording is stopped if currently active.

**Phase 2 — drain pending uploads** *(up to 30 seconds)*
- Uploader walks `data/pending/` sequentially and ships every file
  to every enabled target as fast as possible, ignoring the normal
  upload interval and per-target backoff.
- A file that gets its markers for all enabled targets moves to
  `data/uploaded/` immediately.
- If all files finalize before the 30-second deadline, the process
  exits cleanly.
- If the deadline hits (slow WiFi, a target is down), files that
  aren't fully marked stay in `data/pending/` with their partial
  markers. Next startup resumes — healthy targets skip files they
  already marked; the unhealthy target retries only what it owes.

**Tolerance of ungraceful termination** (`SIGKILL`, power loss,
crash):

- The JSONL file may be missing its `session_end` marker — this is
  **fine**. `session_end` is just another entry in the data stream;
  the upload protocol doesn't require it.
- Data buffered in the `bufio.Writer` (up to a few KB) is lost — but
  the periodic sync ticker flushes to disk every 2 seconds, so the
  loss is bounded.
- On next startup, the uploader finds the unfinished file in
  `data/pending/` and uploads it via the normal flow. The server
  tracks `uploadedCount` per session transactionally, so the upload
  is idempotent and resumable — no duplicate entries.
- `POST /api/telemetry/session/{id}/complete` uses the **last
  timestamp in the file** as `endedAt`, which still gives the
  server a reasonable session boundary even without the explicit
  marker.

---

## Diagnostics

### `rbping`

A small companion command for verifying upload-target connectivity:

- `rbping --target ravenbrain` — runs `/api/ping` → `/login` →
  `/api/validate` against the RavenBrain URL from config.
- `rbping --target ravenscope` — runs `/api/health` → authenticated
  probe against the RavenScope URL. A 404 on the probe path is the
  expected success signal for auth.

### Limelight `reachable=false` debugging

- RavenLink polls from the machine running it (the DS laptop). Make
  sure that laptop is on the robot subnet — a laptop on the venue
  WiFi can't reach `10.TE.AM.11`.
- If you see sporadic `reachable=false` blips, a complex pipeline
  may be exceeding the 1000 ms timeout. Raise `limelight.timeout_ms`
  further or check the Limelight's CPU load.
- The log shows `limelight: camera went unreachable reason=…` on
  the first failure transition — the `reason` field is the specific
  error (e.g.
  `dial tcp 10.13.10.11:5807: connect: connection refused`,
  `http 404`, `decode json: …`). Sustained failures are silent on
  purpose; only transitions are logged.
- Verify the Limelight's REST server is enabled (it is by default;
  some reimaging workflows disable it).
- Check the last-octet list actually matches your installation. If
  you only have one camera at `.11`, set `last_octets: [11]`.

### Stuck pending file

If a file seems stuck in `data/pending/` and only one target is
configured: check for a stray `.done` marker from a
previously-enabled target. Startup sweeps orphan markers, but a
marker written while that target was enabled persists; once its
target is disabled, the finalize sweep moves the file on the next
tick.

### Tray icon goroutine

Check logs for `tray: onReady fired` — if present, the tray IS
installed (and the icon is hidden somewhere — see README
troubleshooting). If missing, the tray goroutine didn't start; the
process is otherwise running.

---

## Dependencies

| Library | Purpose | CGo |
|---------|---------|-----|
| `github.com/coder/websocket` | WebSocket for NT4 client | No |
| `github.com/vmihailenco/msgpack/v5` | NT4 binary frame decoding | No |
| `github.com/andreykaipov/goobs` | OBS WebSocket v5 (code-generated) | No |
| `fyne.io/systray` | Cross-platform system tray | **macOS only** (uses Cocoa); pure Go on Windows/Linux |
| `gopkg.in/yaml.v3` | Config file parsing | No |
| `golang.org/x/sys/windows/registry` | Windows launch-on-login (build-tagged) | No |

Everything else (HTTP server/client, JSON, embed, JWT decode, file
I/O) is Go stdlib.

`fyne.io/systray` is vendored into `third_party/systray/` via a
`replace` directive in `go.mod`. The only patch is a one-line fix in
`systray_darwin.m` that positions the popup menu at `(0, 0)` instead
of `(0, button.height + 6)` — the upstream coordinate places the
menu above the top of the screen, which forces macOS to clamp it
and show a scroll arrow that hides the first menu item.

---

## License

[BSD-3-Clause](../LICENSE).
