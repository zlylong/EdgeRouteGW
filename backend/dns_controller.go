package main

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type DNSController struct {
	repo *DNSRepository
}

func NewDNSController(repo *DNSRepository) *DNSController { return &DNSController{repo: repo} }

func (ctl *DNSController) GetDNS(c *gin.Context) {
	local, remote, lazy, mode, err := ctl.repo.GetDNSSettingsWithDefaults()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if strings.TrimSpace(mode) == "" {
		mode = "smart"
		_ = ctl.repo.UpsertSetting("dns_mode", mode)
	}
	c.JSON(http.StatusOK, gin.H{"local": local, "remote": remote, "lazy": lazy == "true", "mode": mode})
}

func (ctl *DNSController) SetDNS(c *gin.Context) {
	var req struct {
		Local, Remote, Mode string
		Lazy                bool
	}
	if c.BindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}

	local, ok := normalizeUpstreamCSV(req.Local)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid local upstream"})
		return
	}
	remote, ok := normalizeUpstreamCSV(req.Remote)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid remote upstream"})
		return
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "smart"
	}
	if err := ctl.repo.UpdateSetting("dns_local", local); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := ctl.repo.UpdateSetting("dns_remote", remote); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := ctl.repo.UpdateSetting("dns_lazy", boolToString(req.Lazy)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := ctl.repo.UpsertSetting("dns_mode", mode); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := applyMosdnsConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Mosdns failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
