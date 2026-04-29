package main

import "github.com/gin-gonic/gin"

func registerRemoteNodeRoutes(authed *gin.RouterGroup) {
	ctl := NewRemoteNodesController()
	ctl.RegisterRoutes(authed)
}
