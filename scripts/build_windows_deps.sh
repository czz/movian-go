#!/usr/bin/env bash
#
# build_windows_deps.sh - vendored third-party deps for the Windows
# (mingw-w64) build: zlib, glfw3, libxml2. Installs into
# third_party/<dep>/windows/<arch>/{include,lib} following the
# third_party/<dep>/android/<abi>/ convention.
#
# No upstream C counterpart — upstream Movian has no Windows port.
# FreeType is not vendored here anymore: fonts run inside the pure-Go
# wasm2go module (internal/ftwasm) on all platforms.
#
# Usage: ./scripts/build_windows_deps.sh [amd64|386] [--force]
#   amd64 → third_party/*/windows/amd64 (x86_64-w64-mingw32)
#   386   → third_party/*/windows/386   (i686-w64-mingw32)
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
TP="$ROOT/third_party"
GOARCH=amd64
FORCE=0
for arg in "$@"; do
    case "$arg" in
        --force)      FORCE=1 ;;
        amd64|386)    GOARCH="$arg" ;;
        *)            ;; # ignore unknown args
    esac
done

case "$GOARCH" in
    amd64) MINGW_PREFIX=x86_64-w64-mingw32 ;;
    386)   MINGW_PREFIX=i686-w64-mingw32 ;;
esac
CC=${MINGW_PREFIX}-gcc
AR=${MINGW_PREFIX}-ar
JOBS=$(nproc)

log() { printf '[win-deps:%s] %s\n' "$GOARCH" "$*"; }
die() { printf '[win-deps:%s] ERROR: %s\n' "$GOARCH" "$*" >&2; exit 1; }
command -v "$CC" >/dev/null || die "$CC not found (apt install gcc-mingw-w64)"

# Canonical dependency manifest (name/version/URL/SHA-256) + verified fetch.
source "$ROOT/scripts/deps.env"
source "$ROOT/scripts/fetch_dep.sh"
ZLIB_VER=$ZLIB_VERSION
GLFW_VER=$GLFW_VERSION

# Verified download cache in .deps/ (gitignored, survives cleans).
ZLIB_A=$(dep_fetch "$ZLIB_TARBALL" "$ZLIB_URL" "$ZLIB_SHA256") || die "zlib fetch failed"
GLFW_A=$(dep_fetch "$GLFW_TARBALL" "$GLFW_URL" "$GLFW_SHA256") || die "glfw fetch failed"
XML2_A=$(dep_fetch "$LIBXML2_TARBALL" "$LIBXML2_URL" "$LIBXML2_SHA256") || die "libxml2 fetch failed"
# zlib builds in-source (win32/Makefile.gcc drops .o next to the .c) and the
# tarball always lands as zlib-<ver>/, so give each arch its own copy or
# objects leak across arches.
if [[ ! -d "/tmp/zlib-${ZLIB_VER}-${GOARCH}" ]]; then
    rm -rf "/tmp/zlib-${ZLIB_VER}"
    dep_unpack_tarball "$ZLIB_A" "/tmp/zlib-${ZLIB_VER}"
    mv "/tmp/zlib-${ZLIB_VER}" "/tmp/zlib-${ZLIB_VER}-${GOARCH}"
fi
dep_unpack_zip "$GLFW_A" "/tmp/glfw-${GLFW_VER}"
dep_unpack_tarball "$XML2_A" "/tmp/libxml2-${LIBXML2_VERSION}"

# ---- zlib -----------------------------------------------------------------
ZP="$TP/zlib/windows/$GOARCH"
if [[ ! -f "$ZP/lib/libz.a" || $FORCE -eq 1 ]]; then
    log "zlib ${ZLIB_VER}"
    rm -rf /tmp/zlib-build-win-$GOARCH && mkdir -p /tmp/zlib-build-win-$GOARCH "$ZP/lib" "$ZP/include"
    (cd /tmp/zlib-${ZLIB_VER}-${GOARCH} && \
     make -f win32/Makefile.gcc PREFIX=${MINGW_PREFIX}- -j"$JOBS" >/dev/null && \
     cp zlib.h zconf.h "$ZP/include/" && cp libz.a "$ZP/lib/")
else
    log "zlib already built"
fi

# ---- glfw3 -----------------------------------------------------------------
GP="$TP/glfw/windows/$GOARCH"
if [[ ! -f "$GP/lib/libglfw3.a" || $FORCE -eq 1 ]]; then
    log "glfw ${GLFW_VER}"
    rm -rf /tmp/glfw-build-win-$GOARCH && mkdir -p /tmp/glfw-build-win-$GOARCH "$GP"
    cat > /tmp/glfw-build-win-$GOARCH/toolchain.cmake <<EOF
set(CMAKE_SYSTEM_NAME Windows)
set(CMAKE_C_COMPILER $CC)
set(CMAKE_RC_COMPILER ${MINGW_PREFIX}-windres)
EOF
    (cd /tmp/glfw-build-win-$GOARCH && cmake /tmp/glfw-${GLFW_VER} \
        -DCMAKE_TOOLCHAIN_FILE=toolchain.cmake \
        -DCMAKE_BUILD_TYPE=Release \
        -DCMAKE_INSTALL_PREFIX="$GP" \
        -DBUILD_SHARED_LIBS=OFF \
        -DGLFW_BUILD_EXAMPLES=OFF -DGLFW_BUILD_TESTS=OFF \
        -DGLFW_BUILD_DOCS=OFF >/dev/null && \
     make -j"$JOBS" >/dev/null && make install >/dev/null)
else
    log "glfw already built"
fi

# sqlite3 is no longer vendored — the DB layer runs on the pure-Go
# modernc.org/sqlite implementation on every platform.

# ---- libxml2 ---------------------------------------------------------------
# FFmpeg's DASH demuxer requires libxml2 (dash_demuxer_deps). Minimal
# static build — the MPD parser only reads XML: no python/zlib/lzma/
# iconv/http/ftp.
XP="$TP/libxml2/windows/$GOARCH"
if [[ ! -f "$XP/lib/libxml2.a" || $FORCE -eq 1 ]]; then
    log "libxml2 ${LIBXML2_VERSION}"
    rm -rf /tmp/libxml2-build-win-$GOARCH && mkdir -p /tmp/libxml2-build-win-$GOARCH "$XP"
    (cd /tmp/libxml2-build-win-$GOARCH && \
     CC="$CC" AR="$AR" \
     /tmp/libxml2-${LIBXML2_VERSION}/configure \
        --host=${MINGW_PREFIX} --prefix="$XP" \
        --enable-static --disable-shared \
        --with-pic \
        --without-python \
        --without-zlib --without-lzma \
        --without-iconv --without-icu \
        --without-http --without-ftp \
        --without-readline --without-schemas \
        --without-schematron --without-modules \
        >/dev/null && \
     make -j"$JOBS" >/dev/null && make install >/dev/null)
else
    log "libxml2 already built"
fi

log "done -> third_party/*/windows/$GOARCH"
