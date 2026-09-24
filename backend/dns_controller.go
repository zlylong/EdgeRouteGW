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

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "smart"
	}
	if !isValidDNSMode(mode) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dns mode"})
		return
	}
	req.LogLevel = strings.ToLower(strings.TrimSpace(req.LogLevel))
	if req.LogLevel != "" && !isValidMosdnsLogLevel(req.LogLevel) {
		// The value is interpolated into mosdns' YAML; a newline in it would
		// inject arbitrary configuration.
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log_level (debug|info|warn|error)"})
		return
	}
	if req.CacheSize < 0 || req.CacheSize > 10_000_000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "cache_size out of range"})
		return
	}
	if req.LazyTTL < 0 || req.LazyTTL > 30*24*3600 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lazy_ttl out of range (seconds, max 30 days)"})
		return
	}

	// Snapshot the current settings so a failed mosdns apply can put them
	// back instead of leaving the UI showing values mosdns never loaded.
	prevLocal, _ := ctl.repo.GetSetting("dns_local")
	prevRemote, _ := ctl.repo.GetSetting("dns_remote")
	prevLazy, _ := ctl.repo.GetSetting("dns_lazy")
	prevMode, _ := ctl.repo.GetSetting("dns_mode")
	prevLogLevel, _ := ctl.repo.GetSetting("dns_log_level")
	prevCacheSize, _ := ctl.repo.GetSetting("dns_cache_size")
	prevLazyTTL, _ := ctl.repo.GetSetting("dns_lazy_ttl")
	restore := func() {
		_ = ctl.repo.UpsertSetting("dns_local", prevLocal)
		_ = ctl.repo.UpsertSetting("dns_remote", prevRemote)
		_ = ctl.repo.UpsertSetting("dns_lazy", prevLazy)
		_ = ctl.repo.UpsertSetting("dns_mode", prevMode)
		if prevLogLevel != "" {
			_ = ctl.repo.UpsertSetting("dns_log_level", prevLogLevel)
		}
		if prevCacheSize != "" {
			_ = ctl.repo.UpsertSetting("dns_cache_size", prevCacheSize)
		}
		if prevLazyTTL != "" {
			_ = ctl.repo.UpsertSetting("dns_lazy_ttl", prevLazyTTL)
		}
	}
	// The OSPF resolve cache only depends on which upstreams answer and on
	// the mode; a log-level or cache-size change must not throw away every
	// resolved domain (and re-dig all of them on the next sync).
	resolverChanged := strings.TrimSpace(prevLocal) != local || strings.TrimSpace(prevRemote) != remote || strings.TrimSpace(prevMode) != mode

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

	if err := applyMosdnsConfigFn(); err != nil {
		restore()
		log.Printf("[ERR] apply mosdns after dns settings change: %v", err)
		apiError(c, http.StatusInternalServerError, errCodeInternal, "Mosdns apply failed; settings were restored")
		return
	}
	if resolverChanged {
		// Clear domain resolve cache to ensure new DNS settings take effect for OSPF
		_, _ = getDB().Exec("DELETE FROM domain_resolve_cache")
		log.Println("[INFO] Cleared domain_resolve_cache due to DNS upstream/mode update")
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "DNS设置已更新。请确保内网设备的DNS服务器指向本网关地址以实现最佳分流效果。",
	})
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
