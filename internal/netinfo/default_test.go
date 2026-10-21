package netinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultIfaceFrom(t *testing.T) {
	p := filepath.Join(t.TempDir(), "route")
	os.WriteFile(p, []byte("Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\neth0\t0064A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\nwan0\t00000000\t0164A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"), 0o644)
	if got, err := defaultIfaceFrom(p); err != nil || got != "wan0" {
		t.Fatalf("got %q %v", got, err)
	}
	os.WriteFile(p, []byte("Iface\tDestination\n"), 0o644)
	if _, err := defaultIfaceFrom(p); err == nil {
		t.Fatal("expected error without default route")
	}
}
