package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	minDomainCacheTTLSeconds     = 300
	maxDomainCacheTTLSeconds     = 3600
	domainFailureRetrySeconds    = 300
	staleDomainFallbackTTLSecond = 300
)

var nowFunc = time.Now

var resolveDomainIPv4WithTTL = func(domain string) ([]string, int, error) {
	ips, err := geoQueryLookupIP(domain)
	if err != nil {
		return nil, 0, err
	}
	return ips, minDomainCacheTTLSeconds, nil
}

func ensureRouteCacheTables() {
	if db == nil {
		return
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS geosite_expand_cache (
			tag TEXT NOT NULL,
			geodata_ver TEXT NOT NULL,
			domains_json TEXT NOT NULL,
			skipped_count INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (tag, geodata_ver)
		);`,
		`CREATE TABLE IF NOT EXISTS domain_resolve_cache (
			domain TEXT PRIMARY KEY,
			ips_json TEXT NOT NULL,
			dns_ttl INTEGER NOT NULL DEFAULT 300,
			resolved_at DATETIME NOT NULL,
			expire_at DATETIME NOT NULL,
			last_error TEXT NOT NULL DEFAULT '',
			fail_count INTEGER NOT NULL DEFAULT 0,
			geodata_ver TEXT NOT NULL DEFAULT ''
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			log.Printf("[WARN] ensure route cache table failed: %v", err)
		}
	}
}

func getGeoDataVersion() string {
	data, err := os.ReadFile(getPath("core", "mosdns", "geodata.ver"))
	if err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	if info, statErr := os.Stat(getPath("core", "mosdns", "geosite.dat")); statErr == nil {
		return info.ModTime().UTC().Format(time.RFC3339)
	}
	return "unknown"
}

func clampDomainCacheTTL(ttl int) int {
	if ttl < minDomainCacheTTLSeconds {
		return minDomainCacheTTLSeconds
	}
	if ttl > maxDomainCacheTTLSeconds {
		return maxDomainCacheTTLSeconds
	}
	return ttl
}

func normalizeIPList(ips []string) []string {
	if len(ips) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ips))
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		if _, ok := seen[ip]; ok {
			continue
		}
		seen[ip] = struct{}{}
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}

func getOrRefreshGeositeDomainCache(tag string) ([]string, int, error) {
	ensureRouteCacheTables()
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return nil, 0, nil
	}
	geodataVer := getGeoDataVersion()
	var domainsJSON string
	var skipped int
	if err := db.QueryRow("SELECT domains_json, skipped_count FROM geosite_expand_cache WHERE tag=? AND geodata_ver=?", tag, geodataVer).Scan(&domainsJSON, &skipped); err == nil {
		var domains []string
		if err := json.Unmarshal([]byte(domainsJSON), &domains); err == nil {
			return domains, skipped, nil
		}
	}

	domains, skipped, err := extractGeoSiteResolvableDomains(getPath("core", "mosdns", "geosite.dat"), tag)
	if err != nil {
		return nil, 0, err
	}
	payload, _ := json.Marshal(domains)
	if _, err := db.Exec("INSERT INTO geosite_expand_cache (tag, geodata_ver, domains_json, skipped_count, updated_at) VALUES (?, ?, ?, ?, datetime('now')) ON CONFLICT(tag, geodata_ver) DO UPDATE SET domains_json=excluded.domains_json, skipped_count=excluded.skipped_count, updated_at=datetime('now')", tag, geodataVer, string(payload), skipped); err != nil {
		log.Printf("[WARN] persist geosite cache %q failed: %v", tag, err)
	}
	return domains, skipped, nil
}

func getOrRefreshDomainCache(domain string) ([]string, int, bool, error) {
	ensureRouteCacheTables()
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" {
		return nil, 0, false, nil
	}

	now := nowFunc().UTC()
	var cachedIPsJSON, lastError string
	var expireAtUnix, resolvedAtUnix int64
	var dnsTTL, failCount int
	cacheHit := false
	if err := db.QueryRow("SELECT ips_json, dns_ttl, CAST(resolved_at AS INTEGER), CAST(expire_at AS INTEGER), last_error, fail_count FROM domain_resolve_cache WHERE domain=?", domain).Scan(&cachedIPsJSON, &dnsTTL, &resolvedAtUnix, &expireAtUnix, &lastError, &failCount); err == nil {
		cacheHit = true
		var cachedIPs []string
		_ = json.Unmarshal([]byte(cachedIPsJSON), &cachedIPs)
		cachedIPs = normalizeIPList(cachedIPs)
		if expireAtUnix > now.Unix() && len(cachedIPs) > 0 {
			return cachedIPs, dnsTTL, true, nil
		}
	}

	ips, ttl, err := resolveDomainIPv4WithTTL(domain)
	if err == nil {
		ips = normalizeIPList(ips)
		ttl = clampDomainCacheTTL(ttl)
		payload, _ := json.Marshal(ips)
		expireAt := now.Add(time.Duration(ttl) * time.Second).Unix()
		resolvedAt := now.Unix()
		if _, execErr := db.Exec("INSERT INTO domain_resolve_cache (domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver) VALUES (?, ?, ?, ?, ?, '', 0, ?) ON CONFLICT(domain) DO UPDATE SET ips_json=excluded.ips_json, dns_ttl=excluded.dns_ttl, resolved_at=excluded.resolved_at, expire_at=excluded.expire_at, last_error='', fail_count=0, geodata_ver=excluded.geodata_ver", domain, string(payload), ttl, resolvedAt, expireAt, getGeoDataVersion()); execErr != nil {
			log.Printf("[WARN] persist domain cache %q failed: %v", domain, execErr)
		}
		return ips, ttl, false, nil
	}

	if cacheHit {
		var cachedIPs []string
		_ = json.Unmarshal([]byte(cachedIPsJSON), &cachedIPs)
		cachedIPs = normalizeIPList(cachedIPs)
		if len(cachedIPs) > 0 {
			nextRetryTTL := dnsTTL
			if nextRetryTTL <= 0 {
				nextRetryTTL = staleDomainFallbackTTLSecond
			}
			nextRetryTTL = clampDomainCacheTTL(nextRetryTTL)
			nextRetryAt := now.Add(time.Duration(domainFailureRetrySeconds) * time.Second).Unix()
			if _, execErr := db.Exec("UPDATE domain_resolve_cache SET expire_at=?, last_error=?, fail_count=fail_count+1 WHERE domain=?", nextRetryAt, err.Error(), domain); execErr != nil {
				log.Printf("[WARN] update domain cache failure state %q failed: %v", domain, execErr)
			}
			return cachedIPs, nextRetryTTL, false, nil
		}
	}
	return nil, 0, false, fmt.Errorf("resolve %s failed: %w", domain, err)
}
