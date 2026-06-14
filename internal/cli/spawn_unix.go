//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// applyDetachAttrs puts the child in its own session so terminal close,
// Ctrl+C, and SIGHUP never reach it. Setsid rather than Setpgid: a new
// session has no controlling terminal at all, which is also what keeps an
// SSH logout from waiting on (or killing) an in-flight upgrade.
func applyDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
