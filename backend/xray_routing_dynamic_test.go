package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildXrayDomainRuleValues(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []string
		wantErr bool
	}{
		{name: "plain suffix", value: "c.com", want: []string{"domain:c.com"}},
		{name: "double star suffix", value: "**.c.com", want: []string{"domain:c.com"}},
		{name: "single star regex", value: "*.c.com", want: []string{`regexp:^(?:([^.]+\.)?c\.com)$`}},
		{name: "invalid pattern", value: "foo.*.c.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildXrayDomainRuleValues(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) || got[0] != tt.want[0] {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestSyncXrayRoutingRulesDynamicallySupportsDomainWildcard(t *testing.T) {
	tdb, _ := setupTestDB(t)
	db = tdb
	t.Cleanup(func() { db.Close() })
	_, err := db.Exec(`DELETE FROM rules; INSERT INTO rules(type, value, policy) VALUES ('domain', '*.c.com', 'proxy')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO nodes(id, name, grp, type, address, port, uuid, params, active, ping) VALUES (2, 'n2', 'g2', 'Vless', '2.2.2.2', 8443, 'u2', '{}', 1, 20)`)
	if err != nil {
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

	if err := syncXrayRoutingRulesDynamically(); err != nil {
		t.Fatalf("syncXrayRoutingRulesDynamically error: %v", err)
	}

	payload, err := os.ReadFile("/tmp/proxygw_xray_routing_rules.json")
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
	domains := last["domain"].([]any)
	if len(domains) != 1 || domains[0].(string) != `regexp:^(?:([^.]+\.)?c\.com)$` {
		t.Fatalf("unexpected domain payload: %#v", domains)
	}
}
