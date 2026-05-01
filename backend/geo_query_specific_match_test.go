package main

import (
	"os"
	"testing"
	"sort"
	"reflect"
)

func TestQueryGeoIPBestCIDRsByIP_SmallestSubnet(t *testing.T) {
	setupFeatureSuiteRouter(t)
	
	// Create a geoip.dat with overlapping subnets
	// Tag 'overlap' has 10.0.0.0/8 and 10.1.1.0/24
	geoip := buildTestGeoIPDat([]testGeoIPEntry{
		{Tag: "broad", CIDRs: []struct {
			IP     []byte
			Prefix int
		}{{IP: []byte{10, 0, 0, 0}, Prefix: 8}}},
		{Tag: "specific", CIDRs: []struct {
			IP     []byte
			Prefix int
		}{{IP: []byte{10, 1, 1, 0}, Prefix: 24}}},
	})
	
	geoipPath := getPath("core", "mosdns", "geoip.dat")
	if err := os.WriteFile(geoipPath, geoip, 0o644); err != nil {
		t.Fatal(err)
	}
	
	// Reset matcher cache
	geoIPMatcherMu.Lock()
	geoIPMatcherCache = map[string]*geoIPMatcher{}
	geoIPMatcherMu.Unlock()

	t.Run("picks specific over broad", func(t *testing.T) {
		cidrs := queryGeoIPBestCIDRsByIP(geoipPath, "10.1.1.1")
		expected := []string{"10.1.1.0/24"}
		sort.Strings(cidrs)
		if !reflect.DeepEqual(cidrs, expected) {
			t.Fatalf("expected %v, got %v (should pick most specific match)", expected, cidrs)
		}
	})
	
	t.Run("picks multiple specific if they have same prefix length", func(t *testing.T) {
		geoip2 := buildTestGeoIPDat([]testGeoIPEntry{
			{Tag: "tag1", CIDRs: []struct {
				IP     []byte
				Prefix int
			}{{IP: []byte{10, 1, 1, 0}, Prefix: 24}}},
			{Tag: "tag2", CIDRs: []struct {
				IP     []byte
				Prefix int
			}{{IP: []byte{10, 1, 1, 0}, Prefix: 24}}},
		})
		if err := os.WriteFile(geoipPath, geoip2, 0o644); err != nil {
			t.Fatal(err)
		}
		geoIPMatcherMu.Lock()
		geoIPMatcherCache = map[string]*geoIPMatcher{}
		geoIPMatcherMu.Unlock()
		
		cidrs := queryGeoIPBestCIDRsByIP(geoipPath, "10.1.1.1")
		expected := []string{"10.1.1.0/24"} // It dedupes by CIDR string
		if !reflect.DeepEqual(cidrs, expected) {
			t.Fatalf("expected %v, got %v", expected, cidrs)
		}
	})

    t.Run("tags lookup also picks specific", func(t *testing.T) {
		// Using the first geoip data (broad /8 and specific /24)
		if err := os.WriteFile(geoipPath, geoip, 0o644); err != nil {
			t.Fatal(err)
		}
		geoIPMatcherMu.Lock()
		geoIPMatcherCache = map[string]*geoIPMatcher{}
		geoIPMatcherMu.Unlock()
        
        tags := queryGeoIPTagsByIP(geoipPath, "10.1.1.1")
        expected := []string{"specific"}
        if !reflect.DeepEqual(tags, expected) {
            t.Fatalf("expected tags %v, got %v", expected, tags)
        }
    })
}
