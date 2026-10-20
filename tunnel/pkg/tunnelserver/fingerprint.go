package tunnelserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// fingerprintHexLen is the number of hex characters kept from the SHA-256 digest.
// 12 characters (48 bits) tell tunnels apart in logs without being a usable
// credential.
const fingerprintHexLen = 12

// APIKeyFingerprint returns a short, stable, non-reversible identifier for an
// API key. Logs, admin API responses, metrics, error messages and warnings must
// show this instead of the key itself: anyone who can read them would otherwise
// be able to connect as that user. The same key always yields the same value, so
// entries can still be correlated.
func APIKeyFingerprint(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:fingerprintHexLen]
}

var requestIDFallback atomic.Uint64

// newRequestID returns an unpredictable identifier for one tunnelled request.
// It carries no information about the API key.
func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// crypto/rand does not fail on supported platforms; keep ids unique anyway.
		return fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), requestIDFallback.Add(1))
	}
	return hex.EncodeToString(raw[:])
}
