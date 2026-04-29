package main

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	defaultOspfReconcileInterval   = 45 * time.Second
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
	res := sysCmd.runCombinedOutput("vtysh", "-f", tmpFile)
	return string(res.Output), res.Err
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

	staticRows, err := db.Query("SELECT value FROM rules WHERE type='ip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
	if err == nil {
		for staticRows.Next() {
			var ip string
			if err := staticRows.Scan(&ip); err == nil {
				addRoute(ip, 999999999, "static_rule")
			}
		}
		staticRows.Close()
	}

	geoipRows, err := db.Query("SELECT value FROM rules WHERE type='geoip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
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

	if mode == "C" {
		geositeRows, err := db.Query("SELECT value, policy FROM rules WHERE type='geosite' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%' OR policy LIKE 'ha-%')")
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

		domainRows, err := db.Query("SELECT value, policy FROM rules WHERE type='domain' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%' OR policy LIKE 'ha-%')")
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

	staticRows, err := db.Query("SELECT value FROM rules WHERE type='ip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
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
	geoipRows, err := db.Query("SELECT value FROM rules WHERE type='geoip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
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

	domainRows, err := db.Query("SELECT value, policy FROM rules WHERE type='domain' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%' OR policy LIKE 'ha-%')")
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

func ospfController() {
	var lastUpdate time.Time
	var lastReconcile time.Time
	modeDemotedForNonBC := false

	for {
		time.Sleep(2 * time.Second)
		settings := getOspfControllerSettings()
		coolingTime := time.Duration(settings.PushIntervalSeconds) * time.Second
		var mode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] SELECT value FROM settings WHERE key='mode' err: %v", err)
		}
		if mode != "C" && mode != "B" {
			if !modeDemotedForNonBC {
				if _, err := db.Exec("UPDATE routes_table SET status='candidate' WHERE status='published'"); err != nil {
					log.Printf("[WARN] demote published routes to candidate failed: %v", err)
				}
				modeDemotedForNonBC = true
			}
			continue
		}
		modeDemotedForNonBC = false
		if lastReconcile.IsZero() || time.Since(lastReconcile) >= defaultOspfReconcileInterval {
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

	var mode string
	db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	proxyDomains, err := buildMosdnsProxyDomains(mode)
	if err != nil {
		return err
	}
	if err := os.WriteFile(getPath("core", "mosdns", "proxy_domains.txt"), []byte(strings.Join(proxyDomains, "\n")), 0644); err != nil {
		return fmt.Errorf("failed to write proxy_domains.txt: %v", err)
	}

	config := renderMosdnsConfig(local, remote, lazyStr == "true", mode)

	if err := os.WriteFile(getPath("core", "mosdns", "config.yaml"), []byte(config), 0644); err != nil {
		return fmt.Errorf("failed to write mosdns config.yaml: %v", err)
	}
	err = sysCmd.run("systemctl", "restart", "mosdns")
	if err != nil {
		return err
	}
	return nil
}

func getPrimaryLANIPAndSubnet() (string, string) {
	serviceIface := ""
	if db != nil {
		_ = db.QueryRow("SELECT value FROM settings WHERE key='service_iface'").Scan(&serviceIface)
		serviceIface = strings.TrimSpace(serviceIface)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}

	isPrivateIPv4 := func(ip net.IP) bool {
		return ip.IsPrivate() && ip.To4() != nil
	}

	resolveIface := func(target string) (string, string) {
		for _, iface := range ifaces {
			if target != "" && iface.Name != target {
				continue
			}
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

	if serviceIface != "" {
		if ip, subnet := resolveIface(serviceIface); ip != "" && subnet != "" {
			return ip, subnet
		}
	}
	return resolveIface("")
}

func domainIPUpdater() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		var mode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
			mode = "A"
		}
		if mode == "C" {
			// Only Mode C needs periodic domain/geosite DNS-driven OSPF materialization.
			scheduleStaticRouteSync(mode)
		}
	}
}

func main() {
	repo := NewAppRepository()
	service := NewAppService(repo)
	controller := NewAppController()

	service.Bootstrap()
	r := controller.BuildRouter()
	controller.Run(r)
}
