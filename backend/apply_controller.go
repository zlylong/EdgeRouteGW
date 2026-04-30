package main

import (
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

type ApplyController struct{}

func NewApplyController() *ApplyController { return &ApplyController{} }

func (ctl *ApplyController) HandleApply(c *gin.Context) {
	if !requireHighRiskMutationGuard(c, "apply_config") {
		return
	}
	releaseLock, ok := tryAcquireHighRiskMutationLock(c, "apply_config")
	if !ok {
		return
	}
	defer releaseLock()

	var req struct {
		Mosdns      *bool `json:"mosdns"`
		Xray        *bool `json:"xray"`
		DynamicXray *bool `json:"dynamic_xray"`
	}
	applyMosdns := true
	applyXray := true
	dynamicXray := true
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "invalid apply payload"})
		return
	}
	if req.Mosdns != nil {
		applyMosdns = *req.Mosdns
	}
	if req.Xray != nil {
		applyXray = *req.Xray
	}
	if req.DynamicXray != nil {
		dynamicXray = *req.DynamicXray
	}

	if isDryRun(c) {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"dry_run": true,
			"action":  "apply_config",
			"plan": gin.H{
				"mosdns":       applyMosdns,
				"xray":         applyXray,
				"dynamic_xray": dynamicXray,
			},
		})
		return
	}

	if applyMosdns {
		if err := applyMosdnsConfig(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Mosdns failed: " + err.Error()})
			return
		}
	}
	if applyXray {
		if dynamicXray {
			if err := applyNodeChangeDynamically(); err != nil {
				log.Printf("[WARN] /api/apply dynamic xray failed, fallback restart: %v", err)
				if err := applyXrayConfig(); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Xray failed: " + err.Error()})
					return
				}
			}
		} else {
			if err := applyXrayConfig(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "Xray failed: " + err.Error()})
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
