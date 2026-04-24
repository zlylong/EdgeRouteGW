package main

func buildBaseXrayConfig(mode string) map[string]interface{} {
	config := map[string]interface{}{
		"log":    map[string]string{"loglevel": "warning", "access": "/run/proxygw/xray_access.log"},
		"api":    map[string]interface{}{"services": []string{"StatsService", "RoutingService", "HandlerService"}, "tag": "api"},
		"stats":  map[string]interface{}{},
		"policy": map[string]interface{}{"system": map[string]interface{}{"statsInboundDownlink": true, "statsInboundUplink": true}},
		"inbounds": []map[string]interface{}{
			{
				"port": 12345, "listen": "::", "protocol": "dokodemo-door",
				"settings":       map[string]interface{}{"network": "tcp,udp", "followRedirect": true},
				"streamSettings": map[string]interface{}{"sockopt": map[string]string{"tproxy": "tproxy"}},
				"sniffing":       map[string]interface{}{"enabled": true, "destOverride": []string{"http", "tls"}, "routeOnly": true},
				"tag":            "tproxy_in",
			},
			{
				"listen": "127.0.0.1", "port": 10085, "protocol": "dokodemo-door",
				"settings": map[string]interface{}{"address": "127.0.0.1"},
				"tag":      "api_inbound",
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
		inbounds[0]["sniffing"].(map[string]interface{})["destOverride"] = []string{"http", "tls", "fakedns"}

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
