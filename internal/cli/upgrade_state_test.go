package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcquireUpgradeLockExclusive(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())

	h, err := acquireUpgradeLock()
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if _, err := acquireUpgradeLock(); err == nil {
		t.Fatal("expected second acquire to fail while held")
	} else {
		var held *lockHeldError
		if !errors.As(err, &held) {
			t.Fatalf("expected lockHeldError, got %T: %v", err, err)
		}
		if !strings.Contains(err.Error(), h.path) {
			t.Fatalf("expected lock path in error, got %q", err.Error())
		}
	}
	h.release()
	h2, err := acquireUpgradeLock()
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	h2.release()
}

func writeLockFile(t *testing.T, stateDir string, lk upgradeLock) string {
	t.Helper()
	data, err := json.Marshal(lk)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, upgradeLockFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAcquireUpgradeLockStaleTakeover(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)

	// Old wall-clock age: stale even if the PID looks alive (our own).
	writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC().Add(-upgradeLockStaleAfter - time.Minute)})
	h, err := acquireUpgradeLock()
	if err != nil {
		t.Fatalf("expected stale takeover, got %v", err)
	}
	h.release()

	// Fresh age but verifiably dead PID: the liveness early-out fires on
	// every platform (Windows FindProcess errors for dead PIDs, Unix signal 0
	// reports ESRCH).
	writeLockFile(t, stateDir, upgradeLock{PID: 1 << 30, StartedAt: time.Now().UTC()})
	h, err = acquireUpgradeLock()
	if err != nil {
		t.Fatalf("expected dead-PID takeover, got %v", err)
	}
	h.release()
}

func TestAcquireUpgradeLockFreshLiveStaysHeld(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if _, err := acquireUpgradeLock(); err == nil {
		t.Fatal("expected fresh live lock to block acquisition")
	}
	if !freshUpgradeLockExists() {
		t.Fatal("expected freshUpgradeLockExists to report the live lock")
	}
}

func TestAcquireUpgradeLockNeverCreatesStateDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	t.Setenv("BASELOOP_STATE", missing)
	if _, err := acquireUpgradeLock(); err == nil {
		t.Fatal("expected acquire to fail with a missing state dir")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("expected state dir to stay absent, got %v", err)
	}
}

func TestUpgradeLockStillOwned(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	h, err := acquireUpgradeLock()
	if err != nil {
		t.Fatal(err)
	}
	if !h.stillOwned() {
		t.Fatal("expected freshly acquired lock to be owned")
	}
	// A takeover rewrites the content with a different identity.
	writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid() + 1, StartedAt: time.Now().UTC()})
	if h.stillOwned() {
		t.Fatal("expected usurped lock to read as not owned")
	}
	// Torn content also reads as not owned: doubt aborts the swap.
	if err := os.WriteFile(h.path, []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h.stillOwned() {
		t.Fatal("expected torn lock to read as not owned")
	}
}

func TestFreshUpgradeLockExists(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	if freshUpgradeLockExists() {
		t.Fatal("expected no lock initially")
	}
	writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC().Add(-upgradeLockStaleAfter - time.Minute)})
	if freshUpgradeLockExists() {
		t.Fatal("expected stale lock to not count as fresh")
	}
}

func TestAutoUpdateFailureRecordLifecycle(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())

	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected no record initially")
	}
	clearAutoUpdateFailure() // no-op when absent

	recordAutoUpdateFailure("v0.8.0", "boom", false)
	rec, ok := readAutoUpdateFailure()
	if !ok || rec.Target != "v0.8.0" || rec.Attempts != 1 || rec.Partial {
		t.Fatalf("unexpected record %+v ok=%v", rec, ok)
	}

	// Same target accumulates attempts; a new target restarts the count.
	recordAutoUpdateFailure("v0.8.0", "boom again", false)
	if rec, _ = readAutoUpdateFailure(); rec.Attempts != 2 {
		t.Fatalf("expected attempts=2, got %+v", rec)
	}
	recordAutoUpdateFailure("v0.9.0", "new target", false)
	if rec, _ = readAutoUpdateFailure(); rec.Target != "v0.9.0" || rec.Attempts != 1 {
		t.Fatalf("expected fresh count for new target, got %+v", rec)
	}

	clearAutoUpdateFailure()
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected record cleared")
	}
}

func TestAutoUpdateFailureCorruptReadsAbsent(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	if err := os.WriteFile(filepath.Join(stateDir, autoUpdateFailureFile), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected corrupt record to read as absent")
	}
}

func TestAutoUpdateFailureBlocksTarget(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name string
		rec  autoUpdateFailure
		want bool
	}{
		{"fresh full record blocks", autoUpdateFailure{Target: "v1", Attempts: 1, LastAttemptAt: now}, true},
		{"retry window elapsed re-arms", autoUpdateFailure{Target: "v1", Attempts: 1, LastAttemptAt: now.Add(-autoUpdateRetryAfter - time.Minute)}, false},
		{"attempts exhausted blocks regardless of age", autoUpdateFailure{Target: "v1", Attempts: autoUpdateMaxAttempts, LastAttemptAt: now.Add(-48 * time.Hour)}, true},
		{"different target never blocks", autoUpdateFailure{Target: "v0", Attempts: 3, LastAttemptAt: now}, false},
		{"partial never blocks", autoUpdateFailure{Target: "v1", Attempts: 3, LastAttemptAt: now, Partial: true}, false},
	}
	for _, c := range cases {
		if got := c.rec.blocksTarget("v1"); got != c.want {
			t.Errorf("%s: blocksTarget = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRecordWriteNeverCreatesStateDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	t.Setenv("BASELOOP_STATE", missing)
	recordAutoUpdateFailure("v1", "boom", false)
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("expected record write to not resurrect the state dir, got %v", err)
	}
}

func TestSanitizeRecordText(t *testing.T) {
	in := "download \x1b[31mfailed\x1b[0m at\nhttps://evil.test \x07"
	out := sanitizeRecordText(in)
	if strings.ContainsAny(out, "\x1b\n\x07") {
		t.Fatalf("expected control characters stripped, got %q", out)
	}
	if !strings.Contains(out, "download") || !strings.Contains(out, "https://evil.test") {
		t.Fatalf("expected text preserved, got %q", out)
	}
	long := strings.Repeat("a", 500)
	if got := sanitizeRecordText(long); len([]rune(got)) > 210 {
		t.Fatalf("expected length cap, got %d runes", len([]rune(got)))
	}
}

func TestUpgradeLockReleaseOnlyWhenOwned(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	h, err := acquireUpgradeLock()
	if err != nil {
		t.Fatal(err)
	}
	// A takeover replaced the content: this handle's deferred release must
	// not delete the usurper's live lock on its way out.
	usurper := upgradeLock{PID: os.Getpid() + 1, StartedAt: time.Now().UTC()}
	writeLockFile(t, stateDir, usurper)
	h.release()
	got, ok := readUpgradeLock(filepath.Join(stateDir, upgradeLockFile))
	if !ok || got.PID != usurper.PID {
		t.Fatalf("expected usurper's lock to survive release, got %+v ok=%v", got, ok)
	}
	// An owned release still removes the file.
	_ = os.Remove(filepath.Join(stateDir, upgradeLockFile))
	h2, err := acquireUpgradeLock()
	if err != nil {
		t.Fatal(err)
	}
	h2.release()
	if _, err := os.Stat(filepath.Join(stateDir, upgradeLockFile)); !os.IsNotExist(err) {
		t.Fatalf("expected owned release to remove the lock, got %v", err)
	}
}
