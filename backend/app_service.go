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
	go startTrafficMonitor()
	go startNftablesMonitor()
	syncFRRConfig()
	go ospfController()
	go cronUpdater()
	go domainIPUpdater()
	go runDatabaseMaintenance()
	applyMosdnsConfig()
	applyXrayConfig()
	if err := applyNftablesConfig(); err != nil {
		log.Printf("[WARN] applyNftablesConfig on startup failed: %v", err)
	}

	os.MkdirAll("/run/proxygw", 0755)
	StartConnectionTracker()
	s.initTPROXYRules()
	go s.reconcileTPROXYRulesLoop()
}

func (s *AppService) initTPROXYRules() {
	_ = sysCmd.run("ip", "rule", "del", "fwmark", "1", "lookup", "tproxy")
	if err := sysCmd.run("ip", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
		log.Printf("[WARN] init ip rule v4 failed: %v", err)
	}
	_ = sysCmd.run("ip", "route", "del", "local", "default", "dev", "lo", "table", "tproxy")
	if err := sysCmd.run("ip", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
		log.Printf("[WARN] init ip route v4 failed: %v", err)
	}
	_ = sysCmd.run("ip", "-6", "rule", "del", "fwmark", "1", "lookup", "tproxy")
	if err := sysCmd.run("ip", "-6", "rule", "add", "fwmark", "1", "lookup", "tproxy"); err != nil {
		log.Printf("[WARN] init ip rule v6 failed: %v", err)
	}
	_ = sysCmd.run("ip", "-6", "route", "del", "local", "default", "dev", "lo", "table", "tproxy")
	if err := sysCmd.run("ip", "-6", "route", "add", "local", "default", "dev", "lo", "table", "tproxy"); err != nil {
		log.Printf("[WARN] init ip route v6 failed: %v", err)
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

