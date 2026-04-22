package main

import "testing"

func TestPruneStaticRoutesPreferBroad(t *testing.T) {
	in := map[string]routeState{
		"8.0.0.0/8":       {ttl: 100},
		"8.8.0.0/16":      {ttl: 100},
		"8.8.8.0/24":      {ttl: 100},
		"8.8.8.8/32":      {ttl: 100},
		"9.9.9.9/32":      {ttl: 100},
		"11.11.11.0/24":   {ttl: 100},
		"11.11.11.11/32":  {ttl: 100},
		"100.64.0.0/10":   {ttl: 100},
		"100.64.1.0/24":   {ttl: 100},
		"101.64.1.0/24":   {ttl: 100},
	}

	out, removed := pruneStaticRoutesPreferBroad(in)
	if removed < 5 {
		t.Fatalf("expected at least 5 routes pruned, got %d", removed)
	}
	mustKeep := []string{"8.0.0.0/8", "9.9.9.9/32", "11.11.11.0/24", "100.64.0.0/10", "101.64.1.0/24"}
	for _, k := range mustKeep {
		if _, ok := out[k]; !ok {
			t.Fatalf("expected kept route %s", k)
		}
	}
	mustDrop := []string{"8.8.0.0/16", "8.8.8.0/24", "8.8.8.8/32", "11.11.11.11/32", "100.64.1.0/24"}
	for _, k := range mustDrop {
		if _, ok := out[k]; ok {
			t.Fatalf("expected pruned route %s", k)
		}
	}
}
