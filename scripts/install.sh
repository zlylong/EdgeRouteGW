#!/bin/bash
# EdgeRouteGW 2.0 installer: downloads the latest 2.x .deb for this machine,
# verifies it against the release's SHA256SUMS and installs it with apt.
#
#   bash <(curl -fsSL https://raw.githubusercontent.com/zlylong/EdgeRouteGW/main/scripts/install.sh)
#   ERGW_VERSION=v2.0.0 bash install.sh      # pin a version
#
# Debian 12/13 and Ubuntu 22.04/24.04, amd64 or arm64, run as root.
set -euo pipefail

REPO="zlylong/EdgeRouteGW"
VERSION="${ERGW_VERSION:-}"

if [ "$(id -u)" -ne 0 ]; then echo "run as root" >&2; exit 1; fi
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
if ! command -v apt-get >/dev/null; then echo "this installer needs a Debian/Ubuntu system (apt)" >&2; exit 1; fi

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl ca-certificates nftables iproute2 >/dev/null

if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=30" | grep -o '"tag_name": *"v2[^"]*"' | grep -v -- '-rc\|-beta' | head -1 | sed 's/.*"\(v2[^"]*\)"/\1/')
  [ -n "$VERSION" ] || { echo "could not determine the latest 2.x release" >&2; exit 1; }
fi
DEB="edgeroutegw_${VERSION#v}_${ARCH}.deb"
BASE="https://github.com/$REPO/releases/download/$VERSION"
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
echo "==> downloading $DEB ($VERSION)"
curl -fsSL --retry 3 -o "$WORK/$DEB" "$BASE/$DEB"
curl -fsSL --retry 3 -o "$WORK/SHA256SUMS" "$BASE/SHA256SUMS"
(cd "$WORK" && grep " $DEB\$" SHA256SUMS | sha256sum -c -) || { echo "checksum mismatch for $DEB; aborting" >&2; exit 1; }
echo "==> installing"
apt-get install -y -qq "$WORK/$DEB" >/dev/null
echo "==> installing frr (OSPF for Mode B/C; optional)"
apt-get install -y -qq frr >/dev/null || echo "frr not installed; Mode B/C unavailable until it is"

systemctl enable --now edgeroutegw.service >/dev/null
sleep 1
IP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -1)
echo
echo "EdgeRouteGW $VERSION is running."
echo "  UI:               http://${IP:-<this-host>}/"
if [ -f /var/lib/edgeroutegw/bootstrap_password.txt ]; then
  echo "  initial password: $(cat /var/lib/edgeroutegw/bootstrap_password.txt)   (change it at first login)"
fi
echo "  then: System -> Components -> install Xray, Mosdns and GeoData."
if [ -d /root/proxygw ]; then
  echo
  echo "A 1.x installation was found in /root/proxygw. To carry its configuration over:"
  echo "  systemctl stop edgeroutegw && edgeroutegw import-v1 --old-key /root/proxygw/config/aes.key /root/proxygw/config/proxygw.db && systemctl start edgeroutegw"
  echo "See docs/UPGRADE-FROM-1.x.md for the full procedure (stop the 1.x services first)."
fi
