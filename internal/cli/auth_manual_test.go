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
	"testing"

	"github.com/baseloop-hq/baseloop-cli/internal/config"
)

func TestAuthLoginManualPasteCode(t *testing.T) {
	var tokenForm url.Values
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
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		uris, _ := payload["redirect_uris"].([]any)
		if len(uris) != 1 || uris[0] != server.URL+"/cli/callback" {
			t.Errorf("expected hosted callback redirect URI, got %v", payload["redirect_uris"])
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "test-client"})
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
	oldStdin := stdin
	// Paste the full callback URL, the forgiving path a user hits when the
	// hosted page is unavailable and they copy the address bar instead.
	stdin = strings.NewReader(server.URL + "/cli/callback?code=pasted-code&state=xyz\n")
	t.Cleanup(func() { stdin = oldStdin })

	var out bytes.Buffer
	code := Run([]string{"auth", "login", "--manual", "--api-url", server.URL, "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), server.URL+"/authorize?") {
		t.Errorf("expected the login URL to be printed, got %s", out.String())
	}
	if got := tokenForm.Get("code"); got != "pasted-code" {
		t.Errorf("expected pasted code in token exchange, got %q", got)
	}
	if got := tokenForm.Get("redirect_uri"); got != server.URL+"/cli/callback" {
		t.Errorf("expected hosted callback redirect URI in token exchange, got %q", got)
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

func TestAuthLoginManualRejectsEmptyCode(t *testing.T) {
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
	server = httptest.NewServer(mux)
	defer server.Close()

	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	oldStdin := stdin
	stdin = strings.NewReader("\n")
	t.Cleanup(func() { stdin = oldStdin })

	var out bytes.Buffer
	code := Run([]string{"auth", "login", "--manual", "--api-url", server.URL, "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "OAUTH_CALLBACK_FAILED") {
		t.Errorf("expected OAUTH_CALLBACK_FAILED, got %s", out.String())
	}
}
