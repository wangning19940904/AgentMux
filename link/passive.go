package link

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Passive link.
//
// Usually a node dials its hub (Client), so the node needs no public address.
// A node with an address the hub can reach can instead listen, and the hub
// dials it: for a hub behind NAT, which its nodes cannot reach. The node
// keeps a TLS key of its own, and the hub pins that key's SPKI fingerprint,
// which it takes from the node's invite; nothing else about the certificate
// is checked. The node in turn admits only the holder of the invite's
// one-time secret (to pair) and then only the credential that hub gave it.
//
// Once the connection is up, both modes are the same: the hub is the
// multiplexer's client (Hub.serveSession), the node its server
// (Client.serveSession), and nothing above the session knows who dialed.

const (
	// PassiveInfoPath answers who the node is, to the invite's secret or the
	// paired hub's credential.
	PassiveInfoPath = "/link/v1/passive/info"
	// PassivePairPath trades the invite's secret for the hub's credential.
	PassivePairPath = "/link/v1/passive/pair"
	// PassiveConnectPath upgrades to the multiplexed connection.
	PassiveConnectPath = "/link/v1/passive/connect"

	// headerDeviceID and headerDeviceName tell a node which device it is at
	// the hub that dialed it, for its own status.
	headerDeviceID   = "X-AgentMux-Device-ID"
	headerDeviceName = "X-AgentMux-Device-Name"

	invitePrefix     = "amxi_"
	credentialPrefix = "amxc_"

	// PassiveSecretTTL is how long an invite can be used.
	PassiveSecretTTL = 30 * time.Minute
	// DefaultPassiveListen is where a node listens when it is not told.
	DefaultPassiveListen = ":8767"

	// A node counts failed secrets and credentials per source: ten in a
	// minute hold the source off for the rest of that minute.
	passiveFailures    = 10
	passiveFailureSpan = time.Minute
)

// ErrPassiveRefused means the node does not accept the hub's credential: it
// paired with another hub, or forgot this one. The hub stops dialing it.
var ErrPassiveRefused = errors.New("the node no longer accepts this hub's credential; ask it for a new invite and add it again")

// FingerprintMismatchError means the node at Addr presented another key than
// the one pinned for it. The hub never connects then, and never falls back to
// trusting the certificate any other way.
type FingerprintMismatchError struct {
	Addr string
	Want string
	Got  string
}

func (e *FingerprintMismatchError) Error() string {
	return fmt.Sprintf("the node at %s presented key %s, not the pinned %s: it is another machine, or its key was replaced; "+
		"if the key was replaced on purpose, add the node again from a new invite", e.Addr, e.Got, e.Want)
}

// IsFingerprintMismatch reports whether err is a pinned key that did not match.
func IsFingerprintMismatch(err error) bool {
	var mismatch *FingerprintMismatchError
	return errors.As(err, &mismatch)
}

// Invite is everything a hub needs to dial and pair a node that listens:
// where the node is (best address first), its key's SPKI fingerprint, and a
// one-time secret.
type Invite struct {
	Addrs       []string `json:"a"`
	Fingerprint string   `json:"f"`
	Secret      string   `json:"s"`
	// Expires is when the secret stops working, in Unix seconds.
	Expires int64 `json:"e,omitempty"`
}

func (i Invite) String() string {
	data, _ := json.Marshal(i)
	return invitePrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseInvite reads an invite printed by a node.
func ParseInvite(value string) (Invite, error) {
	value = strings.TrimSpace(value)
	encoded, ok := strings.CutPrefix(value, invitePrefix)
	if !ok {
		return Invite{}, errors.New("not a node invite: it should start with " + invitePrefix)
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	var invite Invite
	if err != nil || json.Unmarshal(data, &invite) != nil {
		return Invite{}, errors.New("the invite is damaged; copy it again from the node")
	}
	if invite.Fingerprint, err = NormalizeFingerprint(invite.Fingerprint); err != nil {
		return Invite{}, err
	}
	if strings.TrimSpace(invite.Secret) == "" {
		return Invite{}, errors.New("the invite has no pairing secret")
	}
	if invite.Addrs, err = NormalizePassiveAddrs(invite.Addrs); err != nil {
		return Invite{}, err
	}
	return invite, nil
}

// NormalizeFingerprint accepts a SHA-256 fingerprint in hex, with or without
// colons, in either case.
func NormalizeFingerprint(value string) (string, error) {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
	if decoded, err := hex.DecodeString(value); err != nil || len(decoded) != sha256.Size {
		return "", errors.New("the key fingerprint must be 64 hex digits (SHA-256)")
	}
	return value, nil
}

// NormalizePassiveAddrs checks the addresses a hub may dial a node at: at
// least one, each host:port, without duplicates.
func NormalizePassiveAddrs(addrs []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil || host == "" {
			return nil, fmt.Errorf("node address %q must be host:port", addr)
		}
		if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
			return nil, fmt.Errorf("node address %q has no valid port", addr)
		}
		addr = net.JoinHostPort(host, port)
		if !seen[addr] {
			seen[addr] = true
			out = append(out, addr)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("at least one node address is needed")
	}
	if len(out) > 16 {
		return nil, errors.New("at most 16 node addresses are allowed")
	}
	return out, nil
}

// NewPassiveCredential creates the secret a hub presents to a node that
// listens. The node keeps only its hash.
func NewPassiveCredential() string {
	return credentialPrefix + randomToken(32)
}

func validCredential(value string) bool {
	return strings.HasPrefix(value, credentialPrefix) && len(value) >= 32 && len(value) <= 512 &&
		!strings.ContainsAny(value, " \t\r\n")
}

func randomToken(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic("link: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func secretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// sameHash compares a value against a stored hash in constant time.
func sameHash(value, hash string) bool {
	if hash == "" || value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secretHash(value)), []byte(hash)) == 1
}

// pinnedTLS trusts exactly the key with this SPKI fingerprint at addr: not its
// name, not its issuer, not its dates.
func pinnedTLS(addr, fingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		// The pinned key replaces the name and authority checks.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("the node sent no certificate")
			}
			got := SPKIFingerprint(state.PeerCertificates[0])
			if subtle.ConstantTimeCompare([]byte(got), []byte(fingerprint)) != 1 {
				return &FingerprintMismatchError{Addr: addr, Want: fingerprint, Got: got}
			}
			return nil
		},
	}
}
