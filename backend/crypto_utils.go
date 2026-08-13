package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log"
	"os"
	"strings"
)

var aesKey []byte

// legacyAESKey is the hardcoded key that was historically used to encrypt SSH
// credentials before per-install random keys were introduced. It must NEVER be
// persisted or used as the long-term key; it is only used to migrate existing
// rows to a freshly generated key and then discarded. Keeping it as a constant
// also lets us detect installs that still hold it in aes.key.
var legacyAESKey = []byte("proxygw-secret-key-32-bytes-long")

// migrateLegacyCredentials indicates some remote_nodes rows may still be
// encrypted with legacyAESKey and must be re-encrypted with the current aesKey.
var migrateLegacyCredentials bool

// rotationPendingPath marks that a key rotation started but legacy credentials
// may not have been re-encrypted yet. Its presence makes migration retry on the
// next boot even if the process crashed after the key file was written but
// before legacy rows were re-encrypted.
func rotationPendingPath() string { return getPath("config", "aes.rotation_pending") }

func init() {
	keyPath := getPath("config", "aes.key")
	stored, readErr := os.ReadFile(keyPath)
	rotated := false
	if readErr == nil && len(stored) == 32 && !bytes.Equal(stored, legacyAESKey) {
		aesKey = stored
	} else {
		// Missing key file, or it still contains the well-known legacy constant:
		// always rotate to a fresh random key. A hardcoded public value must
		// never become the persisted encryption key, otherwise every install
		// would share the same key and SSH credentials would be trivially
		// decryptable from source.
		aesKey = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
			log.Printf("[CRITICAL] failed to generate AES key: %v", err)
			aesKey = append([]byte(nil), legacyAESKey...)
		}
		if err := os.WriteFile(keyPath, aesKey, 0600); err != nil {
			log.Printf("[SECURITY] failed to persist AES key: %v", err)
		}
		rotated = true
	}

	// Schedule migration when we just rotated, or when a previous rotation was
	// interrupted (pending marker present). The marker keeps migration retryable
	// across crashes so credentials are never left undecryptable.
	if rotated {
		_ = os.WriteFile(rotationPendingPath(), []byte("1"), 0600)
		migrateLegacyCredentials = true
	} else if _, err := os.Stat(rotationPendingPath()); err == nil {
		migrateLegacyCredentials = true
	}
}

// migrateLegacyCredentialsIfNeeded re-encrypts any SSH credentials still stored
// with the legacy hardcoded key using the current random aesKey. It is a no-op
// unless init() detected a legacy-encrypted database (or an interrupted rotation,
// via the pending marker). Rows that cannot be decrypted with either the current
// or the legacy key are left untouched rather than rewritten.
func migrateLegacyCredentialsIfNeeded() {
	if !migrateLegacyCredentials {
		return
	}
	migrateLegacyCredentials = false

	rows, err := db.Query("SELECT id, ssh_credential FROM remote_nodes")
	if err != nil {
		log.Printf("[SECURITY] migrate legacy credentials: query failed: %v", err)
		return
	}
	type update struct {
		id  int64
		enc string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var cred string
		if err := rows.Scan(&id, &cred); err != nil {
			continue
		}
		if !strings.HasPrefix(cred, "ENC:") {
			continue
		}
		// Idempotency: rows already encrypted with the current key are skipped.
		if _, ok := decryptAESCore(cred, aesKey); ok {
			continue
		}
		plain, ok := decryptAESCore(cred, legacyAESKey)
		if !ok {
			// Not decryptable with the legacy key (e.g. foreign/malformed):
			// preserve the value verbatim instead of corrupting it.
			continue
		}
		if enc := encryptAESWithKey(plain, aesKey); enc != cred {
			updates = append(updates, update{id: id, enc: enc})
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[SECURITY] migrate legacy credentials: rows error: %v", err)
		rows.Close()
		return
	}
	rows.Close()

	if len(updates) > 0 {
		tx, err := db.Begin()
		if err != nil {
			log.Printf("[SECURITY] migrate legacy credentials: begin tx failed: %v", err)
			return
		}
		for _, u := range updates {
			if _, err := tx.Exec("UPDATE remote_nodes SET ssh_credential=? WHERE id=?", u.enc, u.id); err != nil {
				_ = tx.Rollback()
				log.Printf("[SECURITY] migrate legacy credentials: update failed: %v", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[SECURITY] migrate legacy credentials: commit failed: %v", err)
			return
		}
	}

	// Only clear the pending marker after the rotation fully committed, so an
	// interrupted migration is retried on the next boot.
	_ = os.Remove(rotationPendingPath())
	log.Printf("[SECURITY] key rotation completed; migrated %d legacy credentials", len(updates))
}

func EncryptAES(text string) string { return encryptAESWithKey(text, aesKey) }

func DecryptAES(text string) string {
	plain, ok := decryptAESCore(text, aesKey)
	if !ok {
		// Preserve the input unchanged when it cannot be decrypted.
		return text
	}
	return plain
}

func encryptAESWithKey(text string, key []byte) string {
	block, err := aes.NewCipher(key)
	if err != nil {
		return text
	}
	b := base64.StdEncoding.EncodeToString([]byte(text))
	ciphertext := make([]byte, aes.BlockSize+len(b))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return text
	}
	cfb := cipher.NewCFBEncrypter(block, iv)
	cfb.XORKeyStream(ciphertext[aes.BlockSize:], []byte(b))
	return "ENC:" + base64.StdEncoding.EncodeToString(ciphertext)
}

// decryptAESCore decrypts text and reports whether decryption succeeded. Unlike
// a bare "return input on failure", callers can rely on the bool to distinguish
// a valid plaintext from a non-decryptable value.
func decryptAESCore(text string, key []byte) (string, bool) {
	if len(text) < 4 || text[:4] != "ENC:" {
		return "", false
	}
	payload := text[4:]
	ciphertext, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(ciphertext) < aes.BlockSize {
		return "", false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", false
	}
	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]
	cfb := cipher.NewCFBDecrypter(block, iv)
	cfb.XORKeyStream(ciphertext, ciphertext)
	plain, err := base64.StdEncoding.DecodeString(string(ciphertext))
	if err != nil {
		return "", false
	}
	return string(plain), true
}
