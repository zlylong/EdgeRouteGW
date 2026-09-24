package main

import (
	"bufio"
	"container/ring"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type ConnectionRecord struct {
	Time            string `json:"time"`
	Client          string `json:"client"`
	Network         string `json:"network"`
	Target          string `json:"target"`
	TargetDomain    string `json:"target_domain,omitempty"`
	Policy          string `json:"policy"`
	RuleID          int    `json:"rule_id,omitempty"`
	RuleType        string `json:"rule_type,omitempty"`
	MatchValue      string `json:"match_value,omitempty"`
	UnmatchedReason string `json:"unmatched_reason,omitempty"`
}

var (
	connRing      *ring.Ring
	connRingMutex sync.RWMutex
	// Matches: 2024/04/18 01:23:45 192.168.1.10:4321 accepted tcp:google.com:443 [proxy]
	logRegex = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+from\s+([^\s]+)\s+accepted\s+(tcp|udp):([^\s]+)\s+\[([^\]]+)\]`)
)

func normalizeConnectionPolicy(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	if p == "" {
		return ""
	}
	if idx := strings.LastIndex(p, ">>"); idx >= 0 {
		p = strings.TrimSpace(p[idx+2:])
	}
	if idx := strings.LastIndex(p, "->"); idx >= 0 {
		p = strings.TrimSpace(p[idx+2:])
	}
	return p
}

// connectionRingSize is how many recent connections are kept in memory and
// the upper bound of the ?limit= parameter.
const connectionRingSize = 200

func init() {
	connRing = ring.New(connectionRingSize)
}

func GetRecentConnections() []ConnectionRecord {
	connRingMutex.RLock()
	defer connRingMutex.RUnlock()

	var records []ConnectionRecord
	connRing.Do(func(p interface{}) {
		if p != nil {
			records = append(records, p.(ConnectionRecord))
		}
	})

	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}

	return records
}

// tailerCatchUpBytes bounds how much of an existing access log is parsed
// when the tailer (re)opens it. The ring only holds connectionRingSize
// records, so re-reading a multi-megabyte file line by line under the ring
// lock bought nothing.
const tailerCatchUpBytes = 256 * 1024

// tailerPollInterval is how long the tailer sleeps at EOF before retrying.
var tailerPollInterval = 200 * time.Millisecond

// recordAccessLogLine parses one Xray access-log line into the ring.
func recordAccessLogLine(line string) bool {
	matches := logRegex.FindStringSubmatch(line)
	if len(matches) != 6 {
		return false
	}
	rawPolicy := strings.TrimSpace(matches[5])
	policy := normalizeConnectionPolicy(rawPolicy)
	if policy == "" {
		policy = rawPolicy
	}
	record := ConnectionRecord{
		Time:    matches[1],
		Client:  matches[2],
		Network: matches[3],
		Target:  matches[4],
		Policy:  policy,
	}
	if strings.HasPrefix(record.Client, "127.0.0.1") || record.Policy == "api" || record.Policy == "dns-out" {
		return false
	}
	connRingMutex.Lock()
	connRing.Value = record
	connRing = connRing.Next()
	connRingMutex.Unlock()
	return true
}

// tailAccessLog follows logPath forever, surviving rotation and truncation.
// The previous tailer only reopened the file when it shrank to zero bytes
// exactly at the moment it hit EOF; after the periodic truncation it kept
// its old offset, saw EOF until the file grew past that offset again, and
// silently skipped everything written in between.
func tailAccessLog(logPath string, stop <-chan struct{}) {
	var file *os.File
	var reader *bufio.Reader
	var offset int64
	closeFile := func() {
		if file != nil {
			_ = file.Close()
			file = nil
			reader = nil
		}
	}
	defer closeFile()
	sleep := func(d time.Duration) bool {
		select {
		case <-stop:
			return false
		case <-time.After(d):
			return true
		}
	}
	for {
		select {
		case <-stop:
			return
		default:
		}
		if file == nil {
			f, err := os.Open(logPath)
			if err != nil {
				if !sleep(2 * time.Second) {
					return
				}
				continue
			}
			file = f
			// Catch up on the tail of what is already there, then follow.
			start := int64(0)
			if st, err := f.Stat(); err == nil && st.Size() > tailerCatchUpBytes {
				start = st.Size() - tailerCatchUpBytes
			}
			if _, err := f.Seek(start, io.SeekStart); err != nil {
				closeFile()
				continue
			}
			reader = bufio.NewReader(f)
			offset = start
			if start > 0 {
				// Skip the partial line the seek landed in.
				if skipped, err := reader.ReadString('\n'); err == nil {
					offset += int64(len(skipped))
				}
			}
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 && err == nil {
			offset += int64(len(line))
			recordAccessLogLine(line)
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			closeFile()
			if !sleep(time.Second) {
				return
			}
			continue
		}
		// EOF: detect truncation or replacement before waiting.
		st, statErr := os.Stat(logPath)
		if statErr != nil || st.Size() < offset || !sameFile(file, st) {
			closeFile()
			if !sleep(time.Second) {
				return
			}
			continue
		}
		if !sleep(tailerPollInterval) {
			return
		}
	}
}

// sameFile reports whether the open handle still refers to the path's inode.
func sameFile(f *os.File, pathInfo os.FileInfo) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return os.SameFile(fi, pathInfo)
}

// accessLogMaxSize is the size above which the runtime access log is
// truncated so a tmpfs /run cannot fill up.
const accessLogMaxSize = 5 * 1024 * 1024

func StartConnectionTracker() {
	logPath := xrayAccessLogPath
	goSafeLoop("connection tracker", func() { tailAccessLog(logPath, nil) })
	goSafeLoop("connection tracker cleanup", func() {
		for {
			time.Sleep(5 * time.Minute)
			stat, err := os.Stat(logPath)
			if err == nil && stat.Size() > accessLogMaxSize {
				// Xray opens the file with O_APPEND, so truncating to 0 is
				// safe; the tailer notices the size drop and reopens.
				_ = os.Truncate(logPath, 0)
			}
		}
	})
}

func targetHostOnly(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if strings.Contains(t, ":") {
		if h, _, err := net.SplitHostPort(t); err == nil {
			return strings.Trim(h, "[]")
		}
		if idx := strings.LastIndex(t, ":"); idx > 0 {
			return strings.Trim(t[:idx], "[]")
		}
	}
	return strings.Trim(t, "[]")
}

func isIPRuleMatch(ipStr string, ruleValue string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	v := strings.TrimSpace(ruleValue)
	if v == "" {
		return false
	}
	if strings.Contains(v, "/") {
		_, cidr, err := net.ParseCIDR(v)
		if err != nil || cidr == nil {
			return false
		}
		return cidr.Contains(ip)
	}
	return ip.Equal(net.ParseIP(v))
}

func lookupRecentDomainByIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	var domain string
	if err := getDB().QueryRow(
		"SELECT COALESCE(domain, '') FROM routes_table WHERE (ip=? OR ip=? || '/32') AND COALESCE(domain, '') <> '' ORDER BY CASE WHEN ip=? THEN 0 ELSE 1 END, datetime(last_seen) DESC LIMIT 1",
		ip, ip, ip,
	).Scan(&domain); err == nil {
		return strings.TrimSpace(domain)
	}
	return ""
}

func lookupRecentDomainByIPFromResolveCache(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	var domain string
	if err := getDB().QueryRow(
		"SELECT COALESCE(domain, '') FROM domain_resolve_cache WHERE ips_json LIKE '%' || char(34) || ? || char(34) || '%' ORDER BY CAST(COALESCE(resolved_at, '0') AS INTEGER) DESC LIMIT 1",
		ip,
	).Scan(&domain); err != nil {
		return ""
	}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	if strings.HasPrefix(domain, "remote:") || strings.HasPrefix(domain, "local:") {
		if idx := strings.Index(domain, ":"); idx >= 0 && idx+1 < len(domain) {
			domain = domain[idx+1:]
		}
	}
	return strings.TrimSpace(domain)
}

func resolveConnectionTargetDomain(target string) string {
	host := targetHostOnly(target)
	if host == "" {
		return ""
	}
	if net.ParseIP(host) == nil {
		return host
	}
	if domain := lookupRecentDomainByIP(host); domain != "" {
		return domain
	}
	if domain := lookupRecentDomainByIPFromResolveCache(host); domain != "" {
		return domain
	}
	return host
}

func policyMatchesConnection(rulePolicy, connPolicy string) bool {
	rp := strings.ToLower(strings.TrimSpace(rulePolicy))
	cp := strings.ToLower(strings.TrimSpace(connPolicy))
	if rp == "" || cp == "" {
		return true
	}
	switch {
	case rp == "direct" || rp == "block":
		return cp == rp
	case rp == "proxy":
		return cp == "proxy" || (strings.HasPrefix(cp, "proxy-") && strings.HasSuffix(cp, "-out"))
	case strings.HasPrefix(rp, "proxy-"):
		return cp == rp || cp == rp+"-out"
	case strings.HasPrefix(rp, "ha-"):
		parts := strings.Split(strings.TrimPrefix(rp, "ha-"), "-")
		if len(parts) != 2 {
			return false
		}
		primary := fmt.Sprintf("proxy-%s-out", parts[0])
		standby := fmt.Sprintf("proxy-%s-out", parts[1])
		return cp == primary || cp == standby
	default:
		return cp == rp || cp == rp+"-out"
	}
}

// compiledConnRule is a rule with its match value parsed once, so matching
// 200 connections against N rules does not re-parse each value 200 times.
type compiledConnRule struct {
	id        int
	rtype     string
	value     string
	policy    string
	domainPat domainRulePattern
	domainOK  bool
	ipNet     *net.IPNet
	ip        net.IP
	tag       string
}

func compileConnRules() ([]compiledConnRule, error) {
	rows, err := getDB().Query("SELECT id, type, value, policy FROM rules ORDER BY priority ASC, id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]compiledConnRule, 0, 128)
	for rows.Next() {
		var r compiledConnRule
		if err := rows.Scan(&r.id, &r.rtype, &r.value, &r.policy); err != nil {
			continue
		}
		r.rtype = strings.ToLower(strings.TrimSpace(r.rtype))
		r.value = strings.TrimSpace(r.value)
		r.policy = strings.TrimSpace(r.policy)
		switch r.rtype {
		case "domain":
			if p, err := parseDomainRulePattern(r.value); err == nil {
				r.domainPat, r.domainOK = p, true
			}
		case "ip":
			if strings.Contains(r.value, "/") {
				if _, cidr, err := net.ParseCIDR(r.value); err == nil {
					r.ipNet = cidr
				}
			} else {
				r.ip = net.ParseIP(r.value)
			}
		case "geoip", "geolocation":
			r.tag = strings.ToLower(strings.TrimPrefix(strings.ToLower(r.value), "!"))
		case "geosite":
			r.tag = strings.ToLower(r.value)
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}

func (r compiledConnRule) matchesIP(ip net.IP) bool {
	if r.ipNet != nil {
		return r.ipNet.Contains(ip)
	}
	return r.ip != nil && ip.Equal(r.ip)
}

// matchDomainPattern is isDomainMatch with the pattern already parsed.
func matchDomainPattern(host string, parsed domainRulePattern) bool {
	switch parsed.Kind {
	case domainRulePatternFull:
		return host == parsed.Base
	case domainRulePatternSuffix:
		return host == parsed.Base || strings.HasSuffix(host, "."+parsed.Base)
	case domainRulePatternSingleLevel:
		if host == parsed.Base {
			return true
		}
		prefix, ok := strings.CutSuffix(host, "."+parsed.Base)
		if !ok || prefix == "" {
			return false
		}
		return !strings.Contains(prefix, ".")
	default:
		return false
	}
}

// The resolve cache stores each domain's IPs as a JSON array; finding the
// domain for an IP therefore needs a scan. Instead of a LIKE '%ip%' full
// table scan per connection (up to 200 per poll), keep a reverse index of
// the most recent rows and rebuild it at most every resolveIndexTTL.
const (
	resolveIndexTTL  = 10 * time.Second
	resolveIndexRows = 5000
)

var (
	resolveIndexMu    sync.Mutex
	resolveIndexDB    *sql.DB
	resolveIndexAt    time.Time
	resolveIndexIPMap map[string]string
)

func stripResolverGroupPrefix(domain string) string {
	domain = strings.TrimSpace(domain)
	if strings.HasPrefix(domain, "remote:") || strings.HasPrefix(domain, "local:") {
		if idx := strings.Index(domain, ":"); idx >= 0 && idx+1 < len(domain) {
			domain = domain[idx+1:]
		}
	}
	return strings.TrimSpace(domain)
}

func resolveCacheReverseIndex() map[string]string {
	d := getDB()
	resolveIndexMu.Lock()
	defer resolveIndexMu.Unlock()
	if d != nil && resolveIndexDB == d && time.Since(resolveIndexAt) < resolveIndexTTL {
		return resolveIndexIPMap
	}
	index := make(map[string]string, 1024)
	if d != nil {
		rows, err := d.Query("SELECT COALESCE(domain, ''), ips_json FROM domain_resolve_cache ORDER BY CAST(COALESCE(resolved_at, '0') AS INTEGER) DESC LIMIT ?", resolveIndexRows)
		if err == nil {
			for rows.Next() {
				var domain, ipsJSON string
				if rows.Scan(&domain, &ipsJSON) != nil {
					continue
				}
				domain = stripResolverGroupPrefix(domain)
				if domain == "" {
					continue
				}
				var ips []string
				if json.Unmarshal([]byte(ipsJSON), &ips) != nil {
					continue
				}
				for _, ip := range ips {
					// Newest row wins: rows are ordered by resolved_at DESC.
					if _, seen := index[ip]; !seen {
						index[ip] = domain
					}
				}
			}
			rows.Close()
		}
	}
	resolveIndexDB = d
	resolveIndexAt = time.Now()
	resolveIndexIPMap = index
	return index
}

func invalidateResolveCacheIndex() {
	resolveIndexMu.Lock()
	resolveIndexDB = nil
	resolveIndexMu.Unlock()
}

// batchLookupRecentDomains maps each IP to the most recently seen domain in
// routes_table with one query, falling back to the resolve-cache index.
func batchLookupRecentDomains(ips []string) map[string]string {
	out := make(map[string]string, len(ips))
	if len(ips) == 0 {
		return out
	}
	d := getDB()
	if d != nil {
		placeholders := make([]string, 0, len(ips)*2)
		args := make([]interface{}, 0, len(ips)*2)
		for _, ip := range ips {
			placeholders = append(placeholders, "?", "?")
			args = append(args, ip, ip+"/32")
		}
		// Ordered newest first; the first hit per IP wins, with an exact key
		// preferred over the /32 form as lookupRecentDomainByIP does.
		q := "SELECT ip, COALESCE(domain, '') FROM routes_table WHERE ip IN (" + strings.Join(placeholders, ",") + ") AND COALESCE(domain, '') <> '' ORDER BY datetime(last_seen) DESC"
		rows, err := d.Query(q, args...)
		if err == nil {
			exact := make(map[string]bool, len(ips))
			for rows.Next() {
				var key, domain string
				if rows.Scan(&key, &domain) != nil {
					continue
				}
				ip := strings.TrimSuffix(key, "/32")
				isExact := key == ip
				if prev, ok := out[ip]; !ok || (isExact && !exact[ip] && prev != "") {
					if !ok || isExact {
						out[ip] = strings.TrimSpace(domain)
						exact[ip] = isExact
					}
				}
			}
			rows.Close()
		}
	}
	var index map[string]string
	for _, ip := range ips {
		if out[ip] != "" {
			continue
		}
		if index == nil {
			index = resolveCacheReverseIndex()
		}
		if domain := index[ip]; domain != "" {
			out[ip] = domain
		}
	}
	return out
}

func attachRuleMatchMeta(records []ConnectionRecord) []ConnectionRecord {
	rules, err := compileConnRules()
	if err != nil {
		return records
	}

	geoipPath := getPath("core", "mosdns", "geoip.dat")
	geositePath := getPath("core", "mosdns", "geosite.dat")

	// Resolve IP targets to domains for the whole batch up front.
	ipTargets := make([]string, 0, len(records))
	seenIP := make(map[string]struct{}, len(records))
	for i := range records {
		host := targetHostOnly(records[i].Target)
		if host == "" || net.ParseIP(host) == nil {
			continue
		}
		if _, ok := seenIP[host]; !ok {
			seenIP[host] = struct{}{}
			ipTargets = append(ipTargets, host)
		}
	}
	domainsByIP := batchLookupRecentDomains(ipTargets)

	for i := range records {
		host := targetHostOnly(records[i].Target)
		switch {
		case host == "":
			records[i].TargetDomain = ""
		case net.ParseIP(host) == nil:
			records[i].TargetDomain = host
		case domainsByIP[host] != "":
			records[i].TargetDomain = domainsByIP[host]
		default:
			records[i].TargetDomain = host
		}
		records[i].UnmatchedReason = ""
		if host == "" {
			records[i].UnmatchedReason = "目标地址为空或格式无效"
			continue
		}
		parsedIP := net.ParseIP(host)
		domainHost := strings.TrimSpace(records[i].TargetDomain)
		if net.ParseIP(domainHost) != nil {
			domainHost = ""
		}
		var geoipTags []string
		geoipLoaded := false
		hasPolicyMatch := false
		hasTypeCandidate := false
		hasValueMismatch := false
		geositeMatchCache := make(map[string]bool)
		geositeMatchChecked := make(map[string]bool)

		for _, rule := range rules {
			if !policyMatchesConnection(rule.policy, records[i].Policy) {
				continue
			}
			hasPolicyMatch = true
			matched := false
			switch rule.rtype {
			case "domain":
				if domainHost != "" {
					hasTypeCandidate = true
					matched = rule.domainOK && matchDomainPattern(strings.ToLower(strings.TrimSuffix(domainHost, ".")), rule.domainPat)
				}
			case "ip":
				if parsedIP != nil {
					hasTypeCandidate = true
					matched = rule.matchesIP(parsedIP)
				}
			case "geoip", "geolocation":
				if parsedIP != nil {
					hasTypeCandidate = true
					if !geoipLoaded {
						geoipTags = queryGeoIPTagsByIP(geoipPath, parsedIP.String())
						geoipLoaded = true
					}
					tag := rule.tag
					for _, t := range geoipTags {
						if strings.EqualFold(t, tag) {
							matched = true
							break
						}
					}
				}
			case "geosite":
				if domainHost != "" {
					hasTypeCandidate = true
					tag := rule.tag
					if tag != "" {
						if !geositeMatchChecked[tag] {
							geositeMatchCache[tag] = geoSiteTagMatchesDomain(geositePath, tag, domainHost)
							geositeMatchChecked[tag] = true
						}
						matched = geositeMatchCache[tag]
					}
				}
			}
			if matched {
				records[i].RuleID = rule.id
				records[i].RuleType = rule.rtype
				records[i].MatchValue = rule.value
				records[i].UnmatchedReason = ""
				break
			}
			if hasTypeCandidate {
				hasValueMismatch = true
			}
		}

		if records[i].RuleID == 0 {
			switch {
			case !hasPolicyMatch:
				records[i].UnmatchedReason = "无策略一致的规则"
			case !hasTypeCandidate && parsedIP != nil && domainHost == "":
				records[i].UnmatchedReason = "目标为IP且无域名回填，无法匹配域名/Geosite规则"
			case !hasTypeCandidate:
				records[i].UnmatchedReason = "无可匹配的规则类型"
			case hasValueMismatch:
				records[i].UnmatchedReason = "存在同策略规则，但匹配值未命中"
			default:
				records[i].UnmatchedReason = "未命中可关联规则"
			}
		}
	}
	return records
}

func registerConnectionRoutes(r *gin.RouterGroup) {
	r.GET("/connections", func(c *gin.Context) {
		ip := c.Query("ip")
		limit := connectionRingSize
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < limit {
				limit = n
			}
		}
		allConns := GetRecentConnections()

		queryIP := strings.ToLower(strings.TrimSpace(ip))

		// Always an array: the UI gates rendering on "data" being truthy, and
		// a nil slice encoded as null left the previous list on screen.
		filtered := make([]ConnectionRecord, 0, len(allConns))
		for _, conn := range allConns {
			if queryIP != "" {
				clientHit := strings.Contains(strings.ToLower(conn.Client), queryIP)
				targetHit := strings.Contains(strings.ToLower(conn.Target), queryIP)
				if !clientHit && !targetHit {
					continue
				}
			}
			filtered = append(filtered, conn)
		}
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}
		filtered = attachRuleMatchMeta(filtered)
		if filtered == nil {
			filtered = make([]ConnectionRecord, 0)
		}

		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    filtered,
		})
	})
}
