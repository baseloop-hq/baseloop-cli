package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// setSpawnSeam intercepts background spawns and returns a counter, mirroring
// the upgradeTargetPath test-seam pattern.
func setSpawnSeam(t *testing.T) *int {
	t.Helper()
	old := spawnBackgroundUpgrade
	t.Cleanup(func() { spawnBackgroundUpgrade = old })
	calls := new(int)
	spawnBackgroundUpgrade = func() error {
		*calls++
		return nil
	}
	return calls
}

func TestBackgroundUpgradeCmdContract(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_REPO", "evil/mirror")
	t.Setenv("BASELOOP_SKIP_SETUP", "1")
	t.Setenv("BASELOOP_TOKEN", "secret-token")
	// Windows env names are case-insensitive, so a case-variant must be
	// stripped too (on Unix this is simply a distinct variable that the
	// uniform case-insensitive rule also drops).
	t.Setenv("Baseloop_Token", "sneaky-secret")
	t.Setenv(upgradeChildEnvVar, "1") // leaked marker must not duplicate

	cmd, logFile, err := backgroundUpgradeCmd()
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) != 3 || cmd.Args[0] != self || cmd.Args[1] != "upgrade" || cmd.Args[2] != "--background" {
		t.Fatalf("unexpected args %v", cmd.Args)
	}
	if cmd.Dir != stateDir {
		t.Fatalf("expected Dir=%s, got %s", stateDir, cmd.Dir)
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("expected detach attributes to be applied")
	}
	if cmd.Stdout != logFile || cmd.Stderr != logFile {
		t.Fatal("expected stdio wired to the log file")
	}
	if logFile.Name() != filepath.Join(stateDir, autoUpdateLogFile) {
		t.Fatalf("unexpected log path %s", logFile.Name())
	}

	// Assert against the production drop list itself so a newly added drop
	// var cannot silently escape test coverage. Case-insensitive, matching
	// the filter's own rule.
	dropped := make(map[string]bool, len(childEnvDropVars))
	for _, key := range childEnvDropVars {
		dropped[strings.ToLower(key)] = true
	}
	marker := 0
	for _, kv := range cmd.Env {
		key, value, _ := strings.Cut(kv, "=")
		if dropped[strings.ToLower(key)] {
			t.Fatalf("expected %s stripped from child env", key)
		}
		if strings.EqualFold(key, upgradeChildEnvVar) {
			marker++
			if value != "1" {
				t.Fatalf("expected marker=1, got %q", value)
			}
		}
	}
	if marker != 1 {
		t.Fatalf("expected exactly one recursion marker, got %d", marker)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(logFile.Name())
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected 0600 log, got %v", info.Mode().Perm())
		}
	}
}

func TestBackgroundUpgradeCmdAppendsLog(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	logPath := filepath.Join(stateDir, autoUpdateLogFile)
	if err := os.WriteFile(logPath, []byte("winner in flight\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, logFile, err := backgroundUpgradeCmd()
	if err != nil {
		t.Fatal(err)
	}
	logFile.Close()

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "winner in flight") {
		t.Fatalf("expected existing log preserved (O_APPEND, not O_TRUNC), got %q", content)
	}
}

func TestBackgroundUpgradeCmdRequiresStateDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	t.Setenv("BASELOOP_STATE", missing)
	if _, _, err := backgroundUpgradeCmd(); err == nil {
		t.Fatal("expected error with missing state dir")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("expected state dir to stay absent, got %v", err)
	}
}

func TestBackgroundUpgradeCmdPinsRelativeStateDir(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	if err := os.Mkdir("state", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BASELOOP_STATE", "state")

	cmd, logFile, err := backgroundUpgradeCmd()
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	if !filepath.IsAbs(cmd.Dir) {
		t.Fatalf("expected absolute cmd.Dir, got %q", cmd.Dir)
	}
	stateSeen := ""
	for _, kv := range cmd.Env {
		key, value, _ := strings.Cut(kv, "=")
		if strings.EqualFold(key, "BASELOOP_STATE") {
			if stateSeen != "" {
				t.Fatalf("expected exactly one BASELOOP_STATE in child env, got %q and %q", stateSeen, value)
			}
			stateSeen = value
		}
	}
	// The child runs with cmd.Dir = the state dir itself; a relative value
	// would re-resolve beneath it (state/state) and wedge the lock.
	if stateSeen == "" || !filepath.IsAbs(stateSeen) {
		t.Fatalf("expected absolute BASELOOP_STATE pinned in child env, got %q", stateSeen)
	}
	if !filepath.IsAbs(logFile.Name()) {
		t.Fatalf("expected absolute log path, got %q", logFile.Name())
	}
}
