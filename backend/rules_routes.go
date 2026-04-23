package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func splitRuleBatchValues(raw string) []string {
	normalized := strings.NewReplacer("，", ",", "\n", ",", "\r", ",", "\t", ",").Replace(raw)
	parts := strings.Split(normalized, ",")
	seen := make(map[string]struct{}, len(parts))
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func newRuleGroupID() string {
	return fmt.Sprintf("rg-%x", time.Now().UnixNano())
}

func rulesContainBatchSeparator(raw string) bool {
	return strings.ContainsAny(raw, ",，\n\r\t")
}

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
		rows, err := db.Query("SELECT id, type, value, policy, COALESCE(group_id, '') FROM rules ORDER BY id ASC")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
			return
		}
		defer rows.Close()
		var rules []map[string]interface{}
		for rows.Next() {
			var id int
			var rtype, value, policy, groupID string
			if err := rows.Scan(&id, &rtype, &value, &policy, &groupID); err != nil {
				continue
			}
			rules = append(rules, map[string]interface{}{"id": id, "type": rtype, "value": value, "policy": policy, "group_id": groupID})
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

		values := []string{r.Value}
		if r.Type == "domain" {
			values = splitRuleBatchValues(r.Value)
			if len(values) == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "type/value/policy required"})
				return
			}
			for _, value := range values {
				if _, err := parseDomainRulePattern(value); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if currentMode() != "A" && isWildcardDomainRuleValue(value) {
					c.JSON(http.StatusBadRequest, gin.H{"error": "wildcard domain rules (*.example.com / **.example.com) only support Mode A; Mode B/C cannot expand them into OSPF static routes"})
					return
				}
			}
		} else if rulesContainBatchSeparator(r.Value) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "only domain rules support comma-separated batch add"})
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

		groupID := ""
		if r.Type == "domain" && len(values) > 1 {
			groupID = newRuleGroupID()
		}
		tx, err := db.Begin()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		for _, value := range values {
			if _, err := tx.Exec("INSERT INTO rules (type, value, policy, group_id) VALUES (?, ?, ?, ?)", r.Type, value, r.Policy, groupID); err != nil {
				_ = tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
				return
			}
		}
		if err := tx.Commit(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		needMosdns := r.Type == "domain"
		if err := applyRuleChangeDynamically(needMosdns); err != nil {
			log.Printf("[WARN] dynamic rule apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(needMosdns)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "count": len(values), "group_id": groupID})
	})

	api.DELETE("/rules/group/:group_id", func(c *gin.Context) {
		groupID := strings.TrimSpace(c.Param("group_id"))
		if groupID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "group_id required"})
			return
		}
		var domainCount int
		if err := db.QueryRow("SELECT COUNT(*) FROM rules WHERE group_id=? AND type='domain'", groupID).Scan(&domainCount); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		res, err := db.Exec("DELETE FROM rules WHERE group_id=?", groupID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
			return
		}
		needMosdns := domainCount > 0
		if err := applyRuleChangeDynamically(needMosdns); err != nil {
			log.Printf("[WARN] dynamic rule group delete apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(needMosdns)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "deleted": affected})
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
