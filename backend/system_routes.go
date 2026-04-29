package main

import "github.com/gin-gonic/gin"

func registerSystemRoutes(api *gin.RouterGroup) {
	sysCtl := NewSystemController(NewSystemRepository())
	api.GET("/status", sysCtl.HandleStatus)
	api.POST("/network_config", sysCtl.HandleNetworkConfig)
	api.POST("/mode", sysCtl.HandleMode)
	api.GET("/cron", sysCtl.HandleGetCron)
	api.POST("/cron", sysCtl.HandleSetCron)
	api.GET("/traffic", sysCtl.HandleTraffic)
	api.GET("/ospf", sysCtl.HandleGetOspf)
	api.POST("/ospf/settings", sysCtl.HandleOspfSettings)
	api.POST("/ospf/reset_pending", sysCtl.HandleResetOspfPending)
}
