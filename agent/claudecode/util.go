package claudecode

import (
	"crypto/rand"
	"encoding/hex"
)

// randID returns a short random hex id for session naming.
func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
