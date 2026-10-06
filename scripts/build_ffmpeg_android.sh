#!/usr/bin/env bash
#
# build_ffmpeg_android.sh - FFmpeg 9.0.1 static libraries for Android,
# one install prefix per ABI under ffmpeg/android/<abi>/.
#
# Mirrors the configure choices of scripts/build_ffmpeg.sh but for the
# NDK toolchain: no X11/vdpau/vaapi/cuda/drm/vulkan (desktop-only),
# MediaCodec + JNI enabled (canonical android_video_codec.c uses JNI
# directly; FFmpeg's mediacodec support is enabled so libavcodec can
# also expose it), bzlib/lzma off (not in the NDK). zlib is in the NDK.
#
# Usage:
#   ./scripts/build_ffmpeg_android.sh [API] [--force] [--shared]
#   API defaults to 26 (Android 8.0).
#
# --shared additionally builds shared libav*.so into
# ffmpeg/android/<abi>-shared/ — for bundling inside the APK
# (android/vendorlibs/<abi>/) on firmware whose system libs have a
# DT_NEEDED on vendor libavutil.so that the app namespace cannot reach
# (upstream shipped shared libav* in the APK for the same reason).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Canonical dependency manifest (name/version/URL/SHA-256).
source "${SCRIPT_DIR}/deps.env"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
FFMPEG_PREFIX="${FFMPEG_PREFIX:-${REPO_ROOT}/ffmpeg}"
FFMPEG_JOBS="${FFMPEG_JOBS:-$(nproc 2>/dev/null || echo 4)}"
API=26
FORCE=0
SHARED=0
for arg in "$@"; do
    case "$arg" in
        --force)  FORCE=1 ;;
        --shared) SHARED=1 ;;
        *)        API="$arg" ;;
    esac
done

NDK=${ANDROID_NDK:-/opt/android/android-ndk-r23b}
TC="$NDK/toolchains/llvm/prebuilt/linux-x86_64"

log() { printf '[build_ffmpeg_android] %s\n' "$*"; }
die() { printf '[build_ffmpeg_android] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -x "$TC/bin/llvm-ar" ]] || die "NDK toolchain not found at $TC"

# ---- source tarball (verified, shared cache in FFMPEG_PREFIX) --------------
mkdir -p "${FFMPEG_PREFIX}"
DEPS_CACHE="${FFMPEG_PREFIX}"
source "${SCRIPT_DIR}/fetch_dep.sh"
SRC_TAR=$(dep_fetch "${FFMPEG_TARBALL}" "${FFMPEG_URL}" "${FFMPEG_SHA256}") || die "ffmpeg fetch failed"

SRC_DIR="/tmp/ffmpeg-src-android-${FFMPEG_VERSION}"
if [[ ! -d "$SRC_DIR" ]]; then
    tar -xf "$SRC_TAR" -C /tmp
    mv "/tmp/ffmpeg-${FFMPEG_VERSION}" "$SRC_DIR"
fi
apply_ffmpeg_patches "$SRC_DIR" || die "vendored patches failed"

build_abi() {
    local abi=$1 triple=$2 farch=$3 cpu=$4 mode=${5:-static}
    local prefix="${FFMPEG_PREFIX}/android/${abi}"
    local bdir="/tmp/ffmpeg-build-android-${abi}"
    local libopts libdone
    if [[ "$mode" == shared ]]; then
        prefix="${FFMPEG_PREFIX}/android/${abi}-shared"
        bdir="/tmp/ffmpeg-build-android-${abi}-shared"
        libopts="--disable-static --enable-shared"
        libdone="$prefix/lib/libavutil.so"
    else
        libopts="--enable-static --disable-shared"
        libdone="$prefix/lib/libavcodec.a"
    fi

    if [[ $FORCE -eq 0 && -f "$libdone" ]]; then
        log "$abi $mode already built (use --force)"
        return 0
    fi
    rm -rf "$bdir" && mkdir -p "$bdir" "$prefix"

    # libxml2 is vendored per-ABI (scripts/build_libxml2_android.sh) —
    # FFmpeg's dash demuxer requires it (dash_demuxer_deps="libxml2").
    # PKG_CONFIG_LIBDIR isolates the vendored .pc from host packages.
    local xml2root="${REPO_ROOT}/third_party/libxml2/android/${abi}"
    local xml2pc="${xml2root}/lib/pkgconfig"
    [[ -f "$xml2pc/libxml-2.0.pc" ]] || \
        die "vendored libxml2 missing for $abi — run scripts/build_libxml2_android.sh first"

    log "configuring $abi $mode ($triple, API $API)"
    (cd "$bdir" && PKG_CONFIG_LIBDIR="$xml2pc" "$SRC_DIR/configure" \
        --prefix="$prefix" \
        --enable-cross-compile \
        --target-os=android \
        --arch="$farch" \
        --cpu="$cpu" \
        --cc="$TC/bin/${triple}${API}-clang" \
        --cxx="$TC/bin/${triple}${API}-clang++" \
        --nm="$TC/bin/llvm-nm" \
        --ar="$TC/bin/llvm-ar" \
        --ranlib="$TC/bin/llvm-ranlib" \
        --strip="$TC/bin/llvm-strip" \
        --sysroot="$TC/sysroot" \
        \
        $libopts \
        \
        --disable-programs \
        --disable-doc \
        --disable-debug \
        \
        --enable-gpl \
        \
        --enable-zlib \
        --disable-bzlib \
        --disable-lzma \
        --disable-iconv \
        --enable-libxml2 \
        \
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
        --disable-d3d11va \
        --disable-dxva2 \
        --disable-sdl2 \
        --disable-alsa \
        --disable-sndio \
        --disable-libxcb-shm \
        --disable-libxcb-xfixes \
        --disable-libxcb-shape \
        \
        --enable-jni \
        --enable-mediacodec \
        \
        --extra-cflags="-O2 -fPIC -I${xml2root}/include" \
        --extra-ldflags="-L${xml2root}/lib" \
        >"$bdir/configure.log" 2>&1) || {
            tail -30 "$bdir/configure.log"; die "configure failed for $abi"; }

    log "building $abi $mode (-j$FFMPEG_JOBS)"
    make -C "$bdir" -j"$FFMPEG_JOBS" >"$bdir/build.log" 2>&1 || {
        tail -30 "$bdir/build.log"; die "build failed for $abi"; }
    make -C "$bdir" install >"$bdir/install.log" 2>&1
    log "$abi $mode -> $prefix"
}

build_abi arm64 aarch64-linux-android      aarch64 armv8-a
build_abi arm   armv7a-linux-androideabi   arm     armv7-a

if [[ $SHARED -eq 1 ]]; then
    build_abi arm64 aarch64-linux-android      aarch64 armv8-a shared
    build_abi arm   armv7a-linux-androideabi   arm     armv7-a shared
    log "shared libs under ${FFMPEG_PREFIX}/android/<abi>-shared/lib/"
    log "bundle needed ones via android/vendorlibs/<abi>/ (build_apk.sh picks them up)"
fi

log "done"
