package main

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var modeSwitchSetServices = func(mode string) error {
	if err := sysCmd.run("systemctl", "is-active", "--quiet", "nftables"); err != nil {
		if err := sysCmd.run("systemctl", "start", "nftables"); err != nil {
			return err
		}
	}

	_ = sysCmd.run("systemctl", "reset-failed", "frr")
	if mode == "A" {
		if sysCmd.run("systemctl", "is-active", "--quiet", "frr") == nil {
			if err := sysCmd.run("systemctl", "stop", "frr"); err != nil {
				return err
			}
		}
		return nil
	}

	if err := sysCmd.run("systemctl", "is-active", "--quiet", "frr"); err != nil {
		if err := sysCmd.run("systemctl", "start", "frr"); err != nil {
			return err
		}
	}
	return nil
}

var modeSwitchSyncFRR = func() error {
	syncFRRConfig()
	return nil
}

var modeSwitchApplyNftables = func() error { return applyNftablesConfig() }
var modeSwitchApplyMosdns = func() error { return applyMosdnsConfig() }
var modeSwitchApplyXray = func() error { return applyXrayConfig() }
var modeSwitchFinalizeRoutes = func(mode string) error {
	if mode == "C" {
		return nil
	}
	_, err := db.Exec("UPDATE routes_table SET status='candidate' WHERE status='published'")
	return err
}

func currentMode() string {
	var mode string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil || strings.TrimSpace(mode) == "" {
		return "A"
	}
	return strings.TrimSpace(mode)
}

func setModeValue(mode string) error {
	_, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('mode', ?)", mode)
	return err
}

func rollbackModeChange(mode string) {
	_ = setModeValue(mode)
	_ = modeSwitchSyncFRR()
	_ = modeSwitchApplyNftables()
	_ = modeSwitchApplyMosdns()
	_ = modeSwitchApplyXray()
	_ = modeSwitchSetServices(mode)
}

func applyModeChange(newMode string) error {
	if conflicts := detectModeSwitchProtectedConflicts(newMode); len(conflicts) > 0 {
		return fmt.Errorf("mode switch blocked: protected endpoint route conflict (%d): %s", len(conflicts), sampleRouteKeys(conflicts, 10))
	}

	oldMode := currentMode()
	if err := setModeValue(newMode); err != nil {
		return err
	}

	steps := []func() error{
		modeSwitchSyncFRR,
		modeSwitchApplyNftables,
		modeSwitchApplyMosdns,
		modeSwitchApplyXray,
		func() error { return modeSwitchSetServices(newMode) },
		func() error { return modeSwitchFinalizeRoutes(newMode) },
	}

	for _, step := range steps {
		if err := step(); err != nil {
			rollbackModeChange(oldMode)
			return err
		}
	}
	return nil
}

func requireHighRiskMutationGuard(c *gin.Context, action string) bool {
	if gin.Mode() == gin.TestMode {
		return true
	}
	confirm := strings.TrimSpace(c.GetHeader("X-ProxyGW-Confirm"))
	if confirm == "" {
		confirm = strings.TrimSpace(c.Query("confirm"))
	}
	if confirm != "APPLY" {
		logGatewayEvent("warn", "api", "high_risk_guard_blocked", "high risk mutation blocked by confirm guard", map[string]interface{}{
			"source_ip": c.ClientIP(),
			"method":    c.Request.Method,
			"path":      c.FullPath(),
			"action":    action,
		})
		c.JSON(http.StatusForbidden, gin.H{"success": false, "error": "high-risk mutation requires confirmation", "hint": "set header X-ProxyGW-Confirm: APPLY or query ?confirm=APPLY"})
		return false
	}
	return true
}

func isDryRun(c *gin.Context) bool {
	raw := strings.ToLower(strings.TrimSpace(c.Query("dry_run")))
	return raw == "1" || raw == "true" || raw == "yes"
}

func readCPUUsage() float64 {
	getStat := func() (idle, total float64) {
		b, err := os.ReadFile("/proc/stat")
		if err != nil {
			return 0, 0
		}
		lines := strings.Split(string(b), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "cpu ") {
				fields := strings.Fields(line)
				for i, f := range fields[1:] {
					val, _ := strconv.ParseFloat(f, 64)
					total += val
					if i == 3 {
						idle = val
					}
				}
				return
			}
		}
		return
	}
	idle1, total1 := getStat()
	time.Sleep(200 * time.Millisecond)
	idle2, total2 := getStat()

	if total2-total1 > 0 {
		return 100.0 * (1.0 - (idle2-idle1)/(total2-total1))
	}
	return 0
}

func readMemoryUsage() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	memTotal := 0.0
	memAvailable := 0.0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				memTotal, _ = strconv.ParseFloat(parts[1], 64)
			}
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				memAvailable, _ = strconv.ParseFloat(parts[1], 64)
			}
		}
	}
	if memTotal <= 0 {
		return 0
	}
	used := memTotal - memAvailable
	if used < 0 {
		used = 0
	}
	return used / memTotal * 100
}

type networkInterfaceInfo struct {
	Name   string `json:"name"`
	IPv4   string `json:"ipv4"`
	Subnet string `json:"subnet"`
}

func listPrivateIPv4Interfaces() []networkInterfaceInfo {
	ifaces, err := net.Interfaces()
	if err != nil {
		return []networkInterfaceInfo{}
	}
	out := make([]networkInterfaceInfo, 0)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			network := ip.Mask(ipnet.Mask)
			maskSize, _ := ipnet.Mask.Size()
			out = append(out, networkInterfaceInfo{
				Name:   iface.Name,
				IPv4:   ip.String(),
				Subnet: fmt.Sprintf("%s/%d", network.String(), maskSize),
			})
			break
		}
	}
	return out
}

func findNetworkByIface(options []networkInterfaceInfo, ifaceName string) (networkInterfaceInfo, bool) {
	for _, item := range options {
		if item.Name == ifaceName {
			return item, true
		}
	}
	return networkInterfaceInfo{}, false
}

func loadNetworkRoleSettings() (string, string) {
	var managementIface, serviceIface string
	_ = db.QueryRow("SELECT value FROM settings WHERE key='management_iface'").Scan(&managementIface)
	_ = db.QueryRow("SELECT value FROM settings WHERE key='service_iface'").Scan(&serviceIface)
	managementIface = strings.TrimSpace(managementIface)
	serviceIface = strings.TrimSpace(serviceIface)
	return managementIface, serviceIface
}

func ensureDefaultNetworkRoleSettings() {
	options := listPrivateIPv4Interfaces()
	if len(options) == 0 {
		return
	}
	managementIface, serviceIface := loadNetworkRoleSettings()
	if managementIface == "" {
		managementIface = options[0].Name
	}
	if serviceIface == "" {
		serviceIface = managementIface
	}
	_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('management_iface', ?)", managementIface)
	_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('service_iface', ?)", serviceIface)
}

func getBuildInfo() (string, string) {
	commit := "unknown"
	if out, err := sysCmd.output("git", "-C", getPath(), "rev-parse", "--short", "HEAD"); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	buildTime := "unknown"
	if info, err := os.Stat(getPath("backend", "proxygw-backend")); err == nil {
		buildTime = info.ModTime().Format(time.RFC3339)
	}
	return commit, buildTime
}

func registerSystemRoutes(api *gin.RouterGroup) {
	api.GET("/status", func(c *gin.Context) {
		ensureDefaultNetworkRoleSettings()
		xray := sysCmd.run("systemctl", "is-active", "--quiet", "xray") == nil
		frr := sysCmd.run("systemctl", "is-active", "--quiet", "frr") == nil
		mosdns := sysCmd.run("systemctl", "is-active", "--quiet", "mosdns") == nil

		cpu := readCPUUsage()
		ram := readMemoryUsage()

		var mode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] SELECT value FROM settings WHERE key='mode' err: %v", err)
		}

		xrayVer := "Unknown"
		xrayVersionOut, err := sysCmd.output(getPath("core", "xray", "xray"), "version")
		if err == nil {
			xrayVer = parseXrayVersionOutput(string(xrayVersionOut))
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

		c.JSON(http.StatusOK, gin.H{
			"status": "running", "mode": mode,
			"xray": xray, "ospf": frr, "mosdns": mosdns,
			"xrayVersion": xrayVer, "geoVersion": geoVer, "mosdnsVersion": mosdnsVer,
			"cpu": fmt.Sprintf("%.1f", cpu), "ram": fmt.Sprintf("%.1f", ram),
			"up": upStr, "down": downStr,
			"interface_options":  interfaceOptions,
			"management_network": gin.H{"iface": managementNetwork.Name, "ip": managementNetwork.IPv4, "subnet": managementNetwork.Subnet},
			"service_network":    gin.H{"iface": serviceNetwork.Name, "ip": serviceNetwork.IPv4, "subnet": serviceNetwork.Subnet},
			"commit":             commit,
			"binary_build_time":  binaryBuildTime,
		})

	})

	api.POST("/network_config", func(c *gin.Context) {
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
			c.JSON(http.StatusOK, gin.H{"success": true, "dry_run": true, "planned": gin.H{"management_iface": req.ManagementIface, "service_iface": req.ServiceIface, "actions": []string{"update settings.management_iface", "update settings.service_iface", "syncFRRConfig"}}})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('management_iface', ?)", req.ManagementIface); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('service_iface', ?)", req.ServiceIface); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		syncFRRConfig()
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	api.POST("/mode", func(c *gin.Context) {
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
			c.JSON(http.StatusOK, gin.H{"success": true, "dry_run": true, "planned": gin.H{"mode": req.Mode, "actions": []string{"set mode", "syncFRRConfig", "applyNftablesConfig", "applyMosdnsConfig", "applyXrayConfig", "service reconcile", "route state finalize"}}})
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
	})

	api.GET("/cron", func(c *gin.Context) {
		cfg := loadCronScheduleSettings()
		_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_time', ?)", cfg.Time)
		_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_schedule_type', ?)", cfg.ScheduleType)
		_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_weekday', ?)", strconv.Itoa(cfg.Weekday))
		_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_monthday', ?)", strconv.Itoa(cfg.Monthday))
		c.JSON(http.StatusOK, gin.H{
			"enabled":       cfg.Enabled,
			"time":          cfg.Time,
			"schedule_type": cfg.ScheduleType,
			"weekday":       cfg.Weekday,
			"monthday":      cfg.Monthday,
		})
	})

	api.POST("/cron", func(c *gin.Context) {
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

		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_enabled', ?)", fmt.Sprintf("%t", enabled)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_time', ?)", cronTime); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_schedule_type', ?)", scheduleType); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_weekday', ?)", strconv.Itoa(weekday)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('cron_monthday', ?)", strconv.Itoa(monthday)); err != nil {
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
	})

	api.GET("/traffic", func(c *gin.Context) {
		trafficMutex.Lock()
		upSpeed := currentSpeedUp
		downSpeed := currentSpeedDown
		trafficMutex.Unlock()

		var totalMonthUp, totalMonthDown sql.NullInt64
		if err := db.QueryRow(`
			SELECT COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
			FROM traffic_history
			WHERE datetime(ts, 'localtime') >= datetime('now', 'localtime', 'start of month')
		`).Scan(&totalMonthUp, &totalMonthDown); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] query traffic_history monthly summary failed: %v", err)
		}

		nodeRows, err := db.Query(`
			SELECT n.id,
			       n.name,
			       COALESCE(SUM(h.up_bytes), 0)   AS up_bytes,
			       COALESCE(SUM(h.down_bytes), 0) AS down_bytes,
			       COALESCE(SUM(h.up_bytes), 0) + COALESCE(SUM(h.down_bytes), 0) AS total_bytes
			FROM nodes n
			LEFT JOIN node_traffic_history h
			       ON h.node_id = n.id
			      AND datetime(h.ts, 'localtime') >= datetime('now', 'localtime', 'start of month')
			GROUP BY n.id, n.name
			HAVING total_bytes > 0
			ORDER BY total_bytes DESC
			LIMIT 10
		`)
		nodeRanking := make([]gin.H, 0)
		if err != nil {
			log.Printf("[WARN] query node_traffic_history monthly ranking failed: %v", err)
		} else {
			defer nodeRows.Close()
			for nodeRows.Next() {
				var nodeID int
				var nodeName string
				var upBytes, downBytes, totalBytes int64
				if scanErr := nodeRows.Scan(&nodeID, &nodeName, &upBytes, &downBytes, &totalBytes); scanErr != nil {
					continue
				}
				nodeRanking = append(nodeRanking, gin.H{
					"node_id":     nodeID,
					"node_name":   nodeName,
					"up":          upBytes,
					"down":        downBytes,
					"total_bytes": totalBytes,
				})
			}
			if rowsErr := nodeRows.Err(); rowsErr != nil {
				log.Printf("[WARN] iterate node_traffic_history monthly ranking failed: %v", rowsErr)
			}
		}

		c.JSON(200, gin.H{
			"speed":        gin.H{"up": upSpeed, "down": downSpeed},
			"total_month":  gin.H{"up": totalMonthUp.Int64, "down": totalMonthDown.Int64},
			"node_ranking": nodeRanking,
		})
	})

	api.GET("/ospf", func(c *gin.Context) {
		settings := getOspfControllerSettings()
		var pub, cand int
		if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE status='published'").Scan(&pub); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] SELECT count(*) FROM routes_table WHERE status='published' err: %v", err)
		}
		if err := db.QueryRow("SELECT count(*) FROM routes_table WHERE status='candidate'").Scan(&cand); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] SELECT count(*) FROM routes_table WHERE status='candidate' err: %v", err)
		}

		frrOut, _ := sysCmd.output("vtysh", "-c", "show ip ospf neighbor json")
		neighborsCount := 0
		if strings.Contains(string(frrOut), "nbrState") {
			neighborsCount = 1
		}

		var allowlist string
		_ = db.QueryRow("SELECT value FROM settings WHERE key='ospf_publish_allowlist'").Scan(&allowlist)
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
	})

	api.POST("/ospf/settings", func(c *gin.Context) {
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
			c.JSON(http.StatusOK, gin.H{"success": true, "dry_run": true, "planned": gin.H{"push_batch_limit": batchLimit, "push_interval_seconds": intervalSeconds, "resolve_workers": resolveWorkers, "publish_ip_allowlist": allowlist}})
			return
		}

		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_push_batch_limit', ?)", strconv.Itoa(batchLimit)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_push_interval_seconds', ?)", strconv.Itoa(intervalSeconds)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_resolve_workers', ?)", strconv.Itoa(resolveWorkers)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_publish_allowlist', ?)", allowlist); err != nil {
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
	})
}
