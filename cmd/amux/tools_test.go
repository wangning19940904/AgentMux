package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestConfirmInternalInstallAcceptsExplicitYes(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("yes\n"))
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	if err := confirmInternalInstall(cmd, "ByteDance Internal Toolkit"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "only available inside ByteDance") {
		t.Fatalf("prompt = %q", stderr.String())
	}
}

func TestConfirmInternalInstallRejectsNonInteractiveInput(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("yes\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetIn(input)
	if err := confirmInternalInstall(cmd, "Internal"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v", err)
	}
}

func TestBundleJSONInstallRequiresYes(t *testing.T) {
	cmd := toolsBundleInstallCmd()
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"bytedance-internal", "--json"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %v", err)
	}
}
