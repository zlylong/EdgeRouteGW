package main

import (
	"errors"
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
	oldResolve := resolveDomainIPv4WithTTL
	resolveDomainIPv4WithTTL = func(domain string) ([]string, int, error) {
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
	defer func() { resolveDomainIPv4WithTTL = oldResolve }()

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
	oldResolve := resolveDomainIPv4WithTTL
	resolveDomainIPv4WithTTL = func(domain string) ([]string, int, error) {
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
	defer func() { resolveDomainIPv4WithTTL = oldResolve }()

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
