#!/usr/bin/env bash
#
# build_libxml2_android.sh - libxml2 static libraries for Android, one
# install prefix per ABI under third_party/libxml2/android/<abi>/.
#
# Vendored because FFmpeg's DASH demuxer (dash_demuxer_deps="libxml2")
# needs it and the NDK does not ship libxml2 — mirrors the retired
# build_freetype_android.sh pattern: NDK clang per ABI, autotools
# --host cross-configure, static-only install.
#
# The MPD parser only reads XML, so the build is minimal: no python,
# zlib, lzma, iconv, icu, http or ftp (network I/O stays in fileaccess).
#
# Usage:
#   ./scripts/build_libxml2_android.sh [API] [--force]
#   API defaults to 26 (Android 8.0), same as build_ffmpeg_android.sh.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
source "${SCRIPT_DIR}/deps.env"
source "${SCRIPT_DIR}/fetch_dep.sh"

OUT="${REPO_ROOT}/third_party/libxml2"
API=26
FORCE=0
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
        *)       API="$arg" ;;
    esac
done

NDK=${ANDROID_NDK:-/opt/android/android-ndk-r23b}
TC="$NDK/toolchains/llvm/prebuilt/linux-x86_64"

log() { printf '[build_libxml2_android] %s\n' "$*"; }
die() { printf '[build_libxml2_android] ERROR: %s\n' "$*" >&2; exit 1; }

[[ -x "$TC/bin/llvm-ar" ]] || die "NDK toolchain not found at $TC"

TAR=$(dep_fetch "${LIBXML2_TARBALL}" "${LIBXML2_URL}" "${LIBXML2_SHA256}") \
    || die "libxml2 fetch failed"
dep_unpack_tarball "$TAR" "/tmp/libxml2-${LIBXML2_VERSION}"
SRC="/tmp/libxml2-${LIBXML2_VERSION}"

build_abi() {
    local abi=$1 triple=$2
    local prefix="$OUT/android/$abi"
    local bdir="/tmp/libxml2-build-android-$abi"
    if [[ $FORCE -eq 0 && -f "$prefix/lib/libxml2.a" ]]; then
        log "$abi already built (use --force)"
        return 0
    fi
    rm -rf "$bdir" && mkdir -p "$bdir" "$prefix"
    log "building $abi ($triple, API $API)"
    (cd "$bdir" && \
     CC="$TC/bin/${triple}${API}-clang" \
     AR="$TC/bin/llvm-ar" \
     RANLIB="$TC/bin/llvm-ranlib" \
     "$SRC/configure" --host="$triple" --prefix="$prefix" \
        --enable-static --disable-shared \
        --with-pic \
        --without-python \
        --without-zlib --without-lzma \
        --without-iconv --without-icu \
        --without-http --without-ftp \
        --without-readline --without-schemas \
        --without-schematron --without-modules \
        >"$bdir/configure.log" 2>&1 && \
     make -j"$(nproc)" >"$bdir/build.log" 2>&1 && \
     make install >"$bdir/install.log" 2>&1) || {
        tail -30 "$bdir/configure.log" "$bdir/build.log" 2>/dev/null
        die "build failed for $abi"; }
    log "$abi -> $prefix"
}

build_abi arm64 aarch64-linux-android
build_abi arm   armv7a-linux-androideabi

log "done"
