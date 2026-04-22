package main

import (
	"log"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

func registerRuleRoutes(api *gin.RouterGroup) {
	api.GET("/rules/categories", func(c *gin.Context) {
		cacheMutex.Lock()
		if len(cachedGeosite) == 0 {
			cachedGeosite = parseDatFile(getPath("core", "mosdns/geosite.dat"))
		}
		if len(cachedGeoip) == 0 {
			cachedGeoip = parseDatFile(getPath("core", "mosdns/geoip.dat"))
		}
		resGeosite := append([]string(nil), cachedGeosite...)
		resGeoip := append([]string(nil), cachedGeoip...)
		cacheMutex.Unlock()

		// Virtual geoip tags supported by Xray but not present in geoip.dat tag list
		hasNotCN := false
		for _, tag := range resGeoip {
			if tag == "!cn" {
				hasNotCN = true
				break
			}
		}
		if !hasNotCN {
			resGeoip = append([]string{"!cn"}, resGeoip...)
		}

		c.JSON(http.StatusOK, gin.H{"geosite": resGeosite, "geoip": resGeoip})
	})

	api.GET("/geo/query", func(c *gin.Context) {
		input := strings.TrimSpace(c.Query("input"))
		if input == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "input required"})
			return
		}

		geoipPath := getPath("core", "mosdns", "geoip.dat")
		geositePath := getPath("core", "mosdns", "geosite.dat")
		if kind, tag, ok := parseGeoRuleInput(input); ok {
			if kind == "geoip" {
				values := []string{}
				exists := false
				if strings.HasPrefix(tag, "!") {
					exists = hasGeoIPTag(geoipPath, tag)
					if exists {
						values = extractGeoIPsExclude(geoipPath, tag, "private")
					}
				} else {
					values = extractGeoIPs(geoipPath, tag)
					exists = hasGeoIPTag(geoipPath, tag)
				}
				c.JSON(http.StatusOK, gin.H{"mode": "expand", "query_type": "geoip", "input": input, "rule": "geoip:" + tag, "exists": exists, "count": len(values), "values": values})
				return
			}

			values := extractGeoSiteValues(geositePath, tag)
			c.JSON(http.StatusOK, gin.H{"mode": "expand", "query_type": "geosite", "input": input, "rule": "geosite:" + tag, "exists": hasGeoSiteTag(geositePath, tag), "count": len(values), "values": values})
			return
		}

		if ip := net.ParseIP(input); ip != nil {
			matches := queryGeoIPTagsByIP(geoipPath, ip.String())
			c.JSON(http.StatusOK, gin.H{"mode": "lookup", "query_type": "ip", "input": ip.String(), "resolved_ips": []string{ip.String()}, "geoip_matches": matches, "geosite_matches": []string{}})
			return
		}

		resolvedIPs, _ := geoQueryLookupIP(input)
		sort.Strings(resolvedIPs)
		geoipMatches := make(map[string]struct{})
		for _, resolvedIP := range resolvedIPs {
			for _, tag := range queryGeoIPTagsByIP(geoipPath, resolvedIP) {
				geoipMatches[tag] = struct{}{}
			}
		}
		mergedGeoipMatches := make([]string, 0, len(geoipMatches))
		for tag := range geoipMatches {
			mergedGeoipMatches = append(mergedGeoipMatches, tag)
		}
		sort.Strings(mergedGeoipMatches)

		c.JSON(http.StatusOK, gin.H{"mode": "lookup", "query_type": "domain", "input": input, "resolved_ips": resolvedIPs, "geoip_matches": mergedGeoipMatches, "geosite_matches": queryGeoSiteTagsByDomain(geositePath, input)})
	})

	api.GET("/rules", func(c *gin.Context) {
		rows, err := db.Query("SELECT id, type, value, policy FROM rules")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
			return
		}
		defer rows.Close()
		var rules []map[string]interface{}
		for rows.Next() {
			var id int
			var rtype, value, policy string
			if err := rows.Scan(&id, &rtype, &value, &policy); err != nil {
				continue
			}
			rules = append(rules, map[string]interface{}{"id": id, "type": rtype, "value": value, "policy": policy})
		}
		if err := rows.Err(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db rows error"})
			return
		}
		if err := rows.Err(); err != nil {
			c.JSON(500, gin.H{"error": "db rows error"})
			return
		}
		if rules == nil {
			rules = make([]map[string]interface{}, 0)
		}
		c.JSON(http.StatusOK, rules)
	})

	api.POST("/rules", func(c *gin.Context) {
		var r struct{ Type, Value, Policy string }
		if c.BindJSON(&r) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}

		r.Type = strings.ToLower(strings.TrimSpace(r.Type))
		r.Value = strings.TrimSpace(r.Value)
		r.Policy = strings.ToLower(strings.TrimSpace(r.Policy))
		if r.Type == "" || r.Value == "" || r.Policy == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "type/value/policy required"})
			return
		}

		allowedType := map[string]bool{"domain": true, "geosite": true, "geoip": true, "geolocation": true, "ip": true}
		if !allowedType[r.Type] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid rule type"})
			return
		}
		if r.Type == "ip" && !isValidIPOrCIDR(r.Value) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ip/cidr rule value"})
			return
		}
		if r.Policy != "direct" && r.Policy != "block" && !strings.HasPrefix(r.Policy, "proxy") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid policy"})
			return
		}

		if _, err := db.Exec("INSERT INTO rules (type, value, policy) VALUES (?, ?, ?)", r.Type, r.Value, r.Policy); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		needMosdns := r.Type == "domain"
		if err := applyRuleChangeDynamically(needMosdns); err != nil {
			log.Printf("[WARN] dynamic rule apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(needMosdns)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.DELETE("/rules/:id", func(c *gin.Context) {
		ruleID := c.Param("id")
		var ruleType string
		if err := db.QueryRow("SELECT type FROM rules WHERE id=?", ruleID).Scan(&ruleType); err != nil {
			ruleType = ""
		}
		if _, err := db.Exec("DELETE FROM rules WHERE id=?", ruleID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		needMosdns := strings.EqualFold(strings.TrimSpace(ruleType), "domain")
		if err := applyRuleChangeDynamically(needMosdns); err != nil {
			log.Printf("[WARN] dynamic rule delete apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(needMosdns)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
}
