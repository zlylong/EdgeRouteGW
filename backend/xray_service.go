package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

func buildBaseXrayConfig(mode string) map[string]interface{} {
	config := map[string]interface{}{
		"log":   map[string]string{"loglevel": "warning", "access": "/run/proxygw/xray_access.log"},
		"api":   map[string]interface{}{"services": []string{"StatsService", "RoutingService", "HandlerService"}, "tag": "api"},
		"stats": map[string]interface{}{},
		"policy": map[string]interface{}{
			"system": map[string]interface{}{
				"statsInboundDownlink":  true,
				"statsInboundUplink":    true,
				"statsOutboundDownlink": true,
				"statsOutboundUplink":   true,
			},
		},
		"inbounds": []map[string]interface{}{
			{
				"port": 12345, "listen": "::", "protocol": "dokodemo-door",
				"settings":       map[string]interface{}{"network": "tcp,udp", "followRedirect": true},
				"streamSettings": map[string]interface{}{"sockopt": map[string]string{"tproxy": "tproxy"}},
				"sniffing":       map[string]interface{}{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true},
				"tag":            "tproxy_in",
			},
			{
				"listen": "127.0.0.1", "port": 10085, "protocol": "dokodemo-door",
				"settings": map[string]interface{}{"address": "127.0.0.1"},
				"tag":      "api_inbound",
			},
			{
				"listen":   "127.0.0.1",
				"port":     10808,
				"protocol": "socks",
				"settings": map[string]interface{}{"udp": true, "auth": "noauth"},
				"sniffing": map[string]interface{}{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true},
				"tag":      "socks_in",
			},
			{
				"listen":   "127.0.0.1",
				"port":     10809,
				"protocol": "http",
				"settings": map[string]interface{}{"allowTransparent": false},
				"sniffing": map[string]interface{}{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true},
				"tag":      "http_in",
			},
		},
		"outbounds": []map[string]interface{}{
			{"protocol": "freedom", "tag": "direct", "streamSettings": map[string]interface{}{"sockopt": map[string]interface{}{"mark": 2}}},
			{"protocol": "blackhole", "tag": "block"},
		},
		"routing": map[string]interface{}{
			"domainStrategy": "IPIfNonMatch",
			"rules": []map[string]interface{}{
				{"inboundTag": []string{"api_inbound"}, "outboundTag": "api", "type": "field"},
			},
		},
	}

	if mode == "B" {
		config["fakedns"] = []map[string]interface{}{
			{
				"id":       "fakedns",
				"ipPool":   "198.18.0.0/16",
				"poolSize": 65535,
			},
		}
		config["dns"] = map[string]interface{}{
			"servers": []string{"fakedns"},
		}

		inbounds := config["inbounds"].([]map[string]interface{})
		inbounds[0]["sniffing"].(map[string]interface{})["destOverride"] = []string{"http", "tls", "quic", "fakedns"}

		inbounds = append(inbounds, map[string]interface{}{
			"port": 5353, "listen": "127.0.0.1", "protocol": "dokodemo-door",
			"settings": map[string]interface{}{"address": "8.8.8.8", "port": 53, "network": "udp"},
			"tag":      "dns-in",
		})
		config["inbounds"] = inbounds

		outbounds := config["outbounds"].([]map[string]interface{})
		outbounds = append(outbounds, map[string]interface{}{
			"protocol": "dns", "tag": "dns-out", "streamSettings": map[string]interface{}{"sockopt": map[string]interface{}{"mark": 2}},
		})
		config["outbounds"] = outbounds

		routing := config["routing"].(map[string]interface{})
		rules := routing["rules"].([]map[string]interface{})
		rules = append(rules, map[string]interface{}{
			"inboundTag": []string{"dns-in"}, "outboundTag": "dns-out", "type": "field",
		})
		routing["rules"] = rules
	}

	return config
}

func applyXrayConfig() error {
	return applyXrayConfigInternal(true)
}

func writeXrayConfigOnly() error {
	return applyXrayConfigInternal(false)
}

func applyXrayConfigInternal(restart bool) error {
	applyMutex.Lock()
	defer applyMutex.Unlock()
	if restart {
		log.Println("[AUDIT] Applying Xray Config")
	} else {
		log.Println("[AUDIT] Updating Xray Config File (no restart)")
	}

	var mode string
	getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	config := buildBaseXrayConfig(mode)

	rows, err := getDB().Query("SELECT id, name, type, address, port, uuid, COALESCE(params, '{}'), active FROM nodes")
	if err != nil {
		return err
	}
	defer rows.Close()
	var defNodeStr string
	getDB().QueryRow("SELECT value FROM settings WHERE key='default_node_id'").Scan(&defNodeStr)
	defaultNodeId, _ := strconv.Atoi(defNodeStr)

	var activeIds []int
	var proxyTags []string
	for rows.Next() {
		var name, ntype, address, uuid, paramsStr string
		var port, id int
		var active bool
		if err := rows.Scan(&id, &name, &ntype, &address, &port, &uuid, &paramsStr, &active); err != nil {
			continue
		}

		if active {
			activeIds = append(activeIds, id)
		}

		ntypeLow := strings.ToLower(ntype)

		var params map[string]interface{}
		json.Unmarshal([]byte(paramsStr), &params)
		if params == nil {
			params = make(map[string]interface{})
		}

		if uuid != "" {
			if params["settings"] == nil {
				if ntypeLow == "vmess" {
					params["settings"] = map[string]interface{}{"vnext": []map[string]interface{}{{"users": []map[string]interface{}{{"id": uuid, "alterId": 0}}}}}
				} else if ntypeLow == "vless" {
					user := map[string]interface{}{"id": uuid, "encryption": "none"}
					if flow, ok := params["flow"].(string); ok && flow != "" {
						user["flow"] = flow
					}
					params["settings"] = map[string]interface{}{"vnext": []map[string]interface{}{{"users": []map[string]interface{}{user}}}}
				} else if ntypeLow == "trojan" {
					params["settings"] = map[string]interface{}{"servers": []map[string]interface{}{{"password": uuid}}}
				} else if ntypeLow == "shadowsocks" || ntypeLow == "ss" {
					method := "aes-256-gcm"
					if m, ok := params["method"].(string); ok && m != "" {
						method = m
					}
					params["settings"] = map[string]interface{}{"servers": []map[string]interface{}{{"password": uuid, "method": method}}}
				}
			}
			if ntypeLow == "vless" || ntypeLow == "trojan" {
				if params["type"] != nil && params["streamSettings"] == nil {
					ss := map[string]interface{}{"network": params["type"]}
					if params["security"] != nil {
						ss["security"] = params["security"]
					}
					if params["security"] == "reality" {
						ss["realitySettings"] = map[string]interface{}{
							"fingerprint": params["fp"], "serverName": params["sni"],
							"publicKey": params["pbk"], "shortId": params["sid"], "spiderX": "/",
						}
					} else if params["security"] == "tls" {
						ss["tlsSettings"] = map[string]interface{}{"serverName": params["sni"]}
					}
					params["streamSettings"] = ss
				}
			}
		}

		outbound := params
		outbound["protocol"] = ntypeLow
		outbound["tag"] = fmt.Sprintf("proxy-%d-out", id)

		if settings, ok := outbound["settings"].(map[string]interface{}); ok {
			if vnext, ok := settings["vnext"].([]interface{}); ok && len(vnext) > 0 {
				if node, ok := vnext[0].(map[string]interface{}); ok {
					node["address"] = address
					node["port"] = port
				}
			} else if vnext, ok := settings["vnext"].([]map[string]interface{}); ok && len(vnext) > 0 {
				vnext[0]["address"] = address
				vnext[0]["port"] = port
			}
			if servers, ok := settings["servers"].([]interface{}); ok && len(servers) > 0 {
				if server, ok := servers[0].(map[string]interface{}); ok {
					server["address"] = address
					server["port"] = port
				}
			} else if servers, ok := settings["servers"].([]map[string]interface{}); ok && len(servers) > 0 {
				servers[0]["address"] = address
				servers[0]["port"] = port
			}
		} else if ntypeLow != "custom" && ntypeLow != "wireguard" {
			if ntypeLow == "vmess" || ntypeLow == "vless" {
				outbound["settings"] = map[string]interface{}{
					"vnext": []map[string]interface{}{{"address": address, "port": port}},
				}
			} else {
				outbound["settings"] = map[string]interface{}{
					"servers": []map[string]interface{}{{"address": address, "port": port}},
				}
			}
		}

		if outbound != nil {
			if ss, ok := outbound["streamSettings"].(map[string]interface{}); ok {
				ss["sockopt"] = map[string]interface{}{"mark": 2}
			} else {
				outbound["streamSettings"] = map[string]interface{}{"sockopt": map[string]interface{}{"mark": 2}}
			}
			config["outbounds"] = append(config["outbounds"].([]map[string]interface{}), outbound)
			proxyTags = append(proxyTags, fmt.Sprintf("proxy-%d-out", id))
		}
	}

	rRows, err := getDB().Query("SELECT id, type, value, policy FROM rules ORDER BY priority ASC, id ASC")
	if err != nil {
		log.Printf("[WARN] routing rules query err: %v", err)
		return err
	}
	defer rRows.Close()
	rules := config["routing"].(map[string]interface{})["rules"].([]map[string]interface{})
	for rRows.Next() {
		var id int
		var rtype, value, policy string
		if err := rRows.Scan(&id, &rtype, &value, &policy); err != nil {
			continue
		}
		rule := map[string]interface{}{"type": "field", "ruleTag": fmt.Sprintf("db-rule-%d", id)}

		if policy == "direct" || policy == "block" {
			rule["outboundTag"] = policy
		} else if policy == "proxy" {
			rule["balancerTag"] = "proxy-balancer"
		} else if strings.HasPrefix(policy, "proxy-") {
			// Single node binding (e.g. proxy-1)
			rule["outboundTag"] = policy + "-out"
		} else if strings.HasPrefix(policy, "ha-") {
			// HA Mode (e.g. ha-1-2)
			parts := strings.Split(strings.TrimPrefix(policy, "ha-"), "-")
			if len(parts) == 2 {
				rule["balancerTag"] = "bal-" + policy
			} else {
				rule["outboundTag"] = "proxy"
			}
		} else {
			rule["outboundTag"] = policy
		}

		if rtype == "domain" {
			domainValues, err := buildXrayDomainRuleValues(value)
			if err != nil {
				log.Printf("[WARN] skip invalid domain rule %q: %v", value, err)
				continue
			}
			rule["domain"] = domainValues
			rules = append(rules, rule)
		} else if rtype == "geosite" {
			rule["domain"] = []string{"geosite:" + value}
			rules = append(rules, rule)
		} else if rtype == "geoip" || rtype == "ip" || rtype == "geolocation" {
			if rtype == "ip" {
				rule["ip"] = []string{value}
				rules = append(rules, rule)
			} else if rtype == "geolocation" {
				if strings.EqualFold(value, "private") {
					rule["ip"] = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"}
				} else {
					rule["ip"] = []string{"geoip:" + value}
				}
				rules = append(rules, rule)
			} else {
				if strings.EqualFold(value, "private") {
					rule["ip"] = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10"}
				} else {
					rule["ip"] = []string{"geoip:" + value}
				}
				rules = append(rules, rule)
			}
		} else {
			// Skip invalid rules without domain/ip to prevent Xray crash
			continue
		}
	}
	if err := rRows.Err(); err != nil {
		log.Printf("[WARN] rRows err: %v", err)
	}

	defaultPolicy := "proxy"
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='lan_default_policy'").Scan(&defaultPolicy); err != nil || strings.TrimSpace(defaultPolicy) == "" {
		defaultPolicy = "proxy"
	}
	var failoverMode string
	_ = getDB().QueryRow("SELECT value FROM settings WHERE key='node_failover_mode'").Scan(&failoverMode)
	strictFailover := normalizeNodeFailoverMode(strings.TrimSpace(strings.ToLower(failoverMode))) == "strict"
	catchAllRule := map[string]interface{}{
		"type":    "field",
		"network": "tcp,udp",
		"ruleTag": "default-fallback",
	}
	policy := strings.ToLower(strings.TrimSpace(defaultPolicy))
	if mode == "A" {
		// Mode A should only proxy traffic that explicitly matches proxy rules.
		if policy == "block" {
			catchAllRule["outboundTag"] = "block"
		} else {
			catchAllRule["outboundTag"] = "direct"
		}
	} else {
		switch policy {
		case "proxy":
			if len(proxyTags) > 0 {
				catchAllRule["balancerTag"] = "proxy-balancer"
			} else {
				catchAllRule["outboundTag"] = "direct"
			}
		case "block":
			catchAllRule["outboundTag"] = "block"
		default:
			catchAllRule["outboundTag"] = "direct"
		}
	}
	rules = append(rules, catchAllRule)

	if len(proxyTags) > 0 {
		config["observatory"] = map[string]interface{}{
			"subjectSelector": []string{"proxy-"},
			"probeURL":        "http://cp.cloudflare.com/",
			"probeInterval":   "30s",
		}
		routing := config["routing"].(map[string]interface{})
		routing["balancers"] = []map[string]interface{}{
			{
				"tag":      "proxy-balancer",
				"selector": []string{"proxy-"},
				"strategy": map[string]interface{}{
					"type": "leastPing",
				},
			},
		}

		customBalancers := make(map[string]map[string]interface{})
		actualDefault := 0
		if len(activeIds) == 1 {
			actualDefault = activeIds[0]
		} else {
			for _, aid := range activeIds {
				if aid == defaultNodeId {
					actualDefault = aid
					break
				}
			}
		}

		for _, r := range rules {
			if r["balancerTag"] == "proxy-balancer" {
				if actualDefault > 0 {
					delete(r, "balancerTag")
					r["outboundTag"] = fmt.Sprintf("proxy-%d-out", actualDefault)
				} else {
					delete(r, "outboundTag")
					r["balancerTag"] = "proxy-balancer"
				}
			}
			if bTag, ok := r["balancerTag"].(string); ok && strings.HasPrefix(bTag, "bal-ha-") {
				parts := strings.Split(strings.TrimPrefix(bTag, "bal-ha-"), "-")
				if len(parts) == 2 {
					customBalancers[bTag] = map[string]interface{}{
						"tag":         bTag,
						"selector":    []string{"proxy-" + parts[0] + "-out"},
						"fallbackTag": "proxy-" + parts[1] + "-out",
					}
				}
			}
		}

		balancers := routing["balancers"].([]map[string]interface{})
		for _, cb := range customBalancers {
			balancers = append(balancers, cb)
		}
		routing["balancers"] = balancers
	}

	validOutbounds := map[string]struct{}{}
	for _, ob := range config["outbounds"].([]map[string]interface{}) {
		if tag, ok := ob["tag"].(string); ok && tag != "" {
			validOutbounds[tag] = struct{}{}
		}
	}
	validBalancers := map[string]struct{}{}
	if routing, ok := config["routing"].(map[string]interface{}); ok {
		if balancers, ok := routing["balancers"].([]map[string]interface{}); ok {
			for _, b := range balancers {
				if tag, ok := b["tag"].(string); ok && tag != "" {
					validBalancers[tag] = struct{}{}
				}
			}
		}
	}
	for _, r := range rules {
		if ot, ok := r["outboundTag"].(string); ok {
			if strings.HasPrefix(ot, "proxy-") || ot == "proxy" {
				if _, exists := validOutbounds[ot]; !exists {
					if strictFailover {
						continue
					}
					delete(r, "balancerTag")
					r["outboundTag"] = "direct"
				}
			}
		}
		if bt, ok := r["balancerTag"].(string); ok {
			if _, exists := validBalancers[bt]; !exists {
				if strictFailover {
					continue
				}
				delete(r, "balancerTag")
				r["outboundTag"] = "direct"
			}
		}
	}

	config["routing"].(map[string]interface{})["rules"] = rules

	scheduleStaticRouteSync(mode)

	configData, _ := json.MarshalIndent(config, "", "  ")

	tmpTest, err := os.CreateTemp("", "proxygw_xray_test-*.json")
	if err != nil {
		return fmt.Errorf("failed to create temp xray test config: %v", err)
	}
	tempTestPath := tmpTest.Name()
	defer os.Remove(tempTestPath)
	if _, err := tmpTest.Write(configData); err != nil {
		tmpTest.Close()
		return fmt.Errorf("failed to write temp xray test config: %v", err)
	}
	if err := tmpTest.Close(); err != nil {
		return fmt.Errorf("failed to close temp xray test config: %v", err)
	}
	if err := sysCmd.run(getPath("core", "xray", "xray"), "-test", "-config", tempTestPath); err != nil {
		log.Printf("[ERROR] Xray config validation failed: %v. Config rejected.", err)
		return fmt.Errorf("xray config validation failed, check node parameters")
	}

	if err := os.WriteFile(getPath("core", "xray", "config.json"), configData, 0644); err != nil {
		return fmt.Errorf("failed to write xray config.json: %v", err)
	}
	if !restart {
		return nil
	}
	cleanupTransientWireguardInterfaces()
	if mode == "B" {
		// Flush Mosdns FakeIP cache by restarting it, ensuring consistency with Xray's new session
		_ = sysCmd.run("systemctl", "restart", "mosdns")
	}
	return sysCmd.run("systemctl", "restart", "xray")
}
