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
