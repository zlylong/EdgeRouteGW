package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyXrayConfigAddsDefaultFallbackByLanPolicy(t *testing.T) {
	tdb, _ := setupTestDB(t)
	db = tdb
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES ('mode','A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES ('lan_default_policy','proxy')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key,value) VALUES ('default_node_id','3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM rules; INSERT INTO rules(type, value, policy) VALUES ('geosite', 'anthropic', 'proxy-3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO nodes(id, name, grp, type, address, port, uuid, params, active, ping) VALUES (3, 'n3', 'g', 'Vless', '1.1.1.1', 443, 'u3', '{}', 1, 20)`); err != nil {
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
	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	if len(rules) < 2 {
		t.Fatalf("unexpected rules len: %d", len(rules))
	}
	last := rules[len(rules)-1].(map[string]any)
	if last["ruleTag"] != "default-fallback" {
		t.Fatalf("last ruleTag want default-fallback got %#v", last["ruleTag"])
	}
	if last["network"] != "tcp,udp" {
		t.Fatalf("last network want tcp,udp got %#v", last["network"])
	}
	if last["outboundTag"] != "proxy-3-out" {
		t.Fatalf("last outboundTag want proxy-3-out got %#v", last["outboundTag"])
	}
	if _, ok := last["balancerTag"]; ok {
		t.Fatalf("last rule should not keep balancerTag: %#v", last)
	}
}
