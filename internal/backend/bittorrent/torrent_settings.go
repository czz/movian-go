package bittorrent

// Canonical port of src/backend/bittorrent/torrent_settings.c.

import (
	"fmt"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
)

// C: static int allow_update — now a BtGlobal field.

// btDeps.settingsManager — C's global settings context. Wired via
// SetSettingsManager before INIT_GROUP_ASYNCIO fires (mirrors the
// blobcache.SetSettingsMgr pattern; C uses the global settings_* API).

// btDeps.propManager — C's global prop context for prop_create_root.

// SetSettingsManager — injects the settings/prop managers used by
// torrent_settings_init. Called from cmd/movian-go after settings_init().
func (btg *BtGlobal) SetSettingsManager(sm *settings.SettingsManager, pm *propcore.PropManager) {
	btg.settingsManager = sm
	btg.propManager = pm
}

// setTorrentCachePath — C: set_torrent_cache_path
// (torrent_settings.c:33-39)
func (btg *BtGlobal) setTorrentCachePath(opaque, value any) {
	str, _ := value.(string)
	misc.RstrRelease(btg.cachePath)
	btg.cachePath = misc.RstrAllocStr(str)
	if btg.torrentSettingsAllowUpdate != 0 {
		btg.torrentDiskioScan(false)
	}
}

// setTorrentFreePercentage — C: set_torrent_free_percentage
// (torrent_settings.c:45-50)
func (btg *BtGlobal) setTorrentFreePercentage(opaque, value any) {
	v, _ := value.(int)
	btg.freeSpacePercent = v
	if btg.torrentSettingsAllowUpdate != 0 {
		btg.torrentDiskioScan(false)
	}
}

// setTorrentUploadSpeed — C: set_torrent_upload_speed
// (torrent_settings.c:53-57)
func (btg *BtGlobal) setTorrentUploadSpeed(opaque, value any) {
	v, _ := value.(int)
	// How many bytes we should refill send limiter for every tenth second
	btg.maxSendSpeed = v * (1000000 / 8 / 10)
}

// torrentSettingsStart — C: torrent_settings_init
// (torrent_settings.c:61-129)
func (btg *BtGlobal) torrentSettingsStart() {
	sm := btg.settingsManager
	if sm == nil {
		return
	}

	dir := sm.SettingGetDir("general:filebrowse")
	s := sm.AddDir(dir, sm.P("BitTorrent"),
		"", "", nil, "settings:bittorrent")

	defpath := fmt.Sprintf("%s/bittorrentcache", arch.GetCachePath())
	freespace := 10
	// C: #ifdef STOS freespace = 75 (torrent_settings.c:77-79)
	if mgosTorrent {
		freespace = 75
	}

	sm.SettingCreate(settings.SettingBool, s, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, sm.P("Enable bittorrent"),
		settings.SettingTagMutex, &btg.mu,
		settings.SettingTagWriteInt, &btg.enabled,
		settings.SettingTagValue, 1,
		settings.SettingTagStore, "bittorrent", "enable",
		0)

	sm.SettingCreate(settings.SettingInt, s, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, sm.P("Max upload speed"),
		settings.SettingTagMutex, &btg.mu,
		settings.SettingTagCallback, btg.setTorrentUploadSpeed, nil,
		settings.SettingTagValue, 5,
		settings.SettingTagRange, 0, 100,
		settings.SettingTagUnitCStr, "Mbit/s",
		settings.SettingTagStore, "bittorrent", "uploadspeed",
		0)

	sm.SettingCreate(settings.SettingInt, s, settings.SettingsInitialUpdate,
		settings.SettingTagTitle,
		sm.P("Max usage of free space for caching torrents"),
		settings.SettingTagMutex, &btg.mu,
		settings.SettingTagCallback, btg.setTorrentFreePercentage, nil,
		settings.SettingTagValue, freespace,
		settings.SettingTagRange, 1, 90,
		settings.SettingTagUnitCStr, "%",
		settings.SettingTagStore, "bittorrent", "freepercentage",
		0)

	sm.SettingCreate(settings.SettingString, s,
		settings.SettingsInitialUpdate|settings.SettingsDir,
		settings.SettingTagTitle, sm.P("Torrent cache path"),
		settings.SettingTagMutex, &btg.mu,
		settings.SettingTagCallback, btg.setTorrentCachePath, nil,
		settings.SettingTagValue, defpath,
		settings.SettingTagStore, "bittorrent", "path",
		0)

	sm.SettingCreate(settings.SettingAction, s, 0,
		settings.SettingTagTitle, sm.P("Clear cache"),
		settings.SettingTagMutex, &btg.mu,
		settings.SettingTagCallback,
		func(opaque, value any) { btg.torrentDiskioCacheClear() }, nil,
		0)

	sm.CreateSeparatorProp(s, sm.P("Status"))

	btg.torrentStatus = btg.propManager.CreateRoot("")
	sm.CreateInfo(s, "", btg.torrentStatus)

	btg.diskStatus = btg.propManager.CreateRoot("")
	sm.CreateInfo(s, "", btg.diskStatus)

	btg.torrentSettingsAllowUpdate = 1
	btg.torrentDiskioScan(false)
}
