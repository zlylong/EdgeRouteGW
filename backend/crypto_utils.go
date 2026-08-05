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
// persisted or used as the long-term key; it is only used once to migrate
// existing rows to a freshly generated key and then discarded. Keeping it as a
// constant also lets us detect installs that still hold it in aes.key.
var legacyAESKey = []byte("proxygw-secret-key-32-bytes-long")

// migrateLegacyCredentials indicates some remote_nodes rows may still be
// encrypted with legacyAESKey and must be re-encrypted with the current aesKey.
var migrateLegacyCredentials bool

func init() {
	keyPath := getPath("config", "aes.key")
	stored, readErr := os.ReadFile(keyPath)
	if readErr == nil && len(stored) == 32 && !bytes.Equal(stored, legacyAESKey) {
		aesKey = stored
		return
	}

	// No key file, or it still contains the well-known legacy constant: always
	// rotate to a fresh random key. A hardcoded public value must never become
	// the persisted encryption key, otherwise every install would share the same
	// key and SSH credentials would be trivially decryptable from source.
	aesKey = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
		log.Printf("[CRITICAL] failed to generate AES key: %v", err)
		aesKey = append([]byte(nil), legacyAESKey...)
	}

	// Only schedule a migration when an existing database may hold rows that were
	// encrypted with the legacy key: either the key file was missing while a DB
	// is present, or the key file itself still contained the legacy constant.
	var dbPresent bool
	if _, err := os.Stat(getPath("config", "proxygw.db")); err == nil {
		dbPresent = true
	}
	migrateLegacyCredentials = dbPresent || (readErr == nil && bytes.Equal(stored, legacyAESKey))

	if err := os.WriteFile(keyPath, aesKey, 0600); err != nil {
		log.Printf("[SECURITY] failed to persist AES key: %v", err)
	}
}

// migrateLegacyCredentialsIfNeeded re-encrypts any SSH credentials that were
// stored with the legacy hardcoded key using the current random aesKey. It is
// a no-op unless init() detected a legacy-encrypted database.
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
		plain := decryptAESWithKey(cred, legacyAESKey)
		if plain == cred {
			// Cannot be decoded with the legacy key; leave the row untouched.
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
	if len(updates) == 0 {
		return
	}

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
	log.Printf("[SECURITY] rotated %d legacy SSH credentials to a fresh AES key", len(updates))
}

func EncryptAES(text string) string { return encryptAESWithKey(text, aesKey) }

func DecryptAES(text string) string { return decryptAESWithKey(text, aesKey) }

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

func decryptAESWithKey(text string, key []byte) string {
	if len(text) < 4 || text[:4] != "ENC:" {
		return text
	}
	text = text[4:]
	ciphertext, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(ciphertext) < aes.BlockSize {
		return text
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return text
	}
	iv := ciphertext[:aes.BlockSize]
	ciphertext = ciphertext[aes.BlockSize:]
	cfb := cipher.NewCFBDecrypter(block, iv)
	cfb.XORKeyStream(ciphertext, ciphertext)
	data, err := base64.StdEncoding.DecodeString(string(ciphertext))
	if err != nil {
		return text
	}
	return string(data)
}
