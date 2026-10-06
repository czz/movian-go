#!/usr/bin/env bash
#
# build_glfw_linux.sh - vendored static GLFW for the Linux build.
# Installs into third_party/glfw/linux/{include,lib} following the
# third_party/<dep>/<platform>/<arch>/ convention.
#
# Why vendored: Debian/Ubuntu LTS ship libglfw3-dev 3.3.x — an
# X11-only build with no glfwGetPlatform()/GLFW_PLATFORM_* API (GLFW
# 3.4) and no Wayland symbols at all. Movian Go needs 3.4's runtime
# platform selection (X11 + Wayland on one binary). A static .a also
# keeps the dist binary free of a libglfw version dependency.
#
# The installed glfw3.pc is patched to fold Libs.private into Libs:
# cgo's "#cgo linux pkg-config: glfw3" does not pass --static, so
# private deps (-lrt -lm -ldl) would never reach the link line. GLFW
# 3.4 dlopens the X11/Wayland client libraries at runtime — that's how
# it selects the platform — so nothing else is needed.
#
# Build deps (apt): cmake unzip wayland-protocols libwayland-bin
# libwayland-dev libxkbcommon-dev libegl-dev libx11-dev libxrandr-dev
# libxinerama-dev libxcursor-dev libxi-dev
#
# Usage: ./scripts/build_glfw_linux.sh [--force]
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PREFIX="$ROOT/third_party/glfw/linux"
FORCE=0
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
        *) echo "unknown arg: $arg" >&2; exit 1 ;;
    esac
done

log() { printf '[glfw-linux] %s\n' "$*"; }
die() { printf '[glfw-linux] ERROR: %s\n' "$*" >&2; exit 1; }

if [[ -f "$PREFIX/lib/libglfw3.a" && $FORCE -eq 0 ]]; then
    log "already built: $PREFIX"
    exit 0
fi

command -v cmake           >/dev/null || die "cmake not found"
command -v unzip           >/dev/null || die "unzip not found"
command -v wayland-scanner >/dev/null || die "wayland-scanner not found (apt install libwayland-bin)"
pkg-config --exists wayland-client xkbcommon || die "wayland/xkbcommon dev packages missing"

source "$ROOT/scripts/deps.env"
source "$ROOT/scripts/fetch_dep.sh"

GLFW_A=$(dep_fetch "$GLFW_TARBALL" "$GLFW_URL" "$GLFW_SHA256") \
    || die "glfw fetch failed"

SRC="/tmp/glfw-${GLFW_VERSION}-linux-src"
[[ -d "$SRC" ]] || {
    dep_unpack_zip "$GLFW_A" "/tmp/glfw-${GLFW_VERSION}"
    mv "/tmp/glfw-${GLFW_VERSION}" "$SRC"
}

BLD="/tmp/glfw-linux-build"
rm -rf "$BLD" && mkdir -p "$BLD" "$PREFIX"
log "configuring (static, X11 + Wayland backends)"
cmake -S "$SRC" -B "$BLD" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX="$PREFIX" \
    -DBUILD_SHARED_LIBS=OFF \
    -DGLFW_BUILD_EXAMPLES=OFF \
    -DGLFW_BUILD_TESTS=OFF \
    -DGLFW_BUILD_DOCS=OFF >/dev/null
make -C "$BLD" -j"$(nproc)" >/dev/null
make -C "$BLD" install >/dev/null

# cgo does not pass --static to pkg-config: fold Libs.private into the
# public Libs line so -lrt/-lm/-ldl reach the link line. (GLFW 3.4
# dlopens X11/Wayland — these are the only direct deps left.)
PC=$(find "$PREFIX" -name glfw3.pc)
PRIV=$(sed -n 's/^Libs\.private: *//p' "$PC")
sed -i "/^Libs\.private:/d; s|^Libs: \(.*\)|Libs: \1 ${PRIV}|" "$PC"

log "done -> $PREFIX"
log "use: PKG_CONFIG_PATH=$(dirname "$PC") go build ..."
