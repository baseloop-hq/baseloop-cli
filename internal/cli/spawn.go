// Detached spawning for background auto-update: launch `baseloop upgrade
// --background` so it survives this process exiting, owns no terminal, and
// writes only to the state-dir log. The platform-specific detach attributes
// live in spawn_unix.go / spawn_windows.go.
package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/baseloop-hq/baseloop-cli/internal/state"
)

// upgradeChildEnvVar marks the spawned upgrade child and everything it execs.
// It does recursion suppression ONLY: it must be an env var because it has to
// survive the exec chain (child → new binary `setup skills` → grandchildren —
// `setup` is not in the notice exclusion list, so this inherited marker is
// what prevents a grandchild spawn). Child-mode behavior is signaled by the
// --background flag instead, so a marker leaked into a long-lived shell can
// never make a manual `baseloop upgrade` exit silently.
const upgradeChildEnvVar = "BASELOOP_UPGRADE_CHILD"

// childEnvDropVars never reach the spawned child:
//   - BASELOOP_RELEASES_API_URL / BASELOOP_REPO: the endpoint pin. The
//     automatic path resolves releases only from the canonical repo —
//     honoring these here would let two injected env vars redirect a binary
//     swap to an endpoint whose same-origin checksums verify anything it
//     serves. Manual `baseloop upgrade` still honors them (consented,
//     foreground; also what tests and mirrors use).
//   - BASELOOP_SKIP_SETUP: an inherited skip would silently hollow out every
//     background plugin refresh while the upgrade reports success.
//   - BASELOOP_TOKEN: the child only talks to GitHub Releases; a live
//     Baseloop credential has no business in its env or, via subprocess
//     output, its log.
var childEnvDropVars = []string{"BASELOOP_RELEASES_API_URL", "BASELOOP_REPO", "BASELOOP_SKIP_SETUP", "BASELOOP_TOKEN"}

// spawnBackgroundUpgrade launches the detached upgrade child. A package var
// so tests can intercept the spawn (the upgradeTargetPath pattern).
var spawnBackgroundUpgrade = func() error {
	cmd, logFile, err := backgroundUpgradeCmd()
	if err != nil {
		return err
	}
	// The child holds its own fd after Start; the parent's copy must close so
	// exit leaves nothing pinned.
	defer logFile.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	// Wait is never called: Release drops the handle and lets init/the OS
	// reap the child after we exit.
	return cmd.Process.Release()
}

// backgroundUpgradeCmd builds the detached child invocation. Split from the
// spawn so tests can assert the contract (args, env, stdio, dir) without
// launching anything.
func backgroundUpgradeCmd() (*exec.Cmd, *os.File, error) {
	// Captured at spawn time, before any swap can move the binary: after an
	// upgrade, os.Executable on Linux reports the renamed-aside path.
	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	dir, err := state.Dir()
	if err != nil {
		return nil, nil, err
	}
	// Absolute, always: the child runs with cmd.Dir set to this very
	// directory, so a relative BASELOOP_STATE would re-resolve beneath it
	// (state/state/...), the lock acquisition would fail, and every command
	// would re-announce a background update that never actually runs.
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, nil, err
	}
	// O_APPEND, not O_TRUNC: several commands can race to spawn, and an
	// O_TRUNC open here would let a losing spawn zero the log of an upgrade
	// already in flight. The lock winner truncates the path itself, so the
	// log still holds exactly one (winning) attempt. The open also fails when
	// the state dir is missing — by design, never created here (R10).
	logPath := filepath.Join(dir, autoUpdateLogFile)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(self, "upgrade", "--background")
	cmd.Env = childEnv(os.Environ())
	// Pin the child's state dir to the absolutized path; passing through a
	// relative BASELOOP_STATE verbatim would hit the state/state nesting
	// described above.
	if os.Getenv("BASELOOP_STATE") != "" {
		cmd.Env = withEnvVar(cmd.Env, "BASELOOP_STATE", dir)
	}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// The state dir, not the user's cwd: an inherited cwd would pin the
	// user's directory (undeletable on Windows) and mounts for minutes.
	cmd.Dir = dir
	applyDetachAttrs(cmd)
	return cmd, logFile, nil
}

// childEnv is the parent env minus the pinned/stripped vars, plus the
// recursion marker (added exactly once, even if the parent itself carried it).
// Comparisons are case-insensitive: Windows env names are case-insensitive, so
// a `Baseloop_Token=...` would otherwise survive the strip and reach the
// child. On Unix a case-variant is technically a different variable, but the
// CLI never reads one, so dropping it uniformly is the safer single rule.
func childEnv(parent []string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		key, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(key, upgradeChildEnvVar) {
			continue
		}
		dropped := false
		for _, drop := range childEnvDropVars {
			if strings.EqualFold(key, drop) {
				dropped = true
				break
			}
		}
		if !dropped {
			out = append(out, kv)
		}
	}
	return append(out, upgradeChildEnvVar+"=1")
}

// withEnvVar returns env with key set to value, replacing any existing entry
// (case-insensitively, matching childEnv's rule) so the child never sees two
// conflicting definitions.
func withEnvVar(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, key) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+value)
}
