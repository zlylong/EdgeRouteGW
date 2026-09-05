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
			// Falling back to the well-known legacy constant would encrypt
			// every SSH credential written this session with a key that is in
			// the public source tree, and persist it below. Refusing to start is
			// the only safe answer to a CSPRNG failure.
			log.Fatalf("[CRITICAL] failed to generate AES key, refusing to start: %v", err)
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

// migrateLegacyCredentialsIfNeeded rewrites stored SSH credentials into the
// current format under the current key. It runs at every boot: the scan is one
// query, and a row is rewritten only if it is decryptable but not already in
// the current format under the current key — i.e. it is either still under the
// legacy hardcoded key (a rotation was pending) or still in the unauthenticated
// CFB format. Rows that decrypt with neither key are left untouched rather
// than corrupted. The rotation-pending marker is cleared only once every row
// has been committed, so an interrupted rotation is retried next boot.
func migrateLegacyCredentialsIfNeeded() {
	rotationPending := migrateLegacyCredentials
	migrateLegacyCredentials = false

	rows, err := getDB().Query("SELECT id, ssh_credential FROM remote_nodes")
	if err != nil {
		log.Printf("[SECURITY] migrate credentials: query failed: %v", err)
		return
	}
	type update struct {
		id  int64
		enc string
	}
	var updates []update
	undecryptable := 0
	for rows.Next() {
		var id int64
		var cred string
		if err := rows.Scan(&id, &cred); err != nil {
			continue
		}
		if !isEncryptedCredential(cred) {
			continue
		}
		// Already current: GCM under the current key.
		if strings.HasPrefix(cred, gcmPrefix) {
			if _, ok := decryptAESCore(cred, aesKey); ok {
				continue
			}
		}
		plain, ok := decryptAESCore(cred, aesKey)
		if !ok {
			plain, ok = decryptAESCore(cred, legacyAESKey)
		}
		if !ok {
			// Not decryptable with either key: preserve the value verbatim
			// instead of corrupting it, but count it — a whole table of these
			// means the per-install key was replaced out from under the data.
			undecryptable++
			continue
		}
		if enc := encryptAESWithKey(plain, aesKey); enc != cred {
			updates = append(updates, update{id: id, enc: enc})
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[SECURITY] migrate credentials: rows error: %v", err)
		rows.Close()
		return
	}
	rows.Close()

	if len(updates) > 0 {
		tx, err := getDB().Begin()
		if err != nil {
			log.Printf("[SECURITY] migrate credentials: begin tx failed: %v", err)
			return
		}
		for _, u := range updates {
			if _, err := tx.Exec("UPDATE remote_nodes SET ssh_credential=? WHERE id=?", u.enc, u.id); err != nil {
				_ = tx.Rollback()
				log.Printf("[SECURITY] migrate credentials: update failed: %v", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[SECURITY] migrate credentials: commit failed: %v", err)
			return
		}
		log.Printf("[SECURITY] re-encrypted %d stored credential(s) into the current format", len(updates))
	}

	if rotationPending {
		// Only clear the pending marker after the rotation fully committed, so
		// an interrupted migration is retried on the next boot.
		_ = os.Remove(rotationPendingPath())
		log.Printf("[SECURITY] key rotation completed")
	}

	if undecryptable > 0 {
		// Silent preservation is right for the data; it is wrong for the
		// operator, who would otherwise first learn of this when a deploy
		// fails to authenticate. The usual cause is config/aes.key being
		// replaced — historically by "git reset --hard" in update.sh, back
		// when the file was tracked.
		log.Printf("[CRITICAL] %d stored SSH credential(s) cannot be decrypted with the current or legacy key and were left untouched; the per-install key in config/aes.key was most likely replaced (an older update.sh ran git reset --hard over it). Those nodes need their SSH credentials re-entered.", undecryptable)
	}
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

const (
	// cfbPrefix marks the original format: AES-256-CFB with a random IV over a
	// base64-encoded plaintext. CFB is unauthenticated, so a stored value can
	// be altered without the key and still decrypt to something. It is read
	// for compatibility and rewritten at startup; nothing writes it any more.
	cfbPrefix = "ENC:"
	// gcmPrefix marks the current format: AES-256-GCM, nonce || ciphertext ||
	// tag, base64-encoded. Any modification fails authentication.
	gcmPrefix = "ENC2:"
)

// isEncryptedCredential reports whether s carries either on-disk format.
func isEncryptedCredential(s string) bool {
	return strings.HasPrefix(s, gcmPrefix) || strings.HasPrefix(s, cfbPrefix)
}

func encryptAESWithKey(text string, key []byte) string {
	block, err := aes.NewCipher(key)
	if err != nil {
		return text
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return text
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return text
	}
	sealed := gcm.Seal(nil, nonce, []byte(text), nil)
	return gcmPrefix + base64.StdEncoding.EncodeToString(append(nonce, sealed...))
}

// decryptAESCore decrypts text and reports whether decryption succeeded. Unlike
// a bare "return input on failure", callers can rely on the bool to distinguish
// a valid plaintext from a non-decryptable value. Both on-disk formats are
// accepted; only the GCM one is ever produced.
func decryptAESCore(text string, key []byte) (string, bool) {
	switch {
	case strings.HasPrefix(text, gcmPrefix):
		return decryptGCM(text[len(gcmPrefix):], key)
	case strings.HasPrefix(text, cfbPrefix):
		return decryptCFBLegacy(text[len(cfbPrefix):], key)
	}
	return "", false
}

func decryptGCM(payload string, key []byte) (string, bool) {
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", false
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", false
	}
	if len(data) < gcm.NonceSize() {
		return "", false
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", false
	}
	return string(plain), true
}

// decryptCFBLegacy reads the pre-GCM format. The inner base64 layer was the
// old code's only integrity check: a wrong key or a flipped byte usually
// produces something that no longer base64-decodes.
func decryptCFBLegacy(payload string, key []byte) (string, bool) {
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
	//nolint:staticcheck // SA1019: CFB is retained read-only to migrate stored rows.
	cfb := cipher.NewCFBDecrypter(block, iv)
	cfb.XORKeyStream(ciphertext, ciphertext)
	plain, err := base64.StdEncoding.DecodeString(string(ciphertext))
	if err != nil {
		return "", false
	}
	return string(plain), true
}
