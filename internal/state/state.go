// Package state records local installer state for the Baseloop CLI.
package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// SchemaVersion is bumped when the manifest layout changes incompatibly.
const SchemaVersion = 1

// Manifest is the on-disk record of a Baseloop CLI install.
type Manifest struct {
	Schema                 int      `json:"schema"`
	WindowsUserPathEntries []string `json:"windows_user_path_entries,omitempty"`
}

// Dir returns the directory that holds the install manifest.
//
// Resolution order: BASELOOP_STATE (explicit dir, used by tests) >
// XDG_STATE_HOME/baseloop > ~/.local/state/baseloop.
func Dir() (string, error) {
	if dir := os.Getenv("BASELOOP_STATE"); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "baseloop"), nil
}

// Path returns the manifest file path.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "manifest.json"), nil
}

// Load reads the manifest, returning an empty manifest when none exists yet.
func Load() (Manifest, error) {
	m := Manifest{Schema: SchemaVersion}
	path, err := Path()
	if err != nil {
		return m, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Schema == 0 {
		m.Schema = SchemaVersion
	}
	return m, nil
}

// Save writes the manifest with 0600 permissions.
func Save(m Manifest) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if m.Schema == 0 {
		m.Schema = SchemaVersion
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
