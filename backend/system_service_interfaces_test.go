package main

import (
	"strings"
	"testing"
)

func TestShouldSkipInterfaceInManagement(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "", want: true},
		{name: "wg0", want: true},
		{name: "WG1", want: true},
		{name: "eth0", want: false},
		{name: "br-lan", want: false},
	}
	for _, tc := range cases {
		if got := shouldSkipInterfaceInManagement(tc.name); got != tc.want {
			t.Fatalf("name=%q got=%v want=%v", tc.name, got, tc.want)
		}
	}
}

func TestListPrivateIPv4Interfaces_NoWireGuardInOptions(t *testing.T) {
	for _, iface := range listPrivateIPv4Interfaces() {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(iface.Name)), "wg") {
			t.Fatalf("wireguard interface should not appear in interface options: %q", iface.Name)
		}
	}
}
