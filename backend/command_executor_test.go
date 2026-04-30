package main

import "testing"

func TestRedactSensitiveCommandArgs_KeyValueAndSeparateArg(t *testing.T) {
	in := []string{"--token=abc123", "--password", "p@ss", "--normal", "ok", "api_key=xyz"}
	out := redactSensitiveCommandArgs(in)

	if out[0] != "--token=[REDACTED]" {
		t.Fatalf("expected token redacted, got %q", out[0])
	}
	if out[2] != "[REDACTED]" {
		t.Fatalf("expected password value redacted, got %q", out[2])
	}
	if out[3] != "--normal" || out[4] != "ok" {
		t.Fatalf("expected normal args unchanged, got %q %q", out[3], out[4])
	}
	if out[5] != "api_key=[REDACTED]" {
		t.Fatalf("expected api_key redacted, got %q", out[5])
	}
}

func TestRedactSensitiveCommandArgs_AuthorizationHeader(t *testing.T) {
	in := []string{"-H", "Authorization: Bearer abc.def", "-H", "X-Test: 1"}
	out := redactSensitiveCommandArgs(in)

	if out[1] != "Authorization: [REDACTED]" {
		t.Fatalf("expected authorization header redacted, got %q", out[1])
	}
	if out[3] != "X-Test: 1" {
		t.Fatalf("expected non-sensitive header unchanged, got %q", out[3])
	}
}
