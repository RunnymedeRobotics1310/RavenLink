---
status: pending
priority: p3
issue_id: 015
tags: [code-review, quality, cleanup]
dependencies: []
---

# P3: Miscellaneous code cleanup and code smells

## Problem Statement

A handful of minor issues that don't block anything but are worth cleaning up during the next pass through the codebase.

### 1. FMSState.String() reinvents strings.Join
`internal/statemachine/fmsstate.go:47-84`:
```go
label := "NONE"
if len(flags) > 0 {
    for i, f := range flags {
        if i == 0 { label = f } else { label += " | " + f }
    }
}
```
Replace with `strings.Join(flags, " | ")`.

### 2. rbping JWT padding hack is needlessly clever
`cmd/rbping/main.go:190-208`:
```go
if pad := 4 - len(segment)%4; pad != 4 {
    segment += string(make([]byte, pad)) // zero-fill, then replace
    segment = segment[:len(segment)-pad] + "===="[:pad]
}
```
Use `strings.Repeat("=", pad)` or just `base64.RawURLEncoding.DecodeString` directly (which handles unpadded input).

### 3. statemachine uses *float64 for optional timestamps
`internal/statemachine/machine.go:44-48` — uses `*float64` with a `ptrFloat64()` helper. Use `time.Time{}.IsZero()` or a `struct{ set bool; t time.Time }` instead. Better type safety, more idiomatic Go, serializes cleanly.

### 4. FMS-detach stop grace uses back-dated math
`internal/statemachine/machine.go:158`:
```go
m.stopPendingAt = ptrFloat64(now - m.stopDelay + 3.0)
```
Clever but brittle. Add an explicit `stopBy time.Time` field and use it directly, or at minimum add a comment explaining the trick.

### 5. Machine.State is exported for cross-package reads
`internal/statemachine/machine.go:35-36` — public field read from `cmd/ravenlink/main.go`. A `State() State` method is more idiomatic and gives a seam for future locking.

### 6. openBrowser doesn't reap child process
`internal/tray/tray.go:196-209` — `cmd.Start()` without `cmd.Wait()` leaves a zombie on Unix. Add `go cmd.Wait()`.

### 7. getJSON/postJSON dead return paths
`internal/uploader/uploader.go:483` returns `nil, nil` on loop exit (unreachable but confusing). Replace with explicit error.

### 8. Tray menu goroutine leak on shutdown
`internal/tray/tray.go:107-120` — the `onReady` goroutine reading `mOpen.ClickedCh` and `mQuit.ClickedCh` has no cancellation path. On SIGINT shutdown, it leaks until the process exits. Add a `done` channel and include it in the select.

### 9. tray.UpdateStatus holds lock across SetIcon
`internal/tray/tray.go:129-181` — `SetIcon` decodes PNG, slow. Release the lock before systray calls, or scope the lock tightly to `currentColor` field access only.

### 10. Struct tag alignment inconsistency
`internal/status/status.go:21, 35` — `RavenBrainReachable` / `CurrentlyUploading` tag alignment is off-by-a-space. Cosmetic, but reviewers will notice.

### 11. runMainLoop has 12 parameters
`cmd/ravenlink/main.go:187` — symptom of accumulated responsibilities. Wrap in a `coordinator` struct. (Partial fix comes naturally from #003 and #004.)

## Findings

- **Agents**: Code quality P3 #18-28, architecture P3 #13-19
- Most are single-file, one-commit cleanups

## Proposed Solutions

Pick off as convenient during other refactors. None are urgent. Each fix is 1-10 lines.

## Recommended Action

Batch these into a single "code cleanup" commit once the P1/P2 work is done. Or fix opportunistically when touching the relevant file.

## Technical Details

- Affected files: scattered across `internal/statemachine/`, `internal/tray/`, `internal/uploader/`, `internal/status/`, `cmd/ravenlink/`, `cmd/rbping/`

## Acceptance Criteria

- [ ] `strings.Join` used where appropriate
- [ ] JWT base64 decode is straightforward (no padding hack)
- [ ] `time.Time` used for optional timestamps
- [ ] Tray menu goroutine exits cleanly on shutdown
- [ ] `openBrowser` reaps child processes
- [ ] Dead return paths have explicit errors
- [ ] `gofmt -s` clean
