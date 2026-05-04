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
		"hint":       "请务必将内网设备的 DNS 设置指向本代理网关 IP，以确保分流规则生效。",
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

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "DNS设置已更新。请确保内网设备的DNS服务器指向本网关地址以实现最佳分流效果。",
	})
}

func (ctl *DNSController) FlushDNSCache(c *gin.Context) {
	if _, err := db.Exec("DELETE FROM domain_resolve_cache"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": err.Error()})
		return
	}
	log.Println("[INFO] Manual triggered domain_resolve_cache flush from UI")
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "代理域名解析缓存已清空，系统将在后台自动重新解析"})
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
