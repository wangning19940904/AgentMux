package link

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// MachineUIDFileName holds this machine's identity in the state directory.
//
// An SSH host, a paired device and a device reached through the hub can all
// be the same computer. Each daemon reports the same machine UID however it
// is reached, so the machines that know it more than one way list it once.
// The UID is not a credential: it only says which entries are one computer.
const MachineUIDFileName = "machine-uid"

// ValidMachineUID reports whether value is a machine UID: a UUIDv4 in its
// canonical lower-case form.
func ValidMachineUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122 && parsed.String() == value
}

// NormalizeMachineUID returns value as a machine UID, or "" when it is not one.
func NormalizeMachineUID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if !ValidMachineUID(value) {
		return ""
	}
	return value
}

// MachineUIDPath is where the machine UID lives for a state directory (see
// StateDir).
func MachineUIDPath(dir string) (string, error) {
	dir, err := StateDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, MachineUIDFileName), nil
}

// LoadOrCreateMachineUID returns the machine UID kept in the state directory.
// A missing file is created; a file that does not hold a UUIDv4 is replaced,
// so a damaged file never stops the daemon.
func LoadOrCreateMachineUID(dir string) (string, error) {
	path, err := MachineUIDPath(dir)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if uid := NormalizeMachineUID(string(data)); uid != "" {
			// The file is this machine's alone, as when it was written.
			if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm() != 0o600 {
				_ = os.Chmod(path, 0o600)
			}
			return uid, nil
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return writeMachineUID(path)
}

// ResetMachineUID gives this machine a new UID, for a VM cloned with the
// state directory of another: until it is reset, both report one identity.
// The running daemon reads the UID when it starts, so restart it afterwards.
func ResetMachineUID(dir string) (string, error) {
	path, err := MachineUIDPath(dir)
	if err != nil {
		return "", err
	}
	return writeMachineUID(path)
}

func writeMachineUID(path string) (string, error) {
	uid := uuid.New().String()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := writePrivate(path, []byte(uid+"\n")); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return uid, nil
}
