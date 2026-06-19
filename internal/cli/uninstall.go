package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/baseloop-hq/baseloop-cli/internal/config"
	"github.com/baseloop-hq/baseloop-cli/internal/output"
	"github.com/baseloop-hq/baseloop-cli/internal/state"
)

// path markers must match the comments the installers write around their PATH
// edit. pathMarker is the legacy single-line marker; new Unix installs use an
// explicit begin/end block so future installs can rewrite the managed section.
const (
	pathMarker      = "# Added by Baseloop CLI installer"
	pathBeginMarker = "# >>> Baseloop CLI installer >>>"
	pathEndMarker   = "# <<< Baseloop CLI installer <<<"
)

const uninstallPluginNote = "Claude and Codex plugin state is managed by each agent's plugin manager and was not removed."

// uninstall removes the CLI-owned PATH entry, install state, and (last) the
// binary. It keeps the config/token by default so a reinstall preserves auth;
// --purge removes those too.
func uninstall(args []string, g globals, stdout io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dryRun := fs.Bool("dry-run", false, "Print what would be removed without removing anything")
	purge := fs.Bool("purge", false, "Also remove the config file and stored auth token")
	keepBinary := fs.Bool("keep-binary", false, "Do not remove the baseloop binary (the uninstall script handles it)")
	if err := fs.Parse(args); err != nil {
		return render(stdout, g, output.Failure("USAGE", err.Error(), "Use baseloop uninstall [--dry-run] [--purge] [--keep-binary].", nil), 2)
	}

	plan := removalTargets()
	binaryPath, _ := os.Executable()

	// An in-flight background upgrade and uninstall must not interleave: the
	// upgrader could rename a fresh binary into the path uninstall just
	// cleared, or recreate state files mid-removal. Holding the upgrade lock
	// for the whole removal forces strict ordering. A fresh held lock refuses
	// the uninstall; a stale one is taken over; any other acquisition error
	// (state dir already gone, unwritable) means nothing can be in flight —
	// the background child aborts without a usable state dir — so removal
	// proceeds. The lock file itself is deleted with the state dir.
	if !*dryRun {
		lock, err := acquireUpgradeLock()
		if err != nil {
			var held *lockHeldError
			if errors.As(err, &held) {
				return render(stdout, g, output.Failure("UPGRADE_IN_PROGRESS", err.Error(), "A background upgrade is in flight; retry baseloop uninstall shortly.", nil), 1)
			}
		} else {
			defer lock.release()
		}
	}

	if *dryRun {
		would := plan.existingTargets()
		if *purge && safeConfigPath(plan.configPath) && pathExistsForRemoval(plan.configPath) {
			would = append(would, plan.configPath)
		}
		if !*keepBinary && binaryPath != "" {
			would = append(would, binaryPath)
		}
		return render(stdout, g, output.Success(map[string]any{
			"dryRun":      true,
			"purge":       *purge,
			"wouldRemove": would,
			"pathFiles":   plan.pathFilesWithMarker(),
		}, "Dry run: nothing was removed.", nil), 0)
	}

	removed := []string{}
	kept := []string{}
	errs := []string{}
	notes := []string{}

	for _, file := range plan.pathFiles {
		changed, err := stripPathMarker(file)
		if err != nil {
			errs = append(errs, file+": "+err.Error())
			continue
		}
		if changed {
			removed = append(removed, file+" (PATH entry)")
		}
	}

	if plan.stateDir != "" && pathExistsForRemoval(plan.stateDir) {
		if err := removeStateDir(plan.stateDir); err != nil {
			errs = append(errs, plan.stateDir+": "+err.Error())
		} else {
			removed = append(removed, plan.stateDir)
		}
	}

	for _, target := range entrySkillTargets() {
		if !ownedBaseloopEntrySkillDir(target.dir, target.content) {
			continue
		}
		if err := os.RemoveAll(target.dir); err != nil {
			errs = append(errs, target.dir+": "+err.Error())
		} else {
			removed = append(removed, target.dir)
			removeEmptySkillsDir(filepath.Dir(target.dir))
		}
	}

	if *purge && plan.configPath != "" && pathExistsForRemoval(plan.configPath) {
		if !safeConfigPath(plan.configPath) {
			errs = append(errs, plan.configPath+": refusing to remove config path that is not a config.json file")
		} else if err := os.Remove(plan.configPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, plan.configPath+": "+err.Error())
		} else {
			removed = append(removed, plan.configPath)
			_ = os.Remove(filepath.Dir(plan.configPath)) // only succeeds when empty
		}
	}

	if !*purge {
		notes = append(notes, "Kept your Baseloop sign-in, so reinstalling later is easy. Run with --purge to remove it.")
	}
	notes = append(notes, uninstallPluginNote)

	binaryRemoved := false
	if *keepBinary {
		if binaryPath != "" {
			kept = append(kept, binaryPath+" (binary, --keep-binary)")
		}
	} else if binaryPath != "" {
		if err := os.Remove(binaryPath); err != nil {
			// Windows locks the running executable; the uninstall script deletes it.
			kept = append(kept, binaryPath+" (binary)")
			notes = append(notes, "Could not remove the binary while it is running. Delete it manually: "+binaryPath)
		} else {
			binaryRemoved = true
			removed = append(removed, binaryPath)
		}
	}

	ok := len(errs) == 0
	summary := "Baseloop has been uninstalled."
	if !ok {
		summary = "Uninstall finished with some errors."
	}
	payload := map[string]any{
		"purge":         *purge,
		"removed":       removed,
		"kept":          kept,
		"errors":        errs,
		"binary":        binaryPath,
		"binaryRemoved": binaryRemoved,
		"notes":         notes,
	}
	if !ok {
		return renderUninstall(stdout, g, output.Failure("UNINSTALL_INCOMPLETE", summary, "Review the errors and rerun baseloop uninstall after fixing them.", payload), 1)
	}
	return renderUninstall(stdout, g, output.Success(payload, summary, nil), 0)
}

func renderUninstall(w io.Writer, g globals, env output.Envelope, code int) int {
	if g.agent || g.json || !env.OK {
		return render(w, g, env, code)
	}
	if env.Summary != "" {
		_, _ = io.WriteString(w, env.Summary+"\n")
	}
	if payload, ok := env.Data.(map[string]any); ok {
		if notes, ok := payload["notes"].([]string); ok && len(notes) > 0 {
			_, _ = io.WriteString(w, "\nNotes:\n")
			for _, note := range notes {
				_, _ = io.WriteString(w, "  - "+note+"\n")
			}
		}
	}
	return code
}

// removal describes everything an uninstall will delete.
type removal struct {
	stateDir   string   // install manifest/state, removed after PATH updates
	pathFiles  []string // shell rc files that may hold the PATH marker
	configPath string
}

// removalTargets builds the deletion plan for CLI-owned state.
func removalTargets() removal {
	plan := removal{
		pathFiles: pathRCFiles(),
	}
	if stateDir, err := state.Dir(); err == nil {
		plan.stateDir = stateDir
	}

	if path, err := config.DefaultPath(); err == nil {
		plan.configPath = path
	}
	return plan
}

func removeStateDir(dir string) error {
	if os.Getenv("BASELOOP_STATE") == "" {
		return os.RemoveAll(dir)
	}
	// Explicit allowlist: a redirected BASELOOP_STATE could point anywhere, so
	// only files the CLI itself writes are removed — including the upgrade
	// lock uninstall itself just acquired and the auto-update bookkeeping.
	for _, name := range []string{"manifest.json", versionCheckFile, upgradeLockFile, autoUpdateFailureFile, autoUpdateLogFile} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// A crashed atomic write (kill between CreateTemp and rename) leaves a
	// .baseloop-tmp-* file nothing else ever cleans, and one leftover would
	// fail the empty-dir removal below forever.
	if tmps, err := filepath.Glob(filepath.Join(dir, ".baseloop-tmp-*")); err == nil {
		for _, tmp := range tmps {
			if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return os.Remove(dir) // only succeeds when empty
}

func safeConfigPath(path string) bool {
	if path == "" || filepath.Base(filepath.Clean(path)) != "config.json" {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	return info.Mode().IsRegular()
}

func (r removal) existingTargets() []string {
	out := []string{}
	if r.stateDir != "" && pathExistsForRemoval(r.stateDir) {
		out = append(out, r.stateDir)
	}
	for _, target := range entrySkillTargets() {
		if ownedBaseloopEntrySkillDir(target.dir, target.content) {
			out = append(out, target.dir)
		}
	}
	return out
}

func baseloopClaudeSkillDir() string {
	return filepath.Dir(baseloopClaudeSkillPath())
}

func baseloopCodexSkillDir() string {
	return filepath.Dir(baseloopCodexSkillPath())
}

// entrySkillTarget pairs an entry-skill directory with the content this CLI
// would have written there, so ownership checks compare per agent: a Codex
// dir holding the Claude constant is not ours (and vice versa).
type entrySkillTarget struct {
	dir     string
	content string
}

func entrySkillTargets() []entrySkillTarget {
	return []entrySkillTarget{
		{dir: baseloopClaudeSkillDir(), content: baseloopClaudeSkill},
		{dir: baseloopCodexSkillDir(), content: baseloopCodexSkill},
	}
}

// removeEmptySkillsDir best-effort removes an agent's skills dir after the
// entry skill is gone. Only a real, empty directory is removed: os.Remove on
// a symlinked skills dir would unlink the user's other skills (the link goes
// even when its target is non-empty), and it never reaches higher because
// ~/.codex holds config.toml and other agent-owned state this CLI must not
// touch.
func removeEmptySkillsDir(dir string) {
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	_ = os.Remove(dir) // only succeeds when empty
}

// ownedBaseloopEntrySkillDir reports whether the CLI may remove an entry
// skill dir. The marker check is deliberately self-referential (marker ==
// hash of the current SKILL.md) rather than pinned to this binary's embedded
// content: setup stamps the marker alongside whatever version it writes, so
// the pair proves "a baseloop CLI wrote this and nobody edited it since"
// across binary upgrades. Pinning to expectedContent would orphan skill dirs
// written by older releases after every content change. expectedContent is
// only the fallback for legacy installs that predate the marker.
func ownedBaseloopEntrySkillDir(dir, expectedContent string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return false
	}
	marker, markerErr := os.ReadFile(filepath.Join(dir, ".baseloop.sha256"))
	if markerErr == nil {
		sum := sha256.Sum256(data)
		return strings.TrimSpace(string(marker)) == hex.EncodeToString(sum[:])
	}
	return string(data) == expectedContent
}

func (r removal) pathFilesWithMarker() []string {
	out := []string{}
	for _, f := range r.pathFiles {
		if fileHasMarker(f) {
			out = append(out, f)
		}
	}
	return out
}

// pathRCFiles lists the shell startup files the installers may have edited.
func pathRCFiles() []string {
	home := homeDir()
	files := []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".zshenv"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".bash_login"),
		filepath.Join(home, ".profile"),
	}
	if zdotdir := os.Getenv("ZDOTDIR"); zdotdir != "" && zdotdir != home {
		files = append([]string{filepath.Join(zdotdir, ".zshrc")}, files...)
	}
	return files
}

func fileHasMarker(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if isPathMarker(line) {
			return true
		}
	}
	return false
}

func isPathMarker(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == pathMarker || trimmed == pathBeginMarker
}

func pathExistsForRemoval(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// stripPathMarker removes the managed PATH block. It supports the current
// begin/end marker format plus the legacy single-marker format. Returns whether
// the file changed.
func stripPathMarker(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	changed := false
	inBlock := false
	for i := 0; i < len(lines); i++ {
		switch strings.TrimSpace(lines[i]) {
		case pathBeginMarker:
			changed = true
			inBlock = true
			// Drop a single blank line we previously emitted above the block.
			if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
				out = out[:n-1]
			}
			continue
		case pathEndMarker:
			if inBlock {
				inBlock = false
				continue
			}
		case pathMarker:
			changed = true
			// Drop a single blank line we previously emitted above the marker.
			if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
				out = out[:n-1]
			}
			// Skip the export line that follows the marker, if it still looks
			// like the line written by install.sh. Preserve manually edited
			// content after the marker rather than guessing.
			if i+1 < len(lines) && isInstallerPathLine(lines[i+1]) {
				i++
			}
			continue
		}
		if inBlock {
			continue
		}
		out = append(out, lines[i])
	}
	if inBlock {
		return false, errUnclosedPathBlock(path)
	}

	if !changed {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(strings.Join(out, "\n")), info.Mode().Perm())
}

func isInstallerPathLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, `export PATH="`) && strings.HasSuffix(trimmed, `:$PATH"`)
}

func errUnclosedPathBlock(path string) error {
	return &unclosedPathBlockError{path: path}
}

type unclosedPathBlockError struct {
	path string
}

func (e *unclosedPathBlockError) Error() string {
	return "Baseloop PATH block is missing its end marker in " + e.path
}
