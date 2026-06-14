// Coordination state for background auto-update: the upgrade lock and the
// failure record, both living in the state dir beside version-check.json.
//
// The lock makes "at most one upgrade at a time" true across manual runs,
// detached children, and uninstall. It is an O_CREATE|O_EXCL file rather than
// flock: flock-style locks have documented Windows problems (a held lock file
// cannot be unlinked; byte-range locks block reads), while exclusive-create
// behaves identically on all three OSes. The failure record is what keeps a
// failed background upgrade from silently re-downloading on every command:
// the spawn guard consults it, the notice surfaces it, doctor explains it.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/baseloop-hq/baseloop-cli/internal/state"
)

const (
	upgradeLockFile       = "upgrade.lock"
	autoUpdateFailureFile = "auto-update-failure.json"
	autoUpdateLogFile     = "auto-update.log"
	// upgradeLockStaleAfter is the hard wall-clock timeout after which a lock
	// may be taken over. Generous against the manual upgrade's own 3-minute
	// context: a lock this old belongs to a crashed process — or to a machine
	// that suspended mid-download, which is why takeover alone is not enough
	// and the holder re-checks ownership right before the swap (stillOwned).
	upgradeLockStaleAfter = 10 * time.Minute
	// autoUpdateRetryAfter / autoUpdateMaxAttempts implement the R11 retry
	// policy: a transient failure must not wedge a fleet that never reads
	// notices until the next release, but a persistent failure must not
	// download the release daily forever either.
	autoUpdateRetryAfter  = 24 * time.Hour
	autoUpdateMaxAttempts = 3
)

func statePath(name string) (string, error) {
	dir, err := state.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// writeStateFileAtomic writes via temp-file-then-rename in the target's own
// directory so a concurrent reader can never observe a torn file. It fails —
// deliberately does not create — when the directory is missing: a write after
// uninstall must not resurrect the state dir.
func writeStateFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".baseloop-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// --- upgrade lock ---

type upgradeLock struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// upgradeLockHandle is a held lock. The file content identifies the holder so
// a usurped holder can detect the takeover (stillOwned) before swapping.
type upgradeLockHandle struct {
	path string
	lk   upgradeLock
}

// lockHeldError reports a live lock. The path is part of the message by
// design: when auto-update wedges, the lock file's location is what lets a
// user self-serve instead of filing "updates stopped working".
type lockHeldError struct{ path string }

func (e *lockHeldError) Error() string {
	return "another upgrade is already in progress (lock: " + e.path + "; stale locks are taken over after " + upgradeLockStaleAfter.String() + ")"
}

func upgradeLockPath() (string, error) { return statePath(upgradeLockFile) }

// acquireUpgradeLock takes the exclusive upgrade lock. It never creates the
// state dir: a missing dir means uninstall ran (or the install is broken),
// and either way an upgrade must not resurrect it — callers treat that error
// as "skip", not "mkdir".
func acquireUpgradeLock() (*upgradeLockHandle, error) {
	path, err := upgradeLockPath()
	if err != nil {
		return nil, err
	}
	lk := upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()}
	data, err := json.Marshal(lk)
	if err != nil {
		return nil, err
	}
	create := func() error {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		// A failed identity write must fail the acquisition: stillOwned()
		// would later read the torn lock as "not mine" and abort the upgrade
		// after the download, while the identity-less lock blocks every other
		// attempt until the stale timeout. Remove and report instead.
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return werr
		}
		// Close immediately so a wedged-but-alive holder never pins the file
		// open on Windows, where an open file cannot be unlinked by takeover.
		if cerr := f.Close(); cerr != nil {
			_ = os.Remove(path)
			return cerr
		}
		return nil
	}
	err = create()
	if err == nil {
		return &upgradeLockHandle{path: path, lk: lk}, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("cannot create upgrade lock %s: %w", path, err)
	}
	if !upgradeLockIsStale(path) {
		return nil, &lockHeldError{path: path}
	}
	// Stale takeover: delete then one O_EXCL retry. Losing the retry means
	// another process won the same takeover race — treat as held.
	_ = os.Remove(path)
	if err := create(); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, &lockHeldError{path: path}
		}
		return nil, fmt.Errorf("cannot create upgrade lock %s: %w", path, err)
	}
	return &upgradeLockHandle{path: path, lk: lk}, nil
}

// release removes the lock only while this handle still owns it. An
// unconditional remove would let a usurped holder's deferred release delete
// the usurper's live lock on its way out, re-opening exactly the concurrency
// the takeover existed to close. A torn own-write reads as not-owned and
// leaves the file for the staleness timeout to clean up.
func (h *upgradeLockHandle) release() {
	if h.stillOwned() {
		_ = os.Remove(h.path)
	}
}

// stillOwned re-reads the lock and reports whether it still carries this
// holder's identity. Lock staleness is wall-clock while a process's own
// deadlines are monotonic, so a machine that suspends mid-download can wake
// with a live holder whose lock has already been taken over; checking
// ownership immediately before replaceBinary is what keeps that usurped
// holder from racing the usurper's swap. Any read or parse failure reads as
// "not owned": the only safe response to doubt is aborting the swap.
func (h *upgradeLockHandle) stillOwned() bool {
	lk, ok := readUpgradeLock(h.path)
	if !ok {
		return false
	}
	return lk.PID == h.lk.PID && lk.StartedAt.Equal(h.lk.StartedAt)
}

func readUpgradeLock(path string) (upgradeLock, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return upgradeLock{}, false
	}
	var lk upgradeLock
	if err := json.Unmarshal(data, &lk); err != nil {
		return upgradeLock{}, false
	}
	return lk, true
}

// upgradeLockIsStale reports whether the lock at path may be taken over.
// Age past the hard timeout is the authority; a verifiably dead holder is a
// best-effort early-out. A live-looking PID never blocks the age-based
// takeover — PIDs recycle, so liveness can vouch for nothing.
func upgradeLockIsStale(path string) bool {
	var startedAt time.Time
	lk, ok := readUpgradeLock(path)
	if ok && !lk.StartedAt.IsZero() {
		startedAt = lk.StartedAt
	} else if info, err := os.Stat(path); err == nil {
		// Torn or empty content: fall back to the file's mtime.
		startedAt = info.ModTime()
	} else {
		// Vanished between EEXIST and here: whoever held it released; let the
		// caller's retry settle it.
		return true
	}
	if time.Since(startedAt) > upgradeLockStaleAfter {
		return true
	}
	return ok && lk.PID > 0 && !pidAlive(lk.PID)
}

// freshUpgradeLockExists is the spawn guard's advisory read: a live lock means
// an upgrade is already in flight, so spawning another child (which would lose
// on O_EXCL anyway) is pure waste. A stat, never an acquisition — the child's
// O_EXCL remains the only authority.
func freshUpgradeLockExists() bool {
	path, err := upgradeLockPath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return !upgradeLockIsStale(path)
}

// pidAlive is best-effort liveness. On Windows FindProcess opens a real
// process handle, so an error means the PID is gone (the dead-PID early-out
// works there too) and success means alive — the GOOS branch below only
// avoids the unsupported Signal(0) probe on a live handle. Caveat: an
// ACCESS_DENIED open on Windows also reads as dead; the state dir is
// user-private so a foreign-user holder is not expected, and the pre-swap
// stillOwned re-check backstops any premature takeover. On Unix FindProcess
// always succeeds and signal 0 probes existence (EPERM still means "exists").
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	sigErr := proc.Signal(syscall.Signal(0))
	return sigErr == nil || errors.Is(sigErr, syscall.EPERM)
}

// --- failure record ---

// autoUpdateFailure records why the last background upgrade attempt failed,
// keyed to the release it was trying to reach. Partial means the binary swap
// succeeded but the post-swap plugin refresh did not — recovery is
// `baseloop setup skills`, not another upgrade.
type autoUpdateFailure struct {
	Target        string    `json:"target"`
	Attempts      int       `json:"attempts"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	Error         string    `json:"error"`
	Partial       bool      `json:"partial,omitempty"`
}

func autoUpdateFailurePath() (string, error) { return statePath(autoUpdateFailureFile) }

func readAutoUpdateFailure() (autoUpdateFailure, bool) {
	path, err := autoUpdateFailurePath()
	if err != nil {
		return autoUpdateFailure{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return autoUpdateFailure{}, false
	}
	var rec autoUpdateFailure
	if err := json.Unmarshal(data, &rec); err != nil {
		// Torn or corrupt records read as absent; the atomic write makes this
		// rare and the worst case is one extra upgrade attempt.
		return autoUpdateFailure{}, false
	}
	return rec, true
}

// recordAutoUpdateFailure writes or advances the failure record. Attempts
// accumulate per target so the retry policy can go dormant; a new target
// starts the count over. Best-effort like all state-dir bookkeeping — and it
// never creates the state dir, so a child that lost the uninstall race cannot
// resurrect it.
func recordAutoUpdateFailure(target, message string, partial bool) {
	path, err := autoUpdateFailurePath()
	if err != nil {
		return
	}
	rec := autoUpdateFailure{Target: target, Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: message, Partial: partial}
	if prev, ok := readAutoUpdateFailure(); ok && prev.Target == target {
		rec.Attempts = prev.Attempts + 1
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = writeStateFileAtomic(path, data)
}

func clearAutoUpdateFailure() {
	path, err := autoUpdateFailurePath()
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

// blocksTarget reports whether this record should stop a new background
// attempt at target. Partial records never block: the binary already updated,
// and what remains (plugin refresh) is not fixed by re-upgrading. A record
// whose own target does not parse as a version (a resolve failure recorded
// before any tag was known) applies the retry policy to every target —
// treating it as "different release, re-armed" would spawn a doomed retry on
// every single command.
func (f autoUpdateFailure) blocksTarget(target string) bool {
	if f.Partial {
		return false
	}
	if _, parses := versionParts(f.Target); parses && f.Target != target {
		return false
	}
	if f.Attempts >= autoUpdateMaxAttempts {
		return true
	}
	return time.Since(f.LastAttemptAt) < autoUpdateRetryAfter
}

// sanitizeRecordText makes record-derived text safe to print. The error
// strings originate from network responses (URLs, HTTP statuses, release
// asset names), which makes them attacker-influenceable: control and ANSI
// sequences are stripped and the length is capped so a hostile string cannot
// drive the terminal or impersonate CLI output.
func sanitizeRecordText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	const maxLen = 200
	if runes := []rune(out); len(runes) > maxLen {
		out = string(runes[:maxLen]) + "..."
	}
	return out
}
