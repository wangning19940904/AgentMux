//go:build !darwin

package store

func loadOrCreateObservationMasterKey(string) ([]byte, error) {
	return nil, errObservationSecureKeyUnavailable
}
