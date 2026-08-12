package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

func TestNewSessionToken(t *testing.T) {
	token, tokenHash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken() error: %v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not raw base64url: %v", err)
	}

	if len(raw) != tokenBytes {
		t.Errorf("token carries %d bytes of entropy, want %d", len(raw), tokenBytes)
	}

	if len(tokenHash) != sha256.Size {
		t.Errorf("hash length = %d, want %d", len(tokenHash), sha256.Size)
	}

	// The stored hash must be the SHA-256 of the token string the client sends.
	want := sha256.Sum256([]byte(token))
	if !bytes.Equal(tokenHash, want[:]) {
		t.Error("returned hash is not SHA-256 of the token")
	}

	// The raw token must not be recoverable from what gets stored.
	if bytes.Contains(tokenHash, raw) {
		t.Error("stored hash contains the raw token bytes")
	}
}

func TestNewSessionTokenIsUnique(t *testing.T) {
	const iterations = 100

	seen := make(map[string]struct{}, iterations)

	for range iterations {
		token, _, err := NewSessionToken()
		if err != nil {
			t.Fatalf("NewSessionToken() error: %v", err)
		}

		if _, duplicate := seen[token]; duplicate {
			t.Fatal("NewSessionToken() returned a duplicate token")
		}
		seen[token] = struct{}{}
	}
}

func TestHashToken(t *testing.T) {
	const token = "a-token"

	want := sha256.Sum256([]byte(token))
	if got := HashToken(token); !bytes.Equal(got, want[:]) {
		t.Errorf("HashToken() = %x, want %x", got, want)
	}

	// Deterministic: the same token always maps to the same row.
	if !bytes.Equal(HashToken(token), HashToken(token)) {
		t.Error("HashToken() is not deterministic")
	}

	if bytes.Equal(HashToken(token), HashToken(token+"x")) {
		t.Error("HashToken() collided on different tokens")
	}
}

func TestSessionTTL(t *testing.T) {
	if want := 30 * 24 * time.Hour; SessionTTL != want {
		t.Errorf("SessionTTL = %v, want %v", SessionTTL, want)
	}
}
