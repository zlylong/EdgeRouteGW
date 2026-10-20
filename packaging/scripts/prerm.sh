#!/bin/sh
set -e
# Stop the service on remove/upgrade. Data in /var/lib/edgeroutegw is kept;
# purging it is a deliberate operator action, not something dpkg does for you.
if [ -d /run/systemd/system ]; then
    systemctl stop edgeroutegw.service >/dev/null 2>&1 || true
    if [ "$1" = "remove" ]; then
        systemctl disable edgeroutegw.service >/dev/null 2>&1 || true
    fi
fi
exit 0
