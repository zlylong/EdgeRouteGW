package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestAESCryptoRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	enc := encryptAESWithKey("topsecret", key)
	if enc == "topsecret" || len(enc) < 4 || enc[:4] != "ENC:" {
		t.Fatalf("encrypt did not produce an ENC: payload: %q", enc)
	}
	if got, ok := decryptAESCore(enc, key); !ok || got != "topsecret" {
		t.Fatalf("round-trip failed: ok=%v got=%q", ok, got)
	}
	if _, ok := decryptAESCore(enc, []byte("ffffffffffffffffffffffffffffffff")); ok {
		t.Fatal("decrypt with wrong key leaked plaintext")
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
	db = tdb
	aesKey = []byte("abcdef0123456789abcdef0123456789")
	return tdb, func() { tdb.Close(); db = oldDB; aesKey = oldKey; migrateLegacyCredentials = oldMigrate }
}

func TestMigrateLegacyCredentialsIfNeeded(t *testing.T) {
	_, restore := newTestDB(t)
	defer restore()

	if _, err := db.Exec(`INSERT INTO remote_nodes (ssh_credential) VALUES (?)`,
		encryptAESWithKey("legacy-secret", legacyAESKey)); err != nil {
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
	if !strings.HasPrefix(stored, "ENC:") {
		t.Fatalf("unexpected stored value: %q", stored)
	}
	if stored != current {
		t.Fatalf("already-current row was rewritten: %q", stored)
	}
}
