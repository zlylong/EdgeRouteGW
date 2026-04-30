package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureSingleGeodataHealthy_RecoversFromXrayCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.dat")
	dst := filepath.Join(dir, "dst.dat")

	big := make([]byte, minHealthyGeodataSize+128)
	for i := range big {
		big[i] = 'A'
	}
	if err := os.WriteFile(src, big, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("bad"), 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	if err := ensureSingleGeodataHealthy("geoip.dat", dst, src); err != nil {
		t.Fatalf("ensureSingleGeodataHealthy failed: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Size() < minHealthyGeodataSize {
		t.Fatalf("dst not recovered, size=%d", info.Size())
	}
}

func TestEnsureSingleGeodataHealthy_NoopWhenHealthy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.dat")
	dst := filepath.Join(dir, "dst.dat")

	healthy := make([]byte, minHealthyGeodataSize+256)
	for i := range healthy {
		healthy[i] = 'B'
	}
	if err := os.WriteFile(src, healthy, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(dst, healthy, 0o644); err != nil {
		t.Fatalf("write dst: %v", err)
	}

	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	if err := ensureSingleGeodataHealthy("geosite.dat", dst, src); err != nil {
		t.Fatalf("ensureSingleGeodataHealthy failed: %v", err)
	}
	after, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("dst changed unexpectedly")
	}
}
