---
status: pending
priority: p2
issue_id: 011
tags: [code-review, testing, quality]
dependencies: []
---

# P2: Zero test coverage outside statemachine — no safety net for refactoring

## Problem Statement

The `statemachine` package has excellent tests (53 passing, table-ish cases, injectable FakeClock). Every other package has **zero tests**:

- `ntclient/protocol.go` — MessagePack encode/decode, type conversions
- `ntclient/client.go` — NT4 WebSocket flow, announce handling, reconnect
- `ntlogger/logger.go` — JSONL format, session lifecycle, match markers
- `uploader/uploader.go` — store-and-forward protocol, resume from server count, batching
- `uploader/auth.go` — JWT parsing, login, expiry, renewal
- `config/config.go` — YAML loading, CLI flag override, SaveConfig round-trip
- `status/status.go` — thread-safe snapshot/update
- `dashboard/server.go` — GET/POST handlers

Critical untested paths:
1. **JSONL format stability** — upload depends on `session_start` being the first line with specific field names. If ntlogger ever drifts, uploader silently skips files ("no session_start found").
2. **Upload resume logic** — the `serverCount >= len(entries)` branch, batching math, `findLastTimestamp` — none tested.
3. **JWT expiry parsing** — `decodeJWTExp` silently returns zero time on any error, meaning a malformed JWT makes the uploader re-login forever.
4. **FMS dispatch** — the forwarder in main.go that picks `/FMSInfo/FMSControlData` out of the stream is where P1 #002 lives and has no test that would catch it.
5. **NT protocol round-trip** — `DecodeDataFrame`, `toInt`, `toInt64`, `typeNameToID` are all pure functions with no tests.

Without these tests, it's impossible to refactor safely — every change is blind.

## Findings

- **Evidence**: `find . -name "*_test.go"` returns only `internal/statemachine/machine_test.go`
- **Agent**: Code quality reviewer flagged as P2 #15

## Proposed Solutions

### Minimum viable test coverage (recommended)

Create these test files in priority order:

1. **`internal/ntclient/protocol_test.go`** — round-trip Encode/Decode, boundary cases, type conversions, invalid input handling
2. **`internal/ntlogger/logger_test.go`** — StartSession + write + EndSession, session_start format, match markers, empty sessions, concurrent safety (after #003)
3. **`internal/uploader/uploader_test.go`** — `httptest.Server` simulating RavenBrain, happy path, resume from server count, 401 retry, batching math, partial failure
4. **`internal/uploader/auth_test.go`** — real base64url JWT for `decodeJWTExp`, token expiry, re-login, malformed JWT handling
5. **`internal/config/config_test.go`** — YAML round-trip (SaveConfig → LoadConfig), CLI flag override, default merge, malformed YAML
6. **Integration test in `cmd/ravenlink`** — mock `nt.Values()` with scripted FMSControlData sequence, assert logger emits correct match-start/match-end markers. **This would have caught both #001 and #002.**

Effort: Medium (several hours to set up + write tests)
Risk: None

## Recommended Action

Write tests in the order above. The integration test is the highest-value single test because it exercises the full flow that currently doesn't work (#001, #002).

## Technical Details

- Affected files: create new `*_test.go` files under each internal package
- Use `httptest.Server` for uploader tests (stdlib, no mocks needed)
- Use `testing/synctest` or custom FakeClock pattern for time-dependent tests

## Acceptance Criteria

- [ ] `go test ./...` runs more than one test package
- [ ] `go test -race ./...` passes (depends on #003 being fixed)
- [ ] Upload resume logic has at least 4 test cases (fresh, partial, complete, 401 retry)
- [ ] JWT parsing has tests for valid, expired, malformed, and missing-exp tokens
- [ ] End-to-end test: scripted NT sequence → JSONL file → upload → RavenBrain receives correct data
- [ ] CI fails on test failure
