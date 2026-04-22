package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

var (
	cachedGeosite []string
	cachedGeoip   []string
	cacheMutex    sync.Mutex
)
var ospfLogs []string
var ospfLogsMu sync.RWMutex
var syncStaticRoutesToOSPFFunc = syncStaticRoutesToOSPF

var (
	staticRouteSyncMu      sync.Mutex
	staticRouteSyncRunning bool
	staticRouteSyncPending bool
)

var (
	domainGeoIPMatchCacheMu sync.Mutex
	domainGeoIPMatchCache   = map[string]domainGeoIPMatchCacheEntry{}
)

const (
	defaultOspfPushBatchLimit      = 500
	defaultOspfPushIntervalSeconds = 10
	defaultOspfResolveWorkers      = 16
	domainGeoIPMatchCacheTTL       = 10 * time.Minute
	domainGeoIPMatchCacheMax       = 200000
)

type domainGeoIPMatchCacheEntry struct {
	tags      []string
	expiresAt time.Time
}

type routeState struct {
	ttl    int
	domain string
}

func addOspfLog(msg string) {
	ospfLogsMu.Lock()
	defer ospfLogsMu.Unlock()
	ospfLogs = append([]string{time.Now().Format("15:04:05") + " " + msg}, ospfLogs...)
	if len(ospfLogs) > 50 {
		ospfLogs = ospfLogs[:50]
	}
}

func getOspfLogsSnapshot() []string {
	ospfLogsMu.RLock()
	defer ospfLogsMu.RUnlock()
	out := make([]string, len(ospfLogs))
	copy(out, ospfLogs)
	return out
}

type ospfControllerSettings struct {
	PushBatchLimit      int
	PushIntervalSeconds int
	ResolveWorkers      int
}

func clampOspfPushBatchLimit(v int) int {
	switch {
	case v < 1:
		return 1
	case v > 100000:
		return 100000
	default:
		return v
	}
}

func clampOspfPushIntervalSeconds(v int) int {
	switch {
	case v < 1:
		return 1
	case v > 3600:
		return 3600
	default:
		return v
	}
}

func clampOspfResolveWorkers(v int) int {
	switch {
	case v < 1:
		return 1
	case v > 128:
		return 128
	default:
		return v
	}
}

func readIntSettingWithDefault(key string, fallback int, clamp func(int) int) int {
	value := fallback
	var raw string
	err := db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&raw)
	switch {
	case err == nil:
		if parsed, parseErr := strconv.Atoi(strings.TrimSpace(raw)); parseErr == nil {
			value = parsed
		}
	case err != sql.ErrNoRows:
		log.Printf("[WARN] SELECT value FROM settings WHERE key=%q err: %v", key, err)
	}
	if clamp != nil {
		value = clamp(value)
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, strconv.Itoa(value)); err != nil {
		log.Printf("[WARN] persist default setting %s failed: %v", key, err)
	}
	return value
}

func getOspfControllerSettings() ospfControllerSettings {
	return ospfControllerSettings{
		PushBatchLimit:      readIntSettingWithDefault("ospf_push_batch_limit", defaultOspfPushBatchLimit, clampOspfPushBatchLimit),
		PushIntervalSeconds: readIntSettingWithDefault("ospf_push_interval_seconds", defaultOspfPushIntervalSeconds, clampOspfPushIntervalSeconds),
		ResolveWorkers:      readIntSettingWithDefault("ospf_resolve_workers", defaultOspfResolveWorkers, clampOspfResolveWorkers),
	}
}

func cloneStringSliceMain(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func getDomainGeoIPMatchCache(key string) ([]string, bool) {
	domainGeoIPMatchCacheMu.Lock()
	defer domainGeoIPMatchCacheMu.Unlock()
	entry, ok := domainGeoIPMatchCache[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(domainGeoIPMatchCache, key)
		return nil, false
	}
	return cloneStringSliceMain(entry.tags), true
}

func setDomainGeoIPMatchCache(key string, tags []string) {
	domainGeoIPMatchCacheMu.Lock()
	defer domainGeoIPMatchCacheMu.Unlock()
	domainGeoIPMatchCache[key] = domainGeoIPMatchCacheEntry{tags: cloneStringSliceMain(tags), expiresAt: time.Now().Add(domainGeoIPMatchCacheTTL)}
	if len(domainGeoIPMatchCache) <= domainGeoIPMatchCacheMax {
		return
	}
	now := time.Now()
	for k, v := range domainGeoIPMatchCache {
		if now.After(v.expiresAt) {
			delete(domainGeoIPMatchCache, k)
		}
	}
	if len(domainGeoIPMatchCache) <= domainGeoIPMatchCacheMax {
		return
	}
	trim := len(domainGeoIPMatchCache) - domainGeoIPMatchCacheMax
	for k := range domainGeoIPMatchCache {
		delete(domainGeoIPMatchCache, k)
		trim--
		if trim <= 0 {
			break
		}
	}
}

var runVtyshConfigBatch = func(config string) (string, error) {
	tmpFile := "/tmp/proxygw_vtysh_batch.conf"
	if err := os.WriteFile(tmpFile, []byte(config), 0600); err != nil {
		return "", err
	}
	defer os.Remove(tmpFile)
	out, err := exec.Command("vtysh", "-f", tmpFile).CombinedOutput()
	return string(out), err
}

func isDirtyRouteIPv4(ip4 net.IP, prefix int) bool {
	if ip4 == nil || ip4.To4() == nil {
		return true
	}
	v4 := ip4.To4()
	if prefix < 0 || prefix > 32 {
		return true
	}
	if prefix == 0 {
		return true // block default route injection into OSPF
	}
	if v4.Equal(net.IPv4zero) {
		return true // 0.0.0.0 or 0.0.0.0/32
	}
	if v4[0] == 127 {
		return true // loopback
	}
	if v4[0] == 169 && v4[1] == 254 {
		return true // link-local
	}
	if v4[0] >= 224 {
		return true // multicast/reserved/broadcast
	}
	return false
}

func normalizeRouteKey(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !strings.Contains(raw, "/") {
		ip := net.ParseIP(raw)
		if ip == nil || ip.To4() == nil {
			return "", false
		}
		ip4 := ip.To4()
		if isDirtyRouteIPv4(ip4, 32) {
			return "", false
		}
		return ip4.String() + "/32", true
	}
	_, ipNet, err := net.ParseCIDR(raw)
	if err != nil || ipNet == nil || ipNet.IP == nil {
		return "", false
	}
	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return "", false
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 || ones < 0 || ones > 32 {
		return "", false
	}
	if isDirtyRouteIPv4(ip4, ones) {
		return "", false
	}
	return (&net.IPNet{IP: ip4, Mask: net.CIDRMask(ones, 32)}).String(), true
}

func extractHostForProtection(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err == nil {
			if host := strings.TrimSpace(u.Hostname()); host != "" {
				return strings.TrimSuffix(host, ".")
			}
		}
	}
	if at := strings.LastIndex(s, "@"); at >= 0 && at+1 < len(s) {
		s = s[at+1:]
	}
	s = strings.TrimPrefix(s, "//")
	if host, _, err := net.SplitHostPort(s); err == nil {
		host = strings.Trim(host, "[]")
		return strings.TrimSuffix(strings.TrimSpace(host), ".")
	}
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i+1:], ":") {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			host := strings.Trim(s[:i], "[]")
			return strings.TrimSuffix(strings.TrimSpace(host), ".")
		}
	}
	s = strings.Trim(s, "[]")
	return strings.TrimSuffix(strings.TrimSpace(s), ".")
}

func collectProtectedRouteKeys() map[string]struct{} {
	protected := make(map[string]struct{})
	hosts := make(map[string]struct{})

	addHost := func(raw string) {
		host := strings.ToLower(extractHostForProtection(raw))
		if host == "" || host == "localhost" {
			return
		}
		hosts[host] = struct{}{}
	}

	collectColumn := func(query string) {
		rows, err := db.Query(query)
		if err != nil {
			log.Printf("[OSPF] collect protected hosts query failed: %v", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				continue
			}
			addHost(value)
		}
	}

	collectColumn("SELECT address FROM nodes WHERE active=1")
	collectColumn("SELECT ssh_host FROM remote_nodes")
	collectColumn("SELECT endpoint FROM remote_node_wg")
	collectColumn("SELECT dest FROM remote_node_vless")

	for host := range hosts {
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			if routeKey, ok := normalizeRouteKey(ip.String()); ok {
				protected[routeKey] = struct{}{}
			}
			continue
		}
		ips, _, _, err := getOrRefreshDomainCacheWithResolver(host, resolverGroupRemote)
		if err != nil {
			log.Printf("[OSPF] resolve protected host %q failed: %v", host, err)
			continue
		}
		for _, ip := range ips {
			if routeKey, ok := normalizeRouteKey(ip); ok {
				protected[routeKey] = struct{}{}
			}
		}
	}

	return protected
}

func collectStaticRoutesForMode(mode string, protected map[string]struct{}) (map[string]routeState, []string) {
	ensureRouteCacheTables()
	staticRoutes := make(map[string]routeState)
	conflictSet := make(map[string]struct{})
	geoipPath := getPath("core", "mosdns", "geoip.dat")
	geodataVer := getGeoDataVersion()
	geoipTagCIDRCache := make(map[string][]string)

	resolveWorkers := getOspfControllerSettings().ResolveWorkers

	runParallel := func(total int, fn func(idx int)) {
		if total <= 0 {
			return
		}
		workers := resolveWorkers
		if workers < 1 {
			workers = 1
		}
		if workers > total {
			workers = total
		}
		jobs := make(chan int)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range jobs {
					fn(idx)
				}
			}()
		}
		for i := 0; i < total; i++ {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}

	var routeStateMu sync.Mutex
	addRoute := func(ip string, ttl int, domain string) {
		routeKey, ok := normalizeRouteKey(ip)
		if !ok {
			return
		}
		routeStateMu.Lock()
		defer routeStateMu.Unlock()
		if _, blocked := protected[routeKey]; blocked {
			conflictSet[routeKey] = struct{}{}
			return
		}
		if ttl <= 0 {
			ttl = 999999999
		}
		cur, ok := staticRoutes[routeKey]
		if !ok || ttl > cur.ttl {
			staticRoutes[routeKey] = routeState{ttl: ttl, domain: domain}
		}
	}

	var geoipTagCIDRCacheMu sync.Mutex
	addGeoIPTagRoutes := func(tag string, domain string) int {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			return 0
		}
		if strings.Contains(tag, "/") {
			routeKey, ok := normalizeRouteKey(tag)
			if !ok {
				return 0
			}
			addRoute(routeKey, 999999999, domain)
			return 1
		}
		geoipTagCIDRCacheMu.Lock()
		cidrs, ok := geoipTagCIDRCache[tag]
		if !ok {
			cidrs = extractGeoIPs(geoipPath, tag)
			geoipTagCIDRCache[tag] = cidrs
		}
		geoipTagCIDRCacheMu.Unlock()
		for _, cidr := range cidrs {
			addRoute(cidr, 999999999, domain)
		}
		return len(cidrs)
	}

	reduceCIDRsPreferBroad := func(cidrs []string) []string {
		if len(cidrs) == 0 {
			return nil
		}
		uniq := make(map[string]struct{}, len(cidrs))
		norm := make([]string, 0, len(cidrs))
		for _, raw := range cidrs {
			routeKey, ok := normalizeRouteKey(raw)
			if !ok {
				continue
			}
			if _, exists := uniq[routeKey]; exists {
				continue
			}
			uniq[routeKey] = struct{}{}
			norm = append(norm, routeKey)
		}
		if len(norm) <= 1 {
			return norm
		}
		sort.Slice(norm, func(i, j int) bool {
			_, ni, _ := net.ParseCIDR(norm[i])
			_, nj, _ := net.ParseCIDR(norm[j])
			if ni == nil || nj == nil {
				return norm[i] < norm[j]
			}
			pi, _ := ni.Mask.Size()
			pj, _ := nj.Mask.Size()
			if pi != pj {
				return pi < pj
			}
			return norm[i] < norm[j]
		})
		keptNets := make([]*net.IPNet, 0, len(norm))
		keptStr := make([]string, 0, len(norm))
		for _, cidr := range norm {
			_, n, err := net.ParseCIDR(cidr)
			if err != nil || n == nil || n.IP == nil || n.IP.To4() == nil {
				continue
			}
			covered := false
			for _, k := range keptNets {
				kp, _ := k.Mask.Size()
				np, _ := n.Mask.Size()
				if kp > np {
					continue
				}
				if k.Contains(n.IP) {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
			keptNets = append(keptNets, n)
			keptStr = append(keptStr, n.String())
		}
		return keptStr
	}

	staticRows, err := db.Query("SELECT value FROM rules WHERE type='ip' AND policy LIKE 'proxy%'")
	if err == nil {
		for staticRows.Next() {
			var ip string
			if err := staticRows.Scan(&ip); err == nil {
				addRoute(ip, 999999999, "static_rule")
			}
		}
		staticRows.Close()
	}

	geoipRows, err := db.Query("SELECT value FROM rules WHERE type='geoip' AND policy LIKE 'proxy%'")
	if err == nil {
		for geoipRows.Next() {
			var tag string
			if err := geoipRows.Scan(&tag); err == nil {
				var ips []string
				if strings.HasPrefix(tag, "!") {
					excludeTag := strings.TrimSpace(strings.TrimPrefix(tag, "!"))
					ips = extractGeoIPsExclude(geoipPath, excludeTag, "private")
					log.Printf("[OSPF] expanded inverted geoip tag %q to %d CIDRs (excluding %q and private)", tag, len(ips), excludeTag)
				} else {
					ips = extractGeoIPs(geoipPath, tag)
				}
				for _, ip := range ips {
					addRoute(ip, 999999999, "static_rule")
				}
			}
		}
		geoipRows.Close()
	}

	if mode == "B" || mode == "C" {
		geositeRows, err := db.Query("SELECT value, policy FROM rules WHERE type='geosite' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%')")
		if err != nil {
			log.Printf("[OSPF] geosite rule query failed: %v", err)
		} else {
			type geositeRule struct {
				tag    string
				policy string
			}
			var geositeRules []geositeRule
			for geositeRows.Next() {
				var rule geositeRule
				if err := geositeRows.Scan(&rule.tag, &rule.policy); err != nil {
					log.Printf("[OSPF] geosite row scan failed: %v", err)
					continue
				}
				geositeRules = append(geositeRules, rule)
			}
			if err := geositeRows.Err(); err != nil {
				log.Printf("[OSPF] geosite row iteration failed: %v", err)
			}
			geositeRows.Close()
			for _, rule := range geositeRules {
				tag := strings.ToLower(strings.TrimSpace(rule.tag))
				if tag == "" {
					continue
				}
				resolverGroup := resolverGroupRemote
				policy := strings.ToLower(strings.TrimSpace(rule.policy))
				if strings.HasPrefix(policy, "direct") {
					resolverGroup = resolverGroupLocal
				}
				if hasGeoIPTag(geoipPath, tag) {
					count := addGeoIPTagRoutes(tag, "static_rule")
					log.Printf("[OSPF] geosite %q (%s) matched geoip tag and expanded to %d CIDRs", tag, policy, count)
					continue
				}

				domains, skipped, err := getOrRefreshGeositeDomainCache(tag)
				if err != nil {
					log.Printf("[OSPF] geosite %q (%s) cache failed: %v", tag, policy, err)
					continue
				}
				type domainResolveResult struct {
					domain     string
					ips        []string
					ttl        int
					lockedTags []string
				}
				results := make([]domainResolveResult, len(domains))
				runParallel(len(domains), func(idx int) {
					domain := domains[idx]
					results[idx].domain = domain
					lockedTags := loadDomainGeoIPLockedTags(domain, resolverGroup, geodataVer)
					if len(lockedTags) > 0 {
						results[idx].lockedTags = lockedTags
						return
					}

					ips, ttl, _, err := getOrRefreshDomainCacheWithResolver(domain, resolverGroup)
					if err != nil {
						log.Printf("[OSPF] geosite %q (%s) resolve %q failed: %v", tag, policy, domain, err)
						return
					}
					results[idx].ips = ips
					results[idx].ttl = ttl
				})

				resolvedIPs := 0
				promotedDomains := 0
				cachedTagSetHits := 0
				uniqueIPSet := make(map[string]struct{})
				for _, res := range results {
					if len(res.lockedTags) > 0 {
						for _, geoTag := range res.lockedTags {
							_ = addGeoIPTagRoutes(geoTag, res.domain)
						}
						promotedDomains++
						continue
					}
					for _, ip := range res.ips {
						uniqueIPSet[ip] = struct{}{}
					}
				}

				uniqueIPs := make([]string, 0, len(uniqueIPSet))
				for ip := range uniqueIPSet {
					uniqueIPs = append(uniqueIPs, ip)
				}
				sort.Strings(uniqueIPs)
				ipMatchedCIDRs := make(map[string][]string, len(uniqueIPs))
				var ipTagMu sync.Mutex
				runParallel(len(uniqueIPs), func(idx int) {
					ip := uniqueIPs[idx]
					cidrs := queryGeoIPBestCIDRsByIP(geoipPath, ip)
					if len(cidrs) == 0 {
						return
					}
					ipTagMu.Lock()
					ipMatchedCIDRs[ip] = cidrs
					ipTagMu.Unlock()
				})

				for _, res := range results {
					if len(res.lockedTags) > 0 || len(res.ips) == 0 {
						continue
					}
					cacheKey := geodataVer + "|" + resolverGroup + "|" + res.domain + "|" + strings.Join(res.ips, ",")
					cidrs, ok := getDomainGeoIPMatchCache(cacheKey)
					if ok {
						cachedTagSetHits++
					} else {
						matchedCIDRs := make(map[string]struct{})
						for _, ip := range res.ips {
							for _, c := range ipMatchedCIDRs[ip] {
								c = strings.TrimSpace(c)
								if c != "" {
									matchedCIDRs[c] = struct{}{}
								}
							}
						}
						if len(matchedCIDRs) > 0 {
							cidrs = make([]string, 0, len(matchedCIDRs))
							for c := range matchedCIDRs {
								cidrs = append(cidrs, c)
							}
							sort.Strings(cidrs)
							cidrs = reduceCIDRsPreferBroad(cidrs)
						}
						setDomainGeoIPMatchCache(cacheKey, cidrs)
					}
					if len(cidrs) > 0 {
						addedCIDRs := 0
						for _, c := range cidrs {
							addedCIDRs += addGeoIPTagRoutes(c, res.domain)
						}
						if addedCIDRs > 0 {
							saveDomainGeoIPLockTags(res.domain, resolverGroup, geodataVer, cidrs)
							promotedDomains++
							continue
						}
					}
					resolvedIPs += len(res.ips)
					for _, ip := range res.ips {
						addRoute(ip, res.ttl, res.domain)
					}
				}
				log.Printf("[OSPF] geosite %q (%s) resolved %d domains into %d IPv4 routes (promoted_geoip_domains=%d, unique_resolved_ips=%d, domain_tag_cache_hits=%d, skipped_non_domain=%d, dns_group=%s, workers=%d)", tag, policy, len(domains), resolvedIPs, promotedDomains, len(uniqueIPs), cachedTagSetHits, skipped, resolverGroup, resolveWorkers)
			}
		}

		domainRows, err := db.Query("SELECT value, policy FROM rules WHERE type='domain' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%')")
		if err == nil {
			type domainRule struct {
				domain string
				policy string
			}
			var domains []domainRule
			for domainRows.Next() {
				var rule domainRule
				if err := domainRows.Scan(&rule.domain, &rule.policy); err == nil {
					domains = append(domains, rule)
				}
			}
			domainRows.Close()
			runParallel(len(domains), func(idx int) {
				rule := domains[idx]
				resolverGroup := resolverGroupRemote
				policy := strings.ToLower(strings.TrimSpace(rule.policy))
				if strings.HasPrefix(policy, "direct") {
					resolverGroup = resolverGroupLocal
				}
				ips, ttl, _, err := getOrRefreshDomainCacheWithResolver(rule.domain, resolverGroup)
				if err != nil {
					log.Printf("[OSPF] domain %q (%s) resolve failed: %v", rule.domain, policy, err)
					return
				}
				for _, ip := range ips {
					addRoute(ip, ttl, rule.domain)
				}
			})
		}
	}

	conflicts := make([]string, 0, len(conflictSet))
	for routeKey := range conflictSet {
		conflicts = append(conflicts, routeKey)
	}
	sort.Strings(conflicts)
	return staticRoutes, conflicts
}

func sampleRouteKeys(routeKeys []string, limit int) string {
	if len(routeKeys) == 0 {
		return ""
	}
	if limit <= 0 || len(routeKeys) <= limit {
		return strings.Join(routeKeys, ",")
	}
	return strings.Join(routeKeys[:limit], ",")
}

func pruneStaticRoutesPreferBroad(staticRoutes map[string]routeState) (map[string]routeState, int) {
	type routeItem struct {
		key     string
		network uint32
		prefix  int
		state   routeState
	}
	items := make([]routeItem, 0, len(staticRoutes))
	passthrough := make(map[string]routeState)
	for key, state := range staticRoutes {
		_, ipNet, err := net.ParseCIDR(key)
		if err != nil || ipNet == nil || ipNet.IP == nil {
			passthrough[key] = state
			continue
		}
		ipValue, ok := ipv4ToUint32(ipNet.IP)
		if !ok {
			passthrough[key] = state
			continue
		}
		prefix, bits := ipNet.Mask.Size()
		if bits != 32 || prefix < 0 || prefix > 32 {
			passthrough[key] = state
			continue
		}
		items = append(items, routeItem{key: key, network: ipValue, prefix: prefix, state: state})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].prefix != items[j].prefix {
			return items[i].prefix < items[j].prefix
		}
		if items[i].network != items[j].network {
			return items[i].network < items[j].network
		}
		return items[i].key < items[j].key
	})
	result := make(map[string]routeState, len(staticRoutes))
	for k, v := range passthrough {
		result[k] = v
	}
	kept := make(map[string]struct{}, len(items))
	removed := 0
	toCIDRKey := func(network uint32, prefix int) string {
		ip := net.IPv4(byte(network>>24), byte(network>>16), byte(network>>8), byte(network)).To4()
		return fmt.Sprintf("%s/%d", ip.String(), prefix)
	}
	for _, item := range items {
		covered := false
		for p := 0; p < item.prefix; p++ {
			var mask uint32
			if p == 0 {
				mask = 0
			} else {
				mask = ^uint32(0) << uint(32-p)
			}
			ancestor := toCIDRKey(item.network&mask, p)
			if _, ok := kept[ancestor]; ok {
				covered = true
				break
			}
		}
		if covered {
			removed++
			continue
		}
		kept[item.key] = struct{}{}
		result[item.key] = item.state
	}
	return result, removed
}

func detectModeSwitchProtectedConflicts(mode string) []string {
	if mode != "B" && mode != "C" {
		return nil
	}
	protected := collectProtectedRouteKeys()
	if len(protected) == 0 {
		return nil
	}

	conflictSet := make(map[string]struct{})
	addIfConflict := func(route string) {
		routeKey, ok := normalizeRouteKey(route)
		if !ok {
			return
		}
		if _, hit := protected[routeKey]; hit {
			conflictSet[routeKey] = struct{}{}
		}
	}

	rows, err := db.Query("SELECT ip FROM routes_table WHERE status IN ('candidate','published') AND source='static'")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ip string
			if rows.Scan(&ip) == nil {
				addIfConflict(ip)
			}
		}
	}

	staticRows, err := db.Query("SELECT value FROM rules WHERE type='ip' AND policy LIKE 'proxy%'")
	if err == nil {
		defer staticRows.Close()
		for staticRows.Next() {
			var ip string
			if staticRows.Scan(&ip) == nil {
				addIfConflict(ip)
			}
		}
	}

	geoipPath := getPath("core", "mosdns", "geoip.dat")
	geoipRows, err := db.Query("SELECT value FROM rules WHERE type='geoip' AND policy LIKE 'proxy%'")
	if err == nil {
		defer geoipRows.Close()
		for geoipRows.Next() {
			var tag string
			if geoipRows.Scan(&tag) != nil {
				continue
			}
			var ips []string
			if strings.HasPrefix(tag, "!") {
				excludeTag := strings.TrimSpace(strings.TrimPrefix(tag, "!"))
				ips = extractGeoIPsExclude(geoipPath, excludeTag, "private")
			} else {
				ips = extractGeoIPs(geoipPath, tag)
			}
			for _, ip := range ips {
				addIfConflict(ip)
			}
		}
	}

	domainRows, err := db.Query("SELECT value, policy FROM rules WHERE type='domain' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%')")
	if err == nil {
		defer domainRows.Close()
		for domainRows.Next() {
			var domain, policy string
			if domainRows.Scan(&domain, &policy) != nil {
				continue
			}
			resolverGroup := resolverGroupRemote
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(policy)), "direct") {
				resolverGroup = resolverGroupLocal
			}
			ips, _, _, err := getOrRefreshDomainCacheWithResolver(domain, resolverGroup)
			if err != nil {
				continue
			}
			for _, ip := range ips {
				addIfConflict(ip)
			}
		}
	}

	conflicts := make([]string, 0, len(conflictSet))
	for routeKey := range conflictSet {
		conflicts = append(conflicts, routeKey)
	}
	sort.Strings(conflicts)
	return conflicts
}

func formatRouteCIDR(ip string) string {
	routeStr, ok := normalizeRouteKey(ip)
	if !ok {
		return ""
	}
	return routeStr
}

func parseFRRTaggedRoutesFromConfig(conf string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ip route ") || !strings.Contains(line, " tag 100") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		routeKey, ok := normalizeRouteKey(fields[2])
		if !ok {
			continue
		}
		set[routeKey] = struct{}{}
	}
	return set
}

func readFRRTaggedStaticRoutes() (map[string]struct{}, error) {
	out, err := exec.Command("vtysh", "-c", "show running-config").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("vtysh show running-config failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return parseFRRTaggedRoutesFromConfig(string(out)), nil
}

func reconcilePublishedRoutesWithFRR() {
	frrRoutes, err := readFRRTaggedStaticRoutes()
	if err != nil {
		log.Printf("[WARN] OSPF reconcile skip: %v", err)
		return
	}
	rows, err := db.Query("SELECT ip, status FROM routes_table WHERE source='static'")
	if err != nil {
		log.Printf("[WARN] OSPF reconcile query failed: %v", err)
		return
	}
	defer rows.Close()

	tx, err := db.Begin()
	if err != nil {
		log.Printf("[WARN] OSPF reconcile begin failed: %v", err)
		return
	}

	updatedToPublished := 0
	demotedToCandidate := 0
	totalPublished := 0
	for rows.Next() {
		var ip string
		var status string
		if rows.Scan(&ip, &status) != nil {
			continue
		}
		routeKey, ok := normalizeRouteKey(ip)
		if !ok {
			continue
		}
		_, inFRR := frrRoutes[routeKey]
		if status == "published" {
			totalPublished++
		}
		if inFRR && status != "published" {
			if _, err := tx.Exec("UPDATE routes_table SET status='published', last_seen=datetime('now'), miss_count=0 WHERE ip=?", routeKey); err == nil {
				updatedToPublished++
			}
			continue
		}
		if !inFRR && status == "published" {
			if _, err := tx.Exec("UPDATE routes_table SET status='candidate', miss_count=0 WHERE ip=?", routeKey); err == nil {
				demotedToCandidate++
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		log.Printf("[WARN] OSPF reconcile row err: %v", err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[WARN] OSPF reconcile commit failed: %v", err)
		return
	}
	if updatedToPublished > 0 || demotedToCandidate > 0 || totalPublished != len(frrRoutes) {
		log.Printf("[OSPF] reconcile DB<->FRR: frr_tagged=%d db_published(before)=%d promoted=%d demoted=%d", len(frrRoutes), totalPublished, updatedToPublished, demotedToCandidate)
	}
}

func applyOspfDeleteBatch(toDel []string) bool {
	if len(toDel) == 0 {
		return false
	}
	var buf bytes.Buffer
	for _, ip := range toDel {
		addOspfLog("[DEL] " + ip + " (Miss count >= 3)")
		routeStr := formatRouteCIDR(ip)
		if routeStr == "" {
			continue
		}
		buf.WriteString(fmt.Sprintf("no ip route %s 127.0.0.1 tag 100\n", routeStr))
	}
	out, err := runVtyshConfigBatch(buf.String())
	if err != nil {
		log.Printf("[FRR] DEL batch=%d apply_failed: %v, out=%q", len(toDel), err, strings.TrimSpace(out))
		return false
	}
	tx, _ := db.Begin()
	for _, ip := range toDel {
		tx.Exec("DELETE FROM routes_table WHERE ip=?", ip)
	}
	tx.Commit()
	log.Printf("[FRR] DEL batch=%d applied via vtysh", len(toDel))
	return true
}

func applyOspfAddBatch(toAdd []string) bool {
	if len(toAdd) == 0 {
		return false
	}
	var buf bytes.Buffer
	for _, ip := range toAdd {
		addOspfLog("[ADD] " + ip + " to published_set")
		routeStr := formatRouteCIDR(ip)
		if routeStr == "" {
			continue
		}
		buf.WriteString(fmt.Sprintf("ip route %s 127.0.0.1 tag 100\n", routeStr))
	}
	out, err := runVtyshConfigBatch(buf.String())
	if err != nil {
		log.Printf("[FRR] ADD batch=%d apply_failed: %v, out=%q", len(toAdd), err, strings.TrimSpace(out))
		return false
	}
	tx, _ := db.Begin()
	for _, ip := range toAdd {
		tx.Exec("UPDATE routes_table SET status='published', last_seen=datetime('now'), miss_count=0 WHERE ip=?", ip)
	}
	tx.Commit()
	log.Printf("[FRR] ADD batch=%d applied via vtysh", len(toAdd))
	return true
}

func parseDatFile(filename string) []string {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil
	}

	tags := make(map[string]bool)
	idx := 0
	for idx < len(data) {
		if data[idx] == 0x0A {
			idx++
			msgLen, shift := 0, 0
			for {
				if idx >= len(data) {
					break
				}
				b := data[idx]
				idx++
				msgLen |= (int(b&0x7F) << shift)
				if (b & 0x80) == 0 {
					break
				}
				shift += 7
			}

			endIdx := idx + msgLen
			if idx < endIdx && idx < len(data) && data[idx] == 0x0A {
				idx++
				strLen, shift := 0, 0
				for {
					if idx >= len(data) {
						break
					}
					b := data[idx]
					idx++
					strLen |= (int(b&0x7F) << shift)
					if (b & 0x80) == 0 {
						break
					}
					shift += 7
				}

				if idx+strLen <= endIdx && idx+strLen <= len(data) && strLen > 0 && strLen < 50 {
					tag := string(data[idx : idx+strLen])
					tag = strings.ToLower(tag)
					valid := true
					for _, c := range tag {
						if c < 32 || c > 126 || c == ' ' {
							valid = false
							break
						}
					}
					if valid {
						tags[tag] = true
					}
				}
			}
			idx = endIdx
		} else {
			idx++
		}
	}

	var res []string
	for k := range tags {
		res = append(res, k)
	}
	sort.Strings(res)
	return res
}

func initDB() {
	var err error
	db, err = sql.Open("sqlite3", getPath("config", "proxygw.db"))
	if err != nil {
		log.Fatal(err)
	}

	// Enable WAL mode for high concurrency
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")
	db.Exec("PRAGMA busy_timeout=5000;")

	tables := []string{

		"CREATE TABLE IF NOT EXISTS remote_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, type TEXT, ssh_host TEXT, ssh_port INTEGER, ssh_user TEXT, ssh_auth_type TEXT, ssh_credential TEXT, ssh_host_key TEXT, region TEXT, status TEXT, remark TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);",
		"CREATE TABLE IF NOT EXISTS remote_node_wg (node_id INTEGER PRIMARY KEY, server_priv TEXT, server_pub TEXT, client_priv TEXT, client_pub TEXT, endpoint TEXT, port INTEGER, tunnel_addr TEXT, client_addr TEXT);",
		"CREATE TABLE IF NOT EXISTS remote_node_vless (node_id INTEGER PRIMARY KEY, uuid TEXT, reality_priv TEXT, reality_pub TEXT, short_id TEXT, server_name TEXT, dest TEXT, port INTEGER, share_link TEXT);",
		"CREATE TABLE IF NOT EXISTS remote_node_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id INTEGER, action TEXT, status TEXT, log_text TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);",

		`CREATE TABLE IF NOT EXISTS routes_table (
			ip TEXT PRIMARY KEY, domain TEXT, source TEXT,
			first_seen DATETIME, last_seen DATETIME, ttl INTEGER, status TEXT, miss_count INTEGER DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, grp TEXT, type TEXT, address TEXT, port INTEGER, uuid TEXT, active BOOLEAN DEFAULT 1, ping INTEGER DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT
		);`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);`,
		`CREATE TABLE IF NOT EXISTS lan_acls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT,
			value TEXT,
			policy TEXT,
			remark TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
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
	for _, t := range tables {
		if _, err := db.Exec(t); err != nil {
			log.Fatalf("[FATAL] failed to create table: %v", err)
		}
	}

	if _, err := db.Exec("ALTER TABLE nodes ADD COLUMN params TEXT DEFAULT '{}'"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}

	if _, err := db.Exec("ALTER TABLE remote_nodes ADD COLUMN ssh_host_key TEXT DEFAULT ''"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE nodes ADD COLUMN ping INTEGER DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE routes_table ADD COLUMN miss_count INTEGER DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}

	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('mode', 'B')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_local', '119.29.29.29,223.5.5.5')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_remote', '1.1.1.1,8.8.8.8')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_lazy', 'true')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_mode', 'smart')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_enabled', 'true')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_time', '04:00')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_schedule_type', 'daily')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_weekday', '1')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_monthday', '1')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('ospf_push_batch_limit', '500')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('ospf_push_interval_seconds', '10')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('ospf_resolve_workers', '16')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('lan_default_policy', 'proxy')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT count(*) FROM rules").Scan(&count); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] SELECT count(*) FROM rules err: %v", err)
	}
	if count == 0 {
		db.Exec("INSERT INTO rules (type, value, policy) VALUES ('geosite', 'cn', 'direct')")
		db.Exec("INSERT INTO rules (type, value, policy) VALUES ('geosite', 'category-ads-all', 'block')")
		db.Exec("INSERT INTO rules (type, value, policy) VALUES ('geolocation', '!cn', 'proxy')")
	}

	purgeDirtyRoutesTable()
	db.Exec("UPDATE routes_table SET status='candidate' WHERE status='published'")

	ensurePasswordInitialized()
}

func purgeDirtyRoutesTable() {
	rows, err := db.Query("SELECT ip FROM routes_table")
	if err != nil {
		log.Printf("[WARN] purge dirty routes query failed: %v", err)
		return
	}
	defer rows.Close()

	var dirty []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			continue
		}
		if _, ok := normalizeRouteKey(ip); !ok {
			dirty = append(dirty, ip)
		}
	}
	if len(dirty) == 0 {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("[WARN] purge dirty routes begin tx failed: %v", err)
		return
	}
	for _, ip := range dirty {
		if _, err := tx.Exec("DELETE FROM routes_table WHERE ip=?", ip); err != nil {
			_ = tx.Rollback()
			log.Printf("[WARN] purge dirty route delete failed for %q: %v", ip, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[WARN] purge dirty routes commit failed: %v", err)
		return
	}
	log.Printf("[OSPF] purged %d dirty routes from routes_table", len(dirty))
}

func ensurePasswordInitialized() {
	var pwdHash, legacyPwd string
	err := db.QueryRow("SELECT value FROM settings WHERE key='password_hash'").Scan(&pwdHash)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] get password_hash err: %v", err)
	}
	err = db.QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&legacyPwd)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] get legacy pwd err: %v", err)
	}
	if strings.TrimSpace(pwdHash) != "" || strings.TrimSpace(legacyPwd) != "" {
		return
	}

	bootstrap := strings.TrimSpace(os.Getenv("PROXYGW_BOOTSTRAP_PASSWORD"))
	generated := false
	if bootstrap == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err == nil {
			bootstrap = fmt.Sprintf("%x", b)
			generated = true
		}
	}
	if strings.TrimSpace(bootstrap) == "" {
		log.Println("[SECURITY] password bootstrap failed: empty bootstrap password")
		return
	}

	hash, err := hashPassword(bootstrap)
	if err != nil {
		log.Printf("[SECURITY] password bootstrap hash failed: %v", err)
		return
	}
	if _, err = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('password_hash', ?)", hash); err != nil {
		log.Printf("[SECURITY] password bootstrap db write failed: %v", err)
		return
	}

	if generated {
		bootstrapPath := getPath("config", "bootstrap_password.txt")
		if err := os.WriteFile(bootstrapPath, []byte(bootstrap+"\n"), 0600); err != nil {
			log.Printf("[SECURITY] initialized random bootstrap password (save failed: %v)", err)
		} else {
			log.Printf("[SECURITY] initialized random bootstrap password, saved to %s (change it immediately)", bootstrapPath)
		}
	} else {
		log.Println("[SECURITY] initialized password from PROXYGW_BOOTSTRAP_PASSWORD")
	}
}

func ospfController() {
	var lastUpdate time.Time
	var lastReconcile time.Time

	for {
		time.Sleep(2 * time.Second)
		settings := getOspfControllerSettings()
		coolingTime := time.Duration(settings.PushIntervalSeconds) * time.Second
		var mode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] SELECT value FROM settings WHERE key='mode' err: %v", err)
		}
		if mode != "C" && mode != "B" {
			db.Exec("UPDATE routes_table SET status='candidate' WHERE status='published'")
		}
		if mode != "C" && mode != "B" {
			continue
		}
		if lastReconcile.IsZero() || time.Since(lastReconcile) >= 15*time.Second {
			reconcilePublishedRoutesWithFRR()
			lastReconcile = time.Now()
		}

		if time.Since(lastUpdate) < coolingTime {
			continue
		}
		updated := false

		db.Exec("UPDATE routes_table SET miss_count = miss_count + 1 WHERE status='published' AND datetime(last_seen, '+' || ttl || ' seconds') < datetime('now')")

		var toDel []string
		rowsDel, err := db.Query("SELECT ip FROM routes_table WHERE status='published' AND miss_count >= 3 LIMIT ?", settings.PushBatchLimit)
		if err == nil {
			for rowsDel.Next() {
				var ip string
				if err := rowsDel.Scan(&ip); err == nil {
					toDel = append(toDel, ip)
				}
			}
			if err := rowsDel.Err(); err != nil {
				log.Printf("[WARN] rowsDel err: %v", err)
			}
			rowsDel.Close()
		} else {
			log.Printf("[WARN] query rowsDel err: %v", err)
		}

		log.Printf("[DEBUG] toDel len = %d", len(toDel))
		if applyOspfDeleteBatch(toDel) {
			updated = true
		}

		var toAdd []string
		rowsAdd, err := db.Query("SELECT ip FROM routes_table WHERE status='candidate' AND first_seen <= datetime('now', '-60 seconds') LIMIT ?", settings.PushBatchLimit)
		if err == nil {
			for rowsAdd.Next() {
				var ip string
				if err := rowsAdd.Scan(&ip); err == nil {
					toAdd = append(toAdd, ip)
				}
			}
			if err := rowsAdd.Err(); err != nil {
				log.Printf("[WARN] rowsAdd err: %v", err)
			}
			rowsAdd.Close()
		} else {
			log.Printf("[WARN] query rowsAdd err: %v", err)
		}

		if applyOspfAddBatch(toAdd) {
			updated = true
		}

		if updated {
			lastUpdate = time.Now()
		}
	}
}

var cronUpdateChan = make(chan struct{}, 1)

type cronScheduleSettings struct {
	Enabled      bool
	Time         string
	ScheduleType string
	Weekday      int
	Monthday     int
}

func normalizeCronScheduleType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "weekly":
		return "weekly"
	case "monthly":
		return "monthly"
	default:
		return "daily"
	}
}

func clampCronWeekday(v int) int {
	if v < 1 {
		return 1
	}
	if v > 7 {
		return 7
	}
	return v
}

func clampCronMonthday(v int) int {
	if v < 1 {
		return 1
	}
	if v > 31 {
		return 31
	}
	return v
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.Local).Day()
}

func calcNextCronRun(now time.Time, scheduleType string, hour int, minute int, weekday int, monthday int) time.Time {
	scheduleType = normalizeCronScheduleType(scheduleType)
	base := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	switch scheduleType {
	case "weekly":
		today := int(now.Weekday())
		if today == 0 {
			today = 7
		}
		delta := weekday - today
		if delta < 0 || (delta == 0 && !base.After(now)) {
			delta += 7
		}
		return base.AddDate(0, 0, delta)
	case "monthly":
		day := monthday
		maxCur := daysInMonth(now.Year(), now.Month())
		if day > maxCur {
			day = maxCur
		}
		next := time.Date(now.Year(), now.Month(), day, hour, minute, 0, 0, now.Location())
		if !next.After(now) {
			nextMonth := now.Month() + 1
			nextYear := now.Year()
			if nextMonth > 12 {
				nextMonth = 1
				nextYear++
			}
			maxNext := daysInMonth(nextYear, nextMonth)
			if monthday > maxNext {
				day = maxNext
			} else {
				day = monthday
			}
			next = time.Date(nextYear, nextMonth, day, hour, minute, 0, 0, now.Location())
		}
		return next
	default:
		if !base.After(now) {
			return base.Add(24 * time.Hour)
		}
		return base
	}
}

func loadCronScheduleSettings() cronScheduleSettings {
	cfg := cronScheduleSettings{Enabled: false, Time: "04:00", ScheduleType: "daily", Weekday: 1, Monthday: 1}
	var enabled, cronTime, scheduleType, weekday, monthday string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='cron_enabled'").Scan(&enabled); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_enabled check err: %v", err)
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='cron_time'").Scan(&cronTime); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_time check err: %v", err)
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='cron_schedule_type'").Scan(&scheduleType); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_schedule_type check err: %v", err)
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='cron_weekday'").Scan(&weekday); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_weekday check err: %v", err)
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='cron_monthday'").Scan(&monthday); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_monthday check err: %v", err)
	}
	cfg.Enabled = strings.TrimSpace(enabled) == "true"
	if t := strings.TrimSpace(cronTime); t != "" {
		cfg.Time = t
	}
	cfg.ScheduleType = normalizeCronScheduleType(scheduleType)
	if n, err := strconv.Atoi(strings.TrimSpace(weekday)); err == nil {
		cfg.Weekday = clampCronWeekday(n)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(monthday)); err == nil {
		cfg.Monthday = clampCronMonthday(n)
	}
	if _, err := time.Parse("15:04", cfg.Time); err != nil {
		cfg.Time = "04:00"
	}
	return cfg
}

func triggerCronReload() {
	select {
	case cronUpdateChan <- struct{}{}:
	default:
	}
}

func cronUpdater() {
	for {
		cfg := loadCronScheduleSettings()
		t, _ := time.Parse("15:04", cfg.Time)
		now := time.Now()
		next := calcNextCronRun(now, cfg.ScheduleType, t.Hour(), t.Minute(), cfg.Weekday, cfg.Monthday)
		sleepDuration := next.Sub(now)
		if sleepDuration < time.Second {
			sleepDuration = time.Second
		}

		timer := time.NewTimer(sleepDuration)
		select {
		case <-timer.C:
			if cfg.Enabled {
				log.Printf("Running cron update for GeoData... (type=%s time=%s weekday=%d monthday=%d)", cfg.ScheduleType, cfg.Time, cfg.Weekday, cfg.Monthday)
				if err := updateGeodata(); err != nil {
					log.Printf("[SECURITY] Cron update failed: %v", err)
				} else {
					log.Println("Cron update for GeoData completed securely.")
				}
			}
		case <-cronUpdateChan:
			timer.Stop()
			log.Println("Cron configuration updated, recalculating next run...")
		}
	}
}

var (
	applyTimer *time.Timer
	applyMutex sync.Mutex
)

var pendingMosdnsApply bool

func scheduleApply() {
	scheduleApplyWithMosdns(false)
}

func scheduleApplyWithMosdns(needMosdns bool) {
	applyMutex.Lock()
	defer applyMutex.Unlock()
	if needMosdns {
		pendingMosdnsApply = true
	}
	if applyTimer != nil {
		applyTimer.Stop()
	}
	applyTimer = time.AfterFunc(3*time.Second, func() {
		applyMutex.Lock()
		runMosdns := pendingMosdnsApply
		pendingMosdnsApply = false
		applyMutex.Unlock()

		if runMosdns {
			if err := applyMosdnsConfig(); err != nil {
				log.Printf("[ERROR] apply mosdns failed: %v", err)
			}
		}
		if err := applyXrayConfig(); err != nil {
			log.Printf("[ERROR] apply xray failed: %v", err)
		}
	})
}

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
			items = append(items, fmt.Sprintf(`{ addr: "%s" }`, clean))
		}
	}
	if len(items) == 0 {
		if useSocks {
			return `[{ addr: "1.1.1.1", socks5: "127.0.0.1:10808" }, { addr: "8.8.8.8", socks5: "127.0.0.1:10808" }]`
		}
		return `[{ addr: "119.29.29.29" }, { addr: "223.5.5.5" }]`
	}
	return "[" + strings.Join(items, ", ") + "]"
}

func applyMosdnsConfig() error {
	applyMutex.Lock()
	defer applyMutex.Unlock()
	log.Println("[AUDIT] Applying Mosdns Config")
	var local, remote, lazyStr string

	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_local'").Scan(&local); err != nil {
		local = "119.29.29.29,223.5.5.5"
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_remote'").Scan(&remote); err != nil {
		remote = "1.1.1.1,8.8.8.8"
	}
	if err := db.QueryRow("SELECT value FROM settings WHERE key='dns_lazy'").Scan(&lazyStr); err != nil {
		lazyStr = "true"
	}

	var proxyDomains []string
	dRows, err := db.Query("SELECT value FROM rules WHERE type='domain' AND policy LIKE 'proxy%'")
	if err == nil {
		for dRows.Next() {
			var d string
			if err := dRows.Scan(&d); err == nil {
				proxyDomains = append(proxyDomains, d)
			}
		}
		if err := dRows.Err(); err != nil {
			log.Printf("[WARN] dRows err: %v", err)
		}
		dRows.Close()
	} else {
		log.Printf("[WARN] query dRows err: %v", err)
	}
	if err := os.WriteFile(getPath("core", "mosdns", "proxy_domains.txt"), []byte(strings.Join(proxyDomains, "\n")), 0644); err != nil {
		return fmt.Errorf("failed to write proxy_domains.txt: %v", err)
	}

	var mode string
	db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	config := renderMosdnsConfig(local, remote, lazyStr == "true", mode)

	if err := os.WriteFile(getPath("core", "mosdns", "config.yaml"), []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write mosdns config.yaml: %v", err)
	}
	err = exec.Command("systemctl", "restart", "mosdns").Run()
	if err != nil {
		return err
	}
	return nil
}

func applyXrayConfig() error {
	return applyXrayConfigInternal(true)
}

func writeXrayConfigOnly() error {
	return applyXrayConfigInternal(false)
}

func applyXrayConfigInternal(restart bool) error {
	applyMutex.Lock()
	defer applyMutex.Unlock()
	if restart {
		log.Println("[AUDIT] Applying Xray Config")
	} else {
		log.Println("[AUDIT] Updating Xray Config File (no restart)")
	}

	var mode string
	db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	config := buildBaseXrayConfig(mode)

	rows, err := db.Query("SELECT id, name, type, address, port, uuid, COALESCE(params, '{}') FROM nodes WHERE active=1")
	if err != nil {
		return err
	}
	defer rows.Close()
	var defNodeStr string
	db.QueryRow("SELECT value FROM settings WHERE key='default_node_id'").Scan(&defNodeStr)
	defaultNodeId, _ := strconv.Atoi(defNodeStr)

	var activeIds []int
	var proxyTags []string
	for rows.Next() {
		var name, ntype, address, uuid, paramsStr string
		var port, id int
		if err := rows.Scan(&id, &name, &ntype, &address, &port, &uuid, &paramsStr); err != nil {
			continue
		}

		activeIds = append(activeIds, id)

		ntypeLow := strings.ToLower(ntype)

		var params map[string]interface{}
		json.Unmarshal([]byte(paramsStr), &params)
		if params == nil {
			params = make(map[string]interface{})
		}

		if uuid != "" {
			if params["settings"] == nil {
				if ntypeLow == "vmess" {
					params["settings"] = map[string]interface{}{"vnext": []map[string]interface{}{{"users": []map[string]interface{}{{"id": uuid, "alterId": 0}}}}}
				} else if ntypeLow == "vless" {
					user := map[string]interface{}{"id": uuid, "encryption": "none"}
					if flow, ok := params["flow"].(string); ok && flow != "" {
						user["flow"] = flow
					}
					params["settings"] = map[string]interface{}{"vnext": []map[string]interface{}{{"users": []map[string]interface{}{user}}}}
				} else if ntypeLow == "trojan" {
					params["settings"] = map[string]interface{}{"servers": []map[string]interface{}{{"password": uuid}}}
				} else if ntypeLow == "shadowsocks" || ntypeLow == "ss" {
					method := "aes-256-gcm"
					if m, ok := params["method"].(string); ok && m != "" {
						method = m
					}
					params["settings"] = map[string]interface{}{"servers": []map[string]interface{}{{"password": uuid, "method": method}}}
				}
			}
			if ntypeLow == "vless" || ntypeLow == "trojan" {
				if params["type"] != nil && params["streamSettings"] == nil {
					ss := map[string]interface{}{"network": params["type"]}
					if params["security"] != nil {
						ss["security"] = params["security"]
					}
					if params["security"] == "reality" {
						ss["realitySettings"] = map[string]interface{}{
							"fingerprint": params["fp"], "serverName": params["sni"],
							"publicKey": params["pbk"], "shortId": params["sid"], "spiderX": "/",
						}
					} else if params["security"] == "tls" {
						ss["tlsSettings"] = map[string]interface{}{"serverName": params["sni"]}
					}
					params["streamSettings"] = ss
				}
			}
		}

		outbound := params
		outbound["protocol"] = ntypeLow
		outbound["tag"] = fmt.Sprintf("proxy-%d-out", id)

		if settings, ok := outbound["settings"].(map[string]interface{}); ok {
			if vnext, ok := settings["vnext"].([]interface{}); ok && len(vnext) > 0 {
				if node, ok := vnext[0].(map[string]interface{}); ok {
					node["address"] = address
					node["port"] = port
				}
			} else if vnext, ok := settings["vnext"].([]map[string]interface{}); ok && len(vnext) > 0 {
				vnext[0]["address"] = address
				vnext[0]["port"] = port
			}
			if servers, ok := settings["servers"].([]interface{}); ok && len(servers) > 0 {
				if server, ok := servers[0].(map[string]interface{}); ok {
					server["address"] = address
					server["port"] = port
				}
			} else if servers, ok := settings["servers"].([]map[string]interface{}); ok && len(servers) > 0 {
				servers[0]["address"] = address
				servers[0]["port"] = port
			}
		} else if ntypeLow != "custom" && ntypeLow != "wireguard" {
			if ntypeLow == "vmess" || ntypeLow == "vless" {
				outbound["settings"] = map[string]interface{}{
					"vnext": []map[string]interface{}{{"address": address, "port": port}},
				}
			} else {
				outbound["settings"] = map[string]interface{}{
					"servers": []map[string]interface{}{{"address": address, "port": port}},
				}
			}
		}

		if outbound != nil {
			if ss, ok := outbound["streamSettings"].(map[string]interface{}); ok {
				ss["sockopt"] = map[string]interface{}{"mark": 2}
			} else {
				outbound["streamSettings"] = map[string]interface{}{"sockopt": map[string]interface{}{"mark": 2}}
			}
			config["outbounds"] = append(config["outbounds"].([]map[string]interface{}), outbound)
			proxyTags = append(proxyTags, fmt.Sprintf("proxy-%d-out", id))
		}
	}

	rRows, err := db.Query("SELECT id, type, value, policy FROM rules")
	if err != nil {
		log.Printf("[WARN] routing rules query err: %v", err)
		return err
	}
	defer rRows.Close()
	rules := config["routing"].(map[string]interface{})["rules"].([]map[string]interface{})
	for rRows.Next() {
		var id int
		var rtype, value, policy string
		if err := rRows.Scan(&id, &rtype, &value, &policy); err != nil {
			continue
		}
		rule := map[string]interface{}{"type": "field", "ruleTag": fmt.Sprintf("db-rule-%d", id)}

		if policy == "direct" || policy == "block" {
			rule["outboundTag"] = policy
		} else if policy == "proxy" {
			rule["balancerTag"] = "proxy-balancer"
		} else if strings.HasPrefix(policy, "proxy-") {
			// Single node binding (e.g. proxy-1)
			rule["outboundTag"] = policy + "-out"
		} else if strings.HasPrefix(policy, "ha-") {
			// HA Mode (e.g. ha-1-2)
			parts := strings.Split(strings.TrimPrefix(policy, "ha-"), "-")
			if len(parts) == 2 {
				rule["balancerTag"] = "bal-" + policy
			} else {
				rule["outboundTag"] = "proxy"
			}
		} else {
			rule["outboundTag"] = policy
		}

		if rtype == "geosite" || rtype == "domain" {
			rule["domain"] = []string{rtype + ":" + value}
			rules = append(rules, rule)
		} else if rtype == "geoip" || rtype == "ip" || rtype == "geolocation" {
			if rtype == "ip" {
				rule["ip"] = []string{value}
				rules = append(rules, rule)
			} else if rtype == "geolocation" {
				if strings.EqualFold(value, "private") {
					rule["ip"] = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"}
				} else {
					rule["ip"] = []string{"geoip:" + value}
				}
				rules = append(rules, rule)
			} else {
				if strings.EqualFold(value, "private") {
					rule["ip"] = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"}
				} else {
					rule["ip"] = []string{"geoip:" + value}
				}
				rules = append(rules, rule)
			}
		} else {
			// Skip invalid rules without domain/ip to prevent Xray crash
			continue
		}
	}
	if err := rRows.Err(); err != nil {
		log.Printf("[WARN] rRows err: %v", err)
	}
	if err := rRows.Err(); err != nil {
		log.Printf("[WARN] rRows err: %v", err)
	}

	if len(proxyTags) > 0 {
		config["observatory"] = map[string]interface{}{
			"subjectSelector": []string{"proxy-"},
			"probeURL":        "http://cp.cloudflare.com/",
			"probeInterval":   "30s",
		}
		routing := config["routing"].(map[string]interface{})
		routing["balancers"] = []map[string]interface{}{
			{
				"tag":      "proxy-balancer",
				"selector": []string{"proxy-"},
				"strategy": map[string]interface{}{
					"type": "leastPing",
				},
			},
		}

		customBalancers := make(map[string]map[string]interface{})
		actualDefault := 0
		if len(activeIds) == 1 {
			actualDefault = activeIds[0]
		} else {
			for _, aid := range activeIds {
				if aid == defaultNodeId {
					actualDefault = aid
					break
				}
			}
		}

		for _, r := range rules {
			if r["balancerTag"] == "proxy-balancer" {
				if actualDefault > 0 {
					delete(r, "balancerTag")
					r["outboundTag"] = fmt.Sprintf("proxy-%d-out", actualDefault)
				} else {
					delete(r, "outboundTag")
					r["balancerTag"] = "proxy-balancer"
				}
			}
			if bTag, ok := r["balancerTag"].(string); ok && strings.HasPrefix(bTag, "bal-ha-") {
				parts := strings.Split(strings.TrimPrefix(bTag, "bal-ha-"), "-")
				if len(parts) == 2 {
					customBalancers[bTag] = map[string]interface{}{
						"tag":         bTag,
						"selector":    []string{"proxy-" + parts[0] + "-out"},
						"fallbackTag": "proxy-" + parts[1] + "-out",
					}
				}
			}
		}

		balancers := routing["balancers"].([]map[string]interface{})
		for _, cb := range customBalancers {
			balancers = append(balancers, cb)
		}
		routing["balancers"] = balancers
	}

	validOutbounds := map[string]struct{}{}
	for _, ob := range config["outbounds"].([]map[string]interface{}) {
		if tag, ok := ob["tag"].(string); ok && tag != "" {
			validOutbounds[tag] = struct{}{}
		}
	}
	validBalancers := map[string]struct{}{}
	if routing, ok := config["routing"].(map[string]interface{}); ok {
		if balancers, ok := routing["balancers"].([]map[string]interface{}); ok {
			for _, b := range balancers {
				if tag, ok := b["tag"].(string); ok && tag != "" {
					validBalancers[tag] = struct{}{}
				}
			}
		}
	}
	for _, r := range rules {
		if ot, ok := r["outboundTag"].(string); ok {
			if strings.HasPrefix(ot, "proxy-") || ot == "proxy" {
				if _, exists := validOutbounds[ot]; !exists {
					delete(r, "balancerTag")
					r["outboundTag"] = "direct"
				}
			}
		}
		if bt, ok := r["balancerTag"].(string); ok {
			if _, exists := validBalancers[bt]; !exists {
				delete(r, "balancerTag")
				r["outboundTag"] = "direct"
			}
		}
	}

	config["routing"].(map[string]interface{})["rules"] = rules

	scheduleStaticRouteSync(mode)

	configData, _ := json.MarshalIndent(config, "", "  ")

	tempTestPath := "/tmp/proxygw_xray_test.json"
	os.WriteFile(tempTestPath, configData, 0644)
	if err := exec.Command(getPath("core", "xray", "xray"), "-test", "-config", tempTestPath).Run(); err != nil {
		log.Printf("[ERROR] Xray config validation failed: %v. Config rejected.", err)
		return fmt.Errorf("xray config validation failed, check node parameters")
	}

	if err := os.WriteFile(getPath("core", "xray", "config.json"), configData, 0644); err != nil {
		return fmt.Errorf("failed to write xray config.json: %v", err)
	}
	if !restart {
		return nil
	}
	return exec.Command("systemctl", "restart", "xray").Run()
}

func getPrimaryLANIPAndSubnet() (string, string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}

	isPrivateIPv4 := func(ip net.IP) bool {
		return ip.IsPrivate() && ip.To4() != nil
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || !isPrivateIPv4(ip) {
				continue
			}
			network := ip.Mask(ipnet.Mask)
			maskSize, _ := ipnet.Mask.Size()
			return ip.String(), fmt.Sprintf("%s/%d", network.String(), maskSize)
		}
	}

	return "", ""
}

func syncFRRConfig() {
	var mode string
	if db != nil {
		db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	}

	if mode == "A" || mode == "" {
		exec.Command("vtysh", "-c", "conf t", "-c", "no route-map OSPF-EXPORT permit 10").Run()
		exec.Command("systemctl", "stop", "frr").Run()
		return
	}

	ip, subnet := getPrimaryLANIPAndSubnet()
	if ip == "" || subnet == "" {
		return
	}

	var newContent string
	if mode == "B" {
		newContent = fmt.Sprintf(`! FRR OSPF Config (Generated)
ip route 198.18.0.0/16 127.0.0.1 tag 100
router ospf
 ospf router-id %s
 redistribute static route-map OSPF-EXPORT
 network %s area 0
!
route-map OSPF-EXPORT permit 10
 match tag 100
!`, ip, subnet)
	} else if mode == "C" {
		newContent = fmt.Sprintf(`! FRR OSPF Config (Generated)
router ospf
 ospf router-id %s
 redistribute static route-map OSPF-EXPORT
 network %s area 0
!
route-map OSPF-EXPORT permit 10
 match tag 100
!`, ip, subnet)
	}

	b, _ := os.ReadFile("/etc/frr/frr.conf")
	content_frr := string(b)

	if newContent != content_frr {
		log.Printf("[OSPF] Auto-updating FRR config: mode=%s, router-id=%s, network=%s", mode, ip, subnet)
		os.WriteFile(getPath("core", "frr", "frr.conf"), []byte(newContent), 0644)
		os.WriteFile("/etc/frr/frr.conf", []byte(newContent), 0644)
		exec.Command("sed", "-i", "s/ospfd=no/ospfd=yes/", "/etc/frr/daemons").Run()
		exec.Command("systemctl", "restart", "frr").Run()
		db.Exec("UPDATE routes_table SET status='candidate' WHERE status='published'")
	}
}

func scheduleStaticRouteSync(mode string) {
	if mode != "B" && mode != "C" {
		return
	}
	staticRouteSyncMu.Lock()
	staticRouteSyncPending = true
	if staticRouteSyncRunning {
		staticRouteSyncMu.Unlock()
		return
	}
	staticRouteSyncRunning = true
	staticRouteSyncMu.Unlock()

	go func() {
		for {
			staticRouteSyncMu.Lock()
			staticRouteSyncPending = false
			staticRouteSyncMu.Unlock()

			currentMode := mode
			if db != nil {
				var dbMode string
				if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&dbMode); err == nil && dbMode != "" {
					currentMode = dbMode
				}
			}
			if currentMode == "B" || currentMode == "C" {
				syncStaticRoutesToOSPFFunc(currentMode)
			}

			staticRouteSyncMu.Lock()
			if !staticRouteSyncPending {
				staticRouteSyncRunning = false
				staticRouteSyncMu.Unlock()
				return
			}
			staticRouteSyncMu.Unlock()
		}
	}()
}

func syncStaticRoutesToOSPF(mode string) {
	protected := collectProtectedRouteKeys()
	staticRoutes, conflicts := collectStaticRoutesForMode(mode, protected)
	staticRoutes, pruned := pruneStaticRoutesPreferBroad(staticRoutes)
	if pruned > 0 {
		log.Printf("[OSPF] pruned %d narrower routes covered by broader prefixes", pruned)
	}
	if len(conflicts) > 0 {
		log.Printf("[OSPF] skipped %d protected endpoint routes to avoid loop, samples=%s", len(conflicts), sampleRouteKeys(conflicts, 10))
	}

	var toDelete []string
	oldRows, err := db.Query("SELECT ip FROM routes_table WHERE source='static'")
	if err == nil {
		for oldRows.Next() {
			var ip string
			if err := oldRows.Scan(&ip); err == nil {
				if _, ok := staticRoutes[ip]; !ok {
					toDelete = append(toDelete, ip)
				}
			}
		}
		oldRows.Close()
	}

	txSync, _ := db.Begin()
	for _, ipStr := range toDelete {
		txSync.Exec("UPDATE routes_table SET miss_count=99, ttl=0, last_seen=datetime('now', '-1 hour') WHERE ip=?", ipStr)
	}

	for ipStr, state := range staticRoutes {
		domain := state.domain
		if domain == "" {
			domain = "static_rule"
		}
		txSync.Exec("INSERT INTO routes_table (ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES (?, ?, 'static', datetime('now', '-61 seconds'), datetime('now'), ?, 'candidate', 0) ON CONFLICT(ip) DO UPDATE SET domain=excluded.domain, source='static', ttl=excluded.ttl, miss_count=0, last_seen=datetime('now')", ipStr, domain, state.ttl)
	}
	txSync.Commit()
}

func domainIPUpdater() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		var mode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
			mode = "A"
		}
		if mode == "B" || mode == "C" {
			// Periodically sync to catch DNS/CDN IP changes
			syncStaticRoutesToOSPF(mode)
		}
	}
}

func main() {
	initDB()
	go startTrafficMonitor()
	syncFRRConfig()
	go ospfController()
	go cronUpdater()
	go domainIPUpdater()
	applyMosdnsConfig()
	applyXrayConfig()

	// Init connection tracking
	os.MkdirAll("/run/proxygw", 0755)
	StartConnectionTracker()

	exec.Command("sh", "-c", "ip rule del fwmark 1 lookup tproxy 2>/dev/null || true; ip rule add fwmark 1 lookup tproxy").Run()
	exec.Command("sh", "-c", "ip route del local default dev lo table tproxy 2>/dev/null || true; ip route add local default dev lo table tproxy").Run()
	exec.Command("sh", "-c", "ip -6 rule del fwmark 1 lookup tproxy 2>/dev/null || true; ip -6 rule add fwmark 1 lookup tproxy").Run()
	exec.Command("sh", "-c", "ip -6 route del local default dev lo table tproxy 2>/dev/null || true; ip -6 route add local default dev lo table tproxy").Run()

	r := gin.Default()
	registerAPIRoutes(r)

	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/ui") {
			c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
		}
		c.Next()
	})
	r.Static("/ui", getPath("frontend", "dist"))
	r.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/ui/") })

	log.Println("ProxyGW backend starting on :80")
	r.Run(":80")
}
