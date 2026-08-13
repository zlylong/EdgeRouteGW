package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

type TestController struct{}

func NewTestController() *TestController {
	return &TestController{}
}

func (ctl *TestController) RegisterRoutes(api *gin.RouterGroup) {
	api.GET("/test/trace", ctl.HandleTrace)
	api.GET("/test/health_check", ctl.HandleHealthCheck)
}

type HealthCheckResult struct {
	Component string `json:"component"`
	Status    string `json:"status"`
	Details   string `json:"details"`
}

func (ctl *TestController) HandleHealthCheck(c *gin.Context) {
	results := []HealthCheckResult{}

	// 1. DB Check
	dbStatus := "OK"
	dbDetails := ""
	if err := getDB().Ping(); err != nil {
		dbStatus = "Error"
		dbDetails = err.Error()
	}
	results = append(results, HealthCheckResult{"Database", dbStatus, dbDetails})

	// 2. Xray Check
	xrayStatus := "OK"
	xrayDetails := ""
	if _, err := os.Stat(getPath("core", "xray", "xray")); err != nil {
		xrayStatus = "Error"
		xrayDetails = "Binary missing: " + err.Error()
	} else {
		res := sysCmd.runCombinedOutput("systemctl", "is-active", "--quiet", "xray")
		if res.Err != nil {
			xrayStatus = "Warn"
			xrayDetails = "Service not active (systemctl)"
		}
	}
	results = append(results, HealthCheckResult{"Xray", xrayStatus, xrayDetails})

	// 3. Mosdns Check
	mosdnsStatus := "OK"
	mosdnsDetails := ""
	if _, err := os.Stat(getPath("core", "mosdns", "mosdns")); err != nil {
		mosdnsStatus = "Error"
		mosdnsDetails = "Binary missing: " + err.Error()
	} else {
		res := sysCmd.runCombinedOutput("systemctl", "is-active", "--quiet", "mosdns")
		if res.Err != nil {
			mosdnsStatus = "Warn"
			mosdnsDetails = "Service not active (systemctl)"
		}
	}
	results = append(results, HealthCheckResult{"Mosdns", mosdnsStatus, mosdnsDetails})

	// 4. Geodata Check
	geoStatus := "OK"
	geoDetails := ""
	files := []string{"geoip.dat", "geosite.dat"}
	for _, f := range files {
		p := getPath("core", "mosdns", f)
		info, err := os.Stat(p)
		if err != nil {
			geoStatus = "Error"
			geoDetails += fmt.Sprintf("%s missing; ", f)
		} else if info.Size() < minHealthyGeodataSize {
			geoStatus = "Error"
			geoDetails += fmt.Sprintf("%s too small (%d); ", f, info.Size())
		}
	}
	results = append(results, HealthCheckResult{"GeoData", geoStatus, geoDetails})

	// 5. Mode-specific check
	var mode string
	_ = getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	if mode == "A" {
		res := sysCmd.runCombinedOutput("nft", "list", "ruleset")
		if res.Err != nil {
			results = append(results, HealthCheckResult{"Nftables", "Error", res.Err.Error()})
		} else if !strings.Contains(string(res.Output), "proxygw") {
			results = append(results, HealthCheckResult{"Nftables", "Warn", "Ruleset 'proxygw' not found"})
		} else {
			results = append(results, HealthCheckResult{"Nftables", "OK", ""})
		}
	} else {
		res := sysCmd.runCombinedOutput("systemctl", "is-active", "--quiet", "frr")
		if res.Err != nil {
			results = append(results, HealthCheckResult{"FRR/OSPF", "Warn", "Service not active"})
		} else {
			results = append(results, HealthCheckResult{"FRR/OSPF", "OK", ""})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"results": results,
		"mode":    mode,
	})
}

type TraceResult struct {
	Target      string         `json:"target"`
	Type        string         `json:"type"`
	MatchedRule map[string]any `json:"matched_rule"`
	Outbound    string         `json:"outbound"`
	Reason      string         `json:"reason"`
}

func (ctl *TestController) HandleTrace(c *gin.Context) {
	target := strings.TrimSpace(c.Query("target"))
	if target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target is required"})
		return
	}

	active, defaultID := getActiveNodeContext()
	var failoverMode string
	_ = getDB().QueryRow("SELECT value FROM settings WHERE key='node_failover_mode'").Scan(&failoverMode)
	strictFailover := normalizeNodeFailoverMode(strings.TrimSpace(strings.ToLower(failoverMode))) == "strict"

	ip := net.ParseIP(target)
	isIP := ip != nil
	targetType := "domain"
	if isIP {
		targetType = "ip"
	}

	rows, err := getDB().Query("SELECT id, type, value, policy FROM rules ORDER BY priority ASC, id ASC")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	defer rows.Close()

	var result *TraceResult

	for rows.Next() {
		var id int
		var rtype, rvalue, policy string
		if err := rows.Scan(&id, &rtype, &rvalue, &policy); err != nil {
			continue
		}

		matched := false
		reason := ""

		if isIP {
			if rtype == "ip" {
				_, ipNet, err := net.ParseCIDR(rvalue)
				if err == nil {
					if ipNet.Contains(ip) {
						matched = true
						reason = fmt.Sprintf("matched IP CIDR %s", rvalue)
					}
				} else {
					// Single IP match
					if rvalue == target {
						matched = true
						reason = fmt.Sprintf("matched direct IP %s", rvalue)
					}
				}
			} else if rtype == "geoip" || rtype == "geolocation" {
				if strings.EqualFold(rvalue, "private") {
					privateNets := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"}
					for _, pn := range privateNets {
						_, ipNet, _ := net.ParseCIDR(pn)
						if ipNet.Contains(ip) {
							matched = true
							reason = "matched geoip:private"
							break
						}
					}
				} else {
					tags := queryGeoIPTagsByIP(getPath("core", "mosdns", "geoip.dat"), target)
					targetTag := strings.ToLower(strings.TrimSpace(rvalue))
					isExclude := strings.HasPrefix(targetTag, "!")
					pureTag := strings.TrimPrefix(targetTag, "!")

					tagFound := false
					for _, t := range tags {
						if t == pureTag {
							tagFound = true
							break
						}
					}

					if (isExclude && !tagFound) || (!isExclude && tagFound) {
						matched = true
						reason = fmt.Sprintf("matched geoip:%s", rvalue)
					}
				}
			}
		} else {
			// Domain match
			if rtype == "domain" {
				domainValues, _ := buildXrayDomainRuleValues(rvalue)
				for _, dv := range domainValues {
					if matchDomain(target, dv) {
						matched = true
						reason = fmt.Sprintf("matched domain rule %s", dv)
						break
					}
				}
			} else if rtype == "geosite" {
				if geoSiteTagMatchesDomain(getPath("core", "mosdns", "geosite.dat"), rvalue, target) {
					matched = true
					reason = fmt.Sprintf("matched geosite:%s", rvalue)
				}
			}
		}

		if matched {
			result = &TraceResult{
				Target: target,
				Type:   targetType,
				MatchedRule: map[string]any{
					"id":     id,
					"type":   rtype,
					"value":  rvalue,
					"policy": policy,
				},
				Outbound: outboundTagForPolicy(policy, active, defaultID, strictFailover),
				Reason:   reason,
			}
			break
		}
	}

	if result == nil {
		// Default fallback
		var defaultPolicy string
		if err := getDB().QueryRow("SELECT value FROM settings WHERE key='lan_default_policy'").Scan(&defaultPolicy); err != nil {
			defaultPolicy = "proxy"
		}

		outbound := "direct"
		if strings.ToLower(defaultPolicy) == "proxy" {
			if defaultID > 0 {
				outbound = fmt.Sprintf("proxy-%d-out", defaultID)
			}
		} else if strings.ToLower(defaultPolicy) == "block" {
			outbound = "block"
		}

		result = &TraceResult{
			Target:      target,
			Type:        targetType,
			MatchedRule: nil,
			Outbound:    outbound,
			Reason:      "no rules matched, fell back to default policy: " + defaultPolicy,
		}
	}

	c.JSON(http.StatusOK, result)
}

func matchDomain(target, pattern string) bool {
	target = strings.ToLower(target)
	if strings.HasPrefix(pattern, "full:") {
		return target == strings.TrimPrefix(pattern, "full:")
	}
	if strings.HasPrefix(pattern, "domain:") {
		val := strings.TrimPrefix(pattern, "domain:")
		return target == val || strings.HasSuffix(target, "."+val)
	}
	if strings.HasPrefix(pattern, "keyword:") {
		return strings.Contains(target, strings.TrimPrefix(pattern, "keyword:"))
	}
	if strings.HasPrefix(pattern, "regexp:") {
		matched, _ := regexp.MatchString(strings.TrimPrefix(pattern, "regexp:"), target)
		return matched
	}
	// Default Xray domain match behavior (same as domain:)
	return target == pattern || strings.HasSuffix(target, "."+pattern)
}
