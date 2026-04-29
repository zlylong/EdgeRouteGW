package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func applyRuleChangeDynamically(needMosdns bool) error {
	if _, err := os.Stat(getPath("core", "xray", "xray")); err != nil {
		return fmt.Errorf("xray runtime not ready: %w", err)
	}
	if needMosdns {
		if err := applyMosdnsConfig(); err != nil {
			return fmt.Errorf("apply mosdns failed: %w", err)
		}
	}
	if err := syncXrayRoutingRulesDynamically(); err != nil {
		return err
	}
	if err := writeXrayConfigOnly(); err != nil {
		return fmt.Errorf("persist xray config failed: %w", err)
	}
	return nil
}

func getActiveNodeContext() (map[int]struct{}, int) {
	active := map[int]struct{}{}
	rows, err := db.Query("SELECT id FROM nodes WHERE active=1")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			if rows.Scan(&id) == nil {
				active[id] = struct{}{}
			}
		}
	}

	var defNodeStr string
	_ = db.QueryRow("SELECT value FROM settings WHERE key='default_node_id'").Scan(&defNodeStr)
	defaultID, _ := strconv.Atoi(strings.TrimSpace(defNodeStr))
	if _, ok := active[defaultID]; !ok {
		defaultID = 0
		for id := range active {
			defaultID = id
			break
		}
	}
	return active, defaultID
}

func outboundTagForPolicy(policy string, active map[int]struct{}, defaultID int) string {
	policy = strings.TrimSpace(strings.ToLower(policy))
	switch {
	case policy == "direct", policy == "block":
		return policy
	case strings.HasPrefix(policy, "proxy-"):
		idStr := strings.TrimPrefix(policy, "proxy-")
		id, _ := strconv.Atoi(idStr)
		if _, ok := active[id]; ok {
			return fmt.Sprintf("proxy-%d-out", id)
		}
	case strings.HasPrefix(policy, "ha-"):
		parts := strings.Split(strings.TrimPrefix(policy, "ha-"), "-")
		if len(parts) == 2 {
			first, _ := strconv.Atoi(parts[0])
			second, _ := strconv.Atoi(parts[1])
			if _, ok := active[first]; ok {
				return fmt.Sprintf("proxy-%d-out", first)
			}
			if _, ok := active[second]; ok {
				return fmt.Sprintf("proxy-%d-out", second)
			}
		}
	case policy == "proxy":
		fallthrough
	default:
		if defaultID > 0 {
			return fmt.Sprintf("proxy-%d-out", defaultID)
		}
	}
	return "direct"
}

func syncXrayRoutingRulesDynamically() error {
	var mode string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil {
		mode = "A"
	}
	active, defaultID := getActiveNodeContext()
	cfg := map[string]interface{}{
		"routing": map[string]interface{}{
			"domainStrategy": "IPIfNonMatch",
			"rules":          []map[string]interface{}{{"inboundTag": []string{"api_inbound"}, "outboundTag": "api", "type": "field"}},
		},
	}
	if mode == "B" {
		cfg["routing"].(map[string]interface{})["rules"] = append(
			cfg["routing"].(map[string]interface{})["rules"].([]map[string]interface{}),
			map[string]interface{}{"inboundTag": []string{"dns-in"}, "outboundTag": "dns-out", "type": "field"},
		)
	}

	rRows, err := db.Query("SELECT id, type, value, policy FROM rules ORDER BY id ASC")
	if err != nil {
		return fmt.Errorf("query rules failed: %w", err)
	}
	defer rRows.Close()
	rules := cfg["routing"].(map[string]interface{})["rules"].([]map[string]interface{})
	for rRows.Next() {
		var id int
		var rtype, value, policy string
		if err := rRows.Scan(&id, &rtype, &value, &policy); err != nil {
			continue
		}
		rule := map[string]interface{}{
			"type":        "field",
			"ruleTag":     fmt.Sprintf("db-rule-%d", id),
			"outboundTag": outboundTagForPolicy(policy, active, defaultID),
		}
		switch rtype {
		case "domain":
			domainValues, err := buildXrayDomainRuleValues(value)
			if err != nil {
				continue
			}
			rule["domain"] = domainValues
		case "geosite":
			rule["domain"] = []string{"geosite:" + value}
		case "ip":
			rule["ip"] = []string{value}
		case "geoip", "geolocation":
			if strings.EqualFold(value, "private") {
				rule["ip"] = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"}
			} else {
				rule["ip"] = []string{"geoip:" + value}
			}
		default:
			continue
		}
		rules = append(rules, rule)
	}
	if err := rRows.Err(); err != nil {
		return fmt.Errorf("iterate rules failed: %w", err)
	}
	cfg["routing"].(map[string]interface{})["rules"] = rules

	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal routing payload failed: %w", err)
	}
	tmp := "/tmp/proxygw_xray_routing_rules.json"
	if err := os.WriteFile(tmp, payload, 0644); err != nil {
		return fmt.Errorf("write routing payload failed: %w", err)
	}
	if res := sysCmd.runCombinedOutput(getPath("core", "xray", "xray"), "api", "adrules", "-s", "127.0.0.1:10085", tmp); res.Err != nil {
		return fmt.Errorf("xray api adrules failed: %v, output: %s", res.Err, string(res.Output))
	}
	return nil
}
