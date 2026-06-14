package cli

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/baseloop-hq/baseloop-cli/internal/client"
	"github.com/baseloop-hq/baseloop-cli/internal/config"
	"github.com/baseloop-hq/baseloop-cli/internal/oauth"
	"github.com/baseloop-hq/baseloop-cli/internal/output"
	"github.com/baseloop-hq/baseloop-cli/internal/version"
)

type globals struct {
	json   bool
	agent  bool
	apiURL string
}

type command struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Usage       string   `json:"usage"`
	Summary     string   `json:"summary"`
	Subcommands []string `json:"subcommands,omitempty"`
	Actions     []string `json:"actions,omitempty"`
}

var catalog = []command{
	{Name: "auth", Category: "auth", Usage: "baseloop auth <login|status|token|logout>", Summary: "Manage local Baseloop CLI credentials", Subcommands: []string{"login", "status", "token", "logout"}, Actions: []string{"login", "status", "token", "logout"}},
	{Name: "doctor", Category: "diagnostics", Usage: "baseloop doctor", Summary: "Check install, auth, API connectivity, and Claude and Codex entry skill and plugin state"},
	{Name: "me", Category: "auth", Usage: "baseloop me", Summary: "Show the authenticated Baseloop user and active organization"},
	{Name: "tools", Category: "platform", Usage: "baseloop tools <list|describe|schema|call>", Summary: "Discover, inspect, and execute Baseloop platform tools", Subcommands: []string{"list", "describe", "schema", "call"}, Actions: []string{"list", "describe", "schema", "call"}},
	{Name: "setup", Category: "agents", Usage: "baseloop setup <skills|auto-update [on|off]>", Summary: "Install or refresh the Baseloop entry skills and plugins for Claude and Codex, and manage background auto-update", Subcommands: []string{"skills", "auto-update"}, Actions: []string{"skills", "auto-update"}},
	{Name: "upgrade", Category: "maintenance", Usage: "baseloop upgrade", Summary: "Update the CLI binary to the latest release and refresh agent setup"},
	{Name: "uninstall", Category: "agents", Usage: "baseloop uninstall [--dry-run] [--purge] [--keep-binary]", Summary: "Remove Baseloop local files, PATH entry, install state, and the binary"},
	{Name: "commands", Category: "introspection", Usage: "baseloop commands", Summary: "Print the CLI command catalog"},
	{Name: "version", Category: "introspection", Usage: "baseloop version", Summary: "Print the CLI version"},
}

func Run(args []string, stdout, stderr io.Writer) int {
	g, rest, err := parseGlobals(args)
	if err != nil {
		return render(stdout, g, output.Failure("USAGE", err.Error(), "Run baseloop commands.", nil), 2)
	}
	if len(rest) == 0 {
		usage(stdout)
		return 0
	}
	if hasHelp(rest) {
		return help(rest, g, stdout)
	}
	code := dispatch(rest, g, stdout)
	// After the command's own output, so the notice is the last thing seen.
	// Stderr keeps --json/--agent stdout parseable. This is also where the
	// opt-in background auto-update spawns; the command's own exit is never
	// delayed by it.
	maybeAutoUpdate(rest[0], stderr)
	return code
}

func dispatch(rest []string, g globals, stdout io.Writer) int {
	switch rest[0] {
	case "--version", "version":
		fmt.Fprintf(stdout, "baseloop %s\n", version.Version)
		return 0
	case "commands":
		return render(stdout, g, output.Success(map[string]any{"commands": catalog}, "", nil), 0)
	case "auth":
		return auth(rest[1:], g, stdout)
	case "doctor":
		return doctor(g, stdout)
	case "me":
		return apiGet("me", g, stdout)
	case "tools":
		return tools(rest[1:], g, stdout)
	case "setup":
		return setup(rest[1:], g, stdout)
	case "upgrade":
		return upgrade(rest[1:], g, stdout)
	case "uninstall":
		return uninstall(rest[1:], g, stdout)
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	default:
		return render(stdout, g, output.Failure("UNKNOWN_COMMAND", "Unknown command: "+rest[0], "Run baseloop commands.", nil), 2)
	}
}

func parseGlobals(args []string) (globals, []string, error) {
	g := globals{}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			g.json = true
		case arg == "--agent":
			g.agent = true
			g.json = true
		case arg == "--api-url":
			i++
			if i >= len(args) {
				return g, nil, fmt.Errorf("--api-url requires a value")
			}
			g.apiURL = args[i]
		case strings.HasPrefix(arg, "--api-url="):
			g.apiURL = strings.TrimPrefix(arg, "--api-url=")
		default:
			rest = append(rest, arg)
		}
	}
	return g, rest, nil
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Baseloop CLI")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  baseloop <command> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	for _, cmd := range catalog {
		fmt.Fprintf(w, "  %-10s %s\n", cmd.Name, cmd.Summary)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --json             Print JSON envelope")
	fmt.Fprintln(w, "  --agent            Print raw data on success and JSON errors")
	fmt.Fprintln(w, "  --api-url <url>    Override API URL, default https://api-v2.baseloop.io/v1/cli")
}

func hasHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			return true
		}
	}
	return false
}

func withoutHelp(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func help(args []string, g globals, stdout io.Writer) int {
	path := withoutHelp(args)
	if len(path) == 0 {
		path = []string{"baseloop"}
	}
	payload := helpPayload(path)
	if g.agent || g.json {
		return render(stdout, g, output.Success(payload, "", nil), 0)
	}
	if cmd, ok := payload["command"].(string); ok {
		fmt.Fprintf(stdout, "%s\n\n", cmd)
	}
	if usage, ok := payload["usage"].(string); ok {
		fmt.Fprintf(stdout, "Usage: %s\n\n", usage)
	}
	if summary, ok := payload["summary"].(string); ok && summary != "" {
		fmt.Fprintf(stdout, "%s\n\n", summary)
	}
	if subs, ok := payload["subcommands"].([]string); ok && len(subs) > 0 {
		fmt.Fprintln(stdout, "Subcommands:")
		for _, sub := range subs {
			fmt.Fprintf(stdout, "  %s\n", sub)
		}
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout, "Global flags:")
	for _, f := range globalFlagHelp() {
		fmt.Fprintf(stdout, "  %-18s %s\n", f["name"], f["usage"])
	}
	return 0
}

func helpPayload(path []string) map[string]any {
	if len(path) == 0 || path[0] == "baseloop" {
		return map[string]any{
			"command":     "baseloop",
			"path":        "baseloop",
			"usage":       "baseloop <command> [flags]",
			"summary":     "Command-line interface for Baseloop.",
			"subcommands": commandNames(),
			"flags":       globalFlagHelp(),
			"notes": []string{
				"Use --json for a full CLI envelope.",
				"Use --agent for raw success data and structured errors.",
				"Use baseloop tools list to discover tools, then baseloop tools describe <name> for a schema.",
			},
		}
	}
	name := path[0]
	for _, cmd := range catalog {
		if cmd.Name == name {
			return map[string]any{
				"command":     name,
				"path":        "baseloop " + strings.Join(path, " "),
				"usage":       cmd.Usage,
				"summary":     cmd.Summary,
				"subcommands": cmd.Subcommands,
				"actions":     cmd.Actions,
				"flags":       globalFlagHelp(),
			}
		}
	}
	return map[string]any{
		"command": "baseloop",
		"path":    "baseloop",
		"usage":   "baseloop <command> [flags]",
		"summary": "Unknown command. Run baseloop commands.",
		"flags":   globalFlagHelp(),
	}
}

func commandNames() []string {
	names := make([]string, 0, len(catalog))
	for _, cmd := range catalog {
		names = append(names, cmd.Name)
	}
	return names
}

func globalFlagHelp() []map[string]string {
	return []map[string]string{
		{"name": "--json", "type": "bool", "usage": "Print JSON envelope"},
		{"name": "--agent", "type": "bool", "usage": "Print raw data on success and JSON errors"},
		{"name": "--api-url", "type": "string", "usage": "Override API URL"},
		{"name": "--help", "type": "bool", "usage": "Show help"},
		{"name": "--version", "type": "bool", "usage": "Show version"},
	}
}

func render(w io.Writer, g globals, env output.Envelope, code int) int {
	if g.agent {
		_ = output.Agent(w, env)
	} else if g.json {
		_ = output.JSON(w, env)
	} else {
		_ = output.Text(w, env)
	}
	if !env.OK && code == 0 {
		return 1
	}
	return code
}

func loadClient(g globals) (client.Client, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return client.Client{}, cfg, err
	}
	if g.apiURL != "" {
		cfg.APIURL = config.NormalizeAPIURL(g.apiURL)
	}
	if os.Getenv("BASELOOP_TOKEN") == "" && cfg.OAuth.RefreshToken != "" && config.OAuthExpired(cfg) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := refreshOAuthToken(ctx, &cfg); err != nil {
			return client.Client{}, cfg, err
		}
		if err := config.Save(cfg); err != nil {
			return client.Client{}, cfg, err
		}
	}
	return client.New(cfg.APIURL, config.Token(cfg)), cfg, nil
}

func apiGet(path string, g globals, stdout io.Writer) int {
	c, _, err := loadClient(g)
	if err != nil {
		return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
	}
	env, status, err := c.Get(path)
	if err != nil {
		return render(stdout, g, output.Failure("API_ERROR", err.Error(), "Check baseloop doctor.", map[string]any{"status": status}), 1)
	}
	return renderAPI(stdout, g, env, status)
}

func renderAPI(stdout io.Writer, g globals, env client.Envelope, status int) int {
	if env.OK {
		var data any
		_ = json.Unmarshal(env.Data, &data)
		var meta any = map[string]any{}
		if len(env.Meta) > 0 {
			_ = json.Unmarshal(env.Meta, &meta)
		}
		return render(stdout, g, output.Success(data, env.Summary, meta), 0)
	}
	code := "API_ERROR"
	message := fmt.Sprintf("API returned HTTP %d", status)
	hint := ""
	if env.Error != nil {
		code = env.Error.Code
		message = env.Error.Message
		hint = env.Error.Hint
	}
	return render(stdout, g, output.Failure(code, message, hint, map[string]any{"status": status}), exitFor(code, status))
}

func exitFor(code string, status int) int {
	switch code {
	case "AUTH_REQUIRED", "INVALID_TOKEN":
		return 3
	case "ORG_NOT_ALLOWED", "NO_ORGANIZATIONS":
		return 4
	case "INVALID_INPUT", "ORG_REQUIRED", "TOOL_NOT_FOUND":
		return 2
	default:
		if status >= 500 || status == 0 {
			return 5
		}
		return 1
	}
}

func auth(args []string, g globals, stdout io.Writer) int {
	if len(args) == 0 {
		return render(stdout, g, output.Failure("USAGE", "auth requires a subcommand", "Use baseloop auth login, status, token, or logout.", nil), 2)
	}
	switch args[0] {
	case "login":
		fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		token := fs.String("token", "", "Clerk OAuth access token")
		noBrowser := fs.Bool("no-browser", false, "Print the login URL instead of opening a browser")
		apiURL := fs.String("api-url", "", "API URL")
		if err := fs.Parse(args[1:]); err != nil {
			return render(stdout, g, output.Failure("USAGE", err.Error(), "", nil), 2)
		}
		cfg, err := config.Load()
		if err != nil {
			return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
		}
		if *apiURL != "" {
			cfg.APIURL = config.NormalizeAPIURL(*apiURL)
		} else if g.apiURL != "" {
			cfg.APIURL = config.NormalizeAPIURL(g.apiURL)
		}
		value := *token
		if value == "" {
			value = os.Getenv("BASELOOP_TOKEN")
		}
		if value == "" {
			return oauthLogin(cfg, *noBrowser, g, stdout)
		}
		cfg.Token = value
		cfg.OAuth = config.OAuthConfig{}
		if err := config.Save(cfg); err != nil {
			return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
		}
		return render(stdout, g, output.Success(map[string]any{"apiUrl": config.NormalizeAPIURL(cfg.APIURL), "tokenStored": true, "source": "manual_token"}, "Token stored. Run baseloop me to verify access.", nil), 0)
	case "status":
		cfg, err := config.Load()
		if err != nil {
			return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
		}
		token := config.Token(cfg)
		return render(stdout, g, output.Success(map[string]any{
			"authenticated": token != "",
			"apiUrl":        cfg.APIURL,
			"source":        tokenSource(cfg),
			"configPath":    mustConfigPath(),
			"expiresAt":     cfg.OAuth.ExpiresAt,
		}, authSummary(token != ""), nil), 0)
	case "token":
		cfg, err := config.Load()
		if err != nil {
			return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
		}
		token := config.Token(cfg)
		if token == "" {
			return render(stdout, g, output.Failure("AUTH_REQUIRED", "No token is configured.", "Run baseloop auth login.", nil), 3)
		}
		fmt.Fprintln(stdout, token)
		return 0
	case "logout":
		if err := config.ClearToken(); err != nil {
			return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
		}
		return render(stdout, g, output.Success(map[string]any{"authenticated": false}, "Logged out.", nil), 0)
	default:
		return render(stdout, g, output.Failure("USAGE", "unknown auth subcommand: "+args[0], "Use baseloop auth login, status, token, or logout.", nil), 2)
	}
}

func oauthLogin(cfg config.Config, noBrowser bool, g globals, stdout io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute+30*time.Second)
	defer cancel()

	redirectURI, codeCh, shutdown, err := oauth.StartCallbackServer(ctx)
	if err != nil {
		return render(stdout, g, output.Failure("OAUTH_ERROR", err.Error(), "Check whether a local port can be opened on 127.0.0.1.", nil), 1)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = shutdown(shutdownCtx)
	}()

	metadata, err := oauth.Discover(ctx, cfg.APIURL)
	if err != nil {
		return render(stdout, g, output.Failure("OAUTH_DISCOVERY_FAILED", err.Error(), "Check --api-url and the Baseloop API OAuth metadata endpoints.", nil), 1)
	}
	registration, err := oauth.RegisterClient(ctx, metadata.RegistrationEndpoint, redirectURI)
	if err != nil {
		return render(stdout, g, output.Failure("OAUTH_REGISTRATION_FAILED", err.Error(), "Check the Baseloop OAuth app configuration.", nil), 1)
	}
	verifier, challenge, err := oauth.PKCEPair()
	if err != nil {
		return render(stdout, g, output.Failure("OAUTH_ERROR", err.Error(), "", nil), 1)
	}
	state, err := oauth.RandomURLSafe(24)
	if err != nil {
		return render(stdout, g, output.Failure("OAUTH_ERROR", err.Error(), "", nil), 1)
	}
	authURL := oauth.AuthorizeURL(metadata.AuthorizationEndpoint, registration.ClientID, redirectURI, state, challenge)
	if noBrowser {
		fmt.Fprintf(stdout, "Open this URL to log in:\n%s\n\n", authURL)
	} else if err := oauth.OpenBrowser(authURL); err != nil {
		fmt.Fprintf(stdout, "Open this URL to log in:\n%s\n\n", authURL)
	} else {
		fmt.Fprintln(stdout, "Opening Baseloop login in your browser...")
		fmt.Fprintf(stdout, "Closed the window by accident? Use this link:\n%s\n\n", authURL)
	}
	code, err := oauth.WaitForCode(ctx, codeCh, state)
	if err != nil {
		return render(stdout, g, output.Failure("OAUTH_CALLBACK_FAILED", err.Error(), "Run baseloop auth login again.", nil), 1)
	}
	token, err := oauth.ExchangeCode(ctx, metadata.TokenEndpoint, registration.ClientID, redirectURI, code, verifier)
	if err != nil {
		return render(stdout, g, output.Failure("OAUTH_TOKEN_FAILED", err.Error(), "Run baseloop auth login again.", nil), 1)
	}
	applyOAuthToken(&cfg, metadata.TokenEndpoint, registration.ClientID, token)
	cfg.Token = ""
	if err := config.Save(cfg); err != nil {
		return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
	}
	return render(stdout, g, output.Success(map[string]any{
		"apiUrl":       cfg.APIURL,
		"tokenStored":  true,
		"source":       "oauth",
		"expiresAt":    cfg.OAuth.ExpiresAt,
		"refreshToken": cfg.OAuth.RefreshToken != "",
	}, "Authenticated. Run baseloop me to verify access.", nil), 0)
}

func refreshOAuthToken(ctx context.Context, cfg *config.Config) error {
	if cfg.OAuth.TokenEndpoint == "" || cfg.OAuth.ClientID == "" || cfg.OAuth.RefreshToken == "" {
		return nil
	}
	token, err := oauth.Refresh(ctx, cfg.OAuth.TokenEndpoint, cfg.OAuth.ClientID, cfg.OAuth.RefreshToken)
	if err != nil {
		return err
	}
	applyOAuthToken(cfg, cfg.OAuth.TokenEndpoint, cfg.OAuth.ClientID, token)
	return nil
}

func applyOAuthToken(cfg *config.Config, tokenEndpoint, clientID string, token oauth.TokenResponse) {
	refreshToken := token.RefreshToken
	if refreshToken == "" {
		refreshToken = cfg.OAuth.RefreshToken
	}
	expiresAt := int64(0)
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix()
	}
	tokenType := token.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	cfg.OAuth = config.OAuthConfig{
		ClientID:      clientID,
		TokenEndpoint: tokenEndpoint,
		AccessToken:   token.AccessToken,
		RefreshToken:  refreshToken,
		TokenType:     tokenType,
		ExpiresAt:     expiresAt,
	}
}

func tokenSource(cfg config.Config) string {
	if os.Getenv("BASELOOP_TOKEN") != "" {
		return "BASELOOP_TOKEN"
	}
	if cfg.OAuth.AccessToken != "" {
		return "oauth"
	}
	if cfg.Token != "" {
		return "config"
	}
	return "none"
}

func authSummary(ok bool) string {
	if ok {
		return "Authenticated."
	}
	return "Not authenticated. Run baseloop auth login."
}

func mustConfigPath() string {
	path, err := config.DefaultPath()
	if err != nil {
		return ""
	}
	return path
}

func tools(args []string, g globals, stdout io.Writer) int {
	if len(args) == 0 {
		return render(stdout, g, output.Failure("USAGE", "tools requires a subcommand", "Use baseloop tools list, baseloop tools describe <tool_name>, or baseloop tools call.", nil), 2)
	}
	switch args[0] {
	case "list":
		return apiGet("tools", g, stdout)
	case "describe", "schema":
		if len(args) < 2 {
			return render(stdout, g, output.Failure("USAGE", "tool name is required", "Use baseloop tools describe <tool_name> --json.", nil), 2)
		}
		return apiGet("tools/"+url.PathEscape(args[1]), g, stdout)
	case "call":
		if len(args) < 2 {
			return render(stdout, g, output.Failure("USAGE", "tool name is required", "Use baseloop tools call <tool_name> --input '{}'.", nil), 2)
		}
		toolName := args[1]
		fs := flag.NewFlagSet("tools call", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		input := fs.String("input", "{}", "JSON object input")
		inputFile := fs.String("input-file", "", "Path to JSON input file")
		if err := fs.Parse(args[2:]); err != nil {
			return render(stdout, g, output.Failure("USAGE", err.Error(), "", nil), 2)
		}
		raw := []byte(*input)
		if *inputFile != "" {
			data, err := os.ReadFile(*inputFile)
			if err != nil {
				return render(stdout, g, output.Failure("INPUT_FILE_ERROR", err.Error(), "", nil), 2)
			}
			raw = data
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return render(stdout, g, output.Failure("INVALID_INPUT", "Input must be a JSON object.", err.Error(), nil), 2)
		}
		c, _, err := loadClient(g)
		if err != nil {
			return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
		}
		env, status, err := c.Post("tools/"+url.PathEscape(toolName), map[string]any{"input": obj})
		if err != nil {
			return render(stdout, g, output.Failure("API_ERROR", err.Error(), "Check baseloop doctor.", map[string]any{"status": status}), 1)
		}
		return renderAPI(stdout, g, env, status)
	default:
		return render(stdout, g, output.Failure("USAGE", "unknown tools subcommand: "+args[0], "Use baseloop tools list, baseloop tools describe <tool_name>, or baseloop tools call.", nil), 2)
	}
}

func doctor(g globals, stdout io.Writer) int {
	cfg, err := config.Load()
	checks := []map[string]any{}
	add := func(name string, ok bool, hint string) {
		checks = append(checks, map[string]any{"name": name, "ok": ok, "hint": hint})
	}
	add("config", err == nil, "Check permissions for "+mustConfigPath()+".")
	if err != nil {
		return render(stdout, g, output.Success(map[string]any{"checks": checks}, "Doctor found issues.", nil), 1)
	}
	if g.apiURL != "" {
		cfg.APIURL = config.NormalizeAPIURL(g.apiURL)
	}
	if os.Getenv("BASELOOP_TOKEN") == "" && cfg.OAuth.RefreshToken != "" && config.OAuthExpired(cfg) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		refreshErr := refreshOAuthToken(ctx, &cfg)
		cancel()
		add("oauth_refresh", refreshErr == nil, "Run baseloop auth login again.")
		if refreshErr == nil {
			_ = config.Save(cfg)
		}
	}
	add("token", config.Token(cfg) != "", "Run baseloop auth login.")
	c := client.New(cfg.APIURL, config.Token(cfg))
	env, status, apiErr := c.Get("health")
	add("api_health", apiErr == nil && env.OK, fmt.Sprintf("API URL %s returned status %d.", cfg.APIURL, status))
	if config.Token(cfg) != "" {
		me, meStatus, meErr := c.Get("me")
		add("authenticated_api", meErr == nil && me.OK, fmt.Sprintf("Run baseloop auth login again. HTTP status %d.", meStatus))
	}
	addAdvisory := func(name string, ok bool, hint string) {
		checks = append(checks, map[string]any{"name": name, "ok": ok, "advisory": true, "hint": hint})
	}
	add("claude_entry_skill", fileExists(baseloopClaudeSkillPath()), "Run baseloop setup skills.")
	// Agent plugin checks are emitted only when that agent's CLI is on PATH:
	// a Codex-only machine is a supported install, so a hard claude_plugin
	// check there would fail doctor forever (and vice versa). The entry-skill
	// check above stays unconditional because setup writes it unconditionally.
	if _, err := exec.LookPath("claude"); err == nil {
		add("claude_plugin", claudePluginOwnsGTMSkills(), "Run baseloop setup skills.")
	}
	if _, err := exec.LookPath("codex"); err == nil {
		addAdvisory("codex_entry_skill", fileExists(baseloopCodexSkillPath()), "Run baseloop setup skills.")
		addAdvisory("codex_plugin", codexPluginOwnsGTMSkills(), "Run baseloop setup skills.")
	}
	// Update signal: agents run doctor before multi-step work, so this
	// advisory is how an installed fleet learns a newer release (and any newer
	// Claude setup behavior) exists. Throttled to one lookup per 24h via the state
	// dir; absent entirely for dev builds, opt-out, or when no release is
	// known. Advisory because a stale CLI still works.
	if versionOK, versionHint, show := cliVersionAdvisory(); show {
		addAdvisory("cli_version", versionOK, versionHint)
	}
	// Auto-update state belongs in doctor regardless of the notice's own
	// rules: this is where "enabled but never updates" and recorded failures
	// stay diagnosable even after the operator turned the feature off.
	if auOK, auHint, show := autoUpdateAdvisory(); show {
		addAdvisory("auto_update", auOK, auHint)
	}
	ok := true
	for _, check := range checks {
		if advisory, _ := check["advisory"].(bool); advisory {
			continue
		}
		if check["ok"] == false {
			ok = false
			break
		}
	}
	summary := "Baseloop CLI is ready."
	if !ok {
		summary = "Doctor found issues."
	}
	return render(stdout, g, output.Success(map[string]any{"checks": checks, "apiUrl": cfg.APIURL, "version": version.Version}, summary, nil), boolExit(ok))
}

func boolExit(ok bool) int {
	if ok {
		return 0
	}
	return 1
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

func setup(args []string, g globals, stdout io.Writer) int {
	target := "skills"
	if len(args) > 0 {
		target = args[0]
	}
	switch target {
	case "skills":
		return setupSkills(g, stdout)
	case "auto-update":
		return setupAutoUpdate(args[1:], g, stdout)
	default:
		return render(stdout, g, output.Failure("USAGE", "unknown setup target: "+target, "Use baseloop setup skills or baseloop setup auto-update.", nil), 2)
	}
}

// setupAutoUpdate and the auto-update enablement helpers live in upgrade.go
// with the rest of the auto-update machinery.

// setupSkills runs every agent leg and aggregates the results: a missing
// agent CLI degrades to a note, a genuine failure exits 1, and neither stops
// the other legs from running. The Claude entry skill is CLI-owned and
// installs unconditionally (even with no agent CLI present); the entire
// Codex leg, entry skill included, is gated on the codex binary so setup
// never creates ~/.codex for software that is not installed.
func setupSkills(g globals, stdout io.Writer) int {
	var installed, notes, failures []string

	if entrySkill, err := installBaseloopClaudeSkill(); err != nil {
		failures = append(failures, "Claude entry skill: "+err.Error())
	} else {
		installed = append(installed, entrySkill)
	}

	claudeSkipped := false
	claudeInstalled, claudeNotes, claudeErr := installClaudePlugin()
	installed = append(installed, claudeInstalled...)
	notes = append(notes, claudeNotes...)
	switch {
	case errors.Is(claudeErr, errClaudeCLIMissing):
		claudeSkipped = true
		notes = append(notes, "Claude plugin setup not completed: "+claudeErr.Error()+". Install Claude Code, then re-run baseloop setup skills.")
	case claudeErr != nil:
		failures = append(failures, "Claude: "+claudeErr.Error())
	}

	codexMissing := false
	codexSkipReason := ""
	if _, err := exec.LookPath("codex"); err != nil {
		codexMissing = true
		codexSkipReason = "Codex CLI not found"
		notes = append(notes, "Codex setup not completed: "+errCodexCLIMissing.Error()+". Install Codex, then re-run baseloop setup skills.")
	} else {
		// Entry skill first, mirroring the Claude leg's ordering: the
		// CLI-owned artifact lands before any plugin command can fail.
		if entrySkill, err := installBaseloopCodexSkill(); err != nil {
			failures = append(failures, "Codex entry skill: "+err.Error())
		} else {
			installed = append(installed, entrySkill)
		}
		codexInstalled, codexNotes, codexErr := installCodexPlugin()
		installed = append(installed, codexInstalled...)
		notes = append(notes, codexNotes...)
		switch {
		case errors.Is(codexErr, errCodexPluginUnsupported):
			codexSkipReason = "Codex plugin commands unsupported"
			notes = append(notes, "Codex plugin setup not completed: "+codexErr.Error()+". Upgrade Codex, then re-run baseloop setup skills.")
		case codexErr != nil && !errors.Is(codexErr, errCodexCLIMissing):
			failures = append(failures, "Codex: "+codexErr.Error())
		}
	}

	payload := map[string]any{"installed": installed}
	if len(notes) > 0 {
		payload["notes"] = notes
	}
	if len(failures) > 0 {
		return render(stdout, g, output.Failure("SETUP_FAILED", strings.Join(failures, "; "), "Retry after fixing the named agent: baseloop setup skills.", payload), 1)
	}

	// A fully successful Claude refresh is the recovery a partial auto-update
	// failure asks for (swap landed, plugin refresh did not); clear the
	// record so its notice stops. A skipped Claude leg keeps it: the plugin
	// is still stale.
	if claudeErr == nil {
		if rec, ok := readAutoUpdateFailure(); ok && rec.Partial {
			clearAutoUpdateFailure()
		}
	}

	var summary string
	switch {
	case claudeSkipped && codexMissing:
		summary = "Installed Claude entry skill; no agent CLI found, plugin setup skipped."
	case claudeSkipped && codexSkipReason != "":
		summary = "Installed entry skills; Claude plugin setup skipped (Claude Code CLI not found); Codex plugin setup skipped (" + codexSkipReason + ")."
	case codexSkipReason != "":
		summary = "Installed Claude entry skill and plugin; Codex setup skipped (" + codexSkipReason + ")."
	case claudeSkipped:
		summary = "Installed Claude entry skill and Codex entry skill and plugin; Claude plugin setup skipped (Claude Code CLI not found)."
	default:
		summary = "Installed Baseloop entry skills and plugins for Claude and Codex."
	}
	return render(stdout, g, output.Success(payload, summary, nil), 0)
}

const (
	// pluginMarketplaceSource is the one marketplace repo both agents
	// install from; Claude and Codex only differ in how they register it.
	pluginMarketplaceSource = "baseloop-hq/baseloop-gtm-plugin"
	claudePluginName        = "baseloop-gtm"
	agentPluginTimeout      = 2 * time.Minute

	// codexMarketplaceName is the name Codex derives from the marketplace
	// repo and uses as the table key in config.toml and in plugin selectors.
	codexMarketplaceName = "baseloop-gtm-plugin"
	codexPluginName      = "baseloop-gtm"
	// codexPluginSelector is the PLUGIN@MARKETPLACE form `codex plugin add`
	// requires; the same string is the table key under [plugins.] once
	// installed.
	codexPluginSelector = codexPluginName + "@" + codexMarketplaceName
)

func baseloopClaudeSkillPath() string {
	return filepath.Join(homeDir(), ".claude", "skills", "baseloop", "SKILL.md")
}

// codexHomeDir resolves the Codex root the same way the codex CLI does:
// $CODEX_HOME when set, otherwise ~/.codex. Setup shells out to codex for
// plugin state under this root, so the entry skill must land under the same
// one or codex never sees what we wrote.
func codexHomeDir() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".codex")
}

func baseloopCodexSkillPath() string {
	return filepath.Join(codexHomeDir(), "skills", "baseloop", "SKILL.md")
}

func installBaseloopClaudeSkill() (string, error) {
	return installBaseloopEntrySkill(baseloopClaudeSkillPath(), baseloopClaudeSkill)
}

func installBaseloopCodexSkill() (string, error) {
	return installBaseloopEntrySkill(baseloopCodexSkillPath(), baseloopCodexSkill)
}

func installBaseloopEntrySkill(path, content string) (string, error) {
	dir := filepath.Dir(path)
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dir); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	marker := filepath.Join(dir, ".baseloop.sha256")
	if err := os.WriteFile(marker, []byte(hashString(content)+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// errClaudeCLIMissing marks the one plugin failure setup tolerates: no
// `claude` binary on PATH. Everything downstream of it assumes Claude exists.
var errClaudeCLIMissing = errors.New("Claude Code CLI not found on PATH")

func installClaudePlugin() ([]string, []string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, nil, errClaudeCLIMissing
	}
	var notes []string
	var installed []string
	if out, err := runClaudePluginCommand("plugin", "marketplace", "add", pluginMarketplaceSource); err != nil && !alreadyConfigured(out) {
		return installed, notes, fmt.Errorf("claude plugin marketplace add %s failed: %w%s", pluginMarketplaceSource, err, commandDetail(out))
	}
	installed = append(installed, "claude marketplace "+pluginMarketplaceSource)

	if claudePluginOwnsGTMSkills() {
		if out, err := runClaudePluginCommand("plugin", "marketplace", "update"); err != nil {
			notes = append(notes, "Claude marketplace update failed: "+err.Error()+commandDetail(out))
		}
		if out, err := runClaudePluginCommand("plugin", "update", claudePluginName); err != nil {
			notes = append(notes, "Claude plugin update failed: "+err.Error()+commandDetail(out))
		}
		installed = append(installed, "claude plugin "+claudePluginName)
		notes = append(notes, "Claude plugin was already installed; refreshed marketplace/plugin metadata.")
		return installed, notes, nil
	}

	if out, err := runClaudePluginCommand("plugin", "install", claudePluginName); err != nil {
		if !alreadyConfigured(out) {
			return installed, notes, fmt.Errorf("claude plugin install %s failed: %w%s", claudePluginName, err, commandDetail(out))
		}
		if !claudePluginOwnsGTMSkills() {
			return installed, notes, fmt.Errorf("claude plugin install %s reported an existing plugin, but the installed plugin could not be verified%s", claudePluginName, commandDetail(out))
		}
	}
	installed = append(installed, "claude plugin "+claudePluginName)
	return installed, notes, nil
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func runClaudePluginCommand(args ...string) (string, error) {
	return runAgentPluginCommand("claude", args...)
}

func runAgentPluginCommand(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentPluginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %v", agentPluginTimeout)
	}
	return string(out), err
}

func alreadyConfigured(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "already") || strings.Contains(lower, "exists")
}

func commandDetail(output string) string {
	detail := strings.TrimSpace(output)
	if detail == "" {
		return ""
	}
	if len(detail) > 300 {
		detail = detail[:300] + "..."
	}
	return " (" + detail + ")"
}

// claudePluginOwnsGTMSkills reports whether the Baseloop GTM Claude plugin is
// installed. Installed-but-disabled counts as installed: the plugin manager
// owns that lifecycle, not the CLI.
func claudePluginOwnsGTMSkills() bool {
	data, err := os.ReadFile(filepath.Join(homeDir(), ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), `"baseloop-gtm@`)
}

// errCodexCLIMissing is the Codex twin of errClaudeCLIMissing: the one plugin
// failure setup tolerates per agent.
var errCodexCLIMissing = errors.New("Codex CLI not found on PATH")

// errCodexPluginUnsupported marks a codex binary without the plugin command
// surface (a pre-plugin release, or an unrelated binary named codex).
// Presence on PATH is not capability: treating this as a skip keeps an old
// codex from aborting an otherwise healthy install.
var errCodexPluginUnsupported = errors.New("Codex CLI does not support plugin commands")

// installCodexPlugin mirrors installClaudePlugin with Codex's command shapes:
// `plugin add PLUGIN@MARKETPLACE` instead of `plugin install`, and
// `marketplace upgrade` + re-add instead of `plugin update` (Codex has no
// update subcommand). Idempotency decisions read config.toml state first;
// output sniffing is last-resort tolerance only, because the
// alreadyConfigured vocabulary was calibrated to Claude's output.
func installCodexPlugin() ([]string, []string, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return nil, nil, errCodexCLIMissing
	}
	var notes []string
	var installed []string
	if out, err := runAgentPluginCommand("codex", "plugin", "marketplace", "add", pluginMarketplaceSource); err != nil {
		switch {
		// Unsupported wins over state tolerance: a binary that does not
		// understand `plugin` at all cannot run the commands that follow,
		// no matter what config.toml says about a previous install.
		case codexPluginCommandUnsupported(out):
			return installed, notes, errCodexPluginUnsupported
		case codexMarketplaceConfigured() || alreadyConfigured(out):
			// Tolerated: the marketplace is already registered. Keep the
			// failure detail visible instead of swallowing it, and do not
			// claim the add succeeded in the installed payload.
			notes = append(notes, "Codex marketplace add failed but the marketplace is already configured: "+err.Error()+commandDetail(out))
		default:
			return installed, notes, fmt.Errorf("codex plugin marketplace add %s failed: %w%s", pluginMarketplaceSource, err, commandDetail(out))
		}
	} else {
		installed = append(installed, "codex marketplace "+pluginMarketplaceSource)
	}

	if codexPluginOwnsGTMSkills() {
		if out, err := runAgentPluginCommand("codex", "plugin", "marketplace", "upgrade", codexMarketplaceName); err != nil {
			notes = append(notes, "Codex marketplace upgrade failed: "+err.Error()+commandDetail(out))
		}
		if out, err := runAgentPluginCommand("codex", "plugin", "add", codexPluginSelector); err != nil {
			notes = append(notes, "Codex plugin refresh failed: "+err.Error()+commandDetail(out))
		}
		installed = append(installed, "codex plugin "+codexPluginName)
		notes = append(notes, "Codex plugin was already installed; refreshed marketplace/plugin metadata.")
		return installed, notes, nil
	}

	if out, err := runAgentPluginCommand("codex", "plugin", "add", codexPluginSelector); err != nil {
		if codexPluginCommandUnsupported(out) {
			return installed, notes, errCodexPluginUnsupported
		}
		if !alreadyConfigured(out) && !codexPluginOwnsGTMSkills() {
			return installed, notes, fmt.Errorf("codex plugin add %s failed: %w%s", codexPluginSelector, err, commandDetail(out))
		}
		if !codexPluginOwnsGTMSkills() {
			return installed, notes, fmt.Errorf("codex plugin add %s reported an existing plugin, but the installed plugin could not be verified%s", codexPluginSelector, commandDetail(out))
		}
	}
	installed = append(installed, "codex plugin "+codexPluginName)
	return installed, notes, nil
}

// codexPluginCommandUnsupported sniffs the usage error a codex binary
// without the plugin surface prints for `codex plugin ...`. Distinct from
// alreadyConfigured: this is "the subcommand does not exist", not "the work
// was already done".
func codexPluginCommandUnsupported(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "unknown command") ||
		strings.Contains(lower, "unrecognized subcommand") ||
		strings.Contains(lower, "usage:")
}

func codexConfigPath() string {
	return filepath.Join(codexHomeDir(), "config.toml")
}

// codexPluginOwnsGTMSkills reports whether the Baseloop GTM plugin is
// installed in Codex. Codex records installs as [plugins."name@marketplace"]
// tables in config.toml; matching the quoted selector (either quote style,
// comment lines skipped) mirrors the loose-but-stable substring check the
// Claude side does against installed_plugins.json. A missing or unreadable
// file is "not installed", never an error: the plugin manager owns that state.
func codexPluginOwnsGTMSkills() bool {
	return codexConfigContains(`"`+codexPluginSelector+`"`, `'`+codexPluginSelector+`'`)
}

// codexMarketplaceConfigured reports whether the Baseloop marketplace source
// is already registered, so a failing `marketplace add` on an
// already-configured machine degrades instead of failing setup.
func codexMarketplaceConfigured() bool {
	return codexConfigContains(
		"[marketplaces."+codexMarketplaceName+"]",
		`[marketplaces."`+codexMarketplaceName+`"]`,
		`[marketplaces.'`+codexMarketplaceName+`']`,
	)
}

func codexConfigContains(needles ...string) bool {
	data, err := os.ReadFile(codexConfigPath())
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, needle := range needles {
			if strings.Contains(trimmed, needle) {
				return true
			}
		}
	}
	return false
}

type githubRelease struct {
	TagName    string               `json:"tag_name"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func extractTarGzFile(path, target string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		cleanName := filepath.Clean(header.Name)
		if cleanName == "." || strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return fmt.Errorf("unsafe path in archive: %s", header.Name)
		}
		dest := filepath.Join(target, cleanName)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

// verifyFileSHA returns an error unless the file at path hashes to expected
// (case-insensitive hex SHA256).
func verifyFileSHA(path, expected string) error {
	got, err := fileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, got)
	}
	return nil
}

// httpGetBytes fetches a small file (a checksums manifest) into memory.
func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // URL is operator-configurable installer input.
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("download %s returned HTTP %d", url, res.StatusCode)
	}
	return io.ReadAll(res.Body)
}

// shaForFile parses a checksums manifest and returns the lowercase hash for
// fileName. It accepts the common "<sha>  <name>" / "<sha> *<name>" layout and a
// bare single-hash file (a <asset>.sha256). Returns "" when not found.
func shaForFile(checksums []byte, fileName string) string {
	scanner := bufio.NewScanner(strings.NewReader(string(checksums)))
	var single string
	lines := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 1 {
			single = fields[0]
			lines++
			continue
		}
		lines++
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if baseName(name) == fileName {
			return strings.ToLower(fields[0])
		}
	}
	if single != "" && lines == 1 {
		return strings.ToLower(single)
	}
	return ""
}

// gtmAssetBaseName extracts the file name from a bundle URL or local path,
// stripping any query/fragment and a file:// prefix.
func gtmAssetBaseName(rawURL string) string {
	u := strings.TrimPrefix(rawURL, "file://")
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return baseName(u)
}

// baseName returns the last "/"-separated segment, which works for both URLs
// and POSIX paths without depending on the OS path separator.
func baseName(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func extractZipFile(path, target string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, file := range zr.File {
		cleanName := filepath.Clean(file.Name)
		if cleanName == "." || strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return fmt.Errorf("unsafe path in archive: %s", file.Name)
		}
		dest := filepath.Join(target, cleanName)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if !file.FileInfo().Mode().IsRegular() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = in.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = in.Close()
			_ = out.Close()
			return err
		}
		if err := in.Close(); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}
