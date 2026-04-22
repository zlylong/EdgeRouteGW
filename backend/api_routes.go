package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func authMiddleware(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		c.AbortWithStatusJSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if !validateSession(token) {
		c.AbortWithStatusJSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	c.Next()
}

func registerAPIRoutes(r *gin.Engine) {
	api := r.Group("/api")

	authed := api.Group("")
	authed.Use(authMiddleware)
	registerAuthRoutes(api, authed)

	registerConfigRoutes(authed)
	registerSystemRoutes(authed)
	registerNodeRoutes(authed)
	registerRuleRoutes(authed)
	registerDNSRoutes(authed)
	registerLanACLRoutes(authed)
	registerConnectionRoutes(authed)
	registerUpdateRoutes(authed)
	registerRemoteNodeRoutes(authed)
	registerSyslogsRoutes(authed)

	authed.POST("/apply", func(c *gin.Context) {
		var req struct {
			Mosdns      *bool `json:"mosdns"`
			Xray        *bool `json:"xray"`
			DynamicXray *bool `json:"dynamic_xray"`
		}
		applyMosdns := true
		applyXray := true
		dynamicXray := true
		if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid apply payload"})
			return
		}
		if req.Mosdns != nil {
			applyMosdns = *req.Mosdns
		}
		if req.Xray != nil {
			applyXray = *req.Xray
		}
		if req.DynamicXray != nil {
			dynamicXray = *req.DynamicXray
		}

		if applyMosdns {
			if err := applyMosdnsConfig(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Mosdns failed: " + err.Error()})
				return
			}
		}
		if applyXray {
			if dynamicXray {
				if err := applyNodeChangeDynamically(); err != nil {
					log.Printf("[WARN] /api/apply dynamic xray failed, fallback restart: %v", err)
					if err := applyXrayConfig(); err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Xray failed: " + err.Error()})
						return
					}
				}
			} else {
				if err := applyXrayConfig(); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Xray failed: " + err.Error()})
					return
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
}
