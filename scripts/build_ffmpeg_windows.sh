#!/usr/bin/env bash
#
# build_ffmpeg_windows.sh - FFmpeg 9.0.1 static libraries for Windows
# amd64 via mingw-w64, installed to ffmpeg/windows/amd64/.
#
# Mirrors the configure choices of build_ffmpeg_darwin.sh /
# build_ffmpeg_android.sh. Windows hw decode: --enable-d3d11va +
# --enable-dxva2 (AV_HWDEVICE_TYPE_D3D11VA / DXVA2 — FFmpeg loads
# d3d11.dll/dxgi.dll/d3d9.dll at runtime via the system, needs
# -ld3d11 -ldxgi -ld3d9 -lole32 -luuid at link). No upstream C
# counterpart (upstream Movian has no Windows port).
#
# Requires: gcc-mingw-w64, cmake, nasm/yasm, and
# scripts/build_windows_deps.sh run first (vendored zlib).
#
# Usage: ./scripts/build_ffmpeg_windows.sh [amd64|386] [--force]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Canonical dependency manifest (name/version/URL/SHA-256).
source "${SCRIPT_DIR}/deps.env"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
FFMPEG_PREFIX="${FFMPEG_PREFIX:-${REPO_ROOT}/ffmpeg}"
FFMPEG_JOBS="${FFMPEG_JOBS:-$(nproc)}"
GOARCH=amd64
FORCE=0
for arg in "$@"; do
    case "$arg" in
        --force)   FORCE=1 ;;
        amd64|386) GOARCH="$arg" ;;
    esac
done
case "$GOARCH" in
    amd64) MINGW=x86_64-w64-mingw32; FARCH=x86_64; FCPU=x86-64 ;;
    386)   MINGW=i686-w64-mingw32;   FARCH=x86;    FCPU=i686 ;;
esac
ZP="${REPO_ROOT}/third_party/zlib/windows/${GOARCH}"
XP="${REPO_ROOT}/third_party/libxml2/windows/${GOARCH}"

log() { printf '[build_ffmpeg_windows:%s] %s\n' "$GOARCH" "$*"; }
die() { printf '[build_ffmpeg_windows:%s] ERROR: %s\n' "$GOARCH" "$*" >&2; exit 1; }

command -v ${MINGW}-gcc >/dev/null || die "${MINGW}-gcc not found"
[[ -f "$ZP/lib/libz.a" ]] || die "vendored zlib missing — run scripts/build_windows_deps.sh first"
[[ -f "$XP/lib/pkgconfig/libxml-2.0.pc" ]] || die "vendored libxml2 missing — run scripts/build_windows_deps.sh first"

# Verified source tarball, shared cache in FFMPEG_PREFIX.
mkdir -p "${FFMPEG_PREFIX}"
DEPS_CACHE="${FFMPEG_PREFIX}"
source "${SCRIPT_DIR}/fetch_dep.sh"
SRC_TAR=$(dep_fetch "${FFMPEG_TARBALL}" "${FFMPEG_URL}" "${FFMPEG_SHA256}") || die "ffmpeg fetch failed"

SRC_DIR="/tmp/ffmpeg-src-windows-${FFMPEG_VERSION}"
if [[ ! -d "$SRC_DIR" ]]; then
    tar -xf "$SRC_TAR" -C /tmp
    mv "/tmp/ffmpeg-${FFMPEG_VERSION}" "$SRC_DIR"
fi
apply_ffmpeg_patches "$SRC_DIR" || die "vendored patches failed"

prefix="${FFMPEG_PREFIX}/windows/${GOARCH}"
bdir="/tmp/ffmpeg-build-windows-${GOARCH}"
if [[ -f "$prefix/lib/libavcodec.a" && "$FORCE" -eq 0 ]]; then
    log "$GOARCH already built — skipping (use --force)"
    exit 0
fi
mkdir -p "$bdir"
log "configuring $GOARCH (mingw-w64, d3d11va+dxva2)"
# PKG_CONFIG_LIBDIR isolates the vendored libxml2 .pc (DASH demuxer
# dep) from host packages.
(cd "$bdir" && PKG_CONFIG_LIBDIR="$XP/lib/pkgconfig" "$SRC_DIR/configure" \
    --prefix="$prefix" \
    --enable-cross-compile \
    --target-os=mingw32 \
    --arch="${FARCH}" \
    --cpu="${FCPU}" \
    --cross-prefix="${MINGW}-" \
    --pkg-config=pkg-config \
    --enable-static \
    --disable-shared \
    --disable-programs \
    --disable-doc \
    --disable-debug \
    --enable-pic \
    --enable-gpl \
    --enable-zlib \
    --disable-bzlib \
    --disable-lzma \
    --disable-iconv \
    --enable-libxml2 \
    --disable-xlib \
    --disable-libxcb \
    --disable-vulkan \
    --disable-vdpau \
    --disable-vaapi \
    --disable-libdrm \
    --disable-cuda \
    --disable-cuvid \
    --disable-nvdec \
    --disable-nvenc \
    --disable-videotoolbox \
    --disable-d3d12va \
    --enable-d3d11va \
    --enable-dxva2 \
    --disable-mediafoundation \
    --disable-sdl2 \
    --disable-alsa \
    --disable-sndio \
    --disable-libx264 \
    --disable-libx265 \
    --disable-libvpx \
    --disable-libfdk-aac \
    --disable-libmp3lame \
    --disable-libopus \
    --disable-libvorbis \
    --disable-libtheora \
    --disable-libass \
    --disable-libfreetype \
    --disable-libfontconfig \
    --disable-libbluray \
    --extra-cflags="-O2 -I$ZP/include -I$XP/include" \
    --extra-ldflags="-L$ZP/lib -L$XP/lib" \
    >"$bdir/configure.log" 2>&1) || {
        tail -30 "$bdir/configure.log"; die "configure failed"; }

log "building $GOARCH (-j$FFMPEG_JOBS)"
make -C "$bdir" -j"$FFMPEG_JOBS" >"$bdir/build.log" 2>&1 || {
    tail -30 "$bdir/build.log"; die "build failed"; }
make -C "$bdir" install >"$bdir/install.log" 2>&1
log "$GOARCH -> $prefix"
log "done"
