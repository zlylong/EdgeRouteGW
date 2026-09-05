package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"io"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// encryptCFBLegacyForTest reproduces the pre-GCM writer so tests can seed the
// exact bytes an upgraded install still has on disk. Production no longer
// contains a CFB encryptor.
func encryptCFBLegacyForTest(t *testing.T, text string, key []byte) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	b := base64.StdEncoding.EncodeToString([]byte(text))
	ciphertext := make([]byte, aes.BlockSize+len(b))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		t.Fatal(err)
	}
	//nolint:staticcheck // deliberately the legacy mode
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(ciphertext[aes.BlockSize:], []byte(b))
	return cfbPrefix + base64.StdEncoding.EncodeToString(ciphertext)
}

func TestAESCryptoRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	enc := encryptAESWithKey("topsecret", key)
	if enc == "topsecret" || !strings.HasPrefix(enc, gcmPrefix) {
		t.Fatalf("encrypt did not produce an %s payload: %q", gcmPrefix, enc)
	}
	if got, ok := decryptAESCore(enc, key); !ok || got != "topsecret" {
		t.Fatalf("round-trip failed: ok=%v got=%q", ok, got)
	}
	if _, ok := decryptAESCore(enc, []byte("ffffffffffffffffffffffffffffffff")); ok {
		t.Fatal("decrypt with wrong key leaked plaintext")
	}
	// Two encryptions of the same value must not be equal: a fresh nonce each time.
	if encryptAESWithKey("topsecret", key) == enc {
		t.Fatal("nonce reuse: identical ciphertext for identical plaintext")
	}
}

// CFB would decrypt a modified ciphertext into modified plaintext with no
// error, and DecryptAES has to hand whatever comes out to ssh.ParsePrivateKey
// or a password prompt. GCM refuses instead.
func TestGCMRejectsTamperedCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	enc := encryptAESWithKey("-----BEGIN OPENSSH PRIVATE KEY-----", key)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, gcmPrefix))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01 // flip a tag bit
	tampered := gcmPrefix + base64.StdEncoding.EncodeToString(raw)
	if got, ok := decryptAESCore(tampered, key); ok {
		t.Fatalf("tampered ciphertext was accepted: %q", got)
	}
	oldKey := aesKey
	aesKey = key
	defer func() { aesKey = oldKey }()
	if got := DecryptAES(tampered); got != tampered {
		t.Fatalf("DecryptAES must return the input unchanged on failure, got %q", got)
	}
}

// Rows written before this change are CFB; they must still read.
func TestLegacyCFBRowsStillDecrypt(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	legacy := encryptCFBLegacyForTest(t, "old-format-secret", key)
	if got, ok := decryptAESCore(legacy, key); !ok || got != "old-format-secret" {
		t.Fatalf("legacy CFB row unreadable: ok=%v got=%q", ok, got)
	}
	if _, ok := decryptAESCore(legacy, []byte("ffffffffffffffffffffffffffffffff")); ok {
		t.Fatal("legacy row decrypted with the wrong key")
	}
}

// A CFB row under the *current* key is not a key-rotation case, but it is
// still unauthenticated on disk. Startup rewrites it even when no rotation
// is pending.
func TestMigrateRewritesCFBRowsUnderTheCurrentKey(t *testing.T) {
	_, restore := newTestDB(t)
	defer restore()

	cfb := encryptCFBLegacyForTest(t, "current-key-cfb", aesKey)
	if _, err := db.Exec(`INSERT INTO remote_nodes (ssh_credential) VALUES (?)`, cfb); err != nil {
		t.Fatal(err)
	}
	migrateLegacyCredentials = false
	migrateLegacyCredentialsIfNeeded()

	var stored string
	if err := db.QueryRow(`SELECT ssh_credential FROM remote_nodes WHERE id=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, gcmPrefix) {
		t.Fatalf("CFB row was not rewritten to GCM: %q", stored)
	}
	if got, ok := decryptAESCore(stored, aesKey); !ok || got != "current-key-cfb" {
		t.Fatalf("rewritten row unreadable: ok=%v got=%q", ok, got)
	}
}

func newTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	tdb, err := sql.Open("sqlite3", filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tdb.Exec(`CREATE TABLE remote_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, ssh_credential TEXT)`); err != nil {
		t.Fatal(err)
	}
	oldDB := db
	oldKey := aesKey
	oldMigrate := migrateLegacyCredentials
	setDB(tdb)
	aesKey = []byte("abcdef0123456789abcdef0123456789")
	return tdb, func() { tdb.Close(); setDB(oldDB); aesKey = oldKey; migrateLegacyCredentials = oldMigrate }
}

func TestMigrateLegacyCredentialsIfNeeded(t *testing.T) {
	_, restore := newTestDB(t)
	defer restore()

	// What a pre-rotation install actually has on disk: CFB under the
	// hardcoded key.
	if _, err := db.Exec(`INSERT INTO remote_nodes (ssh_credential) VALUES (?)`,
		encryptCFBLegacyForTest(t, "legacy-secret", legacyAESKey)); err != nil {
		t.Fatal(err)
	}
	// A plaintext (never-encrypted) value must be left untouched.
	if _, err := db.Exec(`INSERT INTO remote_nodes (ssh_credential) VALUES (?)`, "plain-legacy"); err != nil {
		t.Fatal(err)
	}

	migrateLegacyCredentials = true
	migrateLegacyCredentialsIfNeeded()

	var enc string
	if err := db.QueryRow(`SELECT ssh_credential FROM remote_nodes WHERE id=1`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if got, ok := decryptAESCore(enc, aesKey); !ok || got != "legacy-secret" {
		t.Fatalf("migrated credential not decryptable with new key: ok=%v got=%q", ok, got)
	}
	// The new key must differ from the legacy key, so the credential is no
	// longer decryptable with the well-known constant.
	if _, ok := decryptAESCore(enc, legacyAESKey); ok {
		t.Fatalf("migrated credential is still decryptable with the legacy key: %q", enc)
	}

	var plain string
	if err := db.QueryRow(`SELECT ssh_credential FROM remote_nodes WHERE id=2`).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != "plain-legacy" {
		t.Fatalf("plaintext non-ENC legacy value should be untouched, got %q", plain)
	}
}

// A row encrypted with a key that is neither the current nor the legacy key
// must be preserved verbatim, not re-encrypted into garbage (P2).
func TestMigrateLegacyLeavesForeignENCRowUntouched(t *testing.T) {
	_, restore := newTestDB(t)
	defer restore()

	foreignKey := []byte("11111111111111111111111111111111")
	foreign := encryptAESWithKey("secret-of-another-key", foreignKey)
	if _, err := db.Exec(`INSERT INTO remote_nodes (ssh_credential) VALUES (?)`, foreign); err != nil {
		t.Fatal(err)
	}

	migrateLegacyCredentials = true
	migrateLegacyCredentialsIfNeeded()

	var stored string
	if err := db.QueryRow(`SELECT ssh_credential FROM remote_nodes WHERE id=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != foreign {
		t.Fatalf("foreign ENC credential was rewritten: %q", stored)
	}
	// Ensure the foreign value is still recoverable with its own key.
	if got, ok := decryptAESCore(stored, foreignKey); !ok || got != "secret-of-another-key" {
		t.Fatalf("foreign credential corrupted: ok=%v got=%q", ok, got)
	}
}

// Rows already encrypted with the current key must be skipped so a retried
// migration is idempotent (P1).
func TestMigrateLegacyIsIdempotent(t *testing.T) {
	_, restore := newTestDB(t)
	defer restore()

	current := encryptAESWithKey("already-current", aesKey)
	if _, err := db.Exec(`INSERT INTO remote_nodes (ssh_credential) VALUES (?)`, current); err != nil {
		t.Fatal(err)
	}

	migrateLegacyCredentials = true
	migrateLegacyCredentialsIfNeeded()

	var stored string
	if err := db.QueryRow(`SELECT ssh_credential FROM remote_nodes WHERE id=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, gcmPrefix) {
		t.Fatalf("unexpected stored value: %q", stored)
	}
	if stored != current {
		t.Fatalf("already-current row was rewritten: %q", stored)
	}
}
