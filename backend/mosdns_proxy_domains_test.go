package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildMosdnsProxyDomainsIncludesGeositeDomains(t *testing.T) {
	tdb, _ := setupTestDB(t)
	db = tdb
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("DELETE FROM rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', 'example.com', 'proxy')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('geosite', 'anthropic', 'proxy')"); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	oldHome := os.Getenv("PROXYGW_HOME")
	if err := os.Setenv("PROXYGW_HOME", home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("PROXYGW_HOME", oldHome)
	})

	mosdnsDir := filepath.Join(home, "core", "mosdns")
	if err := os.MkdirAll(mosdnsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	geosite := buildTestGeoSiteDat([]testGeoSiteEntry{
		{Tag: "anthropic", Domains: []testGeoSiteDomain{{Type: 2, Value: "anthropic.com"}}},
	})
	if err := os.WriteFile(filepath.Join(mosdnsDir, "geosite.dat"), geosite, 0o644); err != nil {
		t.Fatal(err)
	}

	domains, err := buildMosdnsProxyDomains("A")
	if err != nil {
		t.Fatalf("buildMosdnsProxyDomains error: %v", err)
	}

	seen := map[string]bool{}
	for _, d := range domains {
		seen[d] = true
	}
	if !seen["full:example.com"] {
		t.Fatalf("expected full:example.com in proxy domains, got: %v", domains)
	}
	if !seen["domain:anthropic.com"] {
		t.Fatalf("expected domain:anthropic.com in proxy domains, got: %v", domains)
	}
}
