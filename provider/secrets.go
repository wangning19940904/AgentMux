package provider

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

const (
	providerSecretService       = "AgentMux Provider API Keys"
	legacyProviderSecretService = "AgentNexus Provider API Keys"
)

var errProviderSecretNotFound = errors.New("provider API key not found")

type providerSecretBackend interface {
	Save(account, secret string) error
	Load(account string) (string, error)
	Delete(account string) error
}

// migratingSecretBackend keeps provider credentials available across the
// AgentNexus -> AgentMux rename. Reads prefer the current service and
// transparently copy legacy-only entries into it. Deletes cover both services
// so a removed key cannot be restored from a stale legacy entry later.
type migratingSecretBackend struct {
	current providerSecretBackend
	legacy  providerSecretBackend
}

func (m migratingSecretBackend) Save(account, secret string) error {
	return m.current.Save(account, secret)
}

func (m migratingSecretBackend) Load(account string) (string, error) {
	secret, err := m.current.Load(account)
	if err == nil || !errors.Is(err, errProviderSecretNotFound) {
		return secret, err
	}
	secret, err = m.legacy.Load(account)
	if err != nil {
		return "", err
	}
	if err := m.current.Save(account, secret); err != nil {
		return "", fmt.Errorf("migrate legacy provider API key: %w", err)
	}
	return secret, nil
}

func (m migratingSecretBackend) Delete(account string) error {
	currentErr := m.current.Delete(account)
	legacyErr := m.legacy.Delete(account)
	currentMissing := errors.Is(currentErr, errProviderSecretNotFound)
	legacyMissing := errors.Is(legacyErr, errProviderSecretNotFound)

	switch {
	case currentErr != nil && !currentMissing && legacyErr != nil && !legacyMissing:
		return errors.Join(currentErr, legacyErr)
	case currentErr != nil && !currentMissing:
		return currentErr
	case legacyErr != nil && !legacyMissing:
		return legacyErr
	case currentMissing && legacyMissing:
		return errProviderSecretNotFound
	default:
		return nil
	}
}

var (
	providerSecretsMu sync.Mutex
	providerSecrets   = newProviderSecretBackend()
)

// SaveProviderAPIKey stores a provider key in the OS secret backend and
// mirrors it into the current process environment for immediate use.
func SaveProviderAPIKey(apiKeyEnv, apiKey string) error {
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	apiKey = strings.TrimSpace(apiKey)
	if apiKeyEnv == "" || apiKey == "" {
		return nil
	}
	if err := os.Setenv(apiKeyEnv, apiKey); err != nil {
		return fmt.Errorf("set API key env %s: %w", apiKeyEnv, err)
	}
	providerSecretsMu.Lock()
	defer providerSecretsMu.Unlock()
	if err := providerSecrets.Save(apiKeyEnv, apiKey); err != nil {
		return err
	}
	return nil
}

// EnsureProviderAPIKeyEnv restores a saved provider key into the current
// process environment. It returns false when no saved key exists.
func EnsureProviderAPIKeyEnv(apiKeyEnv string) (bool, error) {
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	if apiKeyEnv == "" {
		return false, nil
	}
	if os.Getenv(apiKeyEnv) != "" {
		return true, nil
	}
	providerSecretsMu.Lock()
	secret, err := providerSecrets.Load(apiKeyEnv)
	providerSecretsMu.Unlock()
	if errors.Is(err, errProviderSecretNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return false, nil
	}
	if err := os.Setenv(apiKeyEnv, secret); err != nil {
		return false, fmt.Errorf("set API key env %s: %w", apiKeyEnv, err)
	}
	return true, nil
}

// DeleteProviderAPIKey removes a provider key from the OS secret backend and
// clears the matching process environment variable.
func DeleteProviderAPIKey(apiKeyEnv string) error {
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	if apiKeyEnv == "" {
		return nil
	}
	_ = os.Unsetenv(apiKeyEnv)
	providerSecretsMu.Lock()
	err := providerSecrets.Delete(apiKeyEnv)
	providerSecretsMu.Unlock()
	if errors.Is(err, errProviderSecretNotFound) {
		return nil
	}
	return err
}

func providerAPIKeyFromEnvOrSecret(apiKeyEnv string) (string, error) {
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	if apiKeyEnv == "" {
		return "", nil
	}
	if v := os.Getenv(apiKeyEnv); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	ok, err := EnsureProviderAPIKeyEnv(apiKeyEnv)
	if err != nil || !ok {
		return "", err
	}
	return strings.TrimSpace(os.Getenv(apiKeyEnv)), nil
}
