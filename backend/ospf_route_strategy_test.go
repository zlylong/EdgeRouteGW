package main

import (
	"testing"
	"time"
)

func TestFilterStaticRoutesByPrefixPolicy(t *testing.T) {
	input := map[string]routeState{
		"1.1.1.1/32":    {ttl: 100},
		"1.1.1.0/24":    {ttl: 100},
		"1.1.0.0/16":    {ttl: 100},
		"203.0.113.0/8": {ttl: 100},
	}

	filtered, removed := filterStaticRoutesByPrefixPolicy(input, false, 24)
	if removed != 1 {
		t.Fatalf("unexpected removed count: %d", removed)
	}
	if _, ok := filtered["1.1.1.1/32"]; ok {
		t.Fatalf("/32 should be filtered when allowSlash32=false")
	}
	if _, ok := filtered["1.1.1.0/24"]; !ok {
		t.Fatalf("/24 should be kept")
	}
	if _, ok := filtered["1.1.0.0/16"]; !ok {
		t.Fatalf("/16 should be kept")
	}
}

func TestPruneStaticRoutesByLRU(t *testing.T) {
	setupFeatureSuiteRouter(t)

	base := time.Now().UTC()
	if _, err := db.Exec("INSERT OR REPLACE INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES (?, 'a', 'static', ?, ?, 300, 'published', 0)", "10.0.0.0/8", base.Add(-10*time.Minute).Format("2006-01-02 15:04:05"), base.Add(-10*time.Minute).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO routes_table(ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES (?, 'b', 'static', ?, ?, 300, 'published', 0)", "10.1.0.0/16", base.Add(-2*time.Minute).Format("2006-01-02 15:04:05"), base.Add(-2*time.Minute).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}

	input := map[string]routeState{
		"10.0.0.0/8":  {ttl: 100},
		"10.1.0.0/16": {ttl: 100},
		"10.2.0.0/16": {ttl: 100}, // no history, treated as newest
	}

	out, removed := pruneStaticRoutesByLRU(input, 2)
	if removed != 1 {
		t.Fatalf("unexpected removed count: %d", removed)
	}
	if len(out) != 2 {
		t.Fatalf("unexpected output size: %d", len(out))
	}
	if _, ok := out["10.0.0.0/8"]; ok {
		t.Fatalf("oldest route should be pruned")
	}
	if _, ok := out["10.1.0.0/16"]; !ok {
		t.Fatalf("recent route should be kept")
	}
	if _, ok := out["10.2.0.0/16"]; !ok {
		t.Fatalf("new route should be kept")
	}
}
