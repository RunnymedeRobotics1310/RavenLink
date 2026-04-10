---
status: pending
priority: p1
issue_id: 008
tags: [code-review, security, rbping, credentials]
dependencies: []
---

# P1: rbping utility prints full JWT and password fingerprint to stdout

## Problem Statement

`cmd/rbping/main.go` is a diagnostic tool that leaks credential material to stdout — which typically ends up in terminal scrollback, CI logs, screenshots pasted to Discord/Slack, or committed to issues.

### Leak A: Full JWT access token
`cmd/rbping/main.go:92`:
```go
fmt.Printf("      Body:   %s\n", string(loginRespBody))
```
Unconditionally prints the full `POST /login` response body, which contains `{"access_token":"eyJ..."}`. This is a **valid bearer token** until expiry (12 hours) that grants the telemetry-agent full telemetry API access. Line 136 separately prints only head/tail of the token (safer), but line 92 has already dumped the full token a few lines above.

### Leak B: Password fingerprint
`cmd/rbping/main.go:42-55`:
```go
fmt.Printf("Password: len=%d", len(pw))
fmt.Printf(" first=%q last=%q", pw[:1], pw[len(pw)-1:])
fmt.Printf(" bytesum=%d\n", sum)
```
Printing length + first char + last char + byte-sum is a **very strong fingerprint** of a short password. For a 9-char service-account password, this reduces the brute-force search space by several orders of magnitude. The byte-sum in particular makes it trivial to **confirm a password guess offline with zero network round-trips**.

This is a diagnostic tool — people will naturally paste its output into chat when troubleshooting, inadvertently leaking credential material.

## Findings

- **Location**: `cmd/rbping/main.go:42-55, 92, 167`
- **Agent**: Security Sentinel flagged as H4, H5

## Proposed Solutions

### Fix A: Remove password fingerprint entirely
Print only `len=N` or nothing at all. If fingerprint is useful for debugging, gate it behind a `--debug-password` flag.
- Effort: Small
- Risk: None

### Fix B: Don't print raw login response body
Print only status code and, on success, the decoded JWT metadata (`exp`, `sub`, `roles`) which are already computed below. Mask the token itself.
- Effort: Small
- Risk: None

### Fix C: Also mask the /api/validate response body
Line 167 prints the full response body from `/api/validate`. Not credential material today (it's `{"status":"ok"}`), but on principle, diagnostic tools should print only what's needed.
- Effort: Small
- Risk: None

## Recommended Action

All three fixes. This is a 10-line change that removes a significant disclosure risk.

## Technical Details

- Affected files:
  - `cmd/rbping/main.go`

## Acceptance Criteria

- [ ] rbping output no longer contains the raw JWT
- [ ] rbping output no longer contains password first/last/bytesum (len only, if at all)
- [ ] Diagnostic info (token expiry, subject, roles) is still available via the JWT-decode path
- [ ] Screenshot of rbping output can be safely shared in a public channel
