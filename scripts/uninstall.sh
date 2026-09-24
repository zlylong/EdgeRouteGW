#!/bin/bash
# EdgeRouteGW Uninstall Script

set -euo pipefail

echo "=== EdgeRouteGW Uninstallation ==="

echo "[1/6] Stopping the backend..."
# The backend first: it is what rewrites the ruleset and routes below.
systemctl disable --now proxygw 2>/dev/null || true

echo "[2/6] Removing routing and nftables rules..."
# Remove our rules and table before stopping Xray, otherwise LAN traffic is
# still TPROXYed to a port nothing listens on for the rest of this script.
# Loop: the backend re-adds the rule on every reconcile, so duplicates happen.
while ip rule del fwmark 1 lookup tproxy 2>/dev/null; do :; done
while ip -6 rule del fwmark 1 lookup tproxy 2>/dev/null; do :; done
ip route flush table tproxy 2>/dev/null || true
ip -6 route flush table tproxy 2>/dev/null || true
rm -f /etc/iproute2/rt_tables.d/proxygw.conf
sed -i '/100 tproxy/d' /etc/iproute2/rt_tables 2>/dev/null || true
nft delete table inet proxygw 2>/dev/null || true
if [ -f /etc/nftables.conf.pre-proxygw ]; then
    mv -f /etc/nftables.conf.pre-proxygw /etc/nftables.conf
else
    rm -f /etc/nftables.conf
    # install.sh enabled nftables.service to load /etc/nftables.conf at boot;
    # with that file gone the unit would fail on every boot. Disable it without
    # --now: stopping nftables.service flushes the whole ruleset, not just ours.
    systemctl disable nftables 2>/dev/null || true
fi

echo "[3/6] Stopping and disabling services..."
# One unit per call so a missing unit file does not skip the others.
for unit in mosdns xray frr; do
    systemctl disable --now "$unit" 2>/dev/null || true
done
rm -f /etc/frr/frr.conf

echo "[4/6] Removing Systemd units..."
rm -f /etc/systemd/system/proxygw.service
rm -f /etc/systemd/system/mosdns.service
rm -f /etc/systemd/system/xray.service
# Drop-ins created with `systemctl edit proxygw` (the documented way to set
# PROXYGW_* environment overrides) live next to the unit.
rm -rf /etc/systemd/system/proxygw.service.d
# Note: we don't remove frr.service as it's a system package, just disable it.
systemctl daemon-reload

echo "[5/6] Removing kernel tunings..."
rm -f /etc/sysctl.d/99-proxygw.conf
sysctl --system >/dev/null 2>&1 || true
echo "  (ip_forward, route_localnet and the BBR/qdisc settings stay in effect until reboot)"

echo "[5.5/6] Restoring DNS and systemd-resolved..."
if [ -f /etc/systemd/resolved.conf ]; then
    sed -i 's/^DNSStubListener=no/#DNSStubListener=yes/' /etc/systemd/resolved.conf || true
    systemctl restart systemd-resolved 2>/dev/null || true
fi
chattr -i /etc/resolv.conf 2>/dev/null || true
if [ -f /etc/resolv.conf.pre-proxygw ]; then
    # Put back what install.sh found.
    rm -f /etc/resolv.conf
    mv -f /etc/resolv.conf.pre-proxygw /etc/resolv.conf
elif systemctl is-enabled systemd-resolved >/dev/null 2>&1 && [ -e /run/systemd/resolve/stub-resolv.conf ]; then
    rm -f /etc/resolv.conf
    ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
else
    # No resolved on this host: the static public-DNS file install.sh wrote is
    # the only working configuration left, so keep it rather than a dead link.
    echo "  systemd-resolved is not enabled; keeping the current /etc/resolv.conf"
fi

echo "[6/6] Cleaning up directories..."
# Under set -e a failed read (stdin closed, e.g. `curl | bash` or a non-tty
# session) would abort the script here; treat it as "keep the directory".
answer=""
read -r -p "Do you want to delete the /root/proxygw directory (including the database and configs)? [y/N]: " answer || true
if [[ "$answer" =~ ^[Yy]$ ]]; then
    rm -rf /root/proxygw
    echo "Directory /root/proxygw removed."
else
    echo "Directory /root/proxygw retained."
fi

echo "Uninstallation complete!"
