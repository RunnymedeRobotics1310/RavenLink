---
status: pending
priority: p2
issue_id: 010
tags: [code-review, dashboard, ux, config]
dependencies: []
---

# P2: Config hot-reload is advertised but almost nothing actually reloads

## Problem Statement

The dashboard has a "Reload" button and the CLAUDE.md documents hot-reloadable fields, but at runtime **only `LaunchOnLogin` actually hot-reloads**. Everything else is silently ignored until restart:

- **`poll_interval`** — captured into `time.NewTicker` at `runMainLoop` start (`cmd/ravenlink/main.go:235`)
- **`upload_interval`** — captured into `time.NewTicker` at `runMainLoop` start (line 245)
- **`stop_delay`, `auto_teleop_gap`, `nt_disconnect_grace`, `record_trigger`** — copied into `statemachine.Machine` at `NewMachine` time (`internal/statemachine/machine.go:85-97`) with no setters
- **`nt_paths`** — stored in ntclient but only applied on reconnect
- **`obs_host`, `obs_port`, `obs_password`** — baked into OBS client at `New` time
- **`data_dir`** — baked into logger and uploader at construction
- **`dashboard.port`, `dashboard.enabled`** — requires full restart

The dashboard's config editor currently tags some fields as "restart required" in the UI, but the mapping is incomplete and the Reload button gives users false confidence that their edits took effect.

## Findings

- **Location**: `cmd/ravenlink/main.go:235,245`, `internal/statemachine/machine.go:85-97`, `internal/dashboard/server.go:216` (`*s.cfg = *newCfg`)
- **Agent**: Architecture reviewer flagged as P2 #7

## Proposed Solutions

### Option A: Honest UI — mark everything as restart-required except LaunchOnLogin and LogLevel (recommended)
Update the dashboard UI to clearly indicate which fields take effect immediately vs need restart. Gray out the "Reload" button or rename it to "Save (restart required for most changes)".
- Pros: Honest; zero backend changes
- Cons: Less polished UX
- Effort: Small
- Risk: Low

### Option B: Wire live updates where feasible
Add setters to state machine (`SetStopDelay`, `SetRecordTrigger` etc.) and to ticker intervals (channel-based reset). For poll_interval and upload_interval, use `time.Reset` on the tickers.
- Pros: Actually works
- Cons: Needs locking on state machine; more code to test
- Effort: Medium
- Risk: Medium (data races if not careful)

### Option C: Restart on save
When user clicks Save on any field, trigger a graceful self-restart (exec self). Works for all fields.
- Pros: Simplest mental model
- Cons: User loses any in-flight session; unusual pattern
- Effort: Small
- Risk: Medium (session loss during match)

## Recommended Action

**Option A for now**, with **Option B for LogLevel** (which is trivially hot-reloadable via `logging.SetLevel` style). Live-update of state machine fields can be a follow-up.

## Technical Details

- Affected files:
  - `internal/dashboard/static/index.html` (UI clarification)
  - `internal/dashboard/server.go` (return `restart_required` metadata in GET /api/config)
  - `cmd/ravenlink/main.go` (add LogLevel reload hook)

## Acceptance Criteria

- [ ] Dashboard UI clearly shows which fields take effect immediately vs need restart
- [ ] LogLevel change via dashboard takes effect on next log line without restart
- [ ] Reload button is either removed or clearly labeled
- [ ] CLAUDE.md hot-reload table updated to reflect reality
