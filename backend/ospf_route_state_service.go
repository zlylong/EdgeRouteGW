package main

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

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
	d := getDB()
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
		if d == nil {
			return
		}
		rows, err := d.Query(query)
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
	d := getDB()
	if d == nil {
		return map[string]routeState{}, nil
	}
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

	staticRows, err := d.Query("SELECT value FROM rules WHERE type='ip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
	if err == nil {
		for staticRows.Next() {
			var ip string
			if err := staticRows.Scan(&ip); err == nil {
				addRoute(ip, 999999999, "static_rule")
			}
		}
		staticRows.Close()
	}

	geoipRows, err := d.Query("SELECT value FROM rules WHERE type='geoip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
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
		geositeRows, err := d.Query("SELECT value, policy FROM rules WHERE type='geosite' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
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

		domainRows, err := d.Query("SELECT value, policy FROM rules WHERE type='domain' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%' OR policy LIKE 'ha-%')")
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

	rows, err := getDB().Query("SELECT ip FROM routes_table WHERE status IN ('candidate','published') AND source='static'")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ip string
			if rows.Scan(&ip) == nil {
				addIfConflict(ip)
			}
		}
	}

	staticRows, err := getDB().Query("SELECT value FROM rules WHERE type='ip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
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
	geoipRows, err := getDB().Query("SELECT value FROM rules WHERE type='geoip' AND (policy LIKE 'proxy%' OR policy LIKE 'ha-%')")
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

	domainRows, err := getDB().Query("SELECT value, policy FROM rules WHERE type='domain' AND (policy LIKE 'proxy%' OR policy LIKE 'direct%' OR policy LIKE 'ha-%')")
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
