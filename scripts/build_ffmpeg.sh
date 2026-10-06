#!/usr/bin/env bash
#
# build_ffmpeg.sh - Reproducible build of FFmpeg 9.0.1 static libraries
# for the movian-go project.
#
# Produces static libraries + headers in ${FFMPEG_PREFIX} (default:
# /tmp/movian-go/ffmpeg) matching the cgo flags declared in
# internal/media/libav/format.go:
#
#   -lavformat -lavcodec -lavutil -lswresample -lswscale
#   -lm -lpthread -lz -lbz2 -llzma
#
# The build is static, has no external codec deps (no x264/x265/vpx/...),
# disables programs/docs, and disables GUI/hwaccel backends (X11, drm,
# vulkan, SDL2, vdpau, etc.) except VAAPI/NVDEC, so the resulting
# archives only depend on the system libraries listed above.
#
# Usage:
#   ./scripts/build_ffmpeg.sh            # build (skip if already built)
#   ./scripts/build_ffmpeg.sh --force    # rebuild even if libs exist
#   ./scripts/build_ffmpeg.sh --clean    # remove build output and exit
#
# Environment overrides:
#   FFMPEG_PREFIX  install prefix (default: repo/ffmpeg)
#   FFMPEG_JOBS    parallel build jobs  (default: nproc)
#   FFMPEG_SRC_TAR local tarball path   (default: ${FFMPEG_PREFIX}/ffmpeg-9.0.1.tar.xz)
#
set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------
# Resolve repo root from script location so it works from any CWD.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Canonical dependency manifest (name/version/URL/SHA-256).
source "${SCRIPT_DIR}/deps.env"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

FFMPEG_PREFIX="${FFMPEG_PREFIX:-${REPO_ROOT}/ffmpeg}"
FFMPEG_JOBS="${FFMPEG_JOBS:-$(nproc 2>/dev/null || echo 4)}"
FFMPEG_SRC_TAR="${FFMPEG_SRC_TAR:-${FFMPEG_PREFIX}/${FFMPEG_TARBALL}}"

# The set of static archives that must exist for the build to be considered
# complete. Matches the libraries referenced by the cgo LDFLAGS plus the
# avdevice/avfilter archives that FFmpeg installs by default.
REQUIRED_LIBS=(
    "libavcodec.a"
    "libavdevice.a"
    "libavfilter.a"
    "libavformat.a"
    "libavutil.a"
    "libswresample.a"
    "libswscale.a"
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { printf '[build_ffmpeg] %s\n' "$*"; }
err()  { printf '[build_ffmpeg] ERROR: %s\n' "$*" >&2; }
die()  { err "$*"; exit 1; }

have_all_libs() {
    [[ -d "${FFMPEG_PREFIX}/include" ]] || return 1
    [[ -d "${FFMPEG_PREFIX}/lib" ]]     || return 1
    local lib
    for lib in "${REQUIRED_LIBS[@]}"; do
        [[ -f "${FFMPEG_PREFIX}/lib/${lib}" ]] || return 1
    done
    # pkgconfig is what downstream tooling may consult.
    [[ -d "${FFMPEG_PREFIX}/lib/pkgconfig" ]] || return 1
    return 0
}

# ---------------------------------------------------------------------------
# Argument handling
# ---------------------------------------------------------------------------
FORCE=0
CLEAN=0
for arg in "$@"; do
    case "$arg" in
        --force)  FORCE=1 ;;
        --clean)  CLEAN=1 ;;
        -h|--help)
            sed -n '2,28p' "$0"
            exit 0
            ;;
        *) die "unknown argument: $arg (use --force or --clean)" ;;
    esac
done

if [[ "${CLEAN}" -eq 1 ]]; then
    log "cleaning ${FFMPEG_PREFIX} (preserving any local tarball)"
    rm -rf "${FFMPEG_PREFIX}/include" "${FFMPEG_PREFIX}/lib"
    rm -rf "${FFMPEG_PREFIX}/ffmpeg-${FFMPEG_VERSION}"
    log "clean done"
    exit 0
fi

# ---------------------------------------------------------------------------
# Idempotency check
# ---------------------------------------------------------------------------
if [[ "${FORCE}" -eq 0 ]] && have_all_libs; then
    log "FFmpeg ${FFMPEG_VERSION} static libraries already present in ${FFMPEG_PREFIX}"
    log "nothing to do (use --force to rebuild)"
    exit 0
fi

# ---------------------------------------------------------------------------
# Preflight: build tools & dev headers
# ---------------------------------------------------------------------------
command -v make    >/dev/null 2>&1 || die "make is required"
command -v nasm    >/dev/null 2>&1 || command -v yasm >/dev/null 2>&1 || \
    die "nasm or yasm is required for x86 asm optimisation"

for hdr in zlib.h bzlib.h lzma.h; do
    if ! echo "#include <${hdr}>" | gcc -E - >/dev/null 2>&1; then
        die "missing development header <${hdr}>; install zlib1g-dev libbz2-dev liblzma-dev"
    fi
done

# VAAPI: libva headers/pkg-config come either from the system
# (libva-dev) or from the rootless sysroot under third_party/libva
# (dpkg -x of the libva-dev .deb, .pc files re-prefixed).
LIBVA_SYSROOT="${LIBVA_SYSROOT:-${REPO_ROOT}/third_party/libva/usr}"
if pkg-config --exists "libva >= 0.35.0" libva-drm 2>/dev/null; then
    : # system libva-dev
elif [[ -d "${LIBVA_SYSROOT}/lib/x86_64-linux-gnu/pkgconfig" ]]; then
    export PKG_CONFIG_PATH="${LIBVA_SYSROOT}/lib/x86_64-linux-gnu/pkgconfig${PKG_CONFIG_PATH:+:${PKG_CONFIG_PATH}}"
    pkg-config --exists "libva >= 0.35.0" libva-drm || \
        die "libva sysroot present but pkg-config cannot resolve libva/libva-drm"
    log "using libva sysroot at ${LIBVA_SYSROOT}"
else
    die "missing libva dev files; install libva-dev or populate ${LIBVA_SYSROOT}"
fi

# NVDEC: ffnvcodec headers (headers-only, vendored under
# third_party/nv-codec-headers). FFmpeg loads libcuda/libnvcuvid via
# dlopen at runtime, so no CUDA toolkit or driver libs are needed at
# build time — only the headers.
FFNV_DIR="${FFNV_DIR:-${REPO_ROOT}/third_party/nv-codec-headers}"
if pkg-config --exists ffnvcodec 2>/dev/null; then
    : # system ffnvcodec
else
    # The vendored .pc is generated and gitignored; create it on clean
    # checkouts (same rule as the bundled nv-codec-headers Makefile).
    if [[ ! -f "${FFNV_DIR}/ffnvcodec.pc" ]]; then
        sed "s#@@PREFIX@@#${FFNV_DIR}#" "${FFNV_DIR}/ffnvcodec.pc.in" \
            > "${FFNV_DIR}/ffnvcodec.pc"
    fi
    export PKG_CONFIG_PATH="${FFNV_DIR}${PKG_CONFIG_PATH:+:${PKG_CONFIG_PATH}}"
    pkg-config --exists ffnvcodec || \
        die "missing ffnvcodec headers; populate ${FFNV_DIR}"
    log "using vendored ffnvcodec at ${FFNV_DIR}"
fi

# ---------------------------------------------------------------------------
# Download / verify tarball (shared verified cache in FFMPEG_PREFIX)
# ---------------------------------------------------------------------------
mkdir -p "${FFMPEG_PREFIX}"
DEPS_CACHE="${FFMPEG_PREFIX}"
source "${SCRIPT_DIR}/fetch_dep.sh"
if [[ "${FFMPEG_SRC_TAR}" != "${FFMPEG_PREFIX}/${FFMPEG_TARBALL}" ]]; then
    # User-provided tarball (e.g. vendored/offline): verify only, never download.
    [[ -f "${FFMPEG_SRC_TAR}" ]] || die "FFMPEG_SRC_TAR not found: ${FFMPEG_SRC_TAR}"
    actual="$(dep_sha256_of "${FFMPEG_SRC_TAR}")"
    [[ "${actual}" == "${FFMPEG_SHA256}" ]] || die "sha256 mismatch for ${FFMPEG_SRC_TAR}
  expected: ${FFMPEG_SHA256}
  got:      ${actual}"
else
    FFMPEG_SRC_TAR="$(dep_fetch "${FFMPEG_TARBALL}" "${FFMPEG_URL}" "${FFMPEG_SHA256}")" \
        || die "ffmpeg tarball fetch/verify failed"
fi
log "checksum OK"

# ---------------------------------------------------------------------------
# Extract
# ---------------------------------------------------------------------------
BUILD_DIR="${FFMPEG_PREFIX}/ffmpeg-${FFMPEG_VERSION}"
if [[ ! -d "${BUILD_DIR}" ]]; then
    log "extracting tarball"
    tar -xf "${FFMPEG_SRC_TAR}" -C "${FFMPEG_PREFIX}"
fi
[[ -f "${BUILD_DIR}/configure" ]] || die "extraction failed: no configure in ${BUILD_DIR}"
apply_ffmpeg_patches "${BUILD_DIR}" || die "vendored patches failed"

# ---------------------------------------------------------------------------
# Configure
#
# Flags chosen to match the cgo link line in internal/media/libav/format.go:
#   -lavformat -lavcodec -lavutil -lswresample -lswscale -lm -lpthread -lz -lbz2 -llzma
#
# We keep the default codec/demuxer/muxer/protocol set (no --disable-everything)
# so the libraries are feature-complete for a media player, while disabling
# every external GUI/hwaccel dependency so the archives only pull in the
# system libs above.
# ---------------------------------------------------------------------------
log "configuring (prefix=${FFMPEG_PREFIX}, jobs=${FFMPEG_JOBS})"

cd "${BUILD_DIR}"

# A clean configure state makes the build reproducible across re-runs.
rm -f ffbuild/config.log ffbuild/config.lsr config.h config.mak

./configure \
    --prefix="${FFMPEG_PREFIX}" \
    --pkg-config-flags="--static" \
    \
    --enable-static \
    --disable-shared \
    \
    --disable-programs \
    --disable-doc \
    --disable-debug \
    \
    --enable-gpl \
    \
    --enable-zlib \
    --enable-bzlib \
    --enable-lzma \
    \
    --enable-libxml2 \
    \
    --disable-xlib \
    --disable-libxcb \
    --disable-libxcb-shm \
    --disable-libxcb-xfixes \
    --disable-libxcb-shape \
    --disable-vulkan \
    --disable-vdpau \
    --enable-vaapi \
    --disable-d3d11va \
    --disable-dxva2 \
    --enable-libdrm \
    --disable-sdl2 \
    --disable-alsa \
    --disable-cuda-llvm \
    --enable-cuvid \
    --enable-nvdec \
    --disable-nvenc \
    --disable-mediafoundation \
    \
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
    \
    --enable-pic

# ---------------------------------------------------------------------------
# Build & install
# ---------------------------------------------------------------------------
log "building (make -j${FFMPEG_JOBS})"
make -j"${FFMPEG_JOBS}"

log "installing to ${FFMPEG_PREFIX}"
make install

# The VAAPI/libdrm objects now in the archives need these on the link
# line of every consumer. Provide .so symlinks in the vendored lib dir
# so the existing -L${SRCDIR}/ffmpeg/lib + -lva/-lva-drm/-ldrm flags
# resolve against the system runtime libraries (no -dev packages
# required at link time).
for spec in libva:libva.so.2 libva-drm:libva-drm.so.2 libdrm:libdrm.so.2; do
    name="${spec%%:*}"; soname="${spec##*:}"
    for d in /usr/lib/x86_64-linux-gnu /usr/lib /usr/local/lib \
             "${LIBVA_SYSROOT:-${REPO_ROOT}/third_party/libva/usr}/lib/x86_64-linux-gnu"; do
        if [[ -e "${d}/${soname}" ]]; then
            ln -sf "${d}/${soname}" "${FFMPEG_PREFIX}/lib/${name}.so"
            break
        fi
    done
done

# ---------------------------------------------------------------------------
# Verify
# ---------------------------------------------------------------------------
if ! have_all_libs; then
    die "build finished but expected libraries are missing in ${FFMPEG_PREFIX}/lib"
fi

log "success: FFmpeg ${FFMPEG_VERSION} static libraries installed in ${FFMPEG_PREFIX}"
log "  libs:  ${REQUIRED_LIBS[*]}"
log "  include: ${FFMPEG_PREFIX}/include"
