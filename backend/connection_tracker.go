package main

import (
	"bufio"
	"container/ring"
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
	Time         string `json:"time"`
	Client       string `json:"client"`
	Network      string `json:"network"`
	Target       string `json:"target"`
	TargetDomain string `json:"target_domain,omitempty"`
	Policy       string `json:"policy"`
	RuleID       int    `json:"rule_id,omitempty"`
	RuleType     string `json:"rule_type,omitempty"`
	MatchValue   string `json:"match_value,omitempty"`
}

var (
	connRing      *ring.Ring
	connRingMutex sync.RWMutex
	// Matches: 2024/04/18 01:23:45 192.168.1.10:4321 accepted tcp:google.com:443 [proxy]
	logRegex = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+from\s+([^\s]+)\s+accepted\s+(tcp|udp):([^\s]+)\s+\[([^\]]+)\]`)
)

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
	targetIP := net.ParseIP(ip)
	if targetIP == nil {
		return ""
	}
	rows, err := db.Query("SELECT ip, COALESCE(domain, '') FROM routes_table WHERE COALESCE(domain, '') <> '' ORDER BY last_seen DESC")
	if err != nil {
		return ""
	}
	defer rows.Close()
	bestDomain := ""
	bestPrefix := -1
	for rows.Next() {
		var routeKey, domain string
		if err := rows.Scan(&routeKey, &domain); err != nil {
			continue
		}
		routeKey = strings.TrimSpace(routeKey)
		domain = strings.TrimSpace(domain)
		if routeKey == "" || domain == "" {
			continue
		}
		prefix := -1
		if strings.Contains(routeKey, "/") {
			_, ipNet, err := net.ParseCIDR(routeKey)
			if err != nil || ipNet == nil || !ipNet.Contains(targetIP) {
				continue
			}
			prefix, _ = ipNet.Mask.Size()
		} else {
			routeIP := net.ParseIP(routeKey)
			if routeIP == nil || !routeIP.Equal(targetIP) {
				continue
			}
			prefix = 32
		}
		if prefix > bestPrefix {
			bestPrefix = prefix
			bestDomain = domain
		}
	}
	return bestDomain
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
	return host
}

func attachRuleMatchMeta(records []ConnectionRecord) []ConnectionRecord {
	rows, err := db.Query("SELECT id, type, value FROM rules ORDER BY id ASC")
	if err != nil {
		return records
	}
	defer rows.Close()
	type simpleRule struct {
		id    int
		rtype string
		value string
	}
	rules := make([]simpleRule, 0, 128)
	for rows.Next() {
		var r simpleRule
		if err := rows.Scan(&r.id, &r.rtype, &r.value); err != nil {
			continue
		}
		r.rtype = strings.ToLower(strings.TrimSpace(r.rtype))
		r.value = strings.TrimSpace(r.value)
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
		if host == "" {
			continue
		}
		parsedIP := net.ParseIP(host)
		domainHost := strings.TrimSpace(records[i].TargetDomain)
		if net.ParseIP(domainHost) != nil {
			domainHost = ""
		}
		var geoipTags []string
		var geositeTags []string
		geoipLoaded := false
		geositeLoaded := false

		for _, rule := range rules {
			matched := false
			switch rule.rtype {
			case "domain":
				if domainHost != "" {
					matched = isDomainMatch(domainHost, rule.value)
				}
			case "ip":
				matched = isIPRuleMatch(host, rule.value)
			case "geoip", "geolocation":
				if parsedIP != nil {
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
					if !geositeLoaded {
						geositeTags = queryGeoSiteTagsByDomain(geositePath, domainHost)
						geositeLoaded = true
					}
					for _, t := range geositeTags {
						if strings.EqualFold(t, rule.value) {
							matched = true
							break
						}
					}
				}
			}
			if matched {
				records[i].RuleID = rule.id
				records[i].RuleType = rule.rtype
				records[i].MatchValue = rule.value
				break
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
