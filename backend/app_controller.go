package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// AppController encapsulates HTTP route wiring and server lifecycle.
type AppController struct{}

func NewAppController() *AppController {
	return &AppController{}
}

func (c *AppController) BuildRouter() *gin.Engine {
	// gin defaults to debug mode, which dumps the whole route table and a
	// "switch to release mode in production" warning into the journal on every
	// start. An explicit GIN_MODE still wins.
	if os.Getenv(gin.EnvGinMode) == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	// Same middleware as gin.Default(), minus access-log lines for the two
	// endpoints the dashboard polls every two seconds.
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{SkipPaths: []string{"/api/status", "/api/traffic"}}), gin.Recovery())
	r.Use(securityHeadersMiddleware(), staticCacheMiddleware(), gzipMiddleware())
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
	if err := serveWithGracefulShutdown(newHTTPServer(addr, r)); err != nil {
		log.Fatalf("HTTP server: %v", err)
	}
}

func init() {
	os.Setenv("TZ", "Asia/Shanghai")
}
