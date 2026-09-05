package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

func cleanupTransientWireguardInterfaces() {
	out, err := sysCmd.output("ip", "-o", "-4", "addr", "show", "type", "wireguard")
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		ifName := fields[1]
		ifName = strings.TrimSuffix(ifName, ":")
		cidr := fields[3]
		if !strings.HasSuffix(cidr, "/32") {
			continue
		}
		if sysCmd.run("systemctl", "is-active", "--quiet", "wg-quick@"+ifName) == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join("/etc/wireguard", ifName+".conf")); err == nil {
			continue
		}
		if err := sysCmd.run("ip", "link", "delete", ifName); err == nil {
			log.Printf("[INFO] cleaned transient wireguard interface: %s (%s)", ifName, cidr)
		}
	}
}
