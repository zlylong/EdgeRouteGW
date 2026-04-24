package main

import "testing"

func TestPolicyMatchesConnection(t *testing.T) {
	cases := []struct {
		rule string
		conn string
		want bool
	}{
		{"direct", "direct", true},
		{"block", "block", true},
		{"proxy", "proxy-3-out", true},
		{"proxy-3", "proxy-3-out", true},
		{"proxy-3", "proxy-4-out", false},
		{"ha-3-4", "proxy-3-out", true},
		{"ha-3-4", "proxy-4-out", true},
		{"ha-3-4", "direct", false},
	}
	for _, c := range cases {
		got := policyMatchesConnection(c.rule, c.conn)
		if got != c.want {
			t.Fatalf("policyMatchesConnection(%q,%q) want %v got %v", c.rule, c.conn, c.want, got)
		}
	}
}
