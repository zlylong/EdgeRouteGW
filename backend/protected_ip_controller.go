package main

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type ProtectedIPController struct {
	repo *ProtectedIPRepository
}

func NewProtectedIPController(repo *ProtectedIPRepository) *ProtectedIPController {
	return &ProtectedIPController{repo: repo}
}

func (ctl *ProtectedIPController) List(c *gin.Context) {
	recs, err := ctl.repo.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
		return
	}
	items := make([]map[string]interface{}, 0, len(recs))
	for _, rec := range recs {
		items = append(items, map[string]interface{}{
			"id":         rec.ID,
			"value":      rec.Value,
			"remark":     rec.Remark,
			"created_at": rec.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (ctl *ProtectedIPController) Create(c *gin.Context) {
	var req struct {
		Value  string `json:"value"`
		Remark string `json:"remark"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}
	normalized, ok := normalizeProtectedIPValue(req.Value)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 IPv4 或 IPv4 CIDR"})
		return
	}
	if err := ctl.repo.Create(normalized, strings.TrimSpace(req.Remark)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "该 IP/CIDR 已存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add protected ip"})
		return
	}
	if err := applyNftablesConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (ctl *ProtectedIPController) Delete(c *gin.Context) {
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
