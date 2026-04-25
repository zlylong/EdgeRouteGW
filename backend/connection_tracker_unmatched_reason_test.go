package main

import (
	"strings"
	"testing"
)

func ensureConnectionTrackerLookupTables(t *testing.T) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS routes_table (ip TEXT PRIMARY KEY, domain TEXT, source TEXT, first_seen DATETIME, last_seen DATETIME, ttl INTEGER, status TEXT, miss_count INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS domain_resolve_cache (domain TEXT PRIMARY KEY, ips_json TEXT NOT NULL, dns_ttl INTEGER NOT NULL DEFAULT 300, resolved_at DATETIME NOT NULL, expire_at DATETIME NOT NULL, last_error TEXT NOT NULL DEFAULT "", fail_count INTEGER NOT NULL DEFAULT 0, geodata_ver TEXT NOT NULL DEFAULT "")`); err != nil {
		t.Fatal(err)
	}
}

func TestAttachRuleMatchMeta_UnmatchedReason_NoPolicyMatch(t *testing.T) {
	tdb, _ := setupTestDB(t)
	db = tdb
	t.Cleanup(func() { db.Close() })
	ensureConnectionTrackerLookupTables(t)

	if _, err := db.Exec(`DELETE FROM rules`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type,value,policy) VALUES(?,?,?)", "ip", "1.2.3.4", "direct"); err != nil {
		t.Fatal(err)
	}

	records := attachRuleMatchMeta([]ConnectionRecord{{
		Target: "1.2.3.4:443",
		Policy: "proxy-1-out",
	}})
	if len(records) != 1 {
		t.Fatalf("unexpected records len=%d", len(records))
	}
	if records[0].RuleID != 0 {
		t.Fatalf("unexpected rule match: %+v", records[0])
	}
	if records[0].UnmatchedReason != "无策略一致的规则" {
		t.Fatalf("unexpected unmatched reason: %q", records[0].UnmatchedReason)
	}
}

func TestAttachRuleMatchMeta_UnmatchedReason_IPWithoutDomainBackfill(t *testing.T) {
	tdb, _ := setupTestDB(t)
	db = tdb
	t.Cleanup(func() { db.Close() })
	ensureConnectionTrackerLookupTables(t)

	if _, err := db.Exec(`DELETE FROM rules`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type,value,policy) VALUES(?,?,?)", "domain", "example.com", "proxy"); err != nil {
		t.Fatal(err)
	}

	records := attachRuleMatchMeta([]ConnectionRecord{{
		Target: "1.2.3.4:443",
		Policy: "proxy",
	}})
	if len(records) != 1 {
		t.Fatalf("unexpected records len=%d", len(records))
	}
	if records[0].RuleID != 0 {
		t.Fatalf("unexpected rule match: %+v", records[0])
	}
	if !strings.Contains(records[0].UnmatchedReason, "目标为IP且无域名回填") {
		t.Fatalf("unexpected unmatched reason: %q", records[0].UnmatchedReason)
	}
}
