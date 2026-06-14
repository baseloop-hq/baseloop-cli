package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
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
