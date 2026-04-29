package main

import "github.com/gin-gonic/gin"

func registerLanACLRoutes(api *gin.RouterGroup) {
	ctl := NewLanACLController(NewLanACLRepository())
	api.GET("/lan_acls", ctl.List)
	api.POST("/lan_acls", ctl.Create)
	api.DELETE("/lan_acls/:id", ctl.Delete)
	api.POST("/lan_acls/default_policy", ctl.SetDefaultPolicy)
}
