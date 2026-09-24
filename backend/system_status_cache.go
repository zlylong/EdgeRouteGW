package main

import (
	"database/sql"
	"os"
	"strings"
	"sync"
	"time"
)

// The dashboard polls /api/status every two seconds. Most of what that handler
// reports (binary versions, OS release, git commit, build time) only changes
// when a component or the repository is updated, yet it used to be re-derived
// on every poll by forking eight or so child processes. This file caches those
// static pieces for statusStaticTTL and lets the update paths invalidate the
// cache the moment they replace a binary.

// probeUnitActive and probeVersion are the seams the status handler goes
// through so tests can count how often the host is actually probed.
var probeUnitActive = func(unit string) bool {
	return sysCmd.run("systemctl", "is-active", "--quiet", unit) == nil
}

var probeVersion = func(name string, args ...string) (string, error) {
	out, err := sysCmd.output(name, args...)
	return string(out), err
}

type statusStaticInfo struct {
	XrayVersion     string
	MosdnsVersion   string
	FrrVersion      string
	OSVersion       string
	Commit          string
	BinaryBuildTime string
	AppVersion      string
	frrActive       bool
}

var (
	statusStaticMu    sync.Mutex
	statusStaticCache *statusStaticInfo
	statusStaticAt    time.Time
	statusStaticTTL   = 60 * time.Second
)

// loadStatusStaticInfo returns the cached static part of the status payload,
// refreshing it when the TTL has passed, when the FRR unit flipped state (its
// version can only be read while it runs), or after invalidateStatusStaticInfo.
func loadStatusStaticInfo(frrActive bool) statusStaticInfo {
	statusStaticMu.Lock()
	defer statusStaticMu.Unlock()
	if statusStaticCache != nil && statusStaticCache.frrActive == frrActive && time.Since(statusStaticAt) < statusStaticTTL {
		return *statusStaticCache
	}
	info := statusStaticInfo{
		XrayVersion:     "Unknown",
		MosdnsVersion:   "Unknown",
		FrrVersion:      "Unknown",
		OSVersion:       "Unknown",
		Commit:          "unknown",
		BinaryBuildTime: "unknown",
		AppVersion:      "unknown",
		frrActive:       frrActive,
	}
	if out, err := probeVersion(getPath("core", "xray", "xray"), "version"); err == nil {
		info.XrayVersion = parseXrayVersionOutput(out)
	}
	if out, err := probeVersion(getPath("core", "mosdns", "mosdns"), "version"); err == nil {
		info.MosdnsVersion = strings.TrimSpace(out)
	}
	// vtysh cannot answer while frr is stopped (the normal state in Mode A) and
	// fails with "failed to connect to any daemons" on every status poll.
	if frrActive {
		if out, err := probeVersion("vtysh", "-c", "show version"); err == nil {
			if line := strings.TrimSpace(out); line != "" {
				info.FrrVersion = strings.Split(line, "\n")[0]
			}
		}
	}
	info.OSVersion = readOSPrettyName()
	info.Commit, info.BinaryBuildTime = getBuildInfo()
	info.AppVersion = getAppVersion()
	statusStaticCache = &info
	statusStaticAt = time.Now()
	return info
}

// invalidateStatusStaticInfo drops the cached versions so the next status poll
// reflects a binary or geodata that was just replaced.
func invalidateStatusStaticInfo() {
	statusStaticMu.Lock()
	statusStaticCache = nil
	statusStaticMu.Unlock()
}

// readOSPrettyName reads PRETTY_NAME from /etc/os-release directly instead of
// spawning a shell to source the file.
func readOSPrettyName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "Unknown"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "PRETTY_NAME="))
		val = strings.Trim(val, `"'`)
		if val != "" {
			return val
		}
	}
	return "Unknown"
}

// Monthly traffic totals are summed from traffic_history on both /api/status
// and /api/traffic, each polled every two seconds. Rows are only appended once
// a minute, so a short cache keyed on the active database is exact enough.
var (
	monthlyTrafficMu   sync.Mutex
	monthlyTrafficDB   *sql.DB
	monthlyTrafficAt   time.Time
	monthlyTrafficUp   int64
	monthlyTrafficDown int64
	monthlyTrafficTTL  = 10 * time.Second
)

func cachedMonthlyTrafficTotal(query func() (int64, int64, error)) (int64, int64, error) {
	current := getDB()
	monthlyTrafficMu.Lock()
	if monthlyTrafficDB == current && current != nil && time.Since(monthlyTrafficAt) < monthlyTrafficTTL {
		up, down := monthlyTrafficUp, monthlyTrafficDown
		monthlyTrafficMu.Unlock()
		return up, down, nil
	}
	monthlyTrafficMu.Unlock()

	up, down, err := query()
	if err != nil {
		return up, down, err
	}
	monthlyTrafficMu.Lock()
	monthlyTrafficDB = current
	monthlyTrafficAt = time.Now()
	monthlyTrafficUp = up
	monthlyTrafficDown = down
	monthlyTrafficMu.Unlock()
	return up, down, nil
}

func invalidateMonthlyTrafficCache() {
	monthlyTrafficMu.Lock()
	monthlyTrafficDB = nil
	monthlyTrafficMu.Unlock()
}
