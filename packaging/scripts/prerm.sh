#!/bin/sh
set -e
# Stop the service on remove/upgrade. Data in /var/lib/edgeroutegw is kept;
# purging it is a deliberate operator action, not something dpkg does for you.
if [ "${EDGEROUTEGW_SELFUPDATE:-}" = "1" ]; then
    : # in-app update: the running service installed us and restarts itself
elif [ -d /run/systemd/system ]; then
    systemctl stop edgeroutegw.service >/dev/null 2>&1 || true
    if [ "$1" = "remove" ]; then
        systemctl disable edgeroutegw.service >/dev/null 2>&1 || true
    fi
fi
exit 0
