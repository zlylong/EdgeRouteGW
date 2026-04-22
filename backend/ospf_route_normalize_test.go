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
		if domain != "google.com" {
			t.Fatalf("unexpected domain: %s", domain)
		}
		return []string{"8.8.8.8/32", "8.8.8.8"}, 300, nil
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
