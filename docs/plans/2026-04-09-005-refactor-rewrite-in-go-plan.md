---
title: "refactor: Rewrite RavenLink in Go for native cross-platform binaries"
type: refactor
status: active
date: 2026-04-09
---

# refactor: Rewrite RavenLink in Go

## Overview

Rewrite RavenLink from Python to Go, producing a single static binary with zero runtime dependencies. Eliminates PyInstaller bundling issues (native DLL discovery, 50MB+ exe size, slow startup) and the need for a Python installation on the DS laptop.

## Problem Statement

The Python implementation has packaging friction:
- PyInstaller can't auto-discover pyntcore's native C++ DLLs (wpiutil.dll, ntcore.dll)
- Resulting exe is large (~50MB+) and slow to start (unpacks to temp dir)
- Requires a Python venv to develop, adding setup complexity for FRC students
- 6 runtime dependencies (pyntcore, obsws-python, requests, flask, pystray, Pillow)

## Why Go

- **Single static binary** — `go build` produces one file, ~10-15MB, no runtime needed
- **Cross-compilation** — `GOOS=windows GOARCH=amd64 go build` from macOS, trivially
- **No C++ bindings needed** — NT4 is a WebSocket + MessagePack protocol; pure Go implementation avoids all native library issues
- **Great stdlib** — HTTP server/client, JSON, signal handling, file I/O are all built-in
- **`embed` package** — Dashboard HTML/CSS/JS baked into the binary at compile time
- **Concurrency** — goroutines are natural for the concurrent NT polling + upload + dashboard serving pattern

## Architecture (Go)

```
ravenlink/
├── main.go                  # Entry point, wiring, signal handling
├── go.mod / go.sum
├── config/
│   └── config.go            # Config struct, YAML loading, CLI flags
├── ntclient/
│   └── client.go            # NT4 WebSocket client (pure Go, no WPILib)
│   └── fmsstate.go          # FMSState struct + bitmask parsing
│   └── protocol.go          # NT4 MessagePack message types
├── ntlogger/
│   └── logger.go            # Subscribe to topics, write JSONL
├── obsclient/
│   └── client.go            # OBS WebSocket v5 client
├── statemachine/
│   └── machine.go           # Match state machine (same logic, ported)
│   └── machine_test.go      # State machine tests (same test cases)
├── uploader/
│   └── uploader.go          # Store-and-forward to RavenBrain
│   └── auth.go              # JWT login + token management
├── dashboard/
│   └── server.go            # Embedded HTTP server + API
│   └── static/
│       └── index.html       # Dashboard (embedded via //go:embed)
├── tray/
│   └── tray.go              # System tray icon
├── autostart/
│   └── autostart.go         # Launch-on-login (Windows Registry / macOS LaunchAgent)
│   └── autostart_windows.go # Build-tagged Windows impl
│   └── autostart_darwin.go  # Build-tagged macOS impl
└── status/
    └── status.go            # Shared BridgeStatus struct
```

## Go Dependencies (minimal)

| Dependency | Purpose | Why not stdlib |
|-----------|---------|----------------|
| `github.com/coder/websocket` | WebSocket client for NT4 | stdlib has no WebSocket. Successor to nhooyr/websocket, actively maintained by Coder. |
| `github.com/vmihailenco/msgpack/v5` | MessagePack encoding for NT4 protocol | binary protocol, not JSON |
| `github.com/andreykaipov/goobs` | OBS WebSocket v5 client | Code-generated from OBS protocol spec (v1.8.3). Full API: StartRecord, StopRecord, GetRecordStatus, etc. |
| `fyne.io/systray` | Cross-platform system tray icon | OS-specific APIs. Fork of getlantern/systray, requires CGo. |
| `gopkg.in/yaml.v3` | YAML config file parsing | consistent with RavenBrain's application.yml format |

Everything else (HTTP server, JSON, file I/O, JWT decode, base64, CLI flags, embed, templates) is stdlib.

**5 dependencies** vs Python's 6 + transitive deps + native C++ DLLs. And `goobs` replaces a custom OBS client — net fewer things to maintain.

**CGo note:** `fyne.io/systray` is the only dependency requiring CGo. Cross-compile with `CC="zig cc -target x86_64-windows-gnu"` or build natively per platform on GitHub Actions CI.

## Key Design Decisions

### NT4 Client: Pure Go WebSocket Implementation

Instead of wrapping WPILib's C++ ntcore library, implement NT4 directly over WebSocket + MessagePack. The protocol is well-specified:
- Connect via WebSocket to `ws://10.TE.AM.2:5810/nt/ravenlink`
- Subscribe to topics with MessagePack-encoded subscribe messages
- Receive value updates as MessagePack frames with timestamps
- Handle reconnection, topic discovery, and timestamp sync

This eliminates the C++ dependency entirely — the root cause of all PyInstaller issues.

### OBS Client: WebSocket + JSON

OBS WebSocket v5 uses standard WebSocket with JSON messages. No special library needed beyond the WebSocket client.

### Concurrency Model

```
main goroutine
├── NT4 client goroutine (WebSocket read loop → channel)
├── NT logger goroutine (reads channel → writes JSONL)
├── Uploader goroutine (periodic scan + upload)
├── Dashboard HTTP server goroutine
└── System tray goroutine
```

Goroutines communicate via channels. The state machine runs on the main goroutine, receiving FMS state from the NT4 channel. This is cleaner than Python's single-threaded poll loop.

### Dashboard: Embedded Static Files

```go
//go:embed dashboard/static/*
var dashboardFiles embed.FS
```

The HTML/CSS/JS dashboard is compiled into the binary. No external files needed.

### Platform-Specific Code: Build Tags

```go
// autostart_windows.go
//go:build windows

// autostart_darwin.go
//go:build darwin
```

Windows Registry and macOS LaunchAgent code use Go build tags — the right file is compiled for each platform automatically.

## Implementation Phases

### Phase 1: Core (state machine + config + NT4 client)

Port the state machine and FMS bitmask parsing first — these are pure logic with no I/O and can be tested immediately. Then implement the NT4 WebSocket client.

**Files:** `statemachine/`, `ntclient/`, `config/`, `main.go`
**Tests:** Port all existing state machine tests verbatim

### Phase 2: Data pipeline (NT logger + uploader + auth)

Port the JSONL logging, store-and-forward uploader with server-side progress, and JWT auth.

**Files:** `ntlogger/`, `uploader/`, `status/`
**Tests:** Port uploader tests

### Phase 3: OBS + Dashboard + Tray

Port OBS WebSocket client, embedded web dashboard, and system tray icon.

**Files:** `obsclient/`, `dashboard/`, `tray/`, `autostart/`

### Phase 4: Build + CI

- `Makefile` or `goreleaser` config for cross-platform builds
- GitHub Actions workflow: build Windows + macOS binaries on push
- Release binaries as GitHub Release assets

## Migration Strategy

- Develop in a new `go/` directory (or new branch) alongside Python code
- Port module by module, verify feature parity with tests
- Once Go version passes all test cases, replace Python entirely
- Switch from `config.ini` (INI) to `config.yaml` (YAML) — consistent with RavenBrain's `application.yml`

## Build & Cross-Compile

```bash
# Build for current platform
go build -o ravenlink ./

# Cross-compile for Windows from macOS
GOOS=windows GOARCH=amd64 go build -o ravenlink.exe ./

# Cross-compile for macOS from Windows
GOOS=darwin GOARCH=arm64 go build -o ravenlink ./
```

Note: `systray` uses CGo, which complicates cross-compilation. Options:
- Use `fyne.io/systray` (pure Go fork) — simpler, fewer features
- Use Docker-based cross-compile with `xgo`
- Build on CI (GitHub Actions) on each target platform natively

## Acceptance Criteria

- [ ] `go build` produces a single binary (< 20MB) on Windows and macOS
- [ ] NT4 client connects to robot, subscribes to topics, receives value changes
- [ ] State machine handles all trigger modes (fms/auto/any) — all existing test cases pass
- [ ] JSONL logging with session lifecycle and match markers — identical output format
- [ ] Store-and-forward upload with idempotent server-side progress tracking
- [ ] JWT auth with auto-renewal (POST /login flow)
- [ ] OBS WebSocket start/stop recording
- [ ] Web dashboard at localhost:8080 with status, logs, config editor
- [ ] System tray icon with green/yellow/red status
- [ ] Launch-on-login for Windows and macOS
- [ ] Config uses `config.yaml` (YAML format, consistent with RavenBrain)
- [ ] Zero runtime dependencies — single binary, nothing to install

## What Stays the Same

- JSONL file format (exact same schema)
- RavenBrain API contract (same endpoints, same payloads)
- Dashboard HTML/CSS/JS (ported as-is, embedded)
- State machine logic and test cases
- FMS bitmask parsing

## What Changes

- Config format: `config.ini` (INI) → `config.yaml` (YAML)
- OBS client: custom obsws-python wrapper → `goobs` library (code-generated, full API)
- NT4 client: pyntcore C++ bindings → pure Go WebSocket + MessagePack
- Packaging: PyInstaller → `go build` (single static binary)
