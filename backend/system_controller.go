package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type SystemController struct {
	repo *SystemRepository
}

func NewSystemController(repo *SystemRepository) *SystemController {
	return &SystemController{repo: repo}
}

var (
	ospfNeighborsCacheMu   sync.Mutex
	ospfNeighborsCachedAt  time.Time
	ospfNeighborsCachedCnt int
	ospfNeighborsCacheTTL  = 8 * time.Second
)

func getCachedOspfNeighborsCount() int {
	now := time.Now()

	ospfNeighborsCacheMu.Lock()
	if !ospfNeighborsCachedAt.IsZero() && now.Sub(ospfNeighborsCachedAt) < ospfNeighborsCacheTTL {
		cached := ospfNeighborsCachedCnt
		ospfNeighborsCacheMu.Unlock()
		return cached
	}
	ospfNeighborsCacheMu.Unlock()

	frrOut, _ := sysCmd.output("vtysh", "-c", "show ip ospf neighbor json")
	neighborsCount := 0
	var payload interface{}
	if err := json.Unmarshal(frrOut, &payload); err == nil {
		neighborsCount = countOspfNeighborStates(payload)
	}
	if neighborsCount == 0 && strings.Contains(string(frrOut), "nbrState") {
		neighborsCount = 1
	}

	ospfNeighborsCacheMu.Lock()
	ospfNeighborsCachedCnt = neighborsCount
	ospfNeighborsCachedAt = now
	ospfNeighborsCacheMu.Unlock()

	return neighborsCount
}

func countOspfNeighborStates(v interface{}) int {
	switch x := v.(type) {
	case map[string]interface{}:
		total := 0
		for k, child := range x {
			if k == "nbrState" {
				if s, ok := child.(string); ok && strings.TrimSpace(s) != "" {
					total++
				}
				continue
			}
			total += countOspfNeighborStates(child)
		}
		return total
	case []interface{}:
		total := 0
		for _, child := range x {
			total += countOspfNeighborStates(child)
		}
		return total
	default:
		return 0
	}
}

func (ctl *SystemController) HandleStatus(c *gin.Context) {
	ensureDefaultNetworkRoleSettings()
	xray := sysCmd.run("systemctl", "is-active", "--quiet", "xray") == nil
	frr := sysCmd.run("systemctl", "is-active", "--quiet", "frr") == nil
	mosdns := sysCmd.run("systemctl", "is-active", "--quiet", "mosdns") == nil

	cpu := readCPUUsage()
	ram := readMemoryUsage()

	var mode string
	if v, err := ctl.repo.GetMode(); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] SELECT value FROM settings WHERE key='mode' err: %v", err)
	} else {
		mode = v
	}

	xrayVer := "Unknown"
	xrayVersionOut, err := sysCmd.output(getPath("core", "xray", "xray"), "version")
	if err == nil {
		xrayVer = parseXrayVersionOutput(string(xrayVersionOut))
	}

	frrVer := "Unknown"
	if out, err := sysCmd.output("vtysh", "-c", "show version"); err == nil {
		line := strings.TrimSpace(string(out))
		if line != "" {
			frrVer = strings.Split(line, "\n")[0]
		}
	}

	osVer := "Unknown"
	if out, err := sysCmd.output("sh", "-c", ". /etc/os-release 2>/dev/null; echo ${PRETTY_NAME:-Unknown}"); err == nil {
		osVer = strings.Trim(strings.TrimSpace(string(out)), "\"")
	}

	geoVer := "Unknown"
	if data, err := os.ReadFile(getPath("core", "mosdns", "geodata.ver")); err == nil && len(data) > 0 {
		geoVer = strings.TrimSpace(string(data))
	} else if info, err := os.Stat(getPath("core", "mosdns", "geoip.dat")); err == nil {
		geoVer = info.ModTime().Format("2006-01-02")
	}

	upStats, _ := sysCmd.output(getPath("core", "xray", "xray"), "api", "statsquery", "-server=127.0.0.1:10085", "-name=inbound>>>api_inbound>>>traffic>>>uplink")
	downStats, _ := sysCmd.output(getPath("core", "xray", "xray"), "api", "statsquery", "-server=127.0.0.1:10085", "-name=inbound>>>api_inbound>>>traffic>>>downlink")
	upStr := "0 MB"
	downStr := "0 MB"
	if strings.Contains(string(upStats), "value") {
		upStr = "Active"
	}
	if strings.Contains(string(downStats), "value") {
		downStr = "Active"
	}

	mosdnsVer := "Unknown"
	if mosdnsVersionOut, err := sysCmd.output(getPath("core", "mosdns", "mosdns"), "version"); err == nil {
		mosdnsVer = strings.TrimSpace(string(mosdnsVersionOut))
	}

	interfaceOptions := listPrivateIPv4Interfaces()
	managementIface, serviceIface := loadNetworkRoleSettings()
	managementNetwork, ok := findNetworkByIface(interfaceOptions, managementIface)
	if !ok && len(interfaceOptions) > 0 {
		managementNetwork = interfaceOptions[0]
	}
	serviceNetwork, ok := findNetworkByIface(interfaceOptions, serviceIface)
	if !ok {
		serviceNetwork = managementNetwork
	}
	commit, binaryBuildTime := getBuildInfo()
	appVersion := getAppVersion()

	c.JSON(http.StatusOK, gin.H{
		"status": "running", "mode": mode,
		"xray": xray, "ospf": frr, "mosdns": mosdns,
		"xrayVersion": xrayVer, "frrVersion": frrVer, "osVersion": osVer, "appVersion": appVersion, "geoVersion": geoVer, "mosdnsVersion": mosdnsVer,
		"cpu": fmt.Sprintf("%.1f", cpu), "ram": fmt.Sprintf("%.1f", ram),
		"up": upStr, "down": downStr,
		"interface_options":  interfaceOptions,
		"management_network": gin.H{"iface": managementNetwork.Name, "ip": managementNetwork.IPv4, "subnet": managementNetwork.Subnet},
		"service_network":    gin.H{"iface": serviceNetwork.Name, "ip": serviceNetwork.IPv4, "subnet": serviceNetwork.Subnet},
		"commit":             commit,
		"binary_build_time":  binaryBuildTime,
	})
}

func (ctl *SystemController) HandleGetCron(c *gin.Context) {
	cfg := loadCronScheduleSettings()
	ctl.repo.SaveCronDefaults(cfg.Time, cfg.ScheduleType, cfg.Weekday, cfg.Monthday)
	c.JSON(http.StatusOK, gin.H{
		"enabled":       cfg.Enabled,
		"time":          cfg.Time,
		"schedule_type": cfg.ScheduleType,
		"weekday":       cfg.Weekday,
		"monthday":      cfg.Monthday,
	})
}

func (ctl *SystemController) HandleSetCron(c *gin.Context) {
	var req struct {
		Enabled            bool   `json:"enabled"`
		Time               string `json:"time"`
		ScheduleType       string `json:"schedule_type"`
		Weekday            int    `json:"weekday"`
		Monthday           int    `json:"monthday"`
		EnabledLegacy      bool   `json:"Enabled"`
		TimeLegacy         string `json:"Time"`
		ScheduleTypeLegacy string `json:"ScheduleType"`
		WeekdayLegacy      int    `json:"Weekday"`
		MonthdayLegacy     int    `json:"Monthday"`
	}
	if c.BindJSON(&req) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad cron payload"})
		return
	}
	enabled := req.Enabled || req.EnabledLegacy
	cronTime := strings.TrimSpace(req.Time)
	if cronTime == "" {
		cronTime = strings.TrimSpace(req.TimeLegacy)
	}
	if cronTime == "" {
		cronTime = "04:00"
	}
	if _, err := time.Parse("15:04", cronTime); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid cron time, expected HH:MM"})
		return
	}
	scheduleType := req.ScheduleType
	if strings.TrimSpace(scheduleType) == "" {
		scheduleType = req.ScheduleTypeLegacy
	}
	scheduleType = normalizeCronScheduleType(scheduleType)
	weekday := req.Weekday
	if weekday == 0 {
		weekday = req.WeekdayLegacy
	}
	monthday := req.Monthday
	if monthday == 0 {
		monthday = req.MonthdayLegacy
	}
	weekday = clampCronWeekday(weekday)
	monthday = clampCronMonthday(monthday)

	if err := ctl.repo.SaveCronSettings(enabled, cronTime, scheduleType, weekday, monthday); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	triggerCronReload()
	c.JSON(http.StatusOK, gin.H{
		"success":       true,
		"enabled":       enabled,
		"time":          cronTime,
		"schedule_type": scheduleType,
		"weekday":       weekday,
		"monthday":      monthday,
	})
}

func (ctl *SystemController) HandleTraffic(c *gin.Context) {
	trafficMutex.Lock()
	upSpeed := currentSpeedUp
	downSpeed := currentSpeedDown
	trafficMutex.Unlock()

	totalMonthUp, totalMonthDown, err := ctl.repo.GetMonthlyTrafficTotal()
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] query traffic_history monthly summary failed: %v", err)
	}

	nodeRanking := make([]gin.H, 0)
	ranking, err := ctl.repo.GetMonthlyNodeTrafficRanking(10)
	if err != nil {
		log.Printf("[WARN] query node_traffic_history monthly ranking failed: %v", err)
	} else {
		for _, item := range ranking {
			nodeRanking = append(nodeRanking, gin.H{
				"node_id":     item.NodeID,
				"node_name":   item.NodeName,
				"up":          item.UpBytes,
				"down":        item.DownBytes,
				"total_bytes": item.TotalBytes,
			})
		}
	}

	c.JSON(200, gin.H{
		"speed":        gin.H{"up": upSpeed, "down": downSpeed},
		"total_month":  gin.H{"up": totalMonthUp, "down": totalMonthDown},
		"node_ranking": nodeRanking,
	})
}

func (ctl *SystemController) HandleGetOspf(c *gin.Context) {
	settings := getOspfControllerSettings()
	pub, cand, err := ctl.repo.GetOspfRouteCounts()
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] query ospf route counts err: %v", err)
	}

	neighborsCount := getCachedOspfNeighborsCount()

	allowlist, err := ctl.repo.GetOspfPublishAllowlist()
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] SELECT value FROM settings WHERE key='ospf_publish_allowlist' err: %v", err)
		allowlist = ""
	}
	allowlist = strings.TrimSpace(allowlist)
	allowlistOn := allowlist != ""
	c.JSON(http.StatusOK, gin.H{
		"neighbors":               neighborsCount,
		"published":               pub,
		"pending":                 cand,
		"logs":                    getOspfLogsSnapshot(),
		"push_batch_limit":        settings.PushBatchLimit,
		"push_interval_seconds":   settings.PushIntervalSeconds,
		"resolve_workers":         settings.ResolveWorkers,
		"publish_ip_allowlist":    allowlist,
		"publish_ip_allowlist_on": allowlistOn,
	})
}

func (ctl *SystemController) HandleNetworkConfig(c *gin.Context) {
	if !requireHighRiskMutationGuard(c, "network_config") {
		return
	}
	releaseLock, ok := tryAcquireHighRiskMutationLock(c, "network_config")
	if !ok {
		return
	}
	defer releaseLock()
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
	releaseLock, ok := tryAcquireHighRiskMutationLock(c, "mode_switch")
	if !ok {
		return
	}
	defer releaseLock()
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
	releaseLock, ok := tryAcquireHighRiskMutationLock(c, "ospf_settings")
	if !ok {
		return
	}
	defer releaseLock()
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

func (ctl *SystemController) HandleResetOspfPending(c *gin.Context) {
	if !requireHighRiskMutationGuard(c, "ospf_reset_pending") {
		return
	}
	releaseLock, ok := tryAcquireHighRiskMutationLock(c, "ospf_reset_pending")
	if !ok {
		return
	}
	defer releaseLock()
	if isDryRun(c) {
		c.JSON(http.StatusOK, gin.H{"success": true, "dry_run": true, "action": "ospf_reset_pending", "plan": gin.H{"scope": "routes_table WHERE status='candidate' AND source='static'", "effect": "delete pending static routes only; published/stable routes untouched"}})
		return
	}
	deleted, err := ctl.repo.ResetOspfPendingStaticRoutes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	pub, cand, cntErr := ctl.repo.GetOspfRouteCounts()
	if cntErr != nil {
		log.Printf("[WARN] query ospf route counts after reset err: %v", cntErr)
	}
	log.Printf("[OSPF] pending reset requested: deleted_static_candidates=%d remaining_published=%d remaining_candidate=%d", deleted, pub, cand)
	c.JSON(http.StatusOK, gin.H{"success": true, "deleted_pending": deleted, "published": pub, "pending": cand})
}
