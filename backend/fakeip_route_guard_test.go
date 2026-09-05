package main

import "testing"

// A FakeIP is only meaningful inside the gateway that minted it: the mapping
// back to a domain lives in Xray's FakeDNS, which Modes A and C do not run.
// Publishing one over OSPF points the main router at an address that resolves
// to nothing. Observed on a live gateway after a Mode B -> C switch, where a
// resolution in flight returned 198.18.216.7, and that /32 was persisted,
// published, and installed on the main router while FakeDNS was already off.
func TestFakeIPPoolIsNeverPublishable(t *testing.T) {
	for _, raw := range []string{
		"198.18.0.1",
		"198.18.216.7",
		"198.18.255.254",
		"198.19.0.1",
		"198.19.255.254",
		"198.18.0.0/16",
		"198.18.216.0/24",
	} {
		if got, ok := normalizeRouteKey(raw); ok {
			t.Errorf("normalizeRouteKey(%q) = %q, publishable; a FakeIP route strands the main router", raw, got)
		}
	}
}

// The guard must not swallow the rest of 198/8 — 198.18.0.0/15 is the whole
// reserved range and neighbouring space is ordinary routable unicast.
func TestAddressesNextToTheFakeIPPoolStayPublishable(t *testing.T) {
	for _, raw := range []string{
		"198.17.255.254",
		"198.20.0.1",
		"198.51.100.1",
		"142.251.157.119",
	} {
		if _, ok := normalizeRouteKey(raw); !ok {
			t.Errorf("normalizeRouteKey(%q) rejected a routable address", raw)
		}
	}
}
