package provision

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// scriptPrelude is shared by every install script: strict mode, a retry
// helper and BBR where the kernel offers it.
const scriptPrelude = `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
retry_cmd() {
  local attempt=1 max=5 delay=2
  while true; do
    "$@" && return 0
    local rc=$?
    if [ "$attempt" -ge "$max" ]; then return "$rc"; fi
    echo "retry $attempt/$max after exit $rc: $*" >&2
    sleep "$delay"; attempt=$((attempt+1)); delay=$((delay*2))
  done
}
enable_bbr() {
  if ! sysctl net.ipv4.tcp_available_congestion_control 2>/dev/null | grep -qw bbr; then
    modprobe tcp_bbr 2>/dev/null || true
  fi
  if sysctl net.ipv4.tcp_available_congestion_control 2>/dev/null | grep -qw bbr; then
    printf 'net.core.default_qdisc=fq\nnet.ipv4.tcp_congestion_control=bbr\n' > /etc/sysctl.d/99-edgeroutegw-bbr.conf
    sysctl -q -p /etc/sysctl.d/99-edgeroutegw-bbr.conf || true
    echo "BBR enabled"
  else
    echo "BBR not available on this kernel, skipped"
  fi
}
echo "=== EdgeRouteGW remote install: $(hostname) $(uname -m) ==="
`

// WGInstallScript provisions a WireGuard server with NAT to the default
// interface. The gateway is the only peer.
func WGInstallScript(p WGParams) string {
	conf := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s
ListenPort = %d
MTU = %d
PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -A FORWARD -o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -A POSTROUTING -s %s -o $(ip route show default | awk '/default/ {print $5}' | head -1) -j MASQUERADE
PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -D FORWARD -o wg0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT; iptables -t nat -D POSTROUTING -s %s -o $(ip route show default | awk '/default/ {print $5}' | head -1) -j MASQUERADE

[Peer]
PublicKey = %s
AllowedIPs = %s
`, p.ServerPriv, p.TunnelCIDR, p.Port, p.MTU, cidrNet(p.TunnelCIDR), cidrNet(p.TunnelCIDR), p.ClientPub, p.ClientAddr)
	return scriptPrelude + fmt.Sprintf(`retry_cmd apt-get update -qq
retry_cmd apt-get install -y -qq wireguard iptables iproute2 curl
enable_bbr
printf 'net.ipv4.ip_forward=1\n' > /etc/sysctl.d/99-edgeroutegw-forward.conf
sysctl -q -p /etc/sysctl.d/99-edgeroutegw-forward.conf
install -d -m 0700 /etc/wireguard
echo %s | base64 -d > /etc/wireguard/wg0.conf
chmod 600 /etc/wireguard/wg0.conf
systemctl enable wg-quick@wg0 >/dev/null 2>&1
systemctl restart wg-quick@wg0
sleep 1
wg show wg0 | sed 's/private key: .*/private key: (hidden)/'
echo "WireGuard listening on udp/%d"
`, base64.StdEncoding.EncodeToString([]byte(conf)), p.Port)
}

// cidrNet turns 10.77.0.1/24 into 10.77.0.0/24 for the NAT rule.
func cidrNet(cidr string) string {
	host, bits, ok := strings.Cut(cidr, "/")
	if !ok {
		return cidr
	}
	parts := strings.Split(host, ".")
	if len(parts) == 4 && bits == "24" {
		return strings.Join(parts[:3], ".") + ".0/24"
	}
	return cidr
}

// realityProbePort is the loopback SOCKS port the post-install smoke test
// binds on the remote host.
const realityProbePort = 45789

// VlessInstallScript provisions Xray with a VLESS-REALITY inbound, then runs
// a real REALITY handshake against it before reporting success: a dest can
// pass every TLS pre-check and still fail the handshake, which would leave a
// node that looks healthy and carries no traffic.
func VlessInstallScript(p VlessParams) string {
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"listen": "0.0.0.0", "port": p.Port, "protocol": "vless",
			"settings": map[string]any{"clients": []any{map[string]any{"id": p.UUID, "flow": "xtls-rprx-vision"}}, "decryption": "none"},
			"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{
				"show": false, "dest": p.Dest, "xver": 0, "serverNames": []string{p.ServerName}, "privateKey": p.RealityPriv, "shortIds": []string{p.ShortID},
			}},
			"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}, "routeOnly": true},
		}},
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	probe := map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": realityProbePort, "protocol": "socks", "settings": map[string]any{"udp": false}}},
		"outbounds": []any{map[string]any{"protocol": "vless",
			"settings":       map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": p.Port, "users": []any{map[string]any{"id": p.UUID, "encryption": "none", "flow": "xtls-rprx-vision"}}}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"serverName": p.ServerName, "fingerprint": "chrome", "publicKey": p.RealityPub, "shortId": p.ShortID, "spiderX": "/"}},
		}},
	}
	cfgB, _ := json.Marshal(config)
	probeB, _ := json.Marshal(probe)
	return scriptPrelude + fmt.Sprintf(`retry_cmd apt-get update -qq
retry_cmd apt-get install -y -qq curl unzip coreutils
enable_bbr
mkdir -p /usr/local/etc/xray /usr/local/share/xray
case "$(uname -m)" in
  x86_64|amd64)  ASSET="Xray-linux-64.zip" ;;
  aarch64|arm64) ASSET="Xray-linux-arm64-v8a.zip" ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
BASE="https://github.com/XTLS/Xray-core/releases/latest/download"
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
retry_cmd curl -4 -fsSL --retry 5 --retry-delay 2 -o "$WORK/xray.zip" "$BASE/$ASSET"
retry_cmd curl -4 -fsSL --retry 5 --retry-delay 2 -o "$WORK/xray.zip.dgst" "$BASE/$ASSET.dgst"
WANT=$(awk '/^SHA2-256=/ {print $2}' "$WORK/xray.zip.dgst")
GOT=$(sha256sum "$WORK/xray.zip" | awk '{print $1}')
if [ -z "$WANT" ] || [ "$WANT" != "$GOT" ]; then echo "Xray download failed verification (want ${WANT:-none}, got $GOT)" >&2; exit 1; fi
echo "Xray archive verified ($GOT)"
unzip -oq "$WORK/xray.zip" -d "$WORK/xray"
install -m 0755 "$WORK/xray/xray" /usr/local/bin/xray
install -m 0644 "$WORK/xray/geoip.dat" "$WORK/xray/geosite.dat" /usr/local/share/xray/
/usr/local/bin/xray version | head -1
echo %s | base64 -d > /usr/local/etc/xray/config.json
chmod 600 /usr/local/etc/xray/config.json
cat > /etc/systemd/system/xray.service <<'UNIT'
[Unit]
Description=Xray Service (EdgeRouteGW managed)
After=network.target nss-lookup.target

[Service]
User=root
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
NoNewPrivileges=true
Environment=XRAY_LOCATION_ASSET=/usr/local/share/xray
ExecStart=/usr/local/bin/xray run -config /usr/local/etc/xray/config.json
Restart=on-failure
RestartPreventExitStatus=23
LimitNPROC=10000
LimitNOFILE=1000000

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable xray >/dev/null 2>&1
systemctl restart xray
sleep 1
systemctl is-active xray
echo "--- REALITY smoke test"
echo %s | base64 -d > "$WORK/probe.json"
/usr/local/bin/xray run -c "$WORK/probe.json" > "$WORK/probe.log" 2>&1 &
PROBE=$!
trap 'kill $PROBE 2>/dev/null || true; rm -rf "$WORK"' EXIT
ready=0
for _ in $(seq 1 30); do
  if grep -q started "$WORK/probe.log" 2>/dev/null; then ready=1; break; fi
  if ! kill -0 $PROBE 2>/dev/null; then break; fi
  sleep 0.5
done
if [ "$ready" -ne 1 ]; then echo "probe client did not start (port %d busy?)" >&2; cat "$WORK/probe.log" >&2; exit 1; fi
if ! curl -sS --max-time 20 -o /dev/null --socks5-hostname 127.0.0.1:%d "https://%s/"; then
  echo "REALITY smoke test FAILED: xray runs but no client completes a handshake. dest %s is not a usable camouflage target from this host; pick another dest/serverName." >&2
  cat "$WORK/probe.log" >&2
  exit 1
fi
echo "REALITY smoke test passed"
`, base64.StdEncoding.EncodeToString(cfgB), base64.StdEncoding.EncodeToString(probeB), realityProbePort, realityProbePort, p.ServerName, p.Dest)
}

// CleanupScript removes what the install scripts put on the host.
func CleanupScript(kind string) string {
	switch kind {
	case "wg":
		return "#!/bin/bash\nsystemctl disable --now wg-quick@wg0 >/dev/null 2>&1 || true\nrm -f /etc/wireguard/wg0.conf /etc/sysctl.d/99-edgeroutegw-forward.conf\necho cleaned wireguard\n"
	case "vless":
		return "#!/bin/bash\nsystemctl disable --now xray >/dev/null 2>&1 || true\nrm -f /etc/systemd/system/xray.service /usr/local/etc/xray/config.json\nsystemctl daemon-reload || true\necho cleaned xray\n"
	}
	return "#!/bin/bash\necho nothing to clean\n"
}

// CheckScript reports whether the deployed service is up.
func CheckScript(kind string) string {
	switch kind {
	case "wg":
		return "systemctl is-active wg-quick@wg0 && wg show wg0 latest-handshakes"
	case "vless":
		return "systemctl is-active xray && ss -ltn | grep -q ':' && echo listening"
	}
	return "true"
}
