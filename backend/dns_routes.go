package main

import "github.com/gin-gonic/gin"

func registerDNSRoutes(api *gin.RouterGroup) {
	ctl := NewDNSController(NewDNSRepository())
	api.GET("/dns", ctl.GetDNS)
	api.POST("/dns", ctl.SetDNS)
	api.POST("/dns/flush", ctl.FlushDNSCache)
}
