package framework

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Tests control PATH themselves; the developer's Homebrew must not leak in.
	packageManagerDirs = nil
	os.Exit(m.Run())
}
