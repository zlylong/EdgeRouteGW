#!/bin/sh
set -e
# Ask a running edgeroutegw to seed the bundled components into its data
# directory; a stopped one does it on its next start.
if [ -d /run/systemd/system ] && systemctl is-active --quiet edgeroutegw.service; then
    systemctl kill -s HUP edgeroutegw.service 2>/dev/null || true
    # A HUP that arrives before the process can handle it ends it; the unit
    # restarts itself (Restart=always), but make sure either way.
    sleep 2
    systemctl is-active --quiet edgeroutegw.service || systemctl start edgeroutegw.service || true
fi
exit 0
