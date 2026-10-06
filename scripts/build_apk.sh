#!/usr/bin/env bash
#
# build_apk.sh — assemble movian-go.apk from libcore_<abi>.so + the
# android/ Java shell (restored upstream sources).
#
# Implements src/arch/android/android.mk's pipeline with modern tools
# (aapt2 + d8 instead of aapt + dx):
#   AndroidManifest.xml.in  → sed → AndroidManifest.xml
#   aapt2 compile/link      → resources + R.java
#   javac + d8              → classes.dex
#   aapt2 link              → unsigned APK (res + dex + lib/<abi>/libcore.so)
#   zipalign + apksigner    → movian-go.apk (android/movian.keystore)
#
# Unlike upstream, libav* is STATIC inside libcore.so (vendored
# FFmpeg), so the APK only needs libcore.so per ABI.
#
# Usage:
#   ./scripts/build_apk.sh              # unsigned if no keystore pass
#   MOVIAN_KEYSTORE_PASS=xxx ./scripts/build_apk.sh
#   ./scripts/build_apk.sh --sdk 28 --target 28
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

SDK="${ANDROID_HOME:-/opt/android}"
MIN_SDK=26
TARGET_SDK=28
BUILD_TOOLS_VER=""
OUT="${REPO_ROOT}/build/apk"
APPNAME="Movian"
PKG="com.moviango.mediaplayer"

for arg in "$@"; do
    case "$arg" in
        --sdk=*)    TARGET_SDK="${arg#*=}" ;;
        --min=*)    MIN_SDK="${arg#*=}" ;;
        --bt=*)     BUILD_TOOLS_VER="${arg#*=}" ;;
        --out=*)    OUT="${arg#*=}" ;;
        *) echo "unknown arg: $arg" >&2; exit 1 ;;
    esac
done

log() { printf '[build_apk] %s\n' "$*"; }
die() { printf '[build_apk] ERROR: %s\n' "$*" >&2; exit 1; }

# --- toolchain resolution ---------------------------------------------------
# compileSdk (which android.jar we compile against) is independent from
# targetSdkVersion in the manifest: prefer TARGET_SDK's jar, else fall
# back to the newest installed platform so the build works on SDK
# installs that do not ship android-28 (e.g. CI runners).
ANDROID_JAR="${SDK}/platforms/android-${TARGET_SDK}/android.jar"
if [[ ! -f "$ANDROID_JAR" ]]; then
    COMPILE_SDK="$(ls -d "${SDK}"/platforms/android-* 2>/dev/null \
        | sed 's/.*android-//' | sort -n | tail -1)"
    [[ -n "$COMPILE_SDK" ]] \
        || die "no android platforms installed under ${SDK}/platforms"
    ANDROID_JAR="${SDK}/platforms/android-${COMPILE_SDK}/android.jar"
    log "android-$TARGET_SDK not installed — compiling against android-$COMPILE_SDK (manifest still targets $TARGET_SDK)"
fi
[[ -f "$ANDROID_JAR" ]] || die "android.jar not found: $ANDROID_JAR"

if [[ -z "$BUILD_TOOLS_VER" ]]; then
    # pick the newest build-tools that has d8 + aapt2
    for v in 35.0.0 34.0.0 33.0.2 32.0.0 30.0.3 29.0.0 28.0.0; do
        if [[ -x "${SDK}/build-tools/${v}/d8" || -x "${SDK}/build-tools/${v}/dx" ]]; then
            BUILD_TOOLS_VER="$v"; break
        fi
    done
fi
BT="${SDK}/build-tools/${BUILD_TOOLS_VER}"
[[ -d "$BT" ]] || die "no usable build-tools under ${SDK}/build-tools"

AAPT2="${BT}/aapt2";        [[ -x "$AAPT2" ]] || AAPT2="$(command -v aapt2 || true)"
DEXER="${BT}/d8";           [[ -x "$DEXER" ]] || DEXER="${BT}/dx"
ZIPALIGN="${BT}/zipalign";  [[ -x "$ZIPALIGN" ]] || ZIPALIGN="$(command -v zipalign || true)"
APKSIGNER="${BT}/apksigner";[[ -x "$APKSIGNER" ]] || APKSIGNER="$(command -v apksigner || true)"
for t in AAPT2 DEXER ZIPALIGN APKSIGNER; do
    [[ -x "${!t}" ]] || die "$t not found (build-tools ${BUILD_TOOLS_VER})"
done
command -v javac >/dev/null || die "javac not found"

log "SDK=$SDK platform=android-$TARGET_SDK build-tools=$BUILD_TOOLS_VER dex=$(basename "$DEXER")"

# --- inputs -----------------------------------------------------------------
declare -A ABI_SO=(
    [arm64-v8a]="${REPO_ROOT}/libcore_arm64.so"
    [armeabi-v7a]="${REPO_ROOT}/libcore_arm.so"
)
NABI=0
for abi in "${!ABI_SO[@]}"; do
    [[ -f "${ABI_SO[$abi]}" ]] || die "missing ${ABI_SO[$abi]} (run: make android)"
    NABI=$((NABI+1))
done

JAVA_SRCS=("${REPO_ROOT}"/android/src/com/moviango/mediaplayer/*.java)
[[ -f "${JAVA_SRCS[0]}" ]] || die "no java sources under android/src/"

VERSION="$(cd "${REPO_ROOT}" && git describe --tags --always --dirty 2>/dev/null || echo 5.0)"
VERCODE=1

rm -rf "$OUT"
mkdir -p "$OUT"/{java,classes,apklib,res-flat,dex}

# --- 1. manifest -------------------------------------------------------------
sed -e "s/@@VERSION@@/${VERSION}/g" \
    -e "s/@@APPNAME@@/${APPNAME}/g" \
    -e "s/@@VERCODE@@/${VERCODE}/g" \
    -e "s/@@ANDROID_MIN_SDK_VERSION@@/${MIN_SDK}/g" \
    -e "s/@@ANDROID_TARGET_SDK_VERSION@@/${TARGET_SDK}/g" \
    "${REPO_ROOT}/android/AndroidManifest.xml.in" > "$OUT/AndroidManifest.xml"
log "manifest: version=$VERSION vercode=$VERCODE min=$MIN_SDK target=$TARGET_SDK"

# --- 2. resources + R.java ---------------------------------------------------
"$AAPT2" compile --dir "${REPO_ROOT}/android/res" -o "$OUT/res-flat/res.zip" \
    || die "aapt2 compile failed"
"$AAPT2" link -o "$OUT/res.apk" \
    --manifest "$OUT/AndroidManifest.xml" \
    -I "$ANDROID_JAR" \
    --java "$OUT/java" \
    "$OUT/res-flat/res.zip" \
    || die "aapt2 link (R.java) failed"
log "resources + R.java done"

# --- 3. javac + dex ----------------------------------------------------------
RJAVAS=()
while IFS= read -r f; do RJAVAS+=("$f"); done \
    < <(find "$OUT/java" -name '*.java')
javac -source 8 -target 8 -encoding UTF-8 \
    -classpath "$ANDROID_JAR" \
    -d "$OUT/classes" \
    "${RJAVAS[@]}" "${JAVA_SRCS[@]}" \
    || die "javac failed"
"$DEXER" --lib "$ANDROID_JAR" --output "$OUT/dex" \
    $(find "$OUT/classes" -name '*.class') \
    || die "dex failed"
log "dex done"

# --- 4. unsigned APK ----------------------------------------------------------
# aapt2 link only takes the compiled-resources zip; dex and native
# libs are added to the APK zip afterwards (APK is a plain zip; dex
# goes at classes.dex, native libs at lib/<abi>/libcore.so).
"$AAPT2" link -o "$OUT/movian-go.unsigned.apk" \
    --manifest "$OUT/AndroidManifest.xml" \
    -I "$ANDROID_JAR" \
    "$OUT/res-flat/res.zip" \
    || die "aapt2 link (apk) failed"
command -v zip >/dev/null || die "zip not found"
for abi in "${!ABI_SO[@]}"; do
    mkdir -p "$OUT/apklib/lib/$abi"
    cp "${ABI_SO[$abi]}" "$OUT/apklib/lib/$abi/libcore.so"
    # Optional extra native libs: android/vendorlibs/<abi>/*.so are
    # bundled alongside libcore.so. Needed on firmware where system
    # libs (e.g. Amlogic/Rockchip libnativeloader.so) NEEDED vendor
    # libs that the app namespace cannot reach (/system/vendor/lib):
    # the app's own lib dir is searched first and is permitted.
    VLDIR="${REPO_ROOT}/android/vendorlibs/$abi"
    if [[ -d "$VLDIR" ]]; then
        cp "$VLDIR"/*.so "$OUT/apklib/lib/$abi/"
        log "bundled extra libs for $abi: $(ls "$VLDIR"/*.so | xargs -n1 basename | tr '\n' ' ')"
    fi
done
(cd "$OUT" && cp dex/classes.dex . && zip -q movian-go.unsigned.apk classes.dex) \
    || die "adding classes.dex failed"
(cd "$OUT/apklib" && zip -qr "$OUT/movian-go.unsigned.apk" lib) \
    || die "adding native libs failed"
log "unsigned apk done ($NABI ABIs)"

# --- 5. align + sign ----------------------------------------------------------
"$ZIPALIGN" -f -p 4 "$OUT/movian-go.unsigned.apk" "$OUT/movian-go.aligned.apk"

APK="${REPO_ROOT}/movian-go.apk"
# Release signing: the keystore lives OUT of the repo (a committed
# keystore exposes the private key to offline brute-force). Set
# MOVIAN_KEYSTORE (path) + MOVIAN_KEYSTORE_PASS to use it.
if [[ -n "${MOVIAN_KEYSTORE_PASS:-}" ]]; then
    KS="${MOVIAN_KEYSTORE:-${REPO_ROOT}/android/movian.keystore}"
    "$APKSIGNER" sign --ks "$KS" \
        --ks-pass env:MOVIAN_KEYSTORE_PASS \
        --out "$APK" "$OUT/movian-go.aligned.apk"
    log "signed ($KS) -> $APK"
else
    # Android refuses unsigned APKs — sign with the standard debug
    # keystore (created on demand) so the result is installable.
    DBGKS="${HOME}/.android/debug.keystore"
    if [[ ! -f "$DBGKS" ]]; then
        mkdir -p "${HOME}/.android"
        keytool -genkeypair -v -keystore "$DBGKS" \
            -storepass android -keypass android -alias androiddebugkey \
            -keyalg RSA -keysize 2048 -validity 10000 \
            -dname "CN=Android Debug,O=Android,C=US" >/dev/null 2>&1 \
            || die "cannot create debug keystore (keytool missing?)"
        log "created debug keystore $DBGKS"
    fi
    "$APKSIGNER" sign --ks "$DBGKS" --ks-pass pass:android \
        --out "$APK" "$OUT/movian-go.aligned.apk" \
        || die "debug-keystore sign failed"
    log "signed (debug keystore) -> $APK"
fi

"$APKSIGNER" verify --print-certs "$APK" 2>/dev/null | head -3 || true
log "done: $APK"
