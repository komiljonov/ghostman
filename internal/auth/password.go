// Package auth implements password hashing and session-token generation. It
// deliberately knows nothing about HTTP or the database.
package auth

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

// argon2id parameters for newly created hashes. Existing hashes are verified
// with whatever parameters they were created with, so these can be raised later
// without invalidating stored passwords.
const (
	argonTime    uint32 = 1
	argonMemory  uint32 = 64 * 1024 // KiB, i.e. 64 MiB
	argonThreads uint8  = 4
	argonSaltLen uint32 = 16
	argonKeyLen  uint32 = 32
)

// Bounds applied to parameters parsed out of a stored hash. They stop a
// corrupted or hostile row from making the server allocate absurd amounts of
// memory during verification.
const (
	maxParsedMemory  uint32 = 1 << 20 // KiB, i.e. 1 GiB
	maxParsedTime    uint32 = 16
	minParsedSaltLen        = 8
	minParsedKeyLen         = 16
	maxParsedKeyLen         = 64
)

var (
	// ErrInvalidHash means the stored string is not a hash this package wrote.
	ErrInvalidHash = errors.New("auth: password hash is not a valid argon2id encoding")

	// ErrIncompatibleVersion means the hash was produced by a different argon2
	// version than the one this binary links against.
	ErrIncompatibleVersion = errors.New("auth: unsupported argon2 version")
)

// HashPassword returns an encoded argon2id hash in the standard format:
//
//	$argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>
//
// Salt and hash are unpadded standard base64.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generating salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the encoded hash. The
// comparison is constant time, and the cost parameters come from the stored
// string rather than the constants above.
func VerifyPassword(password, encoded string) (bool, error) {
	params, salt, key, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}

	candidate := argon2.IDKey(
		[]byte(password),
		salt,
		params.time,
		params.memory,
		params.threads,
		uint32(len(key)), //nolint:gosec // length is bounded by decodeHash
	)

	return subtle.ConstantTimeCompare(key, candidate) == 1, nil
}

// DummyVerify performs a verification against a throwaway hash. Login uses it
// when the email is unknown so that the response time does not reveal whether
// an account exists.
func DummyVerify(password string) {
	_, _ = VerifyPassword(password, dummyHash())
}

// dummyHash is computed at most once, and only if DummyVerify is ever called.
var dummyHash = sync.OnceValue(func() string {
	hash, err := HashPassword("ghostman-dummy-password")
	if err != nil {
		// rand.Read failing means the process cannot do crypto at all; an
		// unparseable string still costs a decode, which is all this is for.
		return ""
	}
	return hash
})

// hashParams are the cost parameters encoded in a stored hash.
type hashParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func decodeHash(encoded string) (hashParams, []byte, []byte, error) {
	// Format: ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	if parts[1] != "argon2id" {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return hashParams{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return hashParams{}, nil, nil, ErrIncompatibleVersion
	}

	var params hashParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.time, &params.threads); err != nil {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	if params.memory == 0 || params.memory > maxParsedMemory ||
		params.time == 0 || params.time > maxParsedTime ||
		params.threads == 0 {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < minParsedSaltLen {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < minParsedKeyLen || len(key) > maxParsedKeyLen {
		return hashParams{}, nil, nil, ErrInvalidHash
	}

	return params, salt, key, nil
}
