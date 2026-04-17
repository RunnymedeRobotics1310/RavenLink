---
title: "feat: Limelight uptime monitor"
type: feat
status: active
date: 2026-04-17
---

# feat: Limelight uptime monitor

## Overview

Add a polling monitor that tracks Limelight camera uptime and reachability. For each configured Limelight IP, the monitor issues an HTTP `GET http://<ip>:5807/results` once per second with a 200 ms timeout, extracts the `ts` field (ms since boot) from the JSON response, and feeds two synthetic topic values into the existing session logger:

- `/RavenLink/Limelight/<last_octet>/uptime_ms` (int) — raw `ts` value
- `/RavenLink/Limelight/<last_octet>/reachable` (boolean)

Post-processing (AdvantageScope or ad-hoc analysis) derives reboot events (uptime going backwards) and outage windows (runs of `reachable=false`) without needing any extra runtime logic on the RavenLink side.

## Problem Frame

Limelights can reboot mid-match or fall off the network. Today the team has no durable record of when that happens, so post-match debugging can't distinguish "limelight was flaky" from "vision code was flaky" from "radio was flaky". We already have a durable per-session JSONL pipeline with timestamped entries — the lowest-friction way to track this is to teach that pipeline to record Limelight state alongside the NetworkTables values it already captures.

Every FRC team in this stack uses the same `http://<robot-ip-base>.11:5807/results` JSON endpoint (Limelight's built-in results REST surface), so the monitor is portable; only the last-octet list varies by installation.

## Requirements Trace

- R1. Poll every configured Limelight every 1 s with a 200 ms request timeout.
- R2. On success, log the JSON `ts` value (ms since boot) under a synthetic topic name.
- R3. On timeout, HTTP error, non-2xx status, or malformed JSON, log `reachable=false` for that Limelight. On success log `reachable=true` (plus the `ts` value).
- R4. Support an arbitrary list of Limelight last-octets (team default: `[11, 12]`, derived from `10.TE.AM.<octet>`).
- R5. Entries land in the same JSONL session file as NT4 data and ride the existing RavenBrain upload pipeline.
- R6. Feature can be disabled via config without code changes.
- R7. Monitor survives Limelight unreachability indefinitely without leaking goroutines, sockets, or flooding the log.

## Scope Boundaries

- Not monitoring Limelight pipeline, targets, or pose data (those already flow over NT4 if the robot publishes them).
- Not adding dashboard UI for Limelight status. Live visibility can be added later; initial feature is JSONL-only.
- Not sending health alerts, Slack messages, or any real-time notification.
- Not implementing adaptive backoff for unreachable Limelights — every tick polls every configured IP regardless of prior state. (1 Hz × 2 cameras × 200 ms timeout is a trivial load.)

### Deferred to Separate Tasks

- Dashboard display of current Limelight status: separate PR once the data shape proves out.
- WPILog export naming convention for `/RavenLink/Limelight/*` entries: already handled by the existing `NT:` prefix logic in `internal/wpilog/convert.go`, no change needed.

## Context & Research

### Relevant Code and Patterns

- `internal/ntclient/client.go` — defines `TopicValue{Name, Type, Value, ServerTimeMicros}`. Our monitor emits values with this shape so downstream code (logger, uploader, wpilog export) treats Limelight data identically to NT data. `ServerTimeMicros: 0` is fine; `internal/wpilog/convert.go:resolveTimestamp` already falls back to wall-clock when `server_ts == 0`.
- `internal/ntlogger/logger.go:213-222` — `handleValue` buffers every value in `latestValues` (for session-start replay) and writes only when a file is open. This gives us session-lifecycle gating for free; the monitor does not need to know whether a session is active.
- `cmd/ravenlink/main.go:295-323` — single fan-out goroutine between `nt.Values()` and `logCh`. The cleanest extension point: add a second `case` on the same select that reads from a `limelightMonitor.Values()` channel and forwards into `logCh` identically.
- `internal/config/config.go` — YAML-driven config with subsections per feature area (`bridge`, `telemetry`, `ravenbrain`, `dashboard`). Precedent for adding a new section (`limelight`) with defaults in `DefaultConfig()` and a matching template in `cmd/ravenlink/main.go:writeTemplateConfig`.
- `internal/config/config.go:199-205` — `RobotIP()` helper derives the robot IP as `10.TE.AM.2`. Same formula, different last octet, works for Limelights.
- `internal/obsclient/client.go` — in-tree example of a long-running subsystem that wraps an external client, handles reconnection/retry, and exposes a health signal. Useful shape reference even though HTTP and WebSocket differ in failure mode detail.

### Institutional Learnings

- `docs/plans/2026-04-09-005-refactor-rewrite-in-go-plan.md` — the Go rewrite pattern for "new subsystem": standalone package with a `Run(ctx)` actor loop, owned mutable state, a `Values()` channel for outputs, and construction via a factory. Limelight monitor should follow this same shape.
- Existing design note (from project CLAUDE.md): new subsystems are constructed in `main.go` and wired into the main loop. `ntlogger` and `uploader` are the best-shaped precedents.

### External References

- Limelight results JSON documentation: https://docs.limelightvision.io/docs/docs-limelight/apis/complete-networktables-api#json-results — confirms the `ts` field is Limelight uptime in milliseconds and is stable across firmware versions.

## Key Technical Decisions

- **Feed the shared `logCh` via the existing fan-out**: the monitor emits `ntclient.TopicValue` on its own channel, and the fan-out goroutine in `main.go` merges it with `nt.Values()`. This reuses session gating, replay, upload, and WPILog export without touching any of that code.
- **Two topics per Limelight, not one struct**: `uptime_ms` + `reachable` as independent entries. Cheaper to consume in analysis, renders cleanly in AdvantageScope without needing a schema entry, and `reachable=false` with no accompanying `uptime_ms` change is an unambiguous outage marker.
- **Under `/RavenLink/Limelight/<last_octet>/`**: the `/RavenLink/` namespace is already used for synthetic topics (match events) and is unambiguously ours. Using last-octet (e.g. `11`) rather than full IP keeps the key short and stable across team-number changes in test environments.
- **Standalone `net/http.Client` per Limelight is fine** — tiny constant memory, no connection pooling benefit at 1 Hz per camera. Use a shared package-level `http.Client` with a per-request context timeout rather than `client.Timeout`, so we can unit-test with a controllable cancel.
- **Non-blocking send to output channel**: on the rare chance the fan-out falls behind, drop the limelight update (same policy as NT values in the fan-out today). A dropped sample is vastly preferable to a blocked monitor.
- **Poll all Limelights concurrently per tick**: each `time.Tick` fires N goroutines (one per configured last-octet) with a 200 ms deadline. Worst-case one tick is `200 ms`, not `N × 200 ms`. At N=2 this is academic, but the shape is right.

## Open Questions

### Resolved During Planning

- **What key should Limelight entries use?** → `/RavenLink/Limelight/<last_octet>/uptime_ms` and `/RavenLink/Limelight/<last_octet>/reachable`. User said "make something up"; this keeps the `/RavenLink/` namespace for all synthetic topics.
- **Should unreachable Limelights emit any entry at all or just go silent?** → Emit `reachable=false`. Silence is ambiguous — was the Limelight down, or was RavenLink itself down? An explicit false disambiguates.
- **Should the monitor respect the collect pause/session lifecycle?** → Yes, by construction. Feeding `logCh` means `ntlogger.handleValue` gates writes on `l.file != nil`, which is controlled by session start/stop. No special case needed.
- **What to do when team==0 (first-run)?** → Skip monitor startup, same as NT/OBS/uploader. IP derivation is meaningless without a team number.

### Deferred to Implementation

- Exact `context.WithTimeout` vs. `http.Client.Timeout` choice — both work; pick whichever produces the cleanest test harness when writing the unit tests.
- Whether to emit `reachable=true` every tick (constant baseline) or only on transitions — start with every-tick for simplicity; if JSONL volume becomes a concern, squelch unchanged `reachable` updates in a follow-up.
- Whether to parse the full JSON body or scan just for the `ts` field — start with full `json.Unmarshal` into a minimal struct; optimize only if profiling shows it matters.

## Implementation Units

- [ ] **Unit 1: Limelight monitor package**

**Goal:** Add `internal/limelight` with a `Monitor` that polls configured Limelights at a fixed interval and streams `ntclient.TopicValue` updates on an output channel.

**Requirements:** R1, R2, R3, R4, R7

**Dependencies:** None

**Files:**
- Create: `internal/limelight/monitor.go`
- Test: `internal/limelight/monitor_test.go`

**Approach:**
- Expose `New(team int, lastOctets []int, pollInterval time.Duration, timeout time.Duration, bufSize int) *Monitor`.
- Internally derive each target URL as `http://10.{team/100}.{team%100}.{octet}:5807/results`.
- `Run(ctx context.Context)` is the actor loop. On each `time.Ticker.C` fire, spawn N lightweight goroutines (one per last-octet) that each do a single request+parse and send zero or one `TopicValue` pair (uptime + reachable) on the output channel via non-blocking send. Goroutines return when the request completes or the per-request context deadline expires.
- `Values() <-chan ntclient.TopicValue` returns the output channel. Closed on `Run` exit.
- `Close(timeout)` is not needed; lifecycle is driven by `ctx` exclusively, consistent with `ntlogger`.
- Use a package-level `http.Client{}` (no timeout set on the client itself; per-request `context.WithTimeout`).
- Parse JSON into a minimal `struct { TS int64 `json:"ts"` }`. Any unmarshal failure → `reachable=false`, no `uptime_ms`.
- `reachable=true` plus `uptime_ms` are emitted as two separate `TopicValue` sends on success. On failure, only `reachable=false` is sent.

**Patterns to follow:**
- `internal/ntlogger/logger.go` — `Run(ctx)` actor loop shape, owned state, channel lifecycle.
- `internal/ntclient/client.go` — non-blocking channel send on the output channel (drop on full rather than block).

**Test scenarios:**
- Happy path: fake `httptest.Server` returns `{"ts": 12345}` → monitor emits exactly one `reachable=true` (bool, value true) and one `uptime_ms` (int, value 12345) `TopicValue`, both with the expected key containing the last octet.
- Edge case: multiple last-octets configured with the same fake server responding to all → each tick emits 2×N updates (one per camera × two topics), all distinct keys.
- Edge case: server responds with `{"ts": 0}` (Limelight freshly booted) → emits uptime=0 plus reachable=true. Zero is a legitimate value, not a sentinel.
- Error path: fake server delays response longer than timeout → monitor emits `reachable=false` with no `uptime_ms` update. Next tick with fast response emits both again.
- Error path: fake server returns HTTP 500 → `reachable=false`.
- Error path: fake server returns 200 with non-JSON body → `reachable=false`.
- Error path: fake server returns 200 with JSON lacking `ts` field → `reachable=false` (treat missing field as malformed from the monitor's point of view).
- Error path: target host refuses connection → `reachable=false`.
- Integration: cancelling the `Run` context closes the output channel and returns within one tick interval. No goroutine leaks (goleak or manual inspection in the test).
- Integration: full-channel backpressure — consumer reads slowly, monitor continues polling without blocking; newer updates replace older on a full channel (same drop-oldest semantics as `nt.Values()`).

**Verification:**
- `go test ./internal/limelight/ -race` passes.
- Running the monitor against the sim's actual Limelight IPs (or a local `httptest` double) for 10 s produces 10 `reachable=true` updates and 10 `uptime_ms` updates per configured camera.

- [ ] **Unit 2: Config surface for Limelight monitor**

**Goal:** Expose Limelight monitor configuration through `internal/config` and the YAML template.

**Requirements:** R4, R6

**Dependencies:** Unit 1 (type choices for the monitor constructor drive the config shape).

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/ravenlink/main.go` (`writeTemplateConfig` only — wiring is Unit 3)

**Approach:**
- Add a `LimelightConfig` struct with fields `Enabled bool`, `LastOctets []int`, `PollInterval float64` (seconds), `TimeoutMS int`. YAML tags match existing `snake_case` convention.
- Add `Limelight LimelightConfig` to the top-level `Config` struct.
- `DefaultConfig()` sets `Enabled: true`, `LastOctets: []int{11, 12}`, `PollInterval: 1.0`, `TimeoutMS: 200`.
- Extend the first-run `writeTemplateConfig` YAML with a `limelight:` block mirroring the defaults, documented with short comments.
- `LoadConfig` backfill: if an older config lacks the `limelight` section entirely, keep the defaults (YAML unmarshal leaves zero values; extend the post-unmarshal backfill block to re-apply defaults when `LastOctets` is nil and `PollInterval == 0`).
- Do **not** add CLI flags for these — per project convention, runtime tuning happens via the dashboard-triggered save + restart.

**Patterns to follow:**
- `internal/config/config.go:DefaultConfig` for default values.
- Existing backfill pattern at the bottom of `LoadConfig` for missing fields in older configs.

**Test scenarios:**
- Happy path: round-trip a YAML blob containing a full `limelight:` section → `Config` has the expected values.
- Happy path: YAML omitting `limelight:` entirely → `Config.Limelight` has the `DefaultConfig` values (via backfill).
- Edge case: YAML with `limelight.enabled: false` and no other fields → `Enabled` is false and defaults apply to the rest.
- Edge case: YAML with `last_octets: []` (explicit empty) → honored as empty, monitor will start but poll nothing. (Acceptable; the user opted in to this.)

**Verification:**
- `go test ./internal/config/ -race` passes.
- A fresh run with no `config.yaml` writes a template that contains the `limelight:` section and is parseable without modification.

- [ ] **Unit 3: Wire monitor into the main loop**

**Goal:** Construct the Limelight monitor at startup (when `team != 0` and `limelight.enabled`) and merge its output into the existing `logCh` fan-out.

**Requirements:** R5, R6, R7

**Dependencies:** Units 1 and 2.

**Files:**
- Modify: `cmd/ravenlink/main.go`

**Approach:**
- In the `if !firstRun` block where `nt`, `obs`, and `ntLog` are constructed, construct `lm := limelight.New(cfg.Bridge.Team, cfg.Limelight.LastOctets, cfg.Limelight.PollInterval*time.Second, cfg.Limelight.TimeoutMS*time.Millisecond, 32)` guarded by `cfg.Limelight.Enabled`.
- Launch `lm.Run(ctx)` in a new goroutine (added to the existing `wg`).
- Extend the fan-out goroutine at `main.go:299-323` with a second `case v, ok := <-lm.Values():` that forwards into `logCh` using the same non-blocking pattern as NT values. When `lm` is nil (disabled), pass a nil channel so the `case` never fires (Go idiom for conditionally disabled select arms).
- No changes to `ntLog`, `up`, `dash`, or tray — they consume from `logCh` and status respectively and need no Limelight awareness.

**Patterns to follow:**
- Existing fan-out goroutine pattern in `main.go:299-323`.
- Existing `if !firstRun` guarded subsystem construction.

**Test scenarios:**
- Test expectation: none — this unit is wiring. Unit 1's tests cover monitor behavior; end-to-end verification is a manual smoke test (below).

**Verification:**
- Run RavenLink against the sim (or pointed at a Limelight-emulating `httptest` server on 127.0.0.1:5807 with a patched host lookup) with `collect_trigger: any` and enable collection. A session file contains entries with keys starting `/RavenLink/Limelight/`.
- Toggle `limelight.enabled: false` → restart → session file contains zero `/RavenLink/Limelight/` entries and no goroutine for the monitor is live (verifiable via `runtime.NumGoroutine` before/after, or absence of HTTP traffic to `:5807`).
- Pull the Ethernet cable to the robot (or point the last-octets at an unreachable IP) → `/RavenLink/Limelight/<octet>/reachable` entries with `value: false` appear at 1 Hz.

- [ ] **Unit 4: Dashboard display of Limelight config (optional, only if scope allows)**

**Goal:** Render the Limelight section in the dashboard config editor so the user can toggle enabled/interval/octets without hand-editing YAML.

**Requirements:** R6 (usability)

**Dependencies:** Unit 2.

**Files:**
- Modify: `internal/dashboard/static/index.html`
- Modify: `internal/dashboard/server.go`

**Approach:**
- Add `limelight` to the known config section list in `index.html` and render fields.
- Add the matching cases to `server.go`'s POST handler to parse them back into `cfg.Limelight` on save.
- Follow the pattern used for `telemetry.nt_paths` (comma-separated list) for `last_octets`.

**Execution note:** This unit is deferrable; Unit 3 is a complete feature without it. Lands separately if time-boxed.

**Test scenarios:**
- Test expectation: none — dashboard UI is not unit-tested today. Manual smoke: save a Limelight config change, restart triggers, the new values persist.

**Verification:**
- Dashboard shows the Limelight section; saving applies on restart.

## System-Wide Impact

- **Interaction graph:** Monitor produces into a new channel; fan-out goroutine reads it and writes to the existing `logCh`. `ntlogger`, `uploader`, `dashboard`, and `wpilog` export are downstream and require no changes — they see Limelight entries as normal `TopicValue`s.
- **Error propagation:** HTTP failures are swallowed inside the monitor and surfaced as `reachable=false`. No failure in the Limelight monitor can crash or hang RavenLink.
- **State lifecycle risks:** Monitor holds no persistent state beyond the active HTTP request context. No file handles, no DB, no cache. Ctx cancellation → all in-flight requests cancel, all goroutines return, output channel closes.
- **API surface parity:** `/RavenLink/Limelight/*` entries flow through the same JSONL → upload → WPILog path as everything else. The existing `NT:` prefix logic in `internal/wpilog/convert.go` applies to them automatically, making them discoverable in AdvantageScope.
- **Integration coverage:** Unit 1 covers monitor internals. The fan-out merge is trivial enough that Unit 3's verification list is sufficient; a regression test would require either stubbing the ntclient channel or an integration harness that currently does not exist and is out of scope.
- **Unchanged invariants:** `ntclient`, `ntlogger`, `uploader`, `dashboard`, `wpilog`, and state-machine behavior are unchanged by this plan. This is additive.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| JSONL volume at 2 cameras × 2 topics × 1 Hz = 4 entries/s adds noise to session files. | Negligible at realistic session lengths (sub-match: ~600 entries). If it ever matters, squelch unchanged `reachable` updates in a follow-up. |
| Limelight's `/results` endpoint payload is large (all pipeline outputs) even though we only want `ts`. | 200 ms timeout and 1 Hz cadence make even a ~10 KB response trivially cheap. Not worth optimizing until profiling says otherwise. |
| Firmware version returns `ts` under a different field name (e.g. `tc` for some Limelight 3 firmwares). | Validate against the actual Limelight firmware in use during smoke testing. If a field rename ships, bump the parse logic; the surface is one line. |
| First-run mode (team==0) must not try to construct URLs. | Unit 3 guards monitor construction with `!firstRun`, consistent with NT/OBS/logger/uploader. |
| Config hot-reload is not supported; `limelight.enabled` toggle requires a restart. | Existing project convention (save-triggers-restart); no new code paths needed. |

## Documentation / Operational Notes

- Update the "Config Sections" table in `CLAUDE.md` to list `limelight` and note that its fields are hot-reloadable only via the save-triggered restart.
- Add a short operator note (either in the dashboard help text or `CLAUDE.md` Gotchas): "If `reachable` flips false repeatedly but the Limelight is reachable from your laptop, confirm the Driver Station IP is on the robot subnet — RavenLink polls from whatever machine is running it."

## Sources & References

- Limelight `/results` JSON surface: https://docs.limelightvision.io/docs/docs-limelight/apis/complete-networktables-api#json-results
- Existing fan-out pattern: `cmd/ravenlink/main.go:299-323`
- Existing subsystem actor-loop pattern: `internal/ntlogger/logger.go:121-149`
- Existing config backfill pattern: `internal/config/config.go:115-119`
