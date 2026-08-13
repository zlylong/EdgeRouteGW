package main

import (
	"database/sql"
	"log"
	"strconv"
	"strings"
	"time"
)

func normalizeCronScheduleType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "weekly":
		return "weekly"
	case "monthly":
		return "monthly"
	default:
		return "daily"
	}
}

func clampCronWeekday(v int) int {
	if v < 1 {
		return 1
	}
	if v > 7 {
		return 7
	}
	return v
}

func clampCronMonthday(v int) int {
	if v < 1 {
		return 1
	}
	if v > 31 {
		return 31
	}
	return v
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.Local).Day()
}

func calcNextCronRun(now time.Time, scheduleType string, hour int, minute int, weekday int, monthday int) time.Time {
	scheduleType = normalizeCronScheduleType(scheduleType)
	base := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	switch scheduleType {
	case "weekly":
		today := int(now.Weekday())
		if today == 0 {
			today = 7
		}
		delta := weekday - today
		if delta < 0 || (delta == 0 && !base.After(now)) {
			delta += 7
		}
		return base.AddDate(0, 0, delta)
	case "monthly":
		day := monthday
		maxCur := daysInMonth(now.Year(), now.Month())
		if day > maxCur {
			day = maxCur
		}
		next := time.Date(now.Year(), now.Month(), day, hour, minute, 0, 0, now.Location())
		if !next.After(now) {
			nextMonth := now.Month() + 1
			nextYear := now.Year()
			if nextMonth > 12 {
				nextMonth = 1
				nextYear++
			}
			maxNext := daysInMonth(nextYear, nextMonth)
			if monthday > maxNext {
				day = maxNext
			} else {
				day = monthday
			}
			next = time.Date(nextYear, nextMonth, day, hour, minute, 0, 0, now.Location())
		}
		return next
	default:
		if !base.After(now) {
			return base.Add(24 * time.Hour)
		}
		return base
	}
}

func loadCronScheduleSettings() cronScheduleSettings {
	cfg := cronScheduleSettings{Enabled: false, Time: "04:00", ScheduleType: "daily", Weekday: 1, Monthday: 1}
	var enabled, cronTime, scheduleType, weekday, monthday string
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='cron_enabled'").Scan(&enabled); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_enabled check err: %v", err)
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='cron_time'").Scan(&cronTime); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_time check err: %v", err)
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='cron_schedule_type'").Scan(&scheduleType); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_schedule_type check err: %v", err)
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='cron_weekday'").Scan(&weekday); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_weekday check err: %v", err)
	}
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='cron_monthday'").Scan(&monthday); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] cron_monthday check err: %v", err)
	}
	cfg.Enabled = strings.TrimSpace(enabled) == "true"
	if t := strings.TrimSpace(cronTime); t != "" {
		cfg.Time = t
	}
	cfg.ScheduleType = normalizeCronScheduleType(scheduleType)
	if n, err := strconv.Atoi(strings.TrimSpace(weekday)); err == nil {
		cfg.Weekday = clampCronWeekday(n)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(monthday)); err == nil {
		cfg.Monthday = clampCronMonthday(n)
	}
	if _, err := time.Parse("15:04", cfg.Time); err != nil {
		cfg.Time = "04:00"
	}
	return cfg
}

func triggerCronReload() {
	select {
	case cronUpdateChan <- struct{}{}:
	default:
	}
}

func cronUpdater() {
	for {
		cfg := loadCronScheduleSettings()
		t, _ := time.Parse("15:04", cfg.Time)
		now := time.Now()
		next := calcNextCronRun(now, cfg.ScheduleType, t.Hour(), t.Minute(), cfg.Weekday, cfg.Monthday)
		sleepDuration := next.Sub(now)
		if sleepDuration < time.Second {
			sleepDuration = time.Second
		}

		timer := time.NewTimer(sleepDuration)
		select {
		case <-timer.C:
			if cfg.Enabled {
				log.Printf("Running cron update for GeoData... (type=%s time=%s weekday=%d monthday=%d)", cfg.ScheduleType, cfg.Time, cfg.Weekday, cfg.Monthday)
				if err := updateGeodata(); err != nil {
					log.Printf("[SECURITY] Cron update failed: %v", err)
				} else {
					log.Println("Cron update for GeoData completed securely.")
				}
			}
		case <-cronUpdateChan:
			timer.Stop()
			log.Println("Cron configuration updated, recalculating next run...")
		}
	}
}

func scheduleApply() {
	scheduleApplyWithMosdns(false)
}

func scheduleApplyWithMosdns(needMosdns bool) {
	applyMutex.Lock()
	defer applyMutex.Unlock()
	if needMosdns {
		pendingMosdnsApply = true
	}
	if applyTimer != nil {
		applyTimer.Stop()
	}
	applyTimer = time.AfterFunc(3*time.Second, func() {
		applyMutex.Lock()
		runMosdns := pendingMosdnsApply
		pendingMosdnsApply = false
		applyMutex.Unlock()

		if runMosdns {
			if err := applyMosdnsConfig(); err != nil {
				log.Printf("[ERROR] apply mosdns failed: %v", err)
			}
		}
		if err := applyXrayConfig(); err != nil {
			log.Printf("[ERROR] apply xray failed: %v", err)
		}
	})
}

func domainIPUpdater() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		var mode string
		if err := getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
			mode = "A"
		}
		if mode == "C" {
			// Only Mode C needs periodic domain/geosite DNS-driven OSPF materialization.
			scheduleStaticRouteSync(mode)
		}
	}
}
