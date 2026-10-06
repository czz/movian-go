// Canonical port of src/media/media_settings.c — per-URL media settings
// (mp_settings_init / mp_settings_clear) plus the canonical
// video_settings_init (src/video/video_settings.c), which lives here
// because pkg/video cannot import settings/core (cycle via glw/view).
//
// C stores the seven setting lists on media_pipe_t; Go keeps them in a
// side map keyed by *MediaPipe because media/core cannot import
// settings/core (same cycle). Registered via MediaSystem.MpSettingsSetup
// / MpSettingsClear (the ENABLE_MEDIA_SETTINGS seams).

package settings

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/czz/movian-go/internal/gconf"
	mediacore "github.com/czz/movian-go/internal/media/core"
	medialibav "github.com/czz/movian-go/internal/media/libav"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/trace"
	videopkg "github.com/czz/movian-go/internal/video"
)

// maxUserAudioGain — C: MAX_USER_AUDIO_GAIN (media_settings.c:30)
const maxUserAudioGain = 12

// Gconf seams — C: gconf.* globals consumed by media_settings.c /
// video_settings.c. Populated at init.
func (sys *System) gconfSettingAVVolume() *settingscore.Setting {
	if sys.gc == nil {
		return nil
	}
	s, _ := sys.gc.SettingAVVolume.(*settingscore.Setting)
	return s
}

func (sys *System) gconfSettingAVSync() *settingscore.Setting {
	if sys.gc == nil {
		return nil
	}
	s, _ := sys.gc.SettingAVSync.(*settingscore.Setting)
	return s
}

// C: gconf.can_standby / gconf.max_video_buffer_size /
// gconf.setting_av_volume / gconf.setting_av_sync now live on
// gconf.T (the gconf_t counterpart) — gconf is a leaf package.

// msSettingsState — C: settings mutex + settings manager seam +
// mp->mp_settings_* lists (media.h:335-343) kept as a per-pipe map.
// System — C: media_settings.c statics (settings mutex + settings
// manager seam + the mp->mp_settings_* lists, kept as a per-pipe map).
type System struct {
	mu       sync.Mutex
	sm       *settingscore.SettingsManager
	mpGroups map[*mediacore.MediaPipe]*mpSettingGroups
	gc       *gconf.T // C: gconf_t
}

// NewSystem — allocates the media-settings subsystem.
func NewSystem(gc *gconf.T) *System {
	return &System{
		mpGroups: map[*mediacore.MediaPipe]*mpSettingGroups{},
		gc:       gc,
	}
}

// mpSettingGroups — C: the seven setting_list heads + mp_vol_setting on
// media_pipe_t (media.h:335-345).
type mpSettingGroups struct {
	video       []*settingscore.Setting // C: mp_settings_video
	audio       []*settingscore.Setting // C: mp_settings_audio
	subtitle    []*settingscore.Setting // C: mp_settings_subtitle
	videoDir    []*settingscore.Setting // C: mp_settings_video_dir
	audioDir    []*settingscore.Setting // C: mp_settings_audio_dir
	subtitleDir []*settingscore.Setting // C: mp_settings_subtitle_dir
	other       []*settingscore.Setting // C: mp_settings_other
}

func (sys *System) groupsFor(mp *mediacore.MediaPipe) *mpSettingGroups {
	g := sys.mpGroups[mp]
	if g == nil {
		g = &mpSettingGroups{}
		sys.mpGroups[mp] = g
	}
	return g
}

// Start — wires MediaSystem.MpSettingsSetup / MpSettingsClear with the global
// SettingsManager (C: the ENABLE_MEDIA_SETTINGS build).
func (sys *System) Start(sm *settingscore.SettingsManager, ms *mediacore.MediaSystem) {
	sys.mu.Lock()
	sys.sm = sm
	sys.mu.Unlock()
	if sm == nil {
		return
	}
	if ms != nil {
		ms.MpSettingsSetup = sys.mpSettingsSetup
		ms.MpSettingsClear = sys.MpSettingsClear
	}
}

// updateAVDelta — C: update_av_delta (media_settings.c:38-43)
func updateAVDelta(opaque any, v any) {
	mp, _ := opaque.(*mediacore.MediaPipe)
	vi, _ := v.(int)
	mp.AVDelta = int64(vi) * 1000
	mp.FAM.TraceSystem().Trace(trace.TRACE_DEBUG, "AVSYNC", "Set to %d ms", vi)
}

// updateSVDelta — C: update_sv_delta (media_settings.c:50-55)
func updateSVDelta(opaque any, v any) {
	mp, _ := opaque.(*mediacore.MediaPipe)
	vi, _ := v.(int)
	mp.SVDelta = int64(vi) * 1000
	mp.FAM.TraceSystem().Trace(trace.TRACE_DEBUG, "SVSYNC", "Set to %ds", vi)
}

// updateAudioVolumeUser — C: update_audio_volume_user (media_settings.c:61-66)
func updateAudioVolumeUser(opaque any, v any) {
	mp, _ := opaque.(*mediacore.MediaPipe)
	vi, _ := v.(int)
	if vi > maxUserAudioGain {
		vi = maxUserAudioGain
	}
	mp.VolUser = vi
	mediacore.MpSendVolumeUpdateLocked(mp)
}

// MpSettingsClear — C: mp_settings_clear (media_settings.c:72-86)
func (sys *System) MpSettingsClear(mp *mediacore.MediaPipe) {
	sys.mu.Lock()
	sm := sys.sm
	g := sys.mpGroups[mp]
	delete(sys.mpGroups, mp)
	sys.mu.Unlock()

	if g == nil {
		return
	}
	mp.VolSetting = nil // C: mp->mp_vol_setting = NULL
	if sm == nil {
		return
	}
	sm.GroupDestroy(&g.video)
	sm.GroupDestroy(&g.audio)
	sm.GroupDestroy(&g.subtitle)
	sm.GroupDestroy(&g.videoDir)
	sm.GroupDestroy(&g.audioDir)
	sm.GroupDestroy(&g.subtitleDir)
	sm.GroupDestroy(&g.other)
}

// setting group actions — C: set/clr_*_defaults (media_settings.c:90-169).
// Each closure captures mp; the group list is looked up fresh at call time.

func (sys *System) setGlobalDefaults(mp *mediacore.MediaPipe,
	get func(*mpSettingGroups) *[]*settingscore.Setting) func(any, any) {
	return func(any, any) {
		sys.mu.Lock()
		sm := sys.sm
		g := sys.mpGroups[mp]
		sys.mu.Unlock()
		if sm == nil || g == nil {
			return
		}
		list := get(g)
		sm.SettingGroupPushToAncestor(*list, "global")
		settingscore.GroupReset(*list)
	}
}

func (sys *System) setDirectoryDefaults(mp *mediacore.MediaPipe,
	get func(*mpSettingGroups) *[]*settingscore.Setting) func(any, any) {
	return func(any, any) {
		sys.mu.Lock()
		sm := sys.sm
		g := sys.mpGroups[mp]
		sys.mu.Unlock()
		if sm == nil || g == nil {
			return
		}
		list := get(g)
		sm.SettingGroupPushToAncestor(*list, "directory")
		settingscore.GroupReset(*list)
	}
}

func (sys *System) clrDirectoryDefaults(mp *mediacore.MediaPipe,
	get func(*mpSettingGroups) *[]*settingscore.Setting) func(any, any) {
	return func(any, any) {
		g := sys.mpGroups[mp]
		if g == nil {
			return
		}
		settingscore.GroupReset(*get(g))
	}
}

// makeDirSetting — C: make_dir_setting (media_settings.c:173-192) —
// creates a directory-scope setting inheriting from the global one.
func (sys *System) makeDirSetting(sm *settingscore.SettingsManager, settingType int,
	id string, group *[]*settingscore.Setting, dirURL string,
	parent *settingscore.Setting, mp *mediacore.MediaPipe) *settingscore.Setting {
	if dirURL == "" {
		return parent
	}
	return sm.SettingCreate(settingType, nil, settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagKVStore, dirURL, id,
		settingscore.SettingTagGroup, group,
		settingscore.SettingTagInherit, parent,
		settingscore.SettingTagValueOrigin, "directory")
}

// mpSettingsSetup — C: mp_settings_init (media_settings.c:196-588).
// Runs under mp_mutex (called from MpSetURL / MpRelease context).
func (sys *System) mpSettingsSetup(mp *mediacore.MediaPipe, url, dirURL, parentTitle string) {
	sys.mu.Lock()
	sm := sys.sm
	sys.mu.Unlock()
	if sm == nil || mp == nil {
		return
	}

	sys.mpSettingsClearLocked(mp)

	if url == "" || mp.Flags&mediacore.MPVideo == 0 {
		return
	}
	if parentTitle == "" {
		dirURL = ""
	}

	mp.FAM.TraceSystem().Trace(trace.TRACE_DEBUG, "media",
		"Settings initialized for URL %s in folder: %s [%s]",
		url, orUnset(parentTitle), orUnset(dirURL))

	var setDirTitle, clrDirTitle string
	if dirURL != "" {
		// C: _("Save as defaults for folder '%s'") etc.
		setDirTitle = fmt.Sprintf("Save as defaults for folder '%s'", parentTitle)
		clrDirTitle = fmt.Sprintf("Reset defaults for folder '%s'", parentTitle)
	}

	c := mp.PropCtrl
	g := sys.groupsFor(mp)
	vs := mp.Sys.VS
	if vs == nil {
		// C: video_settings is a zero-initialized global — an unwired
		// port reads zeros/nil setting handles instead of panicking.
		vs = &videopkg.VideoSettings{}
	}
	// C: subtitles.c statics — the System lives on the BackendSystem
	// reachable through mp.Sys.Owner (backend/core import would cycle).
	var sub *subtitles.SubtitleSettings
	if h, ok := mp.Sys.Owner.(interface {
		SubSys() *subtitles.System
	}); ok && h.SubSys() != nil {
		sub = h.SubSys().SubSettings()
	}
	if sub == nil {
		sub = &subtitles.SubtitleSettings{}
	}

	newChild := func(name string) *propcore.Prop {
		if c == nil || c.Manager() == nil {
			return nil
		}
		return c.Manager().CreateEx(c, name, nil, false, true)
	}

	// --- Video -------------------------------------------------

	p := sys.makeDirSetting(sm, settingscore.SettingInt, "vzoom",
		&g.videoDir, dirURL, vs.VzoomSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingVideoRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Video zoom"),
		settingscore.SettingTagRange, 50, 200,
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagWriteProp, newChild("vzoom"),
		settingscore.SettingTagKVStore, url, "vzoom",
		settingscore.SettingTagGroup, &g.video,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "panhorizontal",
		&g.videoDir, dirURL, vs.PanHorizontalSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingVideoRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Horizontal pan"),
		settingscore.SettingTagRange, -100, 100,
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagWriteProp, newChild("panhorizontal"),
		settingscore.SettingTagKVStore, url, "panhorizontal",
		settingscore.SettingTagGroup, &g.video,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "panvertical",
		&g.videoDir, dirURL, vs.PanVerticalSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingVideoRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Vertical pan"),
		settingscore.SettingTagRange, -100, 100,
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagWriteProp, newChild("panvertical"),
		settingscore.SettingTagKVStore, url, "panvertical",
		settingscore.SettingTagGroup, &g.video,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "scalehorizontal",
		&g.videoDir, dirURL, vs.ScaleHorizontalSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingVideoRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Horizontal scale"),
		settingscore.SettingTagRange, 10, 300,
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagWriteProp, newChild("scalehorizontal"),
		settingscore.SettingTagKVStore, url, "scalehorizontal",
		settingscore.SettingTagGroup, &g.video,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "scalevertical",
		&g.videoDir, dirURL, vs.ScaleVerticalSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingVideoRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Vertical scale"),
		settingscore.SettingTagRange, 10, 300,
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagWriteProp, newChild("scalevertical"),
		settingscore.SettingTagKVStore, url, "scalevertical",
		settingscore.SettingTagGroup, &g.video,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingBool, "hstretch",
		&g.videoDir, dirURL, vs.StretchHorizontalSetting, mp)

	sm.SettingCreate(settingscore.SettingBool, mp.SettingVideoRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagTitle, sm.P("Stretch video to widescreen"),
		settingscore.SettingTagWriteProp, newChild("hstretch"),
		settingscore.SettingTagKVStore, url, "hstretch",
		settingscore.SettingTagGroup, &g.video,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingBool, "fstretch",
		&g.videoDir, dirURL, vs.StretchFullscreenSetting, mp)

	sm.SettingCreate(settingscore.SettingBool, mp.SettingVideoRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagTitle, sm.P("Stretch video to fullscreen"),
		settingscore.SettingTagWriteProp, newChild("fstretch"),
		settingscore.SettingTagKVStore, url, "fstretch",
		settingscore.SettingTagGroup, &g.video,
		settingscore.SettingTagInherit, p)

	if vs.VinterpolateSetting != nil {
		p = sys.makeDirSetting(sm, settingscore.SettingBool, "vinterpolate",
			&g.videoDir, dirURL, vs.VinterpolateSetting, mp)

		sm.SettingCreate(settingscore.SettingBool, mp.SettingVideoRoot,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			settingscore.SettingTagTitle, sm.P("Video frame interpolation"),
			settingscore.SettingTagWriteProp, newChild("vinterpolate"),
			settingscore.SettingTagKVStore, url, "vinterpolate",
			settingscore.SettingTagGroup, &g.video,
			settingscore.SettingTagInherit, p)
	}

	sm.SettingCreate(settingscore.SettingSeparator, mp.SettingVideoRoot, 0,
		settingscore.SettingTagGroup, &g.video)

	sm.SettingCreate(settingscore.SettingAction, mp.SettingVideoRoot, 0,
		settingscore.SettingTagTitle, sm.P("Save as global default"),
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagCallback,
		sys.setGlobalDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
			return &g.video
		}), mp,
		settingscore.SettingTagGroup, &g.video)

	if dirURL != "" {
		sm.SettingCreate(settingscore.SettingAction, mp.SettingVideoRoot, 0,
			settingscore.SettingTagTitleCStr, setDirTitle,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			settingscore.SettingTagCallback,
			sys.setDirectoryDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
				return &g.video
			}), mp,
			settingscore.SettingTagGroup, &g.video)

		sm.SettingCreate(settingscore.SettingAction, mp.SettingVideoRoot, 0,
			settingscore.SettingTagTitleCStr, clrDirTitle,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			settingscore.SettingTagCallback,
			sys.clrDirectoryDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
				return &g.videoDir
			}), mp,
			settingscore.SettingTagGroup, &g.video)
	}

	// --- Audio -------------------------------------------------

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "audiovolume",
		&g.audioDir, dirURL, sys.gconfSettingAVVolume(), mp)

	volSetting := sm.SettingCreate(settingscore.SettingInt, mp.SettingAudioRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagTitle, sm.P("Audio volume"),
		settingscore.SettingTagRange, -12, maxUserAudioGain,
		settingscore.SettingTagUnitCStr, "dB",
		settingscore.SettingTagCallback, updateAudioVolumeUser, mp,
		settingscore.SettingTagWriteProp, newChild("audiovolume"),
		settingscore.SettingTagKVStore, url, "audiovolume",
		settingscore.SettingTagPropEnabler, newChild("canAdjustVolume"),
		settingscore.SettingTagGroup, &g.audio,
		settingscore.SettingTagInherit, p)

	// C: mp->mp_vol_setting = setting_create(...)
	sys.mu.Lock()
	if gg := sys.mpGroups[mp]; gg != nil {
		mp.VolSetting = volSetting
	}
	sys.mu.Unlock()

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "avdelta",
		&g.audioDir, dirURL, sys.gconfSettingAVSync(), mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingAudioRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagTitle, sm.P("Audio delay"),
		settingscore.SettingTagRange, -5000, 5000,
		settingscore.SettingTagStep, 50,
		settingscore.SettingTagUnitCStr, "ms",
		settingscore.SettingTagCallback, updateAVDelta, mp,
		settingscore.SettingTagWriteProp, newChild("avdelta"),
		settingscore.SettingTagKVStore, url, "avdelta",
		settingscore.SettingTagGroup, &g.audio,
		settingscore.SettingTagInherit, p)

	sm.SettingCreate(settingscore.SettingSeparator, mp.SettingAudioRoot, 0,
		settingscore.SettingTagGroup, &g.audio)

	sm.SettingCreate(settingscore.SettingAction, mp.SettingAudioRoot, 0,
		settingscore.SettingTagTitle, sm.P("Save as global default"),
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagCallback,
		sys.setGlobalDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
			return &g.audio
		}), mp,
		settingscore.SettingTagGroup, &g.audio)

	if dirURL != "" {
		sm.SettingCreate(settingscore.SettingAction, mp.SettingAudioRoot, 0,
			settingscore.SettingTagTitleCStr, setDirTitle,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			settingscore.SettingTagCallback,
			sys.setDirectoryDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
				return &g.audio
			}), mp,
			settingscore.SettingTagGroup, &g.audio)

		sm.SettingCreate(settingscore.SettingAction, mp.SettingAudioRoot, 0,
			settingscore.SettingTagTitleCStr, clrDirTitle,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			settingscore.SettingTagCallback,
			sys.clrDirectoryDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
				return &g.audioDir
			}), mp,
			settingscore.SettingTagGroup, &g.audio)
	}

	// --- Subtitle ----------------------------------------------

	sm.SettingCreate(settingscore.SettingInt, mp.SettingSubtitleRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagTitle, sm.P("Delay"),
		settingscore.SettingTagRange, -600000, 600000,
		settingscore.SettingTagStep, 500,
		settingscore.SettingTagUnitCStr, "ms",
		settingscore.SettingTagCallback, updateSVDelta, mp,
		settingscore.SettingTagWriteProp, newChild("svdelta"),
		settingscore.SettingTagKVStore, url, "svdelta",
		settingscore.SettingTagGroup, &g.subtitle)

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "subscale",
		&g.subtitleDir, dirURL, sub.ScalingSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingSubtitleRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagTitle, sm.P("Scaling"),
		settingscore.SettingTagRange, 30, 500,
		settingscore.SettingTagStep, 5,
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagWriteProp, newChild("subscale"),
		settingscore.SettingTagKVStore, url, "subscale",
		settingscore.SettingTagGroup, &g.subtitle,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingBool, "subalign",
		&g.subtitleDir, dirURL, sub.AlignOnVideoSetting, mp)

	sm.SettingCreate(settingscore.SettingBool, mp.SettingSubtitleRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagTitle, sm.P("Align on video frame"),
		settingscore.SettingTagWriteProp, newChild("subalign"),
		settingscore.SettingTagKVStore, url, "subalign",
		settingscore.SettingTagGroup, &g.subtitle,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "subvdisplace",
		&g.subtitleDir, dirURL, sub.VerticalDisplacementSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingSubtitleRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagRange, -300, 300,
		settingscore.SettingTagStep, 5,
		settingscore.SettingTagUnitCStr, "px",
		settingscore.SettingTagTitle, sm.P("Vertical position"),
		settingscore.SettingTagWriteProp, newChild("subvdisplace"),
		settingscore.SettingTagKVStore, url, "subvdisplace",
		settingscore.SettingTagGroup, &g.subtitle,
		settingscore.SettingTagInherit, p)

	p = sys.makeDirSetting(sm, settingscore.SettingInt, "subhdisplace",
		&g.subtitleDir, dirURL, sub.HorizontalDisplacementSetting, mp)

	sm.SettingCreate(settingscore.SettingInt, mp.SettingSubtitleRoot,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagRange, -300, 300,
		settingscore.SettingTagStep, 5,
		settingscore.SettingTagUnitCStr, "px",
		settingscore.SettingTagTitle, sm.P("Horizontal position"),
		settingscore.SettingTagWriteProp, newChild("subhdisplace"),
		settingscore.SettingTagKVStore, url, "subhdisplace",
		settingscore.SettingTagGroup, &g.subtitle,
		settingscore.SettingTagInherit, p)

	sm.SettingCreate(settingscore.SettingSeparator, mp.SettingSubtitleRoot, 0,
		settingscore.SettingTagGroup, &g.subtitle)

	sm.SettingCreate(settingscore.SettingAction, mp.SettingSubtitleRoot, 0,
		settingscore.SettingTagTitle, sm.P("Save as global default"),
		settingscore.SettingTagMutex, mp,
		settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
		settingscore.SettingTagCallback,
		sys.setGlobalDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
			return &g.subtitle
		}), mp,
		settingscore.SettingTagGroup, &g.subtitle)

	if dirURL != "" {
		sm.SettingCreate(settingscore.SettingAction, mp.SettingSubtitleRoot, 0,
			settingscore.SettingTagTitleCStr, setDirTitle,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			settingscore.SettingTagCallback,
			sys.setDirectoryDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
				return &g.subtitle
			}), mp,
			settingscore.SettingTagGroup, &g.subtitle)

		sm.SettingCreate(settingscore.SettingAction, mp.SettingSubtitleRoot, 0,
			settingscore.SettingTagTitleCStr, clrDirTitle,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			settingscore.SettingTagCallback,
			sys.clrDirectoryDefaults(mp, func(g *mpSettingGroups) *[]*settingscore.Setting {
				return &g.subtitleDir
			}), mp,
			settingscore.SettingTagGroup, &g.subtitle)
	}

	// --- Other (C: media_settings.c:576-586) --------------------

	if sys.gc != nil && sys.gc.CanStandby {
		sm.SettingCreate(settingscore.SettingBool, mp.SettingRoot,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagMutex, mp,
			settingscore.SettingTagLockMgr, mp.Sys.PropLockmgr,
			// C: SETTING_WRITE_INT(&mp->mp_auto_standby)
			settingscore.SettingTagWriteInt, &mp.AutoStandby,
			settingscore.SettingTagTitle, sm.P("Go to standby after video ends"),
			settingscore.SettingTagGroup, &g.other)
	}
}

// mpSettingsClearLocked — MpSettingsClear for callers already in the
// mp-settings path (avoids taking sys.mu twice).
func (sys *System) mpSettingsClearLocked(mp *mediacore.MediaPipe) {
	sys.MpSettingsClear(mp)
}

func orUnset(s string) string {
	if s == "" {
		return "<unset>"
	}
	return s
}

// ---------------------------------------------------------------------------
// video_settings_init (src/video/video_settings.c:30-219) — canonical
// setting_create calls under the "Video playback" settings dir, storing the
// *_setting handles on the mediacore-published VideoSettings for
// mp_settings_init's INHERIT parents.
// ---------------------------------------------------------------------------

// VideoSettingsStart — C: video_settings_init (video_settings.c:31-219).
// vs is the composition-root-owned settings object (C: the
// video_settings global); its *_setting handles become the INHERIT
// parents for mp_settings_init.
func (sys *System) VideoSettingsStart(sm *settingscore.SettingsManager, vs *videopkg.VideoSettings) {
	if sm == nil || vs == nil {
		return
	}

	// C: settings_add_dir(NULL, _p("Video playback"), "video", ...)
	s := sm.AddDir(nil, sm.P("Video playback"), "video", "",
		sm.P("Video acceleration and display behaviour"), "settings:video")
	if s == nil {
		return
	}

	// Hardware acceleration — single mutually-exclusive selector
	// (extension, no C counterpart: upstream only had the VDPAU bool —
	// VDPAU itself is removed). The per-backend bools stay the source
	// of truth for the decoder chain (cgo.go bind order); the
	// selector just writes them: "auto" enables every compiled-in
	// backend (priority chain), a specific backend enables only that
	// one, "off" disables all → software decode. Options are emitted
	// only for backends compiled into this build AND backed by real
	// hardware — cheap node checks only (no libva/libcuda load: the
	// NVIDIA VA bridge would crash headless).
	// Hybrid-GPU systems (Intel+NVIDIA): only the backend of the GPU
	// that actually renders is offered — VAAPI options on a NVIDIA
	// (PRIME offload) renderer would decode cross-GPU with no
	// zero-copy, and vice versa. On NVIDIA-only systems the renderer
	// is NVIDIA by definition even without the offload env vars.
	nvidiaGpu := medialibav.NvdecHwPresent()
	nonNvGpu := medialibav.VaapiHwPresent()
	renderNv := medialibav.NvidiaRenderActive(vs) || (nvidiaGpu && !nonNvGpu)
	vaapiHw := videopkg.VaapiAvailable && nonNvGpu && !renderNv
	nvdecHw := videopkg.NvdecAvailable && nvidiaGpu && renderNv
	d3d11Hw := videopkg.D3d11vaAvailable
	dxva2Hw := videopkg.Dxva2Available
	hwavail := vaapiHw || nvdecHw || d3d11Hw || dxva2Hw
	if hwavail {
		args := []any{
			settingscore.SettingTagTitle, sm.P("Hardware acceleration"),
			settingscore.SettingTagStore, "videoplayback", "hwaccel",
			settingscore.SettingTagCallback,
			func(opaque any, value any) {
				sel, _ := value.(string)
				if sel == "" {
					sel = "auto"
				}
				set := func(p *int, want string, ok bool) {
					*p = 0
					if ok && (sel == "auto" || sel == want) {
						*p = 1
					}
				}
				set(&vs.Vaapi, "vaapi", vaapiHw)
				set(&vs.Nvdec, "nvdec", nvdecHw)
				set(&vs.D3d11va, "d3d11va", d3d11Hw)
				set(&vs.Dxva2, "dxva2", dxva2Hw)
			}, nil,
			settingscore.SettingTagOption, "auto", sm.P("Auto"),
		}
		if vaapiHw {
			args = append(args, settingscore.SettingTagOption,
				"vaapi", sm.P("VAAPI"))
		}
		if nvdecHw {
			args = append(args, settingscore.SettingTagOption,
				"nvdec", sm.P("NVDEC"))
		}
		if d3d11Hw {
			args = append(args, settingscore.SettingTagOption,
				"d3d11va", sm.P("D3D11VA"))
		}
		if dxva2Hw {
			args = append(args, settingscore.SettingTagOption,
				"dxva2", sm.P("DXVA2"))
		}
		args = append(args, settingscore.SettingTagOption,
			"off", sm.P("Off"))
		sm.SettingCreate(settingscore.SettingMultiOpt, s,
			settingscore.SettingsInitialUpdate, args...)
	}

	// C: #if ENABLE_VDPAU (video_settings.c:41-67) — the deinterlacer
	// options went away with VDPAU itself.

	// C: #if defined(__APPLE__) || defined(__ANDROID__)
	//    (video_settings.c:70-77) — gates VideoToolbox / MediaCodec.
	if runtime.GOOS == "darwin" || runtime.GOOS == "android" {
		sm.SettingCreate(settingscore.SettingBool, s,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, sm.P("Hardware accelerated decoding"),
			settingscore.SettingTagStore, "videoplayback", "videoaccel2",
			settingscore.SettingTagValue, 1,
			settingscore.SettingTagWriteInt, &vs.VideoAccel)
	}

	vs.VzoomSetting = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Video zoom"),
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagRange, 50, 200,
		settingscore.SettingTagValue, 100,
		settingscore.SettingTagStore, "videoplayback", "vzoom",
		settingscore.SettingTagValueOrigin, "global")

	vs.PanHorizontalSetting = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Horizontal pan"),
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagRange, -100, 100,
		settingscore.SettingTagValue, 0,
		settingscore.SettingTagStore, "videoplayback", "horizontalpan",
		settingscore.SettingTagValueOrigin, "global")

	vs.PanVerticalSetting = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Vertical pan"),
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagRange, -100, 100,
		settingscore.SettingTagValue, 0,
		settingscore.SettingTagStore, "videoplayback", "verticalpan",
		settingscore.SettingTagValueOrigin, "global")

	vs.ScaleHorizontalSetting = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Horizontal scale"),
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagRange, 10, 300,
		settingscore.SettingTagValue, 100,
		settingscore.SettingTagStore, "videoplayback", "horizontalscale",
		settingscore.SettingTagValueOrigin, "global")

	vs.ScaleVerticalSetting = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Vertical scale"),
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagRange, 10, 300,
		settingscore.SettingTagValue, 100,
		settingscore.SettingTagStore, "videoplayback", "verticalscale",
		settingscore.SettingTagValueOrigin, "global")

	vs.StretchHorizontalSetting = sm.SettingCreate(settingscore.SettingBool, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Stretch video to widescreen"),
		settingscore.SettingTagStore, "videoplayback", "stretch_horizontal",
		settingscore.SettingTagValueOrigin, "global")

	vs.StretchFullscreenSetting = sm.SettingCreate(settingscore.SettingBool, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Stretch video to fullscreen"),
		settingscore.SettingTagStore, "videoplayback", "stretch_fullscreen",
		settingscore.SettingTagValueOrigin, "global")

	vs.VinterpolateSetting = sm.SettingCreate(settingscore.SettingBool, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Video frame interpolation"),
		settingscore.SettingTagStore, "videoplayback", "vinterpolate",
		settingscore.SettingTagValueOrigin, "global",
		settingscore.SettingTagValue, 1)

	sm.SettingCreate(settingscore.SettingMultiOpt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Resume video playback"),
		settingscore.SettingTagWriteInt, &vs.ResumeMode,
		settingscore.SettingTagStore, "videoplayback", "resumemode",
		settingscore.SettingTagOption, "2", sm.P("Ask"),
		settingscore.SettingTagOption, "1", sm.P("Always"),
		settingscore.SettingTagOption, "0", sm.P("Never"))

	sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Count video as played when reaching"),
		settingscore.SettingTagValue, 90,
		settingscore.SettingTagRange, 1, 100,
		settingscore.SettingTagUnitCStr, "%",
		settingscore.SettingTagWriteInt, &vs.PlayedThreshold,
		settingscore.SettingTagStore, "videoplayback", "played_threshold")

	sm.SettingCreate(settingscore.SettingBool, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Automatically play next video in list"),
		settingscore.SettingTagStore, "videoplayback", "continuous_playback",
		settingscore.SettingTagWriteInt, &vs.ContinuousPlayback)

	sm.SettingCreate(settingscore.SettingMultiOpt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Up / Down during video playback controls"),
		settingscore.SettingTagWriteInt, &vs.DpadUpDownMode,
		settingscore.SettingTagStore, "videoplayback", "dpad_up_down_mode",
		settingscore.SettingTagOption, "0", sm.P("Master volume"),
		settingscore.SettingTagOption, "1", sm.P("Per-file volume"))

	// C: show clock during playback — writes through to
	//   $global.clock.showDuringVideo
	if pm := vs.PropManager; pm != nil {
		clock := pm.CreateEx(pm.GetGlobal(), "clock", nil, false, true)
		showDuring := pm.CreateEx(clock, "showDuringVideo", nil, false, true)
		sm.SettingCreate(settingscore.SettingBool, s,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, sm.P("Show clock during playback"),
			settingscore.SettingTagStore, "videoplayback", "show_clock",
			settingscore.SettingTagWriteProp, showDuring)
	}

	sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Step when seeking backward"),
		settingscore.SettingTagValue, 15,
		settingscore.SettingTagRange, 3, 60,
		settingscore.SettingTagUnitCStr, "s",
		settingscore.SettingTagWriteInt, &vs.SeekBackStep,
		settingscore.SettingTagStore, "videoplayback", "seekbackstep")

	sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Step when seeking forward"),
		settingscore.SettingTagValue, 30,
		settingscore.SettingTagRange, 3, 60,
		settingscore.SettingTagUnitCStr, "s",
		settingscore.SettingTagWriteInt, &vs.SeekFwdStep,
		settingscore.SettingTagStore, "videoplayback", "seekfwdstep")

	// C: SETTING_RANGE(16, gconf.max_video_buffer_size ?: 768)
	maxBuf := 0
	if sys.gc != nil {
		maxBuf = sys.gc.MaxVideoBufferSize
	}
	if maxBuf == 0 {
		maxBuf = 768
	}
	sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Video buffer size"),
		settingscore.SettingTagValue, 48,
		settingscore.SettingTagRange, 16, maxBuf,
		settingscore.SettingTagUnitCStr, "MB",
		settingscore.SettingTagStore, "videoplayback", "videobuffersize",
		settingscore.SettingTagWriteInt, &vs.VideoBufferSize)
}
