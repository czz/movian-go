# Movian Go

<p align="center">
  <img src="glwskins/flat/icons/movian-go-icon.png" width="256" alt="Movian Go mascot">
</p>

A Go port of [Movian](https://movian.tv) — the media center formerly
known as Showtime. Faithful to the upstream C implementation in behavior
and feature set, rebuilt with idiomatic Go architecture: dependency
injection instead of global state, and pure-Go replacements where they
match the original semantics (SQLite via `modernc.org/sqlite`, mDNS via
zeroconf, ConnMan via godbus, JavaScript via goja). cgo remains where
the platform requires it — FFmpeg, OpenGL/EGL, D-Bus/glib fallback.

## What changed vs upstream C

**Architecture**

- Idiomatic Go throughout: dependency injection and owner-held state
  replaced C globals; singletons and import cycles eliminated
- Single GLFW frontend on desktop — X11 and Wayland picked at runtime
  (native X11 and GTK frontends dropped)

**Media**

- FFmpeg 9, vendored per platform
- Hardware decode everywhere: VAAPI + NVDEC (Linux), MediaCodec
  (Android), VideoToolbox (macOS), D3D11VA/DXVA2 (Windows),
  OpenMAX IL (Raspberry Pi)
- WebP image support and a faster image loader
- BitTorrent, YouTube (HLS quality selection) and Widevine DRM
  (gowidevine) backends

**C dependencies replaced by Go**

| C | Go |
|---|---|
| Duktape | goja — broader ECMAScript support |
| sqlite3 | modernc.org/sqlite |
| FreeType + fontconfig | ftwasm — transcribed to Go, color emoji |
| libdvdread/nav/css | dvdwasm |
| librtmp | FFmpeg RTMP |
| Avahi | zeroconf (mDNS) |
| GDBus/GIO/GConf | godbus/dbus |
| libcec | pure-Go `/dev/cec*` driver (cgo fallback kept) |

**Networking & integration**

- HTTP/2 and HTTP/3 (QUIC) in the fileaccess layer
- MPRIS2 over D-Bus — desktop media keys/players integration
- `webpopup` plugin API (browser window for OAuth/Cloudflare flows)
- Usage reporting retargeted to a self-hosted Matomo portal
  (`analytics.czz78.com`, replaces the defunct upstream endpoint)

## Trade-offs vs upstream C

**Where Go wins**

- Memory safety — use-after-free and buffer overflows are gone
  Go-side; the manual refcounting the C tree relied on everywhere
  (scanner, prop, media) is handled by the GC. Only the cgo seam
  keeps C-style ownership rules
- `go test -race` catches data races at runtime; upstream relied on
  manual locking discipline alone
- Much smaller C-dependency surface (the table above) → fewer ABI
  breaks, fewer CVEs, no libcec/fontconfig/dbus/avahi installs
- Single toolchain cross-builds (`GOOS`/`GOARCH`) vs per-platform
  autotools setups; `go.mod` + SHA-verified dep scripts replace
  hand-maintained configure flags
- Dependency injection: testable, multi-instance-capable services
  instead of `gconf`-style globals
- One GLFW frontend and one CEF webpopup backend instead of four
  native UI/browser stacks to maintain

**Where upstream C still wins**

- Footprint: ~15–20 MB binary vs ~80–130 MB (Go runtime + cgo
  stubs), and ~20–30 MB less RSS — matters on RPi and old devices
- Deterministic latency: no GC pauses in the render path
- Faster native libs: ftwasm/dvdwasm (transcribed C) run ~2–4×
  slower than native FreeType/libdvdnav, and they are not a
  sandbox — fine while the modules are trusted
- cgo call overhead (~50–100 ns/call) adds up on audio/video hot
  paths where C paid nothing
- VDPAU removal leaves pre-Kepler NVIDIA cards without hardware
  decode (VAAPI + NVDEC cover everything modern)
- Native frontends (glw_x11, Cocoa) had deeper platform integration
  than GLFW in a few edge cases
- 20 years of runtime on real hardware, vs this port verified
  by builds/tests on amd64 and pending hardware testing elsewhere

**Parity**: modern hardware decode coverage, playback behavior,
plugin API, GLW skinning, metadata pipeline.

## Status

| Platform | State |
|---|---|
| Linux (GLFW — X11 & Wayland) | Working — primary development target |
| Linux bundle | Working — self-contained binary, assets embedded (`make linux-bundle`) |
| Android (arm64-v8a, armeabi-v7a) | Builds — single APK, minSdk 26; device testing ongoing |
| Raspberry Pi (ARMv6) | Builds — dispmanx + EGL + OpenMAX IL; hardware testing pending |
| Windows (amd64 / 386) | Builds — static exe with embedded resources |
| macOS (amd64 / arm64) | Builds — GLFW + VideoToolbox; needs a Mac to verify |
| PS3 (psl1ght) | Experimental — FFmpeg recipe in `ffmpeg/psl1ght-build.md` |

## Quick start

```bash
make ffmpeg        # one-off: vendored FFmpeg build
make build         # -> ./movian-go  (x11; run from repo root)
./movian-go
```

GLFW 3.4 selects X11 or Wayland at runtime; pin it with
`--platform wayland` if needed. A build without the `x11` tag
(`make build-glfw-only`) drops libX11 entirely — Wayland-only.

Self-contained binary (skin/fonts/lang embedded):

```bash
make linux-bundle  # -> ./movian-go-bundle
```

System install:

```bash
make install-build DATADIR=/usr/share/movian-go
sudo make install PREFIX=/usr
sudo make install-desktop PREFIX=/usr   # .desktop entry + icon
```

Other targets: `make apk` (Android), `make windows`, `make darwin`,
`make rpi`, `make sunxi`. Full instructions, toolchains and dependency
manifest are in [BUILDING.md](BUILDING.md).

## Documentation

- [USER_GUIDE.md](USER_GUIDE.md) — running, keyboard, sources, settings
- [PLUGINS.md](PLUGINS.md) — writing and shipping plugins (goja/JS)
- [BUILDING.md](BUILDING.md) — toolchains and per-platform builds
- [FFMPEG_BOUNDARY.md](FFMPEG_BOUNDARY.md) — cgo/FFmpeg ownership rules

## Layout

```
cmd/movian-go/   composition root (bootstrap, wire_*, init_<platform>*)
internal/        all application packages
glwskins/        UI skins (flat)
res/  lang/      resources and translations
ffmpeg/          vendored FFmpeg builds per target
third_party/     vendored native deps (freetype, glfw, zlib, libxml2)
scripts/         dependency + FFmpeg cross-build scripts
```

The FFmpeg cgo boundary and ownership rules are documented in
[FFMPEG_BOUNDARY.md](FFMPEG_BOUNDARY.md).

## License

Same as upstream Movian — see [LICENSE](LICENSE).
