package main

import (
	"log"
	"os"
	"strings"
	"time"
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

	os.MkdirAll("/run/proxygw", 0755)
	StartConnectionTracker()
	s.initTPROXYRules()
	goSafe(s.reconcileTPROXYRulesLoop)
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
