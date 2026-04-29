package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func applyNodeChangeDynamically(extraRemoveTags ...string) error {
	if _, err := os.Stat(getPath("core", "xray", "xray")); err != nil {
		return fmt.Errorf("xray runtime not ready: %w", err)
	}
	if err := writeXrayConfigOnly(); err != nil {
		return fmt.Errorf("write xray config failed: %w", err)
	}
	if err := syncXrayOutboundsDynamically(extraRemoveTags...); err != nil {
		return err
	}
	if err := syncXrayRoutingRulesDynamically(); err != nil {
		return err
	}
	return nil
}

func syncXrayOutboundsDynamically(extraRemoveTags ...string) error {
	removeTags := map[string]struct{}{}
	rows, err := db.Query("SELECT id FROM nodes")
	if err != nil {
		return fmt.Errorf("query node ids failed: %w", err)
	}
	for rows.Next() {
		var id int
		if rows.Scan(&id) == nil {
			removeTags[fmt.Sprintf("proxy-%d-out", id)] = struct{}{}
		}
	}
	rows.Close()
	for _, tag := range extraRemoveTags {
		t := strings.TrimSpace(tag)
		if t != "" {
			removeTags[t] = struct{}{}
		}
	}
	if len(removeTags) > 0 {
		args := []string{"api", "rmo", "-s", "127.0.0.1:10085"}
		for tag := range removeTags {
			args = append(args, tag)
		}
		if res := sysCmd.runCombinedOutput(getPath("core", "xray", "xray"), args...); res.Err != nil {
			msg := strings.ToLower(string(res.Output))
			if !strings.Contains(msg, "not found") && !strings.Contains(msg, "failed to dial") {
				return fmt.Errorf("xray api rmo failed: %v, output: %s", res.Err, string(res.Output))
			}
		}
	}

	proxyOutbounds, err := loadProxyOutboundsFromConfigFile(getPath("core", "xray", "config.json"))
	if err != nil {
		return err
	}
	if len(proxyOutbounds) == 0 {
		return nil
	}
	payload := map[string]interface{}{"outbounds": proxyOutbounds}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal outbounds payload failed: %w", err)
	}
	tmpPath := "/tmp/proxygw_xray_outbounds.json"
	if err := os.WriteFile(tmpPath, b, 0644); err != nil {
		return fmt.Errorf("write outbounds payload failed: %w", err)
	}
	if res := sysCmd.runCombinedOutput(getPath("core", "xray", "xray"), "api", "ado", "-s", "127.0.0.1:10085", tmpPath); res.Err != nil {
		return fmt.Errorf("xray api ado failed: %v, output: %s", res.Err, string(res.Output))
	}
	return nil
}

func loadProxyOutboundsFromConfigFile(path string) ([]map[string]interface{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read xray config failed: %w", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse xray config failed: %w", err)
	}
	list, ok := cfg["outbounds"].([]interface{})
	if !ok {
		return nil, nil
	}
	res := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := m["tag"].(string)
		if strings.HasPrefix(tag, "proxy-") && strings.HasSuffix(tag, "-out") {
			res = append(res, m)
		}
	}
	return res, nil
}

func nodeIDToTag(id string) string {
	v, err := strconv.Atoi(strings.TrimSpace(id))
	if err != nil || v <= 0 {
		return ""
	}
	return fmt.Sprintf("proxy-%d-out", v)
}
