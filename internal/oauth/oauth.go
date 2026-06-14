package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
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

var httpClient = http.DefaultClient

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

func StartCallbackServer(ctx context.Context) (redirectURI string, codeCh <-chan callbackResult, shutdown func(context.Context) error, err error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, nil, err
	}
	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		result := callbackResult{
			Code:  query.Get("code"),
			State: query.Get("state"),
			Error: query.Get("error"),
		}
		if result.Error == "" {
			fmt.Fprint(w, successHTML)
		} else {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<html><body><h1>Baseloop login failed</h1><p>%s</p></body></html>", html.EscapeString(result.Error))
		}
		select {
		case results <- result:
		default:
		}
	})
	go func() {
		_ = server.Serve(listener)
	}()
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	return "http://" + listener.Addr().String() + "/callback", results, server.Shutdown, nil
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

func AuthorizeURL(endpoint, clientID, redirectURI, state, challenge string) string {
	params := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
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

const successHTML = `<html><body><h1>Baseloop login complete</h1><p>You can close this tab and return to the terminal.</p></body></html>`

func WaitForCode(ctx context.Context, codeCh <-chan callbackResult, state string) (string, error) {
	select {
	case result := <-codeCh:
		return result.Validate(state)
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(10 * time.Minute):
		return "", fmt.Errorf("timed out waiting for OAuth callback")
	}
}
