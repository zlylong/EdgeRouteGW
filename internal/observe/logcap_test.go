package observe

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestLogCapTruncatesOversizedFilesOnly(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "xray_access.log")
	small := filepath.Join(dir, "mosdns.log")
	missing := filepath.Join(dir, "absent.log")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(small, []byte("fine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An appender keeps writing after the truncation, like Xray does.
	f, err := os.OpenFile(big, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c := NewLogCap(slog.New(slog.NewTextHandler(io.Discard, nil)), big, small, missing)
	c.MaxBytes = 1024
	if n := c.Check(); n != 1 {
		t.Fatalf("truncated %d files, want 1", n)
	}
	if _, err := f.WriteString("after\n"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(big)
	if string(b) != "after\n" {
		t.Fatalf("append after truncate: %q", b)
	}
	if b, _ := os.ReadFile(small); string(b) != "fine\n" {
		t.Fatalf("small file touched: %q", b)
	}
	if n := c.Check(); n != 0 {
		t.Fatalf("second pass truncated %d", n)
	}
}
