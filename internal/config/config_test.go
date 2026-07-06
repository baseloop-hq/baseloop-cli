package config

import "testing"

func TestNormalizeAPIURLAddsCliToV1Root(t *testing.T) {
	got := NormalizeAPIURL("https://api-v2.baseloop.io/v1/")
	want := "https://api-v2.baseloop.io/v1/cli"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizeAPIURLLeavesCliEndpoint(t *testing.T) {
	got := NormalizeAPIURL("https://api-v2.baseloop.io/v1/cli/")
	want := "https://api-v2.baseloop.io/v1/cli"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWebURLResolution(t *testing.T) {
	t.Setenv("BASELOOP_WEB_URL", "")

	// Env override wins.
	t.Setenv("BASELOOP_WEB_URL", "http://localhost:3000/")
	if got := WebURL(Config{APIURL: DefaultAPIURL}); got != "http://localhost:3000" {
		t.Fatalf("expected env override, got %q", got)
	}
	t.Setenv("BASELOOP_WEB_URL", "")

	// Stored value next.
	if got := WebURL(Config{APIURL: DefaultAPIURL, WebURL: "https://staging.baseloop.io"}); got != "https://staging.baseloop.io" {
		t.Fatalf("expected stored value, got %q", got)
	}

	// Loopback API derives the loopback web app, never production.
	if got := WebURL(Config{APIURL: "http://localhost:8911/v1/cli"}); got != "http://localhost:8910" {
		t.Fatalf("expected derived localhost web URL, got %q", got)
	}
	if got := WebURL(Config{APIURL: "http://127.0.0.1:8911/v1/cli"}); got != "http://127.0.0.1:8910" {
		t.Fatalf("expected derived 127.0.0.1 web URL, got %q", got)
	}

	// Production API falls back to the production web app.
	if got := WebURL(Config{APIURL: DefaultAPIURL}); got != DefaultWebURL {
		t.Fatalf("expected production default, got %q", got)
	}
}
