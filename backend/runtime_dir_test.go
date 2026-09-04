package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Xray exits 23 when it cannot open its access log, and xray.service sets
// RestartPreventExitStatus=23, so a missing runtime directory does not produce
// a retry loop — it leaves the unit down until something restarts it by hand.
// On a fresh install that surfaced as the backend rejecting its own freshly
// generated config ("failed to initialize access logger"). Keeping every
// consumer derived from runtimeDir is what makes creating that one directory
// sufficient.
func TestXrayLogPathsLiveUnderRuntimeDir(t *testing.T) {
	for name, p := range map[string]string{
		"access log": xrayAccessLogPath,
		"error log":  xrayErrorLogPath,
	} {
		if filepath.Dir(p) != runtimeDir {
			t.Fatalf("%s %q is not under runtimeDir %q; creating runtimeDir at boot would not cover it", name, p, runtimeDir)
		}
	}
	if xrayAccessLogPath == xrayErrorLogPath {
		t.Fatal("access and error log must be distinct files")
	}
}

// The generated config is what Xray actually validates and loads, so the path
// it carries is the one that has to exist.
func TestGeneratedXrayConfigUsesTheRuntimeAccessLog(t *testing.T) {
	for _, mode := range []string{"A", "B", "C"} {
		cfg := buildBaseXrayConfig(mode)
		logSection, ok := cfg["log"].(map[string]string)
		if !ok {
			t.Fatalf("mode %s: config has no log section of the expected shape", mode)
		}
		got := logSection["access"]
		if got != xrayAccessLogPath {
			t.Fatalf("mode %s: config logs access to %q, but the backend only creates %q", mode, got, runtimeDir)
		}
		if !strings.HasPrefix(got, runtimeDir+"/") {
			t.Fatalf("mode %s: access log %q escapes runtimeDir %q", mode, got, runtimeDir)
		}
	}
}
