---
status: pending
priority: p2
issue_id: 009
tags: [code-review, security, config]
dependencies: []
---

# P2: Config file security hardening (perms, TLS scheme, atomic write, input validation)

## Problem Statement

Multiple medium-severity config and credential issues from the security review. Grouped together because they share a theme and should be fixed as one pass.

### Issue A: config.yaml written with 0644 (world-readable)
`internal/config/config.go:117`:
```go
if err := os.WriteFile(path, data, 0644); err != nil {
```
The file contains `obs_password` and `ravenbrain.password` in plaintext. On a shared student DS laptop, any local user account can read credentials.

### Issue B: No scheme enforcement on ravenbrain_url
`internal/uploader/auth.go:83-99` — no check for `https://`. A typo or malicious config edit to `http://...` sends the username+password in plaintext and then every JWT in every request over unencrypted HTTP.

### Issue C: config.SaveConfig is not atomic
`internal/config/config.go:111-122` — `os.WriteFile` directly overwrites. A crash or power loss mid-write leaves a truncated config.yaml with no backup, losing credentials. Dashboard can trigger this write.

### Issue D: Dashboard POST has no input validation
`internal/dashboard/server.go:131-205`:
- `data_dir` accepts any string (including `..` path traversal)
- `dashboard_port` accepts negative, 0, >65535 silently
- `retention_days` no bounds (negative disables pruning silently)
- `log_level` accepts typos (falls back to INFO silently)
- `record_trigger` doesn't validate against `{fms, auto, any}`

### Issue E: macOS plist generation lacks XML escaping
`internal/autostart/autostart_darwin.go:34-49` — executable path substituted via `fmt.Sprintf` into XML without escaping. If path contains `&`, `<`, `>`, `"`, `'` (possible with unusual home directories), the plist is malformed.

## Findings

- **Locations**: `internal/config/config.go:117,111-122`, `internal/uploader/auth.go:83-99`, `internal/dashboard/server.go:131-205`, `internal/autostart/autostart_darwin.go:34-49`
- **Agent**: Security Sentinel flagged as M1, M2, M3, M4, M5

## Proposed Solutions

### Fix A: Write config with 0600
```go
os.WriteFile(path, data, 0o600)
```
If file existed with looser perms, also `os.Chmod` it.

### Fix B: Refuse non-HTTPS ravenbrain_url
In `auth.go login()`, error out if URL scheme isn't `https://`. Log a loud warning at config load if user set `http://`.

### Fix C: Atomic write
Write to `config.yaml.tmp` in the same directory, `fsync`, then `os.Rename`. Keep a `.bak` of previous version.

### Fix D: Validate dashboard POST fields
- `data_dir`: require absolute path, reject `..`, optionally whitelist a base directory
- `dashboard_port`: enforce `1 <= port <= 65535`
- `retention_days`: enforce `1 <= days <= 365`
- `log_level`: whitelist `{DEBUG, INFO, WARNING, ERROR}`
- `record_trigger`: whitelist `{fms, auto, any}`
- `team`: enforce `1 <= team <= 9999`

### Fix E: Use encoding/xml for plist (or escape before substitution)
Switch `autostart_darwin.go` to marshal the plist struct with `encoding/xml`, or add an `xmlEscape` helper and apply to `exe` before substitution.

## Recommended Action

Fix all five in one pass — they're all small and related to "don't trust inputs, don't expose outputs". A and D are most important.

## Technical Details

- Affected files:
  - `internal/config/config.go` (fix A, C)
  - `internal/uploader/auth.go` (fix B)
  - `internal/dashboard/server.go` (fix D)
  - `internal/autostart/autostart_darwin.go` (fix E)

## Acceptance Criteria

- [ ] `stat config.yaml` shows mode 0600
- [ ] Setting `ravenbrain_url: http://...` either refuses to send credentials or logs a loud warning
- [ ] Atomic write: crash during SaveConfig does not leave corrupt config.yaml
- [ ] Dashboard POST with invalid `data_dir: "../etc"` returns 400
- [ ] Dashboard POST with `port: 99999` returns 400
- [ ] Dashboard POST with `record_trigger: "bogus"` returns 400
- [ ] macOS plist generation with exe path containing `&` produces valid XML
