// Package secret encrypts credentials at rest with AES-256-GCM under a
// per-install master key. The key lives in the data directory, is created on
// first use and is never part of any backup the API hands out.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Keyring holds the master key.
type Keyring struct {
	aead cipher.AEAD
}

const keyBytes = 32

// ErrNoKey is returned by Load when the key file does not exist.
var ErrNoKey = errors.New("master key not found")

// LoadOrCreate reads the hex-encoded key at path, generating one with 0600
// permissions if it is missing.
func LoadOrCreate(path string) (*Keyring, error) {
	kr, err := Load(path)
	if err == nil {
		return kr, nil
	}
	if !errors.Is(err, ErrNoKey) {
		return nil, err
	}
	raw := make([]byte, keyBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}
	// Write via a temp file so a crash never leaves a truncated key behind.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(hex.EncodeToString(raw)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("install master key: %w", err)
	}
	return FromBytes(raw)
}

// Load reads an existing key.
func Load(path string) (*Keyring, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoKey
		}
		return nil, fmt.Errorf("read master key: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	return FromBytes(raw)
}

// FromBytes builds a Keyring from a raw 32-byte key (tests, importers).
func FromBytes(raw []byte) (*Keyring, error) {
	if len(raw) != keyBytes {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", keyBytes, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keyring{aead: aead}, nil
}

// Encrypt seals plaintext. The nonce is prepended; an empty plaintext yields
// nil so "no credential" round-trips as NULL in the database.
func (k *Keyring) Encrypt(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return k.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt opens a value produced by Encrypt. nil/empty input yields "".
func (k *Keyring) Decrypt(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	ns := k.aead.NonceSize()
	if len(sealed) < ns {
		return "", errors.New("ciphertext too short")
	}
	pt, err := k.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}
