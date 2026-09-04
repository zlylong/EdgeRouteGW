package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unit runs with ProtectSystem=strict, so /etc is read-only apart from the
// paths ReadWritePaths bind-mounts. A ReadWritePaths entry cannot make a file
// that does not exist yet writable — the "-" prefix makes systemd skip it — so
// putting scratch files under /etc left every apply failing with "read-only
// file system", and the gateway silently stopped being able to update its own
// firewall. These paths must stay somewhere the unit can actually create files.
func TestNftablesScratchPathsAreWritableUnderSandbox(t *testing.T) {
	tmp := os.TempDir()
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"staging", nftablesStagingPath()},
		{"runtime backup", nftablesRuntimeBackupPath()},
	} {
		if strings.HasPrefix(tc.got, "/etc/") {
			t.Fatalf("%s path %q is under /etc, which ProtectSystem=strict makes read-only", tc.name, tc.got)
		}
		if filepath.Dir(tc.got) != filepath.Clean(tmp) {
			t.Fatalf("%s path %q is not under TempDir %q", tc.name, tc.got, tmp)
		}
	}

	if nftablesStagingPath() == nftablesRuntimeBackupPath() {
		t.Fatal("staging and runtime backup must not collide: the deferred cleanup of one would delete the other")
	}
}

// The active ruleset is the one file in /etc the unit legitimately rewrites; it
// ships with every install, so the bind-mount applies.
func TestNftablesActivePathIsTheSystemdLoadedRuleset(t *testing.T) {
	if nftablesActivePath != "/etc/nftables.conf" {
		t.Fatalf("active path %q is not the ruleset nftables.service loads at boot", nftablesActivePath)
	}
}

// A scratch path that is actually creatable is the whole point, so prove it
// rather than trusting the prefix check above.
func TestNftablesStagingPathIsCreatable(t *testing.T) {
	p := nftablesStagingPath()
	if err := os.WriteFile(p, []byte("# probe\n"), 0644); err != nil {
		t.Fatalf("cannot create staging file %q: %v", p, err)
	}
	t.Cleanup(func() { os.Remove(p) })
}
