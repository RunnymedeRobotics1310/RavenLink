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

# Cross-compile for Windows (NO CGo needed — fyne.io/systray is pure Go on Windows)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -ldflags "-H=windowsgui" -o ravenlink.exe ./cmd/ravenlink

# Build macOS .app bundle (required for menu bar icon)
./scripts/build-macos.sh arm64   # or amd64 or universal
open dist/RavenLink.app
```

## Architecture

```
cmd/ravenlink/main.go         # Entry point + main loop coordinator
cmd/iconbuilder/              # Generates .iconset from embedded team logo
internal/
├── assets/                   # Embedded team 1310 logo PNG
├── autostart/                # Launch-on-login (build-tagged per OS)
├── config/                   # YAML config + CLI flags
├── dashboard/                # Embedded HTTP server + static HTML + shutdown/restart hooks
├── lifecycle/                # Self-restart (exec/spawn) + OpenBrowser
├── ntclient/                 # NT4 WebSocket+MessagePack client
├── ntlogger/                 # JSONL writing, session lifecycle, match markers
├── obsclient/                # OBS WebSocket v5 (wraps goobs)
├── paths/                    # OS-standard config + log file paths
├── statemachine/             # Pure-logic state machine (53 tests)
├── status/                   # Thread-safe shared BridgeStatus
├── tray/                     # Menu bar / system tray icon (fyne.io/systray)
└── uploader/                 # Store-and-forward + JWT auth
third_party/
└── systray/                  # Vendored fyne.io/systray (one-line patch; see go.mod replace)
```

### Key Design Decisions

1. **State machine is side-effect-free** — `Machine.Update(fms)` returns `[]Action` and the main loop dispatches them. Pure logic with injectable `Clock` for testing.

2. **Pure Go NT4 client** — No C++ bindings. NT4 is WebSocket + MessagePack, both have Go libraries (`coder/websocket`, `vmihailenco/msgpack/v5`). This eliminates the packaging pain of wrapping WPILib's ntcore.

3. **Goroutine fan-out** — A single goroutine reads `nt.Values()` and tees the channel: all values go to `ntlogger`, and `/FMSInfo/FMSControlData` updates also go to a separate `fmsCh` that drives the state machine.

4. **Match markers decoupled from OBS actions** — `match_start` fires at state transition into `RecordingAuto`, `match_end` fires at transition into `StopPending` (the actual disable time, not 10s later when OBS stops).

5. **Server-side upload progress** — The server tracks `uploadedCount` per session (transactional with batch INSERT). Client queries it on every upload attempt → no duplicates, no `.progress` files, safe on flaky networks.

6. **JWT auth with auto-renewal** — `POST /login` → cache token → decode `exp` claim → auto-renew 5 minutes before expiry → invalidate + retry once on 401.

7. **Dashboard is embedded** — `//go:embed static/*` bakes the HTML/CSS/JS into the binary at compile time.

8. **Build tags for platform code** — `autostart_windows.go`, `autostart_darwin.go`, `autostart_other.go` compile only on their target OS. Same pattern in `internal/tray/` (`nsapp_darwin.{go,m,h}`, `nsapp_other.go`, `icon_windows.go`, `icon_other.go`, `settricon_{darwin,other}.go`) and `internal/lifecycle/` (`exec_unix.go`, `exec_windows.go`).

9. **macOS = menu-bar-only accessory** — the `.app` bundle sets `LSUIElement=true` so RavenLink has no Dock icon, no ⌘-Tab entry, and no app menu. The ONLY UI is the menu bar icon and the browser dashboard. Running the raw binary on macOS won't show the menu bar icon — it must be launched via the `.app` bundle.

10. **Vendored systray fork** — `third_party/systray/` is a full copy of `fyne.io/systray v1.12.0` with a one-line patch in `systray_darwin.m`: `show_menu` positions the popup at `NSMakePoint(0, 0)` instead of `NSMakePoint(0, button.height + 6)`. Upstream's coordinate is above the top of the screen in the button's non-flipped view coordinates, causing macOS to clamp the menu and show a ^ scroll arrow that hides the first item(s). `go.mod` has a `replace fyne.io/systray => ./third_party/systray` directive.

11. **Save-and-restart config flow** — the dashboard's Save button writes `config.yaml` and triggers `lifecycle.RestartSelf()`. On Unix this is `syscall.Exec` (in-place replacement); on Windows it spawns a new process and `os.Exit(0)`s. This avoids the complexity of hot-reloading arbitrary config fields at runtime.

12. **`--minimized` flag** — `autostart_darwin.go` (LaunchAgent plist) and `autostart_windows.go` (Run key) both register RavenLink with a `--minimized` argument. This flag is handled by `config.ParseFlags` (otherwise `flag.ExitOnError` would kill the auto-launched process!) and causes `main.go` to skip the browser auto-open. First-run (team==0) still opens the browser even when `--minimized`, because the user needs to complete setup.

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
| `fyne.io/systray` | Cross-platform system tray | **macOS only** (Cocoa). Pure Go on Windows (syscall + `x/sys/windows`) and Linux (D-Bus). Vendored in `third_party/systray`. |
| `gopkg.in/yaml.v3` | YAML config parsing | No |
| `golang.org/x/sys/windows/registry` | Windows auto-start (build-tagged) | No |

Everything else is stdlib: `net/http`, `encoding/json`, `log/slog`, `embed`, `context`, `sync`.

**Windows cross-compile needs neither CGo nor a C toolchain** — `CGO_ENABLED=0 GOOS=windows go build` just works.

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
- **Main loop goroutine ordering** — `runMainLoop` is launched AFTER `dash` and `trayIcon` are constructed (not inside the `if !firstRun` subsystem-setup block), because it dereferences both on the first status tick. Launching it earlier with `nil` values crashes with SIGSEGV ~5 seconds after startup when `statusTicker.C` first fires.
- **NT4 channel fan-out** — the `nt.Values()` channel is read by exactly one goroutine that tees to `logCh` and `fmsCh`. Adding a third consumer requires extending the tee.
- **Config save = restart** — the dashboard writes `config.yaml` then triggers `lifecycle.RestartSelf()`. Don't add hot-reload code paths; the restart is the reload mechanism.
- **`--minimized` MUST be registered in `config.ParseFlags`** — `autostart_darwin.go` and `autostart_windows.go` both pass `--minimized` when they auto-launch RavenLink on login. `flag.ExitOnError` means unknown flags kill the process, so removing the flag definition breaks autostart silently.
- **First-run mode** — when `cfg.Bridge.Team == 0`, `main.go` skips NT/OBS/logger/uploader startup and only runs the dashboard + tray + browser. The main loop goroutine is also skipped in this mode. Don't unconditionally dereference subsystem pointers after `if !firstRun` — they're still nil.
- **Template icon on macOS** — `settricon_darwin.go` passes a 22x22 black+alpha silhouette via `SetTemplateIcon`. AppKit tints template icons automatically for light/dark mode. Without the template path, the menu bar icon often fails to appear on recent macOS versions.
- **Icon must be ICO on Windows** — `icon_windows.go` wraps the generated PNG in a minimal ICO container (Vista+ supports PNG-inside-ICO). `systray.SetIcon` silently fails with a raw PNG on Windows.
- **macOS .m file needs `-x objective-c` CFLAG** — `nsapp_darwin.go` sets `#cgo darwin CFLAGS: -x objective-c -fobjc-arc`. Without `-x objective-c` the compiler treats `.m` as C and fails on `@interface`.

## Commit message conventions

- **No attribution footers.** Commit messages must NOT include `Co-Authored-By:`, `Generated with Claude Code`, or similar trailers. Use plain conventional-commit subjects (`feat:`, `fix:`, `refactor:`, etc.) with an optional body.
- Create a new commit rather than amending.
- Never `git push` or `git commit` unless the user explicitly asks.
