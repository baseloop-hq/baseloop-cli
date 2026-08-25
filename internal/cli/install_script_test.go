package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestUnixInstallerIgnoresPollutedProcessPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"
	scriptPath := patchedInstallerScript(t, fakeRelease(t, platform, version))
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = cleanInstallerEnv(home, "/bin/sh", version, "PATH="+filepath.Join(home, "bin")+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "baseloop")); err != nil {
		t.Fatalf("expected baseloop in ~/.local/bin: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "baseloop")); !os.IsNotExist(err) {
		t.Fatalf("expected no baseloop in polluted ~/bin, stat err=%v", err)
	}

	profile, err := os.ReadFile(filepath.Join(home, ".profile"))
	if err != nil {
		t.Fatalf("expected installer to persist PATH in .profile: %v\n%s", err, out)
	}
	if !strings.Contains(string(profile), `export PATH="`+filepath.Join(home, ".local", "bin")+`:$PATH"`) {
		t.Fatalf("expected .profile to add ~/.local/bin, got:\n%s", profile)
	}
}

// A release too old to know `setup install` is moved into place by the
// installer itself, which must then take the CLI's upgrade lock the same way:
// a live lock (here held by this test process) blocks the swap for the
// configured wait, and once it is gone the install proceeds and the lock is
// released again.
func TestUnixInstallerLegacyFallbackHonorsUpgradeLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"
	scriptPath := patchedInstallerScript(t, fakeReleaseWith(t, platform, version, legacyStubBinary))
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(stateDir, "upgrade.lock")
	liveLock := []byte(`{"pid":` + strconv.Itoa(os.Getpid()) + `,"started_at":"` + time.Now().UTC().Format(time.RFC3339) + `"}`)
	if err := os.WriteFile(lockPath, liveLock, 0o600); err != nil {
		t.Fatal(err)
	}
	env := cleanInstallerEnv(home, "/bin/sh", version, "BASELOOP_STATE="+stateDir, "BASELOOP_INSTALL_LOCK_WAIT=2")

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("installer must not swap under a live upgrade lock:\n%s", out)
	}
	if !strings.Contains(string(out), "upgrade is in progress") {
		t.Fatalf("expected the lock to be named as the reason, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "baseloop")); !os.IsNotExist(err) {
		t.Fatalf("nothing may be installed while the lock is held, stat err=%v\n%s", err, out)
	}
	if got, _ := os.ReadFile(lockPath); string(got) != string(liveLock) {
		t.Fatalf("a live lock must not be taken over or removed, got %q", got)
	}

	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", scriptPath)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer failed once the lock was released: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "baseloop")); err != nil {
		t.Fatalf("expected the legacy fallback to install the binary: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("the fallback must release the lock it took, stat err=%v", err)
	}
}

// Downgrading to a release that predates `setup install` while a modern
// release is installed: the installed binary performs the locked install of
// the older one, so the pin is recorded in the same critical section as the
// swap instead of after the lock is released.
func TestUnixInstallerLegacyDowngradeUsesInstalledModernBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"
	scriptPath := patchedInstallerScript(t, fakeReleaseWith(t, platform, version, legacyStubBinary))
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A modern release already installed: it knows `setup install` and, like
	// the real one, records the policy alongside the swap. The marker proves
	// this binary, not the shell fallback, performed the install.
	modern := []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then echo baseloop 0.9.0; exit 0; fi
if [ "$1" = "setup" ] && [ "$2" = "install" ]; then
  src=""; dst=""; policy=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --source) src="$2"; shift 2 ;;
      --target) dst="$2"; shift 2 ;;
      --policy) policy="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  printf '{"schema":1,"install_policy":"%s"}\n' "$policy" > "$BASELOOP_STATE/manifest.json"
  printf '%s' "$dst" > "$BASELOOP_STATE/installed-by-modern"
  cp "$src" "$dst" && chmod +x "$dst"
  exit $?
fi
exit 0
`)
	installed := filepath.Join(binDir, "baseloop")
	if err := os.WriteFile(installed, modern, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = cleanInstallerEnv(home, "/bin/sh", version, "BASELOOP_STATE="+stateDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}
	marker, err := os.ReadFile(filepath.Join(stateDir, "installed-by-modern"))
	if err != nil {
		t.Fatalf("expected the installed modern binary to perform the locked install: %v\n%s", err, out)
	}
	if string(marker) != installed {
		t.Fatalf("expected the modern binary to replace itself at %s, got target %q", installed, marker)
	}
	if got, _ := os.ReadFile(installed); string(got) != string(legacyStubBinary) {
		t.Fatalf("expected the older release in place after the downgrade, got:\n%s", got)
	}
	if manifest, _ := os.ReadFile(filepath.Join(stateDir, "manifest.json")); !strings.Contains(string(manifest), `"install_policy":"pinned"`) {
		t.Fatalf("expected the pin recorded by the locked install, got %q", manifest)
	}
	if strings.Contains(string(out), "upgrade is in progress") {
		t.Fatalf("the shell lock fallback must not run when the installed binary can do the locked install:\n%s", out)
	}
}

func TestUnixInstallerUsesInteractiveShellPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(`export PATH="$HOME/bin:$PATH"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", patchedInstallerScript(t, fakeRelease(t, platform, version)))
	cmd.Env = cleanInstallerEnv(home, fakeZsh(t), version, "ZDOTDIR=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(home, "bin", "baseloop")); err != nil {
		t.Fatalf("expected baseloop in interactive-shell ~/bin: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "baseloop")); !os.IsNotExist(err) {
		t.Fatalf("expected no baseloop in fallback ~/.local/bin, stat err=%v", err)
	}
}

func TestUnixInstallerUsesLinuxBashrcPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	arch := runtime.GOARCH
	switch arch {
	case "amd64", "arm64":
	default:
		t.Skipf("unsupported test architecture %s", arch)
	}

	platform := "linux_" + arch
	version := "0.1.0"

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(`export PATH="$HOME/bin:$PATH"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "uname"), []byte("#!/bin/sh\ncase \"$1\" in\n  -s) echo Linux ;;\n  -m) echo "+arch+" ;;\n  *) echo Linux ;;\nesac\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", patchedInstallerScript(t, fakeRelease(t, platform, version)))
	cmd.Env = cleanInstallerEnv(home, fakeBash(t), version, "PATH="+fakeBin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(home, "bin", "baseloop")); err != nil {
		t.Fatalf("expected baseloop in Linux bashrc-configured ~/bin: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "baseloop")); !os.IsNotExist(err) {
		t.Fatalf("expected no baseloop in fallback ~/.local/bin, stat err=%v", err)
	}
}

func TestUnixInstallerPreservesConfiguredPATHPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"

	home := t.TempDir()
	for _, dir := range []string{filepath.Join(home, "bin"), filepath.Join(home, ".local", "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "bin", "baseloop"), []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(`export PATH="$HOME/bin:$HOME/.local/bin:$PATH"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", patchedInstallerScript(t, fakeRelease(t, platform, version)))
	cmd.Env = cleanInstallerEnv(home, fakeZsh(t), version, "ZDOTDIR=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}

	installed, err := os.ReadFile(filepath.Join(home, "bin", "baseloop"))
	if err != nil {
		t.Fatalf("expected baseloop in first PATH dir ~/bin: %v\n%s", err, out)
	}
	if string(installed) == "#!/bin/sh\necho old\n" {
		t.Fatalf("expected installer to replace old executable in first PATH dir")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "baseloop")); !os.IsNotExist(err) {
		t.Fatalf("expected no baseloop installed behind earlier ~/bin entry, stat err=%v", err)
	}
}

func TestUnixInstallerDoesNotGlobConfiguredPATHEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(`export PATH="$HOME/*:$PATH"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", patchedInstallerScript(t, fakeRelease(t, platform, version)))
	cmd.Env = cleanInstallerEnv(home, fakeZsh(t), version, "ZDOTDIR=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "baseloop")); err != nil {
		t.Fatalf("expected baseloop in fallback ~/.local/bin, not glob-expanded ~/bin: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", "baseloop")); !os.IsNotExist(err) {
		t.Fatalf("expected no baseloop in glob-expanded ~/bin, stat err=%v", err)
	}
}

func TestUnixInstallerPreservesZDOTDIR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"

	home := t.TempDir()
	zdotdir := filepath.Join(home, ".config", "zsh")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(zdotdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zdotdir, ".zshrc"), []byte(`export PATH="$HOME/bin:$PATH"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", patchedInstallerScript(t, fakeRelease(t, platform, version)))
	cmd.Env = cleanInstallerEnv(home, fakeZsh(t), version, "ZDOTDIR="+zdotdir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(home, "bin", "baseloop")); err != nil {
		t.Fatalf("expected baseloop in ZDOTDIR-configured ~/bin: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("expected installer not to write ignored HOME .zshrc, stat err=%v", err)
	}
}

func TestUnixInstallerWritesZDOTDIRRc(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer regression")
	}

	platform := installerPlatform(t)
	version := "0.1.0"

	home := t.TempDir()
	zdotdir := filepath.Join(home, ".config", "zsh")
	if err := os.MkdirAll(zdotdir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", patchedInstallerScript(t, fakeRelease(t, platform, version)))
	cmd.Env = cleanInstallerEnv(home, fakeZsh(t), version, "ZDOTDIR="+zdotdir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, out)
	}

	rc, err := os.ReadFile(filepath.Join(zdotdir, ".zshrc"))
	if err != nil {
		t.Fatalf("expected installer to write ZDOTDIR .zshrc: %v\n%s", err, out)
	}
	if !strings.Contains(string(rc), `export PATH="`+filepath.Join(home, ".local", "bin")+`:$PATH"`) {
		t.Fatalf("expected ZDOTDIR .zshrc to add ~/.local/bin, got:\n%s", rc)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatalf("expected installer not to write HOME .zshrc, stat err=%v", err)
	}
}

func TestUnixInstallerLoginShellProbeClosesStdin(t *testing.T) {
	source := readInstallerScript(t)
	want := `"$shell_bin" "$shell_flag" "printf '\n${marker}%s' \"\$PATH\"" </dev/null 2>/dev/null`
	if !strings.Contains(source, want) {
		t.Fatalf("login shell PATH probe must redirect stdin from /dev/null so piped installs cannot be drained by shell startup files")
	}
}

func TestUnixInstallerPromptsForExistingAccountBeforeAuth(t *testing.T) {
	source := readInstallerScript(t)

	checks := map[string]string{
		"account prompt":      "Do you already have a Baseloop account?",
		"tty prompt read":     "read -r answer </dev/tty",
		"signup auth path":    "auth_args=(auth login --signup)",
		"signup fallback":     "baseloop auth login --signup",
		"api url auth path":   `auth_args+=(--api-url "$BASELOOP_API_URL")`,
		"existing auth login": `"$binary" "${auth_args[@]}" </dev/null`,
	}
	for name, want := range checks {
		if !strings.Contains(source, want) {
			t.Fatalf("installer missing %s: %q", name, want)
		}
	}

	// Presence is not enough: the prompt must run before the auth command so
	// the answer can shape auth_args.
	promptIdx := strings.Index(source, "Do you already have a Baseloop account?")
	authIdx := strings.Index(source, `"$binary" "${auth_args[@]}" </dev/null`)
	if promptIdx > authIdx {
		t.Fatalf("account prompt (offset %d) must precede the auth invocation (offset %d)", promptIdx, authIdx)
	}
}

func TestWindowsInstallerPromptsForExistingAccountBeforeAuth(t *testing.T) {
	source := readWindowsInstallerScript(t)

	checks := map[string]string{
		"account prompt":            "Do you already have a Baseloop account?",
		"signup auth path":          "$authArgs = @('auth', 'login', '--signup')",
		"signup fallback":           "baseloop auth login --signup",
		"api url auth path":         "$authArgs += @('--api-url', $ApiUrl)",
		"argument splatting":        "& $InstalledBinary @authArgs",
		"signup explanation":        "create one and connect this CLI",
		"pending workflow function": "function Invoke-PendingWorkflow",
		"pending workflow handoff":  "Info 'Connected your Baseloop account'\n  Invoke-PendingWorkflow",
	}
	for name, want := range checks {
		if !strings.Contains(source, want) {
			t.Fatalf("Windows installer missing %s: %q", name, want)
		}
	}

	// Ordering: prompt shapes $authArgs, the auth call runs, then the pending
	// workflow handoff fires on success.
	promptIdx := strings.Index(source, "Do you already have a Baseloop account?")
	authIdx := strings.Index(source, "& $InstalledBinary @authArgs")
	handoffIdx := strings.Index(source, "Info 'Connected your Baseloop account'\n  Invoke-PendingWorkflow")
	if promptIdx > authIdx || authIdx > handoffIdx {
		t.Fatalf("Windows installer ordering broken (prompt=%d auth=%d handoff=%d)", promptIdx, authIdx, handoffIdx)
	}
}

func TestInstallersLaunchOnlySessionScopedWorkflowPrompts(t *testing.T) {
	unix := readInstallerScript(t)
	windows := readWindowsInstallerScript(t)

	// Both launchers must refuse to fall back to the shared state-dir prompt
	// file (stale replay from an unrelated signup) and must never launch a
	// flag-shaped prompt as an agent argv.
	unixChecks := map[string]string{
		"session-scoped prompt file": `[[ -n "$prompt_file" ]] || return 0`,
		"flag-shaped prompt guard":   "case \"$prompt\" in\n    -*)",
	}
	for name, want := range unixChecks {
		if !strings.Contains(unix, want) {
			t.Fatalf("installer missing %s: %q", name, want)
		}
	}
	if strings.Contains(unix, `prompt_file="$state_dir/workflow-prompt"`) {
		t.Fatal("installer must not fall back to the shared default workflow-prompt path")
	}

	windowsChecks := map[string]string{
		"session-scoped prompt file": "if (-not $PromptFile) {\n    return\n  }",
		"flag-shaped prompt guard":   "if ($prompt.StartsWith('-')) {",
		"control character strip":    `$Prompt = $Prompt -replace '[\x00-\x1f\x7f]', ''`,
	}
	for name, want := range windowsChecks {
		if !strings.Contains(windows, want) {
			t.Fatalf("Windows installer missing %s: %q", name, want)
		}
	}
	if strings.Contains(windows, "$promptFile = Join-Path (Get-StateDir) 'workflow-prompt'") {
		t.Fatal("Windows installer must not fall back to the shared default workflow-prompt path")
	}
}

func installerPlatform(t *testing.T) string {
	t.Helper()

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	switch goos {
	case "darwin", "linux":
	default:
		t.Skipf("unsupported Unix installer platform %s/%s", goos, goarch)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		t.Skipf("unsupported Unix installer architecture %s/%s", goos, goarch)
	}
	return goos + "_" + goarch
}

func fakeRelease(t *testing.T, platform, version string) string {
	t.Helper()
	return fakeReleaseWith(t, platform, version, modernStubBinary)
}

// modernStubBinary models the release binary's installer contract: it
// reports its version, and `setup install` puts --source at --target the way
// the real locked install does. Everything else (receipt, skills) is a no-op.
var modernStubBinary = []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then echo baseloop 0.1.0; exit 0; fi
if [ "$1" = "setup" ] && [ "$2" = "install" ]; then
  src=""; dst=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --source) src="$2"; shift 2 ;;
      --target) dst="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  cp "$src" "$dst" && chmod +x "$dst"
  exit $?
fi
exit 0
`)

// legacyStubBinary models a release that predates `setup install`: the CLI
// answers an unknown setup target with usage exit 2.
var legacyStubBinary = []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then echo baseloop 0.1.0; exit 0; fi
if [ "$1" = "setup" ] && [ "$2" = "install" ]; then echo "USAGE: unknown setup target: install" >&2; exit 2; fi
exit 0
`)

func fakeReleaseWith(t *testing.T, platform, version string, binary []byte) string {
	t.Helper()

	archiveName := "baseloop_" + version + "_" + platform + ".tar.gz"
	releaseDir := t.TempDir()
	archiveBytes := fakeBaseloopArchive(t, binary)
	if err := os.WriteFile(filepath.Join(releaseDir, archiveName), archiveBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(archiveBytes)
	checksums := hex.EncodeToString(sum[:]) + "  " + archiveName + "\n"
	if err := os.WriteFile(filepath.Join(releaseDir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	return releaseDir
}

func fakeBaseloopArchive(t *testing.T, content []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	if err := tw.WriteHeader(&tar.Header{Name: "baseloop", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fakeZsh(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "zsh")
	if err := os.WriteFile(path, []byte(`#!/bin/sh
flag="$1"
shift
case "$flag" in
  *l*)
    if [ -f "${ZDOTDIR:-$HOME}/.zprofile" ]; then
      . "${ZDOTDIR:-$HOME}/.zprofile"
    fi
    ;;
esac
case "$flag" in
  *i*)
    if [ -f "${ZDOTDIR:-$HOME}/.zshrc" ]; then
      . "${ZDOTDIR:-$HOME}/.zshrc"
    fi
    ;;
esac
exec /bin/sh -c "$1"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeBash(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "bash")
	if err := os.WriteFile(path, []byte(`#!/bin/sh
flag="$1"
shift
case "$flag" in
  *l*)
    if [ -f "$HOME/.bash_profile" ]; then
      . "$HOME/.bash_profile"
    fi
    ;;
esac
case "$flag" in
  *i*)
    case "$flag" in
      *l*) ;;
      *)
        if [ -f "$HOME/.bashrc" ]; then
          . "$HOME/.bashrc"
        fi
        ;;
    esac
    ;;
esac
exec /bin/sh -c "$1"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func cleanInstallerEnv(home, shellPath, version string, extra ...string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"SHELL=" + shellPath,
		"BASELOOP_VERSION=" + version,
		"BASELOOP_SKIP_SETUP=1",
		"BASELOOP_SKIP_AUTH=1",
		"NO_COLOR=1",
	}
	return append(env, extra...)
}

func patchedInstallerScript(t *testing.T, releaseDir string) string {
	t.Helper()

	source := readInstallerScript(t)
	baseURL := "file://" + releaseDir
	text := source
	text = strings.ReplaceAll(text,
		`local base_url="https://github.com/${REPO}/releases/download/v${version}"`,
		`local base_url="`+baseURL+`"`,
	)
	text = strings.ReplaceAll(text,
		`url="https://github.com/${REPO}/releases/download/v${version}/${archive}"`,
		`url="`+baseURL+`/${archive}"`,
	)
	if strings.Contains(text, "https://github.com/${REPO}/releases/download") {
		t.Fatal("installer release URLs were not fully patched")
	}

	scriptPath := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(scriptPath, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	return scriptPath
}

func readInstallerScript(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	source, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// CI on Windows checks out with CRLF; multi-line substring assertions
	// expect LF.
	return strings.ReplaceAll(string(source), "\r\n", "\n")
}

func readWindowsInstallerScript(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	source, err := os.ReadFile(filepath.Join(root, "scripts", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	// CI on Windows checks out with CRLF; multi-line substring assertions
	// expect LF.
	return strings.ReplaceAll(string(source), "\r\n", "\n")
}

func TestInstallersRecordReceiptAndCheckAuthPorcelain(t *testing.T) {
	unix := readInstallerScript(t)
	windows := readWindowsInstallerScript(t)

	unixChecks := map[string]string{
		"pinned default placeholder": `PINNED_DEFAULT_VERSION=""`,
		"user pin detection":         `[[ -n "$VERSION" ]] && USER_PINNED_VERSION=1`,
		"receipt call":               `setup receipt --policy "$policy"`,
		"porcelain pre-check":        `auth status --porcelain`,
	}
	for name, want := range unixChecks {
		if !strings.Contains(unix, want) {
			t.Fatalf("installer missing %s: %q", name, want)
		}
	}

	windowsChecks := map[string]string{
		"pinned default placeholder": `$PinnedDefaultVersion = ''`,
		"receipt call":               `setup receipt --policy $policy`,
		"porcelain pre-check":        `auth status --porcelain`,
	}

	// A user-pinned install saves the auto-update preference but never
	// self-updates; announcing "enabled" there would mislead the operator.
	for label, source := range map[string]string{"unix": unix, "windows": windows} {
		if !strings.Contains(source, "it stays off while this install is pinned to") {
			t.Fatalf("%s installer must report auto-update as deferred on a pinned install", label)
		}
	}

	// The extracted binary installs itself under the CLI's upgrade lock
	// (`setup install`), recording the receipt in the same critical section;
	// the plain move/copy survives only as the fallback for older releases,
	// and the installed version is compared to the one requested.
	for label, tc := range map[string]struct{ source, locked, viaInstalled, noUpdate, fallbackLock, fallback, versionCheck string }{
		"unix": {unix,
			`setup install --source "${tmp_dir}/${binary}" --target "${BIN_DIR}/${binary}" --policy "$policy"`,
			`"${BIN_DIR}/${binary}" setup install --source "${tmp_dir}/${binary}"`,
			`BASELOOP_NO_UPDATE_CHECK=1 "${tmp_dir}/${binary}" setup install`,
			`acquire_legacy_upgrade_lock ||`,
			`mv "${tmp_dir}/${binary}" "${BIN_DIR}/${binary}"`,
			`"${reported##* }" != "$expected"`},
		"windows": {windows,
			`setup install --source $binaryPath --target $installedBinary --policy $policy`,
			`& $installedBinary setup install --source $binaryPath`,
			`$env:BASELOOP_NO_UPDATE_CHECK = '1'`,
			`Enter-LegacyUpgradeLock`,
			`Copy-Item -Force $binaryPath $installedBinary`,
			`$reportedVersion -ne $resolvedVersion`},
	} {
		lockedIdx, fallbackIdx := strings.Index(tc.source, tc.locked), strings.Index(tc.source, tc.fallback)
		if lockedIdx < 0 || fallbackIdx < 0 || lockedIdx > fallbackIdx {
			t.Fatalf("%s installer must try the locked install (offset %d) before the plain-move fallback (offset %d)", label, lockedIdx, fallbackIdx)
		}
		// A downgrade to a pre-command release is delegated to the installed
		// modern binary before any unlocked-receipt path is considered.
		if viaIdx := strings.Index(tc.source, tc.viaInstalled); viaIdx < lockedIdx || viaIdx > fallbackIdx {
			t.Fatalf("%s installer must retry setup install through the installed binary (offset %d) between the extracted attempt (%d) and the fallback (%d)", label, viaIdx, lockedIdx, fallbackIdx)
		}
		// The temporary binary must not start an update check of its own.
		if noUpdateIdx := strings.Index(tc.source, tc.noUpdate); noUpdateIdx < 0 || noUpdateIdx > lockedIdx {
			t.Fatalf("%s installer must suppress update checks for setup install: %q", label, tc.noUpdate)
		}
		// The fallback move must sit under the upgrade lock too.
		if fallbackLockIdx := strings.LastIndex(tc.source[:fallbackIdx], tc.fallbackLock); fallbackLockIdx < 0 {
			t.Fatalf("%s installer must take the upgrade lock before the fallback swap: %q", label, tc.fallbackLock)
		}
		if !strings.Contains(tc.source, tc.versionCheck) {
			t.Fatalf("%s installer must compare the installed version to the requested one: %q", label, tc.versionCheck)
		}
	}
	for name, want := range windowsChecks {
		if !strings.Contains(windows, want) {
			t.Fatalf("Windows installer missing %s: %q", name, want)
		}
	}

	// The porcelain pre-check must run before the account prompt, so an
	// already-authenticated machine is never asked to sign in again.
	for label, source := range map[string]string{"unix": unix, "windows": windows} {
		porcelainIdx := strings.Index(source, "auth status --porcelain")
		promptIdx := strings.Index(source, "Do you already have a Baseloop account?")
		if porcelainIdx < 0 || promptIdx < 0 || porcelainIdx > promptIdx {
			t.Fatalf("%s installer: porcelain pre-check (offset %d) must precede the account prompt (offset %d)", label, porcelainIdx, promptIdx)
		}
	}
}

func TestInstallersOfferAgentPermissions(t *testing.T) {
	unix := readInstallerScript(t)
	windows := readWindowsInstallerScript(t)

	for label, source := range map[string]string{"unix": unix, "windows": windows} {
		for name, want := range map[string]string{
			"skip env var":  "BASELOOP_SKIP_AGENT_PERMISSIONS",
			"pre-check":     "setup agent-permissions --check",
			"prompt":        "Let agents run baseloop commands without asking each time?",
			"default no":    "[y/N]",
			"claude named":  "Bash(baseloop:*)",
			"codex named":   "default.rules",
			"recovery hint": "Later: baseloop setup agent-permissions",
		} {
			if !strings.Contains(source, want) {
				t.Fatalf("%s installer missing %s: %q", label, name, want)
			}
		}

		// The prompt belongs between agent setup (so the skills it unlocks
		// are in place) and the sign-in flow (the last interactive step).
		skillsIdx := strings.Index(source, "setup skills")
		permIdx := strings.Index(source, "setup agent-permissions --check")
		authIdx := strings.Index(source, "auth status --porcelain")
		if skillsIdx < 0 || permIdx < 0 || authIdx < 0 || skillsIdx > permIdx || permIdx > authIdx {
			t.Fatalf("%s installer: expected setup skills (%d) < agent-permissions (%d) < auth porcelain (%d)", label, skillsIdx, permIdx, authIdx)
		}
	}
}

func TestGenInstallerAssetsStampsPinnedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("generator runs under bash")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	generator := filepath.Join(root, "scripts", "gen-installer-assets.sh")

	dist := t.TempDir()
	out, err := exec.Command("bash", generator, "9.9.9", dist).CombinedOutput()
	if err != nil {
		t.Fatalf("generator failed: %v: %s", err, out)
	}

	sh, err := os.ReadFile(filepath.Join(dist, "install-cli"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sh), `PINNED_DEFAULT_VERSION="9.9.9"`) {
		t.Fatal("expected the Unix installer asset pinned to 9.9.9")
	}
	if strings.Contains(string(sh), `PINNED_DEFAULT_VERSION=""`) {
		t.Fatal("expected the placeholder fully replaced in the Unix asset")
	}

	ps, err := os.ReadFile(filepath.Join(dist, "install-cli.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ps), `$PinnedDefaultVersion = '9.9.9'`) {
		t.Fatal("expected the Windows installer asset pinned to 9.9.9")
	}

	// Non-semver input must fail loudly instead of shipping a broken pin.
	if out, err := exec.Command("bash", generator, "not-a-version", t.TempDir()).CombinedOutput(); err == nil {
		t.Fatalf("expected the generator to reject a non-semver version, got: %s", out)
	}
}
