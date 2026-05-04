package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type DNSController struct {
	repo *DNSRepository
}

func NewDNSController(repo *DNSRepository) *DNSController { return &DNSController{repo: repo} }

func (ctl *DNSController) GetDNS(c *gin.Context) {
	local, remote, lazy, mode, logLevel, cacheSize, lazyTTL, err := ctl.repo.GetDNSSettingsWithDefaults()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if strings.TrimSpace(mode) == "" {
		mode = "smart"
		_ = ctl.repo.UpsertSetting("dns_mode", mode)
	}
	c.JSON(http.StatusOK, gin.H{
		"local":      local,
		"remote":     remote,
		"lazy":       lazy == "true",
		"mode":       mode,
		"log_level":  logLevel,
		"cache_size": cacheSize,
		"lazy_ttl":   lazyTTL,
	})
}

func (ctl *DNSController) SetDNS(c *gin.Context) {
	var req struct {
		Local, Remote, Mode string
		LogLevel            string `json:"log_level"`
		CacheSize           int    `json:"cache_size"`
		LazyTTL             int    `json:"lazy_ttl"`
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

	if req.LogLevel != "" {
		_ = ctl.repo.UpsertSetting("dns_log_level", req.LogLevel)
	}
	if req.CacheSize > 0 {
		_ = ctl.repo.UpsertSetting("dns_cache_size", strconv.Itoa(req.CacheSize))
	}
	if req.LazyTTL > 0 {
		_ = ctl.repo.UpsertSetting("dns_lazy_ttl", strconv.Itoa(req.LazyTTL))
	}

	if err := applyMosdnsConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Mosdns failed: " + err.Error()})
		return
	}
	// Clear domain resolve cache to ensure new DNS settings take effect for OSPF
	_, _ = db.Exec("DELETE FROM domain_resolve_cache")
	log.Println("[INFO] Cleared domain_resolve_cache due to DNS settings update")

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
