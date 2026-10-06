// Canonical port of the settings tail of fileaccess_init
// (fileaccess.c:1441-1469): creates the "general:filebrowse" directory
// settings that write into the gconf.fa_* / gconf.show_filename_extensions
// globals. Lives in its own package because it needs both
// fileaccess/core (gconf vars) and settings/core (SettingCreate), and
// fileaccess/core cannot import settings/core (cycle).

package fileaccsettings

import (
	settingscore "github.com/czz/movian-go/internal/settings"
)

// faBool normalizes a bool-setting value (Go bool, or int like C).
func faBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

// FileaccessSettingsStart — C: fileaccess_init lines 1441-1469.
// Called from fileaccess_init via FASettingsStartHook.
func FileaccessSettingsStart(sm *settingscore.SettingsManager) {
	dir := sm.SettingGetDir("general:filebrowse")

	// C: SETTING_WRITE_BOOL(&gconf.fa_allow_delete)
	sm.SettingCreate(settingscore.SettingBool, dir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle,
		sm.P("Enable file deletion from item menu"),
		settingscore.SettingTagCallback,
		func(_, v any) {
			sm.Gconf().FAAllowDelete.Store(faBool(v))
		}, nil,
		settingscore.SettingTagStore, "faconf", "delete")

	// C: SETTING_WRITE_BOOL(&gconf.fa_kvstore_as_xattr), SETTING_VALUE(1)
	sm.SettingCreate(settingscore.SettingBool, dir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle,
		sm.P("Store per-file settings in filesystem"),
		settingscore.SettingTagCallback,
		func(_, v any) {
			sm.Gconf().FAKVStoreAsXattr.Store(faBool(v))
		}, nil,
		settingscore.SettingTagValue, 1,
		settingscore.SettingTagStore, "faconf", "enablexattr")

	// C: SETTING_WRITE_BOOL(&gconf.show_filename_extensions)
	sm.SettingCreate(settingscore.SettingBool, dir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle,
		sm.P("Show filename extensions"),
		settingscore.SettingTagCallback,
		func(_, v any) {
			sm.Gconf().ShowFilenameExtensions.Store(faBool(v))
		}, nil,
		settingscore.SettingTagStore, "faconf", "filenameextensions")

	// C: SETTING_WRITE_BOOL(&gconf.fa_browse_archives), SETTING_VALUE(1)
	sm.SettingCreate(settingscore.SettingBool, dir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle,
		sm.P("Browse archives as folders"),
		settingscore.SettingTagCallback,
		func(_, v any) {
			sm.Gconf().FABrowseArchives.Store(faBool(v))
		}, nil,
		settingscore.SettingTagValue, 1,
		settingscore.SettingTagStore, "faconf", "browsearchives")

	// Go extension: https:// via net/http → HTTP/2 (fa_http2.go) —
	// dedicated "HTTP" sub-page inside the existing "Network settings"
	// section (gconf.settings_network).
	httpDir := sm.AddDir(sm.Network(), sm.P("HTTP"), "", "",
		sm.P("HTTP protocol settings"), "settings:http")
	sm.SettingCreate(settingscore.SettingBool, httpDir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle,
		sm.P("Use HTTP/2 for HTTPS"),
		settingscore.SettingTagCallback,
		func(_, v any) {
			sm.Gconf().FAHTTP2.Store(faBool(v))
		}, nil,
		settingscore.SettingTagValue, 1,
		settingscore.SettingTagStore, "faconf", "http2")

	// Go extension: QUIC/HTTP3 attempt for https hosts (fa_http2.go) —
	// only meaningful while the HTTP/2 path is enabled.
	sm.SettingCreate(settingscore.SettingBool, httpDir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle,
		sm.P("Use HTTP/3 (QUIC) for HTTPS"),
		settingscore.SettingTagCallback,
		func(_, v any) {
			sm.Gconf().FAHTTP3.Store(faBool(v))
		}, nil,
		settingscore.SettingTagValue, 1,
		settingscore.SettingTagStore, "faconf", "http3")
}
