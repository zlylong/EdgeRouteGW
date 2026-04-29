package main

import (
	"fmt"
	"strings"
)

func formatRouteCIDR(ip string) string {
	routeStr, ok := normalizeRouteKey(ip)
	if !ok {
		return ""
	}
	return routeStr
}

func parseFRRTaggedRoutesFromConfig(conf string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ip route ") || !strings.Contains(line, " tag 100") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		routeKey, ok := normalizeRouteKey(fields[2])
		if !ok {
			continue
		}
		set[routeKey] = struct{}{}
	}
	return set
}

func readFRRTaggedStaticRoutes() (map[string]struct{}, error) {
	res := sysCmd.runCombinedOutput("vtysh", "-c", "show running-config")
	out, err := res.Output, res.Err
	if err != nil {
		return nil, fmt.Errorf("vtysh show running-config failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return parseFRRTaggedRoutesFromConfig(string(out)), nil
}
