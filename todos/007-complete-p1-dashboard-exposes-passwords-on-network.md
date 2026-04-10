---
status: pending
priority: p1
issue_id: 007
tags: [code-review, security, dashboard, credentials]
dependencies: []
---

# P1: Dashboard binds to 0.0.0.0 and returns plaintext passwords

## Problem Statement

Two compounding security bugs make credentials trivially exposed on any network the DS laptop is connected to:

### Bug A: Dashboard binds to all interfaces
`internal/dashboard/server.go:67-68`:
```go
addr := fmt.Sprintf(":%d", port)
srv := &http.Server{Addr: addr, Handler: mux}
```
`:8080` in Go means `0.0.0.0:8080` — every interface, including WiFi, Ethernet, and the **robot-radio network** (10.TE.AM.x) that the DS is actively connected to during matches. The log line says `http://localhost:%d`, creating a false sense of localhost-only binding.

### Bug B: GET /api/config returns plaintext passwords
`internal/dashboard/server.go:107,120`:
```go
"obs_password":        cfg.Bridge.OBSPassword,
"ravenbrain_password": cfg.RavenBrain.Password,
```
Both the OBS WebSocket password AND the RavenBrain telemetry-agent password are returned in plaintext on every `GET /api/config` — **with no authentication required**.

## Combined Impact

Anyone on the same network as the DS laptop (at a competition, that includes every team on the field network) can:
```bash
curl http://<ds-laptop-ip>:8080/api/config
```
and harvest:
- The OBS WebSocket password → can start/stop recording remotely, read scene data
- The RavenBrain service account password → can post arbitrary telemetry to your RavenBrain under the `telemetry-agent` identity

There is no CSRF protection, no Origin check, no authentication of any kind on the config editor endpoints.

## Findings

- **Location**: `internal/dashboard/server.go:54-80, 107-120, 131-205`
- **Agent**: Security Sentinel flagged as H1, H2, H3 (three related HIGH findings)

## Proposed Solutions

### Fix A: Bind to 127.0.0.1 only
```go
addr := fmt.Sprintf("127.0.0.1:%d", port)
```
One-line fix. Massive impact reduction — dashboard is no longer reachable from the network.
- Effort: Small
- Risk: None

### Fix B: Mask passwords in GET /api/config
Return `"***"` (or empty string) for password fields. In POST, treat that sentinel as "leave unchanged" so the editor round-trips without leaking secrets to the browser.
- Effort: Small
- Risk: Low

### Fix C: Origin check on state-changing endpoints
`POST /api/config` and `POST /api/config/reload` should reject requests unless `Origin` is empty, `http://localhost:<port>`, or `http://127.0.0.1:<port>`. Defense against localhost CSRF from a malicious page.
- Effort: Small
- Risk: Low

### Fix D: Verify Host header matches expected
Reject requests whose `Host` header isn't `localhost:<port>` or `127.0.0.1:<port>`.
- Effort: Small
- Risk: Low

## Recommended Action

**All four fixes together** — they're defense-in-depth and all trivial:

1. Bind to 127.0.0.1 (removes 99% of the exposure)
2. Mask passwords in GET response (removes the credential-harvesting leak)
3. Origin check on POST handlers (defense against localhost drive-by from a browser)
4. Host header validation (defense against DNS rebinding attacks)

## Technical Details

- Affected files:
  - `internal/dashboard/server.go` (all four fixes)
  - `internal/dashboard/static/index.html` (handle masked password in UI — only POST if user typed a new value)

## Acceptance Criteria

- [ ] `netstat` shows dashboard listening on 127.0.0.1:8080, not 0.0.0.0:8080
- [ ] `curl http://<other-machine-ip>:8080/` from another computer fails to connect
- [ ] `curl http://localhost:8080/api/config` returns `"***"` or `""` for password fields
- [ ] POST /api/config from a non-localhost Origin returns 403
- [ ] Config editor UI works correctly with masked passwords (doesn't overwrite with `***`)
