package main

import (
	"testing"
)

func TestSyncStaticRoutesToOSPF_GeositeFallsBackToGeoIPAndDomainResolution(t *testing.T) {
	setupFeatureSuiteRouter(t)
	writeTestGeoData(t)

	if _, err := db.Exec("DELETE FROM rules"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM routes_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('geosite', 'google', 'proxy')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO rules(type, value, policy) VALUES ('geosite', 'gfw', 'proxy')"); err != nil {
		t.Fatal(err)
	}

	oldResolve := resolveDomainIPv4WithTTLViaServers
	resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string) ([]string, int, error) {
		switch domain {
		case "google.com":
			return []string{"8.8.8.8"}, 300, nil
		case "youtube.com":
			return []string{"9.9.9.9", "8.8.8.8"}, 300, nil
		default:
			return nil, 0, nil
		}
	}
	defer func() { resolveDomainIPv4WithTTLViaServers = oldResolve }()

	syncStaticRoutesToOSPF("C")

	rows, err := db.Query("SELECT ip FROM routes_table WHERE source='static' ORDER BY ip")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			t.Fatal(err)
		}
		ips = append(ips, ip)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	expected := []string{"8.8.8.0/24", "8.8.8.8", "9.9.9.9"}
	if len(ips) != len(expected) {
		t.Fatalf("unexpected static route count: got=%v want=%v", ips, expected)
	}
	for i := range expected {
		if ips[i] != expected[i] {
			t.Fatalf("unexpected static routes: got=%v want=%v", ips, expected)
		}
	}
}
