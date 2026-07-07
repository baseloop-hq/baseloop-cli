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
	"strings"
	"testing"
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

	archiveName := "baseloop_" + version + "_" + platform + ".tar.gz"
	releaseDir := t.TempDir()
	archiveBytes := fakeBaseloopArchive(t)
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

func fakeBaseloopArchive(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	content := []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo baseloop 0.1.0; fi\nexit 0\n")
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
