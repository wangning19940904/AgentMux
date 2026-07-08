//go:build darwin

package provider

import (
	"fmt"
	"os/exec"
	"strings"
)

type keychainSecretBackend struct{}

func newProviderSecretBackend() providerSecretBackend {
	return keychainSecretBackend{}
}

func (keychainSecretBackend) Save(account, secret string) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", providerSecretService, "-a", account, "-w", secret)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("save provider API key to macOS Keychain: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (keychainSecretBackend) Load(account string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", providerSecretService, "-a", account, "-w")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "could not be found") {
			return "", errProviderSecretNotFound
		}
		return "", fmt.Errorf("load provider API key from macOS Keychain: %w: %s", err, msg)
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

func (keychainSecretBackend) Delete(account string) error {
	cmd := exec.Command("security", "delete-generic-password", "-s", providerSecretService, "-a", account)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "could not be found") {
			return errProviderSecretNotFound
		}
		return fmt.Errorf("delete provider API key from macOS Keychain: %w: %s", err, msg)
	}
	return nil
}
