package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func normalizeProtectedIPValue(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", false
	}
	if strings.Contains(v, "/") {
		ip, ipNet, err := net.ParseCIDR(v)
		if err != nil || ip == nil || ipNet == nil || ip.To4() == nil {
			return "", false
		}
		ones, _ := ipNet.Mask.Size()
		return ip.Mask(ipNet.Mask).String() + "/" + strconv.Itoa(ones), true
	}
	ip := net.ParseIP(v)
	if ip == nil || ip.To4() == nil {
		return "", false
	}
	return ip.To4().String() + "/32", true
}

func registerProtectedIPRoutes(api *gin.RouterGroup) {
	api.GET("/protected_ips", func(c *gin.Context) {
		rows, err := db.Query("SELECT id, value, remark, created_at FROM protected_ips ORDER BY id DESC")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
			return
		}
		defer rows.Close()
		items := make([]map[string]interface{}, 0)
		for rows.Next() {
			var id int
			var value, remark, createdAt string
			if err := rows.Scan(&id, &value, &remark, &createdAt); err == nil {
				items = append(items, map[string]interface{}{
					"id":         id,
					"value":      value,
					"remark":     remark,
					"created_at": createdAt,
				})
			}
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})

	api.POST("/protected_ips", func(c *gin.Context) {
		var req struct {
			Value  string `json:"value"`
			Remark string `json:"remark"`
		}
		if err := c.BindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
			return
		}
		normalized, ok := normalizeProtectedIPValue(req.Value)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 IPv4 或 IPv4 CIDR"})
			return
		}
		if _, err := db.Exec("INSERT INTO protected_ips (value, remark) VALUES (?, ?)", normalized, strings.TrimSpace(req.Remark)); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				c.JSON(http.StatusConflict, gin.H{"error": "该 IP/CIDR 已存在"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add protected ip"})
			return
		}
		if err := applyNftablesConfig(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.DELETE("/protected_ips/:id", func(c *gin.Context) {
		id := c.Param("id")
		if _, err := db.Exec("DELETE FROM protected_ips WHERE id=?", id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
			return
		}
		if err := applyNftablesConfig(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
}
