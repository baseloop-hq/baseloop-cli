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
	return string(source)
}
