package main

import (
	"errors"
	"log"
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
	window, ok := parsePageWindow(c)
	if !ok {
		return
	}
	recs, err := ctl.repo.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
		return
	}
	recs = pageSlice(c, window, recs)
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
	newID, err := ctl.repo.CreateReturningID(normalized, strings.TrimSpace(req.Remark))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "该 IP/CIDR 已存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add protected ip"})
		return
	}
	if err := applyNftablesConfigFn(); err != nil {
		if rbErr := ctl.repo.DeleteByID(newID); rbErr != nil {
			log.Printf("[WARN] rollback of protected ip %d after failed apply: %v", newID, rbErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (ctl *ProtectedIPController) Delete(c *gin.Context) {
	id := c.Param("id")
	prev, getErr := ctl.repo.Get(id)
	if err := ctl.repo.Delete(id); err != nil {
		if errors.Is(err, errNotFound) {
			apiNotFound(c, "protected ip")
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete failed"})
		return
	}
	if err := applyNftablesConfigFn(); err != nil {
		if getErr == nil {
			if rbErr := ctl.repo.Restore(prev); rbErr != nil {
				log.Printf("[WARN] restore of protected ip %s after failed apply: %v", id, rbErr)
			}
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
