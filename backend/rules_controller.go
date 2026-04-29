package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
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

func collectRuleGroups() []map[string]interface{} {
	rows, err := db.Query(`SELECT group_id, COALESCE(NULLIF(group_name, ''), group_id) AS display_name, COUNT(*) AS rule_count FROM rules WHERE COALESCE(group_id, '') <> '' GROUP BY group_id, group_name ORDER BY MIN(id) ASC`)
	if err != nil {
		return []map[string]interface{}{}
	}
	defer rows.Close()
	groups := make([]map[string]interface{}, 0)
	for rows.Next() {
		var groupID, displayName string
		var ruleCount int
		if err := rows.Scan(&groupID, &displayName, &ruleCount); err != nil {
			continue
		}
		groups = append(groups, map[string]interface{}{
			"group_id":   groupID,
			"group_name": strings.TrimSpace(displayName),
			"rule_count": ruleCount,
		})
	}
	if groups == nil {
		return []map[string]interface{}{}
	}
	return groups
}

func newRuleGroupID() string {
	return fmt.Sprintf("rg-%x", time.Now().UnixNano())
}

func rulesContainBatchSeparator(raw string) bool {
	return strings.ContainsAny(raw, ",，\n\r\t")
}

var policySingleNodeRe = regexp.MustCompile(`^proxy-(\d+)$`)
var policyHARe = regexp.MustCompile(`^ha-(\d+)-(\d+)$`)

func validateRulePolicy(policy string) error {
	if policy == "direct" || policy == "block" || policy == "proxy" {
		return nil
	}
	if m := policySingleNodeRe.FindStringSubmatch(policy); len(m) == 2 {
		nodeID, _ := strconv.Atoi(m[1])
		var cnt int
		if err := db.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=?", nodeID).Scan(&cnt); err != nil {
			return fmt.Errorf("invalid policy")
		}
		if cnt == 0 {
			return fmt.Errorf("invalid policy: node %d not found", nodeID)
		}
		return nil
	}
	if m := policyHARe.FindStringSubmatch(policy); len(m) == 3 {
		aID, _ := strconv.Atoi(m[1])
		bID, _ := strconv.Atoi(m[2])
		if aID == bID {
			return fmt.Errorf("invalid policy: HA nodes must be different")
		}
		var cnt int
		if err := db.QueryRow("SELECT COUNT(*) FROM nodes WHERE id IN (?,?)", aID, bID).Scan(&cnt); err != nil {
			return fmt.Errorf("invalid policy")
		}
		if cnt != 2 {
			return fmt.Errorf("invalid policy: HA node not found")
		}
		return nil
	}
	return fmt.Errorf("invalid policy")
}

type RulesController struct{}

func NewRulesController() *RulesController { return &RulesController{} }

func (ctl *RulesController) RegisterRoutes(api *gin.RouterGroup) {
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
		groupFilter := strings.TrimSpace(c.Query("group_id"))
		query := "SELECT id, type, value, policy, COALESCE(group_id, ''), COALESCE(group_name, '') FROM rules"
		args := make([]interface{}, 0, 1)
		if groupFilter != "" {
			query += " WHERE COALESCE(group_id, '') = ?"
			args = append(args, groupFilter)
		}
		query += " ORDER BY id ASC"
		rows, err := db.Query(query, args...)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
			return
		}
		defer rows.Close()
		var rules []map[string]interface{}
		for rows.Next() {
			var id int
			var rtype, value, policy, groupID, groupName string
			if err := rows.Scan(&id, &rtype, &value, &policy, &groupID, &groupName); err != nil {
				continue
			}
			rules = append(rules, map[string]interface{}{"id": id, "type": rtype, "value": value, "policy": policy, "group_id": groupID, "group_name": groupName})
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
		c.JSON(http.StatusOK, gin.H{"rules": rules, "groups": collectRuleGroups()})
	})

	api.POST("/rules", func(c *gin.Context) {
		var r struct{ Type, Value, Policy, GroupName string }
		if c.BindJSON(&r) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}

		r.Type = strings.ToLower(strings.TrimSpace(r.Type))
		r.Value = strings.TrimSpace(r.Value)
		r.Policy = strings.ToLower(strings.TrimSpace(r.Policy))
		r.GroupName = strings.TrimSpace(r.GroupName)
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
		} else if r.Type == "geosite" {
			geositeTag := strings.ToLower(strings.TrimSpace(r.Value))
			if geositeTag == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid geosite rule value"})
				return
			}
			geositePath := getPath("core", "mosdns", "geosite.dat")
			if !hasGeoSiteTag(geositePath, geositeTag) {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid geosite tag: %s", geositeTag)})
				return
			}
			r.Value = geositeTag
		} else if rulesContainBatchSeparator(r.Value) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "only domain rules support comma-separated batch add"})
			return
		}

		if r.Type == "geoip" || r.Type == "geolocation" {
			geoTag := strings.ToLower(strings.TrimSpace(r.Value))
			if geoTag == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid geoip/geolocation rule value"})
				return
			}
			if geoTag != "private" {
				geoipPath := getPath("core", "mosdns", "geoip.dat")
				if !hasGeoIPTag(geoipPath, geoTag) {
					c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid geoip tag: %s", geoTag)})
					return
				}
			}
			r.Value = geoTag
		}
		if r.Type == "ip" && !isValidIPOrCIDR(r.Value) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid ip/cidr rule value"})
			return
		}
		if err := validateRulePolicy(r.Policy); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		groupID := ""
		groupName := ""
		if r.Type == "domain" && len(values) > 1 {
			groupID = newRuleGroupID()
			groupName = r.GroupName
		}
		tx, err := db.Begin()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		for _, value := range values {
			if _, err := tx.Exec("INSERT INTO rules (type, value, policy, group_id, group_name) VALUES (?, ?, ?, ?, ?)", r.Type, value, r.Policy, groupID, groupName); err != nil {
				_ = tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
				return
			}
		}
		if err := tx.Commit(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		needMosdns := r.Type == "domain" || r.Type == "geosite"
		if err := applyRuleChangeDynamically(needMosdns); err != nil {
			log.Printf("[WARN] dynamic rule apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(needMosdns)
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "count": len(values), "group_id": groupID, "group_name": groupName})
	})

	api.PUT("/rules/group/:group_id", func(c *gin.Context) {
		groupID := strings.TrimSpace(c.Param("group_id"))
		if groupID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "group_id required"})
			return
		}
		var payload struct {
			GroupName string `json:"group_name"`
		}
		if c.BindJSON(&payload) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		payload.GroupName = strings.TrimSpace(payload.GroupName)
		res, err := db.Exec("UPDATE rules SET group_name=? WHERE group_id=?", payload.GroupName, groupID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "group_id": groupID, "group_name": payload.GroupName})
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
		needMosdns := strings.EqualFold(strings.TrimSpace(ruleType), "domain") || strings.EqualFold(strings.TrimSpace(ruleType), "geosite")
		if err := applyRuleChangeDynamically(needMosdns); err != nil {
			log.Printf("[WARN] dynamic rule delete apply failed, fallback to scheduled apply: %v", err)
			scheduleApplyFallbackIfRuntimeReady(needMosdns)
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
}
