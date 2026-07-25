package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRunIsFailOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stderr bytes.Buffer
	if err := run([]string{"--not-a-real-flag"}, bytes.NewBufferString("not-json"), &stderr); err != nil {
		t.Fatalf("invalid flags must remain fail-open: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr without AMUX_DEBUG: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".agentmux")); !os.IsNotExist(err) {
		t.Fatalf("flag parse failure should not create state, stat err=%v", err)
	}
}
