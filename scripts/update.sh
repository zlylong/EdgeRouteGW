#!/bin/bash
# EdgeRouteGW Update Script
# Downloads and verifies the release binary for this architecture, then syncs
# the repository, re-downloads the core binaries if the tracked amd64 builds do
# not run here, rewrites the systemd units and restarts proxygw. The previous
# binary is kept as proxygw-backend.prev and restored automatically when the
# new one does not stay active for 10 seconds after the restart.

set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

# NOTE:
# Xray/Mosdns 运行时配置由 backend 按模式动态生成并下发。
# - Mode A / Mode C: 禁用 FakeDNS/FakeIP
# - Mode B: 仅此模式启用 FakeDNS/FakeIP
# 请勿在部署脚本内写死模式相关业务配置，避免与运行态冲突。

REPO_DIR="/root/proxygw"
cd "$REPO_DIR"

apt-get update >/dev/null 2>&1 || true
apt-get install -y jq sqlite3 curl wget unzip >/dev/null 2>&1 || true
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
# Downloads below use curl: unlike wget it understands the socks5h:// form,
# so the SOCKS-only branch no longer fails with "Unsupported scheme".
if [ -n "$(ss -Hltn 'sport = :10809' 2>/dev/null)" ]; then
    echo "[INFO] Local HTTP proxy detected at 10809, enabling for release downloads only..."
    UPDATE_HTTP_PROXY=http://127.0.0.1:10809
    UPDATE_HTTPS_PROXY=http://127.0.0.1:10809
elif [ -n "$(ss -Hltn 'sport = :10808' 2>/dev/null)" ]; then
    echo "[INFO] Local SOCKS5 proxy detected at 10808, enabling for release downloads only..."
    UPDATE_HTTP_PROXY=socks5h://127.0.0.1:10808
    UPDATE_HTTPS_PROXY=socks5h://127.0.0.1:10808
fi
DL="env http_proxy=$UPDATE_HTTP_PROXY https_proxy=$UPDATE_HTTPS_PROXY curl -fsSL -4 --retry 3 --connect-timeout 10 -o"
API="env http_proxy=$UPDATE_HTTP_PROXY https_proxy=$UPDATE_HTTPS_PROXY curl --retry 3 --connect-timeout 5 --fail -s -4"

# Prevent proxy loop when scripts call local components or database tools.
export NO_PROXY=127.0.0.1,localhost
export no_proxy=127.0.0.1,localhost

echo "[1/7] Fetching latest changes..."
# Keep git transport direct/SSH and isolated from local HTTP/SOCKS proxy variables.
# This avoids GnuTLS handshake failures when the update script runs on the proxy gateway itself.
# --force tag sync tolerates locally stale tags when a stable tag is re-pointed (e.g. v1.6.1).
# The working tree is only reset in step 3, after the new binary has been
# downloaded and verified: a failed download used to leave the tree (and the
# frontend the running backend serves from disk) already moved to origin/main.
env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u all_proxy git fetch --force origin --tags

echo "[2/7] Downloading backend from GitHub Releases..."
ARCH=$(uname -m)
case "$ARCH" in
    x86_64) XRAY_ARCH="64"; MOSDNS_ARCH="amd64" ;;
    aarch64) XRAY_ARCH="arm64-v8a"; MOSDNS_ARCH="arm64" ;;
    *) echo "Error: unsupported architecture ${ARCH} (need x86_64 or aarch64)"; exit 1 ;;
esac
# Same filesystem as the target so the final swap is an atomic rename; the
# name is gitignored so the sync in step 3 leaves it alone.
TMP_BACKEND="$REPO_DIR/backend/proxygw-backend.new"

# Prefer latest published release tag from GitHub API
PROXYGW_LATEST=$($API https://api.github.com/repos/zlylong/EdgeRouteGW/releases/latest | jq -r '.tag_name // empty' || true)

# Fallback to local tag list (requires --tags fetch above)
if [ -z "$PROXYGW_LATEST" ] && [ -d "$REPO_DIR/.git" ]; then
    # Stable tags only: version sort ranks v1.9.0-rc.1 above v1.8.1, and the
    # API path this replaces never returns pre-releases.
    PROXYGW_LATEST=$(cd "$REPO_DIR" && git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n1 || true)
fi

# Last resort: the tag the current checkout describes. No hardcoded version:
# an update that cannot determine a release must stop rather than downgrade.
if [ -z "$PROXYGW_LATEST" ] && [ -d "$REPO_DIR/.git" ]; then
    PROXYGW_LATEST=$(cd "$REPO_DIR" && git describe --tags --abbrev=0 --exclude '*-*' 2>/dev/null || true)
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
        echo "Error: SHA256SUMS for ${tag} has no entry for ${asset}"; rm -f "$file"; return 1
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
if ! $DL "$TMP_BACKEND" "https://github.com/zlylong/EdgeRouteGW/releases/download/${PROXYGW_LATEST}/${BACKEND_ASSET}"; then
    rm -f "$TMP_BACKEND"
    echo "Error: failed to download ${BACKEND_ASSET} for ${PROXYGW_LATEST}"; exit 1
fi
DOWNLOAD_CMD="$DL" verify_backend_checksum "$TMP_BACKEND" "$BACKEND_ASSET" "$PROXYGW_LATEST" || exit 1
chmod +x "$TMP_BACKEND"

echo "[3/7] Syncing repository..."
# config/aes.key is generated on first boot and is the only copy of the key
# every stored SSH credential is encrypted with. It used to be a tracked file,
# so "git reset --hard" below replaced it with the committed placeholder and
# every credential became undecryptable on the next start. Carry it across
# the reset regardless of what the tree says about it, and put it back even
# if the reset itself fails half-way.
AES_KEY_BACKUP=""
restore_aes_key() {
    if [ -n "$AES_KEY_BACKUP" ] && [ -f "$AES_KEY_BACKUP" ]; then
        mkdir -p "$REPO_DIR/config"
        cp -p "$AES_KEY_BACKUP" "$REPO_DIR/config/aes.key"
        rm -f "$AES_KEY_BACKUP"
    fi
}
if [ -f "$REPO_DIR/config/aes.key" ]; then
    AES_KEY_BACKUP=$(mktemp)
    cp -p "$REPO_DIR/config/aes.key" "$AES_KEY_BACKUP"
    trap restore_aes_key EXIT
fi
env -u http_proxy -u https_proxy -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u all_proxy git reset --hard origin/main
# Untracked files only; ignored ones (geodata, generated configs, the DB, the
# .prev/.new binaries) are left alone.
git clean -fd
restore_aes_key
trap - EXIT

# ensure_core_binaries: (re)download Xray and Mosdns when the binaries under
# core/ cannot run on this host. The repository tracks amd64 builds of both, so
# on arm64 they have to be replaced after every clone and after every
# `git reset --hard` (update.sh), or mosdns/xray die with "exec format error".
# The check is "does `<bin> version` run", which is what matters for systemd.
# Downloads honour CORE_DOWNLOAD_CMD (FILE URL) and CORE_API_CMD (URL) so the
# caller can route them through a proxy.
ensure_core_binaries() {
    local tmp; tmp=$(mktemp -d)
    if ! "$REPO_DIR/core/xray/xray" version >/dev/null 2>&1; then
        echo "Downloading Xray for $ARCH..."
        local xray_url="https://github.com/XTLS/Xray-core/releases/latest/download/Xray-linux-${XRAY_ARCH}.zip"
        if ! ${CORE_DOWNLOAD_CMD:-wget -q -4 -O} "$tmp/xray.zip" "$xray_url"; then
            echo "Error: failed to download Xray (${xray_url})" >&2
            rm -rf "$tmp"; return 1
        fi
        # XTLS publishes a digest next to every asset; this binary runs as root.
        ${CORE_DOWNLOAD_CMD:-wget -q -4 -O} "$tmp/xray.zip.dgst" "${xray_url}.dgst" 2>/dev/null || true
        local want got
        want=$(awk '/^SHA2-256=/ {print $2}' "$tmp/xray.zip.dgst" 2>/dev/null || true)
        got=$(sha256sum "$tmp/xray.zip" | awk '{print $1}')
        if [ -z "$want" ]; then
            echo "Warning: could not fetch Xray digest; skipping verification"
        elif [ "$want" != "$got" ]; then
            echo "Error: Xray download failed verification (expected $want, got $got)" >&2
            rm -rf "$tmp"; return 1
        else
            echo "Xray download verified"
        fi
        unzip -qo "$tmp/xray.zip" xray -d "$REPO_DIR/core/xray/"
    fi
    chmod +x "$REPO_DIR/core/xray/xray" || true

    if ! "$REPO_DIR/core/mosdns/mosdns" version >/dev/null 2>&1; then
        echo "Downloading Mosdns for $ARCH..."
        local mosdns_latest
        mosdns_latest=$(${CORE_API_CMD:-curl --retry 3 --connect-timeout 5 --fail -s -4} https://api.github.com/repos/IrineSistiana/mosdns/releases/latest | jq -r '.tag_name // empty' || true)
        if [ -z "$mosdns_latest" ]; then
            echo "Error: could not determine the latest Mosdns release; the current core/mosdns/mosdns does not run on this host" >&2
            rm -rf "$tmp"; return 1
        fi
        if ! ${CORE_DOWNLOAD_CMD:-wget -q -4 -O} "$tmp/mosdns.zip" "https://github.com/IrineSistiana/mosdns/releases/download/${mosdns_latest}/mosdns-linux-${MOSDNS_ARCH}.zip"; then
            echo "Error: failed to download Mosdns ${mosdns_latest} for ${MOSDNS_ARCH}" >&2
            rm -rf "$tmp"; return 1
        fi
        unzip -qo "$tmp/mosdns.zip" mosdns -d "$REPO_DIR/core/mosdns/"
    fi
    chmod +x "$REPO_DIR/core/mosdns/mosdns" || true
    rm -rf "$tmp"

    local bin
    for bin in core/xray/xray core/mosdns/mosdns; do
        if ! "$REPO_DIR/$bin" version >/dev/null 2>&1; then
            echo "Error: $bin is not runnable on this host ($ARCH)" >&2
            return 1
        fi
    done
}

# The reset just put the tracked (amd64) core binaries back; on arm64 they must
# be replaced before anything restarts mosdns or xray.
CORE_DOWNLOAD_CMD="$DL" CORE_API_CMD="$API" ensure_core_binaries || exit 1

# Now that downloads are complete, stop the service to perform the swap.
# Keep the previous binary next to the new one so a failed start can be rolled
# back without another download.
BACKEND_BIN="$REPO_DIR/backend/proxygw-backend"
BACKEND_PREV="$BACKEND_BIN.prev"
systemctl stop proxygw >/dev/null 2>&1 || true
if [ -f "$BACKEND_BIN" ]; then
    cp -p "$BACKEND_BIN" "$BACKEND_PREV"
fi
mv -f "$TMP_BACKEND" "$BACKEND_BIN"

echo "[4/7] Writing Systemd services..."
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
# The generated config logs under /run/proxygw, which proxygw.service creates
# (RuntimeDirectory=). At boot Xray may start first, and a missing log
# directory is one of the exit-23 cases RestartPreventExitStatus stops retrying.
ExecStartPre=/bin/mkdir -p /run/proxygw
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

echo "[5/7] Automatically flushing old DNS and OSPF caches..."
# One statement per call: the sqlite3 shell stops at the first failing
# statement, so a single missing table used to leave the other two unflushed.
if [ -f "$REPO_DIR/config/proxygw.db" ]; then
    for t in domain_resolve_cache routes_table geosite_expand_cache; do
        sqlite3 -cmd ".timeout 5000" "$REPO_DIR/config/proxygw.db" "DELETE FROM $t;" 2>/dev/null || echo "Warning: could not flush $t"
    done
fi

echo "[6/7] Restarting services..."
# A binary that cannot even be exec'd makes this restart fail immediately;
# under set -e that used to abort the script right here, before the health
# check and rollback below ever ran. Let the health check decide.
systemctl restart proxygw || true

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
    sleep 2
    if systemctl is-active --quiet proxygw; then
        echo "Rolled back: the previous binary is running again"
    else
        echo "Rollback did not bring proxygw up either; inspect: journalctl -u proxygw -n 50 --no-pager"
    fi
    return 1
}

echo "[7/7] Verifying backend health..."
# Type=simple reports active as soon as the process is forked, so "active on
# the first poll" says nothing about a binary that dies while opening the
# database two seconds later. Require the unit to stay active for the whole
# 10-second window and to have needed no automatic restart in that time.
healthy=1
for _ in $(seq 1 10); do
    sleep 1
    if ! systemctl is-active --quiet proxygw; then
        healthy=0; break
    fi
done
if [ "$healthy" = "1" ] && [ "$(systemctl show -p NRestarts --value proxygw 2>/dev/null || echo 0)" != "0" ]; then
    healthy=0
fi
if [ "$healthy" != "1" ]; then
    systemctl status proxygw --no-pager -n 20 || true
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