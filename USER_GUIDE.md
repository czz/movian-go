# Movian Go — User Guide

Movian is a media center: browse local files, network sources and
plugins, and play them with hardware-accelerated decoding.

## Running

```bash
./movian-go                      # GLFW window (X11 or Wayland session)
./movian-go --platform wayland   # pin the display server
./movian-go -d ~/.movian-go      # explicit data directory
```

On first run Movian creates its data directory (`persistent/` state,
kvstore, metadb, playqueue). The dev build reads `glwskins/`, `lang/`
and `res/` from its working directory — installed and bundle builds
find them automatically.

## Keyboard

| Keys | Action |
|---|---|
| Arrow keys | Navigate |
| Enter | Activate / open |
| Backspace / Esc | Back |
| Tab / Shift+Tab | Focus next / previous widget |
| Shift+Arrows | Move focused item |
| Alt+← / Alt+→ | History back / forward |
| PageUp / PageDown | Page up/down — prev/next channel during playback |
| Home / End | Top / bottom of a list |
| Delete | Delete selected item |
| Ctrl+← / Ctrl+→ | Skip backward / forward (chapter/track) |
| Ctrl+Shift+← / Ctrl+Shift+→ | Seek backward / forward |
| Ctrl+↑ / Ctrl+↓ | Volume up / down |
| Ctrl+Shift+↓ | Mute toggle |
| Ctrl+= / Ctrl+- / Ctrl+0 | Zoom UI in / out / reset |
| F12 (+Ctrl or Shift) | Screenshot |
| Alt+F4 | Quit |

**Media keys** (XF86 / evdev): Play/Pause, Stop, Eject, Record, Rewind,
Fast-Forward, Next/Previous track are mapped. **MPRIS2** is exposed on
D-Bus: desktop widgets, playerctl and DE media keys control playback.
On Linux with HDMI-CEC hardware (`/dev/cec*`), a TV remote works too
when built with `-tags libcec`.

## Home screen

The home page collects **services**: discovered sources and enabled
plugins. Network discovery (UPnP/DLNA servers, mDNS) populates
automatically. Built-in sources include:

- **Local storage** — browse filesystem folders
- **UPnP/DLNA** — servers discovered on the LAN
- **HTSP** — Tvheadend live TV / recordings
- **YouTube** — search and playback with quality selection
- **BitTorrent** — torrents/magnets stream directly
- **Icecast, HLS, RTMP, HTTP(S)** — network streams by URL
- **DVD** — disc and ISO playback (menus included)

Type a path or URL in the search/open box to play anything directly —
`file://`, `http(s)://`, `hls:`, `magnet:`, `htsp://host:9982`, …

## Playback

- Seek bar, subtitle and audio track selection, aspect/zoom controls
  are on the playback OSD
- Hardware decode is automatic: VAAPI/NVDEC (Linux), MediaCodec
  (Android), VideoToolbox (macOS), D3D11VA/DXVA2 (Windows),
  OpenMAX IL (Raspberry Pi) — see *Settings → Video playback*
- Subtitles load automatically next to the file; *Settings →
  Subtitles* covers fonts (color emoji supported), size and encoding
- Play queue and resume state persist across runs

## Plugins

Browse the **Plugins** page to install from a repository, or drop a
plugin `.zip` onto the file browser — it's detected and offered for
install. Plugin settings appear under *Settings → Plugins*.
Developers: see [PLUGINS.md](PLUGINS.md).

## Settings worth knowing

- *Settings → Video playback* — hw decode, deinterlacing
- *Settings → Audio* — ALSA/Pulse output device, HDMI passthrough
- *Settings → Network* — HTTP/2, HTTP/3 (QUIC) toggles, proxy
- *Settings → Appearance* — skin, language (`lang/` is shipped)
- *Settings → System* — usage statistics (off by default unless
  enabled; reports to the project Matomo portal)

## Logs & troubleshooting

- `log/movian-go-0.log` in the working/data directory
- Console build on Windows (`movian-go-console.exe`) keeps stderr
  visible; on Linux just run from a terminal
- If the window never appears, check OpenGL availability
  (`glxinfo`/Wayland session) — GLFW needs a GL 2.1+/ES2 context
- `failed to sufficiently increase send/receive buffer size` —
  quic-go wants ~7 MB UDP socket buffers for full HTTP/3
  throughput; the Linux default is smaller. Harmless (QUIC still
  works, just slower on high-bandwidth links). To silence/fix:

  ```bash
  sudo sysctl -w net.core.wmem_max=7500000
  sudo sysctl -w net.core.rmem_max=7500000
  echo "net.core.wmem_max=7500000
  net.core.rmem_max=7500000" | sudo tee /etc/sysctl.d/99-movian-quic.conf
  ```
