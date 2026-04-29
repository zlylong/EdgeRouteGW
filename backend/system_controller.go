package main

import (
	"database/sql"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type SystemController struct {
	repo *SystemRepository
}

func NewSystemController(repo *SystemRepository) *SystemController {
	return &SystemController{repo: repo}
}

func (ctl *SystemController) HandleNetworkConfig(c *gin.Context) {
	if !requireHighRiskMutationGuard(c, "network_config") {
		return
	}
	var req struct {
		ManagementIface string `json:"management_iface"`
		ServiceIface    string `json:"service_iface"`
	}
	if c.BindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad network config payload"})
		return
	}
	req.ManagementIface = strings.TrimSpace(req.ManagementIface)
	req.ServiceIface = strings.TrimSpace(req.ServiceIface)
	if req.ManagementIface == "" || req.ServiceIface == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "management/service iface is required"})
		return
	}
	options := listPrivateIPv4Interfaces()
	_, okMgmt := findNetworkByIface(options, req.ManagementIface)
	_, okSvc := findNetworkByIface(options, req.ServiceIface)
	if !okMgmt || !okSvc {
		c.JSON(http.StatusBadRequest, gin.H{"error": "selected iface not found in available private interfaces"})
		return
	}
	if isDryRun(c) {
		c.JSON(http.StatusOK, gin.H{"success": true, "dry_run": true, "action": "network_config", "plan": gin.H{"management_iface": req.ManagementIface, "service_iface": req.ServiceIface, "actions": []string{"update settings.management_iface", "update settings.service_iface", "syncFRRConfig"}}})
		return
	}
	if err := ctl.repo.SaveNetworkRoleSettings(req.ManagementIface, req.ServiceIface); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	syncFRRConfig()
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (ctl *SystemController) HandleMode(c *gin.Context) {
	if !requireHighRiskMutationGuard(c, "mode_switch") {
		return
	}
	var req struct{ Mode string }
	if c.BindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad mode payload"})
		return
	}
	req.Mode = strings.TrimSpace(req.Mode)
	if req.Mode != "A" && req.Mode != "B" && req.Mode != "C" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode must be A, B, or C"})
		return
	}
	if isDryRun(c) {
		c.JSON(http.StatusOK, gin.H{"success": true, "dry_run": true, "action": "mode_switch", "plan": gin.H{"mode": req.Mode, "actions": []string{"set mode", "syncFRRConfig", "applyNftablesConfig", "applyMosdnsConfig", "applyXrayConfig", "service reconcile", "route state finalize"}}})
		return
	}
	if err := applyModeChange(req.Mode); err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, sql.ErrConnDone):
			msg = "db error"
		case strings.Contains(msg, "nft") || strings.Contains(strings.ToLower(msg), "nftables"):
			msg = "Nftables failed: " + err.Error()
		case strings.Contains(strings.ToLower(msg), "mosdns"):
			msg = "Mosdns failed: " + err.Error()
		case strings.Contains(strings.ToLower(msg), "xray"):
			msg = "Xray failed: " + err.Error()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": msg})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (ctl *SystemController) HandleOspfSettings(c *gin.Context) {
	if !requireHighRiskMutationGuard(c, "ospf_settings") {
		return
	}
	var req struct {
		PushBatchLimit     int    `json:"push_batch_limit"`
		PushIntervalSecond int    `json:"push_interval_seconds"`
		ResolveWorkers     int    `json:"resolve_workers"`
		PublishIPAllowlist string `json:"publish_ip_allowlist"`
	}
	if c.BindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad ospf settings payload"})
		return
	}

	batchLimit := clampOspfPushBatchLimit(req.PushBatchLimit)
	intervalSeconds := clampOspfPushIntervalSeconds(req.PushIntervalSecond)
	resolveWorkers := clampOspfResolveWorkers(req.ResolveWorkers)
	allowlist := strings.TrimSpace(req.PublishIPAllowlist)
	allowParts := strings.Split(allowlist, ",")
	for _, p := range allowParts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil || n == nil || n.IP.To4() == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid publish_ip_allowlist CIDR: " + p})
			return
		}
	}
	if isDryRun(c) {
		c.JSON(http.StatusOK, gin.H{"success": true, "dry_run": true, "action": "ospf_settings", "plan": gin.H{"push_batch_limit": batchLimit, "push_interval_seconds": intervalSeconds, "resolve_workers": resolveWorkers, "publish_ip_allowlist": allowlist}})
		return
	}

	if err := ctl.repo.SaveOspfSettings(batchLimit, intervalSeconds, resolveWorkers, allowlist); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success":                 true,
		"push_batch_limit":        batchLimit,
		"push_interval_seconds":   intervalSeconds,
		"resolve_workers":         resolveWorkers,
		"publish_ip_allowlist":    allowlist,
		"publish_ip_allowlist_on": allowlist != "",
	})
}
