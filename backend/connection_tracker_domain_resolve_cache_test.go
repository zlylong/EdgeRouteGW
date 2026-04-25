package main

import "testing"

func TestLookupRecentDomainByIPFromResolveCache(t *testing.T) {
	tdb, _ := setupTestDB(t)
	db = tdb
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
	if _, err := db.Exec(`INSERT OR REPLACE INTO domain_resolve_cache(domain, ips_json, resolved_at, expire_at) VALUES ('remote:anthropic.com', '["160.79.104.10"]', '200', '300')`); err != nil {
		t.Fatal(err)
	}
	if got := lookupRecentDomainByIPFromResolveCache("160.79.104.10"); got != "anthropic.com" {
		t.Fatalf("want anthropic.com got %q", got)
	}
}
