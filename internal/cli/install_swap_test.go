package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func setupInstallTestEnv(t *testing.T) (source, target string) {
	t.Helper()
	t.Setenv("BASELOOP_STATE", t.TempDir())
	dir := t.TempDir()
	source = filepath.Join(dir, "extracted")
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(t.TempDir(), "bin", "baseloop")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	return source, target
}

func runSetupInstall(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Run(append([]string{"setup", "install"}, append(args, "--json")...), &out, &out)
	return code, out.String()
}

func TestSetupInstallPlacesBinaryAndRecordsPolicyUnderLock(t *testing.T) {
	source, target := setupInstallTestEnv(t)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	code, out := runSetupInstall(t, "--source", source, "--target", target, "--policy", "pinned")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), "echo new") {
		t.Fatalf("expected the source content at %s, err=%v content=%q", target, err, data)
	}
	if info, _ := os.Stat(target); runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed binary must be executable, mode %o", info.Mode().Perm())
	}
	if got := installPolicy(); got != installPolicyPinned {
		t.Fatalf("expected pinned policy recorded, got %q", got)
	}
	lockPath, _ := upgradeLockPath()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("upgrade lock must be released after the install, stat err = %v", err)
	}

	// Replacing an existing binary takes the rename-aside path and leaves no
	// staging or backup files behind.
	if err := os.WriteFile(source, []byte("#!/bin/sh\necho newer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := runSetupInstall(t, "--source", source, "--target", target, "--policy", "managed"); code != 0 {
		t.Fatalf("expected exit 0 on replace, got %d: %s", code, out)
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "echo newer") {
		t.Fatalf("expected the replaced content, got %q", data)
	}
	for _, leftover := range []string{target + ".new", target + ".old"} {
		if _, err := os.Stat(leftover); !os.IsNotExist(err) {
			t.Fatalf("expected no leftover %s, stat err = %v", leftover, err)
		}
	}
	if got := installPolicy(); got != installPolicyManaged {
		t.Fatalf("expected managed policy after the second install, got %q", got)
	}
}

// The whole point of the command: a live updater holding the lock blocks the
// swap, and the installer waits for it rather than racing it.
func TestSetupInstallWaitsForLiveUpgradeLock(t *testing.T) {
	source, target := setupInstallTestEnv(t)
	stateDir := os.Getenv("BASELOOP_STATE")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recordInstallPolicy(installPolicyManaged); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireUpgradeLock()
	if err != nil {
		t.Fatal(err)
	}

	code, out := runSetupInstall(t, "--source", source, "--target", target, "--policy", "pinned", "--wait", "300ms")
	if code != 1 || !strings.Contains(out, "UPGRADE_IN_PROGRESS") {
		t.Fatalf("expected UPGRADE_IN_PROGRESS while the lock is held, got %d: %s", code, out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("nothing may be installed while the lock is held, stat err = %v", err)
	}
	if got := installPolicy(); got != installPolicyManaged {
		t.Fatalf("policy must be untouched while the lock is held, got %q", got)
	}
	if !lock.stillOwned() {
		t.Fatal("a waiting installer must never take over a live lock")
	}

	// Release shortly after the installer starts waiting: it must pick the
	// lock up and finish instead of giving up.
	go func() {
		time.Sleep(400 * time.Millisecond)
		lock.release()
	}()
	if code, out := runSetupInstall(t, "--source", source, "--target", target, "--policy", "pinned", "--wait", "10s"); code != 0 {
		t.Fatalf("expected the install to proceed once the lock was released, got %d: %s", code, out)
	}
	if got := installPolicy(); got != installPolicyPinned {
		t.Fatalf("expected pinned policy after the install, got %q", got)
	}
}

// A failed swap restores whatever policy was there before, so a failed
// pinned-to-pinned reinstall keeps the pin and a failed fresh install leaves
// no receipt behind.
func TestSetupInstallRestoresPreviousPolicyWhenSwapFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("relies on a read-only directory refusing writes")
	}
	for name, previous := range map[string]string{"pinned stays pinned": installPolicyPinned, "no receipt stays absent": ""} {
		t.Run(name, func(t *testing.T) {
			source, target := setupInstallTestEnv(t)
			if previous != "" {
				if err := recordInstallPolicy(previous); err != nil {
					t.Fatal(err)
				}
			}
			binDir := filepath.Dir(target)
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(binDir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(binDir, 0o755) })

			code, out := runSetupInstall(t, "--source", source, "--target", target, "--policy", "pinned")
			if code != 1 || !strings.Contains(out, "INSTALL_FAILED") {
				t.Fatalf("expected INSTALL_FAILED, got %d: %s", code, out)
			}
			if got := installPolicy(); got != previous {
				t.Fatalf("expected the previous policy %q restored, got %q", previous, got)
			}
			lockPath, _ := upgradeLockPath()
			if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
				t.Fatalf("upgrade lock must be released after a failed install, stat err = %v", err)
			}
		})
	}
}

// The extracted binary runs `setup install` before it is installed; an
// update check there would spawn an updater targeting the temp file and
// contend on the very lock the install is taking. The gate is in Run, so it
// holds even without the installers' BASELOOP_NO_UPDATE_CHECK=1.
func TestSetupInstallNeverSpawnsAutoUpdate(t *testing.T) {
	_, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	source := mustWriteFile(t, "extracted", "#!/bin/sh\necho new\n")
	target := filepath.Join(t.TempDir(), "bin", "baseloop")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	if code, out := runSetupInstall(t, "--source", source, "--target", target, "--policy", "managed"); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out)
	}
	var out bytes.Buffer
	if code := Run([]string{"setup", "receipt", "--policy", "managed", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if *spawns != 0 {
		t.Fatalf("installer-owned setup commands must never spawn an updater, got %d spawns", *spawns)
	}

	// Control: the same environment does spawn for an ordinary command, so
	// the zero above is the gate at work rather than a dormant harness.
	runOrdinary(t)
	if *spawns != 1 {
		t.Fatalf("expected the control command to spawn once, got %d", *spawns)
	}
}

// The installers' legacy fallback writes the lock file itself, in the shape
// the CLI expects; the CLI must read that as a live lock held by that PID so
// a background updater that starts mid-install stands down.
func TestShellWrittenUpgradeLockIsHonored(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	lockPath, err := upgradeLockPath()
	if err != nil {
		t.Fatal(err)
	}
	shellLock := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"started_at":"` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `"}`
	if err := os.WriteFile(lockPath, []byte(shellLock), 0o600); err != nil {
		t.Fatal(err)
	}
	if upgradeLockIsStale(lockPath) {
		t.Fatal("a fresh shell-written lock with a live PID must not read as stale")
	}
	_, err = acquireUpgradeLock()
	var held *lockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("expected the CLI to see the shell-written lock as held, got %v", err)
	}
	if got, _ := os.ReadFile(lockPath); string(got) != shellLock {
		t.Fatalf("the shell-written lock must be left in place, got %q", got)
	}
}

func TestSetupInstallUsageErrors(t *testing.T) {
	source, target := setupInstallTestEnv(t)
	for name, args := range map[string][]string{
		"missing target": {"--source", source, "--policy", "pinned"},
		"bad policy":     {"--source", source, "--target", target, "--policy", "sometimes"},
		"missing source": {"--source", filepath.Join(t.TempDir(), "nope"), "--target", target, "--policy", "pinned"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, out := runSetupInstall(t, args...); code != 2 || !strings.Contains(out, "USAGE") {
				t.Fatalf("expected usage exit 2, got %d: %s", code, out)
			}
		})
	}
}
