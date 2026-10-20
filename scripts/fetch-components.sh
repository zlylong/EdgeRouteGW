#!/bin/bash
# Downloads and verifies the pinned Xray, Mosdns and GeoData releases for one
# architecture into OUT/{xray,mosdns,geodata} plus OUT/VERSIONS. Used by the
# release workflow to build edgeroutegw-components and the offline bundle;
# usable by hand on any machine that can reach GitHub.
#   scripts/fetch-components.sh amd64 dist/components/amd64
set -euo pipefail
ARCH=${1:?arch (amd64|arm64)}
OUT=${2:?output dir}
LOCK="$(dirname "$0")/../packaging/components.lock"
# shellcheck disable=SC1090
. "$LOCK"
case "$ARCH" in
  amd64) XRAY_ASSET="Xray-linux-64.zip" ;;
  arm64) XRAY_ASSET="Xray-linux-arm64-v8a.zip" ;;
  *) echo "unsupported arch $ARCH" >&2; exit 1 ;;
esac
MOSDNS_ASSET="mosdns-linux-$ARCH.zip"
CURL="curl -fsSL --retry 5 --retry-delay 3"
# The releases API is rate limited for anonymous callers; CI passes its token.
API_AUTH=(); [ -n "${GITHUB_TOKEN:-}" ] && API_AUTH=(-H "Authorization: Bearer $GITHUB_TOKEN")
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT/xray" "$OUT/mosdns" "$OUT/geodata"

echo "==> xray $XRAY_VERSION ($XRAY_ASSET)"
$CURL -o "$WORK/xray.zip" "https://github.com/XTLS/Xray-core/releases/download/$XRAY_VERSION/$XRAY_ASSET"
$CURL -o "$WORK/xray.zip.dgst" "https://github.com/XTLS/Xray-core/releases/download/$XRAY_VERSION/$XRAY_ASSET.dgst"
WANT=$(awk '/^SHA2-256=/ {print $2}' "$WORK/xray.zip.dgst")
echo "$WANT  $WORK/xray.zip" | sha256sum -c - >/dev/null
unzip -oq "$WORK/xray.zip" xray -d "$WORK/xray"
install -m 0755 "$WORK/xray/xray" "$OUT/xray/xray"

echo "==> mosdns $MOSDNS_VERSION ($MOSDNS_ASSET)"
$CURL -o "$WORK/mosdns.zip" "https://github.com/IrineSistiana/mosdns/releases/download/$MOSDNS_VERSION/$MOSDNS_ASSET"
# mosdns publishes no digest file; the releases API carries one per asset.
DIGEST=$($CURL "${API_AUTH[@]}" -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/IrineSistiana/mosdns/releases/tags/$MOSDNS_VERSION" \
  | python3 -c "import json,sys; a=[x for x in json.load(sys.stdin)['assets'] if x['name']=='$MOSDNS_ASSET']; print(a[0].get('digest','').replace('sha256:','') if a else '')")
[ -n "$DIGEST" ] || { echo "no digest for $MOSDNS_ASSET" >&2; exit 1; }
echo "$DIGEST  $WORK/mosdns.zip" | sha256sum -c - >/dev/null
unzip -oq "$WORK/mosdns.zip" mosdns -d "$WORK/mosdns"
install -m 0755 "$WORK/mosdns/mosdns" "$OUT/mosdns/mosdns"

echo "==> geodata $GEODATA_VERSION"
$CURL -o "$WORK/rules.zip" "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/$GEODATA_VERSION/rules.zip"
$CURL -o "$WORK/rules.zip.sha256sum" "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/$GEODATA_VERSION/rules.zip.sha256sum"
echo "$(awk '{print $1}' "$WORK/rules.zip.sha256sum")  $WORK/rules.zip" | sha256sum -c - >/dev/null
unzip -oq "$WORK/rules.zip" geoip.dat geosite.dat -d "$OUT/geodata"
printf '%s\n' "$GEODATA_VERSION" > "$OUT/geodata/geodata.ver"

cat > "$OUT/VERSIONS" <<V
xray=$XRAY_VERSION
mosdns=$MOSDNS_VERSION
geodata=$GEODATA_VERSION
arch=$ARCH
V
echo "==> done:"; cat "$OUT/VERSIONS"; du -sh "$OUT"
