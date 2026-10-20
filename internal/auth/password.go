// Package auth implements the single-admin login: argon2id password hashing,
// bootstrap password generation, cookie sessions with CSRF protection, API
// tokens, and the HTTP middleware that enforces them.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: 64 MiB, 3 passes, 1 lane. Tuned for a 4 GB gateway
// VM where a login takes ~100 ms, which also throttles brute force.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword returns a PHC-formatted argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC argon2id hash in constant time.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported hash format")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("parse hash params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// MinPasswordLen is enforced on change.
const MinPasswordLen = 10

// ValidateNewPassword applies the password policy.
func ValidateNewPassword(p string) error {
	if len(p) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(p) > 256 {
		return errors.New("password too long")
	}
	if strings.TrimSpace(p) != p {
		return errors.New("password must not start or end with whitespace")
	}
	return nil
}

// bootstrapAlphabet avoids look-alike characters so the generated password
// can be read off a terminal.
const bootstrapAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// GenerateBootstrapPassword returns a 20-character random password.
func GenerateBootstrapPassword() (string, error) {
	return randomString(bootstrapAlphabet, 20)
}

func randomString(alphabet string, n int) (string, error) {
	out := make([]byte, n)
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range out {
		out[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(out), nil
}

// randomToken returns n random bytes as URL-safe base64.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
