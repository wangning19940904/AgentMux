//go:build !darwin

package provider

type unsupportedSecretBackend struct{}

func newProviderSecretBackend() providerSecretBackend {
	return unsupportedSecretBackend{}
}

func (unsupportedSecretBackend) Save(account, secret string) error {
	return nil
}

func (unsupportedSecretBackend) Load(account string) (string, error) {
	return "", errProviderSecretNotFound
}

func (unsupportedSecretBackend) Delete(account string) error {
	return errProviderSecretNotFound
}
