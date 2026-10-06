# fetch_dep.sh — shared verified-fetch helper for dependency scripts.
# Source after scripts/deps.env. Provides:
#
#   dep_fetch <outfile> <url> <sha256>
#       Download <url> to <outfile> (skipping if a checksum-valid file
#       already exists), verify SHA-256, die clearly on mismatch.
#       Cache directory: DEPS_CACHE (default: repo .deps/ — gitignored).
#
#   dep_unpack_tarball <archive> <dest-dir>
#   dep_unpack_zip     <archive> <dest-dir>
#       Extract into <dest-dir> if not already present.

if [[ -z "${DEPS_ENV_LOADED:-}" ]]; then
    DEPS_ENV_LOADED=1
fi

DEPS_CACHE="${DEPS_CACHE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.deps}"

dep_sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

dep_fetch() {
    local out=$1 url=$2 sum=$3
    mkdir -p "$DEPS_CACHE"
    out="$DEPS_CACHE/$out"
    if [[ -f "$out" ]] && [[ "$(dep_sha256_of "$out")" != "$sum" ]]; then
        printf '[deps] checksum mismatch on cached %s — re-downloading\n' "$out" >&2
        rm -f "$out"
    fi
    if [[ ! -f "$out" ]]; then
        printf '[deps] downloading %s\n' "$url" >&2
        if command -v curl >/dev/null 2>&1; then
            curl -fL --retry 3 -o "$out" "$url" || return 1
        elif command -v wget >/dev/null 2>&1; then
            wget -O "$out" "$url" || return 1
        else
            printf '[deps] ERROR: curl or wget is required\n' >&2
            return 1
        fi
    fi
    local actual
    actual="$(dep_sha256_of "$out")"
    if [[ "$actual" != "$sum" ]]; then
        printf '[deps] ERROR: sha256 mismatch for %s (got %s, want %s)\n' \
            "$out" "$actual" "$sum" >&2
        return 1
    fi
    printf '%s\n' "$out"
}

dep_unpack_tarball() { # dep_unpack_tarball <archive-path> <dest-dir>
    [[ -d "$2" ]] || tar -xf "$1" -C "$(dirname "$2")"
}

dep_unpack_zip() { # dep_unpack_zip <archive-path> <dest-dir>
    [[ -d "$2" ]] || (cd "$(dirname "$2")" && unzip -q -o "$1")
}

# apply_ffmpeg_patches <src-dir> — apply vendored patches from
# ffmpeg/patches/ idempotently (safe on already-patched trees and on
# trees extracted before a patch existed).
apply_ffmpeg_patches() {
    local srcdir="$1" pdir="${SCRIPT_DIR}/../ffmpeg/patches" p
    [[ -d "$pdir" ]] || return 0
    for p in "$pdir"/*.patch; do
        [[ -f "$p" ]] || continue
        if patch -d "$srcdir" -p1 --forward --dry-run < "$p" >/dev/null 2>&1; then
            patch -d "$srcdir" -p1 --forward < "$p" >/dev/null \
                || { echo "[deps] patch failed: $p" >&2; return 1; }
            echo "[deps] applied $(basename "$p")"
        fi
    done
}
