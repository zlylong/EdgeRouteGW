package main

import (
	"log"
	"os"
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
	go startTrafficMonitor()
	go startNftablesMonitor()
	syncFRRConfig()
	go ospfController()
	go cronUpdater()
	go domainIPUpdater()
	applyMosdnsConfig()
	applyXrayConfig()
	if err := applyNftablesConfig(); err != nil {
		log.Printf("[WARN] applyNftablesConfig on startup failed: %v", err)
	}

	os.MkdirAll("/run/proxygw", 0755)
	StartConnectionTracker()
	s.initTPROXYRules()
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
