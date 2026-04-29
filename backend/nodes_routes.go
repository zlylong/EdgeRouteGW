package main

import "github.com/gin-gonic/gin"

func registerNodeRoutes(api *gin.RouterGroup) {
	ctl := NewNodesController()
	ctl.RegisterRoutes(api)
}
