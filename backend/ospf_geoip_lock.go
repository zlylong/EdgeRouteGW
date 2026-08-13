package main

import (
	"sort"
	"strings"
)

func ensureDomainGeoIPLockTable() {
	if getDB() == nil {
		return
	}
	if _, err := getDB().Exec(`CREATE TABLE IF NOT EXISTS domain_geoip_lock (
		domain TEXT NOT NULL,
		resolver_group TEXT NOT NULL,
		geoip_tag TEXT NOT NULL,
		geodata_ver TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (domain, resolver_group, geoip_tag, geodata_ver)
	)`); err != nil {
		return
	}
}

func loadDomainGeoIPLockedTags(domain, resolverGroup, geodataVer string) []string {
	ensureDomainGeoIPLockTable()
	domain = strings.ToLower(strings.TrimSpace(domain))
	resolverGroup = normalizeResolverGroup(resolverGroup)
	geodataVer = strings.TrimSpace(geodataVer)
	if domain == "" || geodataVer == "" {
		return nil
	}
	rows, err := getDB().Query("SELECT geoip_tag FROM domain_geoip_lock WHERE domain=? AND resolver_group=? AND geodata_ver=?", domain, resolverGroup, geodataVer)
	if err != nil {
		return nil
	}
	defer rows.Close()
	set := map[string]struct{}{}
	invalid := make([]string, 0)
	for rows.Next() {
		var tag string
		if rows.Scan(&tag) != nil {
			continue
		}
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		routeKey, ok := normalizeRouteKey(tag)
		if !ok || !strings.Contains(routeKey, "/") {
			invalid = append(invalid, tag)
			continue
		}
		set[routeKey] = struct{}{}
	}
	for _, bad := range invalid {
		_, _ = getDB().Exec("DELETE FROM domain_geoip_lock WHERE domain=? AND resolver_group=? AND geodata_ver=? AND geoip_tag=?", domain, resolverGroup, geodataVer, bad)
	}
	if len(set) == 0 {
		return nil
	}
	res := make([]string, 0, len(set))
	for tag := range set {
		res = append(res, tag)
	}
	sort.Strings(res)
	return res
}

func saveDomainGeoIPLockTags(domain, resolverGroup, geodataVer string, tags []string) {
	ensureDomainGeoIPLockTable()
	domain = strings.ToLower(strings.TrimSpace(domain))
	resolverGroup = normalizeResolverGroup(resolverGroup)
	geodataVer = strings.TrimSpace(geodataVer)
	if domain == "" || geodataVer == "" {
		return
	}

	normalized := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		tag := strings.ToLower(strings.TrimSpace(t))
		if tag == "" {
			continue
		}
		routeKey, ok := normalizeRouteKey(tag)
		if !ok || !strings.Contains(routeKey, "/") {
			continue
		}
		if _, exists := seen[routeKey]; exists {
			continue
		}
		seen[routeKey] = struct{}{}
		normalized = append(normalized, routeKey)
	}

	tx, err := getDB().Begin()
	if err != nil {
		return
	}
	if _, err := tx.Exec("DELETE FROM domain_geoip_lock WHERE domain=? AND resolver_group=? AND geodata_ver=?", domain, resolverGroup, geodataVer); err != nil {
		_ = tx.Rollback()
		return
	}
	for _, tag := range normalized {
		if _, err := tx.Exec("INSERT INTO domain_geoip_lock(domain, resolver_group, geoip_tag, geodata_ver, updated_at) VALUES (?, ?, ?, ?, datetime('now'))", domain, resolverGroup, tag, geodataVer); err != nil {
			_ = tx.Rollback()
			return
		}
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
	}
}
