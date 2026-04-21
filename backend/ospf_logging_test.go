package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeOspfRoute(t *testing.T) {
	if got := normalizeOspfRoute("1.1.1.1"); got != "1.1.1.1/32" {
		t.Fatalf("want /32 appended, got %s", got)
	}
	if got := normalizeOspfRoute("198.18.0.0/16"); got != "198.18.0.0/16" {
		t.Fatalf("cidr should stay unchanged, got %s", got)
	}
}

func TestAddAdaptiveOspfBatchLogsSmallBatchKeepsPerIPLogs(t *testing.T) {
	oldLogs := append([]string(nil), ospfLogs...)
	ospfLogs = nil
	defer func() { ospfLogs = oldLogs }()

	addAdaptiveOspfBatchLogs("ADD", []string{"1.1.1.1", "2.2.2.2"}, "to published_set")

	logs := getOspfLogsSnapshot()
	if len(logs) != 2 {
		t.Fatalf("want 2 log lines got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "[ADD] 2.2.2.2 to published_set") {
		t.Fatalf("unexpected newest log: %s", logs[0])
	}
	if !strings.Contains(logs[1], "[ADD] 1.1.1.1 to published_set") {
		t.Fatalf("unexpected older log: %s", logs[1])
	}
}

func TestAddAdaptiveOspfBatchLogsLargeBatchUsesSummary(t *testing.T) {
	oldLogs := append([]string(nil), ospfLogs...)
	ospfLogs = nil
	defer func() { ospfLogs = oldLogs }()

	ips := []string{
		"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5",
		"10.0.0.6", "10.0.0.7", "10.0.0.8", "10.0.0.9", "10.0.0.10",
	}
	addAdaptiveOspfBatchLogs("DEL", ips, "(Miss count >= 3)")

	logs := getOspfLogsSnapshot()
	if len(logs) != 1 {
		t.Fatalf("want summary-only log got %d: %v", len(logs), logs)
	}
	line := logs[0]
	for _, want := range []string{
		"[DEL] batch=10",
		"sample_head=[10.0.0.1, 10.0.0.2, 10.0.0.3, 10.0.0.4]",
		"sample_tail=[10.0.0.7, 10.0.0.8, 10.0.0.9, 10.0.0.10]",
		"suppressed=2",
		"(Miss count >= 3)",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("expected %q in %s", want, line)
		}
	}
}

func TestApplyVtyshBatchLogsSuccess(t *testing.T) {
	oldLogs := append([]string(nil), ospfLogs...)
	ospfLogs = nil
	defer func() { ospfLogs = oldLogs }()

	tmpDir := t.TempDir()
	vtyshPath := filepath.Join(tmpDir, "vtysh")
	if err := os.WriteFile(vtyshPath, []byte("#!/bin/sh\ncat \"$2\" >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath)

	buf := bytes.NewBufferString("conf t\nip route 1.1.1.1/32 127.0.0.1 tag 100\n")
	if err := applyVtyshBatch("ADD", filepath.Join(tmpDir, "batch.conf"), buf, 1); err != nil {
		t.Fatalf("applyVtyshBatch failed: %v", err)
	}

	logs := getOspfLogsSnapshot()
	if len(logs) != 1 || !strings.Contains(logs[0], "[FRR] ADD batch=1 applied via vtysh") {
		t.Fatalf("unexpected logs: %v", logs)
	}
}

func TestApplyVtyshBatchLogsFailureOutput(t *testing.T) {
	oldLogs := append([]string(nil), ospfLogs...)
	ospfLogs = nil
	defer func() { ospfLogs = oldLogs }()

	tmpDir := t.TempDir()
	vtyshPath := filepath.Join(tmpDir, "vtysh")
	if err := os.WriteFile(vtyshPath, []byte("#!/bin/sh\necho boom >&2\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath)

	buf := bytes.NewBufferString("conf t\n")
	if err := applyVtyshBatch("DEL", filepath.Join(tmpDir, "batch.conf"), buf, 3); err == nil {
		t.Fatal("expected applyVtyshBatch to fail")
	}

	logs := getOspfLogsSnapshot()
	if len(logs) != 1 {
		t.Fatalf("unexpected logs: %v", logs)
	}
	line := logs[0]
	if !strings.Contains(line, "[FRR] DEL batch=3 apply_failed") || !strings.Contains(line, "boom") {
		t.Fatalf("unexpected failure log: %s", line)
	}
}
