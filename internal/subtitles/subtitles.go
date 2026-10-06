package subtitles

import (
	mediacore "github.com/czz/movian-go/internal/media/core"
	"sync"
	"sync/atomic"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// SubScanner — C: sub_scanner_t (subtitles.h:30-55).
type SubScanner struct {
	refcount atomic.Int32 // C: atomic_t ss_refcount
	beFlags  int          // C: ss_beflags

	title  string // C: rstr_t *ss_title
	imdbID string // C: rstr_t *ss_imdbid

	mutex    sync.Mutex     // C: hts_mutex_t ss_mutex — guards propRoot
	propRoot *propcore.Prop // C: ss_proproot — property where to add subs

	url string // C: ss_url — can be empty (NULL)

	stop atomic.Bool // C: ss_stop

	hashValid   bool     // C: ss_hash_valid
	opensubHash uint64   // C: ss_opensub_hash
	fsize       uint64   // C: ss_fsize
	subDBHash   [16]byte // C: ss_subdbhash

	year    int // C: ss_year
	season  int // C: ss_season
	episode int // C: ss_episode

	duration int // C: ss_duration

	fam *fileaccesscore.FileAccessManager // C: implicit global fa context
	ts  *trace.TraceSystem                // C: trace() global — fam-derived

	sys *System // owning subsystem (C: file statics of subtitles.c)
}

// SubtitleProvider — C: subtitle_provider_t (subtitles.h:62-79).
type SubtitleProvider struct {
	id      string // C: sp_id
	enabled int    // C: sp_enabled
	autosel int    // C: sp_autosel
	prio    int    // C: sp_prio

	query  func(sp *SubtitleProvider, ss *SubScanner, score int, autosel int) // C: sp_query
	retain func(sp *SubtitleProvider)                                         // C: sp_retain

	settings       *propcore.Prop        // C: sp_settings
	settingEnabled *settingscore.Setting // C: sp_setting_enabled
	settingAutosel *settingscore.Setting // C: sp_setting_autosel

	opaque any // C: sp_opaque
}

// SetQuery — C: sp->sp_query assignment (public field in C).
func (sp *SubtitleProvider) SetQuery(
	f func(sp *SubtitleProvider, ss *SubScanner, score int, autosel int)) {
	sp.query = f
}

// SetRetain — C: sp->sp_retain assignment (public field in C).
func (sp *SubtitleProvider) SetRetain(f func(sp *SubtitleProvider)) {
	sp.retain = f
}

// SetOpaque — C: sp->sp_opaque assignment.
func (sp *SubtitleProvider) SetOpaque(v any) { sp.opaque = v }

// Opaque — C: sp->sp_opaque read.
func (sp *SubtitleProvider) Opaque() any { return sp.opaque }

// SubtitleSettings represents subtitle display settings
type SubtitleSettings struct {
	// C: subtitle_settings.*_setting (setting_t *) — global defaults used
	// as SETTING_INHERIT parents by mp_settings_init (media_settings.c).
	ScalingSetting                *settingscore.Setting
	AlignOnVideoSetting           *settingscore.Setting
	VerticalDisplacementSetting   *settingscore.Setting
	HorizontalDisplacementSetting *settingscore.Setting

	alignment          int // LAYOUT_ALIGN_ from layout.h
	styleOverride      int
	color              int
	shadowColor        int
	shadowDisplacement int
	outlineColor       int
	outlineSize        int
}

// Setting handles — C: subtitle_settings.*_setting (setting_t *), used
// as SETTING_INHERIT parents by mp_settings_init (media_settings.c):
// the exported fields above are read directly.

// subtitleSettingsDefaults — C: static initializers of
// subtitle_settings (subtitles.c:626). Read-only fallback returned by
// SubSettings on an unwired System (nil receiver).
var subtitleSettingsDefaults = SubtitleSettings{
	alignment:          0,
	styleOverride:      0,
	color:              0xFFFFFF,
	shadowColor:        0x000000,
	shadowDisplacement: 2,
	outlineColor:       0x000000,
	outlineSize:        2,
}

// SubSettings — C: &subtitle_settings — the process-wide subtitle
// settings owned by the System.
func (s *System) SubSettings() *SubtitleSettings {
	if s == nil {
		return &subtitleSettingsDefaults
	}
	return &s.settings
}

// SystemOf resolves the subtitles System owning mp's media session
// (mp.Sys.Owner → the BackendSystem). Nil when unwired.
func SystemOf(mp *mediacore.MediaPipe) *System {
	if mp == nil || mp.Sys == nil {
		return nil
	}
	h, _ := mp.Sys.Owner.(interface{ SubSys() *System })
	if h == nil {
		return nil
	}
	return h.SubSys()
}

// Accessors used by media core's video_overlay_render_cleartext port
// (C reads the struct fields directly).
func (s *SubtitleSettings) Color() int              { return s.color }
func (s *SubtitleSettings) ShadowColor() int        { return s.shadowColor }
func (s *SubtitleSettings) ShadowDisplacement() int { return s.shadowDisplacement }
func (s *SubtitleSettings) OutlineColor() int       { return s.outlineColor }
func (s *SubtitleSettings) OutlineSize() int        { return s.outlineSize }
func (s *SubtitleSettings) StyleOverride() int      { return s.styleOverride }

// Stop stops a subtitle scanner
func (ss *SubScanner) Stop() {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.stop.Store(true)
}

// IsStopped checks if a subtitle scanner is stopped
func (ss *SubScanner) IsStopped() bool {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	return ss.stop.Load()
}

// SetTitle sets the video title for the scanner
func (ss *SubScanner) SetTitle(title string) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.title = title
}

// SetIMDBID sets the IMDB ID for the scanner
func (ss *SubScanner) SetIMdbID(imdbID string) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.imdbID = imdbID
}

// SetYear sets the year for the scanner
func (ss *SubScanner) SetYear(year int) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.year = year
}

// SetSeason sets the season for the scanner
func (ss *SubScanner) SetSeason(season int) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.season = season
}

// SetEpisode sets the episode for the scanner
func (ss *SubScanner) SetEpisode(episode int) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.episode = episode
}

// SetHash sets the opensubtitles hash
func (ss *SubScanner) SetHash(hash uint64, fsize uint64) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.opensubHash = hash
	ss.fsize = fsize
	ss.hashValid = true
}

// GetTitle returns the video title
func (ss *SubScanner) GetTitle() string {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	return ss.title
}

// GetIMdbID returns the IMDB ID
func (ss *SubScanner) GetIMdbID() string {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	return ss.imdbID
}

// PropRoot — C: ss_proproot. Mutex-guarded like all Go-side accesses.
func (ss *SubScanner) PropRoot() *propcore.Prop {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	return ss.propRoot
}

// Scan-time fields read directly by subtitle providers (C reads the
// struct fields without locking — they are fixed before the scan runs).
func (ss *SubScanner) Season() int         { return ss.season }
func (ss *SubScanner) Year() int           { return ss.year }
func (ss *SubScanner) Episode() int        { return ss.episode }
func (ss *SubScanner) Fsize() uint64       { return ss.fsize }
func (ss *SubScanner) Duration() int       { return ss.duration }
func (ss *SubScanner) HashValid() bool     { return ss.hashValid }
func (ss *SubScanner) OpensubHash() uint64 { return ss.opensubHash }
func (ss *SubScanner) SubDBHash() [16]byte { return ss.subDBHash }
