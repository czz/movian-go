# Building Movian (Go port)

Per-architecture build instructions. The C build system (configure +
support/configure.inc) is replaced by `go build` + vendored static
deps under `third_party/` and `ffmpeg/`.

Supported targets today:

| Target | Build host | UI frontend | Status |
|---|---|---|---|
| Linux amd64/arm64 | native | `glfw` (X11 + Wayland at runtime) | verified |
| Android arm64/arm (API 26+) | linux cross (NDK) | android EGL/GLW | libcore.so + signed APK build; device test pending |
| macOS arm64/amd64 | macOS | `glfw` | code complete; needs a Mac to compile |
| Windows amd64 | mingw-w64 cross | `glfw` | `movian-go.exe` links; runtime on Windows pending |

Not ported: iOS, STOS (deferred to Debian Trixie base). NaCl is dead
upstream. `make ps3` still exists in the Makefile (psl1ght tag,
GOARCH=ppc64) but is **legacy/untested** — ffmpeg 9 was never built for
it and no device verification exists; treat it as dormant until a
toolchain test says otherwise. rpi and sunxi are ported behind their
build tags (device headers/libs needed to link).

### Third-party dependency manifest

`scripts/deps.env` is the single source of truth for dependency
name/version/URL/SHA-256 (ffmpeg 9.0.1, zlib 1.3.1, glfw 3.4,
freetype 2.13.3 — the latter only for regenerating the wasm2go font
module; sqlite needs nothing: internal/db runs on the pure-Go
modernc.org/sqlite transpilation on every platform). All
`scripts/build_*.sh` source it and verify archives via
`scripts/fetch_dep.sh` (`dep_fetch`): downloads land in the gitignored
`.deps/` cache (`DEPS_CACHE` overridable; the ffmpeg scripts keep their
existing `ffmpeg/` prefix cache), corrupt cache entries are
re-downloaded, and a checksum mismatch is fatal. Source/build dirs are
version-keyed (`/tmp/ffmpeg-src-<target>-<version>` etc.). Setting a dep
tarball path explicitly (e.g. `FFMPEG_SRC_TAR=...`) verifies the local
file without downloading — offline builds work from a warm cache.

---

## 1. Linux (native build — this machine)

### 1.1 System dependencies (Debian/Ubuntu)

```bash
sudo apt install build-essential golang pkg-config \
    nasm \                    # FFmpeg x86 asm (or yasm)
    libglfw3-dev libwayland-dev \         # GLFW frontend (X11+Wayland)
    # NOTE: needs GLFW >= 3.4 (runtime X11/Wayland selection). Distro
    # LTS glfw is 3.3 — in that case use the vendored static build:
    #   ./scripts/build_glfw_linux.sh   → third_party/glfw/linux
    # the Makefile auto-prefers it via PKG_CONFIG_PATH when present.
    libx11-dev libxext-dev \              # x11 tag: native X11 helpers in GLFW
    libgl1-mesa-dev \
    libasound2-dev \                      # alsa (default audio)
    libpulse-dev \                        # only for -tags pulse
    libva-dev libva-drm3 libdrm-dev \     # VAAPI hw decode
    libavahi-client-dev                   # only for -tags avahicgo rollback
    libcec-dev libp8-platform-dev \       # only for -tags libceccgo rollback
    libglib2.0-dev \                      # only for -tags "rpi connman connmancgo"
```

FFmpeg is vendored — the cgo LDFLAGS point at `./ffmpeg/lib`:

```bash
./scripts/build_ffmpeg.sh      # one-off: static FFmpeg 9.0.1 → ./ffmpeg
                               # (~15 min; skips if already built)
```

### 1.2 Build

```bash
make build            # -tags "x11 glfw"  — GLFW frontend (X11/Wayland at runtime)
make build-glfw-only  # -tags glfw        — Wayland-only, libX11 not linked
```

The `webpopup` tag (C: `CONFIG_WEBPOPUP`) is auto-enabled when
`pkg-config` finds `cef` — it provides the plugin API
`require('popup').webpopup/webbrowser/webcookies/webuseragent/
injectcookies` for OAuth/Cloudflare flows (trap-URL result,
persistent cookie jar, UA pinning). One backend on every desktop
platform: **CEF in off-screen mode**, frames composited as a GLW
texture (the old WPE/WebKitGTK, WebView2 and WKWebView backends are
gone). Without `cef.pc` the build compiles the canonical
"unsupported" stub. `WEBPOPUP=`/`CEF=` pin the feature off.
`./scripts/fetch_cef.sh <linux64|windows64|windows32|macosx64|
macosarm64>` downloads the pinned CEF 152 minimal dist (SHA-1
verified against the official CDN index) into
`third_party/cef/<plat>/` and emits a matching `cef.pc` — build with
`PKG_CONFIG_PATH=$PWD/third_party/cef/<plat>` (windows exes need the
windows* dist: `PKG_CONFIG_PATH=.../windows64 make windows`). CEF
ships no builds for android or linux-armv6 (RPi) — webpopup is the
stub there. CEF workers re-exec the movian-go binary itself
(`--type=...` intercepted by a C constructor before the Go runtime).

`make linux-bundle` embeds skins/fonts/lang into a single
self-contained binary (`movian-go-bundle`) and includes CEC via the
dependency-free `/dev/cec*` backend (`-tags "glfw x11 bundle libcec"`).
The release `linux-cef` variant adds real CEF webpopup on top
(`webpopup cef libcec`). ConnMan is appliance-only — never in desktop
builds.

`make check-all-tags` compile-checks every platform/build-tag variant
with an installed toolchain — linux `x11 glfw` / `glfw`, `webpopup`
× `cef` (real CEF headers when `cef.pc` is in `PKG_CONFIG_PATH`) ×
`libcec`, windows amd64+386 via mingw (cef when
`third_party/cef/windows{64,32}` exists), android arm64+arm via NDK
— plain `go build ./...` silently skips build-tag-gated files.


Run:

```bash
./movian-go                      # GLFW frontend (X11 or Wayland session)
./movian-go --platform wayland   # pin GLFW to a display server
```

HW decode on linux: VAAPI (Intel/AMD, EGL zero-copy) → NVDEC
(NVIDIA, transfer fallback). Settings under *Settings → Video playback*.

---

## 2. Android (cross-build on Linux — libcore.so, API 26 / Android 8+)

### 2.1 Toolchain

```bash
export ANDROID_NDK=/opt/android/android-ndk-r23b   # r23b used/verified
# NDK r23b provides aarch64-linux-android26-clang etc. under
# toolchains/llvm/prebuilt/linux-x86_64/bin
```

### 2.2 Vendored per-ABI deps (one-off)

```bash
./scripts/build_ffmpeg_android.sh    # FFmpeg 9 static → ffmpeg/android/{arm64,arm}
./scripts/build_ffmpeg_android.sh --shared
# also builds shared libav*.so → ffmpeg/android/{arm64,arm}-shared/ —
# for APK bundling on firmware whose system libs NEED vendor
# libavutil.so the app namespace cannot reach (broken Amlogic/Rockchip
# images; upstream shipped shared libav* in the APK anyway). Copy the
# needed .so — or the device-pulled vendor lib for versioned DT_NEEDED —
# into android/vendorlibs/<abi>/ and build_apk.sh bundles them into
# lib/<abi>/ automatically.
```

SQLite needs no per-ABI build either — `internal/db` uses the pure-Go
modernc.org/sqlite transpilation everywhere (`build_sqlite_android.sh`
is retired; the old `third_party/sqlite/` archives are unused).
FreeType/fontconfig likewise: they run inside the pure-Go wasm2go
module (`internal/ftwasm`) on every platform.

### 2.3 libcore.so per ABI

```bash
make android            # both ABIs (API 26) — uses ANDROID_NDK env var
# or manually:
GOOS=android GOARCH=arm64 CGO_ENABLED=1 \
  CC=$ANDROID_NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/aarch64-linux-android26-clang \
  go build -buildmode=c-shared -o libcore_arm64.so ./cmd/movian-go
GOOS=android GOARCH=arm GOARM=7 CGO_ENABLED=1 \
  CC=$ANDROID_NDK/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi26-clang \
  go build -buildmode=c-shared -o libcore_arm.so ./cmd/movian-go
```

Verify: `readelf -d libcore_arm64.so` — NEEDED must list only NDK
system libs (liblog/libandroid/libGLESv2/libOpenSLES/libjnigraphics/
libmediandk/libz/libdl/libm/libc); `nm -D` must show the Java_*
exports (Core_coreInit, glw*, vd*, openUri, permissionResult, ...).

### 2.4 APK assembly — `scripts/build_apk.sh` (verified)

The Java shell lives in `android/` (restored from git history —
upstream package `com.lonelycoder.mediaplayer`). The script performs
the whole pipeline — manifest → resources/R.java → javac → d8 →
packaging → zipalign → sign:

```bash
make apk          # libcore per ABI + assembly in one step
# -> movian-go.apk  (signed, minSdk 26, both ABIs)
```

Environment knobs (all optional, shown with defaults):

```bash
ANDROID_HOME=/opt/android        # SDK root (build-tools + platforms)
ANDROID_NDK=/opt/android/android-ndk-r23b
BUILD_TOOLS=35.0.0               # SDK build-tools version to use
PLATFORM=android-28              # compile platform
MIN_SDK=26                       # manifest minSdkVersion
TARGET_SDK=28                    # manifest targetSdkVersion
```

The webpopup clearance policy defaults to the first-party variant
(`android_cf_firstparty`): Android WebView stores Cloudflare's
`cf_clearance` as a Partitioned cookie, and the variant accepts it
only for the exact top-level origin and the plugin that opened the
popup. `make apk ANDROID_FIRSTPARTY=0` builds the strict variant,
which refuses every Partitioned cookie.

Signing: if `MOVIAN_KEYSTORE_PASS` is set the APK is signed with
`$MOVIAN_KEYSTORE` (default `android/movian.keystore` — a local file
kept out of git); otherwise the script signs with the standard
`~/.android/debug.keystore` (created on demand via keytool) —
Android refuses unsigned APKs, so the output is always installable.

Install/run:

```bash
adb install -r movian-go.apk
adb shell am start -n com.lonelycoder.mediaplayer/.GLWActivity
adb logcat | grep -i movian   # JNI / EGL / MediaCodec diagnostics
```

Note: native libs are fetched fresh only if missing — run `make
android` first to rebuild `libcore_{arm64,arm}.so` after Go changes.

---

## 3. macOS (on a Mac — GLFW frontend)

Upstream's Cocoa frontend (`src/arch/osx/*.m`) is NOT ported — GLFW
owns the Cocoa window/GL context (same role as on linux/Wayland).

### 3.1 Dependencies

```bash
xcode-select --install                 # clang + SDK
brew install go pkg-config glfw
# sqlite3: nothing to install — DB runs on modernc.org/sqlite (pure Go)
```

### 3.2 Vendored FFmpeg (one-off, on the Mac)

```bash
./scripts/build_ffmpeg_darwin.sh   # ffmpeg/darwin/{arm64,amd64}
                                   # --enable-videotoolbox, no x11/vdpau/cuda
```

### 3.3 Build

```bash
make darwin
# produces movian-go-darwin-arm64 and movian-go-darwin-amd64 (both -tags glfw)
./movian-go-darwin-arm64
```

What works on darwin out of the box: CoreAudio (`mac_audio_darwin.c`),
Bonjour service discovery (`sd/bonjour`), Spotlight search
(`fa_spotlight`), mach cpu/mem monitor (`darwin.c`), GLFW frontend
(Cocoa via GLFW), desktop-OpenGL backend + video engines.

Status on darwin: the VideoToolbox port exists
(`internal/video/decoder/vtb_darwin.go` — hw decode; VDA is dead API,
deliberately skipped) but its runtime is unverified — needs a Mac.

---

## 4. Windows (cross from Linux — GLFW frontend)

```bash
# prerequisite: mingw-w64 cross toolchain
#   debian/ubuntu: apt install gcc-mingw-w64-x86-64 cmake nasm
./scripts/build_windows_deps.sh      # third_party/{zlib,glfw,libxml2}/windows/amd64
./scripts/build_ffmpeg_windows.sh    # ffmpeg/windows/amd64 (d3d11va+dxva2 enabled)
make windows                          # movian-go.exe (static, -tags glfw, GUI subsystem)
./movian-go.exe                          # on Windows: GLFW window + OpenGL renderer
make windows-console                  # movian-go-console.exe — console subsystem,
                                      # keeps stderr/GLFW errors visible in cmd
```

`movian-go.exe` links with `-H=windowsgui` so no cmd/console window appears
on launch — the GLFW window IS the whole UI (there is no separate UI
process; nothing else to start). If no window appears, run the console
build (`movian-go-console.exe`) and look for `Unable to create GLFW
window/context` (missing/failed OpenGL — e.g. RDP sessions or VMs
without GPU drivers), or check `log\movian-go-0.log` next to the
executable's working directory.

`movian-go.exe` is self-contained — imports only system DLLs
(KERNEL32/USER32/GDI32/OPENGL32/WS2_32/ole32/bcrypt/secur32/ncrypt/
crypt32/winmm/advapi32). Data lives in `%APPDATA%\Movian`.

What the windows arch adds (NO upstream C counterpart — upstream never
shipped win32; everything is modeled on the osx/linux seams):

- `internal/arch/windows.go` — MachineGuid→MD5 device id; `avtime_windows.go`
  QPC; `pipe_windows.go` loopback socketpair for the asyncio wakeup pipe
- `internal/asyncio/sys_windows.go` — Winsock+WSAPoll event loop (accept/
  ioctlsocket/recv/send via ws2_32 procs; WSA* errno mapping); TLS via
  WSADuplicateSocketW + blocking socketConn
- `internal/ui/glw/glw_glfw_windows.go` — GLFW window, `SetThreadExecutionState`
  screensaver inhibit, win32 media-key scancodes; `glw_win32_glue.c`
  resolves GL 1.2+/EXT via `wglGetProcAddress` (opengl32.dll is 1.1-only)
- `internal/media/libav/d3d_windows.go` — D3D11VA + DXVA2 hwdecode
  (av_hwdevice_ctx_create → get_format picks AV_PIX_FMT_D3D11 /
  AV_PIX_FMT_DXVA2_VLD → frames transferred to software NV12 —
  no GL interop; settings "Enable D3D11VA"/"Enable DXVA2", default on)
- `internal/audio/core/wasapi_windows.go` — WASAPI shared-mode render
  (event-driven IAudioClient, IAudioClock anchor, ISimpleAudioVolume)
- fs xattr → `FAP_NOT_SUPPORTED`; DVD runs on windows like everywhere
  else — the vendored dvd stack is a wasm2go module (`internal/dvdwasm`);
  devevent/lirc/stdin ipc → no-op stubs; fontconfig runs
  inside the wasm2go module with `C:/Windows/Fonts` fallback dirs

---

## 5. Raspberry Pi (dispmanx + EGL + OpenMAX IL)

Two deliverables: the **binary** (`make rpi`, runs on an existing OS) and
the **mgos appliance image** (`make mgos-rpi-image`, a bootable SD card
— the counterpart of upstream STOS).

### 5.1 `make rpi` — application binary

```bash
# prerequisites (one-off):
#   apt install gcc-arm-linux-gnueabihf g++-arm-linux-gnueabihf
./scripts/build_ffmpeg_rpi.sh   # Raspbian sysroot -> third_party/rpi/sysroot
                              # + FFmpeg armv6 static libs -> ffmpeg/rpi/lib
make rpi                       # movian-go-rpi (armv6)
                               # tags: rpi connman mgos webpopup libcec
```

On-device requirements: 32-bit Raspberry Pi OS **Buster** (or any armhf
image that still ships the full `/opt/vc` legacy stack — Bullseye/Bookworm
removed dispmanx/OMX), `gpu_mem=256`, `start_x=1`, vc4 KMS/FKMS overlay
**off** in config.txt. Renders fullscreen via dispmanx from a bare tty;
no X11/Wayland needed.

Supported boards — **not all Pis**: Pi 1 (A/B/A+/B+), Pi Zero/Zero W/
Zero 2 W, Pi 2 B, Pi 3 B/B+, Pi 4 B (use **HDMI0**, the port nearest
USB-C — dispmanx drives only the primary output), Pi 400, CM3/CM4.
**Pi 5 is NOT supported**: Buster never shipped a BCM2712 kernel and
Raspberry Pi removed the legacy dispmanx/OMX stack entirely on that
board. 32-bit userland only — the `/opt/vc` libs are 32-bit.

Tags: `connman` talks to ConnMan over D-Bus for the network settings UI;
`libcec` uses the dependency-free `/dev/cec*` kernel driver (TV remote);
`webpopup` is the stub here — CEF ships no armv6 build.

### 5.2 `make mgos-rpi-image` — bootable appliance SD image

```bash
# prerequisites: curl xz unzip fdisk debugfs mtools dpkg-deb
#                (+ the rpi toolchain/ffmpeg above — builds movian-go-rpi
#                 automatically if missing)
make mgos-rpi-image
# -> build/mgos-rpi/movian-go-mgos-rpi-<version>.img.xz (+ .sha256)
```

The script (`scripts/make_mgos_rpi_image.sh`) performs surgery on the last
Buster Lite image (`2021-05-07-raspios-buster-armhf-lite`) **without
root**: mtools edits the FAT boot partition, dd-split + debugfs edit the
ext4 rootfs, connman comes from the raspbian buster archive as a deb.

What the image contains:

- `/opt/movian-go/` — binary (stripped) + `glwskins`/`res`/`lang`
- **connman** enabled in systemd, `dhcpcd` disabled (connman owns every
  interface; `/etc/resolv.conf` → `/var/run/connman/resolv.conf` via a
  service drop-in)
- `movian-go.service` — autostart on boot, `Restart=always`
- **CEC**: `bcm2835_cec` + `vchiq` in modules-load.d, `dtparam=i2c_vc=on`
- mgos layout: `/mgos/{cache,persistent,fsinfo,media}`, `/mgosversion`,
  `/boot/dl` (mgos update download dir — see `internal/upgrade/mgos.go`)
- USB automount: udev rule + `mgos-media.sh` write/remove fsinfo files in
  `/mgos/fsinfo/<uuid>`; the app mounts/unmounts itself (C: stos_automount)
- `config.txt`: gpu_mem=256, start_x=1, vc4 kms overlays commented
- hostname `movian`, machine-id regenerated on first boot, rootfs
  auto-expands to fill the SD (stock Buster resize)

Flash:

```bash
xz -dc movian-go-mgos-rpi-*.img.xz | sudo dd of=/dev/sdX bs=4M status=progress conv=fsync
```

First boot expands the rootfs and reboots; then connman comes up
(Ethernet is plug&play, WiFi is configured from Movian's network UI)
and Movian starts fullscreen on its own. Board support, per-model
caveats and troubleshooting: [MGOS_RPI.md](MGOS_RPI.md).

### 5.3 Desktop bundle vs appliance

| | desktop `linux-bundle` | mgos appliance image |
|---|---|---|
| UI | GLFW window (X11/Wayland) | dispmanx fullscreen |
| Network | distro-owned (no connman) | connman built-in |
| CEC | `libcec` → `/dev/cec*` | `libcec` → `/dev/cec*` |
| webpopup | real CEF when `cef.pc` present | stub (no armv6 CEF) |
| Install | single binary / `make install` | dd image to SD |

## 6. Cross-build matrix (quick reference)

| Command | Output |
|---|---|
| `make build` | `movian-go` (linux, glfw+x11 — dev: dataroot dalla working dir) |
| `make build-glfw` | alias di `make build` (GLFW è l'unico frontend) |
| `make build-glfw-only` | `movian-go` (linux, glfw — Wayland-only, no libX11) |
| `make linux-bundle` | `movian-go-bundle` (linux, risorse embedded — self-contained) |
| `make install-build DATADIR=...` | `movian-go` per installazione (dataroot compilato) |
| `make install PREFIX=...` | installa binario + res/lang/glwskins in `$PREFIX/share/movian-go` |
| `make install-desktop` | .desktop + icona hicolor (`PREFIX=~/.local` default) |
| `make android` | `libcore_{arm64,arm}.so` |
| `make apk` | `movian-go.apk` (signed, both ABIs — needs ANDROID_HOME) |
| `make darwin` (on macOS) | `movian-go-darwin-{amd64,arm64}` |
| `make macos-app` | `Movian Go.app` bundle (icona icns + Info.plist) |
| `make windows` (mingw cross) | `movian-go.exe` (con icona .ico embedded) |
| `make windows32` | `movian-go32.exe` |
| `make rpi` | `movian-go-rpi` (armv6, dispmanx+EGL+OMX — needs arm cross + ffmpeg/rpi via `scripts/build_ffmpeg_rpi.sh`) |
| `make mgos-rpi-image` | `build/mgos-rpi/movian-go-mgos-rpi-*.img.xz` — bootable SD image (Buster Lite armhf base + movian appliance: connman+CEC+autostart, `/mgos` layout, `/boot/dl` updates). Fully unprivileged build (mtools/debugfs/dpkg-deb). Flash: `xz -dc img.xz \| dd of=/dev/sdX` |
| `make sunxi` | `movian-go-sunxi` (nominale — come rpi, richiede sysroot sunxi) |
| `make ps3` | `movian-go-ps3` (nominale — vedi `ffmpeg/psl1ght-build.md`) |
| `make ffmpeg` | vendored FFmpeg for linux |
| `scripts/build_ffmpeg_android.sh` | vendored FFmpeg for android |
| `scripts/build_ffmpeg_darwin.sh` | vendored FFmpeg for darwin |
| `scripts/build_ffmpeg_rpi.sh` | sysroot Raspbian + FFmpeg armv6 per rpi |
| `scripts/build_windows_deps.sh` | zlib/glfw for windows |
| `scripts/build_ffmpeg_windows.sh` | vendored FFmpeg for windows |


