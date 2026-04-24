package remote_deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func GenerateWGInstallScript(port int, serverPriv, clientPub, tunnelAddr string) string {
	clientIP := strings.Replace(tunnelAddr, ".1/24", ".2/32", 1)
	wgConfig := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s
ListenPort = %d
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -A FORWARD -o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -A POSTROUTING -o $(ip route show default | awk '/default/ {print $5}' | head -1) -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -D FORWARD -o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -D POSTROUTING -o $(ip route show default | awk '/default/ {print $5}' | head -1) -j MASQUERADE

[Peer]
PublicKey = %s
AllowedIPs = %s
`, serverPriv, tunnelAddr, port, clientPub, clientIP)

	wgConfigBase64 := base64.StdEncoding.EncodeToString([]byte(wgConfig))

	script := `#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
retry_cmd() {
  local attempt=1
  local max_attempts=5
  local delay=2
  while true; do
    "$@" && return 0
    local exit_code=$?
    if [ "$attempt" -ge "$max_attempts" ]; then
      return "$exit_code"
    fi
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
}
retry_cmd apt-get update
retry_cmd apt-get install -y wireguard iptables iproute2 curl
mkdir -p /etc/wireguard
echo net.ipv4.ip_forward=1 > /etc/sysctl.d/99-wireguard-forward.conf
sysctl -p /etc/sysctl.d/99-wireguard-forward.conf
if ! sysctl net.ipv4.tcp_available_congestion_control | grep -qw bbr; then
  if command -v modprobe >/dev/null 2>&1; then
    modprobe tcp_bbr 2>/dev/null || true
  fi
fi
if sysctl net.ipv4.tcp_available_congestion_control | grep -qw bbr; then
  cat << 'BBRCONF' > /etc/sysctl.d/99-proxygw-bbr.conf
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
BBRCONF
  sysctl -p /etc/sysctl.d/99-proxygw-bbr.conf
else
  echo "BBR is not available on this kernel, skipped." >&2
fi
echo "%s" | base64 -d > /etc/wireguard/wg0.conf
systemctl enable wg-quick@wg0
systemctl restart wg-quick@wg0
`
	return fmt.Sprintf(script, wgConfigBase64)
}

func GenerateVlessRealityInstallScript(port int, uuid, privateKey, shortId, serverName, dest string) string {
	config := map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "warning"},
		"inbounds": []map[string]interface{}{
			{
				"listen":   "0.0.0.0",
				"port":     port,
				"protocol": "vless",
				"settings": map[string]interface{}{
					"clients": []map[string]interface{}{
						{"id": uuid, "flow": "xtls-rprx-vision"},
					},
					"decryption": "none",
				},
				"streamSettings": map[string]interface{}{
					"network":  "tcp",
					"security": "reality",
					"realitySettings": map[string]interface{}{
						"show":        false,
						"dest":        dest,
						"xver":        0,
						"serverNames": []string{serverName},
						"privateKey":  privateKey,
						"shortIds":    []string{shortId},
					},
				},
				"sniffing": map[string]interface{}{
					"enabled":      true,
					"destOverride": []string{"http", "tls", "quic"},
					"routeOnly":    true,
				},
			},
		},
		"outbounds": []map[string]interface{}{
			{"protocol": "freedom", "tag": "direct"},
		},
	}

	configBytes, _ := json.Marshal(config)
	configBase64 := base64.StdEncoding.EncodeToString(configBytes)

	script := `#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
retry_cmd() {
  local attempt=1
  local max_attempts=5
  local delay=2
  while true; do
    "$@" && return 0
    local exit_code=$?
    if [ "$attempt" -ge "$max_attempts" ]; then
      return "$exit_code"
    fi
    sleep "$delay"
    attempt=$((attempt + 1))
    delay=$((delay * 2))
  done
}
retry_cmd apt-get update
retry_cmd apt-get install -y curl unzip coreutils
if ! sysctl net.ipv4.tcp_available_congestion_control | grep -qw bbr; then
  if command -v modprobe >/dev/null 2>&1; then
    modprobe tcp_bbr 2>/dev/null || true
  fi
fi
if sysctl net.ipv4.tcp_available_congestion_control | grep -qw bbr; then
  cat << 'BBRCONF' > /etc/sysctl.d/99-proxygw-bbr.conf
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
BBRCONF
  sysctl -p /etc/sysctl.d/99-proxygw-bbr.conf
else
  echo "BBR is not available on this kernel, skipped." >&2
fi
mkdir -p /usr/local/etc/xray
mkdir -p /usr/local/share/xray
retry_cmd curl -4 --fail --location --retry 5 --retry-delay 2 --retry-all-errors -H "Cache-Control: no-cache" -o xray.zip https://github.com/XTLS/Xray-core/releases/latest/download/Xray-linux-64.zip
unzip -o xray.zip -d /usr/local/bin/xray-core
mv /usr/local/bin/xray-core/xray /usr/local/bin/xray
mv /usr/local/bin/xray-core/geoip.dat /usr/local/share/xray/
mv /usr/local/bin/xray-core/geosite.dat /usr/local/share/xray/
chmod +x /usr/local/bin/xray
rm -rf xray.zip /usr/local/bin/xray-core

echo "%s" | base64 -d > /usr/local/etc/xray/config.json

cat << 'XSRV' > /etc/systemd/system/xray.service
[Unit]
Description=Xray Service
Documentation=https://github.com/xtls
After=network.target nss-lookup.target

[Service]
User=root
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=/usr/local/bin/xray run -config /usr/local/etc/xray/config.json
Restart=on-failure
RestartPreventExitStatus=23
LimitNPROC=10000
LimitNOFILE=1000000

[Install]
WantedBy=multi-user.target
XSRV

systemctl daemon-reload
systemctl enable xray
systemctl restart xray
`
	return fmt.Sprintf(script, configBase64)
}
