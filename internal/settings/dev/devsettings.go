// Canonical port of init_dev_settings (src/settings.c:~1497-1667) plus
// its helpers add_dev_bool / set_netlog.
//
// This is a separate package because the dev bools write gconf fields
// owned by packages that themselves import settings/core (upnp,
// ecmascript, scanner, smb, ...). A direct reference from settings/core
// would create an import cycle — so the hook is assigned here and
// invoked from SettingsManager.Start.
//
// Platform gates follow C's configure flags for a Linux build:
//
//	ENABLE_UPGRADE   → binreplace, omnigrade
//	!NDEBUG          → disableanalytics (Go builds are not NDEBUG-stripped;
//	                   the option exists on dev URIs only, like C)
//	ENABLE_NETLOG    → netlogdest
//	CONFIG_LIBCEC    → cecdebug (build tag, like C)
//	ENABLE_BITTORRENT→ bt* (6 bools)
//	__ANDROID__      → touchevents, androidMediaCodec (runtime.GOOS gate)
//	PS3 / STOS-only entries are skipped like C skips them here.
package dev

import (
	"net"
	"runtime"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/ipc"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
)

// Deps — C: the gconf fields init_dev_settings binds by pointer.
// Owned by the composition root; the dev bools write through them.
type Deps struct {
	BinReplace *int               // C: gconf.enable_bin_replace
	Omnigrade  *int               // C: gconf.enable_omnigrade
	UsageCfg   *usage.Config      // C: gconf.disable_analytics
	TS         *trace.TraceSystem // C: trace() global — set_netlog writes here
	CecDebug   func(bool)         // C: gconf.enable_cec_debug writer (ipc)
	Gconf      *gconf.T           // C: gconf_t — dev bools write atomics
}

// addDevBool — C: add_dev_bool (settings.c:~1495). Creates a SETTING_BOOL
// under gconf.settings_dev, stored as ("dev", id), writing *val.
func addDevBool(sm *settings.SettingsManager, dev *propcore.Prop,
	title, id string, val *int) {
	sm.SettingCreate(settings.SettingBool, dev,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitleCStr, title,
		settings.SettingTagValue, *val,
		settings.SettingTagWriteInt, val,
		settings.SettingTagStore, "dev", id,
		0)
}

// addDevBoolCallback is add_dev_bool for flags that live behind a setter
// rather than a plain int (C writes gconf directly; here the callback
// reaches the same consumers). Same SETTING_BOOL + SETTINGS_INITIAL_UPDATE
// + SETTING_STORE semantics.
func addDevBoolCallback(sm *settings.SettingsManager, dev *propcore.Prop,
	title, id string, cur int, fn func(enabled bool)) {
	sm.SettingCreate(settings.SettingBool, dev,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitleCStr, title,
		settings.SettingTagValue, cur,
		settings.SettingTagCallback,
		func(opaque, value any) { fn(devBoolToBool(value)) }, nil,
		settings.SettingTagStore, "dev", id,
		0)
}

// devBoolToBool normalizes a bool-setting value (Go bool, or int like C).
func devBoolToBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

// setNetlog — C: set_netlog (settings.c:~1511). Parses "ipv4[:port]"
// (default port 4000) into gconf.log_server_ipv4/log_server_port;
// an unparsable or empty value disables the log server.
func setNetlog(opaque, value any) {
	ts, _ := opaque.(*trace.TraceSystem)
	str, _ := value.(string)
	if str == "" {
		// C: if(str == NULL) { gconf.log_server_ipv4 = 0; return; }
		ts.SetNetLogConfig(false, "", 0)
		return
	}

	host, port := str, 4000
	if before, after, ok := strings.Cut(str, ":"); ok {
		host = before
		if p, err := strconv.Atoi(after); err == nil {
			port = p
		}
	}

	// C: inet_pton(AF_INET, msg, &addr) != 1 → gconf.log_server_ipv4 = 0
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		ts.SetNetLogConfig(false, "", 0)
		return
	}
	ts.SetNetLogConfig(true, host, port)
}

// initDevSettings — C: init_dev_settings (settings.c:~1497). The dev dir
// and its info leaf are created by SettingsManager.Start (C does both in
// init_dev_settings, but the dir creation lives in the settings_init
// port so the hook only adds the entries).
// Start — C: init_dev_settings (settings.c:~1497). Called via
// Called via the SettingsManager seam wired by the composition root.
func Start(sm *settings.SettingsManager, dev *propcore.Prop, deps *Deps) {
	// C: #if ENABLE_UPGRADE
	addDevBool(sm, dev, "Enable binreplace", "binreplace",
		deps.BinReplace)
	addDevBool(sm, dev, "Enable omnigrade", "omnigrade",
		deps.Omnigrade)

	// C: #ifndef NDEBUG
	if deps.UsageCfg != nil {
		addDevBool(sm, dev, "Disable analytics", "disableanalytics",
			&deps.UsageCfg.Disabled)
	}

	addDevBoolCallback(sm, dev, "Always close pages when pressing back",
		"navalwaysclose", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableNavAlwaysClose.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Disable HTTP connection reuse",
		"nohttpreuse", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.DisableHTTPReuse.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Enable indexer option",
		"enable_indexer", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableIndexer.Store(b)
			}
		})

	// C: if(gconf.arch_dev_opts) gconf.arch_dev_opts(&add_dev_bool);
	// arch_dev_opts is only set on PS3 (main.h:333, ps3_main.c:652) —
	// nothing to add on Linux.

	// C: #if ENABLE_NETLOG — SETTING_STRING + SETTING_CALLBACK(set_netlog)
	sm.SettingCreate(settings.SettingString, dev,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitleCStr, "Network log destination",
		settings.SettingTagCallback, setNetlog, deps.TS,
		settings.SettingTagStore, "dev", "netlogdest",
		0)

	// ---------- debug filtering
	// C: setting_add_cstr(gconf.settings_dev, "Debug log filtering",
	//   "separator", 0)
	sm.CreateSeparatorProp(dev, sm.P("Debug log filtering"))

	addDevBoolCallback(sm, dev, "Debug all HTTP requests",
		"httpdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableHTTPDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug various ecmascript engine things",
		"ecmascriptdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableEcmascriptDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Log AV-diff stats",
		"detailedavdiff", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableDetailedAvdiff.Store(b)
			}
		})

	// C: PS3-only "memdebug" skipped like C skips it on Linux.

	addDevBoolCallback(sm, dev, "Debug HLS",
		"hlsdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableHLSDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug FTP Client",
		"ftpdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableFTPClientDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug FTP Server",
		"ftpserverdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableFTPServerDebug.Store(b)
			}
		})

	// C: #if CONFIG_LIBCEC
	if ipc.LibcecEnabled {
		addDevBoolCallback(sm, dev, "Debug CEC", "cecdebug", 0, deps.CecDebug)
	}

	addDevBoolCallback(sm, dev, "Debug directory listings",
		"fascannerdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableFaScannerDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug library indexer",
		"indexerdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableIndexerDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug SMB/CIFS (Windows File Sharing)",
		"smbdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableSMBDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug read/writes to URL key/value store",
		"kvstoredebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableKvstoreDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug icecast streaming",
		"icecastdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableIcecastDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug image loading and decoding",
		"imagedebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableImageDebug.Store(b)
			}
		})

	// C: "Debug metadata lookups" → gconf.enable_metadata_debug.
	// The Go flag lives on TraceSystem — the callback writes it.
	addDevBoolCallback(sm, dev, "Debug metadata lookups",
		"metadatadebug", 0, deps.TS.SetEnableMetadataDebug)

	addDevBoolCallback(sm, dev, "Debug settings store/load from disk",
		"settingsdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableSettingsDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug threads",
		"threadsdebug", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableThreadDebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug UPNP",
		"upnp", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableUpnpDebug.Store(b)
			}
		})

	// C: #if ENABLE_BITTORRENT
	addDevBoolCallback(sm, dev, "Debug Bittorrent general events",
		"bt", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableTorrentDebug.Store(b)
			}
		})
	addDevBoolCallback(sm, dev, "Debug Bittorrent tracker communication",
		"bttracker", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableTorrentTrackerDebug.Store(b)
			}
		})
	addDevBoolCallback(sm, dev, "Debug Bittorrent peer connections",
		"btpeercon", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableTorrentPeerConnectionDebug.Store(b)
			}
		})
	addDevBoolCallback(sm, dev, "Debug Bittorrent peer downloading",
		"btpeerdl", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableTorrentPeerDownloadDebug.Store(b)
			}
		})
	addDevBoolCallback(sm, dev, "Debug Bittorrent peer uploading",
		"btpeerul", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableTorrentPeerUploadDebug.Store(b)
			}
		})
	addDevBoolCallback(sm, dev, "Debug Bittorrent disk I/O",
		"btdiskio", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableTorrentDiskIODebug.Store(b)
			}
		})

	addDevBoolCallback(sm, dev, "Debug input events",
		"inputevents", 0, func(b bool) {
			if deps.Gconf != nil {
				deps.Gconf.EnableInputEventDebug.Store(b)
			}
		})

	// C: __ANDROID__ — touchevents / androidMediaCodec
	// (settings.c:1664-1671). Gate mirrors the #ifdef.
	if runtime.GOOS == "android" && deps.Gconf != nil {
		addDevBoolCallback(sm, dev, "Debug touch events",
			"touchevents", 0, func(b bool) {
				deps.Gconf.EnableTouchDebug = b
			})
		addDevBoolCallback(sm, dev, "Debug MediaCodec",
			"androidMediaCodec", 0, func(b bool) {
				deps.Gconf.EnableMediaCodecDebug.Store(b)
			})
	}
}
