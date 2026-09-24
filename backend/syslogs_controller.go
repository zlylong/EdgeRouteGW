package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type SyslogsController struct{}

func NewSyslogsController() *SyslogsController { return &SyslogsController{} }

// runJournalctl is the seam the logs endpoint reads through.
var runJournalctl = func(args ...string) ([]byte, error) {
	res := sysCmd.runCombinedOutput("journalctl", args...)
	return res.Output, res.Err
}

// The logs tab re-polls the current service every two seconds. journalctl on
// a busy unit is not free, so identical requests inside syslogCacheTTL share
// one result.
const syslogCacheTTL = 2 * time.Second

type syslogCacheEntry struct {
	at   time.Time
	logs string
}

var (
	syslogCacheMu sync.Mutex
	syslogCache   = map[string]syslogCacheEntry{}
)

func (ctl *SyslogsController) GetServiceLogs(c *gin.Context) {
	service := c.Param("service")
	if service != "proxygw" && service != "xray" && service != "mosdns" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid service name"})
		return
	}

	key := service + "|200"
	syslogCacheMu.Lock()
	if entry, ok := syslogCache[key]; ok && time.Since(entry.at) < syslogCacheTTL {
		syslogCacheMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"success": true, "logs": entry.logs})
		return
	}
	syslogCacheMu.Unlock()

	out, err := runJournalctl("-u", service, "-n", "200", "--no-pager")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to fetch logs: %v\nOutput: %s", err, string(out))})
		return
	}
	logs := string(out)
	syslogCacheMu.Lock()
	syslogCache[key] = syslogCacheEntry{at: time.Now(), logs: logs}
	syslogCacheMu.Unlock()

	c.JSON(http.StatusOK, gin.H{"success": true, "logs": logs})
}
