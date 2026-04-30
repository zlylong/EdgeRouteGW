package main

import (
	"fmt"
	"log"
	"os"
)

const minHealthyGeodataSize int64 = 1024 * 1024 // 1MB

func ensureGeodataHealthy() {
	// If both xray assets are missing or too small, trigger a full bootstrap from remote
	xrayGeoIP := getPath("core", "xray", "geoip.dat")
	xrayGeoSite := getPath("core", "xray", "geosite.dat")

	needsBootstrap := false
	if info, err := os.Stat(xrayGeoIP); err != nil || info.Size() < minHealthyGeodataSize {
		needsBootstrap = true
	} else if info, err := os.Stat(xrayGeoSite); err != nil || info.Size() < minHealthyGeodataSize {
		needsBootstrap = true
	}

	if needsBootstrap {
		log.Printf("[INFO] geodata missing or corrupted, triggering remote bootstrap...")
		if err := updateGeodata(); err != nil {
			log.Printf("[ERROR] geodata bootstrap failed: %v", err)
		} else {
			log.Printf("[INFO] geodata bootstrap successful")
		}
	}

	pairs := []struct {
		name string
		dst  string
		src  string
	}{
		{name: "geoip.dat", dst: getPath("core", "mosdns", "geoip.dat"), src: getPath("core", "xray", "geoip.dat")},
		{name: "geosite.dat", dst: getPath("core", "mosdns", "geosite.dat"), src: getPath("core", "xray", "geosite.dat")},
	}
	for _, p := range pairs {
		if err := ensureSingleGeodataHealthy(p.name, p.dst, p.src); err != nil {
			log.Printf("[WARN] geodata self-heal skipped for %s: %v", p.name, err)
		}
	}
}

func ensureSingleGeodataHealthy(name, dst, src string) error {
	dstInfo, dstErr := os.Stat(dst)
	if dstErr == nil && dstInfo.Size() >= minHealthyGeodataSize {
		return nil
	}

	srcInfo, srcErr := os.Stat(src)
	if srcErr != nil {
		return fmt.Errorf("dst unhealthy (%v) and src unavailable: %w", dstErr, srcErr)
	}
	if srcInfo.Size() < minHealthyGeodataSize {
		return fmt.Errorf("dst unhealthy (%v) and src too small (%d)", dstErr, srcInfo.Size())
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read src failed: %w", err)
	}
	if int64(len(data)) < minHealthyGeodataSize {
		return fmt.Errorf("src content too small after read: %d", len(data))
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write dst failed: %w", err)
	}
	log.Printf("[WARN] geodata self-heal recovered %s from %s -> %s (size=%d)", name, src, dst, len(data))
	return nil
}
