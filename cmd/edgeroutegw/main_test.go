package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

func TestVersionCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, &out, &errb, noEnv); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.HasPrefix(out.String(), "edgeroutegw ") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errb, noEnv); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("stderr: %q", errb.String())
	}
}

func TestMigrateCommand(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"migrate", "-data-dir", dir}, &out, &errb, noEnv); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "applied 0001_init") {
		t.Fatalf("stdout: %q", out.String())
	}
	out.Reset()
	if code := run([]string{"migrate", "-data-dir", dir}, &out, &errb, noEnv); code != 0 {
		t.Fatalf("second exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Fatalf("second stdout: %q", out.String())
	}
	if _, err := filepath.Glob(filepath.Join(dir, "edgeroutegw.db")); err != nil {
		t.Fatal(err)
	}
}

func TestBadFlagExitCode(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"serve", "-log-level", "loud"}, &out, &errb, noEnv); code != 2 {
		t.Fatalf("exit %d, want 2; stderr %s", code, errb.String())
	}
}
