package main

import "testing"

func TestParseFRRTaggedRoutesFromConfig(t *testing.T) {
	conf := `
router ospf
 ip route 8.0.0.0/8 127.0.0.1 tag 100
 ip route 8.8.8.0/24 127.0.0.1 tag 100
 ip route 1.1.1.1/32 127.0.0.1 tag 200
 ip route bad-value 127.0.0.1 tag 100
 ip route 9.9.9.0/24 127.0.0.1 tag 100
`
	set := parseFRRTaggedRoutesFromConfig(conf)
	if len(set) != 3 {
		t.Fatalf("unexpected parsed routes len=%d, set=%v", len(set), set)
	}
	for _, k := range []string{"8.0.0.0/8", "8.8.8.0/24", "9.9.9.0/24"} {
		if _, ok := set[k]; !ok {
			t.Fatalf("missing route %s", k)
		}
	}
}
