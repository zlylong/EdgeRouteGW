package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func geoipDatPathForBench() string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(file))
	return filepath.Join(root, "core", "mosdns", "geoip.dat")
}

func BenchmarkExtractGeoIPsCN(b *testing.B) {
	path := geoipDatPathForBench()
	if _, err := os.Stat(path); err != nil {
		b.Skipf("geoip.dat not found: %s", path)
	}

	for b.Loop() {
		_ = extractGeoIPs(path, "cn")
	}
}

func BenchmarkExtractGeoIPsExcludeCNPrivate(b *testing.B) {
	path := geoipDatPathForBench()
	if _, err := os.Stat(path); err != nil {
		b.Skipf("geoip.dat not found: %s", path)
	}

	for b.Loop() {
		_ = extractGeoIPsExclude(path, "cn", "private")
	}
}

func BenchmarkExtractGeoIPsExcludeCNPrivate_Count(b *testing.B) {
	path := geoipDatPathForBench()
	if _, err := os.Stat(path); err != nil {
		b.Skipf("geoip.dat not found: %s", path)
	}

	var n int
	for b.Loop() {
		n = len(extractGeoIPsExclude(path, "cn", "private"))
	}
	b.ReportMetric(float64(n), "cidr/op")
}
