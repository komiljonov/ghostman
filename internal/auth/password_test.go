package auth

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

const testPassword = "correct horse battery staple"

func TestHashPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}

	ok, err := VerifyPassword(testPassword, encoded)
	if err != nil {
		t.Fatalf("VerifyPassword() error: %v", err)
	}

	if !ok {
		t.Error("VerifyPassword() = false for the correct password, want true")
	}
}

func TestVerifyPasswordWrong(t *testing.T) {
	encoded, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}

	for _, wrong := range []string{"", "wrong password", testPassword + "x", strings.ToUpper(testPassword)} {
		ok, err := VerifyPassword(wrong, encoded)
		if err != nil {
			t.Fatalf("VerifyPassword(%q) error: %v", wrong, err)
		}
		if ok {
			t.Errorf("VerifyPassword(%q) = true, want false", wrong)
		}
	}
}

func TestHashPasswordEncoding(t *testing.T) {
	encoded, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		t.Fatalf("encoded hash has %d segments, want 6: %q", len(parts), encoded)
	}

	if parts[1] != "argon2id" {
		t.Errorf("variant = %q, want %q", parts[1], "argon2id")
	}

	if want := "v=19"; parts[2] != want {
		t.Errorf("version = %q, want %q", parts[2], want)
	}

	if want := "m=65536,t=1,p=4"; parts[3] != want {
		t.Errorf("parameters = %q, want %q", parts[3], want)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatalf("decoding salt: %v", err)
	}
	if len(salt) != int(argonSaltLen) {
		t.Errorf("salt length = %d, want %d", len(salt), argonSaltLen)
	}

	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		t.Fatalf("decoding key: %v", err)
	}
	if len(key) != int(argonKeyLen) {
		t.Errorf("key length = %d, want %d", len(key), argonKeyLen)
	}

	// The stored key must be exactly what argon2id produces for this salt.
	want := argon2.IDKey([]byte(testPassword), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if string(key) != string(want) {
		t.Error("stored key does not match a recomputed argon2id key")
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	first, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}

	second, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}

	if first == second {
		t.Error("hashing the same password twice produced identical output, so the salt is not random")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	valid, err := HashPassword(testPassword)
	if err != nil {
		t.Fatalf("HashPassword() error: %v", err)
	}
	parts := strings.Split(valid, "$")

	tests := []struct {
		name    string
		encoded string
		wantErr error
	}{
		{name: "empty", encoded: "", wantErr: ErrInvalidHash},
		{name: "not a hash", encoded: "hunter2", wantErr: ErrInvalidHash},
		{name: "too few segments", encoded: "$argon2id$v=19$m=65536,t=1,p=4$c2FsdA", wantErr: ErrInvalidHash},
		{name: "wrong variant", encoded: "$argon2i$" + strings.Join(parts[2:], "$"), wantErr: ErrInvalidHash},
		{name: "unparseable params", encoded: "$argon2id$v=19$m=x,t=1,p=4$" + parts[4] + "$" + parts[5], wantErr: ErrInvalidHash},
		{name: "zero memory", encoded: "$argon2id$v=19$m=0,t=1,p=4$" + parts[4] + "$" + parts[5], wantErr: ErrInvalidHash},
		{name: "absurd memory", encoded: "$argon2id$v=19$m=99999999,t=1,p=4$" + parts[4] + "$" + parts[5], wantErr: ErrInvalidHash},
		{name: "short salt", encoded: "$argon2id$v=19$m=65536,t=1,p=4$YWJj$" + parts[5], wantErr: ErrInvalidHash},
		{name: "bad base64 key", encoded: "$argon2id$v=19$m=65536,t=1,p=4$" + parts[4] + "$!!!!", wantErr: ErrInvalidHash},
		{name: "old version", encoded: "$argon2id$v=16$m=65536,t=1,p=4$" + parts[4] + "$" + parts[5], wantErr: ErrIncompatibleVersion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := VerifyPassword(testPassword, tt.encoded)
			if ok {
				t.Error("VerifyPassword() = true for a malformed hash, want false")
			}
			if err == nil {
				t.Fatalf("VerifyPassword() error = nil, want %v", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr.Error()) {
				t.Errorf("VerifyPassword() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyPasswordHonoursStoredParameters(t *testing.T) {
	// A hash created with different (lower) cost parameters must still verify,
	// because the parameters are read from the stored string.
	salt := []byte("sixteen-byte-slt")
	const (
		otherMemory  uint32 = 8 * 1024
		otherTime    uint32 = 2
		otherThreads uint8  = 1
	)

	key := argon2.IDKey([]byte(testPassword), salt, otherTime, otherMemory, otherThreads, argonKeyLen)
	encoded := "$argon2id$v=19$m=8192,t=2,p=1$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(key)

	ok, err := VerifyPassword(testPassword, encoded)
	if err != nil {
		t.Fatalf("VerifyPassword() error: %v", err)
	}
	if !ok {
		t.Error("VerifyPassword() = false, want true for a hash with non-default parameters")
	}
}

func TestDummyVerifyDoesNotPanic(t *testing.T) {
	// Login calls this for unknown emails; it must be safe and silent.
	DummyVerify("anything")
}
