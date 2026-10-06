// settings_yt.go — the YouTube settings group. Lives as a top-level
// "YouTube" entry in the settings tree (settings:youtube) and persists
// through the canonical kvstore path (SETTING_STORE → DomainSetting),
// exactly like every other settings group and JS plugin store.
package youtube

import (
	"strconv"

	settings "github.com/czz/movian-go/internal/settings"
)

// updateStatus refreshes the settings info row after an OAuth state
// change. No-op before setupSettings runs.
func (s *System) updateStatus() {
	if s.statusProp != nil {
		s.statusProp.SetString(s.oauthStatus())
	}
}

func (s *System) setMaxHeight(v string) {
	h := 0
	if v != "" && v != "auto" {
		h, _ = strconv.Atoi(v)
	}
	s.prefMu.Lock()
	defer s.prefMu.Unlock()
	s.prefMaxHeight = h
}

func (s *System) maxHeight() int {
	s.prefMu.RLock()
	defer s.prefMu.RUnlock()
	return s.prefMaxHeight
}

func (s *System) searchOn() bool {
	s.prefMu.RLock()
	defer s.prefMu.RUnlock()
	return s.prefSearchOn != 0
}

// setupSettings creates the "YouTube" settings directory at the top
// level of the settings model (next to BitTorrent et al.) and binds
// each pref to the canonical store domain "youtube".
func (s *System) setupSettings(sm *settings.SettingsManager) {
	if sm == nil {
		return
	}
	dir := sm.AddDir(nil, sm.P("YouTube"),
		"", "", sm.P("YouTube feed, search and playback options"),
		"settings:youtube")

	// ── Sign-in (OAuth device flow) ────────────────────────────────
	// Live status line — updated by oauth.go state transitions.
	s.statusProp = s.propMgr.CreateRootEx("", false)
	s.statusProp.SetString(s.oauthStatus())
	sm.CreateInfo(dir, "", s.statusProp)

	sm.CreateActionProp(dir, "Sign in (pair with TV code)", "action",
		func(_, _ any) { go s.startDeviceFlow() }, nil, 0)

	sm.CreateActionProp(dir, "Sign out", "action",
		func(_, _ any) { s.signOut() }, nil, 0)

	sm.CreateSeparatorProp(dir, sm.P("Playback"))

	sm.SettingCreate(settings.SettingMultiOpt, dir, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, sm.P("Maximum video quality"),
		settings.SettingTagStore, "youtube", "maxquality",
		settings.SettingTagOption, "auto", sm.P("Auto (best available)"),
		settings.SettingTagOptionCStr, "1080", "1080p",
		settings.SettingTagOptionCStr, "720", "720p",
		settings.SettingTagOptionCStr, "480", "480p",
		settings.SettingTagOptionCStr, "360", "360p",
		settings.SettingTagCallback, func(opaque, value any) {
			if str, ok := value.(string); ok {
				s.setMaxHeight(str)
			}
		}, nil,
		0)

	sm.SettingCreate(settings.SettingBool, dir, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, sm.P("Show YouTube results in global search"),
		settings.SettingTagWriteInt, &s.prefSearchOn,
		settings.SettingTagValue, 1,
		settings.SettingTagStore, "youtube", "search",
		0)

	sm.SettingCreate(settings.SettingString, dir, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, sm.P("Interface language (hl)"),
		settings.SettingTagValue, "",
		settings.SettingTagCallback, func(opaque, value any) {
			if str, ok := value.(string); ok {
				s.prefMu.Lock()
				s.prefHL = str
				s.prefMu.Unlock()
			}
		}, nil,
		settings.SettingTagStore, "youtube", "hl",
		0)

	sm.SettingCreate(settings.SettingString, dir, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, sm.P("Content region (gl)"),
		settings.SettingTagValue, "",
		settings.SettingTagCallback, func(opaque, value any) {
			if str, ok := value.(string); ok {
				s.prefMu.Lock()
				s.prefGL = str
				s.prefMu.Unlock()
			}
		}, nil,
		settings.SettingTagStore, "youtube", "gl",
		0)
}
