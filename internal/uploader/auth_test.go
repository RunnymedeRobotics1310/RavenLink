package uploader

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// makeJWT crafts a fake JWT with the given payload claims. Signature is not
// verified by the client so we use a placeholder string.
func makeJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header := `{"alg":"HS256","typ":"JWT"}`
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	h := base64.RawURLEncoding.EncodeToString([]byte(header))
	p := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return h + "." + p + ".fake-signature"
}

// fakeServer wraps an httptest.Server with a login-request counter and
// configurable response body.
type fakeServer struct {
	srv        *httptest.Server
	loginCount atomic.Int32
	respFn     func(w http.ResponseWriter, r *http.Request)
}

// newFakeServer returns a fakeServer with an HTTPS-style URL. Auth.login
// refuses plaintext HTTP — httptest.NewTLSServer gives us an https:// URL
// backed by a self-signed cert, but the *Auth* doesn't actually connect via
// our TLS — it uses a private http.Client{Timeout: ...}. To avoid TLS
// verification issues we use NewServer and manually prepend "https://" only
// when testing the scheme check; for functional tests we monkey with the URL
// string so Auth.login's scheme check passes, and then replace the prefix
// back to http:// via a *real* NewServer, which is easier said than done.
//
// Simpler approach: use httptest.NewTLSServer and install a custom
// http.DefaultTransport for the duration of the test. But Auth constructs
// its own http.Client, so we can't inject one.
//
// Simplest approach: httptest.NewTLSServer + set the server URL on the Auth,
// AND set http.DefaultTransport's TLSClientConfig.InsecureSkipVerify. But
// Auth builds a fresh client each call so that's out too.
//
// Cleanest fix: use httptest.NewTLSServer and override the auth's baseURL
// WHILE also temporarily disabling TLS verification on the package-global
// http.DefaultTransport. The Auth's http.Client uses DefaultTransport by
// default because it doesn't set Transport explicitly.
func newFakeServer(t *testing.T, handler http.HandlerFunc) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		fs.loginCount.Add(1)
		handler(w, r)
	})
	fs.srv = httptest.NewTLSServer(mux)
	t.Cleanup(fs.srv.Close)
	return fs
}

// newAuthForServer constructs an Auth pointing at the fake server. Because
// Auth creates its own http.Client without overriding Transport, we mutate
// the *global* http.DefaultTransport's TLS config for the duration of the
// test. httptest.NewTLSServer gives us a server with a self-signed cert; to
// trust it, we reuse fs.srv.Client()'s Transport — but Auth doesn't accept a
// client. Instead we set InsecureSkipVerify on DefaultTransport and restore
// it in cleanup.
func newAuthForServer(t *testing.T, fs *fakeServer, user, pass string) *Auth {
	t.Helper()
	trustTestServer(t, fs)
	return NewAuth(fs.srv.URL, user, pass)
}

// newAuthWithKeyForServer constructs an Auth configured for bearer-token
// mode against the fake server. See newAuthForServer for why we mutate
// http.DefaultTransport.
func newAuthWithKeyForServer(t *testing.T, fs *fakeServer, apiKey string) *Auth {
	t.Helper()
	trustTestServer(t, fs)
	return NewAuthWithKey(fs.srv.URL, apiKey)
}

// trustTestServer configures http.DefaultTransport so it trusts the
// httptest TLS server's self-signed cert for the duration of the test.
func trustTestServer(t *testing.T, fs *fakeServer) {
	t.Helper()
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is not *http.Transport (%T)", http.DefaultTransport)
	}
	// Save prior config and restore after the test.
	prior := tr.TLSClientConfig
	clone := fs.srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tr.TLSClientConfig = clone
	t.Cleanup(func() { tr.TLSClientConfig = prior })
}

// ---------------------------------------------------------------------------
// TestAuthHappyPath — POST /login returns a valid JWT, GetAuthHeader returns
// "Bearer <token>", and the token is cached on subsequent calls.
// ---------------------------------------------------------------------------

func TestAuthHappyPath(t *testing.T) {
	exp := time.Now().Add(1 * time.Hour).Unix()
	token := makeJWT(t, map[string]any{
		"exp":   exp,
		"sub":   "telemetry-agent",
		"roles": []string{"uploader"},
	})

	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	})
	auth := newAuthForServer(t, fs, "telemetry-agent", "hunter2")

	got, err := auth.GetAuthHeader()
	if err != nil {
		t.Fatalf("first GetAuthHeader: %v", err)
	}
	want := "Bearer " + token
	if got != want {
		t.Errorf("header: got %q, want %q", got, want)
	}

	// Second call must be cached — no new login.
	got2, err := auth.GetAuthHeader()
	if err != nil {
		t.Fatalf("second GetAuthHeader: %v", err)
	}
	if got2 != want {
		t.Errorf("cached header: got %q, want %q", got2, want)
	}
	if n := fs.loginCount.Load(); n != 1 {
		t.Errorf("login count: got %d, want 1 (token should be cached)", n)
	}
}

// ---------------------------------------------------------------------------
// TestAuthExpiredToken — a cached token past its expiry (minus the margin)
// forces a re-login on the next GetAuthHeader.
// ---------------------------------------------------------------------------

func TestAuthExpiredToken(t *testing.T) {
	// Return a *fresh* token on every login so both calls succeed.
	exp := time.Now().Add(1 * time.Hour).Unix()
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		token := makeJWT(t, map[string]any{"exp": exp})
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	})
	auth := newAuthForServer(t, fs, "u", "p")

	// Prime the cache with a token that has already expired. We poke the
	// internal fields directly since we're in the same package.
	auth.mu.Lock()
	auth.token = "expired-placeholder"
	auth.tokenExp = time.Now().Add(-1 * time.Hour)
	auth.mu.Unlock()

	if _, err := auth.GetAuthHeader(); err != nil {
		t.Fatalf("GetAuthHeader: %v", err)
	}
	if n := fs.loginCount.Load(); n != 1 {
		t.Errorf("login count after expired token: got %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// TestAuthLogin401 — server returns 401 on login; GetAuthHeader surfaces an
// error mentioning invalid credentials.
// ---------------------------------------------------------------------------

func TestAuthLogin401(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	auth := newAuthForServer(t, fs, "u", "p")

	_, err := auth.GetAuthHeader()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid credentials") {
		t.Errorf("error %q should mention invalid credentials", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestAuthMissingAccessToken — server returns 200 with a body that has no
// access_token field; GetAuthHeader returns an error.
// ---------------------------------------------------------------------------

func TestAuthMissingAccessToken(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"other_field": "value"})
	})
	auth := newAuthForServer(t, fs, "u", "p")

	_, err := auth.GetAuthHeader()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "access_token") {
		t.Errorf("error %q should mention access_token", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestAuthMalformedJWT — a token that isn't 3 dot-separated segments decodes
// to exp=0 (= epoch), so *every* call re-logs-in. This documents the
// current behavior: a malformed JWT effectively disables caching.
// ---------------------------------------------------------------------------

func TestAuthMalformedJWT(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Only two segments — decodeJWTExp will log and return zero time.
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "not.ajwt"})
	})
	auth := newAuthForServer(t, fs, "u", "p")

	if _, err := auth.GetAuthHeader(); err != nil {
		t.Fatalf("first GetAuthHeader: %v", err)
	}
	if _, err := auth.GetAuthHeader(); err != nil {
		t.Fatalf("second GetAuthHeader: %v", err)
	}

	// Because the decoded exp is 0 (= 1970), both calls trigger a login.
	if n := fs.loginCount.Load(); n < 2 {
		t.Errorf("login count: got %d, want >=2 (malformed JWT should not cache)", n)
	}
}

// ---------------------------------------------------------------------------
// TestAuthInvalidateAndRetry — after Invalidate() the next call must re-login.
// ---------------------------------------------------------------------------

func TestAuthInvalidateAndRetry(t *testing.T) {
	exp := time.Now().Add(1 * time.Hour).Unix()
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		token := makeJWT(t, map[string]any{"exp": exp})
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	})
	auth := newAuthForServer(t, fs, "u", "p")

	if _, err := auth.GetAuthHeader(); err != nil {
		t.Fatalf("first: %v", err)
	}
	if n := fs.loginCount.Load(); n != 1 {
		t.Fatalf("login count after first: got %d, want 1", n)
	}

	auth.Invalidate()

	if _, err := auth.GetAuthHeader(); err != nil {
		t.Fatalf("after invalidate: %v", err)
	}
	if n := fs.loginCount.Load(); n != 2 {
		t.Errorf("login count after invalidate: got %d, want 2", n)
	}
}

// ---------------------------------------------------------------------------
// TestAuthHTTPSEnforcement — login() refuses plaintext HTTP URLs.
// ---------------------------------------------------------------------------

func TestAuthHTTPSEnforcement(t *testing.T) {
	auth := NewAuth("http://bad.example", "u", "p")
	_, err := auth.GetAuthHeader()
	if err == nil {
		t.Fatal("expected error for http:// URL, got nil")
	}
	if !strings.Contains(err.Error(), "https://") {
		t.Errorf("error %q should mention https://", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestAuthNotConfigured — empty credentials: IsConfigured is false,
// GetAuthHeader errors with "not configured".
// ---------------------------------------------------------------------------

func TestAuthNotConfigured(t *testing.T) {
	cases := []struct {
		name               string
		baseURL, user, pw  string
	}{
		{"all_empty", "", "", ""},
		{"missing_url", "", "u", "p"},
		{"missing_user", "https://x", "", "p"},
		{"missing_password", "https://x", "u", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			auth := NewAuth(c.baseURL, c.user, c.pw)
			if auth.IsConfigured() {
				t.Error("IsConfigured = true, want false")
			}
			_, err := auth.GetAuthHeader()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "not configured") {
				t.Errorf("error %q should mention 'not configured'", err.Error())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestAuthBaseURL — BaseURL() returns the trimmed baseURL as configured.
// ---------------------------------------------------------------------------

func TestAuthBaseURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://example.com", "https://example.com"},
		{"https://example.com/", "https://example.com"},
		{"https://example.com///", "https://example.com"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			auth := NewAuth(c.in, "u", "p")
			if got := auth.BaseURL(); got != c.want {
				t.Errorf("BaseURL() = %q, want %q", got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Bearer-token (api_key) auth mode — Unit 0-B of the RavenScope plan.
// When ravenbrain.api_key is set, Auth skips POST /login entirely and
// returns "Authorization: Bearer <apiKey>" directly. The legacy
// username/password flow stays intact for RavenBrain compatibility.
// ---------------------------------------------------------------------------

// TestAuthModeSelection is the table-driven core of the new bearer-token
// mode. It covers IsConfigured() and GetAuthHeader() across every
// combination of credentials the config layer can realistically produce,
// including the "api_key wins over username/password" precedence rule and
// the "empty string is not-set" edge case.
func TestAuthModeSelection(t *testing.T) {
	cases := []struct {
		name             string
		baseURL          string
		apiKey           string
		username         string
		password         string
		wantConfigured   bool
		wantHeader       string // exact header value; empty means "expect error"
		wantErrContains  string // substring the error must contain when wantHeader==""
		wantLoginAllowed bool   // true if a /login HTTP call is acceptable (legacy only)
	}{
		{
			name:             "api_key_only_returns_bearer",
			baseURL:          "https://example.test",
			apiKey:           "rsk_live_abc123",
			wantConfigured:   true,
			wantHeader:       "Bearer rsk_live_abc123",
			wantLoginAllowed: false,
		},
		{
			name:             "api_key_wins_over_user_pass",
			baseURL:          "https://example.test",
			apiKey:           "rsk_live_wins",
			username:         "telemetry-agent",
			password:         "hunter2",
			wantConfigured:   true,
			wantHeader:       "Bearer rsk_live_wins",
			wantLoginAllowed: false,
		},
		{
			name:            "empty_api_key_falls_back_to_legacy_when_creds_present",
			baseURL:         "https://example.test",
			apiKey:          "",
			username:        "u",
			password:        "p",
			wantConfigured:  true,
			wantHeader:      "", // legacy flow would try HTTP — verified separately
			wantErrContains: "",
			// This case is validated by a bespoke test below that stubs /login.
			// We skip GetAuthHeader assertions in the table here.
		},
		{
			name:            "only_baseurl_set_is_not_configured",
			baseURL:         "https://example.test",
			wantConfigured:  false,
			wantErrContains: "not configured",
		},
		{
			name:            "api_key_without_baseurl_is_not_configured",
			baseURL:         "",
			apiKey:          "rsk_live_lonely",
			wantConfigured:  false,
			wantErrContains: "not configured",
		},
		{
			name:            "api_key_with_plaintext_http_is_refused",
			baseURL:         "http://bad.example",
			apiKey:          "rsk_live_plain",
			wantConfigured:  true, // creds + url are present
			wantErrContains: "https://",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			auth := NewAuth(c.baseURL, c.username, c.password)
			auth.SetAPIKey(c.apiKey)

			if got := auth.IsConfigured(); got != c.wantConfigured {
				t.Errorf("IsConfigured() = %v, want %v", got, c.wantConfigured)
			}

			// Skip header assertions for the legacy-fallback row — it
			// requires an HTTP server and is exercised in a dedicated test.
			if c.name == "empty_api_key_falls_back_to_legacy_when_creds_present" {
				return
			}

			got, err := auth.GetAuthHeader()
			if c.wantHeader != "" {
				if err != nil {
					t.Fatalf("GetAuthHeader err = %v, want nil", err)
				}
				if got != c.wantHeader {
					t.Errorf("GetAuthHeader = %q, want %q", got, c.wantHeader)
				}
				return
			}
			// Error-expected branch.
			if err == nil {
				t.Fatalf("GetAuthHeader err = nil, want error containing %q", c.wantErrContains)
			}
			if c.wantErrContains != "" && !strings.Contains(err.Error(), c.wantErrContains) {
				t.Errorf("error %q should contain %q", err.Error(), c.wantErrContains)
			}
		})
	}
}

// TestAuthBearerSkipsLogin asserts that when api_key is set, GetAuthHeader
// NEVER makes a /login HTTP call. A fake server with a login handler that
// fails the test on invocation proves this.
func TestAuthBearerSkipsLogin(t *testing.T) {
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("bearer mode must not call /login (got %s %s)", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	})
	auth := newAuthWithKeyForServer(t, fs, "rsk_live_silent")

	for i := 0; i < 3; i++ {
		got, err := auth.GetAuthHeader()
		if err != nil {
			t.Fatalf("call %d: GetAuthHeader: %v", i, err)
		}
		if got != "Bearer rsk_live_silent" {
			t.Errorf("call %d: got %q, want %q", i, got, "Bearer rsk_live_silent")
		}
	}
	if n := fs.loginCount.Load(); n != 0 {
		t.Errorf("login count: got %d, want 0 (bearer mode must skip /login)", n)
	}
}

// TestAuthEmptyAPIKeyFallsBackToLegacy verifies that an empty-string
// api_key is treated as "not set" — Auth falls through to the legacy
// username/password flow against /login. Regression guard: trimming or
// presence checks that accept "" as "configured" would break here.
func TestAuthEmptyAPIKeyFallsBackToLegacy(t *testing.T) {
	exp := time.Now().Add(1 * time.Hour).Unix()
	token := makeJWT(t, map[string]any{"exp": exp})
	fs := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	})
	auth := newAuthForServer(t, fs, "u", "p")
	auth.SetAPIKey("") // explicit empty => not set

	got, err := auth.GetAuthHeader()
	if err != nil {
		t.Fatalf("GetAuthHeader: %v", err)
	}
	want := "Bearer " + token
	if got != want {
		t.Errorf("header: got %q, want %q", got, want)
	}
	if n := fs.loginCount.Load(); n != 1 {
		t.Errorf("login count: got %d, want 1 (empty api_key must hit /login)", n)
	}
}

// TestAuthBearerInvalidateIsNoop — Invalidate() in bearer mode must not
// corrupt the api_key. Bearer credentials are the api_key itself; there
// is no cached token to clear, so the next call still returns the same
// header. Uploader calls Invalidate() on 401, so this matters.
func TestAuthBearerInvalidateIsNoop(t *testing.T) {
	auth := NewAuthWithKey("https://example.test", "rsk_live_persistent")

	auth.Invalidate()

	got, err := auth.GetAuthHeader()
	if err != nil {
		t.Fatalf("GetAuthHeader after Invalidate: %v", err)
	}
	if got != "Bearer rsk_live_persistent" {
		t.Errorf("got %q, want %q", got, "Bearer rsk_live_persistent")
	}
}

// TestIsSecureURL — https:// is always secure; http:// only on loopback.
func TestIsSecureURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://ravenbrain.team1310.ca", true},
		{"https://localhost", true},
		{"http://localhost", true},
		{"http://localhost:8787", true},
		{"http://127.0.0.1", true},
		{"http://127.0.0.1:8787", true},
		{"http://[::1]:8787", true},
		{"http://foo.localhost", true},
		{"http://bad.example", false},
		{"http://10.13.10.5", false},
		{"http://192.168.1.1", false},
		{"", false},
		{"not-a-url://x", false},
		{"ftp://localhost", false},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			if got := IsSecureURL(c.url); got != c.want {
				t.Errorf("IsSecureURL(%q) = %v, want %v", c.url, got, c.want)
			}
		})
	}
}

// TestAuthLocalhostHTTPAllowed — http://localhost is a permitted auth
// target so the WPILib sim / wrangler dev Worker works out of the box.
func TestAuthLocalhostHTTPAllowed(t *testing.T) {
	auth := NewAuthWithKey("http://localhost:8787", "rsk_test_local")
	got, err := auth.GetAuthHeader()
	if err != nil {
		t.Fatalf("GetAuthHeader for localhost http: %v", err)
	}
	if got != "Bearer rsk_test_local" {
		t.Errorf("got %q", got)
	}
}

// TestAuthBearer401BacksOff asserts the auth-layer contract that makes the
// uploader's 401-retry-then-backoff loop terminate in bearer mode: after
// Invalidate() (which postJSON calls on 401), a re-fetch of the header
// returns the same bearer token with no /login attempt. The uploader's
// second attempt therefore deterministically fails with 401 again and
// applyBackoff() is invoked — matching today's 401-on-JWT behaviour.
func TestAuthBearer401BacksOff(t *testing.T) {
	auth := NewAuthWithKey("https://example.test", "rsk_live_401")

	h1, err := auth.GetAuthHeader()
	if err != nil {
		t.Fatalf("first GetAuthHeader: %v", err)
	}
	auth.Invalidate() // uploader's 401 path
	h2, err := auth.GetAuthHeader()
	if err != nil {
		t.Fatalf("second GetAuthHeader: %v", err)
	}
	if h1 != h2 || h1 != "Bearer rsk_live_401" {
		t.Errorf("headers: h1=%q h2=%q, want both %q", h1, h2, "Bearer rsk_live_401")
	}
}

