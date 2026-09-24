package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func formatUpstreams(addrs string, useSocks bool) string {
	parts := strings.Split(addrs, ",")
	var items []string
	for _, p := range parts {
		clean, ok := sanitizeUpstreamItem(p)
		if !ok {
			continue
		}
		if useSocks && isPublicDNSTarget(clean) {
			items = append(items, fmt.Sprintf(`{ addr: "%s", socks5: "127.0.0.1:10808" }`, forceMosdnsTCPAddr(clean)))
		} else {
			items = append(items, fmt.Sprintf(`{ addr: "%s" }`, clean))
		}
	}
	if len(items) == 0 {
		if useSocks {
			return `[{ addr: "tcp://1.1.1.1", socks5: "127.0.0.1:10808" }, { addr: "tcp://8.8.8.8", socks5: "127.0.0.1:10808" }]`
		}
		return `[{ addr: "119.29.29.29" }, { addr: "223.5.5.5" }]`
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

	dRows, err := getDB().Query("SELECT value FROM rules WHERE type='domain' AND policy LIKE 'proxy%'")
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

	gRows, err := getDB().Query("SELECT value FROM rules WHERE type='geosite' AND policy LIKE 'proxy%'")
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

// Seams for the service-management side of a mosdns apply so tests can count
// restarts without systemd.
var (
	restartMosdnsFn = func() error { return sysCmd.run("systemctl", "restart", "mosdns") }
	unitActiveFn    = func(unit string) bool {
		return sysCmd.run("systemctl", "is-active", "--quiet", unit) == nil
	}
)

var (
	lastMosdnsRestartMu sync.Mutex
	lastMosdnsRestartAt time.Time
)

// mosdnsRestartDedupWindow is how recently a restart must have happened for
// a second one to be skipped: /api/apply and a Mode B Xray apply both used to
// restart mosdns within the same second.
const mosdnsRestartDedupWindow = 5 * time.Second

func restartMosdnsTracked() error {
	if err := restartMosdnsFn(); err != nil {
		return err
	}
	lastMosdnsRestartMu.Lock()
	lastMosdnsRestartAt = time.Now()
	lastMosdnsRestartMu.Unlock()
	return nil
}

// restartMosdnsUnlessJustRestarted restarts mosdns unless applyMosdnsConfig
// (or a previous call here) did so within mosdnsRestartDedupWindow.
func restartMosdnsUnlessJustRestarted(reason string) error {
	lastMosdnsRestartMu.Lock()
	recent := !lastMosdnsRestartAt.IsZero() && time.Since(lastMosdnsRestartAt) < mosdnsRestartDedupWindow
	lastMosdnsRestartMu.Unlock()
	if recent {
		log.Printf("[INFO] mosdns restart (%s) skipped: restarted %s ago", reason, time.Since(lastMosdnsRestartAt).Round(time.Millisecond))
		return nil
	}
	return restartMosdnsTracked()
}

type mosdnsArtifacts struct {
	config       []byte
	proxyDomains []byte
}

// writeFileIfChanged writes content to path unless the file already holds
// exactly that content. It reports whether anything was written.
func writeFileIfChanged(path string, content []byte, perm os.FileMode) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return false, err
	}
	return true, nil
}

// applyMosdnsConfig renders the mosdns config and proxy-domain set, writes
// them only if they differ from what is on disk, and restarts mosdns only if
// something changed or the unit is not running. Every rule add, delete and
// reorder used to restart mosdns unconditionally, which dropped the DNS
// cache and in-flight queries for changes (reorders, direct-policy rules)
// that do not touch its input at all.
func applyMosdnsConfig() error {
	applyMutex.Lock()
	defer applyMutex.Unlock()
	art, err := renderMosdnsArtifacts()
	if err != nil {
		return err
	}
	domainsChanged, err := writeFileIfChanged(getPath("core", "mosdns", "proxy_domains.txt"), art.proxyDomains, 0644)
	if err != nil {
		return fmt.Errorf("failed to write proxy_domains.txt: %v", err)
	}
	configChanged, err := writeFileIfChanged(getPath("core", "mosdns", "config.yaml"), art.config, 0644)
	if err != nil {
		return fmt.Errorf("failed to write mosdns config.yaml: %v", err)
	}
	if !domainsChanged && !configChanged && unitActiveFn("mosdns") {
		log.Println("[AUDIT] Mosdns config unchanged, restart skipped")
		return nil
	}
	log.Printf("[AUDIT] Applying Mosdns Config (config_changed=%v domains_changed=%v)", configChanged, domainsChanged)
	return restartMosdnsTracked()
}

// renderMosdnsArtifacts builds the config.yaml and proxy_domains.txt contents
// from the current settings and rules without touching disk or systemd.
func renderMosdnsArtifacts() (mosdnsArtifacts, error) {
	var local, remote, lazyStr, logLevel, cacheSizeStr, lazyTTLStr string

	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='dns_local'").Scan(&local); err != nil {
		local = "119.29.29.29,223.5.5.5"
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='dns_remote'").Scan(&remote); err != nil {
		remote = "1.1.1.1,8.8.8.8"
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='dns_lazy'").Scan(&lazyStr); err != nil {
		lazyStr = "true"
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='dns_log_level'").Scan(&logLevel); err != nil {
		logLevel = "info"
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='dns_cache_size'").Scan(&cacheSizeStr); err != nil {
		cacheSizeStr = "10240"
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='dns_lazy_ttl'").Scan(&lazyTTLStr); err != nil {
		lazyTTLStr = "86400"
	}

	cacheSize, _ := strconv.Atoi(cacheSizeStr)
	lazyTTL, _ := strconv.Atoi(lazyTTLStr)

	var mode string
	getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	proxyDomains, err := buildMosdnsProxyDomains(mode)
	if err != nil {
		return mosdnsArtifacts{}, err
	}
	config := renderMosdnsConfig(local, remote, lazyStr == "true", mode, logLevel, cacheSize, lazyTTL)
	return mosdnsArtifacts{
		config:       []byte(config),
		proxyDomains: []byte(strings.Join(proxyDomains, "\n")),
	}, nil
}
