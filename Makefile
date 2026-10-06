# Movian Go Build System
# Build system for Go port of Movian media player

.PHONY: all clean build linux android apk darwin windows windows-console rpi ps3 sunxi help ffmpeg ffmpeg-force ffmpeg-clean check-ffmpeg check-cgo-drift check-all-tags

# Git version detection (can be overridden via environment or command line)
# VERSIONOVERRIDE takes precedence over git describe
VERSIONOVERRIDE ?=
GIT_DESCRIBE ?= $(if $(VERSIONOVERRIDE),$(VERSIONOVERRIDE),$(shell git describe --dirty --abbrev=5 2>/dev/null | sed -e 's/-/./g'))
ifeq ($(GIT_DESCRIBE),)
GIT_DESCRIBE := 0.0.0
endif
LDFLAGS := -X github.com/czz/movian-go/internal/version.appversion=$(GIT_DESCRIBE)

# Data root directory (equivalent to C's SHOWTIME_DATADIR)
# Default: "" = current working directory (development, like C's wd.c)
# For installed builds: make install DATADIR=/usr/share/showtime
DATADIR ?=
ifneq ($(DATADIR),)
LDFLAGS += -X github.com/czz/movian-go/internal/app.DataDir=$(DATADIR)
endif

# FFmpeg 9 prefix — must match the tracked cgo directives
# (internal/libav/cgo_directives.go, internal/image/cgo_directives.go,
# internal/api/screenshot/screenshot_cgo.go) and scripts/build_ffmpeg.sh.
# NOTE: FFMPEG_PREFIX only redirects check-ffmpeg/build_ffmpeg.sh;
# the #cgo directives always resolve ${SRCDIR}/../ffmpeg (repo root)
# because cgo does not expand Make variables. A non-default prefix is
# therefore a diagnostic override, not a link redirect.
FFMPEG_PREFIX ?= $(CURDIR)/ffmpeg

# check-ffmpeg verifies that the FFmpeg 9 static libraries and headers are
# present in $(FFMPEG_PREFIX). Without this check, cgo silently falls back
# to system headers (/usr/include/x86_64-linux-gnu) which may be a different
# FFmpeg version (e.g. Debian's FFmpeg 7), causing subtle API mismatches
# like the av_parser_init(enum AVCodecID) vs av_parser_init(int) difference.
check-ffmpeg:
	@echo "[CHECK] Verifying FFmpeg 9 static libraries in $(FFMPEG_PREFIX)..."
	@for f in \
		$(FFMPEG_PREFIX)/include/libavcodec/avcodec.h \
		$(FFMPEG_PREFIX)/include/libavformat/avformat.h \
		$(FFMPEG_PREFIX)/include/libavutil/avutil.h \
		$(FFMPEG_PREFIX)/include/libswscale/swscale.h \
		$(FFMPEG_PREFIX)/include/libswresample/swresample.h \
		$(FFMPEG_PREFIX)/lib/libavcodec.a \
		$(FFMPEG_PREFIX)/lib/libavformat.a \
		$(FFMPEG_PREFIX)/lib/libavutil.a \
		$(FFMPEG_PREFIX)/lib/libswscale.a \
		$(FFMPEG_PREFIX)/lib/libswresample.a; do \
		if [ ! -f "$$f" ]; then \
			echo ""; \
			echo "ERROR: FFmpeg 9 not built or incomplete."; \
			echo "  Missing: $$f"; \
			echo ""; \
			echo "  The cgo directives in internal/libav/cgo_directives.go point to:"; \
			echo "    -I$(FFMPEG_PREFIX)/include"; \
			echo "    -L$(FFMPEG_PREFIX)/lib"; \
			echo ""; \
			echo "  Without FFmpeg 9, cgo silently falls back to system FFmpeg"; \
			echo "  headers (e.g. /usr/include/x86_64-linux-gnu) which may be a"; \
			echo "  different version, causing API mismatches."; \
			echo ""; \
			echo "  Run: make ffmpeg"; \
			echo "  Or:  ./scripts/build_ffmpeg.sh"; \
			exit 1; \
		fi; \
	done
	@grep -q 'define LIBAVCODEC_VERSION_MAJOR  *63' \
		$(FFMPEG_PREFIX)/include/libavcodec/version_major.h || { \
		echo ""; \
		echo "ERROR: $(FFMPEG_PREFIX) headers are not FFmpeg 9"; \
		echo "  (LIBAVCODEC_VERSION_MAJOR != 63 in version_major.h)."; \
		echo "  Run: make ffmpeg"; \
		exit 1; \
	}
	@echo "[CHECK] FFmpeg 9 static libraries OK (libavcodec major 63)"

# Drift gate: the generated cgo directive files are tracked and must
# match their generators byte-for-byte. Fails if regeneration
# produces different output.
check-cgo-drift:
	@set -e; \
	cp internal/libav/cgo_directives.go /tmp/.cgo_drift_prev.go; \
	(cd internal/libav && go run generate_cgo.go >/dev/null); \
	diff -q /tmp/.cgo_drift_prev.go internal/libav/cgo_directives.go \
		|| { echo "ERROR: drift in internal/libav/cgo_directives.go"; exit 1; }; \
	cp internal/image/cgo_directives.go /tmp/.cgo_drift_prev.go; \
	(cd internal/image && go run generate_cgo.go >/dev/null); \
	diff -q /tmp/.cgo_drift_prev.go internal/image/cgo_directives.go \
		|| { echo "ERROR: drift in internal/image/cgo_directives.go"; exit 1; }; \
	cp internal/api/screenshot/screenshot_cgo.go /tmp/.cgo_drift_prev.go; \
	(cd internal/api/screenshot && go run generate_screenshot_cgo.go >/dev/null); \
	diff -q /tmp/.cgo_drift_prev.go internal/api/screenshot/screenshot_cgo.go \
		|| { echo "ERROR: drift in internal/api/screenshot/screenshot_cgo.go"; exit 1; }; \
	cp internal/media/libav/cgo_directives.go /tmp/.cgo_drift_prev.go; \
	(cd internal/media/libav && go run generate_cgo.go >/dev/null); \
	diff -q /tmp/.cgo_drift_prev.go internal/media/libav/cgo_directives.go \
		|| { echo "ERROR: drift in internal/media/libav/cgo_directives.go"; exit 1; }; \
	echo "[CHECK] cgo directives: no drift"

# Default target
all: build

# Build (or reuse) the static FFmpeg 9.0.1 libraries used by the cgo
# libav bindings. Idempotent: skips the build when the libraries already
# exist in ./ffmpeg. See scripts/build_ffmpeg.sh for details.
ffmpeg:
	@echo "Ensuring FFmpeg static libraries are built..."
	./scripts/build_ffmpeg.sh

# Force a clean rebuild of the FFmpeg static libraries.
ffmpeg-force:
	@echo "Force-rebuilding FFmpeg static libraries..."
	./scripts/build_ffmpeg.sh --force

# Remove the FFmpeg build output (headers + libs), keeping the tarball.
ffmpeg-clean:
	@echo "Cleaning FFmpeg build output..."
	./scripts/build_ffmpeg.sh --clean

# Webpopup (webpopup tag, C: CONFIG_WEBPOPUP) — THE backend is CEF
# (Chromium Embedded Framework) windowless/offscreen: OnPaint →
# websurface GLW widget — Movian's window/focus/input drive the
# page. Same code path on linux/windows/darwin. Enabled when a
# cef.pc exists: provide one pointing at a CEF binary dist
# (Cflags -I<cef>, Libs -L<cef>/Release -lcef
# -Wl,-rpath,<cef>/Release). ?= lets baseline/CI pin the feature
# explicitly (WEBPOPUP= / CEF= force off).
CEF ?= $(shell pkg-config --exists cef && echo cef)
WEBPOPUP ?= $(CEF:%=webpopup)
ifneq ($(WEBPOPUP),)
# pcshim/ is a repo-local drop-in for cef.pc (takes precedence).
export PKG_CONFIG_PATH := $(CURDIR)/pcshim$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH))
endif
# Vendored GLFW (scripts/build_glfw_linux.sh) shadows distro glfw when
# present: Debian/Ubuntu LTS ship 3.3.x, Movian needs 3.4's runtime
# platform selection (X11+Wayland). pkg-config order makes it win.
ifneq ($(wildcard $(CURDIR)/third_party/glfw/linux/lib/libglfw3.a),)
export PKG_CONFIG_PATH := $(CURDIR)/third_party/glfw/linux/lib/pkgconfig$(if $(PKG_CONFIG_PATH),:$(PKG_CONFIG_PATH))
endif
# EXTRA_TAGS injects additional feature tags (pulse, dummyaudio,
# libcec, glwrec, connman, mgos...) into every target without
# changing the shipped defaults. BUILD_TAGS itself is ?= so a
# verification job can pin the exact tag set: `make build
# BUILD_TAGS="x11 glfw webpopup"`.
EXTRA_TAGS ?=
BUILD_TAGS ?= x11 glfw $(WEBPOPUP) $(CEF) $(EXTRA_TAGS)

# CEF workers re-exec the movian-go binary itself — the cgo C
# constructor in webpopup_cef.go intercepts --type=... before the Go
# runtime starts, so no separate helper binary is needed.
# (MOVIANGO_CEF_SUBPROC env can still point at an external helper.)

# Build for current platform (development, uses CWD as data root)
# GLFW is the only Linux UI frontend (extension: upstream C has none);
# the x11 tag enables its X11-native helpers (fullscreen grab,
# screensaver reset). GLFW 3.4 picks X11 or Wayland at runtime —
# --platform pins it.
build: check-ffmpeg
	@echo "Building Movian Go for current platform..."
	@echo "Version: $(GIT_DESCRIBE)"
	go build -tags "$(BUILD_TAGS)" -ldflags "$(LDFLAGS)" -o movian-go ./cmd/movian-go

# Kept for compatibility: identical to `build` now that GLFW is the
# only frontend.
build-glfw: check-ffmpeg
	@echo "Building Movian Go with GLFW frontend..."
	@echo "Version: $(GIT_DESCRIBE)"
	go build -tags "x11 glfw $(WEBPOPUP) $(CEF) $(EXTRA_TAGS)" -ldflags "$(LDFLAGS)" -o movian-go ./cmd/movian-go

# GLFW frontend without X11 support (no x11 build tag): the
# X11-native GLFW calls (glfwGetX11Display/Window) are compiled out,
# leaving a Wayland-only frontend. The native glw_x11.c backend and
# the VDPAU glue are gone entirely — FFmpeg is now built
# --disable-vdpau, so no libvdpau/libX11 appears in the FFmpeg link
# line either.
build-glfw-only: check-ffmpeg
	@echo "Building Movian Go with GLFW frontend only..."
	@echo "Version: $(GIT_DESCRIBE)"
	go build -tags "glfw $(WEBPOPUP) $(CEF) $(EXTRA_TAGS)" -ldflags "$(LDFLAGS)" -o movian-go ./cmd/movian-go

# Build for installed Linux (uses DATADIR like C's datadir.c)
install-build: check-ffmpeg
	@echo "Building Movian Go for installation..."
	@echo "Version: $(GIT_DESCRIBE)"
	@echo "DataDir: $(DATADIR)"
	go build -tags "x11 glfw $(EXTRA_TAGS)" -ldflags "$(LDFLAGS)" -o movian-go ./cmd/movian-go

# Install binary + dataroot resources — pair with
# 'make install-build DATADIR=<same path>' so the compiled-in datadir
# matches the install location (C: datadir.c SHOWTIME_DATADIR).
# Runtime resources mirror the embedded-bundle set (bundles_bundle.go).
INSTALL_DATADIR ?= $(PREFIX)/share/movian-go
install:
	install -D -m 755 movian-go $(DESTDIR)$(PREFIX)/bin/movian-go
	for d in res/metadb res/kvstore res/fileaccess res/tvheadend \
	  res/shaders res/fonts res/svg res/static res/ecmascript \
	  lang glwskins/flat; do \
	  install -d "$(DESTDIR)$(INSTALL_DATADIR)/$$d"; \
	  cp -a "$$d/." "$(DESTDIR)$(INSTALL_DATADIR)/$$d/"; \
	done
	@echo "Installed to $(DESTDIR)$(PREFIX) (dataroot: $(INSTALL_DATADIR))"

# Desktop integration for Linux: installs the .desktop entry (matched
# by Wayland app_id / X11 WM_CLASS "movian-go") and the mascot icon into
# the hicolor theme so docks/taskbars show it. Defaults to per-user
# install; override PREFIX for system-wide (e.g. PREFIX=/usr).
PREFIX ?= $(HOME)/.local
install-desktop:
	install -D -m 644 res/movian-go.desktop \
		$(DESTDIR)$(PREFIX)/share/applications/movian-go.desktop
	install -D -m 644 glwskins/flat/icons/movian-go-icon.png \
		$(DESTDIR)$(PREFIX)/share/icons/hicolor/512x512/apps/movian-go.png
	@echo "Desktop entry + icon installed under $(DESTDIR)$(PREFIX)/share"

# Build for Linux
linux: check-ffmpeg
	@echo "Building Movian Go for Linux..."
	@echo "Version: $(GIT_DESCRIBE)"
	GOOS=linux GOARCH=amd64 go build -tags "x11 glfw $(EXTRA_TAGS)" -ldflags "$(LDFLAGS)" -o movian-go-linux-amd64 ./cmd/movian-go
	GOOS=linux GOARCH=arm64 go build -tags "x11 glfw $(EXTRA_TAGS)" -ldflags "$(LDFLAGS)" -o movian-go-linux-arm64 ./cmd/movian-go

# Build for Android — libcore.so per ABI (API 26 / Android 8+).
# Requires: ANDROID_NDK (default /opt/android/android-ndk-r23b) and
# the vendored per-ABI deps built by scripts/build_{ffmpeg,
# freetype}_android.sh. The Java shell (android/) assembles the APK —
# see BUILDING.md for the aapt/javac/d8/zipalign/apksigner steps.
ANDROID_NDK ?= /opt/android/android-ndk-r23b
ANDROID_TC := $(ANDROID_NDK)/toolchains/llvm/prebuilt/linux-x86_64/bin
# arm32: -overlay swaps runtime/os_linux32.go for a variant that calls
# the 32-bit futex/timer_settime syscalls directly — the app seccomp
# filter on Android 8 TV boxes answers SIGSYS (not ENOSYS) to the
# *_time64 probes, killing the process before the runtime fallback.
# Overlays may not replace files beneath GOMODCACHE, so the toolchain
# is first copied out of it (ANDROID_GO_TC, overridable).
ANDROID_GO_TC ?= $(HOME)/.cache/movian-go-toolchain
ANDROID_SECCOMP32 := $(CURDIR)/build/seccomp32-overlay.json
$(ANDROID_GO_TC)/bin/go:
	@mkdir -p $(ANDROID_GO_TC)
	cp -a "$$(go env GOROOT)/." $(ANDROID_GO_TC)/
	@touch $@
$(ANDROID_SECCOMP32): $(CURDIR)/android/_seccomp32/os_linux32.go | $(ANDROID_GO_TC)/bin/go
	@mkdir -p $(dir $@)
	@printf '{"Replace":{"%s/src/runtime/os_linux32.go":"%s"}}\n' \
	  "$(ANDROID_GO_TC)" "$(CURDIR)/android/_seccomp32/os_linux32.go" > $@
# The same seccomp filter hits modernc.org/libc (sqlite): its musl
# layer calls statx/time64 syscalls expecting ENOSYS and gets SIGSYS.
# Build arm against a patched copy via an alternate modfile; the guard
# only activates on linux/arm so other targets are untouched.
ANDROID_LIBC := $(CURDIR)/build/libc-modernc
ANDROID_MODFILE := $(CURDIR)/build/android.mod
$(ANDROID_LIBC).patched: $(CURDIR)/scripts/seccomp32_libc.py
	go mod download modernc.org/libc
	python3 $(CURDIR)/scripts/seccomp32_libc.py \
	  "$$(go list -m -f '{{.Dir}}' modernc.org/libc)" "$(ANDROID_LIBC)"
$(ANDROID_MODFILE): go.mod go.sum | $(ANDROID_LIBC).patched
	@mkdir -p $(dir $@)
	cp go.mod $@
	cp go.sum $(CURDIR)/build/android.sum
	printf '\nreplace modernc.org/libc => %s\n' "$(ANDROID_LIBC)" >> $@
android: $(ANDROID_SECCOMP32) $(ANDROID_MODFILE)
	@echo "Building Movian Go libcore.so for Android (API 26)..."
	@echo "Version: $(GIT_DESCRIBE)"
	GOOS=android GOARCH=arm64 CGO_ENABLED=1 \
	  CC=$(ANDROID_TC)/aarch64-linux-android26-clang \
	  go build -buildmode=c-shared -ldflags "$(LDFLAGS)" \
	  -o libcore_arm64.so ./cmd/movian-go
	GOOS=android GOARCH=arm GOARM=7 CGO_ENABLED=1 \
	  CC=$(ANDROID_TC)/armv7a-linux-androideabi26-clang \
	  GOFLAGS="-overlay=$(ANDROID_SECCOMP32) -modfile=$(ANDROID_MODFILE)" \
	  $(ANDROID_GO_TC)/bin/go build -buildmode=c-shared -ldflags "$(LDFLAGS)" \
	  -o libcore_arm.so ./cmd/movian-go

# Full APK — libcore per ABI (android target) + Java shell assembly
# (aapt2/d8/zipalign/sign via scripts/build_apk.sh). Requires
# ANDROID_HOME (SDK with build-tools + platforms). Extra .so bundled
# automatically from android/vendorlibs/<abi>/ when present (broken
# vendor-lib firmware workaround — see scripts/build_apk.sh).
ANDROID_HOME ?= /opt/android
apk: android
	ANDROID_HOME=$(ANDROID_HOME) ./scripts/build_apk.sh

# Build for Darwin/macOS — GLFW frontend (upstream's Cocoa GLWUI is
# not ported; GLFW owns Cocoa). Requires ffmpeg/darwin/<arch> static
# libs (./scripts/build_ffmpeg_darwin.sh, run on a Mac) plus brew
# glfw3 freetype2 fontconfig.
darwin:
	@echo "Building Movian Go for macOS..."
	@echo "Version: $(GIT_DESCRIBE)"
	GOOS=darwin GOARCH=amd64 go build -tags glfw -ldflags "$(LDFLAGS)" -o movian-go-darwin-amd64 ./cmd/movian-go
	GOOS=darwin GOARCH=arm64 go build -tags glfw -ldflags "$(LDFLAGS)" -o movian-go-darwin-arm64 ./cmd/movian-go

# Assemble Movian Go.app from a darwin binary (arm64 preferred, else
# amd64). The macOS dock icon comes from CFBundleIconFile in the
# bundle — glfwSetWindowIcon is a no-op on macOS.
macos-app:
	@test -f movian-go-darwin-arm64 || test -f movian-go-darwin-amd64 || \
	  { echo "run 'make darwin' first"; exit 1; }
	mkdir -p "Movian Go.app/Contents/MacOS" "Movian Go.app/Contents/Resources"
	cp res/macos/Info.plist "Movian Go.app/Contents/Info.plist"
	cp res/movian-go.icns "Movian Go.app/Contents/Resources/movian-go.icns"
	@if test -f movian-go-darwin-arm64; then \
	  cp movian-go-darwin-arm64 "Movian Go.app/Contents/MacOS/movian-go"; \
	else \
	  cp movian-go-darwin-amd64 "Movian Go.app/Contents/MacOS/movian-go"; \
	fi
	@echo "Assembled Movian Go.app"

# Build for Windows (amd64) — GLFW frontend + D3D11VA/DXVA2 hwaccel +
# WASAPI audio (all new code: upstream never shipped win32). Requires
# mingw-w64 plus vendored deps (./scripts/build_windows_deps.sh) and
# FFmpeg (./scripts/build_ffmpeg_windows.sh) under
# third_party/*/windows/amd64 and ffmpeg/windows/amd64.
MINGW_CROSS ?= x86_64-w64-mingw32
# WebView2 webpopup on windows needs the mingw C++ compiler for
# windows: webpopup = CEF (opt-in when `pkg-config --exists cef` finds
# a cef.pc usable by the mingw cross toolchain). Without it the exe
# builds with the no-CEF stub. ?= so baseline/CI can pin the feature
# (WIN_WEBPOPUP= forces off) instead of depending on host config.
WIN_WEBPOPUP ?= $(shell pkg-config --exists cef && echo "webpopup cef")
windows:
	@echo "Building Movian Go for Windows..."
	@echo "Version: $(GIT_DESCRIBE)"
	$(MINGW_CROSS)-windres -O coff -F pe-x86-64 \
	  -o cmd/movian-go/movian-go.syso res/movian-go.rc
	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
	  CC=$(MINGW_CROSS)-gcc CXX=$(MINGW_CROSS)-g++ \
	  go build -trimpath -tags "glfw $(WIN_WEBPOPUP) $(EXTRA_TAGS)" -ldflags "$(LDFLAGS) -H=windowsgui" -o movian-go.exe ./cmd/movian-go; \
	  status=$$?; rm -f cmd/movian-go/movian-go.syso; exit $$status

# 32-bit windows build (i686-w64-mingw32): for 32-bit Windows installs,
# which cannot run the amd64 exe. Requires the same vendored deps and
# FFmpeg built with the 386 arch argument:
#   ./scripts/build_windows_deps.sh 386
#   ./scripts/build_ffmpeg_windows.sh 386
MINGW_CROSS_386 ?= i686-w64-mingw32
WIN_WEBPOPUP_386 ?= $(shell pkg-config --exists cef && echo "webpopup cef")
windows32:
	@echo "Building Movian Go for Windows (32-bit)..."
	@echo "Version: $(GIT_DESCRIBE)"
	$(MINGW_CROSS_386)-windres -O coff -F pe-i386 \
	  -o cmd/movian-go/movian-go.syso res/movian-go.rc
	GOOS=windows GOARCH=386 CGO_ENABLED=1 \
	  CC=$(MINGW_CROSS_386)-gcc CXX=$(MINGW_CROSS_386)-g++ \
	  go build -trimpath -tags "glfw $(WIN_WEBPOPUP_386) $(EXTRA_TAGS)" -ldflags "$(LDFLAGS) -H=windowsgui" -o movian-go32.exe ./cmd/movian-go; \
	  status=$$?; rm -f cmd/movian-go/movian-go.syso; exit $$status

# Compile-check every platform/build-tag variant whose toolchain is
# installed. Plain 'go build ./...' silently skips build-tag-gated
# files (glw_glfw_x11.go needs x11, d3d_windows.go windows,
# mediacodec android) — this target caught the Phase 7 .CPtr()
# regressions.
# Compile-only for cross targets (./internal/...): full links stay in
# the per-OS targets above.
check-all-tags:
	@echo "[check] linux default (no tags)"
	go build ./internal/...
	@echo "[check] linux -tags 'x11 glfw' (default desktop)"
	go build -tags "x11 glfw" ./internal/...
	@echo "[check] linux -tags 'x11 glfw webpopup $(CEF)' (cef if cef.pc)"
	go build -tags "x11 glfw webpopup $(CEF)" ./internal/...
	@echo "[check] linux -tags 'glfw webpopup $(CEF)' (wayland-only, cef if pc)"
	go build -tags "glfw webpopup $(CEF)" ./internal/...
	@echo "[check] linux -tags 'x11 glfw webpopup $(CEF) libcec'"
	go build -tags "x11 glfw webpopup $(CEF) libcec" ./internal/...
	@echo "[check] linux -tags 'glfw webpopup $(CEF) libcec'"
	go build -tags "glfw webpopup $(CEF) libcec" ./internal/...
	@echo "[check] linux -tags glfw (no x11)"
	go build -tags "glfw" ./internal/...
	@echo "[check] linux -tags 'rpi connman' (pure-Go godbus, rpi-only)"
	go build -tags "rpi connman" ./internal/networking/connman/
	@echo "[check] linux -tags 'rpi connman connmancgo' (cgo rollback)"
	go build -tags "rpi connman connmancgo" ./internal/networking/connman/
	@echo "[check] linux default sd (pure-Go zeroconf)"
	go build ./internal/sd/...
	@echo "[check] linux -tags 'avahicgo' (cgo avahi rollback)"
	go build -tags "avahicgo" ./internal/sd/...
	@echo "[check] linux -tags 'libcec' (pure-Go /dev/cec)"
	go build -tags "libcec" ./internal/ipc/
	@echo "[check] linux -tags 'libcec libceccgo' (cgo rollback)"
	go build -tags "libcec libceccgo" ./internal/ipc/


	@if command -v $(MINGW_CROSS)-gcc >/dev/null 2>&1; then \
	  if PKG_CONFIG_PATH=$(CURDIR)/third_party/cef/windows64 \
	     pkg-config --exists cef 2>/dev/null; then WCEF=cef; else WCEF=; fi ; \
	  echo "[check] windows amd64 -tags 'glfw webpopup $$WCEF'"; \
	  GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
	    CC=$(MINGW_CROSS)-gcc CXX=$(MINGW_CROSS)-g++ \
	    PKG_CONFIG_PATH=$(CURDIR)/third_party/cef/windows64 \
	    go build -tags "glfw webpopup $$WCEF" ./internal/... ; \
	  echo "[check] windows amd64 cmd -tags 'glfw webpopup $$WCEF'"; \
	  GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
	    CC=$(MINGW_CROSS)-gcc CXX=$(MINGW_CROSS)-g++ \
	    PKG_CONFIG_PATH=$(CURDIR)/third_party/cef/windows64 \
	    go build -tags "glfw webpopup $$WCEF" ./cmd/movian-go ; \
	else \
	  echo "[check] SKIP windows amd64 — $(MINGW_CROSS)-gcc not found"; \
	fi
	@if command -v $(MINGW_CROSS_386)-gcc >/dev/null 2>&1; then \
	  if PKG_CONFIG_PATH=$(CURDIR)/third_party/cef/windows32 \
	     pkg-config --exists cef 2>/dev/null; then WCEF=cef; else WCEF=; fi ; \
	  echo "[check] windows 386 -tags 'glfw webpopup $$WCEF'"; \
	  GOOS=windows GOARCH=386 CGO_ENABLED=1 \
	    CC=$(MINGW_CROSS_386)-gcc CXX=$(MINGW_CROSS_386)-g++ \
	    PKG_CONFIG_PATH=$(CURDIR)/third_party/cef/windows32 \
	    go build -tags "glfw webpopup $$WCEF" ./internal/... ; \
	  echo "[check] windows 386 cmd -tags 'glfw webpopup $$WCEF'"; \
	  GOOS=windows GOARCH=386 CGO_ENABLED=1 \
	    CC=$(MINGW_CROSS_386)-gcc CXX=$(MINGW_CROSS_386)-g++ \
	    PKG_CONFIG_PATH=$(CURDIR)/third_party/cef/windows32 \
	    go build -tags "glfw webpopup $$WCEF" ./cmd/movian-go ; \
	else \
	  echo "[check] SKIP windows 386 — $(MINGW_CROSS_386)-gcc not found"; \
	fi
	@echo "[check] windows: libcec not possible — libcec on windows is \
	   the cgo backend (needs a Pulse-Eight libcec build), skipped"
	@if [ -x "$(ANDROID_TC)/aarch64-linux-android26-clang" ]; then \
	  echo "[check] android arm64 -tags 'webpopup libcec' (stub popup; no CEF on android)"; \
	  GOOS=android GOARCH=arm64 CGO_ENABLED=1 \
	    CC=$(ANDROID_TC)/aarch64-linux-android26-clang \
	    go build -tags "webpopup libcec" ./internal/... ; \
	  if [ -f "$(CURDIR)/ffmpeg/android/arm64/lib/libavcodec.a" ]; then \
	    echo "[check] android arm64 cmd link"; \
	    GOOS=android GOARCH=arm64 CGO_ENABLED=1 \
	      CC=$(ANDROID_TC)/aarch64-linux-android26-clang \
	      go build -tags "webpopup libcec" ./cmd/movian-go ; \
	  else \
	    echo "[check] SKIP android cmd link — run scripts/build_ffmpeg_android.sh + build_libxml2_android.sh first (or 'make android')"; \
	  fi ; \
	else \
	  echo "[check] SKIP android arm64 — NDK toolchain not found"; \
	fi
	@if [ -x "$(ANDROID_TC)/armv7a-linux-androideabi26-clang" ]; then \
	  echo "[check] android arm -tags 'webpopup libcec'"; \
	  GOOS=android GOARCH=arm GOARM=7 CGO_ENABLED=1 \
	    CC=$(ANDROID_TC)/armv7a-linux-androideabi26-clang \
	    go build -tags "webpopup libcec" ./internal/... ; \
	else \
	  echo "[check] SKIP android arm — NDK toolchain not found"; \
	fi
	@echo "[check] rpi: real build is 'make rpi' (armv6 cross+sysroot); \
	   cef NOT possible on armv6 — no CEF builds exist"
	@echo "check-all-tags: OK"

# Console-subsystem variant: keeps a cmd window attached so stdout/stderr
# (and GLFW/GL init errors) stay visible while debugging on Windows.
windows-console:
	@echo "Building Movian Go for Windows (console subsystem)..."
	@echo "Version: $(GIT_DESCRIBE)"
	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
	  CC=$(MINGW_CROSS)-gcc CXX=$(MINGW_CROSS)-g++ \
	  go build -tags "glfw $(WIN_WEBPOPUP) $(EXTRA_TAGS)" -ldflags "$(LDFLAGS)" -o movian-go-console.exe ./cmd/movian-go

# Build for Raspberry Pi — armv6 hard-float, dispmanx+EGL+OpenMAX.
# Requires: arm-linux-gnueabihf-gcc and the RPi sysroot + FFmpeg arm
# libs produced by scripts/build_ffmpeg_rpi.sh (debs + sources fetched
# automatically on first run).
RPI_SYSROOT := $(CURDIR)/third_party/rpi/sysroot
RPI_CC ?= arm-linux-gnueabihf-gcc
# RPI_TAGS is overridable for matrix checks (e.g. +webpopup libcec);
# cef is NOT possible on armv6 — no CEF builds exist for it.
RPI_TAGS ?= rpi connman mgos

rpi:
	@echo "Building Movian Go for Raspberry Pi..."
	@echo "Version: $(GIT_DESCRIBE)"
	@test -f ffmpeg/rpi/lib/libavcodec.a || ./scripts/build_ffmpeg_rpi.sh
	GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=1 CC=$(RPI_CC) \
	  CGO_CFLAGS="-marm -I$(RPI_SYSROOT)/opt/vc/include -I$(RPI_SYSROOT)/opt/vc/include/IL -I$(RPI_SYSROOT)/usr/include" \
	  CGO_LDFLAGS="-L$(RPI_SYSROOT)/opt/vc/lib -L$(RPI_SYSROOT)/usr/lib/arm-linux-gnueabihf" \
	  go build -ldflags "$(LDFLAGS)" -tags "$(RPI_TAGS)" -o movian-go-rpi ./cmd/movian-go

# Self-contained Linux build — dataroot resources (skins, fonts, lang,
# res/) embedded via the bundle variant (C: support/dataroot/bundle.c).
# The binary runs from anywhere; no res/ folder needed next to it.
linux-bundle: check-ffmpeg
	@echo "Building Movian Go (embedded-bundle Linux)..."
	@echo "Version: $(GIT_DESCRIBE)"
	go build -tags "glfw x11 bundle" -ldflags "$(LDFLAGS)" -o movian-go-bundle ./cmd/movian-go

# Build for PS3
ps3:
	@echo "Building Movian Go for PS3..."
	@echo "Version: $(GIT_DESCRIBE)"
	GOOS=linux GOARCH=ppc64 go build -ldflags "$(LDFLAGS)" -tags psl1ght -o movian-go-ps3 ./cmd/movian-go

# Build for Sunxi
sunxi:
	@echo "Building Movian Go for Sunxi..."
	@echo "Version: $(GIT_DESCRIBE)"
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "$(LDFLAGS)" -tags sunxi -o movian-go-sunxi ./cmd/movian-go

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -f movian movian-go movian-* *.o
	go clean ./...

# Run tests
test:
	@echo "Running tests..."
	go test ./...

# Run tests with race detection
test-race:
	@echo "Running tests with race detection..."
	go test -race ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Lint code
lint:
	@echo "Linting code..."
	golangci-lint run ./...

# Install dependencies
deps:
	@echo "Installing dependencies..."
	go mod download
	go mod tidy

# Help target
help:
	@echo "Movian Go Build System"
	@echo ""
	@echo "Available targets:"
	@echo "  all           - Build for current platform (default)"
	@echo "  build         - Build for current platform"
	@echo "  build-glfw    - Build with GLFW frontend (--ui glfw, Wayland-capable)"
	@echo "  build-glfw-only - GLFW frontend only (no X11 platform support)"
	@echo "  linux         - Build for Linux (amd64, arm64)"
	@echo "  android       - Build for Android (arm, arm64)"
	@echo "  apk           - Full signed movian-go.apk (libcore + Java shell, needs ANDROID_HOME)"
	@echo "  darwin        - Build for macOS (amd64, arm64)"
	@echo "  windows       - Build for Windows (amd64, GLFW+D3D11VA, GUI subsystem)"
	@echo "  windows-console - Windows debug build (console subsystem, stderr visible)"
	@echo "  rpi           - Build for Raspberry Pi"
	@echo "  ps3           - Build for PS3"
	@echo "  sunxi         - Build for Sunxi"
	@echo "  clean         - Clean build artifacts"
	@echo "  test          - Run tests"
	@echo "  test-coverage - Run tests with coverage"
	@echo "  fmt           - Format code"
	@echo "  lint          - Lint code"
	@echo "  deps          - Install dependencies"
	@echo "  ffmpeg        - Build/reuse static FFmpeg 9.0.1 libraries"
	@echo "  ffmpeg-force  - Force rebuild of FFmpeg static libraries"
	@echo "  ffmpeg-clean  - Remove FFmpeg build output"
	@echo "  help          - Show this help message"
