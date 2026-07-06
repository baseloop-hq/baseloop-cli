package config

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultAPIURL = "https://api-v2.baseloop.io/v1/cli"

const DefaultWebURL = "https://app.baseloop.io"

// devWebPort is the Cedar dev-server web port that pairs with the loopback
// API port (web 8910 next to api 8911); see WebURL's local derivation.
const devWebPort = "8910"

type Config struct {
	APIURL string `json:"api_url"`
	// WebURL is the browser-facing app origin, used for post-login handoffs
	// (e.g. the workflow recipes page). Overridable via BASELOOP_WEB_URL for
	// local development.
	WebURL string      `json:"web_url,omitempty"`
	Token  string      `json:"token,omitempty"`
	OAuth  OAuthConfig `json:"oauth,omitempty"`
	// AutoUpdate opts this machine into background self-updates. Off by
	// default: the CLI executing downloaded binaries without a human in the
	// loop is consent the operator gives explicitly (releases are not signed
	// yet, so checksums prove integrity, not authenticity).
	AutoUpdate bool `json:"auto_update,omitempty"`
}

type OAuthConfig struct {
	ClientID      string `json:"client_id,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	AccessToken   string `json:"access_token,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
}

func DefaultPath() (string, error) {
	if dir := os.Getenv("BASELOOP_CONFIG"); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "baseloop", "config.json"), nil
}

func Load() (Config, error) {
	cfg := Config{APIURL: DefaultAPIURL}
	path, err := DefaultPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	cfg.APIURL = NormalizeAPIURL(cfg.APIURL)
	return cfg, nil
}

// WebURL resolves the browser-facing app origin: env override first, then the
// stored config value, then a local-development derivation, then the
// production default. A CLI pointed at a loopback API must never hand the
// browser to the production app, so a localhost API implies the localhost web
// app on the Cedar dev port (devWebPort).
func WebURL(cfg Config) string {
	if fromEnv := strings.TrimRight(strings.TrimSpace(os.Getenv("BASELOOP_WEB_URL")), "/"); fromEnv != "" {
		return fromEnv
	}
	if stored := strings.TrimRight(strings.TrimSpace(cfg.WebURL), "/"); stored != "" {
		return stored
	}
	if u, err := url.Parse(cfg.APIURL); err == nil {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return "http://" + net.JoinHostPort(host, devWebPort)
		}
	}
	return DefaultWebURL
}

func Save(cfg Config) error {
	path, err := DefaultPath()
	if err != nil {
		return err
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	cfg.APIURL = NormalizeAPIURL(cfg.APIURL)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Atomic (temp + rename, 0600 from CreateTemp): the config now has
	// concurrent writers — OAuth token refresh on any API command and
	// `setup auto-update` from installers/fleet tooling — and a torn write
	// would corrupt stored credentials.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func ClearToken() error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	cfg.Token = ""
	cfg.OAuth = OAuthConfig{}
	return Save(cfg)
}

func NormalizeAPIURL(raw string) string {
	url := strings.TrimRight(strings.TrimSpace(raw), "/")
	if url == "" {
		return DefaultAPIURL
	}
	if strings.HasSuffix(url, "/v1") {
		return url + "/cli"
	}
	return url
}

func Token(cfg Config) string {
	if token := os.Getenv("BASELOOP_TOKEN"); token != "" {
		return token
	}
	if cfg.OAuth.AccessToken != "" {
		return cfg.OAuth.AccessToken
	}
	return cfg.Token
}

func OAuthExpired(cfg Config) bool {
	if cfg.OAuth.AccessToken == "" {
		return true
	}
	if cfg.OAuth.ExpiresAt == 0 {
		return false
	}
	return time.Now().Add(60*time.Second).Unix() >= cfg.OAuth.ExpiresAt
}
