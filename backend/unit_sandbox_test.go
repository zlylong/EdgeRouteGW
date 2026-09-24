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

// ProtectSystem=strict mounts /run read-only. The backend creates runtimeDir
// for the Xray logs the connection tracker tails, so the unit has to ask
// systemd for that directory (RuntimeDirectory=) and keep it across backend
// restarts (RuntimeDirectoryPreserve=yes), otherwise a restart of proxygw
// deletes the log file out from under a running Xray.
func TestUnitDeclaresTheRuntimeDirectoryTheBackendWrites(t *testing.T) {
	want := strings.TrimPrefix(filepath.Clean(runtimeDir), "/run/")
	if want == "" || strings.Contains(want, "/") {
		t.Fatalf("runtimeDir %q is not a direct child of /run; RuntimeDirectory= cannot express it", runtimeDir)
	}
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
		if !strings.Contains(body, "RuntimeDirectory="+want+"\n") {
			t.Errorf("%s lacks RuntimeDirectory=%s; os.MkdirAll(%q) fails under ProtectSystem=strict", f, want, runtimeDir)
		}
		if !strings.Contains(body, "RuntimeDirectoryPreserve=yes\n") {
			t.Errorf("%s lacks RuntimeDirectoryPreserve=yes; a backend restart would remove Xray's open log", f)
		}
	}
}
