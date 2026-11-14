package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
)

// Credentials such as device tokens and account sessions are stored as
// SHA-256 hashes: the plaintext is shown once at creation, so a database copy
// cannot be replayed against the API.

const (
	secretBytes          = 32
	secretDisplayedChars = 12
)

// HashSecret is the one-way transform applied to credentials before they
// touch the database.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func generateSecret(prefix string) string {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return prefix + hex.EncodeToString(buf)
}

func secretDisplayPrefix(secret string) string {
	if len(secret) <= secretDisplayedChars {
		return secret
	}
	return secret[:secretDisplayedChars]
}

func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

// newRecordID returns a random identifier such as "acct_1a2b3c4d5e6f7a8b".
func newRecordID(kind string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return kind + "_" + hex.EncodeToString(buf)
}
