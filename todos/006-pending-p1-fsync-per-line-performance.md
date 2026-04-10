---
status: pending
priority: p1
issue_id: 006
tags: [code-review, performance, ntlogger, disk-io]
dependencies: []
---

# P1: fsync per JSONL line — will wreck SSD and cause OBS frame drops

## Problem Statement

`internal/ntlogger/logger.go:205` (in `writeLine`) calls `l.file.Sync()` **after every single line** written.

```go
l.file.Sync()  // fsync on every NT value
```

At a realistic NT data rate of 100–500 values/second (typical for `/SmartDashboard/` traffic), this triggers **hundreds of fsync calls per second**. Each fsync is a synchronous write-through to SSD that:

1. Pegs disk I/O bandwidth
2. Wrecks SSD write endurance over a season
3. **Causes OBS frame drops** because OBS is writing video to the same disk on the same machine
4. Adds latency to every JSONL write, creating backpressure on the logger goroutine

## Findings

- **Location**: `internal/ntlogger/logger.go:205`
- **Evidence**: `fsync` per write on any disk (even NVMe) caps throughput in the low thousands/sec, and coexisting with OBS video encoding is the textbook disk-contention scenario.
- Flagged by code quality reviewer.

## Proposed Solutions

### Option A: Periodic sync via ticker (recommended)
Use `bufio.Writer` wrapping the file. Flush the bufio buffer on every line, but only call `file.Sync()` every N seconds (say 2) via a ticker. Also sync on `match_start` / `match_end` / `session_end` for crash safety at boundaries.
- Pros: Throughput near memory speed; crash-safe at boundaries
- Cons: Up to 2 seconds of data loss on hard crash
- Effort: Small
- Risk: Low

### Option B: Never fsync, rely on OS write-back cache
Remove `Sync()` entirely. Trust the OS to flush periodically. Explicit `Sync()` on `EndSession()`.
- Pros: Best performance
- Cons: Up to ~30s of data loss on hard crash
- Effort: Small
- Risk: Low (the data is in flight to RavenBrain anyway; a crash during a match is rare)

### Option C: Use O_DSYNC on open
Open the file with O_DSYNC so the OS syncs automatically. Same performance problem as current.
- Not recommended.

## Recommended Action

**Option A** — periodic sync every 2s + explicit sync at session/match boundaries. This is the balance of crash-safety and throughput every mature log writer uses.

## Technical Details

- Affected files:
  - `internal/ntlogger/logger.go` (wrap file in bufio.Writer, add sync ticker, remove per-line Sync)

## Acceptance Criteria

- [ ] Writing 1000 values takes < 100ms (vs current likely 1-5 seconds)
- [ ] `session_start`, `match_start`, `match_end`, `session_end` are always flushed+synced
- [ ] Data loss on hard crash is bounded to ~2 seconds of values
- [ ] Benchmark: sustained 500 values/sec does not cause disk I/O utilization > 10%
