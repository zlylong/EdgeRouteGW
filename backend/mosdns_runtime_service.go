package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
)

func formatUpstreams(addrs string, useSocks bool) string {
	parts := strings.Split(addrs, ",")
	var items []string
	for _, p := range parts {
		clean, ok := sanitizeUpstreamItem(p)
		if !ok {
			continue
		}
		if useSocks {
			items = append(items, fmt.Sprintf(`{ addr: "%s", socks5: "127.0.0.1:10808" }`, clean))
		} else {
			// Always force SOCKS5 for remote DNS upstreams in all modes to bypass pollution
			items = append(items, fmt.Sprintf(`{ addr: "%s", socks5: "127.0.0.1:10808" }`, clean))
		}
	}
	if len(items) == 0 {
		if useSocks {
			return `[{ addr: "1.1.1.1", socks5: "127.0.0.1:10808" }, { addr: "8.8.8.8", socks5: "127.0.0.1:10808" }]`
		}
		// Always force SOCKS5 for remote DNS upstreams even if empty
		return `[{ addr: "1.1.1.1", socks5: "127.0.0.1:10808" }, { addr: "8.8.8.8", socks5: "127.0.0.1:10808" }]`
	}
	return "[" + strings.Join(items, ", ") + "]"
}

func buildMosdnsProxyDomains(mode string) ([]string, error) {
	seen := map[string]struct{}{}
	proxyDomains := make([]string, 0)
	addDomain := func(domain string) {
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		if domain == "" {
			return
		}
		if _, ok := seen[domain]; ok {
			return
		}
		seen[domain] = struct{}{}
		proxyDomains = append(proxyDomains, domain)
	}

	dRows, err := db.Query("SELECT value FROM rules WHERE type='domain' AND policy LIKE 'proxy%'")
	if err != nil {
		return nil, fmt.Errorf("query domain rules failed: %w", err)
	}
	for dRows.Next() {
		var d string
		if err := dRows.Scan(&d); err != nil {
			continue
		}
		if normalized, ok := mosdnsRuleDomainValue(d); ok {
			addDomain(normalized)
		} else if strings.Contains(d, "*") {
			log.Printf("[INFO] skip wildcard domain %q from mosdns proxy_domain set; runtime match still handled by xray", d)
		}
	}
	if err := dRows.Err(); err != nil {
		dRows.Close()
		return nil, fmt.Errorf("iterate domain rules failed: %w", err)
	}
	dRows.Close()

	gRows, err := db.Query("SELECT value FROM rules WHERE type='geosite' AND policy LIKE 'proxy%'")
	if err != nil {
		return nil, fmt.Errorf("query geosite rules failed: %w", err)
	}
	geositePath := getPath("core", "mosdns", "geosite.dat")
	for gRows.Next() {
		var tag string
		if err := gRows.Scan(&tag); err != nil {
			continue
		}
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		values := extractGeoSiteValues(geositePath, tag)
		if len(values) == 0 {
			log.Printf("[WARN] geosite %q produced 0 entries for mosdns proxy_domains (mode=%s)", tag, mode)
			continue
		}
		added := 0
		skipped := 0
		for _, value := range values {
			normalized := strings.ToLower(strings.TrimSpace(value))
			if normalized == "" {
				continue
			}
			if strings.HasPrefix(normalized, "domain:") || strings.HasPrefix(normalized, "full:") || strings.HasPrefix(normalized, "keyword:") || strings.HasPrefix(normalized, "regexp:") {
				if _, ok := seen[normalized]; ok {
					continue
				}
				seen[normalized] = struct{}{}
				proxyDomains = append(proxyDomains, normalized)
				added++
				continue
			}
			skipped++
		}
		if added == 0 {
			log.Printf("[WARN] geosite %q had no mosdns-compatible entries (mode=%s)", tag, mode)
		}
		if skipped > 0 {
			log.Printf("[INFO] geosite %q added %d entries to mosdns proxy_domains (skipped=%d, mode=%s)", tag, added, skipped, mode)
		}
	}
	if err := gRows.Err(); err != nil {
		gRows.Close()
		return nil, fmt.Errorf("iterate geosite rules failed: %w", err)
	}
	gRows.Close()

	sort.Strings(proxyDomains)
	return proxyDomains, nil
}

func applyMosdnsConfig() error {
	applyMutex.Lock()
	defer applyMutex.Unlock()
	log.Println("[AUDIT] Applying Mosdns Config")
	var local, remote, lazyStr, logLevel, cacheSizeStr, lazyTTLStr string

	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_local'").Scan(&local); err != nil {
		local = "119.29.29.29,223.5.5.5"
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_remote'").Scan(&remote); err != nil {
		remote = "1.1.1.1,8.8.8.8"
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_lazy'").Scan(&lazyStr); err != nil {
		lazyStr = "true"
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_log_level'").Scan(&logLevel); err != nil {
		logLevel = "info"
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_cache_size'").Scan(&cacheSizeStr); err != nil {
		cacheSizeStr = "10240"
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_lazy_ttl'").Scan(&lazyTTLStr); err != nil {
		lazyTTLStr = "86400"
	}

	cacheSize, _ := strconv.Atoi(cacheSizeStr)
	lazyTTL, _ := strconv.Atoi(lazyTTLStr)

	var mode string
	db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	proxyDomains, err := buildMosdnsProxyDomains(mode)
	if err != nil {
		return err
	}
	if err := os.WriteFile(getPath("core", "mosdns", "proxy_domains.txt"), []byte(strings.Join(proxyDomains, "\n")), 0644); err != nil {
		return fmt.Errorf("failed to write proxy_domains.txt: %v", err)
	}

	config := renderMosdnsConfig(local, remote, lazyStr == "true", mode, logLevel, cacheSize, lazyTTL)

	if err := os.WriteFile(getPath("core", "mosdns", "config.yaml"), []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write mosdns config.yaml: %v", err)
	}
	err = sysCmd.run("systemctl", "restart", "mosdns")
	if err != nil {
		return err
	}
	return nil
}
