//go:build windows

package cli

import (
	"os/exec"
	"syscall"
)

// Console creation flags (Windows API). DETACHED_PROCESS rather than
// CREATE_NO_WINDOW: the child's stdio is redirected to a log file, so it
// needs no console at all, and the two flags are not meant to be combined.
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

// applyDetachAttrs detaches the child from the parent's console (no console
// window flash on every auto-updating command — a real shipped bug in other
// CLIs) and isolates it from the parent console's Ctrl+C.
func applyDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
		HideWindow:    true,
	}
}
