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

// codexKwargRE splits one keyword argument into its name and raw value; the
// argument text has already been cut at top-level commas, so `=` inside a
// string value cannot reach it.
var codexKwargRE = regexp.MustCompile(`(?s)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$`)

// codexBaseloopDecisions returns the decision of every prefix_rule whose
// pattern is exactly ["baseloop"], in file order. This is a small Starlark
// scanner rather than a regex over the raw text, because every shortcut has
// a way to misread a valid rules file: keyword arguments come in any order
// and may span lines, whitespace is allowed between `prefix_rule` and `(`,
// `#` comments may mention rules that are not in force, and a justification
// string may contain text that looks like `decision="forbidden"`. So
// comments are dropped, the source is walked with string literals skipped,
// each call is isolated by matching its parentheses, and its keyword
// arguments are split at top-level commas before the two keywords are read.
// Narrower patterns such as ["baseloop", "tools"] are deliberately not
// matched: they do not decide the whole CLI.
func codexBaseloopDecisions(data []byte) []string {
	src := stripStarlarkComments(data)
	var decisions []string
	for i := 0; i < len(src); {
		c := src[i]
		if c == '"' || c == '\'' {
			i = skipStarlarkString(src, i)
			continue
		}
		if !isIdentByte(c) {
			i++
			continue
		}
		start := i
		for i < len(src) && isIdentByte(src[i]) {
			i++
		}
		if string(src[start:i]) != "prefix_rule" {
			continue
		}
		open := i
		for open < len(src) && isStarlarkSpace(src[open]) {
			open++
		}
		if open >= len(src) || src[open] != '(' {
			continue
		}
		end := matchingParen(src, open+1)
		if end < 0 {
			return decisions
		}
		if decision, ok := codexRuleDecision(src[open+1 : end]); ok {
			decisions = append(decisions, decision)
		}
		i = end + 1
	}
	return decisions
}

// codexRuleDecision reads one call's arguments and returns its decision when
// the pattern is exactly ["baseloop"].
func codexRuleDecision(args []byte) (string, bool) {
	var pattern, decision []byte
	for _, arg := range splitTopLevel(args, ',') {
		m := codexKwargRE.FindSubmatch(arg)
		if m == nil {
			continue
		}
		switch string(m[1]) {
		case "pattern":
			pattern = m[2]
		case "decision":
			decision = m[2]
		}
	}
	if len(pattern) < 2 || pattern[0] != '[' || pattern[len(pattern)-1] != ']' {
		return "", false
	}
	var items []string
	for _, item := range splitTopLevel(pattern[1:len(pattern)-1], ',') {
		if s, ok := unquoteStarlark(item); ok {
			items = append(items, s)
		} else if len(bytes.TrimSpace(item)) > 0 {
			return "", false
		}
	}
	if len(items) != 1 || items[0] != "baseloop" {
		return "", false
	}
	if s, ok := unquoteStarlark(decision); ok {
		return s, true
	}
	return "", false
}

// splitTopLevel cuts at sep outside string literals and outside nested
// brackets or parentheses, dropping empty pieces (a trailing comma).
func splitTopLevel(src []byte, sep byte) [][]byte {
	var pieces [][]byte
	depth, start := 0, 0
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '"' || c == '\'':
			i = skipStarlarkString(src, i)
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
		case c == sep && depth == 0:
			if piece := bytes.TrimSpace(src[start:i]); len(piece) > 0 {
				pieces = append(pieces, piece)
			}
			start = i + 1
		}
		i++
	}
	if piece := bytes.TrimSpace(src[start:]); len(piece) > 0 {
		pieces = append(pieces, piece)
	}
	return pieces
}

// unquoteStarlark returns the contents of a simple string literal. Escapes
// are resolved only enough to compare against plain identifiers such as
// "baseloop" or "allow"; anything more exotic simply fails to match.
func unquoteStarlark(lit []byte) (string, bool) {
	lit = bytes.TrimSpace(lit)
	if len(lit) < 2 || lit[0] != lit[len(lit)-1] || (lit[0] != '"' && lit[0] != '\'') {
		return "", false
	}
	var out strings.Builder
	for i := 1; i < len(lit)-1; i++ {
		if lit[i] == '\\' && i+1 < len(lit)-1 {
			i++
		}
		out.WriteByte(lit[i])
	}
	return out.String(), true
}

// skipStarlarkString returns the index just past the string literal that
// opens at i, honoring backslash escapes.
func skipStarlarkString(src []byte, i int) int {
	quote := src[i]
	for i++; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case quote:
			return i + 1
		}
	}
	return len(src)
}

// isStarlarkSpace covers what may separate a callee from its `(`: a newline
// there ends the statement instead (Codex rejects the file), so only
// horizontal whitespace counts.
func isStarlarkSpace(b byte) bool {
	return b == ' ' || b == '\t'
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// stripStarlarkComments blanks `#` comments outside string literals so a
// commented-out rule cannot count, keeping newlines so nothing else shifts.
func stripStarlarkComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	var quote byte
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case quote != 0:
			out = append(out, c)
			if c == '\\' && i+1 < len(data) {
				i++
				out = append(out, data[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			out = append(out, c)
		case c == '#':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			if i < len(data) {
				out = append(out, '\n')
			}
		default:
			out = append(out, c)
		}
	}
	return out
}

// matchingParen returns the index of the ')' that closes the call whose
// arguments start at from, or -1, ignoring parentheses inside strings.
func matchingParen(src []byte, from int) int {
	depth := 1
	var quote byte
	for i := from; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

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

// claudePermissionOverride reports a permissions.deny or permissions.ask
// entry that would override an allow for the whole CLI. Claude Code resolves
// deny, then ask, then allow, so next to one of these an allow entry still
// prompts or refuses, and claiming the grant would be a lie. Narrower
// entries (one subcommand) are left alone: they limit the grant, not void it.
func claudePermissionOverride(doc map[string]any) (string, error) {
	perms, _ := doc["permissions"].(map[string]any)
	for _, list := range []string{"deny", "ask"} {
		raw, ok := perms[list]
		if !ok || raw == nil {
			continue
		}
		entries, ok := raw.([]any)
		if !ok {
			return "", fmt.Errorf("permissions.%s is not an array", list)
		}
		for _, item := range entries {
			entry, ok := item.(string)
			if !ok {
				return "", fmt.Errorf("permissions.%s contains a non-string entry", list)
			}
			switch entry {
			case claudePermissionEntry, claudePermissionLegacyEntry, "Bash(baseloop)", "Bash", "Bash(*)":
				return "permissions." + list + " has " + entry, nil
			}
		}
	}
	return "", nil
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
	override, err := claudePermissionOverride(doc)
	if err != nil {
		return false, false, fmt.Errorf("unexpected shape in %s: %w", path, err)
	}
	if override != "" {
		return false, false, fmt.Errorf("%s in %s, which overrides any allow entry for baseloop; remove it by hand", override, path)
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
	// Codex weighs every matching rule and the strictest decision wins, so an
	// allow next to a forbidden or prompt rule is still not a grant. Refuse on
	// any non-allow before trusting an allow.
	hasAllow := false
	for _, decision := range codexBaseloopDecisions(data) {
		if decision == "allow" {
			hasAllow = true
		} else {
			return false, false, fmt.Errorf("%s already has a %q rule for baseloop; change it by hand", path, decision)
		}
	}
	if hasAllow {
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
