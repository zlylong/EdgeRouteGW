package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A gateway that emits ICMP redirects tells LAN clients to reach the internet
// through the main router directly. The client caches that and its traffic
// stops entering the TPROXY chain entirely, so Mode A is bypassed without any
// error anywhere. conf.all and conf.default do not prevent this on interfaces
// that already existed, so every interface has to be written explicitly.
func TestDisableSendRedirectsCoversEveryInterface(t *testing.T) {
	root := t.TempDir()
	ifaces := []string{"all", "default", "lo", "eth0", "br-lan"}
	for _, n := range ifaces {
		dir := filepath.Join(root, n)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "send_redirects"), []byte("1\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	old := ipv4ConfDir
	ipv4ConfDir = root
	t.Cleanup(func() { ipv4ConfDir = old })

	disableSendRedirects()

	for _, n := range ifaces {
		b, err := os.ReadFile(filepath.Join(root, n, "send_redirects"))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if got := string(b); got != "0\n" {
			t.Fatalf("send_redirects on %s is %q, want \"0\\n\"; a pre-existing interface left at 1 re-enables the redirect that bypasses the gateway", n, got)
		}
	}
}

// An unreadable sysctl tree must not take the gateway down with it.
func TestDisableSendRedirectsToleratesMissingTree(t *testing.T) {
	old := ipv4ConfDir
	ipv4ConfDir = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { ipv4ConfDir = old })
	disableSendRedirects() // must not panic
}
