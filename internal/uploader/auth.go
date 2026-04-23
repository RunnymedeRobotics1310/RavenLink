// Package uploader provides store-and-forward uploading of JSONL telemetry
// files to a RavenBrain server, including JWT authentication.
package uploader

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// IsSecureURL reports whether the URL is safe to send credentials to.
// A URL is "secure" if it uses https:// OR if it uses http:// with a
// loopback host (localhost, 127.x.x.x, ::1). This mirrors the browser
// "secure context" rule and lets RavenLink talk to a local WPILib sim
// or a wrangler dev worker at http://localhost:8787 without silently
// dropping the upload target.
func IsSecureURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "https" {
		return true
	}
	if scheme != "http" {
		return false
	}
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if strings.HasSuffix(host, ".localhost") {
		return true
	}
	return false
}

const expiryMargin = 5 * time.Minute

// Auth manages authentication with a RavenBrain or RavenScope server.
// It supports two modes:
//
//  1. Legacy username/password via POST /login → JWT (RavenBrain).
//  2. Bearer api_key mode (RavenScope): when apiKey is set, every request
//     carries Authorization: Bearer <apiKey> and no /login call is made.
//
// Bearer mode is selected whenever apiKey is non-empty, even if
// username/password are also set. This lets a single config support
// both servers during migration. It is safe for concurrent use.
type Auth struct {
	mu       sync.Mutex
	baseURL  string
	username string
	password string
	apiKey   string

	token    string
	tokenExp time.Time
}

// NewAuth creates an Auth that authenticates against baseURL using the
// given legacy credentials. The first token is obtained lazily on the
// first call to GetAuthHeader. To enable bearer mode, call SetAPIKey or
// use NewAuthWithKey.
func NewAuth(baseURL, username, password string) *Auth {
	return &Auth{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
	}
}

// NewAuthWithKey creates an Auth that authenticates against baseURL using
// a bearer api_key. Every request will carry Authorization: Bearer <apiKey>
// and no /login call will be made.
func NewAuthWithKey(baseURL, apiKey string) *Auth {
	return &Auth{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
	}
}

// SetAPIKey switches Auth into bearer-token mode (if apiKey is non-empty)
// or back to legacy mode (if apiKey is empty). Intended for config reload
// and test wiring.
func (a *Auth) SetAPIKey(apiKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.apiKey = apiKey
}

// IsConfigured reports whether the auth has enough state to make
// authenticated requests. True when baseURL is set AND either an
// api_key is set, or both username and password are set.
func (a *Auth) IsConfigured() bool {
	if a.baseURL == "" {
		return false
	}
	if a.apiKey != "" {
		return true
	}
	return a.username != "" && a.password != ""
}

// GetAuthHeader returns an "Authorization: Bearer <token>" value string.
//
// In bearer mode (apiKey set) it returns the api_key directly and makes
// no HTTP call. In legacy mode it logs in against /login and caches the
// JWT, renewing 5 minutes before expiry. Returns an error if auth is not
// configured, the base URL is not https://, or the legacy /login call
// fails.
func (a *Auth) GetAuthHeader() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.IsConfigured() {
		return "", fmt.Errorf("ravenbrain credentials not configured")
	}

	// Refuse to send credentials over plaintext HTTP — except to
	// loopback hosts, which are trusted for sim / local dev.
	if !IsSecureURL(a.baseURL) {
		return "", fmt.Errorf("url must use https:// or http://localhost (got %q) — refusing to send credentials over plaintext", a.baseURL)
	}

	if a.apiKey != "" {
		// Bearer mode: the api_key is the credential. No /login, no cache,
		// no renewal. Invalidate() is a no-op in this mode.
		return "Bearer " + a.apiKey, nil
	}

	if a.token == "" || time.Now().After(a.tokenExp.Add(-expiryMargin)) {
		if err := a.login(); err != nil {
			return "", err
		}
	}
	return "Bearer " + a.token, nil
}

// Invalidate forces a re-login on the next call to GetAuthHeader. In
// bearer mode this is a no-op: the api_key is the credential and cannot
// be invalidated client-side.
func (a *Auth) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.apiKey != "" {
		return
	}
	a.token = ""
	a.tokenExp = time.Time{}
}

// BaseURL returns the configured RavenBrain server URL.
func (a *Auth) BaseURL() string {
	return a.baseURL
}

// login performs POST /login and extracts the JWT from the response.
// Must be called with a.mu held. Only reached in legacy mode — bearer
// mode returns from GetAuthHeader before ever calling login().
func (a *Auth) login() error {
	if a.baseURL == "" || a.username == "" || a.password == "" {
		return fmt.Errorf("ravenbrain credentials not configured")
	}

	// Callers (GetAuthHeader) already gate, but keep the defense here.
	if !IsSecureURL(a.baseURL) {
		return fmt.Errorf("url must use https:// or http://localhost (got %q) — refusing to send credentials over plaintext", a.baseURL)
	}

	url := a.baseURL + "/login"
	payload, err := json.Marshal(map[string]string{
		"username": a.username,
		"password": a.password,
	})
	if err != nil {
		return fmt.Errorf("marshal login body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read login response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("login failed: invalid credentials for %q", a.username)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("login failed: HTTP %d %s", resp.StatusCode, resp.Status)
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse login response: %w", err)
	}
	if result.AccessToken == "" {
		return fmt.Errorf("login response missing access_token")
	}

	a.token = result.AccessToken
	a.tokenExp = decodeJWTExp(a.token)

	slog.Info("uploader/auth: authenticated with RavenBrain",
		"user", a.username,
		"expiresIn", time.Until(a.tokenExp).Round(time.Second),
	)
	return nil
}

// decodeJWTExp extracts the "exp" claim from a JWT payload without
// verifying the signature. Returns the zero time on any decode error.
func decodeJWTExp(token string) time.Time {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		slog.Warn("uploader/auth: JWT does not have 3 parts, cannot decode expiry")
		return time.Time{}
	}

	// JWT payload is base64url-encoded (no padding).
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		slog.Warn("uploader/auth: could not base64-decode JWT payload", "err", err)
		return time.Time{}
	}

	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		slog.Warn("uploader/auth: could not parse JWT claims", "err", err)
		return time.Time{}
	}

	if claims.Exp == 0 {
		slog.Warn("uploader/auth: JWT has no exp claim")
		return time.Time{}
	}

	return time.Unix(int64(claims.Exp), 0)
}
