package main

import (
	"errors"
	"sync"
	"testing"
	"time"
)

var errTestDNSFailure = errors.New("dns failure")

func TestSyncStaticRoutesToOSPF_GeositeUsesDomainResolveCache(t *testing.T) {
	setupFeatureSuiteRouter(t)
	writeTestGeoData(t)

	if _, err := db.Exec("DELETE FROM rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('geosite', 'gfw', 'proxy')"); err != nil {
		t.Fatal(err)
	}

	calls := map[string]int{}
	oldResolve := resolveDomainIPv4WithTTLViaServers
	resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string) ([]string, int, error) {
		calls[domain]++
		switch domain {
		case "google.com":
			return []string{"8.8.8.8"}, 30, nil
		case "youtube.com":
			return []string{"9.9.9.9"}, 30, nil
		default:
			return nil, 0, nil
		}
	}
	defer func() { resolveDomainIPv4WithTTLViaServers = oldResolve }()

	syncStaticRoutesToOSPF("C")
	syncStaticRoutesToOSPF("C")

	if calls["google.com"] != 1 || calls["youtube.com"] != 1 {
		t.Fatalf("expected warm cache to avoid repeated DNS lookups, calls=%v", calls)
	}
}

func TestGetOrRefreshDomainCache_RefreshAfterExpireAndKeepStaleOnFailure(t *testing.T) {
	setupFeatureSuiteRouter(t)

	base := time.Date(2026, 4, 21, 18, 0, 0, 0, time.UTC)
	oldNow := nowFunc
	nowFunc = func() time.Time { return base }
	defer func() { nowFunc = oldNow }()

	calls := 0
	oldResolve := resolveDomainIPv4WithTTLViaServers
	resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string) ([]string, int, error) {
		calls++
		switch calls {
		case 1:
			return []string{"8.8.8.8"}, 30, nil
		case 2:
			return []string{"8.8.4.4"}, 30, nil
		default:
			return nil, 0, errTestDNSFailure
		}
	}
	defer func() { resolveDomainIPv4WithTTLViaServers = oldResolve }()

	ips, ttl, fromCache, err := getOrRefreshDomainCache("google.com")
	if err != nil || fromCache || ttl != 300 || len(ips) != 1 || ips[0] != "8.8.8.8" {
		t.Fatalf("unexpected first resolve: ips=%v ttl=%d fromCache=%v err=%v", ips, ttl, fromCache, err)
	}

	ips, ttl, fromCache, err = getOrRefreshDomainCache("google.com")
	if err != nil || !fromCache || ttl != 300 || len(ips) != 1 || ips[0] != "8.8.8.8" {
		t.Fatalf("unexpected cache hit: ips=%v ttl=%d fromCache=%v err=%v", ips, ttl, fromCache, err)
	}

	nowFunc = func() time.Time { return base.Add(301 * time.Second) }
	ips, ttl, fromCache, err = getOrRefreshDomainCache("google.com")
	if err != nil || fromCache || ttl != 300 || len(ips) != 1 || ips[0] != "8.8.4.4" {
		t.Fatalf("unexpected refresh after expiry: ips=%v ttl=%d fromCache=%v err=%v", ips, ttl, fromCache, err)
	}

	nowFunc = func() time.Time { return base.Add(602 * time.Second) }
	ips, ttl, fromCache, err = getOrRefreshDomainCache("google.com")
	if err != nil || fromCache || ttl != 300 || len(ips) != 1 || ips[0] != "8.8.4.4" {
		t.Fatalf("expected stale cache on refresh failure: ips=%v ttl=%d fromCache=%v err=%v", ips, ttl, fromCache, err)
	}

	if calls != 3 {
		t.Fatalf("unexpected resolver calls: %d", calls)
	}
}

func TestGetOrRefreshDomainCacheWithResolver_SelectsDNSGroup(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("UPDATE settings SET value='10.10.10.10,10.10.10.11' WHERE key='dns_local'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE settings SET value='20.20.20.20,20.20.20.21' WHERE key='dns_remote'"); err != nil {
		t.Fatal(err)
	}

	oldResolve := resolveDomainIPv4WithTTLViaServers
	defer func() { resolveDomainIPv4WithTTLViaServers = oldResolve }()

	captured := make([][]string, 0, 2)
	resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string) ([]string, int, error) {
		copied := append([]string(nil), dnsServers...)
		captured = append(captured, copied)
		return []string{"1.1.1.1"}, 300, nil
	}

	if _, _, _, err := getOrRefreshDomainCacheWithResolver("example.com", resolverGroupRemote); err != nil {
		t.Fatalf("remote resolve failed: %v", err)
	}
	if _, _, _, err := getOrRefreshDomainCacheWithResolver("example.org", resolverGroupLocal); err != nil {
		t.Fatalf("local resolve failed: %v", err)
	}

	if len(captured) != 2 {
		t.Fatalf("unexpected resolver calls: %d", len(captured))
	}
	if len(captured[0]) < 2 || captured[0][0] != "20.20.20.20" || captured[0][1] != "20.20.20.21" {
		t.Fatalf("unexpected remote dns servers: %v", captured[0])
	}
	if len(captured[1]) < 2 || captured[1][0] != "10.10.10.10" || captured[1][1] != "10.10.10.11" {
		t.Fatalf("unexpected local dns servers: %v", captured[1])
	}
}

func TestGetOrRefreshDomainCacheWithResolver_MigratesLegacyRemoteCacheKey(t *testing.T) {
	setupFeatureSuiteRouter(t)
	legacyDomainCacheMigrationOnce = sync.Once{}
	legacyDomainCacheLastSweep = time.Time{}

	base := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC)
	oldNow := nowFunc
	nowFunc = func() time.Time { return base }
	defer func() { nowFunc = oldNow }()

	if _, err := db.Exec("INSERT OR REPLACE INTO domain_resolve_cache(domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver) VALUES (?, ?, ?, ?, ?, '', 0, '')", "legacy.example.com", `["203.0.113.10"]`, 600, base.Unix(), base.Add(600*time.Second).Unix()); err != nil {
		t.Fatal(err)
	}

	oldResolve := resolveDomainIPv4WithTTLViaServers
	resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string) ([]string, int, error) {
		t.Fatalf("resolver should not be called when legacy cache is migrated")
		return nil, 0, nil
	}
	defer func() { resolveDomainIPv4WithTTLViaServers = oldResolve }()

	ips, ttl, fromCache, err := getOrRefreshDomainCacheWithResolver("legacy.example.com", resolverGroupRemote)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fromCache {
		t.Fatalf("expected cache hit after legacy migration")
	}
	if ttl != 600 || len(ips) != 1 || ips[0] != "203.0.113.10" {
		t.Fatalf("unexpected cache payload: ttl=%d ips=%v", ttl, ips)
	}

	var migratedCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM domain_resolve_cache WHERE domain='remote:legacy.example.com'").Scan(&migratedCount); err != nil {
		t.Fatal(err)
	}
	if migratedCount != 1 {
		t.Fatalf("legacy cache key not migrated, count=%d", migratedCount)
	}
}

func TestSweepLegacyDomainResolveCacheKeys_RemovesLegacyRows(t *testing.T) {
	setupFeatureSuiteRouter(t)
	if _, err := db.Exec("DELETE FROM domain_resolve_cache"); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC).Unix()
	if _, err := db.Exec("INSERT INTO domain_resolve_cache(domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver) VALUES (?, '[\"1.1.1.1\"]', 300, ?, ?, '', 0, '')", "legacy-only.example", base, base+300); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO domain_resolve_cache(domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver) VALUES (?, '[\"2.2.2.2\"]', 300, ?, ?, '', 0, '')", "remote:legacy-only.example", base, base+300); err != nil {
		t.Fatal(err)
	}

	migrated, removed, err := sweepLegacyDomainResolveCacheKeys(10)
	if err != nil {
		t.Fatalf("sweep error: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected removed=1 got=%d migrated=%d", removed, migrated)
	}
	var legacyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM domain_resolve_cache WHERE domain='legacy-only.example'").Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 0 {
		t.Fatalf("legacy key should be removed, count=%d", legacyCount)
	}
}

func TestParseHostLookupOutput(t *testing.T) {
	output := `Trying "www.google.com"
;; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 8537
;; ANSWER SECTION:
www.google.com.		300	IN	A	142.251.150.119
www.google.com.		120	IN	A	142.251.150.120

;; ADDITIONAL SECTION:
ns.example.		60	IN	A	203.0.113.10
Received 64 bytes from 127.0.0.53#53 in 10 ms`

	ips, ttl, err := parseHostLookupOutput(output)
	if err != nil {
		t.Fatalf("parseHostLookupOutput error: %v", err)
	}
	if ttl != 120 {
		t.Fatalf("unexpected ttl: %d", ttl)
	}
	if len(ips) != 2 || ips[0] != "142.251.150.119" || ips[1] != "142.251.150.120" {
		t.Fatalf("unexpected ips: %v", ips)
	}
}

func TestResolveDomainIPv4WithTTLFallsBackWhenHostUnavailable(t *testing.T) {
	oldRunner := hostLookupCommand
	hostLookupCommand = func(domain string) (string, error) {
		return "", errTestDNSFailure
	}
	defer func() { hostLookupCommand = oldRunner }()

	oldLookup := geoQueryLookupIP
	geoQueryLookupIP = func(host string) ([]string, error) {
		if host != "www.google.com" {
			t.Fatalf("unexpected host: %s", host)
		}
		return []string{"142.251.150.119"}, nil
	}
	defer func() { geoQueryLookupIP = oldLookup }()

	ips, ttl, err := resolveDomainIPv4WithTTL("www.google.com")
	if err != nil {
		t.Fatalf("resolveDomainIPv4WithTTL fallback error: %v", err)
	}
	if ttl != 300 {
		t.Fatalf("unexpected fallback ttl: %d", ttl)
	}
	if len(ips) != 1 || ips[0] != "142.251.150.119" {
		t.Fatalf("unexpected fallback ips: %v", ips)
	}
}
