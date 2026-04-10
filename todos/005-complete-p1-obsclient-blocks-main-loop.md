---
status: pending
priority: p1
issue_id: 005
tags: [code-review, concurrency, deadlock, obsclient]
dependencies: []
---

# P1: obsclient holds mutex across synchronous RPCs — hung OBS deadlocks main loop

## Problem Statement

`internal/obsclient/client.go` takes `c.mu.Lock()` in `IsConnected()`, `StartRecording()`, and `StopRecording()`, then makes synchronous RPC calls on `c.client` (the goobs WebSocket client) **while holding the mutex**.

`IsConnected()` is particularly bad: it calls `c.client.General.GetVersion()` — a full WebSocket round-trip — and **mutates state on failure** (sets `c.client = nil`). This means:

1. A predicate method performs network I/O (surprising to callers)
2. A transient network blip destroys the connection that the next `StartRecording()` call depends on
3. The mutex is held for the duration of the RPC

In `cmd/ravenlink/main.go:324`, the 5-second status ticker calls `obs.IsConnected()`. If OBS hangs:
- `GetVersion()` blocks until its (uncontrolled) timeout — potentially indefinite
- The mutex is held the entire time
- The next state transition calling `StartRecording()` blocks waiting for the mutex
- **FMS events pile up** in `fmsCh` (64 slots) and start being dropped
- Dashboard + tray freeze
- Shutdown via SIGINT blocks because `ctx.Done` branch also tries to `obs.StopRecording()`

This is a **one-hung-dependency-takes-the-whole-bridge-down** failure mode.

## Findings

- **Location**: `internal/obsclient/client.go:83-95` (IsConnected), 100-133 (StartRecording), 138-171 (StopRecording)
- **Call site**: `cmd/ravenlink/main.go:324` (status refresh calls IsConnected)
- Flagged independently by both architecture and code quality reviewers.

## Proposed Solutions

### Option A: Cached connection state + background health check (recommended)
- `IsConnected()` becomes a pure getter returning `atomic.Bool`
- A separate goroutine pings OBS periodically (every 5s) with a timeout
- `StartRecording`/`StopRecording` use per-call timeouts (context.WithTimeout wrapping goobs calls in a goroutine since goobs doesn't support ctx directly)
- Pros: Hot path never blocks on network
- Cons: More complex
- Effort: Medium
- Risk: Low

### Option B: Worker goroutine for OBS commands
OBS client runs its own goroutine with a command channel. Main loop pushes commands and doesn't wait for responses (or uses a response channel with a timeout).
- Pros: Clean separation; fits the actor pattern from #003
- Cons: Changes API shape
- Effort: Medium
- Risk: Low

### Option C: Per-call timeouts without refactoring
Wrap every goobs call in a goroutine + channel with a 3-second timeout. Keep the existing mutex pattern.
- Pros: Minimal change
- Cons: Leaks goroutines if goobs never responds; still mutates state on failure
- Effort: Small
- Risk: Medium (goroutine leaks under persistent failure)

## Recommended Action

**Option A** — eliminates the deadlock class by removing network I/O from the hot path entirely. A background health check goroutine is a well-understood pattern.

## Technical Details

- Affected files:
  - `internal/obsclient/client.go` (add health check goroutine, atomic connection flag)
  - `cmd/ravenlink/main.go` (IsConnected becomes a cheap getter)

## Acceptance Criteria

- [ ] `IsConnected()` is O(1) — no network I/O
- [ ] Hung OBS does not prevent state machine from running
- [ ] Simulated OBS hang: SIGINT shuts down cleanly within 3 seconds
- [ ] `StartRecording`/`StopRecording` have per-call timeouts (no infinite waits)
