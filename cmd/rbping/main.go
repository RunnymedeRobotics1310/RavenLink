// Package main is a small utility to verify connectivity and auth for
// either upload target (RavenBrain or RavenScope).
//
// RavenBrain mode (default):
//  1. GET /api/ping (anonymous) — verifies the server is reachable
//  2. POST /login — authenticates the telemetry-agent service account
//  3. GET /api/validate — verifies the JWT works for authenticated requests
//
// RavenScope mode (--target ravenscope):
//  1. GET /api/health (anonymous) — verifies the worker is reachable
//  2. GET /api/telemetry/session/__rbping_probe__ — uses the configured
//     bearer API key. A 404 means auth passed (session just doesn't
//     exist); 401/403 means the key is wrong or missing.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
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
	fs := flag.NewFlagSet("rbping", flag.ExitOnError)
	targetName := fs.String("target", "ravenbrain", "Which upload target to probe: ravenbrain or ravenscope")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: rbping [--target ravenbrain|ravenscope] [config.yaml]\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	cfgPath := "config.yaml"
	if fs.NArg() > 0 {
		cfgPath = fs.Arg(0)
	}

	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load %s: %v\n", cfgPath, err)
		os.Exit(1)
	}

	switch *targetName {
	case "ravenbrain":
		pingRavenBrain(cfg)
	case "ravenscope":
		pingRavenScope(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown --target %q (expected ravenbrain or ravenscope)\n", *targetName)
		os.Exit(2)
	}
}

func pingRavenBrain(cfg *config.Config) {
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
		fmt.Printf("      Body:   %s\n", string(loginRespBody))
		fmt.Printf("      ✗ FAIL: login rejected\n\n")

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

	auth := uploader.NewAuth(cfg.RavenBrain.URL, cfg.RavenBrain.Username, cfg.RavenBrain.Password)
	if !auth.IsConfigured() {
		fmt.Printf("      ✗ FAIL: auth not configured\n")
		os.Exit(1)
	}
	header, err := auth.GetAuthHeader()
	if err != nil {
		fmt.Printf("      ✗ FAIL: Auth package rejected: %v\n", err)
		os.Exit(1)
	}

	token := ""
	if len(header) > 7 && header[:7] == "Bearer " {
		token = header[7:]
	}
	fmt.Printf("      Token:   %s...%s (len=%d)\n", token[:min(16, len(token))], token[max(0, len(token)-8):], len(token))

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

func pingRavenScope(cfg *config.Config) {
	if cfg.RavenScope.URL == "" {
		fmt.Fprintln(os.Stderr, "ravenscope.url is empty in config")
		os.Exit(1)
	}
	if cfg.RavenScope.APIKey == "" {
		fmt.Fprintln(os.Stderr, "ravenscope.api_key is empty in config")
		os.Exit(1)
	}

	fmt.Printf("=== RavenScope Connectivity Test ===\n")
	fmt.Printf("URL:     %s\n", cfg.RavenScope.URL)
	fmt.Printf("API key: [set, %d chars]\n", len(cfg.RavenScope.APIKey))
	fmt.Printf("\n")

	client := &http.Client{Timeout: 15 * time.Second}

	fmt.Printf("[1/2] GET %s/api/health ...\n", cfg.RavenScope.URL)
	healthResp, err := client.Get(cfg.RavenScope.URL + "/api/health")
	if err != nil {
		fmt.Printf("      ✗ FAIL: %v\n", err)
		os.Exit(1)
	}
	healthBody, _ := io.ReadAll(healthResp.Body)
	healthResp.Body.Close()
	fmt.Printf("      Status: %d %s\n", healthResp.StatusCode, healthResp.Status)
	fmt.Printf("      Body:   %q\n", string(healthBody))
	if healthResp.StatusCode != 200 {
		fmt.Printf("      ✗ FAIL: expected 200\n")
		os.Exit(1)
	}
	fmt.Printf("      ✓ OK — worker is reachable\n\n")

	// Build auth the same way the uploader does.
	auth := uploader.NewAuthWithKey(cfg.RavenScope.URL, cfg.RavenScope.APIKey)
	if !auth.IsConfigured() {
		fmt.Printf("      ✗ FAIL: auth not configured\n")
		os.Exit(1)
	}
	header, err := auth.GetAuthHeader()
	if err != nil {
		fmt.Printf("      ✗ FAIL: Auth package rejected: %v\n", err)
		os.Exit(1)
	}

	// Probe an authenticated route. 404 = auth accepted, session missing
	// (expected). 401/403 = auth rejected. Anything else is suspicious.
	probePath := "/api/telemetry/session/__rbping_probe__"
	fmt.Printf("[2/2] GET %s%s (with Bearer api_key) ...\n", cfg.RavenScope.URL, probePath)
	req, _ := http.NewRequest("GET", cfg.RavenScope.URL+probePath, nil)
	req.Header.Set("Authorization", header)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("      ✗ FAIL: %v\n", err)
		os.Exit(1)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("      Status: %d %s\n", resp.StatusCode, resp.Status)
	switch {
	case resp.StatusCode == 404:
		fmt.Printf("      ✓ OK — auth accepted (404 on a nonexistent session id is the expected success signal)\n\n")
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		fmt.Printf("      Body:   %s\n", string(respBody))
		fmt.Printf("      ✗ FAIL: auth rejected (check ravenscope.api_key)\n")
		os.Exit(1)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		// Unexpected success — probably means __rbping_probe__ really
		// does exist. Still a pass for auth purposes.
		fmt.Printf("      ✓ OK — auth accepted\n\n")
	default:
		fmt.Printf("      Body:   %s\n", string(respBody))
		fmt.Printf("      ✗ FAIL: unexpected status\n")
		os.Exit(1)
	}

	fmt.Printf("=== All checks passed — RavenScope auth is working ===\n")
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
