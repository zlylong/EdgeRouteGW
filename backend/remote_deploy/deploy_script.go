package remote_deploy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// realityProbeSocksPort is the loopback SOCKS port the post-install REALITY
// smoke test binds. It only has to be free for the few seconds the probe runs
// and is never exposed off the host; if it is occupied the probe fails closed
// and the deploy reports why rather than silently skipping the check.
const realityProbeSocksPort = 45789

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

func GenerateVlessRealityInstallScript(port int, uuid, privateKey, publicKey, shortId, serverName, dest string) string {
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

	// probeConfig drives the post-install smoke test below. It is the smallest
	// client that exercises the exact path a real gateway takes: a REALITY
	// handshake against the inbound we just wrote, then a VLESS tunnel out.
	probeConfig := map[string]interface{}{
		"log": map[string]interface{}{"loglevel": "warning"},
		"inbounds": []map[string]interface{}{
			{
				"listen":   "127.0.0.1",
				"port":     realityProbeSocksPort,
				"protocol": "socks",
				"settings": map[string]interface{}{"udp": false},
			},
		},
		"outbounds": []map[string]interface{}{
			{
				"protocol": "vless",
				"settings": map[string]interface{}{
					"vnext": []map[string]interface{}{
						{
							"address": "127.0.0.1",
							"port":    port,
							"users": []map[string]interface{}{
								{"id": uuid, "encryption": "none", "flow": "xtls-rprx-vision"},
							},
						},
					},
				},
				"streamSettings": map[string]interface{}{
					"network":  "tcp",
					"security": "reality",
					"realitySettings": map[string]interface{}{
						"serverName":  serverName,
						"fingerprint": "chrome",
						"publicKey":   publicKey,
						"shortId":     shortId,
						"spiderX":     "/",
					},
				},
			},
		},
	}
	probeBytes, _ := json.Marshal(probeConfig)
	probeBase64 := base64.StdEncoding.EncodeToString(probeBytes)

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
# Pick the asset for this host. The script used to hardcode the x86-64 build,
# which on an arm64 node installed a binary the machine cannot execute.
case "$(uname -m)" in
  x86_64|amd64)  XRAY_ASSET="Xray-linux-64.zip" ;;
  aarch64|arm64) XRAY_ASSET="Xray-linux-arm64-v8a.zip" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
XRAY_BASE="https://github.com/XTLS/Xray-core/releases/latest/download"
retry_cmd curl -4 --fail --location --retry 5 --retry-delay 2 --retry-all-errors -H "Cache-Control: no-cache" -o xray.zip "$XRAY_BASE/$XRAY_ASSET"
# XTLS publishes a digest next to every asset. This binary runs as root on the
# node; verify it before it is unpacked. A mismatch aborts the deploy.
retry_cmd curl -4 --fail --location --retry 5 --retry-delay 2 --retry-all-errors -H "Cache-Control: no-cache" -o xray.zip.dgst "$XRAY_BASE/$XRAY_ASSET.dgst"
XRAY_WANT=$(awk '/^SHA2-256=/ {print $2}' xray.zip.dgst)
XRAY_GOT=$(sha256sum xray.zip | awk '{print $1}')
if [ -z "$XRAY_WANT" ] || [ "$XRAY_WANT" != "$XRAY_GOT" ]; then
  echo "Xray download failed verification for $XRAY_ASSET (expected ${XRAY_WANT:-<none>}, got $XRAY_GOT)" >&2
  rm -f xray.zip xray.zip.dgst
  exit 1
fi
rm -f xray.zip.dgst
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

# --- REALITY smoke test ---------------------------------------------------
# A clean install is not evidence that the node works. REALITY only completes a
# handshake when dest behaves like a TLS reverse proxy for serverName under the
# client's ClientHello, and a dest can serve a valid certificate over TLS 1.3
# while still failing that. Without this probe the deploy reports Online, every
# client silently falls through to dest, and the node is indistinguishable from
# a healthy one until someone inspects traffic.
SMOKE_DIR=$(mktemp -d)
SMOKE_LOG=$SMOKE_DIR/probe.log
SMOKE_PID=""
cleanup_smoke() {
  if [ -n "$SMOKE_PID" ]; then kill "$SMOKE_PID" 2>/dev/null || true; fi
  rm -rf "$SMOKE_DIR"
}
trap cleanup_smoke EXIT

echo "%s" | base64 -d > "$SMOKE_DIR/probe.json"
/usr/local/bin/xray run -c "$SMOKE_DIR/probe.json" > "$SMOKE_LOG" 2>&1 &
SMOKE_PID=$!

smoke_ready=0
for _ in $(seq 1 30); do
  if grep -q started "$SMOKE_LOG" 2>/dev/null; then smoke_ready=1; break; fi
  if ! kill -0 "$SMOKE_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
if [ "$smoke_ready" -ne 1 ]; then
  echo "REALITY smoke test could not start its probe client (port %d may be in use)." >&2
  cat "$SMOKE_LOG" >&2
  exit 1
fi

if ! curl -sS --max-time 20 -o /dev/null --socks5-hostname 127.0.0.1:%d "https://%s/"; then
  echo "REALITY smoke test failed: xray is running, but no client can complete a REALITY handshake against it." >&2
  echo "The usual cause is that dest %s does not work as a REALITY camouflage target from this host." >&2
  echo "Note that a dest can pass certificate and TLS 1.3 checks and still fail here; pick another dest/serverName pair." >&2
  echo "Probe client log:" >&2
  cat "$SMOKE_LOG" >&2
  exit 1
fi
echo "REALITY smoke test passed."
`
	return fmt.Sprintf(script, configBase64, probeBase64, realityProbeSocksPort, realityProbeSocksPort, serverName, dest)
}
