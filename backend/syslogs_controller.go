package main

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

type SyslogsController struct{}

func NewSyslogsController() *SyslogsController { return &SyslogsController{} }

func (ctl *SyslogsController) GetServiceLogs(c *gin.Context) {
	service := c.Param("service")
	if service != "proxygw" && service != "xray" && service != "mosdns" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid service name"})
		return
	}

	res := sysCmd.runCombinedOutput("journalctl", "-u", service, "-n", "200", "--no-pager")
	out, err := res.Output, res.Err
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to fetch logs: %v\nOutput: %s", err, string(out))})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"logs":    string(out),
	})
}
