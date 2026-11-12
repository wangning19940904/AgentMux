package link

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	hubCertFile = "hub-cert.pem"
	hubKeyFile  = "hub-key.pem"
)

// Fingerprint is the SHA-256 of a certificate, in hex. A join token carries
// the hub's, so a node can trust a hub that has no domain name.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// LoadOrCreateCertificate returns the hub's own certificate from dir, creating
// it on first use. Nodes pin its fingerprint, so it is never replaced while it
// can still be read: a new certificate would lock every paired node out.
func LoadOrCreateCertificate(dir string) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, hubCertFile), filepath.Join(dir, hubKeyFile)
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, Fingerprint(cert.Certificate[0]), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, "", fmt.Errorf("read hub certificate in %s: %w", dir, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "AgentMux hub"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	// The key is written first: a certificate without its key would be kept
	// forever by the check above and never work.
	if err := writePrivate(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := writePrivate(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, Fingerprint(der), nil
}

func writePrivate(path string, data []byte) error {
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// clientTLS trusts exactly the certificate with this fingerprint. With no
// fingerprint, the system's certificate authorities decide as usual.
func clientTLS(fingerprint string) *tls.Config {
	fingerprint = strings.ToLower(strings.TrimSpace(fingerprint))
	if fingerprint == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The pinned fingerprint replaces the name and authority checks.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("the hub sent no certificate")
			}
			got := Fingerprint(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare([]byte(got), []byte(fingerprint)) != 1 {
				return fmt.Errorf("the hub's certificate %s is not the one this device paired with", got)
			}
			return nil
		},
	}
}
