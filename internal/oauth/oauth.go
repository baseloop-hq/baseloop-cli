package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Metadata struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

type ClientRegistration struct {
	ClientID string `json:"client_id"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func ServerBaseURL(apiURL string) string {
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return strings.TrimRight(apiURL, "/")
	}
	parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), "/v1/cli")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func Discover(ctx context.Context, apiURL string) (Metadata, error) {
	base := ServerBaseURL(apiURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/.well-known/oauth-authorization-server", nil)
	if err != nil {
		return Metadata{}, err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return Metadata{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Metadata{}, fmt.Errorf("OAuth discovery returned HTTP %d", res.StatusCode)
	}
	var metadata Metadata
	if err := json.NewDecoder(res.Body).Decode(&metadata); err != nil {
		return Metadata{}, err
	}
	if metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" || metadata.RegistrationEndpoint == "" {
		return Metadata{}, fmt.Errorf("OAuth metadata is missing required endpoints")
	}
	return metadata, nil
}

func RegisterClient(ctx context.Context, endpoint, redirectURI string) (ClientRegistration, error) {
	payload, err := json.Marshal(map[string]any{
		"client_name":                "Baseloop CLI",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return ClientRegistration{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return ClientRegistration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		return ClientRegistration{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ClientRegistration{}, fmt.Errorf("OAuth registration returned HTTP %d", res.StatusCode)
	}
	var registration ClientRegistration
	if err := json.NewDecoder(res.Body).Decode(&registration); err != nil {
		return ClientRegistration{}, err
	}
	if registration.ClientID == "" {
		return ClientRegistration{}, fmt.Errorf("OAuth registration did not return client_id")
	}
	return registration, nil
}

// CallbackServerOptions configures the loopback server beyond the bare OAuth
// callback: an optional post-login browser handoff to the web app, and the
// /prompt endpoint that receives a workflow prompt picked in the browser.
type CallbackServerOptions struct {
	// WorkflowBaseURL, when non-empty, is the web-app page the browser is sent
	// to (302) after a successful OAuth callback, instead of the branded page.
	// The server appends its own port and the nonce (?cb=<port>&nonce=<nonce>)
	// since the port is only known once the listener binds. Error callbacks
	// always render the branded error page.
	WorkflowBaseURL string
	// PromptNonce guards POST /prompt; the endpoint is not registered when
	// empty. The nonce is single-use.
	PromptNonce string
	// AllowedOrigin is the exact web-app origin permitted to call /prompt.
	AllowedOrigin string
}

func StartCallbackServer(ctx context.Context, opts CallbackServerOptions) (redirectURI string, codeCh <-chan callbackResult, promptCh <-chan string, shutdown func(context.Context) error, err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, nil, err
	}
	hostAddr := listener.Addr().String()
	successRedirect := ""
	if opts.WorkflowBaseURL != "" && opts.PromptNonce != "" {
		_, port, splitErr := net.SplitHostPort(hostAddr)
		if splitErr != nil {
			_ = listener.Close()
			return "", nil, nil, nil, splitErr
		}
		successRedirect = fmt.Sprintf("%s?cb=%s&nonce=%s", opts.WorkflowBaseURL, port, url.QueryEscape(opts.PromptNonce))
	}
	results := make(chan callbackResult, 1)
	prompts := make(chan string, 1)
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		result := callbackResult{
			Code:  query.Get("code"),
			State: query.Get("state"),
			Error: query.Get("error"),
		}
		if result.Error == "" && successRedirect != "" {
			http.Redirect(w, r, successRedirect, http.StatusFound)
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if result.Error == "" {
				fmt.Fprint(w, callbackPage("Login complete", "You can close this tab and return to the terminal."))
			} else {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, callbackPage("Login failed", html.EscapeString(result.Error)))
			}
		}
		select {
		case results <- result:
		default:
		}
	})
	if opts.PromptNonce != "" {
		mux.HandleFunc("/prompt", promptHandler(hostAddr, opts, prompts))
	}
	go func() {
		_ = server.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	return "http://" + hostAddr + "/callback", results, prompts, server.Shutdown, nil
}

// promptHandler accepts the workflow prompt picked in the browser. Everything
// is enforced server-side — the CORS headers exist so the legitimate web-app
// fetch can read the response, they are not the defense:
//   - Host must equal the listener address exactly (DNS-rebinding defense).
//   - Origin must equal the allowed web-app origin exactly.
//   - The nonce is compared in constant time and consumed on first use.
//   - Bodies are capped at 64 KB.
//
// Launching an agent with the received prompt is still gated behind an
// explicit confirmation in the terminal (see cli.oauthLogin).
func promptHandler(hostAddr string, opts CallbackServerOptions, prompts chan<- string) http.HandlerFunc {
	var mu sync.Mutex
	nonceUsed := false
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host != hostAddr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Header.Get("Origin") != opts.AllowedOrigin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", opts.AllowedOrigin)
		w.Header().Set("Vary", "Origin")
		switch r.Method {
		case http.MethodOptions:
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "content-type")
			// Chrome's Private Network Access requires this on preflights for
			// public→loopback requests.
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				w.Header().Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			var payload struct {
				Nonce  string `json:"nonce"`
				Prompt string `json:"prompt"`
			}
			body := http.MaxBytesReader(w, r.Body, 64<<10)
			if err := json.NewDecoder(body).Decode(&payload); err != nil {
				var maxBytesErr *http.MaxBytesError
				if errors.As(err, &maxBytesErr) {
					http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "invalid request", http.StatusBadRequest)
				}
				return
			}
			if subtle.ConstantTimeCompare([]byte(payload.Nonce), []byte(opts.PromptNonce)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			prompt := strings.TrimSpace(payload.Prompt)
			if prompt == "" {
				http.Error(w, "empty prompt", http.StatusBadRequest)
				return
			}
			mu.Lock()
			if nonceUsed {
				mu.Unlock()
				http.Error(w, "already used", http.StatusConflict)
				return
			}
			nonceUsed = true
			mu.Unlock()
			select {
			case prompts <- prompt:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// WaitForPrompt blocks until the browser posts a workflow prompt or the
// context ends (timeout or Ctrl-C).
func WaitForPrompt(ctx context.Context, prompts <-chan string) (string, error) {
	select {
	case prompt := <-prompts:
		return prompt, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type callbackResult struct {
	Code  string
	State string
	Error string
}

func (r callbackResult) Validate(expectedState string) (string, error) {
	if r.Error != "" {
		return "", fmt.Errorf("OAuth error: %s", r.Error)
	}
	if r.State != expectedState {
		return "", fmt.Errorf("OAuth state mismatch")
	}
	if r.Code == "" {
		return "", fmt.Errorf("OAuth callback did not include a code")
	}
	return r.Code, nil
}

func ExchangeCode(ctx context.Context, endpoint, clientID, redirectURI, code, verifier string) (TokenResponse, error) {
	return tokenRequest(ctx, endpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"code":          {code},
		"code_verifier": {verifier},
	})
}

func Refresh(ctx context.Context, endpoint, clientID, refreshToken string) (TokenResponse, error) {
	return tokenRequest(ctx, endpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {refreshToken},
	})
}

func tokenRequest(ctx context.Context, endpoint string, form url.Values) (TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		return TokenResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return TokenResponse{}, fmt.Errorf("OAuth token endpoint returned HTTP %d", res.StatusCode)
	}
	var token TokenResponse
	if err := json.NewDecoder(res.Body).Decode(&token); err != nil {
		return TokenResponse{}, err
	}
	if token.AccessToken == "" {
		return TokenResponse{}, fmt.Errorf("OAuth token endpoint did not return access_token")
	}
	return token, nil
}

func PKCEPair() (verifier string, challenge string, err error) {
	verifier, err = RandomURLSafe(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func RandomURLSafe(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func AuthorizeURL(endpoint, clientID, redirectURI, state, challenge string, signup bool) string {
	params := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if signup {
		params.Set("signup", "1")
	}
	return endpoint + "?" + params.Encode()
}

func OpenBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

// Branded callback page, matching the app's auth pages (warm cream background,
// ink text, Baseloop glyph). Fully self-contained — the page is served from the
// CLI's loopback listener, so it must not fetch fonts or assets from anywhere.
const callbackPageTemplate = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Baseloop</title>
<style>
  body {
    margin: 0;
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    background: #fafaf9;
    color: #201515;
    font-family: Geist, ui-sans-serif, -apple-system, "Segoe UI", Helvetica, Arial, sans-serif;
    -webkit-font-smoothing: antialiased;
  }
  main {
    text-align: center;
    padding: 48px 24px;
    max-width: 420px;
  }
  .logo {
    width: 48px;
    height: 48px;
    margin: 0 auto 20px;
    border-radius: 12px;
    background: #201515;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  h1 {
    font-size: 20px;
    font-weight: 600;
    letter-spacing: -0.02em;
    margin: 0 0 8px;
  }
  p {
    font-size: 14px;
    line-height: 1.55;
    color: #706d68;
    margin: 0;
  }
</style>
</head>
<body>
<main>
  <div class="logo">
    <svg width="32" height="32" viewBox="0 0 20 20" fill="none" xmlns="http://www.w3.org/2000/svg">
      <path d="M6.97898 13.5247C6.5146 13.5247 6.22797 12.9943 6.46727 12.5779L9.49189 7.31401C9.72391 6.91021 10.2833 6.91021 10.5153 7.31401L13.5399 12.5779C13.7792 12.9943 13.4926 13.5247 13.0282 13.5247H6.97898ZM12.5155 5.47138C12.2091 4.92259 11.4279 4 10.0036 4C8.69086 4 7.90379 4.77742 7.58517 5.31856C6.49909 7.16298 3.85424 11.7979 3.08378 13.1494C2.95471 13.3758 2.97663 13.6605 3.13669 13.8641L5.42015 16.7686C5.632 17.0381 6.01236 17.0713 6.28671 16.8729C7.09096 16.2915 8.33417 15.6857 10.0241 15.6857C11.6581 15.6857 12.8754 16.2516 13.6798 16.813C13.9493 17.0011 14.3162 16.9691 14.527 16.7117L16.856 13.8684C17.024 13.6634 17.0474 13.369 16.9136 13.138C16.1375 11.7978 13.5544 7.33182 12.5155 5.47138Z" fill="#F9FAFB"/>
    </svg>
  </div>
  <h1>%s</h1>
  <p>%s</p>
</main>
</body>
</html>`

func callbackPage(title, message string) string {
	return fmt.Sprintf(callbackPageTemplate, title, message)
}

// WaitForCode blocks until the OAuth callback delivers a code or the caller's
// context ends. The wait budget is the caller's to set: a plain login is
// quick, but a signup flow holds this open while a human creates an account,
// verifies an email, and completes onboarding.
func WaitForCode(ctx context.Context, codeCh <-chan callbackResult, state string) (string, error) {
	select {
	case result := <-codeCh:
		return result.Validate(state)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("timed out waiting for OAuth callback")
		}
		return "", ctx.Err()
	}
}
