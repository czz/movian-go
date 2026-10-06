#!/bin/bash
# Fetches a CEF (Chromium Embedded Framework) binary distribution
# into third_party/cef/<plat>/ and emits a cef.pc so that
#   PKG_CONFIG_PATH=$PWD/third_party/cef/<plat>
#   go build -tags 'webpopup cef'
# resolves the "#cgo pkg-config: cef" directive in
# internal/ui/webpopup_cef.go on every supported platform.
#
# Sources from the official CEF CDN index (cef-builds.spotifycdn.com).
# The index publishes SHA-1 hashes (verified below with sha1sum).
#
# Platforms: linux64 | windows64 | windows32 | macosx64 | macosarm64
# NOT available: android, linux-armv6/armv7 (RPi) — CEF ships no builds
# for them; webpopup falls back to the stub there.
#
# Usage: ./scripts/fetch_cef.sh <platform> [version]
#   ./scripts/fetch_cef.sh linux64
#   ./scripts/fetch_cef.sh windows64
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
CEF_VER="${2:-152.0.12+ge1f344f+chromium-152.0.7977.152}"
PLAT="${1:?usage: $0 linux64|windows64|windows32|macosx64|macosarm64 [version]}"

# CEF CDN publishes SHA-1 of each archive (index.json -> 'sha1' field).
# Plain case, not an assoc array: /bin/bash on macOS is still 3.2.
case "$PLAT" in
  linux64)    SHA=6ad60b31e8683919adc3a6bd9161e53dc93e0eb3 ;;
  windows64)  SHA=18e4970a5529eb1fb46a3b6da6b1debe645a61d0 ;;
  windows32)  SHA=fec33b07c40994891c2af39ce67c492377999915 ;;
  macosx64)   SHA=62ec6163c864e6913377fc5de277d5a6152948c0 ;;
  macosarm64) SHA=cc63672e602a72794ae2b487be06b253677a1b94 ;;
  *) echo "error: no CEF build for '$PLAT' (supported: linux64 windows64 windows32 macosx64 macosarm64)" >&2; exit 1 ;;
esac

URL_VER=${CEF_VER//+/%2B}
NAME="cef_binary_${CEF_VER}_${PLAT}_minimal"
URL="https://cef-builds.spotifycdn.com/${NAME}.tar.bz2?version=${URL_VER}"
DEST="$ROOT/third_party/cef/$PLAT"
TARBALL="$ROOT/.deps/${NAME}.tar.bz2"

mkdir -p "$ROOT/.deps"

if [ ! -f "$TARBALL" ]; then
  echo "[cef] downloading $NAME (~$(echo "$PLAT" | grep -q macos && echo 300 || echo 150)MB)"
  curl -fsSL -o "$TARBALL" "$URL"
fi

echo "[cef] verifying sha1"
if command -v sha1sum >/dev/null 2>&1; then
  got="$(sha1sum "$TARBALL" | awk '{print $1}')"
else
  got="$(shasum -a 1 "$TARBALL" | awk '{print $1}')"
fi
[ "$got" = "$SHA" ] || { echo "error: sha1 mismatch for $NAME (got $got, want $SHA)" >&2; exit 1; }

if [ ! -d "$DEST" ]; then
  echo "[cef] extracting to $DEST"
  mkdir -p "$DEST"
  tar -xjf "$TARBALL" -C "$DEST" --strip-components=1
fi

# macosx dists keep libcef inside the .framework bundle; link it as
# -lcef via a symlink so one cef.pc works across platforms.
if [[ "$PLAT" == macos* ]] && [ ! -e "$DEST/Release/libcef.dylib" ]; then
  ln -s "Chromium Embedded Framework.framework/Chromium Embedded Framework" \
        "$DEST/Release/libcef.dylib"
fi

if [ ! -f "$DEST/cef.pc" ]; then
  cat > "$DEST/cef.pc" <<EOF
prefix=$DEST
Name: cef
Description: Chromium Embedded Framework (minimal dist, $PLAT)
Version: ${CEF_VER%%+*}
Cflags: -I\${prefix}
Libs: -L\${prefix}/Release -lcef
EOF
fi

echo "[cef] done -> $DEST"
echo "[cef] use: PKG_CONFIG_PATH=$DEST go build -tags 'webpopup cef' ..."
