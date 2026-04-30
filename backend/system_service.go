package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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
	confirm := strings.TrimSpace(c.GetHeader("X-EdgeRouteGW-Confirm"))
	if confirm == "" {
		confirm = strings.TrimSpace(c.Query("confirm"))
	}
	if confirm != "APPLY" {
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		logGatewayEvent("warn", "api", "high_risk_guard_blocked", "high risk mutation blocked by confirm guard", map[string]interface{}{
			"source_ip": c.ClientIP(),
			"method":    c.Request.Method,
			"path":      path,
			"action":    action,
		})
		c.JSON(http.StatusForbidden, gin.H{
			"success":    false,
			"error":      "high-risk mutation requires confirmation",
			"error_code": "HIGH_RISK_CONFIRM_REQUIRED",
			"action":     action,
			"path":       path,
			"hint":       "set header X-EdgeRouteGW-Confirm: APPLY or query ?confirm=APPLY",
		})
		return false
	}
	return true
}

var (
	highRiskMutationLockMu   sync.Mutex
	highRiskMutationInFlight = map[string]bool{}
)

func tryAcquireHighRiskMutationLock(c *gin.Context, action string) (func(), bool) {
	if gin.Mode() == gin.TestMode {
		return func() {}, true
	}
	highRiskMutationLockMu.Lock()
	if highRiskMutationInFlight[action] {
		highRiskMutationLockMu.Unlock()
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		logGatewayEventThrottled("high_risk_action_busy_"+action, 5*time.Second, "warn", "api", "high_risk_action_busy", "high-risk mutation action already in progress", map[string]interface{}{
			"source_ip": c.ClientIP(),
			"method":    c.Request.Method,
			"path":      path,
			"action":    action,
		})
		c.JSON(http.StatusConflict, gin.H{
			"success":    false,
			"error":      "high-risk mutation already in progress",
			"error_code": "HIGH_RISK_ACTION_BUSY",
			"action":     action,
		})
		return nil, false
	}
	highRiskMutationInFlight[action] = true
	highRiskMutationLockMu.Unlock()
	return func() {
		highRiskMutationLockMu.Lock()
		delete(highRiskMutationInFlight, action)
		highRiskMutationLockMu.Unlock()
	}, true
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

func getAppVersion() string {
	if out, err := sysCmd.output("git", "-C", getPath(), "describe", "--tags", "--abbrev=0"); err == nil {
		v := strings.TrimSpace(string(out))
		if v != "" {
			return v
		}
	}
	return "unknown"
}
