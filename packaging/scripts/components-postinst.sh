#!/bin/sh
set -e
# Ask a running edgeroutegw to seed the bundled components into its data
# directory; a stopped one does it on its next start.
if [ -d /run/systemd/system ] && systemctl is-active --quiet edgeroutegw.service; then
    systemctl kill -s HUP edgeroutegw.service 2>/dev/null || true
fi
exit 0
