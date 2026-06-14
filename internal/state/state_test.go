package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingManifestReturnsEmpty(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())
	m, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.Schema != SchemaVersion {
		t.Fatalf("expected schema %d, got %d", SchemaVersion, m.Schema)
	}
}

func TestLoadAcceptsUTF8BOMManifest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASELOOP_STATE", dir)
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"schema":1,"windows_user_path_entries":["C:\\Users\\me\\bin"]}`)...)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(m.WindowsUserPathEntries) != 1 {
		t.Fatalf("expected Windows PATH entry from BOM manifest, got %#v", m.WindowsUserPathEntries)
	}
}

func TestPathHonorsStateOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BASELOOP_STATE", dir)
	path, err := Path()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if path != filepath.Join(dir, "manifest.json") {
		t.Fatalf("expected manifest under override dir, got %s", path)
	}
}

func TestDirHonorsXDGStateHome(t *testing.T) {
	t.Setenv("BASELOOP_STATE", "")
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	dir, err := Dir()
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	if dir != filepath.Join(xdg, "baseloop") {
		t.Fatalf("expected XDG state dir, got %s", dir)
	}
}
