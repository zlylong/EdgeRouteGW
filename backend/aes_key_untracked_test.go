package main

import (
	"os"
	"strings"
	"testing"
)

// config/aes.key is written by init() on first boot and is the only copy of
// the key every stored SSH credential is encrypted with. While it was a
// tracked file, update.sh's "git reset --hard origin/main" put the committed
// placeholder back over it on every update, and every credential became
// undecryptable on the next start.
func TestAESKeyIsNeitherTrackedNorResettable(t *testing.T) {
	ig, err := os.ReadFile("../.gitignore")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/config/aes.key", "/config/aes.rotation_pending"} {
		if !strings.Contains(string(ig), want+"\n") {
			t.Errorf(".gitignore does not list %s; a checkout would track a per-install secret again", want)
		}
	}

	up, err := os.ReadFile("../scripts/update.sh")
	if err != nil {
		t.Fatal(err)
	}
	s := string(up)
	reset := strings.Index(s, "git reset --hard origin/main")
	backup := strings.Index(s, `cp -p "$REPO_DIR/config/aes.key" "$AES_KEY_BACKUP"`)
	restore := strings.Index(s, `cp -p "$AES_KEY_BACKUP" "$REPO_DIR/config/aes.key"`)
	if reset < 0 || backup < 0 || restore < 0 {
		t.Fatalf("update.sh must back up config/aes.key before git reset --hard and restore it after (reset=%d backup=%d restore=%d)", reset, backup, restore)
	}
	if !(backup < reset && reset < restore) {
		t.Fatalf("update.sh key backup/restore is not wrapped around git reset --hard (backup=%d reset=%d restore=%d)", backup, reset, restore)
	}
}

// Preserving an undecryptable row is right for the data and wrong for the
// operator, who would otherwise learn about it when a deploy fails to
// authenticate. It has to be said, once, with the cause and the remedy.
func TestMigrateReportsUndecryptableCredentialsLoudly(t *testing.T) {
	_, restore := newTestDB(t)
	defer restore()

	foreign := encryptAESWithKey("lost-key-secret", []byte("22222222222222222222222222222222"))
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(`INSERT INTO remote_nodes (ssh_credential) VALUES (?)`, foreign); err != nil {
			t.Fatal(err)
		}
	}
	out := captureLog(t, migrateLegacyCredentialsIfNeeded)
	for _, want := range []string{"[CRITICAL]", "2 stored SSH credential(s)", "config/aes.key", "re-entered"} {
		if !strings.Contains(out, want) {
			t.Fatalf("migration did not report the undecryptable rows (missing %q):\n%s", want, out)
		}
	}
	var stored string
	if err := db.QueryRow(`SELECT ssh_credential FROM remote_nodes WHERE id=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != foreign {
		t.Fatalf("undecryptable row was rewritten: %q", stored)
	}
}
