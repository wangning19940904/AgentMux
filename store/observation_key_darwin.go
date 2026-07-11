//go:build darwin

package store

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
)

func loadOrCreateObservationMasterKey(account string) ([]byte, error) {
	command := exec.Command("/usr/bin/security", "find-generic-password", "-s", observationKeychainService, "-a", account, "-w")
	output, err := command.CombinedOutput()
	if err == nil {
		key, decodeErr := decodeObservationMasterKey(strings.TrimSpace(string(output)))
		if decodeErr != nil {
			return nil, fmt.Errorf("decode observability master key from macOS Keychain: %w", decodeErr)
		}
		return key, nil
	}
	message := strings.ToLower(strings.TrimSpace(string(output)))
	if !strings.Contains(message, "could not be found") && !strings.Contains(message, "item not found") {
		return nil, fmt.Errorf("%w: read macOS Keychain: %v: %s", errObservationSecureKeyUnavailable, err, strings.TrimSpace(string(output)))
	}

	key := make([]byte, observationMasterKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	command = exec.Command("/usr/bin/security", "add-generic-password", "-U", "-s", observationKeychainService, "-a", account, "-w", encoded)
	if output, err := command.CombinedOutput(); err != nil {
		clearObservationBytes(key)
		return nil, fmt.Errorf("%w: save macOS Keychain master key: %v: %s", errObservationSecureKeyUnavailable, err, strings.TrimSpace(string(output)))
	}
	return key, nil
}
