// Locked install: the installers put the freshly extracted binary in place by
// running `setup install` *from that binary*, so the swap happens under the
// same upgrade lock the background updater takes.
//
// Without that lock there is a real interleaving: a background updater reads
// "not pinned", pauses, the installer publishes a pin and swaps in the
// requested version, and the updater resumes and overwrites it. Holding the
// lock across "record policy, then swap" makes the two atomic with respect to
// each other: an updater already inside its critical section finishes first
// (we wait), and any updater that starts afterwards sees the pin and stands
// down. The policy write remembers what it replaced so a failed swap restores
// it exactly, including a pin that a failed pinned-to-pinned reinstall must
// not release.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/baseloop-hq/baseloop-cli/internal/output"
	"github.com/baseloop-hq/baseloop-cli/internal/state"
)

// installerOwnedSetup reports the hidden setup subcommands the installers
// run while an install is in progress; Run never starts an update check for
// them.
func installerOwnedSetup(rest []string) bool {
	if len(rest) < 2 || rest[0] != "setup" {
		return false
	}
	switch rest[1] {
	case "install", "receipt":
		return true
	}
	return false
}

// installLockWait bounds how long an installer waits for a live updater to
// finish. A manual upgrade's own context is three minutes; a background one
// is usually seconds. Stale locks are taken over regardless.
const installLockWait = 2 * time.Minute

// waitForUpgradeLock retries acquisition while the lock is held by a live
// holder, up to wait. Any other acquisition error is returned at once.
func waitForUpgradeLock(wait time.Duration) (*upgradeLockHandle, error) {
	deadline := time.Now().Add(wait)
	for {
		lock, err := acquireUpgradeLock()
		var held *lockHeldError
		if err == nil || !errors.As(err, &held) || time.Now().After(deadline) {
			return lock, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// installBinary places src at target. An existing target goes through the
// updater's rename-aside swap so a reader never sees a half-written file; a
// fresh install stages next to the target and renames for the same reason.
func installBinary(src, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return replaceBinary(src, target)
	}
	staged := target + ".new"
	if err := copyFileMode(src, staged, 0o755); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Remove(staged)
		return err
	}
	return nil
}

// setupInstall is the hidden `setup install --source <file> --target <file>
// --policy <pinned|managed> [--wait <duration>]` subcommand. Hidden like the
// other installer surfaces: it records installer-owned state and moves
// binaries around, which is nothing an agent or a user should be steered
// into by the catalog.
func setupInstall(args []string, g globals, stdout io.Writer) int {
	fs := flag.NewFlagSet("setup install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "Extracted binary to install")
	target := fs.String("target", "", "Install path")
	policy := fs.String("policy", "", "Install policy to record: pinned or managed")
	wait := fs.Duration("wait", installLockWait, "How long to wait for an in-flight upgrade")
	usage := "Use baseloop setup install --source <file> --target <file> --policy <pinned|managed>."
	if err := fs.Parse(args); err != nil {
		return render(stdout, g, output.Failure("USAGE", err.Error(), usage, nil), 2)
	}
	if *source == "" || *target == "" {
		return render(stdout, g, output.Failure("USAGE", "--source and --target are required", usage, nil), 2)
	}
	switch *policy {
	case installPolicyPinned, installPolicyManaged:
	default:
		return render(stdout, g, output.Failure("USAGE", "install policy must be pinned or managed", usage, nil), 2)
	}
	if info, err := os.Stat(*source); err != nil || !info.Mode().IsRegular() {
		return render(stdout, g, output.Failure("USAGE", "--source must be an existing file: "+*source, usage, nil), 2)
	}
	payload := map[string]any{"source": *source, "target": *target, "policy": *policy}

	// The lock lives in the state dir, which a fresh install does not have yet.
	// Creating it here is correct: this is an install, not an upgrade that
	// must never resurrect an uninstalled state dir.
	stateDir, err := state.Dir()
	if err != nil {
		return render(stdout, g, output.Failure("STATE_ERROR", "Could not resolve the state directory: "+err.Error(), "Check HOME or set BASELOOP_STATE.", payload), 1)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return render(stdout, g, output.Failure("STATE_ERROR", "Could not create the state directory: "+err.Error(), "Check that "+stateDir+" is writable.", payload), 1)
	}
	lock, err := waitForUpgradeLock(*wait)
	if err != nil {
		return render(stdout, g, output.Failure("UPGRADE_IN_PROGRESS", "Could not take the upgrade lock: "+err.Error(), "Wait for the running upgrade to finish, then re-run the installer.", payload), 1)
	}
	defer lock.release()

	previous := installPolicy()
	payload["previousPolicy"] = previous
	if err := recordInstallPolicy(*policy); err != nil {
		return render(stdout, g, output.Failure("STATE_ERROR", "Could not record the install policy: "+err.Error(), "Check that "+stateDir+" is writable.", payload), 1)
	}
	// Same re-check the updater makes: a lock taken over by a usurper (a
	// suspended machine, a stale-timeout race) means the swap is no longer ours.
	if !lock.stillOwned() {
		_ = recordInstallPolicy(previous)
		return render(stdout, g, output.Failure("UPGRADE_IN_PROGRESS", "The upgrade lock was taken over by another process; not swapping.", "Re-run the installer.", payload), 1)
	}
	if err := installBinary(*source, *target); err != nil {
		if restoreErr := recordInstallPolicy(previous); restoreErr != nil {
			err = fmt.Errorf("%v (and the previous install policy %q could not be restored: %v)", err, previous, restoreErr)
		}
		return render(stdout, g, output.Failure("INSTALL_FAILED", "Could not install "+*target+": "+err.Error(), "Check write permission on the install directory, then re-run the installer.", payload), 1)
	}
	return render(stdout, g, output.Success(payload, "Installed "+*target+" ("+*policy+").", nil), 0)
}
