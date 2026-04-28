package main

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func renderNftForMode(t *testing.T, mode string) string {
	t.Helper()
	tmpl, err := template.New("nftables").Parse(nftablesTmpl)
	if err != nil {
		t.Fatal(err)
	}
	data := struct {
		MacProxy      string
		MacDirect     string
		IPProxy       string
		IPDirect      string
		IP6Proxy      string
		IP6Direct     string
		ProtectedIPs  string
		DefaultPolicy string
		Mode          string
	}{DefaultPolicy: "proxy", Mode: mode}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestNftablesModeAIncludesQuicReject(t *testing.T) {
	cfg := renderNftForMode(t, "A")
	if !strings.Contains(cfg, "mode_a_quic_reject") {
		t.Fatalf("mode A nftables should include quic reject rule, got:\n%s", cfg)
	}
	if !strings.Contains(cfg, "meta l4proto udp th dport 443") {
		t.Fatalf("mode A nftables missing udp/443 match")
	}
	if !strings.Contains(cfg, "reject with icmpx type port-unreachable") {
		t.Fatalf("mode A nftables missing immediate reject action")
	}
}

func TestNftablesModeBNoQuicReject(t *testing.T) {
	cfg := renderNftForMode(t, "B")
	if strings.Contains(cfg, "mode_a_quic_reject") {
		t.Fatalf("mode B should not include mode_a_quic_reject")
	}
}

func TestNftablesOutputHostEgressDirect(t *testing.T) {
	cfg := renderNftForMode(t, "A")
	if !strings.Contains(cfg, "host_egress_direct") {
		t.Fatalf("output chain should keep host egress direct")
	}
	if strings.Contains(cfg, "meta l4proto { tcp, udp } mark set 1 accept") {
		t.Fatalf("output chain must not mark all host tcp/udp with fwmark 1")
	}
}
