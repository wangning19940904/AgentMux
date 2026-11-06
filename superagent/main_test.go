package superagent

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points HOME at a temporary directory, so a service built without a
// StateDir can never write into the developer's real ~/.agentmux.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "superagent-test-home-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
