package main

import (
	

	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	minDomainCacheTTLSeconds     = 300
	maxDomainCacheTTLSeconds     = 3600
	domainFailureRetrySeconds    = 300
	staleDomainFallbackTTLSecond = 300
)

var nowFunc = time.Now
var legacyDomainCacheMigrationOnce sync.Once

var (
	legacyDomainCacheSweepMu   sync.Mutex
	legacyDomainCacheLastSweep time.Time
	routeCacheEnsureMu         sync.Mutex
	routeCacheEnsuredDB        *sql.DB
	routeGeoDataVersionCacheMu sync.Mutex
	routeGeoDataVersionCache   geoDataVersionState
)

const geoDataVersionCheckInterval = 5 * time.Second

type geoDataVersionState struct {
	version     string
	checkedAt   time.Time
	geodataVer  fileSignature
	geositeDat  fileSignature
	initialized bool
}

type fileSignature struct {
	exists bool
	size   int64
	mtime  int64
}

const domainResolveTimeout = 5 * time.Second

const (
	resolverGroupRemote = "remote"
	resolverGroupLocal  = "local"
)

var hostLookupCommand = func(domain string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), domainResolveTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "host", "-t", "A", "-v", domain).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("host lookup timeout for %q after %s", domain, domainResolveTimeout)
	}
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

var hostLookupCommandAtServer = func(domain string, server string) (string, error) {
	args := []string{"-t", "A", "-v", domain}
	if normalized, ok := normalizeDNSServerAddr(server); ok {
		args = append(args, normalized)
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainResolveTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "host", args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("host lookup timeout for %q via %q after %s", domain, server, domainResolveTimeout)
	}
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

var answerSectionARecordPattern = regexp.MustCompile(`^\S+\.\s+(\d+)\s+IN\s+A\s+((?:\d{1,3}\.){3}\d{1,3})\s*$`)

func parseHostLookupOutput(output string) ([]string, int, error) {
	lines := strings.Split(output, "\n")
	inAnswer := false
	ips := make([]string, 0)
	minTTL := 0
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ";; ANSWER SECTION:") {
			inAnswer = true
			continue
		}
		if !inAnswer {
			continue
		}
		if strings.HasPrefix(line, ";;") || strings.HasPrefix(line, "Received ") {
			break
		}
		match := answerSectionARecordPattern.FindStringSubmatch(line)
		if len(match) == 0 {
			continue
		}
		ttl, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, 0, fmt.Errorf("invalid ttl in host output: %w", err)
		}
		if minTTL == 0 || ttl < minTTL {
			minTTL = ttl
		}
		ips = append(ips, match[2])
	}
	ips = normalizeIPList(ips)
	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("no A records in host output")
	}
	return ips, minTTL, nil
}

func normalizeDNSServerAddr(server string) (string, bool) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", false
	}
	if strings.Contains(server, ":") {
		host, port, err := net.SplitHostPort(server)
		if err == nil {
			if p := strings.TrimSpace(port); p != "" && p != "53" {
				return "", false
			}
			if h := strings.TrimSpace(host); h != "" {
				return h, true
			}
		}
		if strings.Contains(server, "]") {
			return "", false
		}
	}
	return server, true
}

func parseDNSServerList(raw string) []string {
	items := strings.Split(raw, ",")
	servers := make([]string, 0, len(items))
	for _, item := range items {
		if normalized, ok := normalizeDNSServerAddr(item); ok {
			servers = append(servers, normalized)
		}
	}
	return servers
}

func normalizeResolverGroup(group string) string {
	switch strings.ToLower(strings.TrimSpace(group)) {
	case resolverGroupLocal:
		return resolverGroupLocal
	default:
		return resolverGroupRemote
	}
}

func buildDomainCacheKey(resolverGroup string, domain string) string {
	resolverGroup = normalizeResolverGroup(resolverGroup)
	return resolverGroup + ":" + domain
}

func getResolverDNSServers(resolverGroup string) []string {
	// 100% delegate to local Mosdns. No more direct external queries from OSPF engine.
	return []string{"127.0.0.1"}
}

func lookupIPv4WithDNSServer(domain string, server string, _ bool) ([]string, error) {
	serverAddr, ok := normalizeDNSServerAddr(server)
	if !ok {
		return nil, fmt.Errorf("invalid dns server %q", server)
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: domainResolveTimeout}
			return d.DialContext(ctx, "udp", net.JoinHostPort(serverAddr, "53"))
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), domainResolveTimeout)
	defer cancel()
	addrs, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		return nil, err
	}
	ips := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP == nil || addr.IP.To4() == nil {
			continue
		}
		ips = append(ips, addr.IP.String())
	}
	ips = normalizeIPList(ips)
	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records")
	}
	return ips, nil
}

var resolveDomainIPv4WithTTLViaServers = func(domain string, dnsServers []string, isRemote bool) ([]string, int, error) {
	if len(dnsServers) == 0 {
		return resolveDomainIPv4WithTTL(domain)
	}
	var firstErr error
	for _, server := range dnsServers {
		// No more OS 'host' command for OSPF expansion. 
		// We trust our resolver to query 127.0.0.1 (Mosdns).
		ips, lookupErr := lookupIPv4WithDNSServer(domain, server, isRemote)
		if lookupErr == nil {
			return ips, minDomainCacheTTLSeconds, nil
		}
		if firstErr == nil {
			firstErr = lookupErr
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("all dns servers failed")
	}
	return nil, 0, firstErr
}

var resolveDomainIPv4WithTTL = func(domain string) ([]string, int, error) {
	output, err := hostLookupCommand(domain)
	if err == nil {
		ips, ttl, parseErr := parseHostLookupOutput(output)
		if parseErr == nil {
			return ips, clampDomainCacheTTL(ttl), nil
		}
		log.Printf("[WARN] host output parse failed for %q: %v", domain, parseErr)
	} else {
		log.Printf("[WARN] host lookup failed for %q: %v", domain, err)
	}
	ips, lookupErr := geoQueryLookupIP(domain)
	if lookupErr != nil {
		if err != nil {
			return nil, 0, fmt.Errorf("host lookup failed: %w; fallback lookup failed: %v", err, lookupErr)
		}
		return nil, 0, lookupErr
	}
	return ips, minDomainCacheTTLSeconds, nil
}

func ensureRouteCacheTables() {
	if db == nil {
		return
	}

	routeCacheEnsureMu.Lock()
	if routeCacheEnsuredDB == db {
		routeCacheEnsureMu.Unlock()
		return
	}
	routeCacheEnsuredDB = db
	routeCacheEnsureMu.Unlock()

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
	legacyDomainCacheMigrationOnce.Do(migrateLegacyDomainResolveCacheKeys)
	scheduleLegacyDomainCacheSweep()
}

func migrateLegacyDomainResolveCacheKeys() {
	if db == nil {
		return
	}
	result, err := db.Exec(`
		INSERT OR IGNORE INTO domain_resolve_cache (domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver)
		SELECT 'remote:' || domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver
		FROM domain_resolve_cache
		WHERE instr(domain, ':') = 0
	`)
	if err != nil {
		log.Printf("[WARN] migrate legacy domain cache keys failed: %v", err)
		return
	}
	if n, err := result.RowsAffected(); err == nil && n > 0 {
		log.Printf("[OSPF] migrated %d legacy domain cache rows to remote:* keys", n)
	}
}

func sweepLegacyDomainResolveCacheKeys(limit int) (migrated int64, removed int64, err error) {
	if db == nil {
		return 0, 0, nil
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := db.Query("SELECT domain FROM domain_resolve_cache WHERE instr(domain, ':')=0 LIMIT ?", limit)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	legacyDomains := make([]string, 0, limit)
	for rows.Next() {
		var domain string
		if scanErr := rows.Scan(&domain); scanErr != nil {
			return migrated, removed, scanErr
		}
		legacyDomains = append(legacyDomains, domain)
	}
	if err := rows.Err(); err != nil {
		return migrated, removed, err
	}
	if len(legacyDomains) == 0 {
		return 0, 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, domain := range legacyDomains {
		ins, execErr := tx.Exec(`
			INSERT OR IGNORE INTO domain_resolve_cache (domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver)
			SELECT ?, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver
			FROM domain_resolve_cache
			WHERE domain = ?
		`, buildDomainCacheKey(resolverGroupRemote, domain), domain)
		if execErr != nil {
			err = execErr
			return migrated, removed, err
		}
		if n, nErr := ins.RowsAffected(); nErr == nil {
			migrated += n
		}
		del, execErr := tx.Exec("DELETE FROM domain_resolve_cache WHERE domain=? AND instr(domain, ':')=0", domain)
		if execErr != nil {
			err = execErr
			return migrated, removed, err
		}
		if n, nErr := del.RowsAffected(); nErr == nil {
			removed += n
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		err = commitErr
		return migrated, removed, err
	}
	return migrated, removed, nil
}

func scheduleLegacyDomainCacheSweep() {
	legacyDomainCacheSweepMu.Lock()
	if !legacyDomainCacheLastSweep.IsZero() && time.Since(legacyDomainCacheLastSweep) < 10*time.Second {
		legacyDomainCacheSweepMu.Unlock()
		return
	}
	legacyDomainCacheLastSweep = time.Now()
	legacyDomainCacheSweepMu.Unlock()

	migrated, removed, err := sweepLegacyDomainResolveCacheKeys(500)
	if err != nil {
		log.Printf("[WARN] legacy domain cache sweep failed: %v", err)
		return
	}
	if removed > 0 || migrated > 0 {
		log.Printf("[OSPF] legacy domain cache sweep done: migrated=%d removed=%d", migrated, removed)
	}
}

func getFileSignature(path string) fileSignature {
	info, err := os.Stat(path)
	if err != nil {
		return fileSignature{}
	}
	return fileSignature{exists: true, size: info.Size(), mtime: info.ModTime().UnixNano()}
}

func getGeoDataVersion() string {
	now := time.Now()
	geoVerPath := getPath("core", "mosdns", "geodata.ver")
	geoSitePath := getPath("core", "mosdns", "geosite.dat")

	routeGeoDataVersionCacheMu.Lock()
	cached := routeGeoDataVersionCache
	if cached.initialized && now.Sub(cached.checkedAt) < geoDataVersionCheckInterval {
		version := cached.version
		routeGeoDataVersionCacheMu.Unlock()
		return version
	}
	routeGeoDataVersionCacheMu.Unlock()

	geoVerSig := getFileSignature(geoVerPath)
	geoSiteSig := getFileSignature(geoSitePath)

	routeGeoDataVersionCacheMu.Lock()
	defer routeGeoDataVersionCacheMu.Unlock()
	cached = routeGeoDataVersionCache
	if cached.initialized && cached.geodataVer == geoVerSig && cached.geositeDat == geoSiteSig {
		routeGeoDataVersionCache.checkedAt = now
		return cached.version
	}

	version := "unknown"
	if geoVerSig.exists {
		if data, err := os.ReadFile(geoVerPath); err == nil {
			if v := strings.TrimSpace(string(data)); v != "" {
				version = v
			}
		}
	}
	if version == "unknown" && geoSiteSig.exists {
		version = time.Unix(0, geoSiteSig.mtime).UTC().Format(time.RFC3339)
	}

	routeGeoDataVersionCache = geoDataVersionState{
		version:     version,
		checkedAt:   now,
		geodataVer:  geoVerSig,
		geositeDat:  geoSiteSig,
		initialized: true,
	}
	return version
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
	return getOrRefreshDomainCacheWithResolver(domain, resolverGroupRemote)
}

func getOrRefreshDomainCacheWithResolver(domain string, resolverGroup string) ([]string, int, bool, error) {
	ensureRouteCacheTables()
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" {
		return nil, 0, false, nil
	}
	resolverGroup = normalizeResolverGroup(resolverGroup)
	cacheKey := buildDomainCacheKey(resolverGroup, domain)

	now := nowFunc().UTC()
	var cachedIPsJSON, lastError string
	var expireAtUnix, resolvedAtUnix int64
	var dnsTTL, failCount int
	cacheHit := false
	if err := db.QueryRow("SELECT ips_json, dns_ttl, CAST(resolved_at AS INTEGER), CAST(expire_at AS INTEGER), last_error, fail_count FROM domain_resolve_cache WHERE domain=?", cacheKey).Scan(&cachedIPsJSON, &dnsTTL, &resolvedAtUnix, &expireAtUnix, &lastError, &failCount); err == nil {
		cacheHit = true
		var cachedIPs []string
		_ = json.Unmarshal([]byte(cachedIPsJSON), &cachedIPs)
		cachedIPs = normalizeIPList(cachedIPs)
		if expireAtUnix > now.Unix() && len(cachedIPs) > 0 {
			return cachedIPs, dnsTTL, true, nil
		}
	}

	isRemote := (resolverGroup == resolverGroupRemote)
	ips, ttl, err := resolveDomainIPv4WithTTLViaServers(domain, getResolverDNSServers(resolverGroup), isRemote)
	if err == nil {
		ips = normalizeIPList(ips)
		ttl = clampDomainCacheTTL(ttl)
		payload, _ := json.Marshal(ips)
		expireAt := now.Add(time.Duration(ttl) * time.Second).Unix()
		resolvedAt := now.Unix()
		if _, execErr := db.Exec("INSERT INTO domain_resolve_cache (domain, ips_json, dns_ttl, resolved_at, expire_at, last_error, fail_count, geodata_ver) VALUES (?, ?, ?, ?, ?, '', 0, ?) ON CONFLICT(domain) DO UPDATE SET ips_json=excluded.ips_json, dns_ttl=excluded.dns_ttl, resolved_at=excluded.resolved_at, expire_at=excluded.expire_at, last_error='', fail_count=0, geodata_ver=excluded.geodata_ver", cacheKey, string(payload), ttl, resolvedAt, expireAt, getGeoDataVersion()); execErr != nil {
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
			if _, execErr := db.Exec("UPDATE domain_resolve_cache SET expire_at=?, last_error=?, fail_count=fail_count+1 WHERE domain=?", nextRetryAt, err.Error(), cacheKey); execErr != nil {
				log.Printf("[WARN] update domain cache failure state %q failed: %v", domain, execErr)
			}
			return cachedIPs, nextRetryTTL, false, nil
		}
	}
	return nil, 0, false, fmt.Errorf("resolve %s failed: %w", domain, err)
}
