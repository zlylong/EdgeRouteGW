package main

import "testing"

func TestNormalizeConnectionPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"tproxy_in >> direct", "direct"},
		{"tproxy_in >> proxy-3-out", "proxy-3-out"},
		{"api_inbound -> api", "api"},
		{"dns-out", "dns-out"},
		{"  PROXY-2-OUT  ", "proxy-2-out"},
	}
	for _, c := range cases {
		if got := normalizeConnectionPolicy(c.in); got != c.want {
			t.Fatalf("normalizeConnectionPolicy(%q) want %q got %q", c.in, c.want, got)
		}
	}
}
