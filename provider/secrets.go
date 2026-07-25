package provider

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

const providerSecretService = "AgentMux Provider API Keys"

var errProviderSecretNotFound = errors.New("provider API key not found")

type providerSecretBackend interface {
	Save(account, secret string) error
	Load(account string) (string, error)
	Delete(account string) error
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

func setProviderSecretBackendForTest(backend providerSecretBackend) func() {
	providerSecretsMu.Lock()
	previous := providerSecrets
	providerSecrets = backend
	providerSecretsMu.Unlock()
	return func() {
		providerSecretsMu.Lock()
		providerSecrets = previous
		providerSecretsMu.Unlock()
	}
}
