// Package gconf holds the process-wide configuration singleton.
// C: gconf_t gconf (main.h:198-339, main.c) — the C global config
// struct. The Go port used to scatter these fields across ~15 packages
// as per-package vars (enable*Debug flags, Gconf* config) plus a
// separate GConf type in cmd/movian-go for the parse_opts half; they are
// consolidated here to mirror the C layout 1:1.
//
// Leaf package: imports nothing but sync/atomic, so every package can
// depend on it without cycles.
package gconf

import "sync/atomic"

// T — C: gconf_t. Field names map snake_case → CamelCase; debug toggles
// are atomic.Bool (C plain int, but read/written across goroutines
// without a mutex in both implementations).
type T struct {
	// --- scalars (C ints/strings), set by parse_opts / platform init ---
	Binary                string   // C: binary — argv[0] (linux_main.c:145)
	OSInfo                string   // C: os_info — posix_init (uname/linux_get_dist)
	DeviceID              string   // C: device_id[64] (main.h:323)
	SystemName            string   // C: system_name[64]
	RunningInstance       [16]byte // C: running_instance (main.h:331)
	Concurrency           int      // C: concurrency (main.h:210)
	Skin                  string   // C: skin — --skin <value>
	InitialURL            string   // C: initial_url — first positional arg
	InitialView           string   // C: initial_view — -v <value>
	Platform              string   // --platform <x11|wayland> — extension, no C counterpart (replaces --ui)
	DevPlugins            []string // C: devplugins strvec — -p <value>
	CachePath             string   // C: cache_path — --cache <value>
	PersistentPath        string   // C: persistent_path — --persistent <value>
	PluginRepo            string   // C: plugin_repo — --plugin-repo
	UpgradePath           string   // C: upgrade_path — --upgrade-path
	LoadEcmascript        string   // C: load_ecmascript — --ecmascript
	ProxyHost             string   // C: --proxy <host:port>
	ProxyPort             int      // C: --proxy <host:port>
	ShellFD               int      // --showtime-shell-fd <fd>
	TraceLevel            int      // C: trace_level — -d
	ShowUsageEvents       int      // C: show_usage_events — --show-usage-events
	ConvertPointerToTouch int      // C: convert_pointer_to_touch — --pointer-is-touch
	IgnoreThePrefix       int      // C: ignore_the_prefix
	TimeFormatSystem      int      // C: time_format_system
	LibavLog              bool     // --libav-log
	DebugGLW              bool     // C: debug_glw — --debug-glw
	NoUI                  bool     // --no-ui
	Fullscreen            bool     // C: fullscreen — --fullscreen
	Syslog                bool     // C: trace_to_syslog — --syslog
	DisableUPnP           bool     // --disable-upnp
	DisableSD             bool     // --disable-sd
	DisableUpgrades       bool     // --disable-upgrades
	ListenOnStdin         bool     // C: listen_on_stdin — --stdin
	CanStandby            bool     // C: can_standby — --with-standby
	CanPoweroff           bool     // C: can_poweroff — --with-poweroff
	CanLogout             bool     // C: can_logout — --with-logout
	CanOpenShell          bool     // C: can_open_shell — --with-openshell
	CanNotExit            bool     // C: can_not_exit — --without-exit
	CanRestart            bool     // C: can_restart — --with-restart
	// C: gconf.max_video_buffer_size (media_settings.c reads it for
	// SETTING_RANGE; rpi_main.c clamps it to 64 on 256MB boards)
	MaxVideoBufferSize int
	// C: gconf.setting_av_volume / gconf.setting_av_sync — assigned by
	// audio_init, read by media_settings.c prop bindings. `any` because
	// gconf is a leaf (settingscore would create a cycle).
	SettingAVVolume     any
	SettingAVSync       any
	BypassEcmascriptACL bool // C: bypass_ecmascript_acl — --bypass-ecmascript-acl
	EnableTouchDebug    bool // C: enable_touch_debug (plain int; glw reads as bool)

	// --- debug toggles (C: int enable_*_debug; Go: atomic.Bool) ---
	EnableEcmascriptDebug atomic.Bool // C: enable_ecmascript_debug
	EnableUpnpDebug       atomic.Bool // C: enable_upnp_debug
	EnableCecDebug        atomic.Bool // C: enable_cec_debug
	EnableFTPServerDebug  atomic.Bool // C: enable_ftp_server_debug
	EnableNavAlwaysClose  atomic.Bool // C: enable_nav_always_close
	EnableDetailedAvdiff  atomic.Bool // C: enable_detailed_avdiff
	EnableMediaCodecDebug atomic.Bool // C: enable_MediaCodec_debug
	EnableSettingsDebug   atomic.Bool // C: enable_settings_debug
	EnableSMBDebug        atomic.Bool // C: enable_smb_debug
	EnableIndexerDebug    atomic.Bool // C: enable_indexer_debug
	EnableInputEventDebug atomic.Bool // C: enable_input_event_debug
	EnableKvstoreDebug    atomic.Bool // C: enable_kvstore_debug
	EnableIcecastDebug    atomic.Bool // C: enable_icecast_debug
	EnableHLSDebug        atomic.Bool // C: enable_hls_debug
	EnableThreadDebug     atomic.Bool // C: enable_thread_debug
	EnableImageDebug      atomic.Bool // C: enable_image_debug
	EnableIndexer         atomic.Bool // C: enable_indexer
	EnableFaScannerDebug  atomic.Bool // C: enable_fa_scanner_debug
	EnableHTTPDebug       atomic.Bool // C: enable_http_debug
	EnableFTPClientDebug  atomic.Bool // C: enable_ftp_client_debug

	// fileaccess settings (main.h:296-300)
	FAAllowDelete          atomic.Bool // C: fa_allow_delete
	FAKVStoreAsXattr       atomic.Bool // C: fa_kvstore_as_xattr
	ShowFilenameExtensions atomic.Bool // C: show_filename_extensions
	FABrowseArchives       atomic.Bool // C: fa_browse_archives
	DisableHTTPReuse       atomic.Bool // C: disable_http_reuse

	// Go extension (no C counterpart): route https:// open/stat through
	// net/http → HTTP/2 via ALPN (fa_http2.go).
	FAHTTP2 atomic.Bool
	// Go extension: attempt QUIC/HTTP/3 for https hosts on the modern
	// path (per-host negative cache falls back to TCP permanently).
	FAHTTP3 atomic.Bool

	// C: ENABLE_BITTORRENT section
	EnableTorrentDebug               atomic.Bool // C: enable_torrent_debug
	EnableTorrentTrackerDebug        atomic.Bool // C: enable_torrent_tracker_debug
	EnableTorrentPeerConnectionDebug atomic.Bool // C: enable_torrent_peer_connection_debug
	EnableTorrentPeerDownloadDebug   atomic.Bool // C: enable_torrent_peer_download_debug
	EnableTorrentPeerUploadDebug     atomic.Bool // C: enable_torrent_peer_upload_debug
	EnableTorrentDiskIODebug         atomic.Bool // C: enable_torrent_diskio_debug
}

// New — C: gconf_t gconf (the process-wide config owned by main and
// injected into every consumer).
func New() *T {
	return &T{
		Concurrency: 1,           // C: floors at 1 via get_system_concurrency; default when unwired
		SystemName:  "movian-go", // C: default APPNAME until the "System name" setting lands
	}
}
