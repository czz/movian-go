#!/usr/bin/env bash
#
# build_ffmpeg_darwin.sh - FFmpeg 9.0.1 static libraries for macOS,
# one install prefix per arch under ffmpeg/darwin/<arch>/ (arm64, amd64).
#
# Mirrors the configure choices of scripts/build_ffmpeg_android.sh:
# no X11/vdpau/vaapi/cuda/drm/vulkan/alsa (desktop-linux-only);
# VideoToolbox enabled (canonical src/video/vtb.c uses it — pending Go
# port); zlib/bzlib/iconv are in the macOS SDK. lzma is disabled:
# modern Xcode SDKs removed lzma.h, and a brew xz would be single-arch
# while this script cross-builds both arm64 and amd64 slices.
#
# Run ON macOS — clang's -arch flag cross-builds both slices natively.
# Requires: Xcode CLT (xcode-select --install).
#
# Usage:
#   ./scripts/build_ffmpeg_darwin.sh [--force]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Canonical dependency manifest (name/version/URL/SHA-256).
source "${SCRIPT_DIR}/deps.env"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
FFMPEG_PREFIX="${FFMPEG_PREFIX:-${REPO_ROOT}/ffmpeg}"
FFMPEG_JOBS="${FFMPEG_JOBS:-$(sysctl -n hw.ncpu 2>/dev/null || echo 4)}"
FORCE=0
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
    esac
done

log() { printf '[build_ffmpeg_darwin] %s\n' "$*"; }
die() { printf '[build_ffmpeg_darwin] ERROR: %s\n' "$*" >&2; exit 1; }

[[ "$(uname -s)" == "Darwin" ]] || die "must run on macOS"
command -v clang >/dev/null || die "clang not found (xcode-select --install)"

# ---- source tarball (verified, shared cache in FFMPEG_PREFIX) --------------
mkdir -p "${FFMPEG_PREFIX}"
DEPS_CACHE="${FFMPEG_PREFIX}"
source "${SCRIPT_DIR}/fetch_dep.sh"
SRC_TAR=$(dep_fetch "${FFMPEG_TARBALL}" "${FFMPEG_URL}" "${FFMPEG_SHA256}") || die "ffmpeg fetch failed"

SRC_DIR="/tmp/ffmpeg-src-darwin-${FFMPEG_VERSION}"
if [[ ! -d "$SRC_DIR" ]]; then
    tar -xf "$SRC_TAR" -C /tmp
    mv "/tmp/ffmpeg-${FFMPEG_VERSION}" "$SRC_DIR"
fi
apply_ffmpeg_patches "$SRC_DIR" || die "vendored patches failed"

build_arch() {
    local garch=$1 farch=$2   # garch: dir name under ffmpeg/darwin/;
                              # farch: ffmpeg --arch + clang -arch
    local clang_arch=$3
    local prefix="${FFMPEG_PREFIX}/darwin/${garch}"
    local bdir="/tmp/ffmpeg-build-darwin-${garch}"
    if [[ -f "$prefix/lib/libavcodec.a" && "$FORCE" -eq 0 ]]; then
        log "$garch already built — skipping (use --force)"
        return
    fi
    mkdir -p "$bdir"
    log "configuring $garch"
    (cd "$bdir" && "$SRC_DIR/configure" \
        --prefix="$prefix" \
        --enable-cross-compile \
        --target-os=darwin \
        --arch="$farch" \
        --cc="clang -arch $clang_arch" \
        --enable-static \
        --disable-shared \
        --disable-programs \
        --disable-doc \
        --disable-debug \
        --enable-pic \
        --enable-gpl \
        --enable-zlib \
        --enable-bzlib \
        --disable-lzma \
        --enable-iconv \
        --disable-xlib \
        --disable-libxcb \
        --disable-libxcb-shm \
        --disable-libxcb-xfixes \
        --disable-libxcb-shape \
        --disable-vulkan \
        --disable-vdpau \
        --disable-vaapi \
        --disable-libdrm \
        --disable-cuda \
        --disable-cuvid \
        --disable-nvdec \
        --disable-nvenc \
        --disable-d3d11va \
        --disable-dxva2 \
        --disable-mediafoundation \
        --disable-sdl2 \
        --disable-alsa \
        --disable-sndio \
        --enable-videotoolbox \
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
        --extra-cflags="-O2 -arch $clang_arch" \
        --extra-ldflags="-arch $clang_arch" \
        >"$bdir/configure.log" 2>&1) || {
            tail -30 "$bdir/configure.log"; die "configure failed for $garch"; }

    log "building $garch (-j$FFMPEG_JOBS)"
    make -C "$bdir" -j"$FFMPEG_JOBS" >"$bdir/build.log" 2>&1 || {
        tail -30 "$bdir/build.log"; die "build failed for $garch"; }
    make -C "$bdir" install >"$bdir/install.log" 2>&1
    log "$garch -> $prefix"
}

build_arch arm64 aarch64 arm64
build_arch amd64 x86_64 x86_64

log "done"
