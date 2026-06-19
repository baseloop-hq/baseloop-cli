package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStripPathMarkerRemovesInstallerBlock(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshrc")
	content := strings.Join([]string{
		"export EDITOR=vim",
		"",
		pathMarker,
		`export PATH="$HOME/.local/bin:$PATH"`,
		"alias ll='ls -la'",
		"",
	}, "\n")
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := stripPathMarker(rc)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !changed {
		t.Fatal("expected the file to change")
	}

	got, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"export EDITOR=vim",
		"alias ll='ls -la'",
		"",
	}, "\n")
	if string(got) != want {
		t.Fatalf("unexpected rc content.\n got: %q\nwant: %q", string(got), want)
	}
}

func TestStripPathMarkerRemovesManagedPathBlock(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshrc")
	content := strings.Join([]string{
		"export EDITOR=vim",
		"",
		pathBeginMarker,
		`export PATH="$HOME/.local/bin:$PATH"`,
		pathEndMarker,
		"alias ll='ls -la'",
		"",
	}, "\n")
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := stripPathMarker(rc)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !changed {
		t.Fatal("expected the file to change")
	}

	got, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"export EDITOR=vim",
		"alias ll='ls -la'",
		"",
	}, "\n")
	if string(got) != want {
		t.Fatalf("unexpected rc content.\n got: %q\nwant: %q", string(got), want)
	}
}

func TestStripPathMarkerErrorsOnUnclosedManagedPathBlock(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshrc")
	content := strings.Join([]string{
		"export EDITOR=vim",
		"",
		pathBeginMarker,
		`export PATH="$HOME/.local/bin:$PATH"`,
		"alias ll='ls -la'",
		"",
	}, "\n")
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := stripPathMarker(rc)
	if err == nil {
		t.Fatal("expected unclosed block error")
	}
	if changed {
		t.Fatal("unclosed block must not report a successful change")
	}
	got, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("file should be untouched on error, got %q", string(got))
	}
}

func TestStripPathMarkerNoMarkerIsNoop(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".bashrc")
	content := "export EDITOR=vim\nalias g=git\n"
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := stripPathMarker(rc)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if changed {
		t.Fatal("expected no change when marker is absent")
	}
	got, _ := os.ReadFile(rc)
	if string(got) != content {
		t.Fatalf("file should be untouched, got %q", string(got))
	}
}

func TestStripPathMarkerPreservesUnrelatedLineAfterMarker(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshrc")
	content := strings.Join([]string{
		"export EDITOR=vim",
		"",
		pathMarker,
		"alias bl='baseloop'",
		"",
	}, "\n")
	if err := os.WriteFile(rc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := stripPathMarker(rc)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if !changed {
		t.Fatal("expected the file to change")
	}

	got, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"export EDITOR=vim",
		"alias bl='baseloop'",
		"",
	}, "\n")
	if string(got) != want {
		t.Fatalf("unexpected rc content.\n got: %q\nwant: %q", string(got), want)
	}
}

func TestStripPathMarkerMissingFileIsNoop(t *testing.T) {
	changed, err := stripPathMarker(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if changed {
		t.Fatal("expected no change for missing file")
	}
}

func TestUninstallRemovesPathMarkerAndState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH markers live in unix shell rc files")
	}
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)
	if err := os.WriteFile(filepath.Join(stateDir, "manifest.json"), []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rc := filepath.Join(home, ".zshrc")
	rcBody := "export EDITOR=vim\n\n" + pathMarker + "\nexport PATH=\"$HOME/.local/bin:$PATH\"\n"
	if err := os.WriteFile(rc, []byte(rcBody), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	// --keep-binary so the test never deletes the go test binary.
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}

	rcGot, _ := os.ReadFile(rc)
	if strings.Contains(string(rcGot), pathMarker) {
		t.Fatalf("expected PATH marker stripped, got %q", string(rcGot))
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("expected state dir removed, stat err: %v", err)
	}
}

func TestUninstallRemovesZDOTDIRPathMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH markers live in unix shell rc files")
	}
	home := t.TempDir()
	zdotdir := filepath.Join(home, ".config", "zsh")
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", zdotdir)
	t.Setenv("BASELOOP_STATE", stateDir)
	if err := os.MkdirAll(zdotdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "manifest.json"), []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rc := filepath.Join(zdotdir, ".zshrc")
	rcBody := "export EDITOR=vim\n\n" + pathBeginMarker + "\nexport PATH=\"$HOME/.local/bin:$PATH\"\n" + pathEndMarker + "\n"
	if err := os.WriteFile(rc, []byte(rcBody), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}

	rcGot, _ := os.ReadFile(rc)
	if strings.Contains(string(rcGot), pathBeginMarker) || strings.Contains(string(rcGot), pathEndMarker) {
		t.Fatalf("expected ZDOTDIR PATH block stripped, got %q", string(rcGot))
	}
	if want := "export EDITOR=vim\n"; string(rcGot) != want {
		t.Fatalf("expected preserved content %q, got %q", want, string(rcGot))
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("expected HOME .zshrc to remain absent, stat err: %v", err)
	}
}

func TestUninstallLeavesLocalSkillDirectoriesAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	skillDir := filepath.Join(home, ".claude", "skills", "baseloop-gtm-build")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Lstat(skillDir); err != nil {
		t.Fatalf("uninstall must not remove local Claude files: %v", err)
	}
}

func TestUninstallRemovesOwnedClaudeEntrySkillOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())

	entrySkill, err := installBaseloopClaudeSkill()
	if err != nil {
		t.Fatal(err)
	}
	userSkill := filepath.Join(home, ".claude", "skills", "baseloop-gtm-build")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Dir(entrySkill)); !os.IsNotExist(err) {
		t.Fatalf("expected owned entry skill removed, stat err: %v", err)
	}
	if _, err := os.Stat(userSkill); err != nil {
		t.Fatalf("expected unrelated Claude directory to survive: %v", err)
	}
}

func TestUninstallRemovesOwnedCodexEntrySkillAndLeavesCodexConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE on Windows
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // codex binary absent: removal must not depend on it

	entrySkill, err := installBaseloopCodexSkill()
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.WriteFile(configPath, []byte("[plugins.\"baseloop-gtm@baseloop-gtm-plugin\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Dir(entrySkill)); !os.IsNotExist(err) {
		t.Fatalf("expected owned Codex entry skill removed, stat err: %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("uninstall must never touch ~/.codex/config.toml: %v", err)
	}
}

func TestUninstallPreservesModifiedCodexEntrySkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	entrySkill, err := installBaseloopCodexSkill()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrySkill, []byte("# user edits\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(entrySkill); err != nil {
		t.Fatalf("expected modified Codex entry skill preserved: %v", err)
	}
}

func TestUninstallLeavesSymlinkedSkillsDirAlone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_STATE", t.TempDir())

	// ~/.codex/skills is a symlink to a dotfiles-style dir holding other
	// skills; removing the owned entry skill must not unlink it.
	realDir := filepath.Join(home, "dotfiles", "codex-skills")
	if err := os.MkdirAll(filepath.Join(realDir, "other-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".codex", "skills")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := installBaseloopCodexSkill(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("symlinked skills dir must survive uninstall: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to remain a symlink", link)
	}
	if _, err := os.Stat(filepath.Join(realDir, "other-skill")); err != nil {
		t.Fatalf("other skills must survive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realDir, "baseloop")); !os.IsNotExist(err) {
		t.Fatalf("owned entry skill should still be removed through the symlink, stat err: %v", err)
	}
}

func TestOwnedEntrySkillContentFallbackIsPerAgent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(baseloopCodexSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	// No marker file: ownership falls back to a content comparison.
	if !ownedBaseloopEntrySkillDir(dir, baseloopCodexSkill) {
		t.Fatalf("content matching the Codex constant should count as owned")
	}
	if ownedBaseloopEntrySkillDir(dir, baseloopClaudeSkill) {
		t.Fatalf("the Claude constant must not claim a dir holding the Codex skill")
	}
}

func TestUninstallPreservesModifiedClaudeEntrySkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())

	entrySkill, err := installBaseloopClaudeSkill()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrySkill, []byte("# My Baseloop CLI Agent Skill\n\nbaseloop setup skills\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(entrySkill); err != nil {
		t.Fatalf("modified entry skill should survive uninstall: %v", err)
	}
}

func TestUninstallDryRunRemovesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("CODEX_HOME", "")

	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(pathMarker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--dry-run", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), `"dryRun": true`) {
		t.Fatalf("expected dryRun true in output, got %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err != nil {
		t.Fatalf("dry run must not remove files: %v", err)
	}
}

func TestUninstallTextOutputIncludesActionableNotes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("CODEX_HOME", "")

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	for _, want := range []string{
		"Baseloop has been uninstalled.",
		"Kept your Baseloop sign-in",
		"Claude and Codex plugin state is managed by each agent's plugin manager",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected uninstall output to contain %q, got %s", want, out.String())
		}
	}
}

func TestUninstallPurgeRemovesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	cfg := filepath.Join(home, ".config", "baseloop", "config.json")
	t.Setenv("BASELOOP_CONFIG", cfg)
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--purge", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("expected config removed under --purge, stat err: %v", err)
	}
}

func TestUninstallPurgeRefusesDirectoryConfigOverride(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("BASELOOP_CONFIG", home)
	if err := os.WriteFile(filepath.Join(stateDir, "manifest.json"), []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--purge", "--keep-binary", "--json"}, &out, &out)
	if code == 0 {
		t.Fatalf("expected refusal for unsafe config override: %s", out.String())
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("home dir must not be removed: %v", err)
	}
}

func TestUninstallStateOverrideRemovesOnlyManifest(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("CODEX_HOME", "")
	if err := os.WriteFile(filepath.Join(stateDir, "manifest.json"), []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code == 0 {
		t.Fatalf("expected non-zero exit because override state dir is not empty: %s", out.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("expected manifest removed, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "keep.txt")); err != nil {
		t.Fatalf("override state sidecar file must remain: %v", err)
	}
}

func TestUninstallRefusesFreshUpgradeLock(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)
	if err := os.WriteFile(filepath.Join(stateDir, "manifest.json"), []byte(`{"schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()})

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 1 {
		t.Fatalf("expected refusal exit 1, got %d: %s", code, out.String())
	}
	// Decode before comparing: JSON escapes Windows path backslashes.
	var envlp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &envlp); err != nil {
		t.Fatalf("invalid envelope %q: %v", out.String(), err)
	}
	if !strings.Contains(envlp.Error.Message, lockPath) {
		t.Fatalf("expected lock path in error, got %s", envlp.Error.Message)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "manifest.json")); err != nil {
		t.Fatalf("expected nothing removed while refused, got %v", err)
	}
}

func TestUninstallTakesOverStaleLockAndRemovesAutoUpdateState(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)

	// Everything auto-update can leave behind, plus a stale lock.
	writeLockFile(t, stateDir, upgradeLock{PID: 1 << 30, StartedAt: time.Now().UTC().Add(-upgradeLockStaleAfter - time.Minute)})
	for name, content := range map[string]string{
		"manifest.json":        `{"schema":1}`,
		versionCheckFile:       `{"checked_at":"2026-06-12T00:00:00Z","latest":"v0.2.0"}`,
		autoUpdateFailureFile:  `{"target":"v0.2.0","attempts":1}`,
		autoUpdateLogFile:      "attempt output\n",
		".baseloop-tmp-a1b2c3": "crashed atomic write leftover",
	} {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--keep-binary", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("expected state dir fully removed, stat err: %v", err)
	}
}

func TestUninstallDryRunIgnoresLock(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", stateDir)
	writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()})

	var out bytes.Buffer
	code := Run([]string{"uninstall", "--dry-run", "--json"}, &out, &out)
	if code != 0 {
		t.Fatalf("expected dry-run exit 0, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, upgradeLockFile)); err != nil {
		t.Fatalf("expected dry run to leave the lock alone, got %v", err)
	}
}
