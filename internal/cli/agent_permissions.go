// Agent permission grant: the allow-list entries that let Claude Code and
// Codex run baseloop commands without a per-command approval prompt.
//
// The installers ask once, default no, right after agent setup, and call the
// hidden `baseloop setup agent-permissions` to apply the grant to every agent
// found on the machine; the same command is the recovery path for anyone who
// declined and changed their mind. Edits are additive and refuse anything
// they cannot round-trip: a malformed Claude settings file, a permissions
// block of an unexpected shape, or a Codex rule that already decides
// baseloop differently is left untouched with an error rather than
// rewritten, because those files belong to the agents and guessing at their
// intent could clobber a decision the user made deliberately.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/baseloop-hq/baseloop-cli/internal/output"
)

const (
	claudePermissionEntry = "Bash(baseloop:*)"
	// claudePermissionLegacyEntry is the spelling the install.md runbook had
	// agents write before the CLI owned this edit. It grants the same thing,
	// so a machine that has it is never asked again.
	claudePermissionLegacyEntry = "Bash(baseloop *)"
	// claudePermissionPathEntry covers a Claude Code session that started
	// before the installer extended PATH: the runbook has the agent prefix
	// every command with this export, and Claude Code matches compound
	// commands as a whole, so the bare entry alone would still prompt.
	claudePermissionPathEntry = "Bash(export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop *)"
	codexPermissionRule       = `prefix_rule(pattern=["baseloop"], decision="allow")`
	codexPermissionComment    = "# Added by baseloop setup agent-permissions: run baseloop without approval prompts."
)

// codexBaseloopRuleRE finds every prefix_rule whose pattern is exactly
// ["baseloop"] and captures its decision, tolerating the whitespace and
// trailing-comma variations Starlark allows. Narrower rules such as
// ["baseloop", "tools"] are deliberately not matched: they do not grant the
// whole CLI.
var codexBaseloopRuleRE = regexp.MustCompile(`(?m)^[ \t]*prefix_rule\(\s*pattern\s*=\s*\[\s*"baseloop"\s*,?\s*\]\s*,\s*decision\s*=\s*"([a-z]+)"`)

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func claudeSettingsPath() string {
	return filepath.Join(homeDir(), ".claude", "settings.json")
}

func claudePresent() bool {
	return dirExists(filepath.Join(homeDir(), ".claude"))
}

func codexRulesPath() string {
	return filepath.Join(codexHomeDir(), "rules", "default.rules")
}

func codexPresent() bool {
	return dirExists(codexHomeDir())
}

// loadClaudeSettings parses settings.json into a generic document. Numbers
// stay as json.Number so unrelated values round-trip byte-for-byte; a
// missing or blank file is an empty document.
func loadClaudeSettings(path string) (doc map[string]any, exists bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, true, errors.New("not valid JSON")
	}
	if doc == nil {
		return nil, true, errors.New("not a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, true, errors.New("trailing content after the JSON object")
	}
	return doc, true, nil
}

// claudeAllowList returns permissions.allow, creating the permissions object
// in doc when absent so a later write lands in the right place. An allow
// list is only ever a flat array of strings; anything else is an error.
func claudeAllowList(doc map[string]any) ([]any, error) {
	permsRaw, ok := doc["permissions"]
	if !ok || permsRaw == nil {
		permsRaw = map[string]any{}
		doc["permissions"] = permsRaw
	}
	perms, ok := permsRaw.(map[string]any)
	if !ok {
		return nil, errors.New("permissions is not an object")
	}
	allowRaw, ok := perms["allow"]
	if !ok || allowRaw == nil {
		return []any{}, nil
	}
	allow, ok := allowRaw.([]any)
	if !ok {
		return nil, errors.New("permissions.allow is not an array")
	}
	for _, item := range allow {
		if _, ok := item.(string); !ok {
			return nil, errors.New("permissions.allow contains a non-string entry")
		}
	}
	return allow, nil
}

func claudeAllowContains(allow []any, entry string) bool {
	for _, item := range allow {
		if item == entry {
			return true
		}
	}
	return false
}

// claudePermissionGranted is decided by the bare entry alone, in either
// spelling; the PATH-prefixed entry is a convenience written alongside it,
// not a condition.
func claudePermissionGranted(allow []any) bool {
	return claudeAllowContains(allow, claudePermissionEntry) || claudeAllowContains(allow, claudePermissionLegacyEntry)
}

// encodeClaudeSettings matches Claude Code's own writer: two-space
// indentation, no HTML escaping, trailing newline.
func encodeClaudeSettings(doc map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// applyClaudePermission reports whether Claude Code may run baseloop
// unprompted and, unless check is set, grants it. changed is true only when
// the file was rewritten.
func applyClaudePermission(path string, check bool) (granted, changed bool, err error) {
	doc, exists, err := loadClaudeSettings(path)
	if err != nil {
		return false, false, fmt.Errorf("could not read %s: %w", path, err)
	}
	allow, err := claudeAllowList(doc)
	if err != nil {
		return false, false, fmt.Errorf("unexpected shape in %s: %w", path, err)
	}
	if claudePermissionGranted(allow) {
		return true, false, nil
	}
	if check {
		return false, false, nil
	}
	if exists {
		if err := backupFile(path); err != nil {
			return false, false, fmt.Errorf("could not back up %s: %w", path, err)
		}
	}
	allow = append(allow, claudePermissionEntry)
	if !claudeAllowContains(allow, claudePermissionPathEntry) {
		allow = append(allow, claudePermissionPathEntry)
	}
	doc["permissions"].(map[string]any)["allow"] = allow
	data, err := encodeClaudeSettings(doc)
	if err != nil {
		return false, false, fmt.Errorf("could not encode %s: %w", path, err)
	}
	if err := writeFileAtomic(path, data, 0o644); err != nil {
		return false, false, fmt.Errorf("could not write %s: %w", path, err)
	}
	return true, true, nil
}

// applyCodexPermission is the Codex counterpart over rules/default.rules,
// the execpolicy file Codex itself appends "always allow" prefixes to. The
// edit is append-only: Codex fails to load a rules file it cannot parse, so
// the existing text is never reformatted, only extended.
func applyCodexPermission(path string, check bool) (granted, changed bool, err error) {
	data, err := os.ReadFile(path)
	exists := true
	if os.IsNotExist(err) {
		data, exists, err = nil, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("could not read %s: %w", path, err)
	}
	for _, match := range codexBaseloopRuleRE.FindAllSubmatch(data, -1) {
		switch decision := string(match[1]); decision {
		case "allow":
			return true, false, nil
		default:
			return false, false, fmt.Errorf("%s already has a %q rule for baseloop; change it by hand", path, decision)
		}
	}
	if check {
		return false, false, nil
	}
	if exists {
		if err := backupFile(path); err != nil {
			return false, false, fmt.Errorf("could not back up %s: %w", path, err)
		}
	}
	var buf bytes.Buffer
	buf.Write(data)
	if len(data) > 0 {
		if !bytes.HasSuffix(data, []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(codexPermissionComment + "\n" + codexPermissionRule + "\n")
	if err := writeFileAtomic(path, buf.Bytes(), 0o644); err != nil {
		return false, false, fmt.Errorf("could not write %s: %w", path, err)
	}
	return true, true, nil
}

// resolveWriteTarget follows a symlinked config file (dotfile managers) to
// its target so the link survives a replace-by-rename.
func resolveWriteTarget(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// writeFileAtomic replaces the file through a same-directory temp file and
// rename so an agent reading concurrently never observes a half-written
// document. An existing file keeps its mode; a new one gets defaultMode.
func writeFileAtomic(path string, data []byte, defaultMode os.FileMode) error {
	target := resolveWriteTarget(path)
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := defaultMode
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".baseloop-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// backupFile keeps one pre-edit copy next to the file. A fixed name
// (overwritten on each change) is deliberate: every edit here is a small
// append, so the last known-good copy is all anyone needs to undo it.
func backupFile(path string) error {
	target := resolveWriteTarget(path)
	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	return os.WriteFile(target+".baseloop-backup", data, 0o600)
}

func joinAgentNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// setupAgentPermissions is the hidden `setup agent-permissions [--check]`
// subcommand. It grants every agent present on the machine (Claude Code via
// ~/.claude, Codex via ~/.codex or $CODEX_HOME) in one pass. --check never
// writes and exits 0 only when every present agent already has the grant,
// so the installers can skip the prompt on a machine that said yes before.
func setupAgentPermissions(args []string, g globals, stdout io.Writer) int {
	fs := flag.NewFlagSet("setup agent-permissions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	check := fs.Bool("check", false, "Report whether agents may run baseloop without prompts, without changing anything")
	if err := fs.Parse(args); err != nil {
		return render(stdout, g, output.Failure("USAGE", err.Error(), "Use baseloop setup agent-permissions [--check].", nil), 2)
	}

	legs := []struct {
		key, name, path string
		present         bool
		apply           func(string, bool) (bool, bool, error)
	}{
		{"claude", "Claude Code", claudeSettingsPath(), claudePresent(), applyClaudePermission},
		{"codex", "Codex", codexRulesPath(), codexPresent(), applyCodexPermission},
	}

	payload := map[string]any{}
	var found, granted, changed, absent, failures []string
	for _, leg := range legs {
		entry := map[string]any{"present": leg.present, "path": leg.path, "granted": false, "changed": false}
		payload[leg.key] = entry
		if !leg.present {
			continue
		}
		found = append(found, leg.name)
		ok, did, err := leg.apply(leg.path, *check)
		entry["granted"] = ok
		entry["changed"] = did
		switch {
		case err != nil:
			entry["error"] = err.Error()
			failures = append(failures, err.Error())
		case ok && did:
			granted = append(granted, leg.name)
			changed = append(changed, leg.name)
		case ok:
			granted = append(granted, leg.name)
		default:
			absent = append(absent, leg.name)
		}
	}

	if len(found) == 0 {
		return render(stdout, g, output.Failure("AGENT_NOT_FOUND", "No Claude Code or Codex installation found (looked for ~/.claude and ~/.codex).", "Install Claude Code or Codex, then re-run baseloop setup agent-permissions.", payload), 1)
	}
	if len(failures) > 0 {
		return render(stdout, g, output.Failure("AGENT_SETTINGS_ERROR", strings.Join(failures, "; "), "Fix the named file by hand, then re-run baseloop setup agent-permissions.", payload), 1)
	}
	if *check {
		if len(absent) == 0 {
			return render(stdout, g, output.Success(payload, joinAgentNames(found)+" can already run baseloop without permission prompts.", nil), 0)
		}
		return render(stdout, g, output.Failure("AGENT_PERMISSION_ABSENT", "Not yet allowed to run baseloop without permission prompts: "+joinAgentNames(absent)+".", "Run baseloop setup agent-permissions to allow it.", payload), 1)
	}

	summary := joinAgentNames(found) + " can already run baseloop without permission prompts."
	if len(changed) > 0 {
		summary = joinAgentNames(changed) + " can now run baseloop without permission prompts."
		if already := len(granted) - len(changed); already > 0 {
			var rest []string
			for _, name := range granted {
				isChanged := false
				for _, c := range changed {
					if c == name {
						isChanged = true
					}
				}
				if !isChanged {
					rest = append(rest, name)
				}
			}
			summary += " " + joinAgentNames(rest) + " already could."
		}
	}
	// Codex loads its rules at startup, so a session already open keeps
	// prompting until restarted. Human output is the summary line alone, so
	// the note rides on it as well as in the payload.
	notes := []string{}
	for _, name := range changed {
		if name == "Codex" {
			const restart = "Restart any running Codex session for the new rule to take effect."
			notes = append(notes, restart)
			summary += " " + restart
		}
	}
	payload["notes"] = notes
	return render(stdout, g, output.Success(payload, summary, nil), 0)
}
