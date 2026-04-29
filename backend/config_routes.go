package main

import "github.com/gin-gonic/gin"

func registerConfigRoutes(api *gin.RouterGroup) {
	ctl := NewConfigController()
	api.GET("/config/xray", ctl.GetXrayConfig)
	api.GET("/config/mosdns", ctl.GetMosdnsConfig)
	api.GET("/config/nftables", ctl.GetNftablesConfig)
	api.GET("/config/frr", ctl.GetFrrConfig)
}
