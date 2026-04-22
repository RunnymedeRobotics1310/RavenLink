// Package main is a small utility to verify RavenBrain connectivity and auth.
//
// It reads config.yaml, then:
//  1. GET /api/ping (anonymous) — verifies the server is reachable
//  2. POST /login — authenticates the telemetry-agent service account
//  3. GET /api/validate — verifies the JWT works for authenticated requests
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/config"
	"github.com/RunnymedeRobotics1310/RavenLink/internal/uploader"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load %s: %v\n", cfgPath, err)
		os.Exit(1)
	}

	if cfg.RavenBrain.URL == "" {
		fmt.Fprintln(os.Stderr, "ravenbrain.url is empty in config")
		os.Exit(1)
	}

	fmt.Printf("=== RavenBrain Connectivity Test ===\n")
	fmt.Printf("URL:      %s\n", cfg.RavenBrain.URL)
	fmt.Printf("Username: %s\n", cfg.RavenBrain.Username)
	fmt.Printf("Password: [set, %d chars]\n", len(cfg.RavenBrain.Password))
	fmt.Printf("\n")

	client := &http.Client{Timeout: 15 * time.Second}

	// Step 1: GET /api/ping (anonymous, plain text response)
	fmt.Printf("[1/3] GET %s/api/ping ...\n", cfg.RavenBrain.URL)
	pingResp, err := client.Get(cfg.RavenBrain.URL + "/api/ping")
	if err != nil {
		fmt.Printf("      ✗ FAIL: %v\n", err)
		os.Exit(1)
	}
	pingBody, _ := io.ReadAll(pingResp.Body)
	pingResp.Body.Close()
	fmt.Printf("      Status:  %d %s\n", pingResp.StatusCode, pingResp.Status)
	fmt.Printf("      Version: %s\n", pingResp.Header.Get("X-RavenBrain-Version"))
	fmt.Printf("      Body:    %q\n", string(pingBody))
	if pingResp.StatusCode != 200 {
		fmt.Printf("      ✗ FAIL: expected 200\n")
		os.Exit(1)
	}
	fmt.Printf("      ✓ OK — server is reachable\n\n")

	// Step 2: POST /login — first directly for diagnostics, then via Auth package
	fmt.Printf("[2/3] POST %s/login ...\n", cfg.RavenBrain.URL)
	loginBody, _ := json.Marshal(map[string]string{
		"username": cfg.RavenBrain.Username,
		"password": cfg.RavenBrain.Password,
	})
	loginResp, err := client.Post(cfg.RavenBrain.URL+"/login", "application/json", bytesReader(loginBody))
	if err != nil {
		fmt.Printf("      ✗ FAIL: %v\n", err)
		os.Exit(1)
	}
	loginRespBody, _ := io.ReadAll(loginResp.Body)
	loginResp.Body.Close()
	fmt.Printf("      Status: %d %s\n", loginResp.StatusCode, loginResp.Status)
	if loginResp.StatusCode != 200 {
		// Show error body only on failure (no token to leak)
		fmt.Printf("      Body:   %s\n", string(loginRespBody))
		fmt.Printf("      ✗ FAIL: login rejected\n\n")

		// Fallback: try Basic Auth against /api/validate (what RavenBrain tests use)
		fmt.Printf("[2b]  Fallback: Basic Auth on /api/validate ...\n")
		validateReq, _ := http.NewRequest("GET", cfg.RavenBrain.URL+"/api/validate", nil)
		validateReq.SetBasicAuth(cfg.RavenBrain.Username, cfg.RavenBrain.Password)
		basicResp, err := client.Do(validateReq)
		if err != nil {
			fmt.Printf("      ✗ FAIL: %v\n", err)
		} else {
			io.Copy(io.Discard, basicResp.Body)
			basicResp.Body.Close()
			fmt.Printf("      Status: %d\n", basicResp.StatusCode)
			if basicResp.StatusCode == 200 {
				fmt.Printf("      ✓ Basic Auth works!\n")
				fmt.Printf("      → The credentials ARE correct, but POST /login is rejecting them.\n")
				fmt.Printf("        This could be a Micronaut Security config issue where the\n")
				fmt.Printf("        login endpoint uses a different authenticator than the\n")
				fmt.Printf("        built-in PresharedKeyAuthenticationProvider.\n")
			}
		}
		os.Exit(1)
	}

	// Also verify the Auth package works (same code path as uploader uses)
	auth := uploader.NewAuth(cfg.RavenBrain.URL, cfg.RavenBrain.Username, cfg.RavenBrain.Password)
	auth.SetAPIKey(cfg.RavenBrain.APIKey)
	if !auth.IsConfigured() {
		fmt.Printf("      ✗ FAIL: auth not configured\n")
		os.Exit(1)
	}
	header, err := auth.GetAuthHeader()
	if err != nil {
		fmt.Printf("      ✗ FAIL: Auth package rejected: %v\n", err)
		os.Exit(1)
	}

	// The auth header is "Bearer <jwt>" — extract token for display
	token := ""
	if len(header) > 7 && header[:7] == "Bearer " {
		token = header[7:]
	}
	fmt.Printf("      Token:   %s...%s (len=%d)\n", token[:min(16, len(token))], token[max(0, len(token)-8):], len(token))

	// Decode JWT payload to show expiry
	if parts := splitJWT(token); len(parts) == 3 {
		if payload, err := decodeJWTPayload(parts[1]); err == nil {
			if exp, ok := payload["exp"].(float64); ok {
				expTime := time.Unix(int64(exp), 0)
				fmt.Printf("      Expires: %s (in %s)\n", expTime.Format(time.RFC3339), time.Until(expTime).Round(time.Second))
			}
			if sub, ok := payload["sub"].(string); ok {
				fmt.Printf("      Subject: %s\n", sub)
			}
			if roles, ok := payload["roles"].([]any); ok {
				fmt.Printf("      Roles:   %v\n", roles)
			}
		}
	}
	fmt.Printf("      ✓ OK — login succeeded\n\n")

	// Step 3: GET /api/validate with JWT — verifies authenticated requests work
	fmt.Printf("[3/3] GET %s/api/validate (with Bearer token) ...\n", cfg.RavenBrain.URL)
	req, _ := http.NewRequest("GET", cfg.RavenBrain.URL+"/api/validate", nil)
	req.Header.Set("Authorization", header)
	valResp, err := client.Do(req)
	if err != nil {
		fmt.Printf("      ✗ FAIL: %v\n", err)
		os.Exit(1)
	}
	io.Copy(io.Discard, valResp.Body)
	valResp.Body.Close()
	fmt.Printf("      Status: %d %s\n", valResp.StatusCode, valResp.Status)
	if valResp.StatusCode != 200 {
		fmt.Printf("      ✗ FAIL: expected 200\n")
		os.Exit(1)
	}
	fmt.Printf("      ✓ OK — authenticated request succeeded\n\n")

	fmt.Printf("=== All checks passed — RavenBrain auth is working ===\n")
}

func splitJWT(token string) []string {
	parts := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	return parts
}

func decodeJWTPayload(segment string) (map[string]any, error) {
	// JWT uses base64url without padding; RawURLEncoding handles unpadded input.
	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}
