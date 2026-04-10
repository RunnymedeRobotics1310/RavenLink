---
title: "refactor: Switch from API key to JWT auth for RavenBrain telemetry upload"
type: refactor
status: active
date: 2026-04-09
---

# refactor: Switch from API Key to JWT Auth for RavenBrain Upload

## Overview

RavenBrain commit `e62ea67` replaced the `X-Telemetry-Key` header filter with proper JWT role-based auth. A `telemetry-agent` service account (role `ROLE_TELEMETRY_AGENT`) is created by migration V26, and its password is set from config on every startup. All telemetry endpoints now require `@Secured({"ROLE_TELEMETRY_AGENT", "ROLE_SUPERUSER"})`.

The RavenLink uploader must switch from `X-Telemetry-Key` header to: login with basic auth → obtain JWT → use Bearer token → handle expiry/renewal.

## RavenBrain Auth Flow

Confirmed from `RemoteRavenBrainClient.java:62-98` — RavenBrain's own internal client uses this flow:

1. `POST /login` with JSON body `{"username": "telemetry-agent", "password": "..."}`
2. Response body: `{"access_token": "<jwt>", ...}`
3. All subsequent requests use `Authorization: Bearer <token>`
4. JWT expires after 12 hours (configured in `application.yml`)
5. On 401 response, re-authenticate by calling `POST /login` again

## Proposed Solution

### New module: `src/ravenbrain_auth.py`

```python
class RavenBrainAuth:
    """Manages JWT authentication with RavenBrain."""

    def __init__(self, base_url: str, username: str, password: str):
        ...

    def get_auth_header(self) -> dict[str, str]:
        """Return Authorization header, logging in if needed."""
        if self._token is None or self._is_expired():
            self._login()
        return {"Authorization": f"Bearer {self._token}"}

    def invalidate(self) -> None:
        """Force re-login on next request (call after 401)."""
        self._token = None

    def _login(self) -> None:
        """POST /login with JSON credentials, extract access_token from response."""

    def _is_expired(self) -> bool:
        """Check if token expires within 5 minutes (safety margin)."""
```

- Parses JWT expiry from the token payload (base64-decode the middle segment, read `exp` field)
- Re-authenticates 5 minutes before expiry (safety margin)
- On login failure, logs warning and raises — uploader catches and applies backoff
- No external JWT library needed — standard base64 decode of the payload segment

### Modified: `src/uploader.py`

- Replace `api_key: str` constructor param with `auth: RavenBrainAuth`
- Replace `X-Telemetry-Key` header with `auth.get_auth_header()` in `_post_json` and `_get_json`
- On HTTP 401 response: call `auth.invalidate()` and retry once (re-login + retry)
- On persistent 401: apply backoff like any other error

### Modified: `src/config.py`

- Replace `ravenbrain_api_key: str` with `ravenbrain_username: str = ""` and `ravenbrain_password: str = ""`
- Update CLI args: `--ravenbrain-api-key` → `--ravenbrain-username` and `--ravenbrain-password`
- Update `[ravenbrain]` INI section: `api_key` → `username` and `password`
- Update `save_to_ini()` and `reload_from_ini()`
- Mark `ravenbrain_password` as sensitive in dashboard

### Modified: `src/main.py`

- Create `RavenBrainAuth` instance and pass to `Uploader`

### Modified: `src/web_dashboard.py`

- Update config GET/POST to use `ravenbrain_username`/`ravenbrain_password`
- Add `ravenbrain_password` to the SENSITIVE set (masked in UI)
- Replace `ravenbrain_api_key` in SECTIONS and FIELD_DESCS

### Modified: `config.ini.example`

- Replace `api_key =` with `username = telemetry-agent` and `password =`

## Files to Modify

| File | Change |
|------|--------|
| `src/ravenbrain_auth.py` | **New.** JWT login, token management, expiry checking |
| `src/uploader.py` | Replace api_key with RavenBrainAuth, add 401 retry |
| `src/config.py` | Replace `ravenbrain_api_key` with `username`/`password` |
| `src/main.py` | Create RavenBrainAuth, pass to Uploader |
| `src/web_dashboard.py` | Update config fields for username/password |
| `config.ini.example` | Update [ravenbrain] section |
| `tests/test_uploader.py` | Update to use mock auth instead of api_key |

## Acceptance Criteria

- [ ] Uploader authenticates with basic auth to `/api/validate` and receives JWT
- [ ] All telemetry API calls use `Authorization: Bearer <token>` header
- [ ] JWT is cached and reused until near expiry (5-minute safety margin)
- [ ] On 401 response, token is invalidated, re-login attempted, request retried once
- [ ] On persistent auth failure, exponential backoff applies (same as network errors)
- [ ] `ravenbrain_password` is masked in the dashboard config editor
- [ ] Config stores `username` and `password` (not API key)
- [ ] No external JWT library required (base64 decode of payload only)
- [ ] Works with `telemetry-agent` service account and `ROLE_TELEMETRY_AGENT`
