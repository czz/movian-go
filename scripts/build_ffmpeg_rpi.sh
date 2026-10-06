#!/usr/bin/env bash
#
# build_ffmpeg_rpi.sh - Cross-build FFmpeg 9.0.1 for Raspberry Pi
# (ARMv6 hard-float, GOARM=6) plus the VideoCore sysroot needed by
# the rpi cgo backends (bcm_host / dispmanx / OpenMAX IL / EGL / GLESv2).
#
# Steps:
#   1. Download armhf .debs (libraspberrypi-dev/0, zlib1g-dev, libbz2-dev,
#      liblzma-dev, libxml2-dev) and extract them into
#      third_party/rpi/sysroot — the linker resolves the firmware libs
#      (libbrcmEGL/libbrcmGLESv2 are aliased as libEGL/libGLESv2, matching
#      the Raspbian convention) while zlib/bz2/lzma/xml2 are linked
#      statically from the .a archives.
#   2. Extract the pinned FFmpeg tarball (deps.env SHA-256 applies),
#      apply the vendored patches, configure for armv6zk+vfp hard-float
#      with the arm-linux-gnueabihf cross toolchain, build and install
#      into ffmpeg/rpi/{include,lib}.
#
# Usage:
#   ./scripts/build_ffmpeg_rpi.sh           # sysroot + build (idempotent)
#   ./scripts/build_ffmpeg_rpi.sh --force   # rebuild even if libs exist
#   ./scripts/build_ffmpeg_rpi.sh --clean   # remove build output and exit
#
# Requires: arm-linux-gnueabihf-gcc (Debian gcc-arm-linux-gnueabihf),
#           dpkg-deb, curl, pkg-config.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/deps.env"
source "${SCRIPT_DIR}/fetch_dep.sh"   # apply_ffmpeg_patches
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SYSROOT="${RPI_SYSROOT:-${REPO_ROOT}/third_party/rpi/sysroot}"
RPI_PREFIX="${REPO_ROOT}/ffmpeg/rpi"
CROSS="${RPI_CROSS_PREFIX:-arm-linux-gnueabihf-}"
JOBS="${FFMPEG_JOBS:-$(nproc 2>/dev/null || echo 4)}"
SRC_PARENT="${RPI_SRC_PARENT:-${REPO_ROOT}/build}"
SRC_DIR="${SRC_PARENT}/ffmpeg-${FFMPEG_VERSION}-rpi"

log()  { printf '[ffmpeg-rpi] %s\n' "$*"; }
die()  { printf '[ffmpeg-rpi] ERROR: %s\n' "$*" >&2; exit 1; }

FORCE=0
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
        --clean) rm -rf "${RPI_PREFIX}" "${SYSROOT}" "${SRC_DIR}"; log "cleaned"; exit 0 ;;
        *) die "unknown arg: $arg" ;;
    esac
done

command -v "${CROSS}gcc" >/dev/null || die "${CROSS}gcc not found (apt install gcc-arm-linux-gnueabihf)"
command -v dpkg-deb   >/dev/null || die "dpkg-deb not found"

# ---------------------------------------------------------------------------
# 1. Sysroot — Raspbian armhf .debs extracted into third_party/rpi/sysroot
# ---------------------------------------------------------------------------
RPI_FW_BASE="http://archive.raspberrypi.org/debian/pool/main/r/raspberrypi-firmware"
RASPBIAN="http://raspbian.raspberrypi.org/raspbian/pool/main"

DEBS=(
    "${RPI_FW_BASE}/libraspberrypi-dev_1.20230509~buster-1_armhf.deb"
    "${RPI_FW_BASE}/libraspberrypi0_1.20230509~buster-1_armhf.deb"
    "${RASPBIAN}/z/zlib/zlib1g-dev_1.3.dfsg+really1.3.2-3_armhf.deb"
    "${RASPBIAN}/z/zlib/zlib1g_1.3.dfsg+really1.3.2-3_armhf.deb"
    "${RASPBIAN}/b/bzip2/libbz2-dev_1.0.8-6_armhf.deb"
    "${RASPBIAN}/b/bzip2/libbz2-1.0_1.0.8-6_armhf.deb"
    "${RASPBIAN}/x/xz-utils/liblzma-dev_5.8.4-1_armhf.deb"
    "${RASPBIAN}/x/xz-utils/liblzma5_5.8.4-1_armhf.deb"
    "${RASPBIAN}/libx/libxml2/libxml2-dev_2.15.4+dfsg-1_armhf.deb"
    "${RASPBIAN}/libx/libxml2/libxml2-16_2.15.4+dfsg-1_armhf.deb"
)

if [[ ! -f "${SYSROOT}/opt/vc/include/bcm_host.h" || ${FORCE} -eq 1 ]]; then
    log "fetching Raspbian armhf sysroot debs"
    mkdir -p "${SYSROOT}" "${SRC_PARENT}/rpi-debs"
    for url in "${DEBS[@]}"; do
        deb="${SRC_PARENT}/rpi-debs/$(basename "${url}")"
        [[ -f "${deb}" ]] || curl -sfLo "${deb}" "${url}" \
            || die "download failed: ${url}"
        dpkg-deb -x "${deb}" "${SYSROOT}"
    done
    # Raspbian exposes the Broadcom GL libs under both names.
    ln -sf libbrcmEGL.so    "${SYSROOT}/opt/vc/lib/libEGL.so"
    ln -sf libbrcmGLESv2.so "${SYSROOT}/opt/vc/lib/libGLESv2.so"
    log "sysroot ready: ${SYSROOT}"
else
    log "sysroot already present: ${SYSROOT}"
fi

# ---------------------------------------------------------------------------
# 2. FFmpeg for armv6zk + VFP hard-float
# ---------------------------------------------------------------------------
if [[ -f "${RPI_PREFIX}/lib/libavcodec.a" && ${FORCE} -eq 0 ]]; then
    log "ffmpeg rpi libs already built — skipping (use --force)"
    exit 0
fi

[[ -f "${FFMPEG_PREFIX:-${REPO_ROOT}/ffmpeg}/${FFMPEG_TARBALL}" ]] \
    || die "missing ${FFMPEG_TARBALL} in ffmpeg/ (run ./scripts/build_ffmpeg.sh first — it verifies the SHA-256)"

mkdir -p "${SRC_PARENT}"
if [[ ! -d "${SRC_DIR}" ]]; then
    log "extracting ${FFMPEG_TARBALL}"
    tar -xf "${REPO_ROOT}/ffmpeg/${FFMPEG_TARBALL}" -C "${SRC_PARENT}"
    mv "${SRC_PARENT}/ffmpeg-${FFMPEG_VERSION}" "${SRC_DIR}"
    apply_ffmpeg_patches "${SRC_DIR}" || die "vendored patches failed"
fi

cd "${SRC_DIR}"
rm -f ffbuild/config.log ffbuild/config.lsr config.h config.mak

log "configuring (armv6zk hard-float, cross=${CROSS})"
PKG_CONFIG_LIBDIR="${SYSROOT}/usr/lib/arm-linux-gnueabihf/pkgconfig:${SYSROOT}/usr/share/pkgconfig" \
PKG_CONFIG_SYSROOT_DIR="${SYSROOT}" \
./configure \
    --cross-prefix="${CROSS}" \
    --pkg-config=pkg-config \
    --pkg-config-flags=--static \
    --arch=arm \
    --cpu=arm1176jzf-s \
    --target-os=linux \
    --enable-cross-compile \
    --enable-static \
    --disable-shared \
    --disable-programs \
    --disable-doc \
    --disable-debug \
    --enable-gpl \
    --enable-zlib \
    --enable-bzlib \
    --enable-lzma \
    --enable-libxml2 \
    --disable-xlib \
    --disable-libxcb \
    --disable-libxcb-shm \
    --disable-libxcb-xfixes \
    --disable-libxcb-shape \
    --disable-vulkan \
    --disable-vdpau \
    --disable-vaapi \
    --disable-d3d11va \
    --disable-dxva2 \
    --disable-libdrm \
    --disable-sdl2 \
    --disable-alsa \
    --disable-cuda-llvm \
    --disable-cuvid \
    --disable-nvdec \
    --disable-nvenc \
    --disable-mediafoundation \
    --disable-iconv \
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
    --disable-libmodplug \
    --disable-libopenmpt \
    --disable-librist \
    --disable-libsrt \
    --disable-libssh \
    --disable-libwebp \
    --disable-libzmq \
    --disable-libzvbi \
    --enable-pic \
    --extra-cflags="-marm -mfloat-abi=hard -mfpu=vfp -I${SYSROOT}/usr/include" \
    --extra-ldflags="-L${SYSROOT}/usr/lib/arm-linux-gnueabihf" \
    --extra-libs="-lm -lz -llzma -lbz2" \
    --prefix="${RPI_PREFIX}" \
    || die "configure failed (see ffbuild/config.log)"

log "building (make -j${JOBS})"
make -j"${JOBS}"
make install

log "done — headers+libs in ${RPI_PREFIX}"
