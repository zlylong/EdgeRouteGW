package main

import (
	"net/http"
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
		if r.Type == "geoip" && strings.HasPrefix(r.Value, "!") {
			var mode string
			if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
				mode = "A"
			}
			if mode == "B" || mode == "C" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "geoip 反向标签（如 !cn）在 OSPF 模式(B/C)无法展开为静态路由；请改用显式 geoip 标签/IP 规则，或切换到 Mode A"})
				return
			}
		}
		if r.Policy != "direct" && r.Policy != "block" && !strings.HasPrefix(r.Policy, "proxy") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid policy"})
			return
		}

		if _, err := db.Exec("INSERT INTO rules (type, value, policy) VALUES (?, ?, ?)", r.Type, r.Value, r.Policy); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		scheduleApply()
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.DELETE("/rules/:id", func(c *gin.Context) {
		if _, err := db.Exec("DELETE FROM rules WHERE id=?", c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
			return
		}
		scheduleApply()
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
}
