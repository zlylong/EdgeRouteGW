package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AppController encapsulates HTTP route wiring and server lifecycle.
type AppController struct{}

func NewAppController() *AppController {
	return &AppController{}
}

func (c *AppController) BuildRouter() *gin.Engine {
	r := gin.Default()
	registerAPIRoutes(r)
	r.Use(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/ui") {
			c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
		}
		c.Next()
	})
	r.Static("/ui", getPath("frontend", "dist"))
	r.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/ui/") })
	return r
}

func (c *AppController) Run(r *gin.Engine) {
	log.Println("EdgeRouteGW backend starting on :80")
	r.Run(":80")
}
