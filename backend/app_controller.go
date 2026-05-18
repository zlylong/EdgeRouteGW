package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// respondError logs the real error server-side and returns a generic message to the client.
// Use this for all "something went wrong" responses where we don't want to leak internals.
func respondError(c *gin.Context, status int, err error, contextMsg string) {
	if err != nil {
		log.Printf("[ERR] %s: %v", contextMsg, err)
	}
	c.JSON(status, gin.H{"error": contextMsg})
}

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
	r.StaticFile("/favicon.ico", getPath("frontend", "dist", "favicon.ico"))
	r.Static("/ui", getPath("frontend", "dist"))
	r.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/ui/") })
	return r
}

func (c *AppController) Run(r *gin.Engine) {
	log.Println("EdgeRouteGW backend starting on :80")
	r.Run(":80")
}

func getLogWriter() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		c.Next()
	}
}

func init() {
	os.Setenv("TZ", "Asia/Shanghai")
}
