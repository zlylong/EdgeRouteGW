package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runtimeDir holds the Xray access/error logs the connection tracker tails. It
// lives on tmpfs, so it is gone after every reboot and has to be recreated
// before anything that references it runs.
const runtimeDir = "/run/proxygw"

// xrayAccessLogPath / xrayErrorLogPath are what the generated Xray config points
// at and what the connection tracker tails. Keeping them derived from runtimeDir
// stops the two from drifting: Xray exits 23 if it cannot open the access log,
// and xray.service refuses to restart on that status.
var (
	xrayAccessLogPath = filepath.Join(runtimeDir, "xray_access.log")
	xrayErrorLogPath  = filepath.Join(runtimeDir, "xray_error.log")
)

// AppService orchestrates subsystem startup.
type AppService struct {
	repo *AppRepository
}

func NewAppService(repo *AppRepository) *AppService {
	return &AppService{repo: repo}
}

func (s *AppService) Bootstrap() {
	s.repo.InitDB()

	// Must precede applyXrayConfig: the generated Xray config points its access
	// log at /run/proxygw/xray_access.log, and Xray refuses to start — exit 23,
	// "failed to initialize access logger" — if the directory is missing. That
	// makes the config validation reject a perfectly good config on the first
	// boot after install, and xray.service sets RestartPreventExitStatus=23, so
	// the unit stays down instead of retrying. The connection tracker and the
	// database maintenance job read the same directory.
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		log.Printf("[WARN] failed to create runtime dir %s: %v", runtimeDir, err)
	}

	ensureGeodataHealthy()
	goSafe(startTrafficMonitor)
	goSafe(startNftablesMonitor)
	syncFRRConfig()
	goSafe(ospfController)
	goSafe(cronUpdater)
	goSafe(domainIPUpdater)
	goSafe(runDatabaseMaintenance)
	applyMosdnsConfig()
	applyXrayConfig()
	if err := applyNftablesConfig(); err != nil {
		log.Printf("[WARN] applyNftablesConfig on startup failed: %v", err)
	}

	StartConnectionTracker()
	disableSendRedirects()
	s.initTPROXYRules()
	goSafe(s.reconcileTPROXYRulesLoop)
}

// ipv4ConfDir is the sysctl tree for per-interface IPv4 settings. It is a
// variable so tests can point it at a fixture.
var ipv4ConfDir = "/proc/sys/net/ipv4/conf"

// disableSendRedirects turns ICMP redirects off on every interface that exists
// right now, not just on "all" and "default".
//
// 99-proxygw.conf sets conf.all and conf.default, but neither covers an
// interface that already existed when the file was applied: "default" only
// seeds interfaces created afterwards, and for send_redirects the kernel takes
// the logical OR of conf.all and conf.<iface>, so a pre-existing interface
// keeps its default of 1 and redirects are still emitted.
//
// That is fatal to Mode A. The gateway routes for LAN clients whose next hop
// sits on the same subnet, so it answers with an ICMP redirect, the client
// caches it and then talks to the main router directly. Traffic stops entering
// the TPROXY chain at all — the gateway is bypassed, silently, for exactly the
// destinations a user is most likely to test.
func disableSendRedirects() {
	entries, err := os.ReadDir(ipv4ConfDir)
	if err != nil {
		log.Printf("[WARN] cannot enumerate %s to disable ICMP redirects: %v", ipv4ConfDir, err)
		return
	}
	for _, e := range entries {
		p := filepath.Join(ipv4ConfDir, e.Name(), "send_redirects")
		if err := os.WriteFile(p, []byte("0\n"), 0644); err != nil && !os.IsNotExist(err) {
			log.Printf("[WARN] failed to disable send_redirects on %s: %v", e.Name(), err)
		}
	}
}

func (s *AppService) initTPROXYRules() {
	if !hasTPROXYRuleV4() {
		if err := sysCmd.run("ip", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
			log.Printf("[WARN] init ip rule v4 failed, retrying after del: %v", err)
			_ = sysCmd.run("ip", "rule", "del", "fwmark", "1", "lookup", "tproxy")
			if err := sysCmd.run("ip", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
				log.Printf("[WARN] init ip rule v4 retry failed: %v", err)
			}
		}
	}
	if !hasTPROXYRouteV4() {
		if err := sysCmd.run("ip", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
			log.Printf("[WARN] init ip route v4 failed: %v", err)
			_ = sysCmd.run("ip", "route", "del", "local", "default", "dev", "lo", "table", "tproxy")
			if err := sysCmd.run("ip", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
				log.Printf("[WARN] init ip route v4 retry failed: %v", err)
			}
		}
	}
	if isIPv6Disabled() {
		log.Printf("[INFO] skip IPv6 TPROXY init: IPv6 is disabled on this host")
		return
	}
	if !hasTPROXYRuleV6() {
		if err := sysCmd.run("ip", "-6", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
			log.Printf("[WARN] init ip rule v6 failed, retrying after del: %v", err)
			_ = sysCmd.run("ip", "-6", "rule", "del", "fwmark", "1", "lookup", "tproxy")
			if err := sysCmd.run("ip", "-6", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
				log.Printf("[WARN] init ip rule v6 retry failed: %v", err)
			}
		}
	}
	if !hasTPROXYRouteV6() {
		if err := sysCmd.run("ip", "-6", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
			log.Printf("[WARN] init ip route v6 failed, retrying after del: %v", err)
			_ = sysCmd.run("ip", "-6", "route", "del", "local", "default", "dev", "lo", "table", "tproxy")
			if err := sysCmd.run("ip", "-6", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
				log.Printf("[WARN] init ip route v6 retry failed: %v", err)
			}
		}
	}
}

func hasTPROXYRuleV4() bool {
	out, err := sysCmd.output("ip", "rule", "show")
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "fwmark 0x1 lookup tproxy") || strings.Contains(s, "fwmark 0x1 lookup 100")
}

func hasTPROXYRouteV4() bool {
	out, err := sysCmd.output("ip", "route", "show", "table", "tproxy")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "local default dev lo")
}

func (s *AppService) reconcileTPROXYRulesLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !hasTPROXYRuleV4() {
			if err := sysCmd.run("ip", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
				log.Printf("[WARN] reconcile ip rule v4 failed: %v", err)
			} else {
				log.Printf("[INFO] reconciled missing ip rule: fwmark 1 lookup tproxy")
			}
		}
		if !hasTPROXYRouteV4() {
			if err := sysCmd.run("ip", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
				log.Printf("[WARN] reconcile ip route v4 failed: %v", err)
			} else {
				log.Printf("[INFO] reconciled missing tproxy route table entry")
			}
		}
		if isIPv6Disabled() {
			continue
		}
		// Also reconcile IPv6
		if !hasTPROXYRuleV6() {
			if err := sysCmd.run("ip", "-6", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
				log.Printf("[WARN] reconcile ip rule v6 failed: %v", err)
			} else {
				log.Printf("[INFO] reconciled missing ip6 rule: fwmark 1 lookup tproxy")
			}
		}
		if !hasTPROXYRouteV6() {
			if err := sysCmd.run("ip", "-6", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
				log.Printf("[WARN] reconcile ip route v6 failed: %v", err)
			} else {
				log.Printf("[INFO] reconciled missing tproxy6 route table entry")
			}
		}
	}
}

func isIPv6Disabled() bool {
	paths := []string{
		"/proc/sys/net/ipv6/conf/all/disable_ipv6",
		"/proc/sys/net/ipv6/conf/default/disable_ipv6",
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(data)) == "1" {
			return true
		}
	}
	return false
}

func hasTPROXYRuleV6() bool {
	out, err := sysCmd.output("ip", "-6", "rule", "show")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "fwmark 0x1 lookup tproxy") || strings.Contains(string(out), "fwmark 0x1 lookup 100")
}

func hasTPROXYRouteV6() bool {
	out, err := sysCmd.output("ip", "-6", "route", "show", "table", "tproxy")
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "local default dev lo")
}
