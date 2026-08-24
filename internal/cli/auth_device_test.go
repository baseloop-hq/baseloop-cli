package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baseloop-hq/baseloop-cli/internal/config"
)

func TestAuthLoginDeviceFlow(t *testing.T) {
	var tokenForm url.Values
	var startBody map[string]string
	var polls atomic.Int32
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": server.URL + "/authorize",
			"token_endpoint":         server.URL + "/token",
			"registration_endpoint":  server.URL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "test-client"})
	})
	mux.HandleFunc("/cli/device-auth/start", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&startBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-secret",
			"user_code":                 "BCDF-GHJK",
			"verification_uri":          "http://localhost:8910/cli/auth",
			"verification_uri_complete": "http://localhost:8910/cli/auth?code=BCDF-GHJK",
			"expires_in":                600,
			"interval":                  1,
		})
	})
	mux.HandleFunc("/cli/device-auth/poll", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["device_code"] != "device-secret" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "expired"})
			return
		}
		if polls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending", "interval": 1})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":       "approved",
			"code":         "approved-code",
			"redirect_uri": server.URL + "/cli/device-auth/callback",
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tokenForm, _ = url.ParseQuery(string(body))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-1",
			"refresh_token": "rt-1",
			"token_type":    "bearer",
			"expires_in":    3600,
		})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	var out bytes.Buffer
	code := Run([]string{"auth", "login", "--device", "--no-browser", "--api-url", server.URL, "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Your one-time code: BCDF-GHJK") {
		t.Errorf("expected the user code to be printed, got %s", out.String())
	}
	if !strings.Contains(out.String(), "http://localhost:8910/cli/auth?code=BCDF-GHJK") {
		t.Errorf("expected the approval URL to be printed, got %s", out.String())
	}
	if !strings.Contains(out.String(), `"mode": "device"`) {
		t.Errorf("expected device mode in the success payload, got %s", out.String())
	}
	if startBody["code_challenge"] == "" || startBody["machine_name"] == "" {
		t.Errorf("expected PKCE challenge and machine name at start, got %v", startBody)
	}
	if got := polls.Load(); got < 2 {
		t.Errorf("expected the CLI to keep polling while pending, got %d polls", got)
	}
	if got := tokenForm.Get("code"); got != "approved-code" {
		t.Errorf("expected approved code in token exchange, got %q", got)
	}
	if got := tokenForm.Get("redirect_uri"); got != server.URL+"/cli/device-auth/callback" {
		t.Errorf("expected device callback redirect URI in token exchange, got %q", got)
	}
	if tokenForm.Get("code_verifier") == "" {
		t.Error("expected PKCE code_verifier in token exchange")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load failed: %v", err)
	}
	if cfg.OAuth.AccessToken != "at-1" || cfg.OAuth.RefreshToken != "rt-1" {
		t.Errorf("expected OAuth tokens saved, got %+v", cfg.OAuth)
	}
}

func TestAuthLoginDeviceRejectsConflictingFlows(t *testing.T) {
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	for _, extra := range []string{"--manual", "--signup"} {
		var out bytes.Buffer
		code := Run([]string{"auth", "login", "--device", extra, "--json"}, &out, &out)
		if code != 2 {
			t.Errorf("--device %s: expected exit 2, got %d: %s", extra, code, out.String())
		}
		if !strings.Contains(out.String(), "USAGE") {
			t.Errorf("--device %s: expected USAGE error, got %s", extra, out.String())
		}
	}
}

func TestAuthLoginDeviceFlowDenied(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": server.URL + "/authorize",
			"token_endpoint":         server.URL + "/token",
			"registration_endpoint":  server.URL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "test-client"})
	})
	mux.HandleFunc("/cli/device-auth/start", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "device-secret",
			"user_code":                 "BCDF-GHJK",
			"verification_uri":          "http://localhost:8910/cli/auth",
			"verification_uri_complete": "http://localhost:8910/cli/auth?code=BCDF-GHJK",
			"expires_in":                600,
			"interval":                  1,
		})
	})
	mux.HandleFunc("/cli/device-auth/poll", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "denied", "error": "access_denied"})
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))

	var out bytes.Buffer
	code := Run([]string{"auth", "login", "--device", "--no-browser", "--api-url", server.URL, "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "OAUTH_DEVICE_APPROVAL_FAILED") || !strings.Contains(out.String(), "cancelled") {
		t.Errorf("expected a cancelled approval error, got %s", out.String())
	}
}
