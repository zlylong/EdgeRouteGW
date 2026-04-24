package main

import (
	"strings"
	"testing"
)

func TestRenderMosdnsConfigIncludesProxyDomainAndLazyCache(t *testing.T) {
	cfg := renderMosdnsConfig("223.5.5.5", "8.8.8.8", true, "A")
	if !strings.Contains(cfg, "proxy_domains.txt") {
		t.Fatal("expected proxy_domains.txt in config")
	}
	if !strings.Contains(cfg, "tag: lazy_cache") {
		t.Fatal("expected lazy_cache block when lazy=true")
	}
}

func TestRenderMosdnsConfigNoLazyCacheWhenDisabled(t *testing.T) {
	cfg := renderMosdnsConfig("223.5.5.5", "8.8.8.8", false, "A")
	if strings.Contains(cfg, "tag: lazy_cache") {
		t.Fatal("did not expect lazy_cache block when lazy=false")
	}
}

func TestBuildBaseXrayConfigHasRequiredSections(t *testing.T) {
	cfg := buildBaseXrayConfig("A")
	if _, ok := cfg["inbounds"]; !ok {
		t.Fatal("missing inbounds")
	}
	if _, ok := cfg["outbounds"]; !ok {
		t.Fatal("missing outbounds")
	}
	if _, ok := cfg["routing"]; !ok {
		t.Fatal("missing routing")
	}
}

func TestBuildBaseXrayConfigSniffingIncludesQUIC(t *testing.T) {
	cfg := buildBaseXrayConfig("A")
	inbounds, ok := cfg["inbounds"].([]map[string]interface{})
	if !ok || len(inbounds) == 0 {
		t.Fatal("missing inbounds")
	}
	sniffing, ok := inbounds[0]["sniffing"].(map[string]interface{})
	if !ok {
		t.Fatal("missing sniffing on tproxy inbound")
	}
	dest, ok := sniffing["destOverride"].([]string)
	if !ok {
		t.Fatalf("destOverride type mismatch: %T", sniffing["destOverride"])
	}
	hasQUIC := false
	for _, v := range dest {
		if v == "quic" {
			hasQUIC = true
			break
		}
	}
	if !hasQUIC {
		t.Fatalf("expected quic in destOverride, got %v", dest)
	}
}

func TestBuildBaseXrayConfigModeBIncludesQUICAndFakeDNSSniffing(t *testing.T) {
	cfg := buildBaseXrayConfig("B")
	inbounds, ok := cfg["inbounds"].([]map[string]interface{})
	if !ok || len(inbounds) == 0 {
		t.Fatal("missing inbounds")
	}
	sniffing, ok := inbounds[0]["sniffing"].(map[string]interface{})
	if !ok {
		t.Fatal("missing sniffing on tproxy inbound")
	}
	dest, ok := sniffing["destOverride"].([]string)
	if !ok {
		t.Fatalf("destOverride type mismatch: %T", sniffing["destOverride"])
	}
	want := map[string]bool{"http": true, "tls": true, "quic": true, "fakedns": true}
	for _, v := range dest {
		delete(want, v)
	}
	if len(want) != 0 {
		t.Fatalf("missing sniffing overrides: %v (got %v)", want, dest)
	}
}
