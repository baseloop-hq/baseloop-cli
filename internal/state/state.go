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
	// InstallPolicy records how the install was created: "pinned" when the
	// operator chose an exact version (BASELOOP_VERSION at install time),
	// "managed" otherwise. The update pipeline reads it so a pinned machine is
	// never nagged onto — or auto-updated away from — its chosen version.
	// Optional field, same schema: absent on installs that predate it.
	InstallPolicy string `json:"install_policy,omitempty"`
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

// Save writes the manifest with 0600 permissions. The write goes through a
// same-directory temp file and rename: the background updater reads the
// install pin while an installer or another command may be rewriting the
// manifest, and a torn read would make a pinned install look unpinned.
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
	tmp, err := os.CreateTemp(dir, ".manifest-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}
