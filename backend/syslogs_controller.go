package main

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
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

// syslogServices maps the API name to the systemd unit.
var syslogServices = map[string]string{
	"proxygw":  "proxygw",
	"xray":     "xray",
	"mosdns":   "mosdns",
	"frr":      "frr",
	"nftables": "nftables",
}

var (
	syslogPriorities = map[string]struct{}{"emerg": {}, "alert": {}, "crit": {}, "err": {}, "warning": {}, "notice": {}, "info": {}, "debug": {}, "0": {}, "1": {}, "2": {}, "3": {}, "4": {}, "5": {}, "6": {}, "7": {}}
	// syslogGrepRe limits --grep to a plain word pattern; the value is passed
	// as an argv element (never through a shell) but journalctl compiles it
	// as a regex, so keep it simple.
	syslogGrepRe = regexp.MustCompile(`^[A-Za-z0-9 ._:@/=-]{1,64}$`)
)

const (
	syslogDefaultLines = 200
	syslogMinLines     = 50
	syslogMaxLines     = 2000
)

// buildJournalctlArgs validates the query parameters and turns them into a
// journalctl argument list. It never returns anything shell-interpreted.
func buildJournalctlArgs(service, lines, since, priority, grep string) ([]string, error) {
	unit, ok := syslogServices[service]
	if !ok {
		return nil, fmt.Errorf("invalid service name")
	}
	args := []string{"-u", unit, "--no-pager"}
	n := syslogDefaultLines
	if lines != "" {
		v, err := strconv.Atoi(lines)
		if err != nil {
			return nil, fmt.Errorf("lines must be an integer")
		}
		if v < syslogMinLines {
			v = syslogMinLines
		}
		if v > syslogMaxLines {
			v = syslogMaxLines
		}
		n = v
	}
	args = append(args, "-n", strconv.Itoa(n))
	if since != "" {
		t, ok := parseSinceParam(since)
		if !ok {
			return nil, fmt.Errorf("since must be RFC3339 or a duration like 15m, 2h, 1d")
		}
		args = append(args, "--since", t.Format("2006-01-02 15:04:05"))
	}
	if priority != "" {
		if _, ok := syslogPriorities[strings.ToLower(priority)]; !ok {
			return nil, fmt.Errorf("priority must be 0-7 or a syslog level name")
		}
		args = append(args, "-p", strings.ToLower(priority))
	}
	if grep != "" {
		if !syslogGrepRe.MatchString(grep) {
			return nil, fmt.Errorf("grep contains unsupported characters")
		}
		args = append(args, "--grep", grep)
	}
	return args, nil
}

func (ctl *SyslogsController) GetServiceLogs(c *gin.Context) {
	service := c.Param("service")
	args, err := buildJournalctlArgs(service,
		strings.TrimSpace(c.Query("lines")),
		strings.TrimSpace(c.Query("since")),
		strings.TrimSpace(c.Query("priority")),
		strings.TrimSpace(c.Query("grep")))
	if err != nil {
		apiError(c, http.StatusBadRequest, errCodeBadRequest, err.Error())
		return
	}

	key := strings.Join(args, "\x00")
	syslogCacheMu.Lock()
	if entry, ok := syslogCache[key]; ok && time.Since(entry.at) < syslogCacheTTL {
		syslogCacheMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"success": true, "logs": entry.logs})
		return
	}
	syslogCacheMu.Unlock()

	out, err := runJournalctl(args...)
	if err != nil {
		log.Printf("[ERR] journalctl %v: %v %s", args, err, strings.TrimSpace(string(out)))
		apiError(c, http.StatusInternalServerError, errCodeInternal, "Failed to fetch logs from journalctl")
		return
	}
	logs := string(out)
	syslogCacheMu.Lock()
	// Keep the cache small: one entry per distinct query, dropped as they age.
	for k, v := range syslogCache {
		if time.Since(v.at) > 10*syslogCacheTTL {
			delete(syslogCache, k)
		}
	}
	syslogCache[key] = syslogCacheEntry{at: time.Now(), logs: logs}
	syslogCacheMu.Unlock()

	c.JSON(http.StatusOK, gin.H{"success": true, "logs": logs})
}
