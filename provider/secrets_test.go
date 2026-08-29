package provider

import (
	"os"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

type memorySecretBackend struct {
	values map[string]string
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

func (m *memorySecretBackend) Save(account, secret string) error {
	m.values[account] = secret
	return nil
}

func (m *memorySecretBackend) Load(account string) (string, error) {
	secret, ok := m.values[account]
	if !ok {
		return "", errProviderSecretNotFound
	}
	return secret, nil
}

func (m *memorySecretBackend) Delete(account string) error {
	if _, ok := m.values[account]; !ok {
		return errProviderSecretNotFound
	}
	delete(m.values, account)
	return nil
}

func TestProviderAPIKeyPersistsAndRestoresEnv(t *testing.T) {
	restore := setProviderSecretBackendForTest(&memorySecretBackend{values: map[string]string{}})
	defer restore()

	envName := "AGENTMUX_PROVIDER_TEST_RELAY_API_KEY"
	t.Cleanup(func() { _ = os.Unsetenv(envName) })

	if err := SaveProviderAPIKey(envName, "sk-saved"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(envName); got != "sk-saved" {
		t.Fatalf("saved env = %q", got)
	}
	if err := os.Unsetenv(envName); err != nil {
		t.Fatal(err)
	}

	p := &core.Provider{ID: "test-relay", APIKeyEnv: envName}
	if issue := providerAPIKeyIssue(p); issue != "" {
		t.Fatalf("providerAPIKeyIssue = %q", issue)
	}
	if got := providerAPIKey(p); got != "sk-saved" {
		t.Fatalf("restored key = %q", got)
	}
	if got := os.Getenv(envName); got != "sk-saved" {
		t.Fatalf("restored env = %q", got)
	}
}

func TestDeleteProviderAPIKeyRemovesSavedSecret(t *testing.T) {
	backend := &memorySecretBackend{values: map[string]string{}}
	restore := setProviderSecretBackendForTest(backend)
	defer restore()

	envName := "AGENTMUX_PROVIDER_DELETE_ME_API_KEY"
	t.Cleanup(func() { _ = os.Unsetenv(envName) })

	if err := SaveProviderAPIKey(envName, "sk-delete"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteProviderAPIKey(envName); err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.values[envName]; ok {
		t.Fatalf("secret still stored: %#v", backend.values)
	}
	if got := os.Getenv(envName); got != "" {
		t.Fatalf("env still set = %q", got)
	}
	ok, err := EnsureProviderAPIKeyEnv(envName)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("deleted key should not restore")
	}
}

func TestMigratingSecretBackendRestoresAndCopiesLegacySecret(t *testing.T) {
	current := &memorySecretBackend{values: map[string]string{}}
	legacy := &memorySecretBackend{values: map[string]string{
		"AGENTNEXUS_PROVIDER_LEGACY_API_KEY": "sk-legacy",
	}}
	restore := setProviderSecretBackendForTest(migratingSecretBackend{
		current: current,
		legacy:  legacy,
	})
	defer restore()

	envName := "AGENTNEXUS_PROVIDER_LEGACY_API_KEY"
	t.Cleanup(func() { _ = os.Unsetenv(envName) })

	ok, err := EnsureProviderAPIKeyEnv(envName)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("legacy key should restore")
	}
	if got := os.Getenv(envName); got != "sk-legacy" {
		t.Fatalf("restored env = %q", got)
	}
	if got := current.values[envName]; got != "sk-legacy" {
		t.Fatalf("migrated secret = %q", got)
	}
}

func TestMigratingSecretBackendDeleteRemovesCurrentAndLegacySecrets(t *testing.T) {
	envName := "AGENTNEXUS_PROVIDER_DELETE_LEGACY_API_KEY"
	current := &memorySecretBackend{values: map[string]string{envName: "sk-current"}}
	legacy := &memorySecretBackend{values: map[string]string{envName: "sk-legacy"}}
	restore := setProviderSecretBackendForTest(migratingSecretBackend{
		current: current,
		legacy:  legacy,
	})
	defer restore()
	t.Cleanup(func() { _ = os.Unsetenv(envName) })

	if err := DeleteProviderAPIKey(envName); err != nil {
		t.Fatal(err)
	}
	if _, ok := current.values[envName]; ok {
		t.Fatal("current secret still stored")
	}
	if _, ok := legacy.values[envName]; ok {
		t.Fatal("legacy secret still stored")
	}
}
