---
title: "fix: Make telemetry upload idempotent and resilient to flaky networks"
type: fix
status: active
date: 2026-04-09
---

# fix: Make Telemetry Upload Idempotent and Resilient to Flaky Networks

## Overview

The current upload protocol has five critical failure modes that cause permanent upload blockage or silent data duplication on flaky networks. This plan fixes all five by making every endpoint idempotent and moving the source of truth for upload progress from a local `.progress` file to the server.

## Problem Statement

The upload protocol (POST /session → POST /data batches → POST /complete) breaks when any HTTP response is lost:

| Failure Mode | Current Behavior | Impact |
|---|---|---|
| POST /session retry (same sessionId) | **500 error** — UNIQUE constraint violation | Upload permanently blocked |
| POST /data retry (same batch) | All entries inserted again | **Silent data duplication** |
| POST /complete retry | Overwrites endedAt/entryCount | Harmless but sloppy |
| Progress file lost/corrupt | Client resumes from line 0, hits session UNIQUE | **Upload permanently blocked** |
| Any response timeout | Client can't tell if server processed it | Cascades into above |

These are guaranteed to occur on FRC competition WiFi.

## Proposed Solution

Three changes, all simple:

### 1. Server: Idempotent session creation (upsert)

**`TelemetryService.createSession`** — check `findBySessionId` first; if session exists, return it instead of inserting a duplicate.

```java
public TelemetrySession createSession(String sessionId, int teamNumber, String robotIp, Instant startedAt) {
    Optional<TelemetrySession> existing = sessionRepository.findBySessionId(sessionId);
    if (existing.isPresent()) {
        log.info("Session {} already exists, returning existing", sessionId);
        return existing.get();
    }
    var session = new TelemetrySession(null, sessionId, teamNumber, robotIp,
        startedAt, null, 0, 0, startedAt);
    return sessionRepository.save(session);
}
```

**Effect:** Client can call POST /session as many times as it wants with the same sessionId. First call creates, subsequent calls return existing. No more 500 errors.

### 2. Server: Track uploaded entry count per session

**New migration `V25__telemetry_uploaded_count.sql`:**
```sql
ALTER TABLE RB_TELEMETRY_SESSION ADD COLUMN uploaded_count INT NOT NULL DEFAULT 0;
```

**`TelemetryService.bulkInsertEntries`** — wrap the batch INSERT and `uploaded_count` UPDATE in a single transaction:

```java
conn.setAutoCommit(false);
try {
    ps.executeBatch();
    try (PreparedStatement up = conn.prepareStatement(
            "UPDATE RB_TELEMETRY_SESSION SET uploaded_count = uploaded_count + ? WHERE id = ?")) {
        up.setInt(1, entries.size());
        up.setLong(2, sessionId);
        up.executeUpdate();
    }
    conn.commit();
} catch (Exception e) {
    conn.rollback();
    throw e;
}
```

**New endpoint `GET /session/{sessionId}`** — returns session including `uploadedCount` so the client can ask "how many entries do you have?"

**Effect:** Either a batch + its count both commit, or neither does. No partial state. Client can always ask the server how far it got.

### 3. Client: Use server as source of truth, remove `.progress` files

**Rewritten `_upload_file` flow:**

```python
def _upload_file(self, filepath: Path) -> bool:
    # ... parse session_start, collect entries ...

    # Step 1: Always create/get session (idempotent)
    self._post_json(f"/api/telemetry/session", {...})

    # Step 2: Ask server how many entries it has
    server_count = self._get_uploaded_count(session_id)

    # Step 3: Skip entries the server already has
    remaining = entries[server_count:]
    for batch in chunked(remaining, self._batch_size):
        self._post_json(f"/api/telemetry/session/{session_id}/data", batch)

    # Step 4: Complete session
    self._post_json(f"/api/telemetry/session/{session_id}/complete", {...})
    return True
```

**Remove entirely:** `_read_progress`, `_write_progress`, `_delete_progress`, all `.progress` file handling.

**Add:** `_get_json` helper method and `_get_uploaded_count(session_id)` method.

**Effect:** Progress file can never be wrong because it doesn't exist. Server state is always authoritative.

## Resilience Sequence (Flaky Network)

```
CLIENT                              SERVER
  |--- POST /session (always) -------->|
  |<-- 200 OK (created or existing) ---|
  |                                    |
  |--- GET /session/{id} ------------>|
  |<-- 200 {uploadedCount: 0} --------|
  |                                    |
  |--- POST /data [0-499] ----------->|  (uploadedCount → 500, transactional)
  |<-- 200 OK -------------------------|
  |                                    |
  |--- POST /data [500-999] --------->|  (uploadedCount → 1000)
  |    *** NETWORK TIMEOUT ***         |  (server committed successfully)
  |                                    |
  |  ... exponential backoff ...       |
  |                                    |
  |--- POST /session (idempotent) --->|  ← no error this time
  |<-- 200 OK (existing) -------------|
  |                                    |
  |--- GET /session/{id} ------------>|
  |<-- 200 {uploadedCount: 1000} -----|  ← client learns server has 1000
  |                                    |
  |  (skips entries 0-999)             |
  |--- POST /data [1000-1499] ------->|
  |<-- 200 OK -------------------------|
  |                                    |
  |--- POST /complete --------------->|
  |<-- 200 OK -------------------------|
```

## Files to Modify

### RavenBrain (Java)

| File | Change |
|---|---|
| `src/main/resources/db/migration/V25__telemetry_uploaded_count.sql` | **New.** `ALTER TABLE ADD COLUMN uploaded_count` |
| `src/main/java/.../telemetry/TelemetrySession.java` | Add `uploadedCount` field to record |
| `src/main/java/.../telemetry/TelemetryService.java` | 1. Idempotent `createSession` (check-then-insert). 2. Transactional `bulkInsertEntries` (INSERT + UPDATE count in one tx) |
| `src/main/java/.../telemetry/TelemetryApi.java` | 1. Add `GET /session/{sessionId}` endpoint. 2. Return 200 for both new and existing sessions in `createSession` |
| `src/test/java/.../telemetry/TelemetryApiTest.java` | Add tests: duplicate session creation returns existing, GET session returns uploadedCount, retry after data upload resumes correctly |

### AutoOBS (Python)

| File | Change |
|---|---|
| `src/uploader.py` | 1. Add `_get_json` helper. 2. Add `_get_uploaded_count(session_id)`. 3. Rewrite `_upload_file` to always POST /session + GET count + send remaining. 4. **Remove** `_read_progress`, `_write_progress`, `_delete_progress`. 5. Remove `.progress` cleanup from `prune_uploaded`. |
| `tests/test_uploader.py` | Update tests: remove progress file tests, add server-count-based resumption tests, add duplicate session creation tests |

### Files NOT changed

- `nt_logger.py` — writes JSONL files, not affected
- `config.py` / `main.py` — no new config needed
- `RB_TELEMETRY_ENTRY` schema — no uniqueness constraints added (unnecessary with transactional count tracking)
- JSONL format — fully backwards compatible

## Acceptance Criteria

- [ ] POST /session with an existing sessionId returns 200 with the existing session (not 500)
- [ ] POST /data followed by a simulated timeout, then retry: no duplicate entries in DB
- [ ] GET /session/{id} returns correct uploadedCount after data batches
- [ ] Client with no prior state (fresh start) can resume an interrupted upload by querying server
- [ ] `.progress` files are no longer created or read
- [ ] Batch INSERT and uploaded_count UPDATE are atomic (transactional)
- [ ] POST /complete called twice with same values is harmless
- [ ] Existing test suites pass (state machine, bitmask, RavenBrain)

## Verification

1. **Unit test (Python):** Mock HTTP responses, verify client calls GET /session and skips correct number of entries on retry
2. **Unit test (Python):** Verify no `.progress` files are created during upload
3. **Integration test (Java):** Create session, post 2 batches, verify uploadedCount=1000. Call POST /session again, verify same session returned. POST batch 3, verify uploadedCount=1500
4. **Integration test (Java):** Create session, post batch, call POST /session again — verify 200 (not 500)
5. **Manual test:** Start upload, kill the process mid-batch, restart — verify upload resumes from correct point with no duplicates
