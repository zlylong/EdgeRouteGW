package main

import "testing"

func TestValidateAdvertisableCIDR_DenyDangerousRoutes(t *testing.T) {
	cases := []string{
		"0.0.0.0/0",
		"0.0.0.0/32",
		"127.0.0.1/32",
		"169.254.1.1/32",
		"224.0.0.1/32",
		"255.255.255.255/32",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
	}
	for _, route := range cases {
		if err := validateAdvertisableCIDR(route); err == nil {
			t.Fatalf("expected route %s to be denied", route)
		}
	}
}

func TestValidateAdvertisableCIDR_AllowSpecificHostOrSubnet(t *testing.T) {
	cases := []string{
		"1.1.1.1",
		"1.1.1.0/24",
		"10.1.2.3/32",
		"172.16.1.0/24",
		"192.168.100.0/24",
	}
	for _, route := range cases {
		if err := validateAdvertisableCIDR(route); err != nil {
			t.Fatalf("expected route %s to be allowed, got err=%v", route, err)
		}
	}
}
