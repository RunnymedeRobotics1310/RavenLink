---
status: pending
priority: p2
issue_id: 014
tags: [code-review, quality, uploader, dead-code]
dependencies: [004]
---

# P2: uploader.Run is dead code with a latent bug

## Problem Statement

`internal/uploader/uploader.go:73-88` exports a `Run(ctx)` method that loops on a ticker calling `MaybeUpload("")`. It is **never called** — main.go instead drives `MaybeUpload` from its own tick loop.

Two problems:
1. Dead code rots. Two implementations of "how uploads are scheduled" will diverge.
2. `uploader.Run` passes an **empty** `activeSessionID` to `MaybeUpload`, which means it would upload the currently-active session file mid-match. That's a bug in the "intended" implementation that nobody has noticed because nobody calls it.

This is a symptom of a planning issue resolved in #004 (synchronous uploads block main loop) — the original intent was to use `uploader.Run`, but the wiring was done wrong, and then a quick fix put the upload call in the main loop, leaving `Run` behind.

## Findings

- **Location**: `internal/uploader/uploader.go:73-88`, `cmd/ravenlink/main.go:308-309`
- **Agents**: Both code quality and architecture reviewers flagged this (P2 #9 and #17 in their reports)

## Proposed Solutions

### Option A: Adopt Run and fix the ActiveSessionID bug (recommended, paired with #004)
Fix `Run` to accept a callback or shared state providing the current ActiveSessionID, then call `go up.Run(ctx)` in main.go, and remove the main-loop upload ticker.
- Pros: Solves #004 at the same time
- Cons: Depends on #003 (ActiveSessionID thread safety)
- Effort: Small
- Risk: Low

### Option B: Delete Run
Accept that the main-loop-driven pattern is the design, delete the exported `Run` method and its ticker.
- Pros: Simplest
- Cons: Doesn't fix #004
- Effort: Small
- Risk: Low

## Recommended Action

**Option A** — this is really the same fix as #004. Track both together.

## Technical Details

- Depends on #004 (synchronous upload problem) and #003 (Logger race)
- Affected files:
  - `internal/uploader/uploader.go` (fix Run to get real ActiveSessionID)
  - `cmd/ravenlink/main.go` (call go up.Run(ctx), remove uploadTicker)

## Acceptance Criteria

- [ ] `grep -r "uploader.Run\|\.Run(ctx)" cmd/` returns a call site
- [ ] `grep -r "uploadTicker" cmd/` returns nothing
- [ ] Upload respects active session ID (doesn't upload the currently-writing file)
