"""JWT authentication with RavenBrain — login, token caching, and renewal."""

import base64
import json
import logging
import time
import urllib.error
import urllib.request

log = logging.getLogger(__name__)


class RavenBrainAuth:
    """Manages JWT authentication with a RavenBrain server.

    Authenticates via POST /login, caches the JWT, and renews it
    before expiry or after a 401 response.
    """

    EXPIRY_MARGIN = 300  # re-login 5 minutes before expiry

    def __init__(self, base_url: str, username: str, password: str) -> None:
        self._base_url = base_url.rstrip("/") if base_url else ""
        self._username = username
        self._password = password
        self._token: str | None = None
        self._token_exp: float = 0.0  # unix timestamp when token expires

    @property
    def is_configured(self) -> bool:
        return bool(self._base_url and self._username and self._password)

    def get_auth_header(self) -> dict[str, str]:
        """Return Authorization header with a valid JWT, logging in if needed."""
        if self._token is None or self._is_expired():
            self._login()
        return {"Authorization": f"Bearer {self._token}"}

    def invalidate(self) -> None:
        """Force re-login on next get_auth_header() call."""
        self._token = None
        self._token_exp = 0.0

    def _is_expired(self) -> bool:
        return time.time() >= (self._token_exp - self.EXPIRY_MARGIN)

    def _login(self) -> None:
        """POST /login with JSON credentials, extract access_token from response."""
        if not self.is_configured:
            raise AuthError("RavenBrain credentials not configured")

        url = f"{self._base_url}/login"
        payload = json.dumps({
            "username": self._username,
            "password": self._password,
        }).encode("utf-8")

        req = urllib.request.Request(
            url,
            data=payload,
            method="POST",
            headers={"Content-Type": "application/json"},
        )

        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                body = json.loads(resp.read().decode("utf-8"))
                self._token = body["access_token"]
                self._token_exp = _decode_jwt_exp(self._token)
                log.info("Authenticated with RavenBrain as '%s' (expires in %.0fs)",
                         self._username, self._token_exp - time.time())
        except urllib.error.HTTPError as e:
            if e.code == 401:
                raise AuthError(f"RavenBrain login failed: invalid credentials for '{self._username}'")
            raise AuthError(f"RavenBrain login failed: HTTP {e.code} {e.reason}")
        except (urllib.error.URLError, OSError) as e:
            raise AuthError(f"RavenBrain login failed: {e}")
        except (KeyError, ValueError) as e:
            raise AuthError(f"RavenBrain login response missing access_token: {e}")


class AuthError(Exception):
    """Raised when authentication with RavenBrain fails."""
    pass


def _decode_jwt_exp(token: str) -> float:
    """Extract the 'exp' claim from a JWT without verifying the signature."""
    try:
        parts = token.split(".")
        if len(parts) != 3:
            return 0.0
        # JWT payload is base64url-encoded
        payload_b64 = parts[1]
        # Add padding if needed
        padding = 4 - len(payload_b64) % 4
        if padding != 4:
            payload_b64 += "=" * padding
        payload_json = base64.urlsafe_b64decode(payload_b64)
        payload = json.loads(payload_json)
        return float(payload.get("exp", 0))
    except Exception:
        log.warning("Could not decode JWT expiry — will re-login on next request")
        return 0.0
