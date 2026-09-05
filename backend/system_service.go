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

	_ = sysCmd.run("systemctl", "enable", "frr")
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
	// Mode C keeps whatever is already published: its routes are the resolved
	// addresses of the rules, which the switch does not invalidate, and
	// demoting them would withdraw working routes from the main router only to
	// re-add them moments later. Every other mode starts from a clean slate.
	if mode != "C" {
		if _, err := getDB().Exec("UPDATE routes_table SET status='candidate' WHERE status='published'"); err != nil {
			return err
		}
	}

	// Publishing is otherwise driven solely by domainIPUpdater's five-minute
	// ticker, so switching into B or C left the main router holding the
	// previous mode's routes — or none at all — until the next tick happened to
	// fire. For that window the new mode does not work and nothing says so:
	// the API reports success, every service is healthy, and traffic quietly
	// bypasses the gateway because the router has no reason to send it there.
	//
	// scheduleStaticRouteSync is asynchronous and coalesces concurrent
	// requests, so this starts convergence without making the mode switch wait
	// on DNS resolution of every rule. It is a no-op for Mode A.
	scheduleStaticRouteSync(mode)
	return nil
}

func currentMode() string {
	var mode string
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil || strings.TrimSpace(mode) == "" {
		return "A"
	}
	return strings.TrimSpace(mode)
}

func setModeValue(mode string) error {
	_, err := getDB().Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('mode', ?)", mode)
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

// highRiskLockGroup maps an action to the lock it contends on. Actions that
// rewrite the same runtime files must exclude each other, not just themselves:
// apply_config and mode_switch both regenerate the Xray, Mosdns and nftables
// configs and neither applyMosdnsConfig nor applyNftablesConfig has any lock
// of its own, so a per-action lock let an /api/apply and an /api/mode race and
// interleave their writes. Everything else keeps a lock of its own.
func highRiskLockGroup(action string) string {
	switch action {
	case "apply_config", "mode_switch", "network_config":
		return "config_writers"
	}
	return action
}

func tryAcquireHighRiskMutationLock(c *gin.Context, action string) (func(), bool) {
	if gin.Mode() == gin.TestMode {
		return func() {}, true
	}
	group := highRiskLockGroup(action)
	highRiskMutationLockMu.Lock()
	if highRiskMutationInFlight[group] {
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
	highRiskMutationInFlight[group] = true
	highRiskMutationLockMu.Unlock()
	return func() {
		highRiskMutationLockMu.Lock()
		delete(highRiskMutationInFlight, group)
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
		if shouldSkipInterfaceInManagement(iface.Name) {
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

func shouldSkipInterfaceInManagement(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return true
	}
	// Hide WireGuard tunnel interfaces (e.g. wg0) from 网卡管理/网络角色选择，
	// 避免将节点隧道口误选为管理或业务网卡。
	if strings.HasPrefix(n, "wg") {
		return true
	}
	return false
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
	_ = getDB().QueryRow("SELECT value FROM settings WHERE key='management_iface'").Scan(&managementIface)
	_ = getDB().QueryRow("SELECT value FROM settings WHERE key='service_iface'").Scan(&serviceIface)
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
	_, _ = getDB().Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('management_iface', ?)", managementIface)
	_, _ = getDB().Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('service_iface', ?)", serviceIface)
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
