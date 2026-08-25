package oauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Only the OAuth error code decides whether a refresh failure condemns the
// stored credential; the HTTP status alone must not, or a moved endpoint or
// a rate limit would send users into a needless re-login.
func TestRefreshReportsOAuthErrorCode(t *testing.T) {
	originalClient := httpClient
	t.Cleanup(func() { httpClient = originalClient })

	for name, tc := range map[string]struct {
		status int
		body   string
		code   string
		denied bool
	}{
		"invalid_grant":            {http.StatusBadRequest, `{"error":"invalid_grant","error_description":"revoked"}`, "invalid_grant", true},
		"invalid_client":           {http.StatusUnauthorized, `{"error":"invalid_client"}`, "invalid_client", true},
		"unauthorized_client":      {http.StatusBadRequest, `{"error":"unauthorized_client"}`, "unauthorized_client", true},
		"invalid_request":          {http.StatusBadRequest, `{"error":"invalid_request"}`, "invalid_request", false},
		"rate limited":             {http.StatusTooManyRequests, `{"error":"slow down"}`, "slow down", false},
		"moved endpoint, non-JSON": {http.StatusNotFound, `<html>gone</html>`, "", false},
		"method not allowed":       {http.StatusMethodNotAllowed, ``, "", false},
		"outage":                   {http.StatusBadGateway, `{"error":"upstream"}`, "upstream", false},
	} {
		t.Run(name, func(t *testing.T) {
			httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Request:    r,
				}, nil
			})}
			_, err := Refresh(context.Background(), "https://api.test/oauth/token", "client", "refresh")
			var endpointErr *TokenEndpointError
			if !errors.As(err, &endpointErr) {
				t.Fatalf("expected a TokenEndpointError, got %v", err)
			}
			if endpointErr.StatusCode != tc.status || endpointErr.Code != tc.code {
				t.Fatalf("expected status %d code %q, got status %d code %q", tc.status, tc.code, endpointErr.StatusCode, endpointErr.Code)
			}
			if endpointErr.GrantDenied() != tc.denied {
				t.Fatalf("expected GrantDenied=%v for %s, got %v", tc.denied, name, endpointErr.GrantDenied())
			}
		})
	}
}
