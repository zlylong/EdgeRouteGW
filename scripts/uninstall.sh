#!/bin/bash
# ProxyGW Uninstall Script

set -euo pipefail

echo "=== ProxyGW Uninstallation ==="

echo "[1/5] Stopping and disabling services..."
systemctl disable --now proxygw mosdns xray frr || true

echo "[2/5] Removing Systemd units..."
rm -f /etc/systemd/system/proxygw.service
rm -f /etc/systemd/system/mosdns.service
rm -f /etc/systemd/system/xray.service
# Note: we don't remove frr.service as it's a system package, just disable it.
systemctl daemon-reload

echo "[3/5] Removing routing and iptables rules..."
ip rule del fwmark 1 table tproxy 2>/dev/null || true
ip route flush table tproxy 2>/dev/null || true
sed -i '/100 tproxy/d' /etc/iproute2/rt_tables || true
nft flush table inet proxygw 2>/dev/null || true
nft delete table inet proxygw 2>/dev/null || true
rm -f /etc/nftables.conf /etc/frr/frr.conf

echo "[4/5] Removing kernel tunings..."
rm -f /etc/sysctl.d/99-proxygw.conf
sysctl --system || true


echo "[4.5/5] Restoring DNS and systemd-resolved..."
if [ -f /etc/systemd/resolved.conf ]; then
    sed -i 's/DNSStubListener=no/#DNSStubListener=yes/' /etc/systemd/resolved.conf || true
    systemctl restart systemd-resolved 2>/dev/null || true
fi
chattr -i /etc/resolv.conf 2>/dev/null || true
rm -f /etc/resolv.conf
ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf 2>/dev/null || true

echo "[5/5] Cleaning up directories..."
read -p "Do you want to delete the /root/proxygw directory (including the database and configs)? [y/N]: " answer
if [[ "$answer" =~ ^[Yy]$ ]]; then
    rm -rf /root/proxygw
    echo "Directory /root/proxygw removed."
else
    echo "Directory /root/proxygw retained."
fi

echo "Uninstallation complete!"
