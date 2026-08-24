package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// agentPermissionsTestHome isolates both agent roots under a temp home.
// CODEX_HOME is always pinned so a developer's real Codex root is never the
// target, even when the test wants Codex absent.
func agentPermissionsTestHome(t *testing.T, claude, codex bool) (settings, rules string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	if claude {
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if codex {
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "rules", "default.rules")
}

func readClaudeAllow(t *testing.T, settings string) []any {
	t.Helper()
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("settings are not valid JSON: %v\n%s", err, data)
	}
	perms, _ := doc["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	return allow
}

func assertClaudeAllow(t *testing.T, settings string, want ...string) {
	t.Helper()
	allow := readClaudeAllow(t, settings)
	if len(allow) != len(want) {
		t.Fatalf("expected allow list %q, got %v", want, allow)
	}
	for i := range want {
		if allow[i] != want[i] {
			t.Fatalf("allow[%d]: expected %q, got %q", i, want[i], allow[i])
		}
	}
}

func runAgentPermissions(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Run(append([]string{"setup", "agent-permissions"}, append(args, "--json")...), &out, &out)
	return code, out.String()
}

func TestAgentPermissionsClaudeCreatesSettingsWhenMissing(t *testing.T) {
	settings, _ := agentPermissionsTestHome(t, true, false)

	if code, out := runAgentPermissions(t, "--check"); code != 1 || !strings.Contains(out, "AGENT_PERMISSION_ABSENT") {
		t.Fatalf("expected --check to exit 1 with AGENT_PERMISSION_ABSENT before the grant, got %d: %s", code, out)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("--check must not create settings.json, stat err = %v", err)
	}

	code, out := runAgentPermissions(t)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	if !strings.Contains(out, "Claude Code can now run baseloop") {
		t.Fatalf("expected the summary to name Claude Code, got %s", out)
	}
	assertClaudeAllow(t, settings, claudePermissionEntry, claudePermissionPathEntry)
	if _, err := os.Stat(settings + ".baseloop-backup"); !os.IsNotExist(err) {
		t.Fatalf("no backup expected when settings.json did not exist, stat err = %v", err)
	}

	if code, out := runAgentPermissions(t, "--check"); code != 0 {
		t.Fatalf("expected --check to exit 0 after the grant, got %d: %s", code, out)
	}
}

func TestAgentPermissionsClaudePreservesExistingSettings(t *testing.T) {
	settings, _ := agentPermissionsTestHome(t, true, false)
	original := `{
  "model": "opus",
  "permissions": {
    "allow": ["Bash(git status:*)", "Read(<home>/notes/**)"],
    "deny": ["Bash(rm -rf:*)"]
  },
  "env": {"MAX_THINKING_TOKENS": 10000, "RATIO": 1.50},
  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo a && b"}]}]}
}
`
	if err := os.WriteFile(settings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if code, out := runAgentPermissions(t); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}

	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	assertClaudeAllow(t, settings, "Bash(git status:*)", "Read(<home>/notes/**)", claudePermissionEntry, claudePermissionPathEntry)
	for _, keep := range []string{`"model": "opus"`, `"Bash(rm -rf:*)"`, `"MAX_THINKING_TOKENS": 10000`, `"RATIO": 1.50`, `"echo a && b"`, `"Read(<home>/notes/**)"`} {
		if !strings.Contains(text, keep) {
			t.Fatalf("rewritten settings lost %s:\n%s", keep, text)
		}
	}
	if strings.Contains(text, `\u003c`) || strings.Contains(text, `\u0026`) {
		t.Fatalf("rewritten settings must not HTML-escape values:\n%s", text)
	}
	if !strings.HasPrefix(text, "{\n  \"") || !strings.HasSuffix(text, "}\n") {
		t.Fatalf("expected two-space indented JSON with a trailing newline, got:\n%s", text)
	}
	info, err := os.Stat(settings)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected the original 0600 mode to be preserved, got %o", info.Mode().Perm())
	}
	backup, err := os.ReadFile(settings + ".baseloop-backup")
	if err != nil {
		t.Fatalf("expected a pre-edit backup: %v", err)
	}
	if string(backup) != original {
		t.Fatalf("backup must hold the original file byte-for-byte, got:\n%s", backup)
	}
}

func TestAgentPermissionsClaudeRefusesUnexpectedSettings(t *testing.T) {
	cases := map[string]string{
		"malformed JSON":        `{"permissions": {"allow": [`,
		"trailing garbage":      `{"permissions": {}} {}`,
		"top-level array":       `[]`,
		"top-level null":        `null`,
		"permissions not obj":   `{"permissions": ["Bash(baseloop:*)"]}`,
		"allow not array":       `{"permissions": {"allow": "Bash(baseloop:*)"}}`,
		"allow non-string item": `{"permissions": {"allow": [{"tool": "Bash"}]}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			settings, _ := agentPermissionsTestHome(t, true, false)
			if err := os.WriteFile(settings, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}

			code, out := runAgentPermissions(t)
			if code != 1 || !strings.Contains(out, "AGENT_SETTINGS_ERROR") {
				t.Fatalf("expected exit 1 with AGENT_SETTINGS_ERROR, got %d: %s", code, out)
			}
			after, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != content {
				t.Fatalf("settings.json must be left untouched, got:\n%s", after)
			}
			if _, err := os.Stat(settings + ".baseloop-backup"); !os.IsNotExist(err) {
				t.Fatalf("no backup expected when the edit is refused, stat err = %v", err)
			}
		})
	}
}

func TestAgentPermissionsClaudeTreatsBlankFileAsEmpty(t *testing.T) {
	settings, _ := agentPermissionsTestHome(t, true, false)
	if err := os.WriteFile(settings, []byte("\xEF\xBB\xBF \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := runAgentPermissions(t); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	assertClaudeAllow(t, settings, claudePermissionEntry, claudePermissionPathEntry)
}

// Machines set up by the install.md runbook before the CLI owned this edit
// carry the legacy spelling; it counts as granted and is left exactly as is.
func TestAgentPermissionsClaudeAcceptsLegacyRunbookEntries(t *testing.T) {
	for name, content := range map[string]string{
		"runbook pair":     `{"permissions": {"allow": ["Bash(baseloop *)", "Bash(export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop *)"]}}` + "\n",
		"legacy bare only": `{"permissions": {"allow": ["Bash(baseloop *)"]}}` + "\n",
		"canonical only":   `{"permissions": {"allow": ["Bash(baseloop:*)"]}}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			settings, _ := agentPermissionsTestHome(t, true, false)
			if err := os.WriteFile(settings, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if code, out := runAgentPermissions(t, "--check"); code != 0 {
				t.Fatalf("expected --check to treat the existing entry as granted, got %d: %s", code, out)
			}
			if code, out := runAgentPermissions(t); code != 0 || !strings.Contains(out, "can already run baseloop") {
				t.Fatalf("expected an already-granted no-op, got %d: %s", code, out)
			}
			after, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != content {
				t.Fatalf("an already-granted file must not be rewritten:\n%s", after)
			}
		})
	}
}

// The PATH-prefixed entry alone is not the grant, but it is not duplicated
// when the bare entry is added next to it.
func TestAgentPermissionsClaudeKeepsExistingPathEntry(t *testing.T) {
	settings, _ := agentPermissionsTestHome(t, true, false)
	if err := os.WriteFile(settings, []byte(`{"permissions": {"allow": ["`+claudePermissionPathEntry+`"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := runAgentPermissions(t, "--check"); code != 1 {
		t.Fatalf("expected the PATH entry alone not to count as granted, got %d: %s", code, out)
	}
	if code, out := runAgentPermissions(t); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	assertClaudeAllow(t, settings, claudePermissionPathEntry, claudePermissionEntry)
}

func TestAgentPermissionsClaudeWritesThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs extra privileges on Windows")
	}
	settings, _ := agentPermissionsTestHome(t, true, false)
	real := filepath.Join(filepath.Dir(filepath.Dir(settings)), "dotfiles", "claude-settings.json")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte(`{"permissions": {"allow": []}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, settings); err != nil {
		t.Fatal(err)
	}

	if code, out := runAgentPermissions(t); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	info, err := os.Lstat(settings)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("settings.json symlink must survive the edit")
	}
	assertClaudeAllow(t, real, claudePermissionEntry, claudePermissionPathEntry)
	if _, err := os.Stat(real + ".baseloop-backup"); err != nil {
		t.Fatalf("expected the backup next to the symlink target: %v", err)
	}
}

func TestAgentPermissionsCodexCreatesRulesWhenMissing(t *testing.T) {
	_, rules := agentPermissionsTestHome(t, false, true)

	if code, out := runAgentPermissions(t, "--check"); code != 1 || !strings.Contains(out, "Not yet allowed to run baseloop without permission prompts: Codex.") {
		t.Fatalf("expected --check to exit 1 naming Codex, got %d: %s", code, out)
	}
	if _, err := os.Stat(rules); !os.IsNotExist(err) {
		t.Fatalf("--check must not create default.rules, stat err = %v", err)
	}

	code, out := runAgentPermissions(t)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	if !strings.Contains(out, "Codex can now run baseloop") || !strings.Contains(out, "Restart any running Codex session") {
		t.Fatalf("expected a Codex summary with the restart note, got %s", out)
	}
	data, err := os.ReadFile(rules)
	if err != nil {
		t.Fatalf("expected default.rules to be created: %v", err)
	}
	if string(data) != codexPermissionComment+"\n"+codexPermissionRule+"\n" {
		t.Fatalf("unexpected rules content:\n%s", data)
	}
	if code, out := runAgentPermissions(t, "--check"); code != 0 {
		t.Fatalf("expected --check to exit 0 after the grant, got %d: %s", code, out)
	}
}

func TestAgentPermissionsCodexAppendsWithoutRewriting(t *testing.T) {
	_, rules := agentPermissionsTestHome(t, false, true)
	original := "prefix_rule(pattern=[\"gh\", \"api\"], decision=\"allow\")\nprefix_rule(pattern=[\"baseloop\", \"tools\"], decision=\"allow\")\nprefix_rule(pattern=[\"rg\", \"-o\", \"href=\\\"[^\\\"]*\\\"\"], decision=\"allow\")"
	if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out := runAgentPermissions(t); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	data, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	want := original + "\n\n" + codexPermissionComment + "\n" + codexPermissionRule + "\n"
	if string(data) != want {
		t.Fatalf("expected the original text kept byte-for-byte with the rule appended:\n%s", data)
	}
	backup, err := os.ReadFile(rules + ".baseloop-backup")
	if err != nil {
		t.Fatalf("expected a pre-edit backup: %v", err)
	}
	if string(backup) != original {
		t.Fatalf("backup must hold the original file byte-for-byte, got:\n%s", backup)
	}

	// The narrower ["baseloop", "tools"] rule above must not have counted as
	// the grant, but the appended one now does: a second run is a no-op.
	if code, out := runAgentPermissions(t); code != 0 || !strings.Contains(out, "can already run baseloop") {
		t.Fatalf("expected an already-granted no-op, got %d: %s", code, out)
	}
	after, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != want {
		t.Fatalf("second run must not change default.rules:\n%s", after)
	}
}

func TestAgentPermissionsCodexDetectsExistingRuleVariants(t *testing.T) {
	for name, content := range map[string]string{
		"codex style":     `prefix_rule(pattern=["baseloop"], decision="allow")` + "\n",
		"spaced":          `prefix_rule( pattern = [ "baseloop" ], decision = "allow" )` + "\n",
		"trailing comma":  `prefix_rule(pattern=["baseloop",], decision="allow", justification="x")` + "\n",
		"multiline":       "prefix_rule(\n    pattern = [\"baseloop\"],\n    decision = \"allow\",\n)\n",
		"no trailing eol": `prefix_rule(pattern=["baseloop"], decision="allow")`,
	} {
		t.Run(name, func(t *testing.T) {
			_, rules := agentPermissionsTestHome(t, false, true)
			if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rules, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if code, out := runAgentPermissions(t, "--check"); code != 0 {
				t.Fatalf("expected the existing rule to count as granted, got %d: %s", code, out)
			}
			if code, out := runAgentPermissions(t); code != 0 {
				t.Fatalf("expected exit 0, got %d: %s", code, out)
			}
			after, err := os.ReadFile(rules)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != content {
				t.Fatalf("an already-granted file must not be rewritten:\n%s", after)
			}
		})
	}
}

func TestAgentPermissionsCodexRefusesConflictingRule(t *testing.T) {
	_, rules := agentPermissionsTestHome(t, false, true)
	content := `prefix_rule(pattern=["baseloop"], decision="forbidden")` + "\n"
	if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out := runAgentPermissions(t)
	if code != 1 || !strings.Contains(out, "AGENT_SETTINGS_ERROR") || !strings.Contains(out, "forbidden") {
		t.Fatalf("expected exit 1 naming the forbidden rule, got %d: %s", code, out)
	}
	after, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != content {
		t.Fatalf("a conflicting file must be left untouched:\n%s", after)
	}
}

func TestAgentPermissionsGrantsEveryAgentPresent(t *testing.T) {
	settings, rules := agentPermissionsTestHome(t, true, true)

	code, out := runAgentPermissions(t)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	if !strings.Contains(out, "Claude Code and Codex can now run baseloop without permission prompts.") {
		t.Fatalf("expected the summary to name both agents, got %s", out)
	}
	assertClaudeAllow(t, settings, claudePermissionEntry, claudePermissionPathEntry)
	if data, err := os.ReadFile(rules); err != nil || !strings.Contains(string(data), codexPermissionRule) {
		t.Fatalf("expected the Codex rule to be written, err=%v content=%s", err, data)
	}

	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"claude", "codex"} {
		leg, _ := envelope.Data[key].(map[string]any)
		if leg["present"] != true || leg["granted"] != true || leg["changed"] != true {
			t.Fatalf("expected %s leg present/granted/changed, got %v", key, leg)
		}
	}

	// Granting Claude again on a machine where only Codex is missing the rule
	// reports the split honestly.
	if err := os.Remove(rules); err != nil {
		t.Fatal(err)
	}
	if code, out := runAgentPermissions(t); code != 0 || !strings.Contains(out, "Codex can now run baseloop without permission prompts. Claude Code already could.") {
		t.Fatalf("expected a split summary, got %d: %s", code, out)
	}
}

func TestAgentPermissionsFailsWhenNoAgentPresent(t *testing.T) {
	settings, rules := agentPermissionsTestHome(t, false, false)
	code, out := runAgentPermissions(t)
	if code != 1 || !strings.Contains(out, "AGENT_NOT_FOUND") {
		t.Fatalf("expected exit 1 with AGENT_NOT_FOUND, got %d: %s", code, out)
	}
	for _, path := range []string{settings, rules} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("nothing may be written when no agent is present: %s exists (err=%v)", path, err)
		}
	}
}

// The rule text only matters if Codex itself accepts it. Exercised where a
// codex binary is available (developer machines); CI without codex skips.
func TestAgentPermissionsCodexRuleAcceptedByCodex(t *testing.T) {
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not on PATH")
	}
	_, rules := agentPermissionsTestHome(t, false, true)
	if err := os.MkdirAll(filepath.Dir(rules), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, []byte(`prefix_rule(pattern=["git", "status"], decision="allow")`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := runAgentPermissions(t); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}

	check := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(codexBin, append([]string{"execpolicy", "check", "--rules", rules, "--"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("codex execpolicy check %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if out := check("baseloop", "tables", "list"); !strings.Contains(out, `"decision":"allow"`) {
		t.Fatalf("expected codex to allow baseloop commands, got %s", out)
	}
	if out := check("git", "push"); strings.Contains(out, `"decision":"allow"`) {
		t.Fatalf("the rule must not allow unrelated commands, got %s", out)
	}
}
