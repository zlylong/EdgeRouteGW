#!/usr/bin/env bash
# build_frontend_css.sh — compile frontend/dist/libs/app.css with the Tailwind standalone CLI.
#
# Usage:  scripts/build_frontend_css.sh            (from any cwd)
#         TAILWIND_VERSION=v3.4.17 scripts/build_frontend_css.sh
#
# Downloads the Tailwind CSS standalone binary (once, cached under
# frontend/.cache/, which is gitignored), then runs it against
# frontend/tailwind.config.js + frontend/src/tailwind.css and writes the
# minified stylesheet to frontend/dist/libs/app.css.
#
# Re-run this whenever frontend/dist/index.html or frontend/dist/libs/app.js
# gains a Tailwind class that was not used before (the CSS is precompiled; the
# in-browser JIT runtime is no longer shipped).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

TAILWIND_VERSION="${TAILWIND_VERSION:-v3.4.17}"
CACHE_DIR="$ROOT_DIR/frontend/.cache"
CONFIG="$ROOT_DIR/frontend/tailwind.config.js"
INPUT="$ROOT_DIR/frontend/src/tailwind.css"
OUTPUT="$ROOT_DIR/frontend/dist/libs/app.css"

case "$(uname -s)" in
  Linux)  os="linux" ;;
  Darwin) os="macos" ;;
  *) echo "Unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64)  arch="x64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

ASSET="tailwindcss-${os}-${arch}"
BIN="$CACHE_DIR/${ASSET}-${TAILWIND_VERSION}"
URL="https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/${ASSET}"

mkdir -p "$CACHE_DIR"
if [ ! -x "$BIN" ]; then
  echo "Downloading Tailwind CLI ${TAILWIND_VERSION} (${ASSET}) ..."
  tmp="$(mktemp "$CACHE_DIR/.download.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  curl -fsSL --retry 3 -o "$tmp" "$URL"
  chmod +x "$tmp"
  mv "$tmp" "$BIN"
  trap - EXIT
fi

if [ ! -x "$BIN" ]; then
  echo "Tailwind CLI is not executable: $BIN" >&2
  exit 1
fi
"$BIN" --help >/dev/null 2>&1 || { echo "Tailwind CLI failed to run: $BIN" >&2; exit 1; }

for f in "$CONFIG" "$INPUT"; do
  [ -f "$f" ] || { echo "Missing input: $f" >&2; exit 1; }
done
mkdir -p "$(dirname "$OUTPUT")"

# Tailwind resolves the (relative: true) content globs against the config
# file, and everything else against the cwd; run from the repo root so the
# script behaves the same from any directory.
cd "$ROOT_DIR"
"$BIN" -c frontend/tailwind.config.js -i frontend/src/tailwind.css -o frontend/dist/libs/app.css --minify

echo "Wrote $OUTPUT ($(wc -c < "$OUTPUT") bytes)"
