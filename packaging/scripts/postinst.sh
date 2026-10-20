#!/bin/sh
set -e
# Runs after the files are unpacked. Keep it idempotent: upgrades call it too.

install -d -m 0750 /var/lib/edgeroutegw /etc/edgeroutegw
install -d -m 0755 /var/lib/edgeroutegw/components /var/lib/edgeroutegw/geodata /var/lib/edgeroutegw/xray /var/lib/edgeroutegw/mosdns

if command -v sysctl >/dev/null 2>&1; then
    sysctl -q -p /etc/sysctl.d/99-edgeroutegw.conf >/dev/null 2>&1 || true
fi

# Apply schema migrations before the unit (re)starts so a failed migration
# surfaces here, in the package transaction, rather than as a crash loop.
/usr/bin/edgeroutegw migrate -data-dir /var/lib/edgeroutegw

if [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    systemctl enable edgeroutegw.service >/dev/null 2>&1 || true
    if systemctl is-active --quiet edgeroutegw.service; then
        systemctl restart edgeroutegw.service || true
    else
        systemctl start edgeroutegw.service || true
    fi
fi
exit 0
