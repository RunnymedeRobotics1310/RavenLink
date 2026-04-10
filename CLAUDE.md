# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

RavenLink — FRC robot data bridge for Team 1310. Written in Go. Produces a single native binary that runs on the Driver Station laptop and:

1. Subscribes to NetworkTables (NT4 WebSocket+MessagePack) on the robot
2. Logs all value changes to JSONL files with timestamps and match markers
3. Auto-starts/stops OBS Studio recording based on FMS match state
4. Store-and-forwards telemetry to RavenBrain via JWT-authenticated REST API
5. Serves a web dashboard for status monitoring and config editing
6. Runs as a system tray icon with launch-on-login

The RavenBrain server (Micronaut/Java/MySQL) lives at `~/src/1310/RavenBrain`.

## Commands

```bash
# Build the binary
go build -o ravenlink ./cmd/ravenlink

# Run
./ravenlink --team 1310

# Run tests
go test ./...

# Run a single package's tests
go test ./internal/statemachine/ -v

# Run a specific test
go test ./internal/statemachine/ -run TestFullMatchLifecycle -v

# Vet
go vet ./...

# Cross-compile for Windows (needs Zig for CGo deps)
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
  CC="zig cc -target x86_64-windows-gnu" \
  go build -ldflags "-H=windowsgui" -o ravenlink.exe ./cmd/ravenlink
```

## Architecture

```
cmd/ravenlink/main.go         # Entry point + main loop coordinator
internal/
├── config/                   # YAML config + CLI flags + hot-reload
├── statemachine/             # Pure-logic state machine (53 tests)
├── ntclient/                 # NT4 WebSocket+MessagePack client
├── ntlogger/                 # JSONL writing, session lifecycle, match markers
├── uploader/                 # Store-and-forward + JWT auth
├── obsclient/                # OBS WebSocket v5 (wraps goobs)
├── dashboard/                # Embedded HTTP server + static HTML
├── tray/                     # System tray icon (fyne.io/systray)
├── autostart/                # Launch-on-login (build-tagged per OS)
└── status/                   # Thread-safe shared BridgeStatus
```

### Key Design Decisions

1. **State machine is side-effect-free** — `Machine.Update(fms)` returns `[]Action` and the main loop dispatches them. Pure logic with injectable `Clock` for testing.

2. **Pure Go NT4 client** — No C++ bindings. NT4 is WebSocket + MessagePack, both have Go libraries (`coder/websocket`, `vmihailenco/msgpack/v5`). This eliminates the packaging pain of wrapping WPILib's ntcore.

3. **Goroutine fan-out** — A single goroutine reads `nt.Values()` and tees the channel: all values go to `ntlogger`, and `/FMSInfo/FMSControlData` updates also go to a separate `fmsCh` that drives the state machine.

4. **Match markers decoupled from OBS actions** — `match_start` fires at state transition into `RecordingAuto`, `match_end` fires at transition into `StopPending` (the actual disable time, not 10s later when OBS stops).

5. **Server-side upload progress** — The server tracks `uploadedCount` per session (transactional with batch INSERT). Client queries it on every upload attempt → no duplicates, no `.progress` files, safe on flaky networks.

6. **JWT auth with auto-renewal** — `POST /login` → cache token → decode `exp` claim → auto-renew 5 minutes before expiry → invalidate + retry once on 401.

7. **Dashboard is embedded** — `//go:embed static/*` bakes the HTML/CSS/JS into the binary at compile time.

8. **Build tags for platform code** — `autostart_windows.go`, `autostart_darwin.go`, `autostart_other.go` compile only on their target OS.

### FMS bitmask layout

```
bit 0 (0x01): enabled
bit 1 (0x02): auto mode
bit 2 (0x04): test mode
bit 3 (0x08): e-stop
bit 4 (0x10): FMS attached
bit 5 (0x20): DS attached
```

The state machine's `RecordTrigger` setting (`fms` / `auto` / `any`) determines which bits must be set to trigger recording. In `fms` mode (default), both enabled AND fms_attached are required. In `auto` mode, enabled + auto_mode triggers (catches DS Practice button). In `any` mode, any enable triggers.

### State transitions to know

- Auto-teleop disabled gap (up to `auto_teleop_gap` seconds, default 5) is tolerated — prevents splitting the recording
- FMS detach only triggers stop in `fms` trigger mode
- NT disconnect starts a separate grace period (`nt_disconnect_grace`, default 15s)
- Re-enabling during `STOP_PENDING` cancels the stop (if trigger condition met)

## Testing

- **State machine tests** — `internal/statemachine/machine_test.go` — FakeClock + `makeFMS()` helper. 53 tests covering all lifecycle scenarios, trigger modes, and bitmask parsing. No I/O, no external deps.
- **Run with**: `go test ./internal/statemachine/ -v`

## Dependencies

| Library | Purpose | CGo |
|---------|---------|-----|
| `github.com/coder/websocket` | WebSocket (NT4 client) | No |
| `github.com/vmihailenco/msgpack/v5` | NT4 binary frame decoding | No |
| `github.com/andreykaipov/goobs` | OBS WebSocket v5 client | No |
| `fyne.io/systray` | Cross-platform system tray | **Yes** |
| `gopkg.in/yaml.v3` | YAML config parsing | No |
| `golang.org/x/sys/windows/registry` | Windows auto-start (build-tagged) | No |

Everything else is stdlib: `net/http`, `encoding/json`, `log/slog`, `embed`, `context`, `sync`.

## Config Sections

| Section | Hot-reloadable |
|---------|----------------|
| `bridge` | log_level, stop_delay, poll_interval, auto_teleop_gap, nt_disconnect_grace, record_trigger, launch_on_login |
| `telemetry` | nt_paths, retention_days |
| `ravenbrain` | batch_size, upload_interval |
| `dashboard` | — (restart required) |

Immutable fields (team, obs_host, obs_port) require a restart — dashboard shows a "restart required" indicator after edit.

## Gotchas

- **`fyne.io/systray` must run on the main goroutine on macOS.** `cmd/ravenlink/main.go` calls `trayIcon.Start()` on the main goroutine when `runtime.GOOS == "darwin"`, and in a background goroutine otherwise.
- **NT4 channel fan-out** — the `nt.Values()` channel is read by exactly one goroutine that tees to `logCh` and `fmsCh`. Adding a third consumer requires extending the tee.
- **Config hot-reload** — the `dashboard.Server` writes config.yaml and calls a reload hook. The main loop doesn't currently re-poll config changes for all fields; extend `runMainLoop` if needed.
- **CGo cross-compile** — `systray` requires CGo. Cross-compile from macOS to Windows needs Zig toolchain; the easier path is building natively per platform on CI.
