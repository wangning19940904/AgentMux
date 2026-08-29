//go:build darwin

package provider

import (
	"fmt"
	"os/exec"
	"strings"
)

const (
	providerSecretService       = "AgentMux Provider API Keys"
	legacyProviderSecretService = "AgentNexus Provider API Keys"
)

type keychainSecretBackend struct {
	service string
}

func newProviderSecretBackend() providerSecretBackend {
	return migratingSecretBackend{
		current: keychainSecretBackend{service: providerSecretService},
		legacy:  keychainSecretBackend{service: legacyProviderSecretService},
	}
}

func (b keychainSecretBackend) Save(account, secret string) error {
	cmd := exec.Command("security", "add-generic-password", "-U", "-s", b.service, "-a", account, "-w", secret)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("save provider API key to macOS Keychain: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b keychainSecretBackend) Load(account string) (string, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", b.service, "-a", account, "-w")
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

func (b keychainSecretBackend) Delete(account string) error {
	cmd := exec.Command("security", "delete-generic-password", "-s", b.service, "-a", account)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "could not be found") {
			return errProviderSecretNotFound
		}
		return fmt.Errorf("delete provider API key from macOS Keychain: %w: %s", err, msg)
	}
	return nil
}
