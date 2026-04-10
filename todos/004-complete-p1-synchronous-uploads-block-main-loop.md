---
status: pending
priority: p1
issue_id: 004
tags: [code-review, concurrency, architecture, uploader]
dependencies: []
---

# P1: Synchronous uploads block the main loop — can miss state transitions during matches

## Problem Statement

`cmd/ravenlink/main.go:245` creates `uploadTicker`, and line 308-309 calls `up.MaybeUpload(...)` directly in the same `select` as the state-machine ticker. `MaybeUpload` performs synchronous HTTP with a 30-second client timeout per request across multiple sequential calls:

- `POST /api/telemetry/session` (create/idempotent)
- `GET /api/telemetry/session/{id}` (uploaded count)
- N × `POST /api/telemetry/session/{id}/data` (batches of 500)
- `POST /api/telemetry/session/{id}/complete`

**Impact:** If RavenBrain is slow or unreachable, a single upload cycle can block `runMainLoop` for tens of seconds. During that time:
- `ticker.C` cannot fire → state machine updates don't run
- FMS changes are not processed → enable/disable transitions are missed
- OBS start/stop actions are not executed → **recording fails to start mid-match**
- Dashboard status stops updating
- Tray icon freezes

This is catastrophic at a competition where WiFi is flaky and the bridge is expected to handle matches that start/stop rapidly.

Ironically, `uploader.Run(ctx)` already exists (`internal/uploader/uploader.go:73`) as a standalone goroutine loop but is never invoked.

## Findings

- **Location**: `cmd/ravenlink/main.go:245,308-309`, `internal/uploader/uploader.go:73,406,449`
- **Evidence**: HTTP client created with 30s timeout; 3+ sequential requests per upload; called from the main select block
- Flagged by architecture reviewer. Quality reviewer flagged `uploader.Run` as dead code.

## Proposed Solutions

### Option A: Run uploader in its own goroutine (recommended)
Replace the main-loop ticker call with `go up.Run(ctx)` at startup. The uploader loops on its own schedule and doesn't block anything else. `MaybeUpload` becomes an internal method of `Run`.
- Pros: Simple; `uploader.Run` is already written
- Cons: `uploader.Run` currently always passes empty activeSessionID — needs to share that state
- Effort: Small
- Risk: Low (but depends on #003 — ActiveSessionID must be thread-safe)

### Option B: Upload in a worker goroutine with a job channel
Main loop pushes "check for uploads" signals to a worker via a channel. Worker handles retry/backoff.
- Pros: Explicit coordination
- Cons: More moving parts than Option A
- Effort: Medium
- Risk: Low

## Recommended Action

**Option A** — `uploader.Run` is already 90% correct. Fix ActiveSessionID sharing (depends on #003) and wire it.

## Technical Details

- Affected files:
  - `cmd/ravenlink/main.go` (remove uploadTicker branch, add `go up.Run(ctx)`)
  - `internal/uploader/uploader.go` (fix `Run` to use real ActiveSessionID via callback or shared state)
  - Blocked by #003 (Logger data races) because `ActiveSessionID` is on the Logger

## Acceptance Criteria

- [ ] Upload cycle does not block the main state machine ticker
- [ ] Simulated slow RavenBrain (30s response time) does not prevent OBS recording from starting
- [ ] `uploader.Run` correctly skips the active session file
- [ ] No duplicate goroutines — `MaybeUpload` ticker code removed from main
