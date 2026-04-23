---
title: 'refactor: Split RavenBrain and RavenScope into independent upload targets'
type: refactor
status: active
date: 2026-04-23
deepened: 2026-04-23
---

# refactor: Split RavenBrain and RavenScope into independent upload targets

## Overview

RavenScope support was added to branch `feat/ravenscope-bearer-auth` as a
bearer-token auth mode *inside* the existing `ravenbrain` config section —
the same URL, the same batch settings, one auth field or the other.
Operationally this forces a choice: RavenBrain *or* RavenScope, never both.

This refactor separates the two destinations into two sibling config
sections, each with its own `enabled` flag, URL, auth shape, and batch
tuning. The uploader becomes a multi-target coordinator that fans each
pending session file out to every enabled target independently. A file is
only moved from `pending/` to `uploaded/` once all enabled targets have
accepted it; per-file state is tracked via small sidecar marker files so
the semantics survive restarts.

End result: RavenLink can ship a session to **none**, **one**, or **both**
servers based on config, with independent status indicators in the
dashboard and tray for each.

---

## Problem Frame

The bearer-auth work assumed a smooth cutover: teams would set
`ravenbrain.api_key` once RavenScope was ready and stop using
username/password. In practice we want both destinations running in
parallel — RavenBrain remains the authoritative team server during the
season, while RavenScope is the cloud mirror we iterate on. Folding them
into one `ravenbrain` section forces every deployment to choose a
destination and makes it impossible to, for example, validate RavenScope
against real match data while RavenBrain keeps its current role.

The fix needs to:

- Model each destination as an independent target with its own URL,
  enabled flag, auth, and batch tuning.
- Route each completed session file to every enabled target, surviving
  per-target outages without losing data for the still-healthy target.
- Preserve the existing local-only mode (no destinations configured) and
  the current per-file idempotency semantics (server-side
  `uploadedCount`).
- Keep the dashboard, tray, and status API honest — two destinations get
  two reachability indicators, not one combined signal.

---

## Requirements Trace

- R1. Config exposes two independent sections: `ravenbrain` and
  `ravenscope`. Each carries `enabled`, `url`, auth fields appropriate to
  the server, `batch_size`, and `upload_interval`.
- R2. The uploader sends every completed session file to every *enabled*
  target independently. A failure on one target does not block delivery
  to the other.
- R3. Zero enabled targets degrade to local-only mode (same behavior as
  today when `ravenbrain.url == ""`).
- R4. Files are moved from `pending/` to `uploaded/` only after every
  enabled target has accepted them. Per-target completion survives
  process restarts.
- R5. Existing configs that set `ravenbrain.api_key` today migrate
  transparently on first load so users on the current branch do not lose
  their RavenScope wiring.
- R6. Dashboard, tray, and status API expose per-target reachability,
  pending count, uploaded count, and last-result — not a single combined
  "RavenBrain" signal.
- R7. Legacy `ravenbrain.username`/`password` behavior is preserved for
  the RavenBrain target (no silent change to auth mode).
- R8. `cmd/rbping` still works, and can be pointed at either target.

---

## Scope Boundaries

- Not adding new protocol features or endpoints — both targets speak the
  existing `/api/telemetry/session` contract.
- Not changing the session JSONL format or the on-wire request schema.
- Not changing the Limelight monitor, OBS integration, or NT client.
- Not touching the WPILog exporter (session files are the input;
  destinations are orthogonal).
- Not introducing a generic "N targets" framework — the plan hardcodes
  exactly two named targets (`ravenbrain`, `ravenscope`) because that
  matches the product shape and keeps config simple. If a third
  destination ever becomes interesting we revisit.
- Not adding per-target filtering (e.g., "send markers to RavenBrain but
  not RavenScope"). Both targets get the full stream.

---

## Context & Research

### Relevant Code and Patterns

- `internal/config/config.go` — `RavenBrainConfig` struct (lines 51–68),
  `DefaultConfig()` defaults block (108–115), YAML load+backfill logic
  (131–157), `ParseFlags` CLI wiring (246–331), save-and-restart contract
  (CLAUDE.md gotcha §11). Limelight section (79–84, 120–126, 151–154) is
  the closest existing example of an "enabled + settings + backfill on
  load" section — its pattern of treating zero values as "section
  absent" is directly reusable for the new ravenscope section.
- `internal/uploader/auth.go` — already has two construction paths
  (`NewAuth` for legacy, `NewAuthWithKey` for bearer). RavenScope target
  uses `NewAuthWithKey`; RavenBrain target uses `NewAuth`. The
  `SetAPIKey` switch used on the current branch becomes unnecessary once
  the configs split — each target is constructed with the right auth
  mode directly.
- `internal/uploader/uploader.go` — the single-target shape is the
  starting point. The per-file flow (`uploadFile`: POST `/session` →
  GET uploadedCount → POST data batches → POST `/complete`) is already
  idempotent against server-side progress and will apply to each target
  unchanged.
- `internal/status/status.go` — single `Status` struct with flat
  `RavenBrainReachable`, `FilesPending`, `FilesUploaded`,
  `LastUploadResult`, `CurrentlyUploading` fields (15–48). These become
  a slice of per-target records.
- `internal/dashboard/server.go` — config GET/POST uses a flat
  `key → value` map (lines 55–59, 329, 349–353, 426–437). We extend
  that map with `ravenscope_*` keys rather than introducing a nested
  structure, to keep dashboard JS churn minimal.
- `internal/dashboard/static/index.html` — single RB status row
  (line 99), flat `FIELDS`/`LABELS`/`SENSITIVE`/`SECTIONS` arrays
  (151–173), flat status renderer (210–211). The JS is already
  declarative enough that adding a ravenscope section + second status
  row is straightforward.
- `internal/tray/tray.go` — single `mBrain` menu item (76) with a
  `formatBrainMenu` helper (190–201). Adding a second menu item +
  mirroring the helper is in-pattern.
- `cmd/ravenlink/main.go` lines 371–383 (uploader construction), 224–256
  (shared state decls), 680 (status fan-in) — the wiring points that
  multiply from one to two targets.
- `cmd/rbping/main.go` — single-target diagnostic CLI that assumes the
  RavenBrain section. Extended with a `--target` flag.

### Institutional Learnings

- No relevant docs under `docs/solutions/` — the existing plans
  (`2026-04-09-004-refactor-jwt-auth-for-ravenbrain-plan.md` and
  `2026-04-09-002-fix-idempotent-telemetry-upload-plan.md`) capture the
  current auth + server-side idempotency patterns that this refactor
  preserves verbatim.
- CLAUDE.md §5 "Server-side upload progress" — the `uploadedCount` query
  means per-target duplicate-guarding is already free at the protocol
  level. The refactor does not need to introduce client-side dedupe.
- CLAUDE.md §11 "Save-and-restart config flow" — do not add hot-reload
  for the new fields; reuse the restart-on-save mechanism. Any
  `ravenscope_*` fields land on save, RavenLink restarts, and the new
  uploader target comes up with the saved config.

### External References

None required — no new protocol, no new framework, no security-sensitive
auth change. Bearer-token behavior already exists and was added with
external review in the preceding commit.

---

## Key Technical Decisions

- **Two concrete config sections, not a generic `targets` list.** Only
  two destinations exist. A flat `ravenbrain:` and `ravenscope:` pair is
  easier to read, edit, and validate than `targets:` with a name
  discriminator — and keeps YAML churn predictable for users with
  existing configs.
- **Single sentinel for "target is active".** A target runs when
  `section.Enabled == true AND section.URL != ""`. `Enabled == true`
  with an empty URL is treated as a validation error on dashboard save
  (U5) so we never silently no-op. `Enabled == false` with a URL set is
  a first-class "paused" state; `URL == ""` alone still implies
  "unconfigured", matching today's local-only sentinel. This keeps two
  fields without making them redundant.
- **Marker file naming is load-bearing.** Every current scanner of
  `pending/` (`uploader.getPendingFiles`, `dashboard/server.go` session
  listing) filters with `HasSuffix(".jsonl")`. The marker naming
  `<base>.jsonl.<target>.done` (where `<target>` is the literal string
  `"ravenbrain"` or `"ravenscope"`) relies on that suffix filter to
  hide markers from every reader. Any future `pending/` scanner must
  preserve the same filter; U2 tests pin this behavior.
- **`/complete` idempotency is assumed, not proven.** Server-side
  `uploadedCount` makes repeat `/data` POSTs cheap. `POST /session` is
  already designed as an upsert. `POST /complete` is assumed to be
  safe to call twice for the same session id; a marker-write failure
  between server ack and local disk would otherwise re-POST
  `/complete` on the next tick. Before merging U2, verify against
  RavenBrain's `TelemetryController` that a duplicate `/complete` is
  harmless (no-op or idempotent update). If it is not, U2 tightens
  to write the marker *before* sending `/complete` and accepts that
  a crash between marker write and server ack leaves the server
  thinking the session is still open (acceptable — `/complete` gets
  retried).
- **Each target has its own `batch_size` and `upload_interval`.** Shared
  values would force a compromise between two servers that may have
  wildly different throughput profiles (RavenBrain is team-local;
  RavenScope is cloud). Defaults are identical, so nothing changes for
  existing users.
- **Single `pending/` directory with per-target sidecar markers.** After
  a file `<base>.jsonl` is accepted by target T, write an empty
  `<base>.jsonl.<target>.done` sibling. Move `<base>.jsonl` to
  `uploaded/` (and delete its markers) once all *currently enabled*
  targets have a marker. This avoids duplicating data on disk, survives
  restarts, and handles user-toggled enabled flags (a file stuck with
  only a RavenBrain marker moves to uploaded/ the next pass after the
  user disables RavenScope).
- **Legacy-config migration happens in `LoadConfig` only.** If the
  loaded YAML has `ravenbrain.api_key` non-empty *and* no `ravenscope`
  section is present, the loader moves that key into `ravenscope.url`
  (from `ravenbrain.url`) + `ravenscope.api_key`, sets
  `ravenscope.enabled = true`, and clears `ravenbrain.api_key`. Rationale:
  that shape only exists on the current unmerged branch; it is our own
  bearer-auth transition state. We migrate it once and move on.
- **Status exposes a `upload_targets` array rather than flat per-target
  fields.** Dashboard + tray iterate once instead of hand-wiring two sets
  of fields. The JSON shape for the status API is a breaking internal
  change, but the dashboard JS and tray Go are the only consumers and
  both land in this refactor.
- **Auth mode is fixed per target.** RavenBrain = legacy username/password
  via `NewAuth`. RavenScope = bearer api_key via `NewAuthWithKey`. The
  cross-mode `SetAPIKey` path used on the current branch is removed
  because each target now owns exactly one auth shape. `Auth` struct
  itself keeps both code paths for now (they're not expensive and removal
  is a follow-up); the plan just stops exercising the switch.
- **`cmd/rbping` gets a `--target` flag, not a split binary.** Users
  already know `rbping`; renaming or forking it is churn. Default stays
  `ravenbrain` so existing muscle memory works.

---

## Open Questions

### Resolved During Planning

- **How do we avoid re-reading and re-uploading the same file twice per
  tick across two targets?** Keep the single-file-per-tick cadence, but
  iterate targets inside one pass: pick the oldest file that still has at
  least one enabled target without a `.done` marker; try that target.
  Server-side idempotency means repeated attempts are cheap (server
  reports "already have all entries"). This preserves the current
  slow-and-steady upload rhythm and keeps logs readable. See U2.
- **What happens when the user disables a target while it has files
  waiting on its marker?** The completion check only considers currently
  enabled targets. Disabling a target moves any files that were waiting
  on it to `uploaded/` on the next pass, provided every other enabled
  target has its marker. Re-enabling later does not retroactively
  deliver previously uploaded-elsewhere files — that's the semantics
  we want.
- **How does the legacy-config migration interact with `SaveConfig`?**
  After migration runs in `LoadConfig`, the in-memory `*Config` reflects
  the new shape. The next save (triggered by any dashboard Save action
  or by `SaveConfig` called from startup) writes the new shape to disk.
  The `.bak` written on save captures the pre-migration YAML. No
  separate migration flow needed.

### Deferred to Implementation

- Whether to keep the `SetAPIKey` method on `Auth` or remove it after
  migration. Left for a follow-up cleanup once this refactor is stable.
- The exact wording of tray menu items — "RavenBrain: Connected" vs
  "RavenBrain ✓ Connected" etc. Match the current tray idiom when
  writing U6.

*(Note: JSON key names for the `upload_targets` entries are resolved —
`name`, `enabled`, `reachable`, `files_pending`, `files_uploaded`,
`currently_uploading`, `last_result` per the HL design sketch. U4 must
match those exactly.)*

---

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for
> review, not implementation specification. The implementing agent should
> treat it as context, not code to reproduce.*

### Config shape (YAML, post-refactor)

    ravenbrain:
      enabled: true
      url: https://ravenbrain.team1310.local
      username: telemetry-agent
      password: <secret>
      batch_size: 50
      upload_interval: 10

    ravenscope:
      enabled: true
      url: https://ravenscope.example.com
      api_key: <secret>
      batch_size: 50
      upload_interval: 10

### Uploader coordinator (conceptual)

    Uploader
      ├── dataDir, pendingDir, uploadedDir
      ├── activeSessionFn, pausedFn
      └── targets: []*Target
                    ├── name        ("ravenbrain" | "ravenscope")
                    ├── auth        (*Auth)
                    ├── batchSize
                    ├── httpClient  (one per target — independent
                    │                keep-alive pools)
                    ├── backoff     (per-target, independent)
                    └── status      (Reachable, FilesPending,
                                     FilesUploaded, LastResult,
                                     CurrentlyUploading)

    MaybeUpload tick
      1. Skip if paused.
      2. For each target T: ping if nothing pending for T; record
         reachability.
      3. List pending files.
      4. For the oldest file F:
           for each enabled target T without `<F>.<T>.done`:
             if T.backoff active → skip this target this tick
             else upload(F, T); on success write marker.
      5. If every enabled T now has a marker for F: move F to
         uploaded/, delete its markers.

### Per-file completion state (on disk)

    data/pending/
      abcd1234.jsonl
      abcd1234.jsonl.ravenbrain.done   ← (empty file)
      abcd1234.jsonl.ravenscope.done   ← (empty file) [when present]

    data/uploaded/
      abcd1234.jsonl                   ← moved here only after both
                                         .done markers existed

### Status JSON (dashboard contract)

    {
      "nt_connected": true,
      "obs_connected": true,
      "upload_targets": [
        {"name": "ravenbrain", "enabled": true, "reachable": true,
         "files_pending": 0, "files_uploaded": 12,
         "currently_uploading": false, "last_result": "OK: ..."},
        {"name": "ravenscope", "enabled": true, "reachable": false,
         "files_pending": 3, "files_uploaded": 9,
         "currently_uploading": false, "last_result": "HTTP 503"}
      ],
      ...
    }

---

## Implementation Units

- [ ] U1. **Split config into `ravenbrain` + `ravenscope` sections with legacy migration**

**Goal:** Introduce `RavenScopeConfig`, rename/repurpose `RavenBrainConfig`
so each section carries `enabled`, `url`, auth fields, `batch_size`,
`upload_interval`. Migrate legacy configs that set `ravenbrain.api_key`.

**Requirements:** R1, R5, R7.

**Dependencies:** None.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Test: `internal/config/config_test.go`

**Approach:**
- `RavenBrainConfig`: `Enabled bool`, `URL string`, `Username string`,
  `Password string`, `BatchSize int`, `UploadInterval float64`. Drop
  `APIKey` from this struct.
- New `RavenScopeConfig`: `Enabled bool`, `URL string`, `APIKey string`,
  `BatchSize int`, `UploadInterval float64`.
- `Config.RavenScope` added alongside `Config.RavenBrain`.
- `DefaultConfig`: RavenBrain `Enabled: true` (existing behavior),
  RavenScope `Enabled: false` (opt-in). Shared defaults for batch/interval.
- `LoadConfig` migration ordering (explicit):
  1. Read bytes from disk.
  2. Parse into `map[string]any` (raw YAML pre-pass).
  3. If `ravenbrain.api_key` is set AND no `ravenscope` key exists at
     top level, synthesize a `ravenscope` map with `enabled: true`,
     `url: <ravenbrain.url>`, `api_key: <ravenbrain.api_key>`, and
     delete `ravenbrain.api_key` from the map. Emit an INFO log
     `"config: migrated ravenbrain.api_key -> ravenscope.api_key"`.
  4. Re-marshal the map to YAML bytes.
  5. Typed `yaml.Unmarshal` into `*Config`. Because the refactored
     struct has no `APIKey` field on `RavenBrainConfig`, any stray
     `api_key` key in a hand-edited `ravenbrain:` section is silently
     dropped post-migration — acceptable.
  6. Existing limelight-style zero-value backfill runs after this.
- Intentional consequence of the map round-trip: user-authored comments
  and key ordering in `config.yaml` are lost on first migrated save.
  Call this out in the INFO log so operators aren't surprised.
- `ParseFlags`: add `--ravenscope-url`, `--ravenscope-api-key`,
  `--ravenscope-enabled`, `--ravenscope-batch-size`,
  `--ravenscope-upload-interval`. Rename the existing
  `--ravenbrain-api-key` flag path: dropping it is a breaking change
  for command-line users; add `--ravenscope-api-key` and keep
  `--ravenbrain-api-key` as a deprecated alias that writes into
  `cfg.RavenScope.APIKey` with a warning. (Alias removal is a follow-up.)

**Patterns to follow:**
- Limelight section's zero-value-as-"section absent" backfill
  (`config.go:151–154`) — use the same pattern for ravenscope when a
  pre-migration config has no ravenscope section at all.
- Existing CLI flag blocks in `ParseFlags` (ravenbrain-* group,
  `config.go:273–278`).

**Test scenarios:**
- Happy path: fresh `DefaultConfig()` round-trips through
  `SaveConfig`/`LoadConfig` with both sections intact, RavenBrain
  enabled, RavenScope disabled.
- Happy path: loading a YAML with explicit `ravenscope.enabled: true`
  and `ravenscope.api_key: secret` populates
  `cfg.RavenScope.APIKey == "secret"` and leaves RavenBrain
  credentials untouched.
- Edge case: loading a pre-refactor YAML with only `ravenbrain:` (no
  api_key) leaves `cfg.RavenScope` at its disabled default and does not
  invent a URL.
- Edge case: loading a current-branch YAML that has
  `ravenbrain.api_key: xyz` and `ravenbrain.url: https://…` but no
  `ravenscope:` block migrates to
  `cfg.RavenScope = {Enabled: true, URL: "https://…", APIKey: "xyz"}`
  and leaves `cfg.RavenBrain.Username/Password` in place.
- Edge case: loading a YAML with both `ravenbrain.api_key` *and* an
  explicit `ravenscope:` block treats the explicit block as
  authoritative — no migration overwrite.
- Error path: invalid YAML returns an error from `LoadConfig` (regression
  guard).
- Integration: deprecated `--ravenbrain-api-key` flag writes into
  `cfg.RavenScope.APIKey` and emits a warning log (capture via
  `slog` test handler or observable side effect).

**Verification:**
- `go test ./internal/config/ -v` passes including all new scenarios.
- Hand-check: `cat` a freshly saved `config.yaml` from a dev run — it
  contains two cleanly separated sections in a predictable order.

---

- [ ] U2. **Multi-target uploader with per-file completion markers**

**Goal:** Refactor `internal/uploader` so the `Uploader` coordinator
holds a slice of `*Target`, each with its own auth, batch config,
backoff, and status. A pending file is only moved to `uploaded/` once
all currently enabled targets have accepted it (tracked via sidecar
`.done` markers).

**Requirements:** R2, R3, R4.

**Dependencies:** U1.

**Files:**
- Modify: `internal/uploader/uploader.go`
- Create: `internal/uploader/target.go`
- Modify: `internal/uploader/auth.go` (minor — add target name field for
  log context only; no auth logic changes)
- Modify: `internal/uploader/auth_test.go` (only if the name-field
  addition breaks existing assertions)
- Create: `internal/uploader/uploader_test.go`
- Test: `internal/uploader/target_test.go` and
  `internal/uploader/uploader_test.go`

**Approach:**
- New `Target` struct in `target.go`: encapsulates name (const string,
  either `"ravenbrain"` or `"ravenscope"`), auth, batch size, http
  client, backoff state, and a small `TargetStatus` struct with
  `Reachable`, `FilesPending`, `FilesUploaded`, `LastUploadResult`,
  `CurrentlyUploading`.
- Move `uploadFile`, `getUploadedCount`, `postJSON`, `getJSON`, `ping`,
  `applyBackoff` into methods on `*Target`. The per-target method
  receives the file path and returns `(ok bool, err error)` exactly
  like today's `uploadFile`.
- **`Target.status` concurrency discipline.** Each `*Target` embeds a
  `sync.RWMutex` that guards its `TargetStatus` fields. All writes
  (inside the per-target goroutine's upload path) take the write lock;
  all reads (from the status fan-in in `main.go` and from `DrainPending`)
  take the read lock. Expose a `Target.Snapshot() TargetStatus` method
  so consumers get a copy rather than holding a reference to live
  fields. `go test -race` in U2 covers this.
- `Uploader.New` signature becomes `New(dataDir, targets []*Target,
  activeSessionFn)`; drop `auth`, `batchSize`, `uploadInterval` from
  the coordinator (they move to Target, except interval which is the
  tick cadence). For interval we either pick the *minimum* of enabled
  targets' intervals (simpler and matches "send ASAP") or dispatch each
  target on its own ticker (more faithful). Pick per-target tickers —
  see technical design below.
- `MaybeUpload` tick body:
  1. Early-out on pause.
  2. For each enabled target without pending work, send a ping; update
     reachability. For each enabled target with pending work, skip the
     ping (the upload result gives fresher reachability).
  3. List pending files sorted oldest-first, excluding active session.
  4. Pick the oldest file F. For each enabled target T that has no
     `<F>.<T>.done` marker AND whose backoff window has passed: attempt
     `target.Upload(F)`. On success, write
     `<F>.<T>.done`. Update target status. On failure, set backoff on
     that target only.
  5. After the file's target attempts, if every currently enabled
     target has a marker for F (or the target list is empty), move F
     to `uploaded/` and delete its markers.
- `DrainPending` mirrors the same logic but without backoff gating and
  without the per-tick rhythm.
- Per-target tickers: the coordinator starts one goroutine per enabled
  target, each with its own interval. Each goroutine calls into a
  shared `maybeUploadForTarget(t)` method that handles file selection
  + upload for *that target only*, writes its marker on success, and
  issues a single "are all markers present now?" check. The move step
  is guarded by a mutex to avoid a race where two targets both
  conclude they were the last to mark the same file.
- Marker file creation: `os.WriteFile(path, nil, 0o600)` is sufficient
  — the content never matters, only the existence. Marker deletion
  happens inside the mutex-held move step.

**Execution note:** Start with a test that exercises the two-target
upload path against a pair of `httptest.Server` instances, then grow
outward. Marker-file semantics and the "move on last marker" race are
the most subtle behaviors here.

**Technical design:**

    Uploader.Run(ctx)
      for each target t in u.targets where t.enabled:
        go u.runTargetLoop(ctx, t)

    runTargetLoop(ctx, t):
      ticker := NewTicker(t.uploadInterval)
      for {
        <-ticker.C
        u.maybeUploadForTarget(t, activeID)
      }

    maybeUploadForTarget(t, activeID):
      if paused, return
      files := listPending(activeID)           // shared; cheap ReadDir
      t.FilesPending = countFilesStillOwing(files, t)
      if no files owe t:
        t.Reachable = t.ping()
        return
      if t.backoffUntil > now: return
      F := oldest file still owing t
      ok, err := t.uploadFile(F)
      if ok: writeMarker(F, t)
      ...
      u.maybeFinalize(F)     // mutex-guarded

    maybeFinalize(F):
      lock u.finalizeMu
      enabled := currentlyEnabledTargetNames()
      if every name in enabled has <F>.<name>.done:
        move F to uploaded/
        delete its markers

**Patterns to follow:**
- Current `uploader.go` structure for HTTP flow, backoff, pause gate,
  draining, pending-dir scanning — all preserved on the per-target
  methods.
- `internal/limelight/Monitor` for the per-target goroutine-per-source
  pattern (one ticker, one poll loop).

**Test scenarios:**
- Happy path, two targets: both reachable httptest servers; one pending
  file; both receive full POST `/session` + data + `/complete`;
  corresponding `.done` markers appear; file ends up in `uploaded/`.
- Happy path, one target: RavenScope enabled, RavenBrain disabled;
  `.ravenscope.done` marker appears; file moves to `uploaded/` with no
  RavenBrain marker.
- Happy path, zero targets: file stays in `pending/` indefinitely; no
  HTTP calls made; no panic. (This matches today's behavior when
  `ravenbrain.url == ""`.)
- Edge case: one target 503s, the other 2xxs. Healthy target gets its
  marker; failing target enters backoff; file stays in `pending/` with
  only the healthy marker. On next tick (after backoff), the failing
  target retries.
- Edge case: user disables RavenScope between passes. File has only
  `.ravenbrain.done`. Next tick moves the file to `uploaded/` because
  the enabled set now has all its markers. The stray
  `.ravenscope.done` (if one existed) is cleaned up in the move.
- Edge case: a second tick while a target is still mid-upload does not
  double-upload (covered by the per-target mutex / `CurrentlyUploading`
  guard).
- Edge case: marker file exists but server's `uploadedCount` reports
  zero — target re-checks server state next tick; server-side
  idempotency (not the marker) is the source of truth for *what the
  server has*. Marker is the source of truth for *what we've already
  finalized client-side*. Document the asymmetry.
- Error path: marker-file write fails (disk full, perms) — treat as
  upload failure so the file is retried next tick; do not move to
  `uploaded/`.
- Integration: drain-pending at shutdown with both targets enabled and
  both reachable flushes every file; with one target unreachable,
  drain finishes what it can and leaves partially-marked files in
  `pending/` for next startup.
- Integration: process restart after a partial upload
  (`.ravenbrain.done` exists, `.ravenscope.done` doesn't) — the
  uploader skips the RavenBrain leg and only attempts RavenScope; file
  moves to `uploaded/` once RavenScope succeeds.

**Verification:**
- `go test ./internal/uploader/ -v` passes every scenario.
- Manual: with RavenBrain pointed at a local mock and RavenScope
  pointed at an intentionally-broken URL, run RavenLink through a
  synthesized match and confirm `.ravenbrain.done` appears, the file
  stays in `pending/`, and retry logs reference only the RavenScope
  target.

---

- [ ] U3. **Wire two targets in `cmd/ravenlink/main.go`**

**Goal:** Replace the single-auth single-uploader construction at
`main.go:371–383` with a construction that builds 0, 1, or 2 targets
based on each section's `Enabled` flag and URL presence, then passes
the slice to `uploader.New`. Update drain-pending wiring to match.

**Requirements:** R2, R3.

**Dependencies:** U1, U2.

**Files:**
- Modify: `cmd/ravenlink/main.go`

**Approach:**
- Replace `if cfg.RavenBrain.URL != ""` guard with per-section checks.
  A target is built when `section.Enabled == true && section.URL != ""`.
- Move the `INSECURE ravenbrain_url` warning log into a helper that
  emits per-target warnings (one message each for RavenBrain and
  RavenScope when their URL is http:// not https://).
- RavenBrain target: `uploader.NewAuth(cfg.RavenBrain.URL, ...)`. No
  `SetAPIKey` call anymore.
- RavenScope target: `uploader.NewAuthWithKey(cfg.RavenScope.URL,
  cfg.RavenScope.APIKey)`.
- Pass the resulting slice (possibly empty) to `uploader.New`. If the
  slice is empty, skip `up.Run` — no uploader goroutine spins up,
  preserving today's local-only behavior.
- Status fan-in at `main.go:680` loops over `up.TargetStatuses()` and
  writes into the `UploadTargets` slice on `Status` instead of the
  old flat fields.
- Drain-pending path (`main.go:504–510`) keeps its shape; it calls
  `up.DrainPending` which now iterates targets internally.

**Patterns to follow:**
- The existing Limelight optional-subsystem wiring — conditional
  goroutine start based on a single config flag.

**Test scenarios:**
Test expectation: none — this unit is pure wiring with no branching
logic beyond what's tested in U1 (config) and U2 (uploader). The
first-run manual verification below is the acceptance check.

**Verification:**
- `go build ./...` succeeds.
- Manual: with `ravenbrain.enabled: true, ravenscope.enabled: false`,
  RavenLink behaves exactly as the current `main` branch does (single
  upload destination, single status indicator).
- Manual: with both enabled and pointed at local mock servers, a
  recorded session's JSONL ends up at both servers and in
  `data/uploaded/`.
- Manual: with both disabled, RavenLink runs to completion in
  local-only mode; `data/pending/` keeps its JSONL files; no upload
  goroutines appear in a goroutine dump.

---

- [ ] U4. **Replace flat upload status with per-target slice**

**Goal:** Change `status.Status` so upload state is represented as an
`UploadTargets []UploadTargetStatus` slice instead of flat
`RavenBrainReachable`/`FilesPending`/etc. fields. Update the
dashboard's status JSON contract at the same time.

**Requirements:** R6.

**Dependencies:** U2 (so the uploader exposes per-target status).

**Files:**
- Modify: `internal/status/status.go`
- Modify: `internal/status/status_test.go` *(create if absent — the
  package currently has no test file; add one alongside the struct
  change)*
- Modify: `cmd/ravenlink/main.go` (consumer — fan-in at line 680)

**Approach:**
- Add `UploadTargetStatus` struct: `Name string`, `Enabled bool`,
  `Reachable bool`, `FilesPending int`, `FilesUploaded int`,
  `CurrentlyUploading bool`, `LastUploadResult string`. JSON tags
  driven by the dashboard contract in the High-Level Technical Design
  sketch.
- Remove `RavenBrainReachable`, `FilesPending`, `FilesUploaded`,
  `LastUploadResult`, `CurrentlyUploading` from `Status`.
- Add `UploadTargets []UploadTargetStatus` to `Status`. Document in a
  struct-field comment that an empty slice = local-only mode.
- `ToJSON` behavior does not change structurally; it just serializes
  the new shape.

**Patterns to follow:**
- Existing `Update`/`Snapshot` locking discipline.

**Test scenarios:**
- Happy path: a status struct with two populated targets marshals to
  JSON with a two-element `upload_targets` array whose fields match
  the populated values.
- Edge case: an empty `UploadTargets` slice marshals to `[]`, not
  `null` (force-initialize the slice in `New()` to preserve the
  shape the dashboard JS expects).
- Integration: `Update` → `ToJSON` round-trip under concurrent
  readers doesn't race (`go test -race`).

**Verification:**
- `go test ./internal/status/ -race -v` passes.
- `go build ./...` succeeds with the consumer update in main.go.

---

- [ ] U5. **Dashboard: two config sections + two status rows**

**Goal:** Surface RavenScope in the dashboard UI. The config form gains
a sibling section with its own fields; the Status tab renders one row
per upload target; save-and-restart still works.

**Requirements:** R1, R6.

**Dependencies:** U1, U4.

**Files:**
- Modify: `internal/dashboard/server.go`
- Modify: `internal/dashboard/static/index.html`

**Approach:**
- `server.go`: extend `configFields` with `ravenbrain_enabled`,
  `ravenscope_enabled`, `ravenscope_url`, `ravenscope_api_key`,
  `ravenscope_batch_size`, `ravenscope_upload_interval`. Add
  `ravenscope_api_key` to the sensitive-values mask. Extend the
  GET/POST code paths (lines 329, 349–353, 426–437) to read/write these
  fields on `cfg.RavenScope`. Mark `ravenbrain_password` + password
  and `ravenscope_api_key` as write-only (returned masked on GET, only
  overwritten on POST when non-empty, matching today's password
  handling).
- `index.html`: add `ravenbrain_enabled` and ravenscope_* keys to the
  flat `FIELDS`/`LABELS`/`SENSITIVE` arrays. Update the `SECTIONS`
  object to give RavenScope its own section with the new keys.
- **Save validation rule (server-side in `dashboard/server.go`):** if
  `section.enabled` is `true` but `section.url` is empty for either
  target, `POST /api/config` rejects the save with a 400 and an
  error message pointing at the offending field. This enforces the
  single-sentinel decision from Key Technical Decisions.
- Status tab: replace the single `#s-rb` RavenBrain indicator with a
  rendering loop driven by `status.upload_targets`. Each target gets
  its own row (`RavenBrain`, `RavenScope`) with the same connected /
  disconnected / disabled tri-state as today's indicators.
- First-run banner copy updated to mention both destinations generically
  ("telemetry server credentials") OR to step through them separately —
  pick whichever reads clearly. Acceptance: user can identify which
  fields are required without reading source.

**Patterns to follow:**
- Existing `connClass` / `connText` rendering (`index.html:210–211`)
  — apply to each target in the loop.
- Existing password-masking behavior for `obs_password` and
  `ravenbrain_password` — mirror for `ravenscope_api_key`.

**Test scenarios:**
- Happy path: load the dashboard with RavenBrain enabled and RavenScope
  disabled. Config tab shows both sections; Status tab shows one
  "Connected" row and one "Disabled" row.
- Happy path: edit `ravenscope_url` and `ravenscope_api_key`, toggle
  `ravenscope_enabled` on, Save → `config.yaml` contains the new
  values and RavenLink restarts.
- Edge case: saving with `ravenscope_api_key` field empty preserves
  the previously stored value (password-mask semantics).
- Edge case: both targets disabled renders two "Disabled" rows and
  first-run banner remains appropriate.
- Error path: saving with malformed `ravenscope_batch_size` (non-numeric)
  produces the same validation error as the existing
  `ravenbrain_batch_size` path.
- Error path: saving with `ravenscope_enabled: true` and empty
  `ravenscope_url` returns HTTP 400 with a message identifying the
  empty URL; `config.yaml` is not modified. Same for the
  RavenBrain section.
- Integration: after save-and-restart, the new uploader goroutine for
  the freshly-enabled target appears (verified by Status tab pending
  counter decrementing when test data is present).

**Verification:**
- `go build ./...` + open `http://localhost:8080`; exercise the
  scenarios manually.
- `go test ./internal/dashboard/ -v` passes (existing tests; add
  coverage if any config round-trip assertion exists).

---

- [ ] U6. **Tray: two menu items driven by the upload_targets array**

**Goal:** The menu bar icon shows one status line per enabled target
instead of a single "RavenBrain: …" line.

**Requirements:** R6.

**Dependencies:** U4.

**Files:**
- Modify: `internal/tray/tray.go`

**Approach:**
- Replace the single `mBrain` MenuItem with a pre-built pair: `mRavenBrain`
  and `mRavenScope`, each added during tray setup. Items for a
  disabled target are hidden via `Hide()` on startup and shown/hidden
  based on the current status snapshot.
- Extract `formatTargetMenu(name string, ts UploadTargetStatus) string`
  from the current `formatBrainMenu` helper. Call it once per target.
- If neither target is enabled, both items are hidden — the tray still
  works, just without upload-state lines.

**Patterns to follow:**
- `formatBrainMenu` at `tray.go:190–201` — same state-transition logic,
  duplicated once per target. A generic helper keeps this DRY without
  introducing an abstraction.
- Existing `Hide()`/`Show()` discipline on other tray items (NT, OBS).

**Test scenarios:**
Test expectation: none — tray code has no existing unit tests and is
platform-gated Cocoa code. Manual verification on macOS and Windows is
the acceptance check.

**Verification:**
- Manual (macOS): with both enabled, the menu shows two lines —
  "RavenBrain: Connected" and "RavenScope: 3 pending" (or whichever
  state applies). With only RavenBrain enabled, only that line appears.
- Manual (Windows): same expectations against a Windows build.

---

- [ ] U7. **`cmd/rbping`: add `--target` flag for either destination**

**Goal:** Keep the diagnostic CLI useful now that there are two
possible destinations. Default target remains `ravenbrain`.

**Requirements:** R8.

**Dependencies:** U1.

**Files:**
- Modify: `cmd/rbping/main.go`

**Approach:**
- Add a `--target` string flag, default `"ravenbrain"`. Accepted values:
  `ravenbrain`, `ravenscope`.
- For `ravenbrain`: existing behavior — POST `/login`, validate bearer
  token, call `/api/validate`.
- For `ravenscope`: skip the `/login` step (bearer auth has no login);
  use `uploader.NewAuthWithKey`; call `/api/ping` + `/api/validate` with
  the API-key bearer.
- Rename banner text conditionally: `RavenBrain Connectivity Test` vs
  `RavenScope Connectivity Test`.

**Patterns to follow:**
- The existing rbping three-step flow (`/api/ping` → `/login` →
  `/api/validate`). Steps 1 and 3 apply to both targets; step 2 is
  RavenBrain-only.

**Test scenarios:**
Test expectation: none — `cmd/rbping` is a human-run diagnostic binary
with no existing unit tests. Acceptance is the manual verification
below.

**Verification:**
- Manual: `rbping --target ravenbrain` against a live RavenBrain works
  identically to today.
- Manual: `rbping --target ravenscope` against a live RavenScope (or
  a local mock) prints OK for `/api/ping` + `/api/validate` and does
  not attempt `/login`.
- Manual: `rbping --target bogus` prints a usage error.

---

## System-Wide Impact

- **Interaction graph:** The uploader goroutine becomes N goroutines (one
  per enabled target). Status fan-in at `main.go:680` now reads a slice
  instead of a flat struct. Dashboard JSON consumers (the embedded JS)
  must handle the new `upload_targets` shape — no other consumers exist.
- **Error propagation:** Target failures are per-target (independent
  backoff timers, independent auth invalidation). One target's 503 does
  not interrupt the other's uploads. Drain-pending still returns on the
  first *per-target* error for that target but continues for the others.
- **State lifecycle risks:** The "last marker written wins" race in
  `maybeFinalize` is guarded by a mutex inside the coordinator; without
  that mutex, two goroutines could both move the same file and one
  would error. Marker writes fsync is not strictly required — a lost
  marker just causes one extra idempotent re-upload next tick, which is
  cheap and correct.
- **API surface parity:** The dashboard `GET /api/status` JSON shape
  changes; the dashboard's own JS is the only consumer and ships in the
  same binary. No external API consumers.
- **Integration coverage:** Unit tests with mocked `httptest.Server`
  pairs cover the two-target case; per-target tickers are shared with
  the drain path so a single integration test per scenario is
  sufficient. Covered in U2.
- **Unchanged invariants:** The session JSONL format, the server-side
  `POST /session → /data → /complete` protocol, the match-marker
  timing, the Limelight rider channel, the OBS trigger semantics, and
  the tray-on-main-goroutine macOS rule are all untouched.

---

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Marker files accumulate in `pending/` if the move step is interrupted mid-cleanup (e.g., process crash between marker delete and main-file rename) | The marker deletes happen *after* the main-file rename. Orphaned markers in `pending/` without their main file are harmless; add a best-effort sweep on uploader startup that deletes `.done` markers whose base JSONL is absent. |
| Users upgrade from the current branch and expect `ravenbrain.api_key` to keep working silently | `LoadConfig` migrates the field (U1); the migration is logged at INFO so operators see what happened. The deprecated `--ravenbrain-api-key` flag still routes to the RavenScope section during the transition. |
| Per-target goroutines leak on shutdown | Both goroutines close on `ctx.Done()`; `DrainPending` is called before `cancel()`, matching today's shape. Tested by `-race` + goroutine-dump in U2. |
| Two simultaneous `moveToUploaded` attempts race on the same file | Guarded by the coordinator's `finalizeMu`. U2 test scenario "second tick while target is mid-upload" covers this. |
| Dashboard status JSON shape change breaks a downstream consumer we forgot about | Only the embedded `index.html` reads `/api/status`; `grep -r upload_targets\|ravenbrain_reachable` at the end of U4 confirms no other consumer. |
| Files stuck in `pending/` forever when a target is left `enabled: true` but permanently unreachable | Today's `PruneUploaded` only touches `uploaded/`. The marker scheme inherits that asymmetry: an enabled-but-broken target will pin files in `pending/`. This is *intentional* — losing match data is worse than a bloated pending dir. Operators see the backlog in the dashboard's per-target `files_pending` counter and can disable the stuck target to drain. Call this out in the CLAUDE.md update (see Documentation / Operational Notes). |
| Startup finds orphan `.done` markers whose base `.jsonl` is absent (e.g., crash between main-file rename and marker delete) | Uploader `New` runs a best-effort sweep: scan `pending/` for `*.done` files, delete any whose corresponding `.jsonl` is missing. Log at INFO. |

---

## Documentation / Operational Notes

- Update `CLAUDE.md` "Config Sections" table to list the new `ravenscope`
  section alongside `ravenbrain`. Mark both as restart-required for the
  `url` and `enabled` fields; mark `batch_size`/`upload_interval` as
  hot-reloadable with the same restart-vs-live split as today's
  RavenBrain entries.
- Update `CLAUDE.md` gotcha §11 ("Save-and-restart config flow") is
  unchanged — save still triggers restart.
- Add a short section to CLAUDE.md (under "Key Design Decisions") for the
  per-file marker scheme so future readers don't re-derive it.
- Operationally: deploys that currently use `ravenbrain.api_key` keep
  working through the migration; deploys that want both destinations
  set both sections explicitly.

---

## Sources & References

- Related code:
  - `internal/config/config.go`
  - `internal/uploader/uploader.go`, `internal/uploader/auth.go`
  - `internal/status/status.go`
  - `internal/dashboard/server.go`,
    `internal/dashboard/static/index.html`
  - `internal/tray/tray.go`
  - `cmd/ravenlink/main.go`, `cmd/rbping/main.go`
- Prior plans:
  - `docs/plans/2026-04-09-002-fix-idempotent-telemetry-upload-plan.md`
    (server-side `uploadedCount` contract this refactor preserves)
  - `docs/plans/2026-04-09-004-refactor-jwt-auth-for-ravenbrain-plan.md`
    (auth shape this refactor splits by target)
- Related commits on current branch:
  - `a34d5f4` (feat(uploader): add bearer-token auth mode for RavenScope
    compatibility) — the work this plan restructures
