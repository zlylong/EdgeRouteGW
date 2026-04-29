package main

import "github.com/gin-gonic/gin"

func registerAuthRoutes(public *gin.RouterGroup, authed *gin.RouterGroup) {
	ctl := NewAuthController()
	public.POST("/login", ctl.Login)
	authed.POST("/password", ctl.ChangePassword)
	authed.POST("/logout", ctl.Logout)
}
