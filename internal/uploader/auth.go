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
	"strings"
	"sync"
	"time"
)

const expiryMargin = 5 * time.Minute

// Auth manages JWT authentication with a RavenBrain server.
// It is safe for concurrent use.
type Auth struct {
	mu       sync.Mutex
	baseURL  string
	username string
	password string

	token    string
	tokenExp time.Time
}

// NewAuth creates an Auth that authenticates against baseURL using the
// given credentials. The first token is obtained lazily on the first
// call to GetAuthHeader.
func NewAuth(baseURL, username, password string) *Auth {
	return &Auth{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
	}
}

// IsConfigured reports whether the auth has a server URL and credentials.
func (a *Auth) IsConfigured() bool {
	return a.baseURL != "" && a.username != "" && a.password != ""
}

// GetAuthHeader returns an "Authorization: Bearer <token>" value string,
// logging in or renewing the token as needed. Returns an error if the
// login request fails.
func (a *Auth) GetAuthHeader() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token == "" || time.Now().After(a.tokenExp.Add(-expiryMargin)) {
		if err := a.login(); err != nil {
			return "", err
		}
	}
	return "Bearer " + a.token, nil
}

// Invalidate forces a re-login on the next call to GetAuthHeader.
func (a *Auth) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = ""
	a.tokenExp = time.Time{}
}

// BaseURL returns the configured RavenBrain server URL.
func (a *Auth) BaseURL() string {
	return a.baseURL
}

// login performs POST /login and extracts the JWT from the response.
// Must be called with a.mu held.
func (a *Auth) login() error {
	if !a.IsConfigured() {
		return fmt.Errorf("ravenbrain credentials not configured")
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
