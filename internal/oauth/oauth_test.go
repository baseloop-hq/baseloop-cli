package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerBaseURL(t *testing.T) {
	got := ServerBaseURL("https://api.example.com/v1/cli")
	if got != "https://api.example.com" {
		t.Fatalf("expected server base URL, got %q", got)
	}

	got = ServerBaseURL("https://api.example.com/v1/cli/")
	if got != "https://api.example.com" {
		t.Fatalf("expected trailing slash to be stripped, got %q", got)
	}
}

func TestManualRedirectURI(t *testing.T) {
	got := ManualRedirectURI("https://api.example.com/v1/cli")
	if got != "https://api.example.com/cli/callback" {
		t.Fatalf("expected hosted callback URI, got %q", got)
	}
}

func TestParsePastedCode(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "bare code", input: "abc123", want: "abc123"},
		{name: "bare code with whitespace", input: "  abc123\n", want: "abc123"},
		{name: "full callback URL", input: "https://api.example.com/cli/callback?code=abc123&state=xyz", want: "abc123"},
		{name: "query string only", input: "?code=abc123&state=xyz", want: "abc123"},
		{name: "empty", input: "\n", wantErr: true},
		{name: "URL without code", input: "https://api.example.com/cli/callback?error=denied", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePastedCode(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestRegisterClientSendsJSONPublicClient(t *testing.T) {
	originalClient := httpClient
	t.Cleanup(func() { httpClient = originalClient })
	httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("expected application/json, got %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["token_endpoint_auth_method"] != "none" {
			t.Fatalf("expected public client auth method, got %#v", body["token_endpoint_auth_method"])
		}
		if body["client_name"] != "Baseloop CLI" {
			t.Fatalf("expected client_name, got %#v", body["client_name"])
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"client_id":"baseloop-cli"}`)),
			Request:    r,
		}, nil
	})}

	registration, err := RegisterClient(context.Background(), "https://api.example.com/register", "http://127.0.0.1:1234/callback")
	if err != nil {
		t.Fatal(err)
	}
	if registration.ClientID != "baseloop-cli" {
		t.Fatalf("expected client id, got %q", registration.ClientID)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func TestPKCEPair(t *testing.T) {
	verifier, challenge, err := PKCEPair()
	if err != nil {
		t.Fatal(err)
	}
	if verifier == "" || challenge == "" {
		t.Fatalf("expected verifier and challenge")
	}
	if verifier == challenge {
		t.Fatalf("expected verifier and challenge to differ")
	}
}

func TestAuthorizeURLIncludesSignupHint(t *testing.T) {
	got := AuthorizeURL("https://api.example.com/authorize", "client-id", "http://127.0.0.1/callback", "state", "challenge", true)

	if !strings.Contains(got, "signup=1") {
		t.Fatalf("expected signup hint in authorize URL, got %q", got)
	}
}

// startPromptTestHandler builds the real loopback HTTP surface without opening
// a socket, keeping these tests independent of host network permissions.
func startPromptTestHandler(t *testing.T, nonce, origin, workflowBase string) (http.Handler, string, string, <-chan callbackResult, <-chan string) {
	t.Helper()
	const hostAddr = "127.0.0.1:43123"
	handler, codeCh, promptCh, err := callbackServerHandler(hostAddr, CallbackServerOptions{
		WorkflowBaseURL: workflowBase,
		PromptNonce:     nonce,
		AllowedOrigin:   origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, "http://" + hostAddr, hostAddr, codeCh, promptCh
}

func serveTestRequest(t *testing.T, handler http.Handler, req *http.Request) *http.Response {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	res := recorder.Result()
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func postPrompt(t *testing.T, handler http.Handler, target, origin, body string, mutate func(*http.Request)) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if mutate != nil {
		mutate(req)
	}
	return serveTestRequest(t, handler, req)
}

func TestPromptEndpointAcceptsValidNonceOnce(t *testing.T) {
	origin := "https://app.example.com"
	handler, base, _, _, prompts := startPromptTestHandler(t, "nonce-123", origin, origin+"/cli/workflows")

	res := postPrompt(t, handler, base+"/prompt", origin, `{"nonce":"nonce-123","prompt":"/baseloop do the thing"}`, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("expected CORS origin %q, got %q", origin, got)
	}
	select {
	case prompt := <-prompts:
		if prompt != "/baseloop do the thing" {
			t.Fatalf("unexpected prompt %q", prompt)
		}
	case <-time.After(time.Second):
		t.Fatal("prompt was not delivered")
	}

	res = postPrompt(t, handler, base+"/prompt", origin, `{"nonce":"nonce-123","prompt":"again"}`, nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 on nonce reuse, got %d", res.StatusCode)
	}
}

func TestPromptEndpointRejectsBadNonceOriginAndHost(t *testing.T) {
	origin := "https://app.example.com"
	handler, base, _, _, prompts := startPromptTestHandler(t, "nonce-123", origin, origin+"/cli/workflows")

	res := postPrompt(t, handler, base+"/prompt", origin, `{"nonce":"wrong","prompt":"x"}`, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for bad nonce, got %d", res.StatusCode)
	}

	res = postPrompt(t, handler, base+"/prompt", "https://evil.example.com", `{"nonce":"nonce-123","prompt":"x"}`, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for bad origin, got %d", res.StatusCode)
	}

	res = postPrompt(t, handler, base+"/prompt", "", `{"nonce":"nonce-123","prompt":"x"}`, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for missing origin, got %d", res.StatusCode)
	}

	// DNS rebinding: correct IP, attacker-controlled Host header.
	res = postPrompt(t, handler, base+"/prompt", origin, `{"nonce":"nonce-123","prompt":"x"}`, func(r *http.Request) {
		r.Host = "evil.example.com"
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for rebound host, got %d", res.StatusCode)
	}

	select {
	case prompt := <-prompts:
		t.Fatalf("no prompt should have been delivered, got %q", prompt)
	default:
	}
}

func TestPromptEndpointPreflightAndLimits(t *testing.T) {
	origin := "https://app.example.com"
	handler, base, _, _, _ := startPromptTestHandler(t, "nonce-123", origin, origin+"/cli/workflows")

	req := httptest.NewRequest(http.MethodOptions, base+"/prompt", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Private-Network", "true")
	res := serveTestRequest(t, handler, req)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 preflight, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("expected PNA header, got %q", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); got != "content-type" {
		t.Fatalf("expected allow-headers, got %q", got)
	}

	oversize := `{"nonce":"nonce-123","prompt":"` + strings.Repeat("a", 70<<10) + `"}`
	res = postPrompt(t, handler, base+"/prompt", origin, oversize, nil)
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversize body, got %d", res.StatusCode)
	}

	res = postPrompt(t, handler, base+"/prompt", origin, `{"nonce":"nonce-123","prompt":"   "}`, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty prompt, got %d", res.StatusCode)
	}
}

func TestCallbackRedirectsToWorkflowPage(t *testing.T) {
	origin := "https://app.example.com"
	handler, base, hostAddr, codes, _ := startPromptTestHandler(t, "nonce-123", origin, origin+"/cli/workflows")

	res := serveTestRequest(t, handler, httptest.NewRequest(http.MethodGet, base+"/callback?code=abc&state=s", nil))
	if res.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", res.StatusCode)
	}
	loc, err := res.Location()
	if err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(hostAddr, "127.0.0.1:")
	if loc.Query().Get("cb") != port {
		t.Fatalf("expected cb=%s, got %q", port, loc.Query().Get("cb"))
	}
	if loc.Query().Get("nonce") != "nonce-123" {
		t.Fatalf("expected nonce in redirect, got %q", loc.Query().Get("nonce"))
	}
	if !strings.HasPrefix(loc.String(), origin+"/cli/workflows?") {
		t.Fatalf("unexpected redirect target %q", loc.String())
	}

	// The redirect must not swallow the OAuth code: the callback result still
	// has to reach the waiting login flow.
	select {
	case result := <-codes:
		if result.Code != "abc" || result.State != "s" {
			t.Fatalf("unexpected callback result %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("callback result was not delivered")
	}

	// Error callbacks keep the branded page even in redirect mode.
	res2 := serveTestRequest(t, handler, httptest.NewRequest(http.MethodGet, base+"/callback?error=access_denied", nil))
	if res2.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for error callback, got %d", res2.StatusCode)
	}
}

func TestCallbackKeepsBrandedPageWithoutWorkflowURL(t *testing.T) {
	const base = "http://127.0.0.1:43123"
	handler, _, _, err := callbackServerHandler("127.0.0.1:43123", CallbackServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res := serveTestRequest(t, handler, httptest.NewRequest(http.MethodGet, base+"/callback?code=abc&state=s", nil))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 branded page, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "Login complete") {
		t.Fatal("expected branded success page")
	}
}

func TestPromptEndpointRequiresAllowedOrigin(t *testing.T) {
	// A nonce without an allowed origin must not open the endpoint: an empty
	// AllowedOrigin would let no-Origin (non-browser) requests through the
	// exact-match check.
	const hostAddr = "127.0.0.1:43123"
	handler, _, _, err := callbackServerHandler(hostAddr, CallbackServerOptions{
		WorkflowBaseURL: "https://app.example.com/cli/workflows",
		PromptNonce:     "nonce-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := postPrompt(t, handler, "http://"+hostAddr+"/prompt", "", `{"nonce":"nonce-123","prompt":"x"}`, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when AllowedOrigin is unset, got %d", res.StatusCode)
	}
}

func TestWaitForPromptTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	prompts := make(chan string)
	if _, err := WaitForPrompt(ctx, prompts); err == nil {
		t.Fatal("expected timeout error")
	}
}
