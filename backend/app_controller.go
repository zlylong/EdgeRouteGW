package main

import (
	"log"
	"net/http"
	"os"
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
	// gin trusts every peer as a proxy by default (trustedCIDRs = 0.0.0.0/0 and
	// ::/0) and reads the client address out of X-Forwarded-For / X-Real-IP.
	// On a gateway that listens directly on the LAN that makes c.ClientIP()
	// fully caller-controlled: it forged the source IP recorded in security
	// events and handed anyone a fresh login rate-limit bucket per request.
	// Nothing fronts this server, so trust no proxy and read the peer address.
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Printf("[SECURITY] failed to clear trusted proxies: %v", err)
	}
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
	addr := os.Getenv("PROXYGW_LISTEN_ADDR")
	if addr == "" {
		addr = ":80"
	}
	log.Printf("EdgeRouteGW backend starting on %s", addr)
	r.Run(addr)
}

func init() {
	os.Setenv("TZ", "Asia/Shanghai")
}
