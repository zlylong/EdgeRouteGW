#!/bin/bash
# ProxyGW Update Script
# Executes a full sync and rebuild from the remote repository.

set -euo pipefail

REPO_DIR="/root/proxygw"
cd "$REPO_DIR"

apt-get update >/dev/null 2>&1 || true
apt-get install -y jq >/dev/null 2>&1 || true

echo "=== ProxyGW Update ==="
echo "[1/4] Pulling latest changes..."
# Stop backend first to avoid runtime rewriting tracked files during git reset
systemctl stop proxygw >/dev/null 2>&1 || true
# Force tag sync to tolerate locally stale tags when stable tag is re-pointed (e.g. v1.6.1)
git fetch --force origin --tags
# Hard sync + clean to tolerate local generated/dirty files (geodata, binaries, etc.)
git reset --hard origin/main
git clean -fd

echo "[2/4] Downloading backend from GitHub Releases..."
ARCH=$(uname -m)
TMP_BACKEND="$REPO_DIR/backend/proxygw-backend.new"

# Prefer latest published release tag from GitHub API
PROXYGW_LATEST=$(curl --retry 3 --connect-timeout 5 --fail -s -4 https://api.github.com/repos/zlylong/proxygw/releases/latest | jq -r '.tag_name // empty' || true)

# Fallback to local tag list (requires --tags fetch above)
if [ -z "$PROXYGW_LATEST" ] && [ -d "$REPO_DIR/.git" ]; then
    PROXYGW_LATEST=$(cd "$REPO_DIR" && git tag --sort=-v:refname | head -n1 || true)
fi

# Ultimate fallback if both API and tags fail
if [ -z "$PROXYGW_LATEST" ]; then
    echo "Warning: release tag detect failed. Using fallback version v1.6.3..."
    PROXYGW_LATEST="v1.6.3"
fi

echo "Using release tag: $PROXYGW_LATEST"
if [ "$ARCH" = "x86_64" ]; then
    wget -q -4 -O "$TMP_BACKEND" "https://github.com/zlylong/proxygw/releases/download/${PROXYGW_LATEST}/proxygw-backend-linux-amd64"
elif [ "$ARCH" = "aarch64" ]; then
    wget -q -4 -O "$TMP_BACKEND" "https://github.com/zlylong/proxygw/releases/download/${PROXYGW_LATEST}/proxygw-backend-linux-arm64"
fi
chmod +x "$TMP_BACKEND"
mv "$TMP_BACKEND" "$REPO_DIR/backend/proxygw-backend"

echo "[3/4] Updating Systemd services (if changed)..."

echo "[3/4] Creating Systemd services..."
cat << 'SYS_EOF' > /etc/systemd/system/proxygw.service
[Unit]
Description=ProxyGW Backend Service
After=network.target network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/root/proxygw/backend
ExecStart=/root/proxygw/backend/proxygw-backend
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

# Security Sandboxing
NoNewPrivileges=yes
ProtectSystem=strict
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
ReadWritePaths=-/root/proxygw -/usr/local/bin -/etc/frr -/etc/nftables.conf

[Install]
WantedBy=multi-user.target
SYS_EOF

cat << 'SYS_EOF' > /etc/systemd/system/mosdns.service
[Unit]
Description=Mosdns Service
After=network.target network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/root/proxygw/core/mosdns
ExecStart=/root/proxygw/core/mosdns/mosdns start -d /root/proxygw/core/mosdns
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
SYS_EOF

cat << 'SYS_EOF' > /etc/systemd/system/xray.service
[Unit]
Description=Xray Service
After=network.target network-online.target nss-lookup.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/root/proxygw/core/xray
Environment=XRAY_LOCATION_ASSET=/root/proxygw/core/mosdns
ExecStart=/root/proxygw/core/xray/xray run -confdir /root/proxygw/core/xray
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
SYS_EOF

systemctl daemon-reload

echo "[4/4] Restarting services..."
systemctl restart proxygw

echo "Update Complete!"