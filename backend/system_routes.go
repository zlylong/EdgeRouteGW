package main

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var modeSwitchSetServices = func(mode string) error {
	if exec.Command("systemctl", "is-active", "--quiet", "nftables").Run() != nil {
		if err := exec.Command("systemctl", "start", "nftables").Run(); err != nil {
			return err
		}
	}

	_ = exec.Command("systemctl", "reset-failed", "frr").Run()
	if mode == "A" {
		if exec.Command("systemctl", "is-active", "--quiet", "frr").Run() == nil {
			if err := exec.Command("systemctl", "stop", "frr").Run(); err != nil {
				return err
			}
		}
		return nil
	}

	if exec.Command("systemctl", "is-active", "--quiet", "frr").Run() != nil {
		if err := exec.Command("systemctl", "start", "frr").Run(); err != nil {
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

func registerSystemRoutes(api *gin.RouterGroup) {
	api.GET("/status", func(c *gin.Context) {
		xray := exec.Command("systemctl", "is-active", "--quiet", "xray").Run() == nil
		frr := exec.Command("systemctl", "is-active", "--quiet", "frr").Run() == nil
		mosdns := exec.Command("systemctl", "is-active", "--quiet", "mosdns").Run() == nil

		cpu := readCPUUsage()
		ram := readMemoryUsage()

		var mode string
		if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] SELECT value FROM settings WHERE key='mode' err: %v", err)
		}

		xrayVer := "Unknown"
		xrayVersionOut, err := exec.Command(getPath("core", "xray", "xray"), "version").Output()
		if err == nil {
			xrayVer = parseXrayVersionOutput(string(xrayVersionOut))
		}

		geoVer := "Unknown"
		if data, err := os.ReadFile(getPath("core", "mosdns", "geodata.ver")); err == nil && len(data) > 0 {
			geoVer = strings.TrimSpace(string(data))
		} else if info, err := os.Stat(getPath("core", "mosdns", "geoip.dat")); err == nil {
			geoVer = info.ModTime().Format("2006-01-02")
		}

		upStats, _ := exec.Command(getPath("core", "xray", "xray"), "api", "statsquery", "-server=127.0.0.1:10085", "-name=inbound>>>api_inbound>>>traffic>>>uplink").Output()
		downStats, _ := exec.Command(getPath("core", "xray", "xray"), "api", "statsquery", "-server=127.0.0.1:10085", "-name=inbound>>>api_inbound>>>traffic>>>downlink").Output()
		upStr := "0 MB"
		downStr := "0 MB"
		if strings.Contains(string(upStats), "value") {
			upStr = "Active"
		}
		if strings.Contains(string(downStats), "value") {
			downStr = "Active"
		}

		mosdnsVer := "Unknown"
		if mosdnsVersionOut, err := exec.Command(getPath("core", "mosdns", "mosdns"), "version").Output(); err == nil {
			mosdnsVer = strings.TrimSpace(string(mosdnsVersionOut))
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "running", "mode": mode,
			"xray": xray, "ospf": frr, "mosdns": mosdns,
			"xrayVersion": xrayVer, "geoVersion": geoVer, "mosdnsVersion": mosdnsVer,
			"cpu": fmt.Sprintf("%.1f", cpu), "ram": fmt.Sprintf("%.1f", ram),
			"up": upStr, "down": downStr,
		})

	})

	api.POST("/mode", func(c *gin.Context) {
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

		rows, err := db.Query("SELECT SUM(up_bytes), SUM(down_bytes) FROM traffic_history WHERE ts >= datetime('now', '-24 hours')")
		var totalUp24, totalDown24 sql.NullInt64
		if err == nil {
			defer rows.Close()
			if rows.Next() {
				rows.Scan(&totalUp24, &totalDown24)
			}
		}

		c.JSON(200, gin.H{
			"speed":     gin.H{"up": upSpeed, "down": downSpeed},
			"total_24h": gin.H{"up": totalUp24.Int64, "down": totalDown24.Int64},
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

		frrOut, _ := exec.Command("vtysh", "-c", "show ip ospf neighbor json").Output()
		neighborsCount := 0
		if strings.Contains(string(frrOut), "nbrState") {
			neighborsCount = 1
		}

		c.JSON(http.StatusOK, gin.H{
			"neighbors":             neighborsCount,
			"published":             pub,
			"pending":               cand,
			"logs":                  getOspfLogsSnapshot(),
			"push_batch_limit":      settings.PushBatchLimit,
			"push_interval_seconds": settings.PushIntervalSeconds,
			"resolve_workers":       settings.ResolveWorkers,
			"allow_slash32":         settings.AllowSlash32,
			"max_specific_prefix":   settings.MaxSpecificPrefix,
			"lru_max_routes":        settings.LRUMaxRoutes,
		})
	})

	api.POST("/ospf/settings", func(c *gin.Context) {
		var req struct {
			PushBatchLimit     int   `json:"push_batch_limit"`
			PushIntervalSecond int   `json:"push_interval_seconds"`
			ResolveWorkers     int   `json:"resolve_workers"`
			AllowSlash32       *bool `json:"allow_slash32"`
			MaxSpecificPrefix  *int  `json:"max_specific_prefix"`
			LRUMaxRoutes       *int  `json:"lru_max_routes"`
		}
		if c.BindJSON(&req) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad ospf settings payload"})
			return
		}

		batchLimit := clampOspfPushBatchLimit(req.PushBatchLimit)
		intervalSeconds := clampOspfPushIntervalSeconds(req.PushIntervalSecond)
		resolveWorkers := clampOspfResolveWorkers(req.ResolveWorkers)

		allowSlash32 := defaultOspfAllowSlash32
		if req.AllowSlash32 != nil {
			allowSlash32 = *req.AllowSlash32
		}
		maxSpecificPrefix := defaultOspfMaxSpecificPrefix
		if req.MaxSpecificPrefix != nil {
			maxSpecificPrefix = clampOspfMaxSpecificPrefix(*req.MaxSpecificPrefix)
		}
		lruMaxRoutes := defaultOspfLRUMaxRoutes
		if req.LRUMaxRoutes != nil {
			lruMaxRoutes = clampOspfLRUMaxRoutes(*req.LRUMaxRoutes)
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
		allowSlash32Value := "false"
		if allowSlash32 {
			allowSlash32Value = "true"
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_allow_slash32', ?)", allowSlash32Value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_max_specific_prefix', ?)", strconv.Itoa(maxSpecificPrefix)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('ospf_lru_max_routes', ?)", strconv.Itoa(lruMaxRoutes)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"success":               true,
			"push_batch_limit":      batchLimit,
			"push_interval_seconds": intervalSeconds,
			"resolve_workers":       resolveWorkers,
			"allow_slash32":         allowSlash32,
			"max_specific_prefix":   maxSpecificPrefix,
			"lru_max_routes":        lruMaxRoutes,
		})
	})
}
