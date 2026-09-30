package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	passwordMemory  = 64 * 1024
	passwordTime    = 3
	passwordThreads = 2
	passwordKeyLen  = 32
	passwordSaltLen = 16
)

func HashPassword(password string) (string, error) {
	if len(password) < 6 || len(password) > 1024 {
		return "", fmt.Errorf("password must be between 6 and 1024 characters")
	}
	salt := make([]byte, passwordSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, passwordTime, passwordMemory, passwordThreads, passwordKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$pibase$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", passwordMemory, passwordTime, passwordThreads, enc.EncodeToString(salt), enc.EncodeToString(hash)), nil
}

func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 7 || parts[1] != "pibase" || parts[2] != "argon2id" || parts[3] != "v=19" {
		return false
	}
	var memory, iterations, threads uint32
	if _, err := fmt.Sscanf(parts[4], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil || memory == 0 || iterations == 0 || threads == 0 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[5])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := enc.DecodeString(parts[6])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func RandomToken(size int) (string, []byte, error) {
	if size < 16 || size > 128 {
		return "", nil, fmt.Errorf("token size is out of range")
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	// SHA-256 is deliberately used for opaque random tokens: unlike passwords,
	// these values have 128+ bits of entropy and are never guessable offline.
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func UUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
