package link

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestMachineUIDLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, MachineUIDFileName)

	// First run: the file is created, private, and holds a UUIDv4.
	first, err := LoadOrCreateMachineUID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !core.ValidMachineUID(first) {
		t.Fatalf("created %q, not a UUIDv4", first)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v", info, err)
	}

	// Every later run keeps it, and tightens a loosened mode.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := LoadOrCreateMachineUID(dir)
	if err != nil || again != first {
		t.Fatalf("second run = %q, %v; want %q", again, err, first)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after reuse = %v", info.Mode().Perm())
	}

	// Whitespace and upper case written by hand still read as the same UID.
	if err := os.WriteFile(path, []byte("  "+strings.ToUpper(first)+"\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again, err := LoadOrCreateMachineUID(dir); err != nil || again != first {
		t.Fatalf("hand-edited file = %q, %v", again, err)
	}

	// A damaged file is replaced with a new UID instead of stopping the daemon.
	for _, corrupt := range []string{"", "not-a-uuid", "6ba7b810-9dad-11d1-80b4-00c04fd430c8" /* v1 */} {
		if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
			t.Fatal(err)
		}
		replaced, err := LoadOrCreateMachineUID(dir)
		if err != nil || !core.ValidMachineUID(replaced) || replaced == first {
			t.Fatalf("corrupt %q replaced with %q, %v", corrupt, replaced, err)
		}
		data, _ := os.ReadFile(path)
		if strings.TrimSpace(string(data)) != replaced {
			t.Fatalf("file holds %q, want %q", data, replaced)
		}
		first = replaced
	}

	// Reset gives a new UID that later runs keep.
	reset, err := ResetMachineUID(dir)
	if err != nil || !core.ValidMachineUID(reset) || reset == first {
		t.Fatalf("reset = %q, %v", reset, err)
	}
	if again, err := LoadOrCreateMachineUID(dir); err != nil || again != reset {
		t.Fatalf("after reset = %q, %v; want %q", again, err, reset)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode after reset = %v", info.Mode().Perm())
	}
}

func TestMachineUIDCreatesStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	uid, err := LoadOrCreateMachineUID(dir)
	if err != nil || !core.ValidMachineUID(uid) {
		t.Fatalf("uid = %q, %v", uid, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state directory = %v, %v", info, err)
	}
}

func TestDeviceInfoCarriesOnlyAValidMachineUID(t *testing.T) {
	const uid = "0f8fad5b-d9cb-469f-a165-70867728950e"
	info := LocalDeviceInfo("box", "1.0")
	info.MachineUID = strings.ToUpper(uid)
	if got := decodeInfo(encodeInfo(info)); got.MachineUID != uid {
		t.Fatalf("decoded machine UID = %q", got.MachineUID)
	}
	info.MachineUID = "../../etc/passwd"
	if got := decodeInfo(encodeInfo(info)); got.MachineUID != "" {
		t.Fatalf("an invalid machine UID passed: %q", got.MachineUID)
	}
}
