---
title: "feat: Session file listing and WPILog export in dashboard"
type: feat
status: active
date: 2026-04-14
---

# feat: Session file listing and WPILog export in dashboard

## Overview

Add a "Sessions" tab to the RavenLink web dashboard that lists all recorded JSONL session files (pending + uploaded) with metadata, and offers per-session export to the `.wpilog` binary format compatible with AdvantageScope. This gives drive teams instant post-match data analysis without requiring a RavenBrain server connection.

## Problem Statement / Motivation

Currently the dashboard shows only aggregate counts (`files_pending`, `files_uploaded`) — there's no way to see individual sessions, inspect their metadata, or retrieve the data locally. Teams at competitions often need to review robot data immediately after a match using AdvantageScope, but the JSONL files aren't directly compatible. The only path today is through the RavenBrain server, which may be unavailable at competition venues with limited internet.

WPILog (`.wpilog`) is the standard binary format for FRC robot data. AdvantageScope — the most popular FRC data analysis tool — natively reads it. A local export feature eliminates the server dependency and puts data in the format students already know.

## Proposed Solution

### New Dashboard Tab: Sessions

Add a fourth tab ("Sessions") to the dashboard. When activated, it fetches `GET /api/sessions` and renders a table sorted newest-first:

| Column | Source |
|--------|--------|
| Date/Time | Parsed from filename timestamp prefix |
| Session ID | 8-hex suffix from filename |
| Entries | Parsed from `session_end` line's `entries_written`, or file line count |
| Size | `os.Stat` file size |
| Status | "Pending" / "Uploaded" / "Recording..." (active) |
| Actions | "Export WPILog" button (disabled for active session) |

The active session (matching `ntLog.Stats().ActiveSessionID`) is shown with a "Recording..." badge and no export button — exporting a partially-written file with unflushed buffers is unsafe.

**Refresh mechanism:** The existing SSE status stream already pushes `files_pending` and `files_uploaded` counts every second. The frontend JS watches for changes in these counts and re-fetches `/api/sessions` when they change. No new SSE fields needed.

### New API Endpoints

**`GET /api/sessions`** — Returns JSON array of session metadata:

```json
[
  {
    "id": "0284a1cf",
    "filename": "20260411T151030Z_0284a1cf.jsonl",
    "date": "2026-04-11T15:10:30Z",
    "entries": 3259,
    "size_bytes": 603000,
    "status": "uploaded",
    "active": false
  }
]
```

Implementation: scan both `pending/` and `uploaded/` directories for `.jsonl` files, parse filename for date+ID, read first and last lines for `session_start`/`session_end` metadata, `os.Stat` for size. Exclude the active session ID from export eligibility.

**`GET /api/sessions/{id}/wpilog`** — Converts the JSONL session file to WPILog binary format and returns it as a download:
- `Content-Type: application/octet-stream`
- `Content-Disposition: attachment; filename="20260411T151030Z_0284a1cf.wpilog"`
- Returns 404 if session not found, 409 if session is currently active

### New Package: `internal/wpilog/`

A standalone WPILog encoder package with no dependencies on the rest of RavenLink. This keeps the binary format logic testable in isolation.

## Technical Considerations

### WPILog Binary Format (v1.0)

The format is well-specified by WPILib. Key details for the encoder:

**File header** (12+ bytes): Magic `WPILOG` (6 bytes) + version `0x0100` (2 bytes LE) + extra header length (4 bytes LE) + extra header UTF-8 string. Extra header will contain `{"team":1310,"source":"RavenLink","session":"<id>"}`.

**Record structure**: 1-byte bitfield encoding variable lengths for entry ID (1-4 bytes), payload size (1-4 bytes), and timestamp (1-8 bytes), followed by payload data. All integers little-endian. Bitfield: `(tsLen-1)<<4 | (sizeLen-1)<<2 | (entryLen-1)`.

**Control records** (entry ID = 0):
- Start (type 0x00): declares a data channel — entry ID, name string, type string, metadata string
- Finish (type 0x01): marks channel inactive
- Set Metadata (type 0x02): updates channel metadata

**Conversion strategy** (two-pass):
1. **First pass**: Read all JSONL lines, collect unique `(key, type)` pairs, assign WPILog entry IDs starting from 1. Compute base timestamp (minimum `server_ts`) for relative offset.
2. **Second pass**: Write file header, Start control records for each entry, then data records in order.

In-memory conversion is acceptable — the largest observed session is 2.4MB JSONL which produces a smaller binary output. Can be revisited for streaming if sessions grow significantly larger.

### NT4 to WPILog Type Mapping

| NT4 Type | WPILog Type | Value Encoding | Notes |
|----------|-------------|----------------|-------|
| `boolean` | `boolean` | 1 byte (0x00/0x01) | |
| `double` | `double` | 8 bytes IEEE-754 LE | |
| `int` | `int64` | 8 bytes signed LE | **Name change**; JSON deserializes as float64, must cast to int64 |
| `float` | `double` | 8 bytes IEEE-754 LE | **Promoted** — WPILog has no float32 equivalent that AdvantageScope reliably handles |
| `string` | `string` | Raw UTF-8 (no length prefix) | |
| `raw` | `raw` | Raw bytes | JSONL stores base64 — must decode |
| `boolean[]` | `boolean[]` | 1 byte per element, packed | |
| `double[]` | `double[]` | 8 bytes per element, packed | |
| `int[]` | `int64[]` | 8 bytes per element, packed | **Name change**; elements from JSON as float64→int64 |
| `float[]` | `double[]` | 8 bytes per element, packed | **Promoted** |
| `string[]` | `string[]` | u32 count + (u32 len + UTF-8) per element | Only array type with length prefix |

### Match Markers as Synthetic WPILog Entries

JSONL `match_start` and `match_end` lines contain valuable timing data. Rather than dropping them, synthesize a WPILog string entry at path `/RavenLink/MatchEvent` with values like `"match_start"`, `"match_end"`. This makes match phase boundaries visible on AdvantageScope's timeline — critical for post-match analysis.

Timestamp source for markers: convert the `ts` (wall-clock Unix) field to a monotonic microsecond offset relative to the session's base timestamp (same epoch as data entries).

### Timestamp Handling

- **Primary source**: `server_ts` (NT4 microseconds) — this is the roboRIO monotonic clock and what AdvantageScope plots on the X axis.
- **Base timestamp**: Subtract the minimum `server_ts` across all entries to produce relative timestamps starting near zero. This keeps the variable-length timestamp encoding compact.
- **Fallback for server_ts == 0**: Some NT4 implementations send 0 for the first update after reconnection. When `server_ts` is 0, synthesize a monotonic offset from `ts` (wall-clock): `(ts - session_start_ts) * 1_000_000`.

### Data Directory Resolution

The dashboard `Server` must use the data directory that the running process is actually writing to — not the live config value, which can change after a config save (before the restart that applies it). Capture `dataDir string` at `dashboard.New()` construction time.

### Session Metadata Extraction

For the session list, reading every JSONL file end-to-end on each request is too expensive. Instead:
- Parse the filename for date and session ID (already deterministic format)
- `os.Stat` for file size
- Read only the first line (`session_start`) for team/robot_ip
- Read only the last line (`session_end`) for `entries_written`
- For files missing a `session_end` (interrupted sessions), show entry count as "unknown" and mark as incomplete

### Architecture Impact

- No new goroutines — handlers run synchronously in HTTP request context
- No new persistent state — session data is derived from the filesystem on each request
- The `wpilog` package is pure and side-effect-free (takes `[]byte` JSONL in, returns `[]byte` WPILog out)
- Dashboard server gets one new constructor parameter (`dataDir string`)
- No changes to the main loop, state machine, NT client, or uploader

## Acceptance Criteria

### Functional

- [ ] Dashboard shows a "Sessions" tab listing all session files from both `pending/` and `uploaded/` directories
- [ ] Each session row shows: date, session ID, entry count, file size, upload status
- [ ] Active session shown as "Recording..." with export button disabled
- [ ] "Export WPILog" button triggers browser download of `.wpilog` file
- [ ] Exported `.wpilog` opens correctly in AdvantageScope with all NT topics visible
- [ ] All NT4 data types (boolean, double, int, float, string, raw, and all array types) convert correctly
- [ ] Match markers appear in AdvantageScope as synthetic `/RavenLink/MatchEvent` entries
- [ ] Session list auto-refreshes when `files_pending` or `files_uploaded` counts change in SSE
- [ ] Empty sessions (no data entries) export as valid WPILog with only header + control records
- [ ] First-run mode (team==0): Sessions tab shows empty state, no errors

### Non-Functional

- [ ] WPILog conversion of a 2.4MB JSONL file completes in < 1 second
- [ ] Session listing response returns in < 500ms for 100+ session files
- [ ] No new CGo dependencies — pure Go implementation
- [ ] Cross-compile for Windows continues to work (`CGO_ENABLED=0 GOOS=windows`)

## Implementation Plan

### Phase 1: WPILog Encoder Package

Create `internal/wpilog/` with:

**`internal/wpilog/encoder.go`** — Core encoder:
- `WriteHeader(w io.Writer, extraHeader string) error` — 12+ byte file header
- `WriteStartRecord(w, entryID uint32, name, typeName, metadata string, timestamp uint64) error`
- `WriteDataRecord(w, entryID uint32, timestamp uint64, payload []byte) error`
- Helper: `minBytes(v uint64, max int) int` — minimum byte count for variable-length encoding
- Helper: `writeVarInt(w, value uint64, numBytes int) error`

**`internal/wpilog/types.go`** — Type mapping and value encoding:
- `MapNT4Type(nt4Type string) string` — e.g., "int" -> "int64"
- `EncodeValue(nt4Type string, jsonValue any) ([]byte, error)` — JSON value to WPILog binary payload

**`internal/wpilog/convert.go`** — High-level JSONL-to-WPILog conversion:
- `Convert(jsonlData []byte) ([]byte, error)` — two-pass conversion
- Handles entry ID assignment, timestamp normalization, marker synthesis

**`internal/wpilog/encoder_test.go`** — Table-driven tests:
- Test each NT4 type round-trip (JSON value → binary encoding → known bytes)
- Test `int` via JSON (float64 → int64 cast)
- Test `raw` base64 decoding
- Test `float` promotion to `double`
- Test `string[]` count-prefix encoding
- Test `server_ts == 0` fallback
- Test match marker synthesis
- Test empty session
- Test file header + a single record against known-good bytes
- Validate output with AdvantageScope (manual verification)

### Phase 2: Dashboard API Endpoints

**`internal/dashboard/server.go`** additions:
- Add `dataDir string` field to `Server` struct, set in constructor
- `GET /api/sessions` handler: scan `dataDir/pending/` and `dataDir/uploaded/`, build metadata list
- `GET /api/sessions/{id}/wpilog` handler: find session file by ID, read JSONL, call `wpilog.Convert()`, serve as download
- Helper: `findSessionFile(dataDir, sessionID string) (path string, status string, err error)` — searches both dirs

**`cmd/ravenlink/main.go`** change:
- Pass `cfg.Telemetry.DataDir` to `dashboard.New()` (capture at construction, not from live config)

### Phase 3: Dashboard Frontend

**`internal/dashboard/static/index.html`** additions:
- Add "Sessions" tab button in `.tabs` bar
- Add `#tab-sessions` content area with session table
- JS: `loadSessions()` function fetching `GET /api/sessions`, rendering table rows
- JS: "Export WPILog" button triggers `window.location = '/api/sessions/{id}/wpilog'`
- JS: SSE handler watches for `files_pending`/`files_uploaded` changes and calls `loadSessions()` when the Sessions tab is active
- CSS: Reuse existing `.card`, `.stat`, `.btn` styles for consistent dark theme

### Build Sequence

1. `internal/wpilog/` — encoder + tests (no dependencies on rest of codebase)
2. Dashboard API endpoints — wire data directory, add handlers
3. Dashboard frontend — add Sessions tab UI
4. Integration test — end-to-end: record short session, export, verify in AdvantageScope

## Dependencies & Risks

**Dependencies:**
- WPILog format spec (v1.0) is stable and well-documented — low risk
- AdvantageScope compatibility — test with current release (v4.x)

**Risks:**
- **JSON number precision**: Go's `json.Unmarshal` decodes numbers as `float64`. For `int64` values > 2^53, precision is lost. FRC robot integers rarely exceed this, but the encoder should handle it (use `json.Number` or `json.Decoder` with `UseNumber()` for the conversion path)
- **Large sessions**: In-memory conversion works for observed sizes (up to ~2.4MB). If sessions grow much larger, will need streaming conversion. Defer until needed.
- **Windows file locking**: On Windows, reading a file while another process has it open can fail with a sharing violation. The active-session exclusion handles this — we never try to read the file the ntlogger is writing.

## Sources & References

### Internal References
- Session file creation: `internal/ntlogger/logger.go:229-252`
- JSONL data entry format: `internal/ntlogger/logger.go:208-218`
- Match marker format: `internal/ntlogger/logger.go:295-321`
- Existing file scanning pattern: `internal/uploader/uploader.go:300-339`
- Dashboard server patterns: `internal/dashboard/server.go:116-133`
- Dashboard frontend patterns: `internal/dashboard/static/index.html`
- Status struct: `internal/status/status.go:15-48`

### External References
- WPILog format specification: https://github.com/wpilibsuite/allwpilib/blob/main/wpiutil/doc/datalog.adoc
- WPILog Kaitai Struct definition: https://github.com/wpilibsuite/allwpilib/blob/main/wpiutil/doc/wpilog.ksy
- WPILib Python reference reader: https://github.com/wpilibsuite/allwpilib/blob/main/wpiutil/examples/printlog/datalog.py
- AdvantageScope TypeScript encoder: https://github.com/Mechanical-Advantage/AdvantageScope (src/hub/dataSources/wpilog/)
- AdvantageScope log files documentation: https://docs.advantagescope.org/overview/log-files/
