package store

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Passwords are stored as argon2id hashes in the PHC string format, so the
// parameters travel with each hash and can be raised later without breaking
// existing accounts.

type argon2Params struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
	keyLen    uint32
	saltLen   int
}

// passwordParams follows the OWASP recommendation for argon2id with room to
// spare: 64 MiB, two passes.
var passwordParams = argon2Params{memoryKiB: 64 * 1024, time: 2, threads: 2, keyLen: 32, saltLen: 16}

var errMalformedPasswordHash = errors.New("malformed password hash")

// HashPassword returns the argon2id hash of a password.
func HashPassword(password string) (string, error) {
	params := passwordParams
	salt := make([]byte, params.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, params.time, params.memoryKiB, params.threads, params.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		params.memoryKiB, params.time, params.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches an encoded hash. A
// malformed hash matches nothing.
func VerifyPassword(encoded, password string) bool {
	params, salt, key, err := decodePasswordHash(encoded)
	if err != nil {
		return false
	}
	candidate := argon2.IDKey([]byte(password), salt, params.time, params.memoryKiB, params.threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(candidate, key) == 1
}

func decodePasswordHash(encoded string) (argon2Params, []byte, []byte, error) {
	var params argon2Params
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return params, nil, nil, errMalformedPasswordHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return params, nil, nil, errMalformedPasswordHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memoryKiB, &params.time, &params.threads); err != nil {
		return params, nil, nil, errMalformedPasswordHash
	}
	// Refuse parameters no hash written here would carry, so a tampered row
	// cannot make one sign-in attempt consume the machine.
	if params.memoryKiB == 0 || params.memoryKiB > 1024*1024 || params.time == 0 || params.time > 16 || params.threads == 0 {
		return params, nil, nil, errMalformedPasswordHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return params, nil, nil, errMalformedPasswordHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return params, nil, nil, errMalformedPasswordHash
	}
	return params, salt, key, nil
}

// GeneratePassword returns a random 23-character password for an
// administrator to hand over; the person should change it after signing in.
func GeneratePassword() string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	text := fmt.Sprintf("%x", buf)
	return text[:5] + "-" + text[5:10] + "-" + text[10:15] + "-" + text[15:20]
}

// dummyPasswordHash is verified against when a login does not exist, so a
// failed sign-in takes as long whether or not the account is real.
var dummyPasswordHash = sync.OnceValue(func() string {
	hash, err := HashPassword("agentmux-dummy-password")
	if err != nil {
		panic("store: cannot hash dummy password: " + err.Error())
	}
	return hash
})
