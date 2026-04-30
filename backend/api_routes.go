package main

import (
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
	authed.Use(requestTraceMiddleware)
	authed.Use(auditEventMiddleware)
	registerAuthRoutes(api, authed)

	registerConfigRoutes(authed)
	registerSystemRoutes(authed)
	registerNodeRoutes(authed)
	registerRuleRoutes(authed)
	registerDNSRoutes(authed)
	registerLanACLRoutes(authed)
	registerProtectedIPRoutes(authed)
	registerConnectionRoutes(authed)
	registerUpdateRoutes(authed)
	registerRemoteNodeRoutes(authed)
	registerSyslogsRoutes(authed)
	registerEventRoutes(authed)
	registerNftablesRoutes(authed)

	testCtl := NewTestController()
	testCtl.RegisterRoutes(authed)

	applyCtl := NewApplyController()
	authed.POST("/apply", applyCtl.HandleApply)
}
