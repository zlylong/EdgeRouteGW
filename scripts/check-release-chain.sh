#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

CHANGELOG="$ROOT_DIR/docs/CHANGELOG.md"
FRONTEND="$ROOT_DIR/frontend/dist/index.html"
INSTALL_SH="$ROOT_DIR/scripts/install.sh"
UPDATE_SH="$ROOT_DIR/scripts/update.sh"
RELEASE_YML="$ROOT_DIR/.github/workflows/release.yml"

LATEST_VERSION=$(grep -m1 '^## \[[0-9]\+\.[0-9]\+\.[0-9]\+\] - ' "$CHANGELOG" | sed -E 's/^## \[([^]]+)\].*/v\1/')
if [[ -z "$LATEST_VERSION" ]]; then
  echo "failed: could not detect latest version from CHANGELOG"
  exit 1
fi

grep -q "EdgeRouteGW ${LATEST_VERSION}" "$FRONTEND" || {
  echo "failed: frontend version mismatch, expected ${LATEST_VERSION}"
  exit 1
}

# The scripts must verify the backend binary against the release's SHA256SUMS
# and refuse to install without it; and they must not carry a hardcoded
# fallback release tag (it silently downgraded once stale).
for f in "$INSTALL_SH" "$UPDATE_SH"; do
  grep -q 'verify_backend_checksum()' "$f" || { echo "failed: $(basename "$f") lacks verify_backend_checksum"; exit 1; }
  grep -q 'PROXYGW_ALLOW_UNVERIFIED' "$f" || { echo "failed: $(basename "$f") lacks the PROXYGW_ALLOW_UNVERIFIED override"; exit 1; }
  grep -q 'refusing to install an unverified binary' "$f" || { echo "failed: $(basename "$f") does not fail closed on a missing SHA256SUMS"; exit 1; }
  if grep -q 'PROXYGW_LATEST="v' "$f"; then
    echo "failed: $(basename "$f") still hardcodes a fallback release tag"; exit 1
  fi
done

if ! grep -q 'name: EdgeRouteGW \${{ github.ref_name }} Stable' "$RELEASE_YML" \
   && ! grep -q "contains(github.ref_name, '-rc.')" "$RELEASE_YML"; then
  echo "failed: release workflow missing stable/pre-release title logic"
  exit 1
fi

grep -q 'backend/SHA256SUMS' "$RELEASE_YML" || {
  echo "failed: release workflow does not publish SHA256SUMS; install.sh/update.sh verify against it"
  exit 1
}

grep -q 'body_path: /tmp/release_notes.md' "$RELEASE_YML" || {
  echo "failed: release workflow missing changelog-driven notes"
  exit 1
}

grep -q 'FORCE_JAVASCRIPT_ACTIONS_TO_NODE24: true' "$RELEASE_YML" || {
  echo "failed: release workflow missing node24 env"
  exit 1
}

echo "ok: release chain aligned with ${LATEST_VERSION}"