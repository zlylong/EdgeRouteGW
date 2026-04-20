package main

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

func registerConfigRoutes(api *gin.RouterGroup) {
	api.GET("/config/xray", func(c *gin.Context) {
		data, err := os.ReadFile(getPath("core", "xray/config.json"))
		if err != nil {
			c.String(http.StatusInternalServerError, "read config failed")
			return
		}
		c.String(http.StatusOK, string(data))
	})

	api.GET("/config/mosdns", func(c *gin.Context) {
		data, err := os.ReadFile(getPath("core", "mosdns/config.yaml"))
		if err != nil {
			c.String(http.StatusInternalServerError, "read config failed")
			return
		}
		c.String(http.StatusOK, string(data))
	})

	api.GET("/config/nftables", func(c *gin.Context) {
		data, err := os.ReadFile("/etc/nftables.conf")
		if err != nil {
			c.String(http.StatusInternalServerError, "read config failed")
			return
		}
		c.String(http.StatusOK, string(data))
	})

	api.GET("/config/frr", func(c *gin.Context) {
		paths := []string{"/etc/frr/frr.conf", getPath("core", "frr", "frr.conf")}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err == nil {
				c.String(http.StatusOK, string(data))
				return
			}
		}
		c.String(http.StatusInternalServerError, "read config failed")
	})
}
