package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
)

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	fn()
	return buf.String()
}

// A missing dig disables the whole OSPF resolution path, but the only symptom
// is one "executable file not found" line per rule per sync, which reads like a
// DNS problem with that rule. Say it once, plainly, with the fix.
func TestWarnIfResolverMissingNamesTheDependencyAndTheFix(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookPath = old })

	out := captureLog(t, warnIfResolverMissing)

	for _, want := range []string{resolverBinary, "Mode C", "bind9-dnsutils"} {
		if !strings.Contains(out, want) {
			t.Fatalf("warning does not mention %q:\n%s", want, out)
		}
	}
}

func TestWarnIfResolverMissingSilentWhenPresent(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/dig", nil }
	t.Cleanup(func() { lookPath = old })

	if out := captureLog(t, warnIfResolverMissing); strings.TrimSpace(out) != "" {
		t.Fatalf("warned even though the resolver is installed:\n%s", out)
	}
}

// The install path has to actually install it, or the warning is all a fresh
// deployment ever gets.
func TestInstallScriptsInstallTheResolver(t *testing.T) {
	for _, f := range []string{"../scripts/install.sh", "../scripts/update.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if !strings.Contains(string(b), "bind9-dnsutils") {
			t.Fatalf("%s never installs a dig provider; Mode C would publish nothing on a fresh host", f)
		}
	}
}
