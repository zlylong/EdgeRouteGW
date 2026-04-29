package main

import "github.com/gin-gonic/gin"

func registerSyslogsRoutes(r *gin.RouterGroup) {
	ctl := NewSyslogsController()
	r.GET("/logs/:service", ctl.GetServiceLogs)
}
