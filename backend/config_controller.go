package main

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

type ConfigController struct{}

func NewConfigController() *ConfigController { return &ConfigController{} }

func (ctl *ConfigController) GetXrayConfig(c *gin.Context) {
	data, err := os.ReadFile(getPath("core", "xray/config.json"))
	if err != nil {
		c.String(http.StatusInternalServerError, "read config failed")
		return
	}
	c.String(http.StatusOK, string(data))
}

func (ctl *ConfigController) GetMosdnsConfig(c *gin.Context) {
	data, err := os.ReadFile(getPath("core", "mosdns/config.yaml"))
	if err != nil {
		c.String(http.StatusInternalServerError, "read config failed")
		return
	}
	c.String(http.StatusOK, string(data))
}

func (ctl *ConfigController) GetNftablesConfig(c *gin.Context) {
	data, err := os.ReadFile("/etc/nftables.conf")
	if err != nil {
		c.String(http.StatusInternalServerError, "read config failed")
		return
	}
	c.String(http.StatusOK, string(data))
}

func (ctl *ConfigController) GetFrrConfig(c *gin.Context) {
	paths := []string{"/etc/frr/frr.conf", getPath("core", "frr", "frr.conf")}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			c.String(http.StatusOK, string(data))
			return
		}
	}
	c.String(http.StatusInternalServerError, "read config failed")
}
