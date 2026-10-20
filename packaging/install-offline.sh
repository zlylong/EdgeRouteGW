#!/bin/bash
# Offline installer shipped inside edgeroutegw-full_<version>_<arch>.tar.gz:
# installs the two .deb files next to it (edgeroutegw and
# edgeroutegw-components) with apt, no network needed. frr is optional and
# only required for Mode B/C; install it from your distribution if needed.
set -euo pipefail
cd "$(dirname "$0")"
if [ "$(id -u)" -ne 0 ]; then echo "run as root" >&2; exit 1; fi
sha256sum -c SHA256SUMS
export DEBIAN_FRONTEND=noninteractive
apt-get install -y ./edgeroutegw_*.deb ./edgeroutegw-components_*.deb
systemctl enable --now edgeroutegw.service
sleep 2
echo
echo "EdgeRouteGW installed with bundled Xray, Mosdns and GeoData."
if [ -f /var/lib/edgeroutegw/bootstrap_password.txt ]; then
  echo "initial password: $(cat /var/lib/edgeroutegw/bootstrap_password.txt)"
fi
echo "open http://<this-host>/ ; the components are seeded on first start (System -> Components shows them as installed)."
