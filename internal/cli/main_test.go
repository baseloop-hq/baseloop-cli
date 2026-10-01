package cli

import (
	"errors"
	"os"
	"testing"
)

// TestMain makes the real background spawn unreachable from the suite.
// Auto-update is on by default, so any test that runs an ordinary command on
// a machine where CI is unset would otherwise launch the test binary as a
// detached "baseloop upgrade". A failed spawn falls back to the plain update
// notice. Tests that assert spawns install their own seam with setSpawnSeam,
// which restores this stub on cleanup.
func TestMain(m *testing.M) {
	spawnBackgroundUpgrade = func() error {
		return errors.New("background spawn disabled in tests")
	}
	os.Exit(m.Run())
}
