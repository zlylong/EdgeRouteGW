#!/bin/bash
# EdgeRouteGW Update Script
# Syncs the repository, downloads the checksum-verified release binary for
# this architecture, rewrites the systemd units and restarts proxygw. The
# previous binary is kept as proxygw-backend.prev and restored automatically
# if the new one is not active within 10 seconds.

set -euo pipefail

# NOTE:
# Xray/Mosdns 运行时配置由 backend 按模式动态生成并下发。
# - Mode A / Mode C: 禁用 FakeDNS/FakeIP
# - Mode B: 仅此模式启用 FakeDNS/FakeIP
# 请勿在部署脚本内写死模式相关业务配置，避免与运行态冲突。

REPO_DIR="/root/proxygw"
cd "$REPO_DIR"

apt-get update >/dev/null 2>&1 || true
apt-get install -y jq sqlite3 wget >/dev/null 2>&1 || true
# Installs from before dig became a hard dependency have no resolver at all;
# without it the OSPF engine silently resolves nothing.
command -v dig >/dev/null 2>&1 || apt-get install -y bind9-dnsutils >/dev/null 2>&1 || apt-get install -y dnsutils >/dev/null 2>&1 || true

echo "=== EdgeRouteGW Update ==="

# Auto-detect a local proxy for GitHub API / release asset downloads only.
# Do NOT export it globally before git fetch: Debian Git is linked against GnuTLS,
# and Git-over-HTTPS through the local Xray HTTP inbound can fail with:
#   GnuTLS, handshake failed: The TLS connection was non-properly terminated.
UPDATE_HTTP_PROXY=""
UPDATE_HTTPS_PROXY=""
if ss -tulpn | grep -q ':10809 '; then
    echo "[INFO] Local HTTP proxy detected at 10809, enabling for release downloads only..."
    UPDATE_HTTP_PROXY=http://127.0.0.1:10809
    UPDATE_HTTPS_PROXY=http://127.0.0.1:10809
elif ss -tulpn | grep -q ':10808 '; then
    echo "[INFO] Local SOCKS5 proxy detected at 10808, enabling for release downloads only..."
    UPDATE_HTTP_PROXY=socks5h://127.0.0.1:10808
    UPDATE_HTTPS_PROXY=socks5h://127.0.0.1:10808
fi

# Prevent proxy loop when scripts call local components or database tools.
export NO_PROXY=127.0.0.1,localhost
export no_proxy=127.0.0.1,localhost

echo "[1/6] Pulling latest changes..."
# Keep git transport direct/SSH and isolated from local HTTP/SOCKS proxy variables.
# This avoids GnuTLS handshake failures when the update script runs on the proxy gateway itself.
# Force tag sync to tolerate locally stale tags when stable tag is re-pointed (e.g. v1.6.1)
# config/aes.key is generated on first boot and is the only copy of the key
# every stored SSH credential is encrypted with. It used to be a tracked file,
# so "git reset --hard" below replaced it with the committed placeholder and
# every credential became undecryptable on the next start. Carry it across
# the reset regardless of what the tree says about it.
AES_KEY_BACKUP=""
if [ -f "$REPO_DIR/config/aes.key" ]; then
    AES_KEY_BACKUP=$(mktemp)
    cp -p "$REPO_DIR/config/aes.key" "$AES_KEY_BACKUP"
fi
env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY git fetch --force origin --tags
env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY git reset --hard origin/main
git clean -fd
if [ -n "$AES_KEY_BACKUP" ]; then
    mkdir -p "$REPO_DIR/config"
    cp -p "$AES_KEY_BACKUP" "$REPO_DIR/config/aes.key"
    rm -f "$AES_KEY_BACKUP"
fi
# Hard sync + clean to tolerate local generated/dirty files (geodata, binaries, etc.)

echo "[2/6] Downloading backend from GitHub Releases..."
ARCH=$(uname -m)
TMP_BACKEND="/tmp/proxygw-backend.new"

# Prefer latest published release tag from GitHub API
PROXYGW_LATEST=$(http_proxy="$UPDATE_HTTP_PROXY" https_proxy="$UPDATE_HTTPS_PROXY" curl --retry 3 --connect-timeout 5 --fail -s -4 https://api.github.com/repos/zlylong/EdgeRouteGW/releases/latest | jq -r '.tag_name // empty' || true)

# Fallback to local tag list (requires --tags fetch above)
if [ -z "$PROXYGW_LATEST" ] && [ -d "$REPO_DIR/.git" ]; then
    PROXYGW_LATEST=$(cd "$REPO_DIR" && git tag --sort=-v:refname | head -n1 || true)
fi

# Last resort: the tag the current checkout describes. No hardcoded version:
# an update that cannot determine a release must stop rather than downgrade.
if [ -z "$PROXYGW_LATEST" ] && [ -d "$REPO_DIR/.git" ]; then
    PROXYGW_LATEST=$(cd "$REPO_DIR" && git describe --tags --abbrev=0 2>/dev/null || true)
fi
if [ -z "$PROXYGW_LATEST" ]; then
    echo "Error: could not determine the release tag (GitHub API and git tags both unavailable)"; exit 1
fi

echo "Using release tag: $PROXYGW_LATEST"

# verify_backend_checksum FILE ASSET_NAME TAG
# Releases publish SHA256SUMS next to the binaries. A mismatch is fatal: the
# file is a root-executed binary and a bad byte is worse than no update. A
# missing SHA256SUMS is fatal too (fail closed): a blocked or tampered
# download of the checksum list must not silently disable verification. Set
# PROXYGW_ALLOW_UNVERIFIED=1 to install a release that predates SHA256SUMS.
verify_backend_checksum() {
    local file="$1" asset="$2" tag="$3"
    local sums; sums=$(mktemp)
    if ! ${DOWNLOAD_CMD:-wget -q -4 -O} "$sums" "https://github.com/zlylong/EdgeRouteGW/releases/download/${tag}/SHA256SUMS" 2>/dev/null || [ ! -s "$sums" ]; then
        rm -f "$sums"
        if [ "${PROXYGW_ALLOW_UNVERIFIED:-0}" = "1" ]; then
            echo "Warning: no SHA256SUMS for ${tag}; PROXYGW_ALLOW_UNVERIFIED=1 set, skipping verification"
            return 0
        fi
        echo "Error: could not fetch SHA256SUMS for ${tag}; refusing to install an unverified binary"
        echo "       (set PROXYGW_ALLOW_UNVERIFIED=1 to override for releases that predate checksums)"
        rm -f "$file"; return 1
    fi
    local want; want=$(awk -v a="$asset" '$2==a {print $1}' "$sums")
    rm -f "$sums"
    if [ -z "$want" ]; then
        echo "Error: SHA256SUMS for ${tag} has no entry for ${asset}"; return 1
    fi
    local got; got=$(sha256sum "$file" | awk '{print $1}')
    if [ "$want" != "$got" ]; then
        echo "Error: checksum mismatch for ${asset} (${tag})"
        echo "  expected ${want}"
        echo "  got      ${got}"
        rm -f "$file"; return 1
    fi
    echo "Checksum verified for ${asset} (${tag})"
}

if [ "$ARCH" = "x86_64" ]; then
    BACKEND_ASSET="proxygw-backend-linux-amd64"
elif [ "$ARCH" = "aarch64" ]; then
    BACKEND_ASSET="proxygw-backend-linux-arm64"
else
    echo "Error: unsupported architecture ${ARCH} (need x86_64 or aarch64)"; exit 1
fi
http_proxy="$UPDATE_HTTP_PROXY" https_proxy="$UPDATE_HTTPS_PROXY" wget -q -4 -O "$TMP_BACKEND" "https://github.com/zlylong/EdgeRouteGW/releases/download/${PROXYGW_LATEST}/${BACKEND_ASSET}"
DOWNLOAD_CMD="env http_proxy=$UPDATE_HTTP_PROXY https_proxy=$UPDATE_HTTPS_PROXY wget -q -4 -O" verify_backend_checksum "$TMP_BACKEND" "$BACKEND_ASSET" "$PROXYGW_LATEST" || exit 1
chmod +x "$TMP_BACKEND"

# Now that downloads are complete, stop the service to perform the swap and sync.
# Keep the previous binary next to the new one so a failed start can be rolled
# back without another download.
BACKEND_BIN="$REPO_DIR/backend/proxygw-backend"
BACKEND_PREV="$BACKEND_BIN.prev"
systemctl stop proxygw >/dev/null 2>&1 || true
if [ -f "$BACKEND_BIN" ]; then
    cp -p "$BACKEND_BIN" "$BACKEND_PREV"
fi
mv "$TMP_BACKEND" "$BACKEND_BIN"

echo "[3/6] Writing Systemd services..."
cat << 'SYS_EOF' > /etc/systemd/system/proxygw.service
[Unit]
Description=EdgeRouteGW Backend Service
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
ReadWritePaths=-/root/proxygw -/usr/local/bin -/etc/frr -/etc/nftables.conf /proc/sys/net/ipv4/conf
# ProtectSystem=strict mounts /run read-only; the backend and the connection
# tracker write the Xray logs under /run/proxygw. Preserve keeps the directory
# (and Xray's open log file) across backend restarts.
RuntimeDirectory=proxygw
RuntimeDirectoryPreserve=yes

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
Environment=XRAY_LOCATION_ASSET=/root/proxygw/core/xray
ExecStart=/root/proxygw/core/xray/xray run -confdir /root/proxygw/core/xray
Restart=on-failure
# Xray exits 23 when it rejects its own configuration. Without this, a bad
# config turns Restart=on-failure into an endless restart loop that floods the
# journal instead of stopping with a diagnosable failure.
RestartPreventExitStatus=23
RestartSec=5
LimitNOFILE=1048576
LimitNPROC=10000

[Install]
WantedBy=multi-user.target
SYS_EOF

systemctl daemon-reload

# install.sh no longer writes nf_conntrack.* keys (the TPROXY datapath never
# loads nf_conntrack, so they only produced "cannot stat" errors at boot), but
# an upgrade never rewrote the file on existing hosts. Drop the dead block.
SYSCTL_FILE=/etc/sysctl.d/99-proxygw.conf
if [ -f "$SYSCTL_FILE" ] && grep -q '^net\.netfilter\.nf_conntrack' "$SYSCTL_FILE"; then
    sed -i '/^net\.netfilter\.nf_conntrack/d' "$SYSCTL_FILE"
    echo "Removed dead nf_conntrack keys from $SYSCTL_FILE"
fi

echo "[4/6] Automatically flushing old DNS and OSPF caches..."
if [ -f "$REPO_DIR/config/proxygw.db" ]; then
    sqlite3 "$REPO_DIR/config/proxygw.db" "DELETE FROM domain_resolve_cache; DELETE FROM routes_table; DELETE FROM geosite_expand_cache;" 2>/dev/null || true
fi

echo "[5/6] Restarting services..."
systemctl restart proxygw

# rollback_backend restores the previous binary if the new one does not come
# up. A gateway whose management backend is down cannot be fixed from the UI,
# so the script must not walk away from a failed start.
rollback_backend() {
    if [ ! -f "$BACKEND_PREV" ]; then
        echo "Error: proxygw failed to start and no previous binary is available to roll back to"
        return 1
    fi
    echo "Error: proxygw failed to start with ${PROXYGW_LATEST}; rolling back to the previous binary"
    systemctl stop proxygw >/dev/null 2>&1 || true
    cp -p "$BACKEND_PREV" "$BACKEND_BIN"
    systemctl restart proxygw || true
    return 1
}

echo "[6/6] Verifying backend health..."
healthy=0
for _ in $(seq 1 10); do
    if systemctl is-active --quiet proxygw; then
        healthy=1; break
    fi
    sleep 1
done
if [ "$healthy" != "1" ]; then
    rollback_backend
    exit 1
fi
echo "proxygw is active"

# Run low-risk DB index optimization (idempotent, online-safe)
if [ -x "$REPO_DIR/scripts/db_optimize.sh" ] && [ -f "$REPO_DIR/config/proxygw.db" ]; then
    echo "Running DB index optimization (--index-only)..."
    "$REPO_DIR/scripts/db_optimize.sh" "$REPO_DIR/config/proxygw.db" --index-only || true
fi

echo "Update Complete!"