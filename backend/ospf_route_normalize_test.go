package main

import "testing"

func TestNormalizeRouteKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"8.8.8.8", "8.8.8.8/32", true},
		{"8.8.8.8/32", "8.8.8.8/32", true},
		{"8.8.8.9/24", "8.8.8.0/24", true},
		{" 1.1.1.1 ", "1.1.1.1/32", true},
		{"0.0.0.0", "", false},
		{"0.0.0.0/32", "", false},
		{"0.0.0.0/0", "", false},
		{"127.0.0.1", "", false},
		{"169.254.1.10", "", false},
		{"224.0.0.1", "", false},
		{"255.255.255.255/32", "", false},
		{"2001:db8::1", "", false},
		{"not-an-ip", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeRouteKey(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("normalizeRouteKey(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidateAdvertisableCIDR(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"8.8.8.8/32", false},
		{"10.0.0.0/8", true},
		{"10.1.2.0/24", false},
		{"172.16.0.0/12", true},
		{"172.16.1.0/24", false},
		{"192.168.0.0/16", true},
		{"192.168.10.0/24", false},
		{"0.0.0.0/0", true},
	}
	for _, tc := range cases {
		err := validateAdvertisableCIDR(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("validateAdvertisableCIDR(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
	}
}

func TestSyncStaticRoutesToOSPF_DedupSameIPCanonicalForm(t *testing.T) {
	setupFeatureSuiteRouter(t)
	writeTestGeoData(t)

	if _, err := db.Exec("DELETE FROM rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('ip', '8.8.8.8', 'proxy')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', 'google.com', 'proxy')"); err != nil {
		t.Fatal(err)
	}

	oldResolve := resolveDomainIPv4WithTTLViaServers
	resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string) ([]string, int, error) {
		if domain == "google.com" {
			return []string{"8.8.8.8/32", "8.8.8.8"}, 300, nil
		}
		return []string{"203.0.113.10"}, 300, nil
	}
	defer func() { resolveDomainIPv4WithTTLViaServers = oldResolve }()

	syncStaticRoutesToOSPF("C")

	var cnt int
	if err := db.QueryRow("SELECT COUNT(*) FROM routes_table WHERE source='static'").Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("expected deduplicated single static route, got %d", cnt)
	}

	var ip string
	if err := db.QueryRow("SELECT ip FROM routes_table WHERE source='static' LIMIT 1").Scan(&ip); err != nil {
		t.Fatal(err)
	}
	if ip != "8.8.8.8/32" {
		t.Fatalf("expected canonical ip 8.8.8.8/32, got %s", ip)
	}
}

func TestSyncStaticRoutesToOSPF_ExcludesProtectedNodeEndpoints(t *testing.T) {
	setupFeatureSuiteRouter(t)
	writeTestGeoData(t)

	if _, err := db.Exec("DELETE FROM rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('domain', 'example.com', 'proxy')"); err != nil {
		t.Fatal(err)
	}

	oldResolve := resolveDomainIPv4WithTTLViaServers
	resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string) ([]string, int, error) {
		if domain == "example.com" {
			return []string{"2.2.2.2", "8.8.8.8"}, 300, nil // 2.2.2.2 is seeded active node endpoint
		}
		return []string{"203.0.113.11"}, 300, nil
	}
	defer func() { resolveDomainIPv4WithTTLViaServers = oldResolve }()

	syncStaticRoutesToOSPF("C")

	var protectedCnt, normalCnt int
	if err := db.QueryRow("SELECT COUNT(*) FROM routes_table WHERE source='static' AND ip='2.2.2.2/32'").Scan(&protectedCnt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM routes_table WHERE source='static' AND ip='8.8.8.8/32'").Scan(&normalCnt); err != nil {
		t.Fatal(err)
	}
	if protectedCnt != 0 {
		t.Fatalf("protected node endpoint must be excluded, got count=%d", protectedCnt)
	}
	if normalCnt != 1 {
		t.Fatalf("non-protected route should remain, got count=%d", normalCnt)
	}
}
