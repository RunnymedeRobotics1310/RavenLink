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

