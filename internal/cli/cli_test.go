package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// useOfflineTransport stubs the default HTTP transport with a 500 responder
// so doctor and release-path tests never touch the network.
func useOfflineTransport(t *testing.T) {
	t.Helper()
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("down")), Header: http.Header{}}, nil
	})
}

func TestCommandsJSON(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"commands", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"commands"`) {
		t.Fatalf("expected command catalog JSON, got %s", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"nope", "--json"}, &out, &out)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "UNKNOWN_COMMAND") {
		t.Fatalf("expected unknown command error, got %s", out.String())
	}
}

func TestToolsCallRequiresToolName(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"tools", "call", "--json"}, &out, &out)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "tool name is required") {
		t.Fatalf("expected usage error, got %s", out.String())
	}
}

func TestToolsDescribeRequiresToolName(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"tools", "describe", "--json"}, &out, &out)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "tool name is required") {
		t.Fatalf("expected usage error, got %s", out.String())
	}
}

func TestToolsDescribeFetchesToolDetail(t *testing.T) {
	requests := []string{}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"data":{"tool":{"name":"list_rows","inputSchema":{"type":"object","properties":{"tableId":{"type":"string"}},"required":["tableId"]}}},"meta":{"orgId":"org_a"}}`)),
			Request:    r,
		}, nil
	})

	var out bytes.Buffer
	code := Run([]string{"--api-url", "https://api.test", "tools", "describe", "list_rows", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if len(requests) != 1 || requests[0] != "GET /tools/list_rows" {
		t.Fatalf("expected describe to GET tool detail, got %v", requests)
	}
	if !strings.Contains(out.String(), `"inputSchema"`) {
		t.Fatalf("expected schema in output, got %s", out.String())
	}
}

func TestToolsSchemaAliasFetchesToolDetail(t *testing.T) {
	requests := []string{}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"data":{"tool":{"name":"create_table","inputSchema":{"type":"object"}}}}`)),
			Request:    r,
		}, nil
	})

	var out bytes.Buffer
	code := Run([]string{"--api-url", "https://api.test", "tools", "schema", "create_table", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if len(requests) != 1 || requests[0] != "GET /tools/create_table" {
		t.Fatalf("expected schema alias to GET tool detail, got %v", requests)
	}
}

func TestToolsCallEscapesToolName(t *testing.T) {
	requests := []string{}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"data":{"result":"ok"}}`)),
			Request:    r,
		}, nil
	})

	var out bytes.Buffer
	code := Run([]string{"--api-url", "https://api.test", "tools", "call", "vendor/tool", "--input", "{}", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if len(requests) != 1 || requests[0] != "POST /tools/vendor%2Ftool" {
		t.Fatalf("expected call to escape tool name, got %v", requests)
	}
}

func TestAgentHelpIsStructured(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"tools", "--agent", "--help"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"command": "tools"`) {
		t.Fatalf("expected tools help JSON, got %s", out.String())
	}
	if !strings.Contains(out.String(), `"subcommands"`) {
		t.Fatalf("expected subcommands in help JSON, got %s", out.String())
	}
}

func TestSetupInstallsClaudeEntrySkillAndMarketplacePlugin(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // hermetic: keep the host's codex/claude out
	log := fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)

	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HERMES_HOME", "")

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	assertCommandLog(t, log, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin install baseloop-gtm",
	})
	entrySkill := filepath.Join(home, ".claude", "skills", "baseloop", "SKILL.md")
	content, err := os.ReadFile(entrySkill)
	if err != nil {
		t.Fatalf("expected setup to write the Baseloop entry skill: %v", err)
	}
	if !strings.Contains(string(content), "baseloop setup skills") {
		t.Fatalf("entry skill should point at setup skills, got %s", string(content))
	}
	retiredSetupTargets := []string{"baseloop setup " + "gtm", "baseloop setup " + "all"}
	if strings.Contains(string(content), retiredSetupTargets[0]) || strings.Contains(string(content), retiredSetupTargets[1]) {
		t.Fatalf("entry skill should not mention retired setup targets, got %s", string(content))
	}
	assertBaseloopEntrySkillRoutesCurrentGTM(t, string(content))
}

func TestSetupReplacesLegacyClaudeSkillSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())

	agentsSkill := filepath.Join(home, ".agents", "skills", "baseloop")
	if err := os.MkdirAll(agentsSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	oldAgentsContent := []byte("# Old agents Baseloop skill\n")
	if err := os.WriteFile(filepath.Join(agentsSkill, "SKILL.md"), oldAgentsContent, 0o644); err != nil {
		t.Fatal(err)
	}
	claudeParent := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claudeParent, 0o755); err != nil {
		t.Fatal(err)
	}
	claudeSkill := filepath.Join(claudeParent, "baseloop")
	if err := os.Symlink(agentsSkill, claudeSkill); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	info, err := os.Lstat(claudeSkill)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected legacy symlink replaced with a real directory")
	}
	gotAgentsContent, err := os.ReadFile(filepath.Join(agentsSkill, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotAgentsContent) != string(oldAgentsContent) {
		t.Fatalf("setup should not rewrite old agents skill through symlink, got %q", string(gotAgentsContent))
	}
}

func TestSetupUpdatesExistingClaudePluginWithoutTouchingLocalFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	log := fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HERMES_HOME", "")
	seedClaudePlugin(t, home)

	claudeLeaf := filepath.Join(home, ".claude", "skills", "baseloop-gtm-build")
	if err := os.MkdirAll(claudeLeaf, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	assertCommandLog(t, log, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin marketplace update",
		"plugin update baseloop-gtm",
	})
	if _, err := os.Lstat(claudeLeaf); err != nil {
		t.Fatalf("setup should not touch existing local Claude files, lstat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "baseloop", "SKILL.md")); err != nil {
		t.Fatalf("expected setup to write the Baseloop entry skill: %v", err)
	}
	if !strings.Contains(out.String(), "refreshed marketplace/plugin metadata") {
		t.Fatalf("expected plugin refresh note, got %s", out.String())
	}
}

func TestSetupDegradesGracefullyWhenClaudeMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on unix HOME semantics for skill paths")
	}
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected graceful exit 0 without Claude, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "baseloop", "SKILL.md")); err != nil {
		t.Fatalf("entry skill should install even without Claude: %v", err)
	}
	if !strings.Contains(out.String(), "Claude plugin setup not completed: Claude Code CLI not found on PATH") {
		t.Fatalf("expected missing-Claude note, got %s", out.String())
	}
	if !strings.Contains(out.String(), "Codex setup not completed: Codex CLI not found on PATH") {
		t.Fatalf("expected missing-Codex note, got %s", out.String())
	}
	if !strings.Contains(out.String(), "no agent CLI found, plugin setup skipped") {
		t.Fatalf("expected degraded summary, got %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("setup must not create ~/.codex when Codex is not installed")
	}
}

func TestSetupFailsWhenAlreadyInstalledCannotBeVerified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	fakeClaude(t, `printf 'plugin already exists\n'; exit 1`)
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "could not be verified") {
		t.Fatalf("expected verification failure, got %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "baseloop", "SKILL.md")); err != nil {
		t.Fatalf("entry skill should still be written before plugin failure: %v", err)
	}
}

func TestSetupRejectsUnsupportedAgentTargets(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", stateDir)

	for _, target := range []string{"all", "claude", "gtm", "agents", "codex", "hermes"} {
		var out bytes.Buffer
		code := Run([]string{"setup", target, "--json"}, &out, &out)
		if code != 2 {
			t.Fatalf("setup %s: expected usage exit 2, got %d: %s", target, code, out.String())
		}
		if !strings.Contains(out.String(), "Use baseloop setup skills or baseloop setup auto-update.") {
			t.Fatalf("setup %s: expected supported-targets usage hint, got %s", target, out.String())
		}
	}
}

func TestSetupInstallsBothAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	claudeLog := fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)
	codexLog := fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	assertCommandLog(t, claudeLog, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin install baseloop-gtm",
	})
	assertCommandLog(t, codexLog, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin add baseloop-gtm@baseloop-gtm-plugin",
	})
	if !strings.Contains(out.String(), "for Claude and Codex") {
		t.Fatalf("expected dual-agent summary, got %s", out.String())
	}
	for _, path := range []string{
		filepath.Join(home, ".claude", "skills", "baseloop", "SKILL.md"),
		filepath.Join(home, ".codex", "skills", "baseloop", "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected entry skill at %s: %v", path, err)
		}
	}
}

func TestSetupCodexOnlyMachine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	codexLog := fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0 on a Codex-only machine, got %d: %s", code, out.String())
	}
	assertCommandLog(t, codexLog, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin add baseloop-gtm@baseloop-gtm-plugin",
	})
	if !strings.Contains(out.String(), "Claude plugin setup skipped (Claude Code CLI not found)") {
		t.Fatalf("expected Claude-skipped summary, got %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "baseloop", "SKILL.md")); err != nil {
		t.Fatalf("Claude entry skill installs unconditionally: %v", err)
	}
}

func TestSetupClaudeFailureStillRunsCodexLeg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	fakeClaude(t, `printf 'mysterious explosion\n'; exit 1`)
	codexLog := fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected exit 1 on Claude failure, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Claude: claude plugin marketplace add") {
		t.Fatalf("error should name Claude, got %s", out.String())
	}
	assertCommandLog(t, codexLog, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin add baseloop-gtm@baseloop-gtm-plugin",
	})
	if !strings.Contains(out.String(), "codex plugin baseloop-gtm") {
		t.Fatalf("successful Codex leg should still be reported in the payload, got %s", out.String())
	}
}

func TestSetupCodexFailureNamesCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)
	fakeCodex(t, `printf 'mysterious explosion\n'; exit 1`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected exit 1 on Codex failure, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Codex: codex plugin marketplace add") {
		t.Fatalf("error should name Codex, got %s", out.String())
	}
	if !strings.Contains(out.String(), "claude plugin baseloop-gtm") {
		t.Fatalf("successful Claude leg should still be reported in the payload, got %s", out.String())
	}
}

func TestSetupClaudeEntrySkillWriteFailureStillRunsCodexLeg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	codexLog := fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	// A directory where SKILL.md should be makes the Claude entry-skill
	// write fail without touching permissions (portable across CI users).
	if err := os.MkdirAll(filepath.Join(home, ".claude", "skills", "baseloop", "SKILL.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected exit 1 on entry skill write failure, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Claude entry skill:") {
		t.Fatalf("error should name the Claude entry skill, got %s", out.String())
	}
	assertCommandLog(t, codexLog, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin add baseloop-gtm@baseloop-gtm-plugin",
	})
}

func TestSetupCodexEntrySkillWriteFailureStillRunsClaudeLeg(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	claudeLog := fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)
	fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	// A directory where the Codex SKILL.md should be makes the write fail.
	if err := os.MkdirAll(filepath.Join(home, ".codex", "skills", "baseloop", "SKILL.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected exit 1 on Codex entry skill write failure, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Codex entry skill:") {
		t.Fatalf("error should name the Codex entry skill, got %s", out.String())
	}
	assertCommandLog(t, claudeLog, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin install baseloop-gtm",
	})
	if !strings.Contains(out.String(), "claude plugin baseloop-gtm") {
		t.Fatalf("successful Claude leg should still be reported in the payload, got %s", out.String())
	}
}

func TestSetupSkipsCodexLegWhenPluginCommandsUnsupported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)
	fakeCodex(t, `printf 'Usage: codex [OPTIONS] <COMMAND>\nerror: unrecognized subcommand\n'; exit 2`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	var out bytes.Buffer
	code := Run([]string{"setup", "skills", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("a codex binary without plugin support must not fail setup, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Codex plugin setup not completed: Codex CLI does not support plugin commands") {
		t.Fatalf("expected unsupported-plugin note, got %s", out.String())
	}
	if !strings.Contains(out.String(), "Codex setup skipped (Codex plugin commands unsupported)") {
		t.Fatalf("expected unsupported-skip summary, got %s", out.String())
	}
}

func TestInstallCodexPluginUnsupportedWinsOverConfiguredState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	// A downgraded codex without plugin support, on a machine whose
	// config.toml still carries the marketplace from a previous install:
	// the unsupported sentinel must win over state tolerance, because the
	// commands that follow would fail the same way.
	fakeCodex(t, `printf 'Usage: codex [OPTIONS] <COMMAND>\nerror: unrecognized subcommand\n'; exit 2`)
	seedCodexConfig(t, "[marketplaces.baseloop-gtm-plugin]\nsource = \"https://github.com/baseloop-hq/baseloop-gtm-plugin.git\"\n")

	_, _, err := installCodexPlugin()
	if !errors.Is(err, errCodexPluginUnsupported) {
		t.Fatalf("expected errCodexPluginUnsupported, got %v", err)
	}
}

func TestInstallCodexPluginAddRecoversViaConfigRecheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	// The fake records install state on `plugin add` before failing with
	// already-exists wording, modeling a re-add that codex treats as a
	// conflict while config.toml proves the plugin is present.
	fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"
case "$*" in
  "plugin add"*)
    mkdir -p "$CODEX_HOME"
    printf '[plugins."baseloop-gtm@baseloop-gtm-plugin"]\n' >> "$CODEX_HOME/config.toml"
    printf 'plugin already exists\n'
    exit 1;;
esac`)

	installed, _, err := installCodexPlugin()
	if err != nil {
		t.Fatalf("ground-truth recheck should accept the install, got %v", err)
	}
	found := false
	for _, entry := range installed {
		if entry == "codex plugin baseloop-gtm" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected plugin recorded as installed, got %#v", installed)
	}
}

func TestInstallCodexPluginAddUnverifiedAlreadyExistsFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"
case "$*" in
  "plugin add"*) printf 'plugin already exists\n'; exit 1;;
esac`)

	_, _, err := installCodexPlugin()
	if err == nil || !strings.Contains(err.Error(), "could not be verified") {
		t.Fatalf("already-exists without config state must fail verification, got %v", err)
	}
}

func TestDoctorAgentChecksAreConditionalOnBinaries(t *testing.T) {
	t.Run("codex on PATH, claude absent", func(t *testing.T) {
		useOfflineTransport(t)
		t.Setenv("PATH", t.TempDir())
		fakeCodex(t, `exit 0`)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("CODEX_HOME", "")
		t.Setenv("BASELOOP_STATE", t.TempDir())

		var out bytes.Buffer
		Run([]string{"doctor", "--json"}, &out, &out)
		if strings.Contains(out.String(), `"claude_plugin"`) {
			t.Fatalf("claude_plugin must not be emitted without claude on PATH, got %s", out.String())
		}
		if !strings.Contains(out.String(), `"claude_entry_skill"`) {
			t.Fatalf("claude_entry_skill stays unconditional, got %s", out.String())
		}
		if !strings.Contains(out.String(), `"codex_entry_skill"`) || !strings.Contains(out.String(), `"codex_plugin"`) {
			t.Fatalf("expected codex checks with codex on PATH, got %s", out.String())
		}
		if !strings.Contains(out.String(), `"advisory":true`) && !strings.Contains(out.String(), `"advisory": true`) {
			t.Fatalf("codex checks must be advisory, got %s", out.String())
		}
	})

	t.Run("no agent binaries", func(t *testing.T) {
		useOfflineTransport(t)
		t.Setenv("PATH", t.TempDir())
		t.Setenv("HOME", t.TempDir())
		t.Setenv("CODEX_HOME", "")
		t.Setenv("BASELOOP_STATE", t.TempDir())

		var out bytes.Buffer
		Run([]string{"doctor", "--json"}, &out, &out)
		for _, check := range []string{`"claude_plugin"`, `"codex_entry_skill"`, `"codex_plugin"`} {
			if strings.Contains(out.String(), check) {
				t.Fatalf("%s must not be emitted without its binary, got %s", check, out.String())
			}
		}
	})
}

func TestInstallBaseloopCodexSkillWritesContentAndMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE on Windows; without this the test
	// would write into the real home there.
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")

	path, err := installBaseloopCodexSkill()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".codex", "skills", "baseloop", "SKILL.md")
	if path != want {
		t.Fatalf("skill path = %q, want %q", path, want)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != baseloopCodexSkill {
		t.Fatalf("skill content does not match baseloopCodexSkill")
	}
	marker, err := os.ReadFile(filepath.Join(filepath.Dir(path), ".baseloop.sha256"))
	if err != nil {
		t.Fatalf("expected ownership marker: %v", err)
	}
	if strings.TrimSpace(string(marker)) != hashString(baseloopCodexSkill) {
		t.Fatalf("marker hash does not match skill content")
	}
}

func TestInstallBaseloopCodexSkillHonorsCodexHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	path, err := installBaseloopCodexSkill()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(codexHome, "skills", "baseloop", "SKILL.md")
	if path != want {
		t.Fatalf("skill path = %q, want %q (CODEX_HOME should win)", path, want)
	}
}

func TestInstallBaseloopCodexSkillReplacesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevated privileges on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")

	target := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(home, ".codex", "skills")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "baseloop")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := installBaseloopCodexSkill(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected symlink replaced with a real directory")
	}
}

func TestCodexSkillContentStaysAgentNeutral(t *testing.T) {
	if strings.Contains(baseloopCodexSkill, "Claude") {
		t.Fatalf("Codex entry skill must not carry Claude-specific phrasing")
	}
	if strings.Contains(baseloopCodexSkill, "user-invocable") {
		t.Fatalf("Codex skill frontmatter must not carry the Claude-only user-invocable field")
	}
	if !strings.Contains(baseloopCodexSkill, "baseloop setup skills") {
		t.Fatalf("Codex entry skill should point at setup skills")
	}
	assertBaseloopEntrySkillRoutesCurrentGTM(t, baseloopCodexSkill)
}

func assertBaseloopEntrySkillRoutesCurrentGTM(t *testing.T, content string) {
	t.Helper()
	for _, want := range []string{
		"`baseloop-gtm-plan`",
		"`baseloop-gtm-build`",
		"`baseloop-gtm-diagnose`",
		"`baseloop-gtm-review`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("entry skill should route to %s, got %s", want, content)
		}
	}
	for _, stale := range []string{
		"baseloop-gtm:plan",
		"baseloop-gtm:build",
		"baseloop-gtm:diagnose",
		"baseloop-gtm:review",
		"baseloop-gtm:lfg",
		"baseloop-gtm:help",
		"baseloop-gtm:setup",
		"baseloop-gtm:update",
	} {
		if strings.Contains(content, stale) {
			t.Fatalf("entry skill should not mention stale GTM skill %q, got %s", stale, content)
		}
	}
}

func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	return fakeAgentBin(t, "claude", body)
}

// fakeAgentBin writes a stub agent CLI named `bin` onto PATH that runs `body`
// with $LOG pointing at a per-call log file, and returns that log path. The
// thin fakeClaude/fakeCodex wrappers exist so call sites read by agent name.
func fakeAgentBin(t *testing.T, bin, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// The stub is a unix shell script; exec.LookPath on Windows needs an
		// executable extension and cmd semantics. The behavior these tests
		// cover is platform-independent CLI logic, exercised on unix CI.
		t.Skip("fake agent fixture is a unix shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, bin+".log")
	path := filepath.Join(dir, bin)
	script := "#!/bin/sh\nset -eu\nLOG=" + shellQuote(log) + "\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func assertCommandLog(t *testing.T, log string, want []string) {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(got) == 1 && got[0] == "" {
		got = nil
	}
	if len(got) != len(want) {
		t.Fatalf("command log length mismatch:\nwant %#v\ngot  %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("command %d mismatch: want %q, got %q (all: %#v)", i, want[i], got[i], got)
		}
	}
}

func seedClaudePlugin(t *testing.T, home string) {
	t.Helper()
	pluginsDir := filepath.Join(home, ".claude", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := `{"baseloop-gtm@baseloop": {"version": "0.8.1"}}`
	if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(installed), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeCodex(t *testing.T, body string) string {
	t.Helper()
	return fakeAgentBin(t, "codex", body)
}

func seedCodexConfig(t *testing.T, content string) {
	t.Helper()
	dir := codexHomeDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallCodexPluginFreshInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	log := fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"`)

	installed, notes, err := installCodexPlugin()
	if err != nil {
		t.Fatalf("installCodexPlugin: %v (notes: %v)", err, notes)
	}
	assertCommandLog(t, log, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin add baseloop-gtm@baseloop-gtm-plugin",
	})
	want := []string{"codex marketplace baseloop-hq/baseloop-gtm-plugin", "codex plugin baseloop-gtm"}
	if len(installed) != len(want) || installed[0] != want[0] || installed[1] != want[1] {
		t.Fatalf("installed = %#v, want %#v", installed, want)
	}
}

func TestInstallCodexPluginRefreshesWhenAlreadyInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	log := fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"`)
	seedCodexConfig(t, "[marketplaces.baseloop-gtm-plugin]\nsource = \"https://github.com/baseloop-hq/baseloop-gtm-plugin.git\"\n\n[plugins.\"baseloop-gtm@baseloop-gtm-plugin\"]\nenabled = true\n")

	_, notes, err := installCodexPlugin()
	if err != nil {
		t.Fatalf("installCodexPlugin: %v", err)
	}
	assertCommandLog(t, log, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin marketplace upgrade baseloop-gtm-plugin",
		"plugin add baseloop-gtm@baseloop-gtm-plugin",
	})
	found := false
	for _, note := range notes {
		if strings.Contains(note, "already installed; refreshed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected refresh note, got %#v", notes)
	}
}

func TestInstallCodexPluginToleratesMarketplaceAddFailureWhenConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	log := fakeCodex(t, `printf '%s\n' "$*" >> "$LOG"
case "$*" in
  "plugin marketplace add"*) printf 'unexpected wording\n'; exit 1;;
esac`)
	seedCodexConfig(t, "[marketplaces.baseloop-gtm-plugin]\nsource = \"https://github.com/baseloop-hq/baseloop-gtm-plugin.git\"\n")

	_, _, err := installCodexPlugin()
	if err != nil {
		t.Fatalf("marketplace add failure with configured marketplace should be tolerated, got %v", err)
	}
	assertCommandLog(t, log, []string{
		"plugin marketplace add baseloop-hq/baseloop-gtm-plugin",
		"plugin add baseloop-gtm@baseloop-gtm-plugin",
	})
}

func TestInstallCodexPluginFailsOnMarketplaceAddWithoutState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	fakeCodex(t, `printf 'unexpected wording\n'; exit 1`)

	_, _, err := installCodexPlugin()
	if err == nil {
		t.Fatalf("expected marketplace add failure to surface")
	}
	if !strings.Contains(err.Error(), "codex plugin marketplace add") {
		t.Fatalf("error should name the failing command, got %v", err)
	}
}

func TestInstallCodexPluginMissingBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	_, _, err := installCodexPlugin()
	if !errors.Is(err, errCodexCLIMissing) {
		t.Fatalf("expected errCodexCLIMissing, got %v", err)
	}
}

func TestCodexPluginOwnsGTMSkillsDetection(t *testing.T) {
	cases := map[string]struct {
		config string
		want   bool
	}{
		"double quoted":     {"[plugins.\"baseloop-gtm@baseloop-gtm-plugin\"]\n", true},
		"single quoted":     {"[plugins.'baseloop-gtm@baseloop-gtm-plugin']\n", true},
		"commented out":     {"# [plugins.\"baseloop-gtm@baseloop-gtm-plugin\"]\n", false},
		"unrelated plugins": {"[plugins.\"other@market\"]\nenabled = true\n", false},
		"empty file":        {"", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			if tc.config != "" {
				seedCodexConfig(t, tc.config)
			}
			if got := codexPluginOwnsGTMSkills(); got != tc.want {
				t.Fatalf("codexPluginOwnsGTMSkills = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("absent file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("CODEX_HOME", t.TempDir())
		if codexPluginOwnsGTMSkills() {
			t.Fatalf("missing config.toml must read as not installed")
		}
	})
}

func TestShaForFile(t *testing.T) {
	manifest := []byte("# generated\n" +
		"aaaa1111  other.zip\n" +
		"BBBB2222 *baseloop-gtm-0.8.0.zip\n")
	if got := shaForFile(manifest, "baseloop-gtm-0.8.0.zip"); got != "bbbb2222" {
		t.Fatalf("shaForFile = %q, want bbbb2222 (lowercased, * stripped)", got)
	}
	if got := shaForFile(manifest, "missing.zip"); got != "" {
		t.Fatalf("shaForFile(missing) = %q, want empty", got)
	}
	if got := shaForFile([]byte("CAFE\n"), "anything.zip"); got != "cafe" {
		t.Fatalf("shaForFile(bare) = %q, want cafe", got)
	}
}

func TestGTMAssetBaseName(t *testing.T) {
	cases := map[string]string{
		"https://example.com/d/baseloop-gtm-0.8.0.zip?token=x": "baseloop-gtm-0.8.0.zip",
		"file:///tmp/bundles/baseloop-gtm.zip":                 "baseloop-gtm.zip",
		"/local/path/baseloop-gtm.zip":                         "baseloop-gtm.zip",
	}
	for in, want := range cases {
		if got := gtmAssetBaseName(in); got != want {
			t.Errorf("gtmAssetBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeTarGzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range files {
		data := []byte(content)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSetupNotesPayloadIsStringArray(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // hermetic: keep the host's codex/claude out
	fakeClaude(t, `printf '%s\n' "$*" >> "$LOG"`)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("HERMES_HOME", "")

	var out bytes.Buffer
	if code := Run([]string{"setup", "skills", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Installed []string `json:"installed"`
			Notes     []string `json:"notes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("setup skills output is not the expected JSON envelope: %v\n%s", err, out.String())
	}
	if !envelope.OK || len(envelope.Data.Installed) == 0 {
		t.Fatalf("unexpected envelope content: %+v", envelope)
	}
	if len(envelope.Data.Notes) == 0 {
		t.Fatalf("expected a Codex-skipped note on a Claude-only machine, got none")
	}
	for _, note := range envelope.Data.Notes {
		if note == "" {
			t.Fatalf("notes must be non-empty strings, got %+v", envelope.Data.Notes)
		}
	}
}

// autoUpdateEnvelope decodes the JSON envelope the setup auto-update command
// renders, exposing the per-layer state payload.
type autoUpdateEnvelope struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"`
	Data    struct {
		Effective   bool   `json:"effective"`
		Config      bool   `json:"config"`
		EnvOverride string `json:"env_override"`
		ConfigError string `json:"config_error"`
	} `json:"data"`
}

func runAutoUpdateCmd(t *testing.T, args ...string) (autoUpdateEnvelope, int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Run(append(args, "--json"), &out, &out)
	var env autoUpdateEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("invalid envelope %q: %v", out.String(), err)
	}
	return env, code, out.String()
}

func TestSetupAutoUpdateRoundTrip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("BASELOOP_CONFIG", configPath)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	useOfflineTransport(t)

	env, code, raw := runAutoUpdateCmd(t, "setup", "auto-update")
	if code != 0 || env.Data.Effective {
		t.Fatalf("expected disabled by default, got code=%d %s", code, raw)
	}

	env, code, raw = runAutoUpdateCmd(t, "setup", "auto-update", "on")
	if code != 0 || !env.Data.Effective || !strings.Contains(env.Summary, "enabled") {
		t.Fatalf("expected enable success, got code=%d %s", code, raw)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), `"auto_update": true`) {
		t.Fatalf("expected auto_update persisted, got %s", saved)
	}

	env, code, raw = runAutoUpdateCmd(t, "setup", "auto-update")
	if code != 0 || !env.Data.Effective || !env.Data.Config {
		t.Fatalf("expected enabled status, got code=%d %s", code, raw)
	}

	env, code, raw = runAutoUpdateCmd(t, "setup", "auto-update", "off")
	if code != 0 || env.Data.Effective || !strings.Contains(env.Summary, "disabled") {
		t.Fatalf("expected disable success, got code=%d %s", code, raw)
	}
}

func TestSetupAutoUpdateUsageErrors(t *testing.T) {
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("BASELOOP_STATE", t.TempDir())
	useOfflineTransport(t)

	var out bytes.Buffer
	if code := Run([]string{"setup", "auto-update", "bogus", "--json"}, &out, &out); code != 2 {
		t.Fatalf("expected exit 2 for bogus argument, got %d: %s", code, out.String())
	}
	out.Reset()
	if code := Run([]string{"setup", "auto-update", "on", "extra", "--json"}, &out, &out); code != 2 {
		t.Fatalf("expected exit 2 for extra argument, got %d: %s", code, out.String())
	}
}

func TestAutoUpdateEnabledPrecedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("BASELOOP_CONFIG", configPath)

	// Default: off.
	if autoUpdateEnabled() {
		t.Fatal("expected disabled with no config")
	}

	// Config on.
	if err := os.WriteFile(configPath, []byte(`{"api_url":"https://api.test","auto_update":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !autoUpdateEnabled() {
		t.Fatal("expected enabled via config")
	}

	// Env override beats config in both directions.
	t.Setenv("BASELOOP_AUTO_UPDATE", "0")
	if autoUpdateEnabled() {
		t.Fatal("expected env 0 to override config true")
	}
	t.Setenv("BASELOOP_AUTO_UPDATE", "")
	if err := os.WriteFile(configPath, []byte(`{"api_url":"https://api.test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	if !autoUpdateEnabled() {
		t.Fatal("expected env 1 to override config false")
	}

	// Unparseable override is ignored, falling through to config.
	t.Setenv("BASELOOP_AUTO_UPDATE", "banana")
	if autoUpdateEnabled() {
		t.Fatal("expected unparseable env override to fall through to config (off)")
	}

	// The global opt-out kills the pipeline regardless of everything else.
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	t.Setenv("BASELOOP_NO_UPDATE_CHECK", "1")
	if autoUpdateEnabled() {
		t.Fatal("expected BASELOOP_NO_UPDATE_CHECK to disable auto-update")
	}
}

func TestAutoUpdateEnabledCorruptConfigReadsOff(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("BASELOOP_CONFIG", configPath)
	if err := os.WriteFile(configPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if autoUpdateEnabled() {
		t.Fatal("expected corrupt config to read as disabled")
	}
}

func TestSetupSkillsClearsPartialRecordOnly(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HERMES_HOME", "")
	// fakeClaude already prepends its stub dir to PATH; its return value is
	// the log path, not a PATH entry.
	fakeClaude(t, "exit 0")

	// A fully successful skills refresh is the recovery a partial record
	// asks for: it clears.
	recordAutoUpdateFailure("v0.2.0", "claude missing", true)
	var out bytes.Buffer
	if code := Run([]string{"setup", "skills", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected setup skills success, got %d: %s", code, out.String())
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected partial record cleared by successful setup skills")
	}

	// A full (non-partial) record is about a failed swap; setup skills must
	// not clear it.
	recordAutoUpdateFailure("v0.3.0", "download failed", false)
	out.Reset()
	if code := Run([]string{"setup", "skills", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected setup skills success, got %d: %s", code, out.String())
	}
	if rec, ok := readAutoUpdateFailure(); !ok || rec.Partial {
		t.Fatalf("expected full record to survive setup skills, got %+v ok=%v", rec, ok)
	}
}

func TestSetupAutoUpdateStateReportsLayers(t *testing.T) {
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("BASELOOP_STATE", t.TempDir())
	useOfflineTransport(t)

	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	env, code, raw := runAutoUpdateCmd(t, "setup", "auto-update")
	if code != 0 || !env.Data.Effective || env.Data.EnvOverride != "1" {
		t.Fatalf("expected env override surfaced, got code=%d %s", code, raw)
	}

	t.Setenv("BASELOOP_NO_UPDATE_CHECK", "1")
	env, code, raw = runAutoUpdateCmd(t, "setup", "auto-update")
	if code != 0 || env.Data.Effective {
		t.Fatalf("expected kill switch to win, got code=%d %s", code, raw)
	}
	if !strings.Contains(raw, "update_check_disabled") {
		t.Fatalf("expected update_check_disabled surfaced, got %s", raw)
	}
}
