---
status: pending
priority: p1
issue_id: 001
tags: [code-review, correctness, data-loss, ntlogger]
dependencies: []
---

# P1: Logger never calls StartSession — 100% of telemetry is dropped

## Problem Statement

`internal/ntlogger/logger.go` defines `StartSession()` and `EndSession()` methods, but **nothing in the codebase ever calls `StartSession()`**. The logger's `handleValue` returns early when `l.file == nil`:

```go
// internal/ntlogger/logger.go:82-85
func (l *Logger) handleValue(tv ntclient.TopicValue) {
    if l.file == nil {
        return
    }
    ...
}
```

**Net effect:** The logger goroutine runs, reads values from `logCh`, and silently drops every single one. No JSONL files are ever created in `data/pending/`. `EntriesWritten` stays at zero. The uploader has nothing to upload. The dashboard reports zero entries.

This is a total data-loss bug on day 1 — the bridge produces no output.

## Findings

- **Evidence**: `grep -r "StartSession" cmd/ internal/` shows only the definition and `EndSession` in `defer`, no call sites.
- **Location**: `internal/ntlogger/logger.go:101` (`StartSession` definition), `internal/ntlogger/logger.go:82-85` (`handleValue` guard), `cmd/ravenlink/main.go:284-291` (match events but no session start).
- Flagged independently by both the architecture reviewer and the code quality reviewer.

## Proposed Solutions

### Option A: Drive session lifecycle from the state machine (recommended)
Call `ntLog.StartSession()` on `Idle → RecordingAuto` transition and `ntLog.EndSession()` on `StopPending → Idle` transition. Pair with existing `RecordMatchEvent` calls.
- Pros: One file per match, clean session boundaries, matches the upload unit
- Cons: Non-match data is not captured
- Effort: Small
- Risk: Low

### Option B: Session per NT connectivity period
Call `StartSession()` on NT connect, `EndSession()` on NT disconnect. Match markers embedded in one long session file.
- Pros: Captures practice data even without a match
- Cons: Session files can grow unbounded during long pit sessions
- Effort: Small
- Risk: Low

### Option C: Always-on session with rotation by time or size
`StartSession()` at bridge startup; rotate every N hours or M MB.
- Pros: Never loses data
- Cons: More complex, upload unit is less clear
- Effort: Medium
- Risk: Low

## Recommended Action

**Option B** — matches the original Python design and captures more data. Rotation can be added later.

## Technical Details

- Affected files:
  - `cmd/ravenlink/main.go` (wire StartSession on NT connect)
  - `internal/ntlogger/logger.go` (add `OnConnect()`/`OnDisconnect()` or accept session commands via channel to avoid races)
  - Also requires fixing #003 (Logger data races) before making this change thread-safe

## Acceptance Criteria

- [ ] NT connect triggers `StartSession`; a new JSONL file appears in `data/pending/`
- [ ] NT disconnect triggers `EndSession`; file is closed and eligible for upload
- [ ] `session_start` / `session_end` markers appear in the JSONL
- [ ] `EntriesWritten` counter increments as values arrive
- [ ] Dashboard shows non-zero entries when robot is connected
- [ ] Integration test: feed a scripted NT connect + values + disconnect sequence and assert a valid JSONL file is produced
