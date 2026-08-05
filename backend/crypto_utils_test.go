package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestAESCryptoRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	enc := encryptAESWithKey("topsecret", key)
	if enc == "topsecret" || len(enc) < 4 || enc[:4] != "ENC:" {
		t.Fatalf("encrypt did not produce an ENC: payload: %q", enc)
	}
	if dec := decryptAESWithKey(enc, key); dec != "topsecret" {
		t.Fatalf("round-trip failed: got %q", dec)
	}
	if d := decryptAESWithKey(enc, []byte("ffffffffffffffffffffffffffffffff")); d == "topsecret" {
		t.Fatal("decrypt with wrong key leaked plaintext")
	}
}

func TestMigrateLegacyCredentialsIfNeeded(t *testing.T) {
	dir := t.TempDir()
	tdb, err := sql.Open("sqlite3", filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	if _, err := tdb.Exec(`CREATE TABLE remote_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, ssh_credential TEXT)`); err != nil {
		t.Fatal(err)
	}

	oldDB := db
	oldKey := aesKey
	oldMigrate := migrateLegacyCredentials
	db = tdb
	newKey := []byte("abcdef0123456789abcdef0123456789")
	aesKey = newKey
	defer func() { db = oldDB; aesKey = oldKey; migrateLegacyCredentials = oldMigrate }()

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
	if got := decryptAESWithKey(enc, aesKey); got != "legacy-secret" {
		t.Fatalf("migrated credential not decryptable with new key: got %q", got)
	}
	// The new key must differ from the legacy key, so the credential is no
	// longer decryptable with the well-known constant.
	if got := decryptAESWithKey(enc, legacyAESKey); got == "legacy-secret" {
		t.Fatal("migrated credential is still decryptable with the legacy key")
	}

	var plain string
	if err := db.QueryRow(`SELECT ssh_credential FROM remote_nodes WHERE id=2`).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != "plain-legacy" {
		t.Fatalf("plaintext non-ENC legacy value should be untouched, got %q", plain)
	}
}
