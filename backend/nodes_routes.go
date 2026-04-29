package main

import "github.com/gin-gonic/gin"

func registerNodeRoutes(api *gin.RouterGroup) {
	ctl := NewNodesController(NewNodesRepository())
	ctl.RegisterRoutes(api)
}
