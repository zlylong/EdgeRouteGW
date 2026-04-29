package main

import (
	"bufio"
	"container/ring"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
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

func init() {
	connRing = ring.New(200) // Keep last 200 connections
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

func StartConnectionTracker() {
	logPath := "/run/proxygw/xray_access.log"

	go func() {
		var file *os.File
		var err error
		var reader *bufio.Reader

		for {
			if file == nil {
				file, err = os.Open(logPath)
				if err != nil {
					time.Sleep(2 * time.Second)
					continue
				}
				// Parse existing lines before tailing
				scanner := bufio.NewScanner(file)
				for scanner.Scan() {
					line := scanner.Text()
					matches := logRegex.FindStringSubmatch(line)
					if len(matches) == 6 {
						record := ConnectionRecord{
							Time:    matches[1],
							Client:  matches[2],
							Network: matches[3],
							Target:  matches[4],
							Policy:  matches[5],
						}
						if strings.HasPrefix(record.Client, "127.0.0.1") || record.Policy == "api" || record.Policy == "dns-out" {
							continue
						}
						connRingMutex.Lock()
						connRing.Value = record
						connRing = connRing.Next()
						connRingMutex.Unlock()
					}
				}
				// Seek to the end to start tailing
				file.Seek(0, 2)
				reader = bufio.NewReader(file)
			}

			line, err := reader.ReadString('\n')
			if err != nil {
				stat, errStat := os.Stat(logPath)
				if errStat != nil || stat.Size() == 0 {
					file.Close()
					file = nil
					time.Sleep(1 * time.Second)
					continue
				}
				time.Sleep(200 * time.Millisecond)
				continue
			}

			matches := logRegex.FindStringSubmatch(line)
			if len(matches) == 6 {
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
					continue
				}

				connRingMutex.Lock()
				connRing.Value = record
				connRing = connRing.Next()
				connRingMutex.Unlock()
			}
		}
	}()

	go func() {
		for {
			time.Sleep(5 * time.Minute)
			stat, err := os.Stat(logPath)
			if err == nil && stat.Size() > 5*1024*1024 {
				// Safely truncate the log file to prevent tmpfs overflow
				// Xray opens file with O_APPEND, so truncate to 0 works perfectly
				os.Truncate(logPath, 0)
			}
		}
	}()
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
	if err := db.QueryRow(
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
	if err := db.QueryRow(
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

func attachRuleMatchMeta(records []ConnectionRecord) []ConnectionRecord {
	rows, err := db.Query("SELECT id, type, value, policy FROM rules ORDER BY priority ASC, id ASC")
	if err != nil {
		return records
	}
	defer rows.Close()
	type simpleRule struct {
		id     int
		rtype  string
		value  string
		policy string
	}
	rules := make([]simpleRule, 0, 128)
	for rows.Next() {
		var r simpleRule
		if err := rows.Scan(&r.id, &r.rtype, &r.value, &r.policy); err != nil {
			continue
		}
		r.rtype = strings.ToLower(strings.TrimSpace(r.rtype))
		r.value = strings.TrimSpace(r.value)
		r.policy = strings.TrimSpace(r.policy)
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return records
	}

	geoipPath := getPath("core", "mosdns", "geoip.dat")
	geositePath := getPath("core", "mosdns", "geosite.dat")

	for i := range records {
		host := targetHostOnly(records[i].Target)
		records[i].TargetDomain = resolveConnectionTargetDomain(records[i].Target)
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
					matched = isDomainMatch(domainHost, rule.value)
				}
			case "ip":
				if parsedIP != nil {
					hasTypeCandidate = true
					matched = isIPRuleMatch(host, rule.value)
				}
			case "geoip", "geolocation":
				if parsedIP != nil {
					hasTypeCandidate = true
					if !geoipLoaded {
						geoipTags = queryGeoIPTagsByIP(geoipPath, parsedIP.String())
						geoipLoaded = true
					}
					tag := strings.ToLower(strings.TrimPrefix(strings.ToLower(rule.value), "!"))
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
					tag := strings.ToLower(strings.TrimSpace(rule.value))
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
		allConns := GetRecentConnections()

		if ip == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"data":    []ConnectionRecord{},
			})
			return
		}

		var filtered []ConnectionRecord
		for _, conn := range allConns {
			if strings.Contains(strings.ToLower(conn.Client), strings.ToLower(ip)) {
				filtered = append(filtered, conn)
			}
		}
		filtered = attachRuleMatchMeta(filtered)

		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    filtered,
		})
	})
}
