package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type LanACLController struct{}

func NewLanACLController() *LanACLController { return &LanACLController{} }

func (ctl *LanACLController) List(c *gin.Context) {
	rows, err := db.Query("SELECT id, type, value, policy, remark, created_at FROM lan_acls ORDER BY id DESC")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db query error"})
		return
	}
	defer rows.Close()

	var acls []map[string]interface{}
	for rows.Next() {
		var id int
		var atype, value, policy, remark, createdAt string
		if err := rows.Scan(&id, &atype, &value, &policy, &remark, &createdAt); err == nil {
			acls = append(acls, map[string]interface{}{"id": id, "type": atype, "value": value, "policy": policy, "remark": remark, "created_at": createdAt})
		}
	}

	var defaultPolicy string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='lan_default_policy'").Scan(&defaultPolicy); err != nil {
		defaultPolicy = "proxy"
	}
	c.JSON(http.StatusOK, gin.H{"acls": acls, "default_policy": defaultPolicy})
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
	_, err := db.Exec("INSERT INTO lan_acls (type, value, policy, remark) VALUES (?, ?, ?, ?)", req.Type, req.Value, req.Policy, req.Remark)
	if err != nil {
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
	_, err := db.Exec("DELETE FROM lan_acls WHERE id=?", id)
	if err != nil {
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
	if _, err := db.Exec("UPDATE settings SET value=? WHERE key='lan_default_policy'", req.Policy); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db error"})
		return
	}
	if err := applyNftablesConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply nftables: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
