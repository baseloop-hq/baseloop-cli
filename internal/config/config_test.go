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
