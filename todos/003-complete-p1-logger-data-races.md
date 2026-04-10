---
status: pending
priority: p1
issue_id: 003
tags: [code-review, concurrency, data-race, ntlogger]
dependencies: []
---

# P1: Data races on Logger fields (EntriesWritten, ActiveSessionID, file)

## Problem Statement

`internal/ntlogger/logger.go` has no mutex. Three goroutines access it concurrently:

1. **Logger goroutine**: `handleValue` writes `l.file`, increments `EntriesWritten`
2. **Main loop goroutine** (`cmd/ravenlink/main.go`): reads `ntLog.EntriesWritten` (line 313) and `ntLog.ActiveSessionID` (lines 309, 327-328) on every rateTicker/statusTicker tick
3. **Main loop goroutine**: calls `ntLog.RecordMatchEvent(...)` (lines 285-291), which writes to `l.file` through `writeLine`
4. Once #001 is fixed, main loop will also call `StartSession`/`EndSession` which swap the `l.file` pointer

**Consequences:**
- Multiple goroutines writing `*os.File` → interleaved bytes mid-line → corrupted JSONL
- `StartSession` reassigning `l.file` while `handleValue` reads it → unsynchronized pointer race
- `go test -race` will fail immediately once any integration test exists
- `string` reads of `ActiveSessionID` can tear into len/data mismatches under the Go memory model

## Findings

- **Location**: `internal/ntlogger/logger.go` (entire file, no sync primitives)
- **Call sites**: `cmd/ravenlink/main.go:285-291, 309, 313, 327-328`
- Flagged independently by both architecture and code quality reviewers.

## Proposed Solutions

### Option A: Actor pattern with command channel (recommended)
Convert `Logger` to an actor that owns its own state and receives commands on a single input channel:
```go
type Command interface{ isCommand() }
type StartCmd struct{}
type EndCmd struct{}
type MatchCmd struct{ Type string; FMS FMSState }
type ValueCmd struct{ TV ntclient.TopicValue }
```
`Run(ctx)` is the only goroutine that touches mutable state. External callers push commands. Readers read from snapshot channels or atomic gauges.
- Pros: No locks, no races, idiomatic Go concurrency
- Cons: More refactoring; readers need a different mechanism for EntriesWritten
- Effort: Medium
- Risk: Medium (changes API shape)

### Option B: sync.Mutex around all mutable state
Add `mu sync.Mutex` to Logger. Every access to `file`, `EntriesWritten`, `ActiveSessionID` takes the lock.
- Pros: Minimal change, easy to review
- Cons: Lock contention; readers have to acquire a write lock to increment
- Effort: Small
- Risk: Low

### Option C: Atomic counters + mutex for file ops
`atomic.Int64` for `EntriesWritten`, `atomic.Pointer[string]` for `ActiveSessionID`, mutex only for file ops.
- Pros: Faster than full mutex
- Cons: Mixed concurrency primitives
- Effort: Small
- Risk: Low

## Recommended Action

**Option A (actor)** — matches the existing goroutine-per-module pattern and eliminates races structurally rather than hiding them behind locks.

## Technical Details

- Affected files:
  - `internal/ntlogger/logger.go` (restructure around command channel)
  - `cmd/ravenlink/main.go` (push commands instead of direct field access)
  - `internal/status/status.go` (expose EntriesWritten via gauge/channel from the logger)

## Acceptance Criteria

- [ ] `go test -race ./...` passes
- [ ] No direct field access on `Logger` from outside the package (callers use methods that push commands)
- [ ] `EntriesWritten` is published to status via a safe mechanism (atomic or snapshot)
- [ ] Concurrent value writes and match events produce well-formed JSONL (integration test with scripted events)
