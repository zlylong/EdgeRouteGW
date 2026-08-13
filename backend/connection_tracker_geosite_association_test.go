package main

import "testing"

func TestAttachRuleMatchMetaDomainByResolveCache(t *testing.T) {
	tdb, _ := setupTestDB(t)
	setDB(tdb)
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS domain_resolve_cache (
			domain TEXT PRIMARY KEY,
			ips_json TEXT NOT NULL,
			dns_ttl INTEGER NOT NULL DEFAULT 300,
			resolved_at DATETIME NOT NULL,
			expire_at DATETIME NOT NULL,
			last_error TEXT NOT NULL DEFAULT '',
			fail_count INTEGER NOT NULL DEFAULT 0,
			geodata_ver TEXT NOT NULL DEFAULT ''
		);
	`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO rules(type, value, policy) VALUES ('domain', 'anthropic.com', 'proxy-3')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT OR REPLACE INTO domain_resolve_cache(domain, ips_json, resolved_at, expire_at)
		VALUES ('remote:anthropic.com', '["160.79.104.10"]', '200', '300')
	`); err != nil {
		t.Fatal(err)
	}

	in := []ConnectionRecord{{
		Client:  "192.168.20.158:12345",
		Network: "tcp",
		Target:  "160.79.104.10:443",
		Policy:  "proxy-3-out",
	}}
	out := attachRuleMatchMeta(in)
	if len(out) != 1 {
		t.Fatalf("unexpected len: %d", len(out))
	}
	if out[0].RuleID == 0 || out[0].RuleType != "domain" || out[0].MatchValue != "anthropic.com" {
		t.Fatalf("unexpected association: %+v", out[0])
	}
	if out[0].TargetDomain != "anthropic.com" {
		t.Fatalf("target_domain want anthropic.com got %q", out[0].TargetDomain)
	}
}
