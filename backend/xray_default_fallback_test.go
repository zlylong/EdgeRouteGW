package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func renderXrayConfigForFallbackTest(t *testing.T, mode, lanPolicy, failoverMode string, nodeActive bool) map[string]any {
	t.Helper()
	tdb, _ := setupTestDB(t)
	setDB(tdb)
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES ('mode',?)`, mode); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES ('lan_default_policy',?)`, lanPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES ('default_node_id','3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES ('node_failover_mode',?)`, failoverMode); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM rules; INSERT INTO rules(type, value, policy) VALUES ('geosite', 'anthropic', 'proxy-3')`); err != nil {
		t.Fatal(err)
	}
	activeVal := 0
	if nodeActive {
		activeVal = 1
	}
	if _, err := db.Exec(`INSERT INTO nodes(id, name, grp, type, address, port, uuid, params, active, ping) VALUES (3, 'n3', 'g', 'Vless', '1.1.1.1', 443, 'u3', '{}', ?, 20)`, activeVal); err != nil {
		t.Fatal(err)
	}

	tempRoot := t.TempDir()
	xrayDir := filepath.Join(tempRoot, "core", "xray")
	if err := os.MkdirAll(xrayDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/true", filepath.Join(xrayDir, "xray")); err != nil {
		t.Fatal(err)
	}
	oldHome := os.Getenv("PROXYGW_HOME")
	if err := os.Setenv("PROXYGW_HOME", tempRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("PROXYGW_HOME", oldHome)
	})

	if err := applyXrayConfigInternal(false); err != nil {
		t.Fatalf("applyXrayConfigInternal(false) error: %v", err)
	}

	payload, err := os.ReadFile(filepath.Join(xrayDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(payload, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestApplyXrayConfigModeADefaultFallbackDirect(t *testing.T) {
	cfg := renderXrayConfigForFallbackTest(t, "A", "proxy", "normal", true)
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["ruleTag"] != "default-fallback" {
		t.Fatalf("last ruleTag want default-fallback got %#v", last["ruleTag"])
	}
	if last["network"] != "tcp,udp" {
		t.Fatalf("last network want tcp,udp got %#v", last["network"])
	}
	if last["outboundTag"] != "direct" {
		t.Fatalf("mode A fallback outboundTag want direct got %#v", last["outboundTag"])
	}
	if _, ok := last["balancerTag"]; ok {
		t.Fatalf("mode A fallback should not keep balancerTag: %#v", last)
	}
}

func TestApplyXrayConfigModeBFallbackRespectsProxyPolicy(t *testing.T) {
	cfg := renderXrayConfigForFallbackTest(t, "B", "proxy", "normal", true)
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	last := rules[len(rules)-1].(map[string]any)
	if last["ruleTag"] != "default-fallback" {
		t.Fatalf("last ruleTag want default-fallback got %#v", last["ruleTag"])
	}
	if last["outboundTag"] != "proxy-3-out" {
		t.Fatalf("mode B fallback outboundTag want proxy-3-out got %#v", last["outboundTag"])
	}
}

func TestApplyXrayConfigStrictModeKeepsProxyWhenNodeInactive(t *testing.T) {
	cfg := renderXrayConfigForFallbackTest(t, "B", "proxy", "strict", false)
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	matched := false
	for _, raw := range rules {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		domainRaw, ok := r["domain"].([]any)
		if !ok || len(domainRaw) == 0 {
			continue
		}
		if domainRaw[0] == "geosite:anthropic" {
			matched = true
			if r["outboundTag"] != "proxy-3-out" {
				t.Fatalf("strict mode should keep proxy-3-out, got %#v", r["outboundTag"])
			}
		}
	}
	if !matched {
		t.Fatalf("expected geosite:anthropic rule in routing rules")
	}
}

func findRuleByTag(rules []any, tag string) (map[string]any, int) {
	for i, raw := range rules {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if rt, _ := r["ruleTag"].(string); rt == tag {
			return r, i
		}
	}
	return nil, -1
}

func TestApplyXrayConfigNoModeAQuicXrayRule(t *testing.T) {
	cfg := renderXrayConfigForFallbackTest(t, "A", "proxy", "normal", true)
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	if r, _ := findRuleByTag(rules, "mode-a-disable-quic"); r != nil {
		t.Fatalf("mode A should not include mode-a-disable-quic in xray routing: %#v", r)
	}
}
