package main

import "github.com/gin-gonic/gin"

func registerRuleRoutes(api *gin.RouterGroup) {
	ctl := NewRulesController(NewRulesRepository())
	ctl.RegisterRoutes(api)
}
