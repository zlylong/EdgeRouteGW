package main

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type LanACLController struct {
	repo *LanACLRepository
}

func NewLanACLController(repo *LanACLRepository) *LanACLController {
	return &LanACLController{repo: repo}
}

func (ctl *LanACLController) List(c *gin.Context) {
	recs, err := ctl.repo.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
		return
	}
	acls := make([]map[string]interface{}, 0, len(recs))
	for _, rec := range recs {
		acls = append(acls, map[string]interface{}{
			"id":         rec.ID,
			"type":       rec.Type,
			"value":      rec.Value,
			"policy":     rec.Policy,
			"remark":     rec.Remark,
			"created_at": rec.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"acls": acls, "default_policy": ctl.repo.GetDefaultPolicy()})
}

func (ctl *LanACLController) Create(c *gin.Context) {
	var req struct {
		Type   string `json:"type"`
		Value  string `json:"value"`
		Policy string `json:"policy"`
		Remark string `json:"remark"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}

	// The value is interpolated verbatim into the nftables set template
	// (applyNftablesConfig), so it must be a well-formed MAC or IP/CIDR — an
	// arbitrary string could corrupt or inject into the generated ruleset.
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	req.Policy = strings.ToLower(strings.TrimSpace(req.Policy))
	req.Value = strings.TrimSpace(req.Value)
	if req.Type != "mac" && req.Type != "ip" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid type (mac|ip)"})
		return
	}
	if req.Policy != "proxy" && req.Policy != "direct" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid policy (proxy|direct)"})
		return
	}
	if req.Type == "mac" {
		if _, err := net.ParseMAC(req.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid MAC address"})
			return
		}
	} else if !isValidIPOrCIDR(req.Value) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IP/CIDR"})
		return
	}

	if err := ctl.repo.Create(req.Type, req.Value, req.Policy, req.Remark); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add acl"})
		return
	}
	if err := applyNftablesConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (ctl *LanACLController) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := ctl.repo.Delete(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	if err := applyNftablesConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (ctl *LanACLController) SetDefaultPolicy(c *gin.Context) {
	var req struct {
		Policy string `json:"policy"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	if err := ctl.repo.SetDefaultPolicy(req.Policy); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := applyNftablesConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
