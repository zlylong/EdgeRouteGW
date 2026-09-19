package main

import "testing"

func TestResolveHABalancer(t *testing.T) {
	set := func(tags ...string) map[string]struct{} {
		m := map[string]struct{}{}
		for _, tag := range tags {
			m[tag] = struct{}{}
		}
		return m
	}

	tests := []struct {
		name         string
		bTag         string
		outbounds    map[string]struct{}
		wantBalancer bool
		wantFallback string // fallbackTag inside the balancer, "" = must be absent
		wantDirect   string
	}{
		{"both present", "bal-ha-6-12", set("proxy-6-out", "proxy-12-out"), true, "proxy-12-out", ""},
		// The production case: node 5 was deleted and re-imported as node 12,
		// the rule still said ha-6-5.
		{"fallback deleted", "bal-ha-6-5", set("proxy-6-out", "proxy-12-out"), true, "", ""},
		{"primary deleted", "bal-ha-5-6", set("proxy-6-out"), false, "", "proxy-6-out"},
		{"both deleted", "bal-ha-5-10", set("proxy-6-out"), false, "", ""},
		{"malformed", "bal-ha-6", set("proxy-6-out"), false, "", ""},
		{"empty id", "bal-ha-6-", set("proxy-6-out"), false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			balancer, direct := resolveHABalancer(tc.bTag, tc.outbounds)
			if (balancer != nil) != tc.wantBalancer {
				t.Fatalf("balancer = %v, want present=%v", balancer, tc.wantBalancer)
			}
			if direct != tc.wantDirect {
				t.Errorf("direct outbound = %q, want %q", direct, tc.wantDirect)
			}
			if balancer == nil {
				return
			}
			for _, sel := range balancer["selector"].([]string) {
				if _, ok := tc.outbounds[sel]; !ok {
					t.Errorf("selector %q is not an existing outbound", sel)
				}
			}
			fb, has := balancer["fallbackTag"].(string)
			if tc.wantFallback == "" && has {
				t.Errorf("fallbackTag = %q, want none", fb)
			}
			if tc.wantFallback != "" && fb != tc.wantFallback {
				t.Errorf("fallbackTag = %q, want %q", fb, tc.wantFallback)
			}
		})
	}
}
