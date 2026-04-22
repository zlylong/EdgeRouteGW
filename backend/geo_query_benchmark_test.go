package main

import (
	"os"
	"testing"
)

func BenchmarkQueryGeoIPTagsByIP(b *testing.B) {
	path := geoipDatPathForBench()
	if _, err := os.Stat(path); err != nil {
		b.Skipf("geoip.dat not found: %s", path)
	}

	for b.Loop() {
		_ = queryGeoIPTagsByIP(path, "8.8.8.8")
	}
}
