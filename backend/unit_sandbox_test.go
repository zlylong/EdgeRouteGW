package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// disableSendRedirects writes into /proc/sys/net/ipv4/conf, but the unit sets
// ProtectKernelTunables=yes, which mounts /proc/sys read-only. Without an
// explicit ReadWritePaths entry every write fails with "read-only file system"
// and the gateway keeps emitting the ICMP redirects that let clients bypass
// Mode A — the install-time loop only fixes it once, and interfaces are
// recreated on every boot.
func TestUnitGrantsWriteAccessToTheSysctlTreeTheBackendWrites(t *testing.T) {
	for _, f := range []string{
		"../systemd/proxygw.service",
		"../scripts/install.sh",
		"../scripts/update.sh",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		body := string(b)

		var rwp string
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "ReadWritePaths=") {
				rwp = line
				break
			}
		}
		if rwp == "" {
			t.Fatalf("%s has no ReadWritePaths line", f)
		}
		if !strings.Contains(rwp, ipv4ConfDir) {
			t.Errorf("%s does not grant %s:\n  %s\ndisableSendRedirects would fail read-only and Mode A stays bypassable", f, ipv4ConfDir, rwp)
		}
	}
}

// The path the unit grants and the path the code writes have to be the same
// one; a rename on either side would silently re-break it.
func TestSysctlWritePathMatchesWhatTheCodeUses(t *testing.T) {
	if filepath.Clean(ipv4ConfDir) != "/proc/sys/net/ipv4/conf" {
		t.Fatalf("ipv4ConfDir is %q; update the unit's ReadWritePaths to match", ipv4ConfDir)
	}
}
