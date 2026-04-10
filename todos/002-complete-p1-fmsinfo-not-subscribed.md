---
status: pending
priority: p1
issue_id: 002
tags: [code-review, correctness, ntclient, state-machine]
dependencies: []
---

# P1: /FMSInfo/ never subscribed — state machine never sees a match

## Problem Statement

Default `NTPaths` in `internal/config/config.go:74-78` is:
```go
NTPaths: []string{"/SmartDashboard/", "/Shuffleboard/"},
```

The state machine watches for `/FMSInfo/FMSControlData` to detect match state. The NT4 client subscribes only to the configured prefixes — **it never subscribes to `/FMSInfo/`**, so `fmsCh` never receives a value, so `fms` remains `FMSStateDisconnected()` forever, so **no recording ever starts automatically**.

Combined with #001 (logger drops everything), the bridge is completely non-functional out of the box.

## Findings

- **Location**: `internal/config/config.go:74-78` (defaults), `cmd/ravenlink/main.go:108` (`nt.Connect(..., cfg.Telemetry.NTPaths)`)
- **Evidence**: The state machine's `fmsCh` branch in `runMainLoop` requires `v.Name == FMSControlDataKey` to update `fms`, but that topic is never subscribed.
- Flagged by the code quality reviewer. Silently untested because there are no integration tests for the main loop.

## Proposed Solutions

### Option A: Hardcode `/FMSInfo/` in `ntclient.Connect` (recommended)
Always prepend `/FMSInfo/` to the user's prefix list inside the NT client. The bridge can't function without it, so it should not be optional.
- Pros: Can't be disabled accidentally; explicit in one place
- Cons: Couples ntclient to state machine concepts
- Effort: Small
- Risk: Low

### Option B: Add `/FMSInfo/` to default config
Add to `DefaultConfig()` in `internal/config/config.go`.
- Pros: Visible in config.yaml
- Cons: Users can still remove it and break the bridge silently
- Effort: Small
- Risk: Medium (user can footgun themselves)

### Option C: Both A and B
Hardcode in ntclient AND include in the default config for clarity.
- Effort: Small
- Risk: Lowest

## Recommended Action

**Option C** — defense in depth. The hardcoded version is the safety net; the default config documents intent.

## Technical Details

- Affected files:
  - `internal/ntclient/client.go` (inject `/FMSInfo/` into prefixes)
  - `internal/config/config.go` (add to DefaultConfig NTPaths)

## Acceptance Criteria

- [ ] Connecting to a robot delivers `/FMSInfo/FMSControlData` value changes to `fmsCh`
- [ ] State machine transitions to `RecordingAuto` when FMS is attached + enabled
- [ ] Removing `/FMSInfo/` from config doesn't break the bridge (ntclient still subscribes)
- [ ] Integration test asserts FMS topic is always in the subscribed set
