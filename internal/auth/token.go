package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

const (
	// tokenBytes is the amount of entropy in a session token.
	tokenBytes = 32

	// SessionTTL is how long a session stays valid after it is created.
	SessionTTL = 30 * 24 * time.Hour
)

// NewSessionToken returns a fresh session token and the SHA-256 hash to store
// alongside the session. The raw token is shown to the client exactly once; the
// database only ever holds the hash, so a leaked dump yields no usable tokens.
func NewSessionToken() (token string, tokenHash []byte, err error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("auth: generating session token: %w", err)
	}

	token = base64.RawURLEncoding.EncodeToString(raw)

	return token, HashToken(token), nil
}

// HashToken returns the SHA-256 of a session token as presented by the client.
// A plain hash (no salt, no stretching) is the right tool here: the input is
// 256 bits of uniform randomness, so it is not guessable.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
