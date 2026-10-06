package subtitles

// subtitles_c.go — canonical port of src/subtitles/subtitles.c
//
// Provider registry, provider settings persistence, filesystem subtitle
// scanning, sub_scanner lifecycle, and subtitles_init*.
//
// Layering note: C compiles calls into settings.c (settings_add_dir,
// setting_create, setting_destroy, _p) and ext_subtitles.c's
// subtitles_probe directly inside subtitles.c. In Go pkg/subtitles
// imports settings/core and nls directly (no cycle); only the manager
// instances are injected once via SetDeps. SubtitlesProbe stays a
// hook: pkg/subtitles/ext imports this package, so the reverse would
// cycle.

import (
	"slices"
	"strings"
	"sync"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// C: file-scope statics (subtitles.c:37-51)
// ---------------------------------------------------------------------------

// System — C: subtitle_provider_queue + mutex + num_providers +
// settings_group + central_path + the five static sp_* providers +
// sp_cfg (subtitles.c file scope) — plus the deps C resolves via
// globals (settings mgr, prop mgr, htsmsg store). Owned by the app
// context; backends reach it via BackendSystem.SubSys().
type System struct {
	mu            sync.Mutex
	providers     []*SubtitleProvider // sorted by prio
	numProviders  int
	settingsGroup *propcore.Prop
	centralPath   string

	spSameFilename           *SubtitleProvider
	spAnyFilename            *SubtitleProvider
	spCentralDirSameFilename *SubtitleProvider
	spCentralDirAnyFilename  *SubtitleProvider
	spEmbedded               *SubtitleProvider

	cfg *htsmsg.HTSMsg // C: static htsmsg_t *sp_cfg

	// settings — C: struct subtitle_settings subtitle_settings
	// (subtitles.c:626) — process-wide, owned by this subsystem.
	settings SubtitleSettings

	// deps — C: gconf.* / implicit globals wired by the composition root.
	sm      *settingscore.SettingsManager
	pm      *propcore.PropManager
	store   *htsmsg.Store // C: global htsmsg_store
	TextSys TextSystem    // C: freetype/fontstash statics (font_subs, freetype_family_id)

	// Hooks — the plugin boundary: subtitles_probe lives in
	// pkg/subtitles/ext which imports this package (provider
	// registration), so the reverse import would cycle. Injected
	// once at init; nil-safe (unit tests run without it).
	Hooks struct {
		// C: subtitles_probe (ext_subtitles.c)
		SubtitlesProbe func(url string) string
	}
}

// TextSystem — the slice of pkg/text read by the subtitle renderers
// (sub_ass.c, video_overlay.c → font_subs + freetype_family_id +
// text_parse). *text.System satisfies it; it's an interface because
// text→backend→…→subtitles would cycle on a concrete import.
type TextSystem interface {
	FontSubs() string
	FreetypeFamilyId(str string, fontDomain int) int
	TextParse(str string, flags int, prefix []uint32, context int) ([]uint32, int)
}

// SetTextSystem injects the text subsystem (C: freetype.c/fontstash.c
// statics read by sub_ass.c and video_overlay.c).
func (s *System) SetTextSystem(ts TextSystem) { s.TextSys = ts }

// NewSystem — replaces SetDeps: injects the settings/prop manager
// instances (init-time). Nil-safe: unit tests run without the
// settings layer.
func NewSystem(sm *settingscore.SettingsManager, pm *propcore.PropManager,
	store *htsmsg.Store) *System {
	s := &System{sm: sm, pm: pm, store: store}
	// C: struct subtitle_settings subtitle_settings (subtitles.c:626)
	// static initializers.
	s.settings = subtitleSettingsDefaults
	return s
}

// backendVideoNoFSScan — C: BACKEND_VIDEO_NO_FS_SCAN (backend.h:52).
// pkg/subtitles cannot import pkg/backend/core (rtmp→subtitles→backend
// would cycle), so the flag value is mirrored here.
const backendVideoNoFSScan = 0x4

// pstr — C: _p(str) = nls_get_prop(s). Works unwired too: nls returns
// a prop tracking the key itself when no translation is loaded.
func pstr(s string) *propcore.Prop {
	return nls.GetProp(s)
}

// createRoot — C: prop_create_root(NULL). Falls back to a standalone
// prop when the prop manager is not wired (unit tests).
func (s *System) createRoot() *propcore.Prop {
	if s.pm != nil {
		return s.pm.CreateRoot("")
	}
	return propcore.NewStandaloneProp("")
}

// ---------------------------------------------------------------------------
// subtitle_score (subtitles.c:57-61)
// ---------------------------------------------------------------------------

// subtitleScore — C: subtitle_score (subtitles.c:57-61). C reads
// num_subtitle_providers and sp->sp_prio without the provider mutex at
// some call sites; Go needs the lock for -race cleanliness.
func (s *System) subtitleScoreLocked(sp *SubtitleProvider) int {
	return (1 + s.numProviders - sp.prio) * 1000
}

func (s *System) subtitleScore(sp *SubtitleProvider) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subtitleScoreLocked(sp)
}

// providerEnabled / providerAutosel — C reads sp->sp_enabled /
// sp->sp_autosel directly; Go locks to stay race-clean.
func (s *System) providerEnabled(sp *SubtitleProvider) bool {
	if sp == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sp.enabled != 0
}

func (s *System) providerAutosel(sp *SubtitleProvider) int {
	if sp == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sp.autosel
}

// ---------------------------------------------------------------------------
// subtitles_embedded_score (subtitles.c:66-72)
// ---------------------------------------------------------------------------

func (s *System) SubtitlesEmbeddedScore() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spEmbedded == nil {
		return -1
	}
	if s.spEmbedded.enabled == 0 {
		return -1
	}
	return s.subtitleScoreLocked(s.spEmbedded)
}

// ---------------------------------------------------------------------------
// subtitles_embedded_autosel (subtitles.c:78-82)
// ---------------------------------------------------------------------------

func (s *System) SubtitlesEmbeddedAutosel() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spEmbedded == nil {
		return 0
	}
	return s.spEmbedded.autosel
}

// ---------------------------------------------------------------------------
// sp_set_enable (subtitles.c:88-100)
// ---------------------------------------------------------------------------

func (s *System) spSetEnable(sp *SubtitleProvider, enabled int) {
	sp.enabled = enabled
	if sp.settings != nil {
		propSetvMetadataInt(sp.settings, "enabled", sp.enabled)
	}
	if s.cfg == nil {
		return
	}
	c := s.cfg.GetMap(sp.id)
	if c == nil {
		return
	}
	c.DeleteField("enabled")
	c.AddU32("enabled", uint32(sp.enabled))
	if gs := s.store; gs != nil {
		gs.Save(s.cfg, "subtitleproviders")
	}
}

// ---------------------------------------------------------------------------
// sp_set_autosel (subtitles.c:106-116)
// ---------------------------------------------------------------------------

func (s *System) spSetAutosel(sp *SubtitleProvider, autosel int) {
	sp.autosel = autosel
	if s.cfg == nil {
		return
	}
	c := s.cfg.GetMap(sp.id)
	if c == nil {
		return
	}
	c.DeleteField("autosel")
	c.AddU32("autosel", uint32(sp.autosel))
	if gs := s.store; gs != nil {
		gs.Save(s.cfg, "subtitleproviders")
	}
}

// propSetvMetadataInt — C: prop_setv(p, "metadata", key, NULL,
// PROP_SET_INT, v).
func propSetvMetadataInt(p *propcore.Prop, key string, v int) {
	pm := p.Manager()
	if pm == nil {
		return
	}
	m := pm.CreateEx(p, "metadata", nil, false, false)
	e := pm.CreateEx(m, key, nil, false, false)
	pm.SetIntEx(e, nil, v)
}

// ---------------------------------------------------------------------------
// sp_prio_cmp (subtitles.c:122-126)
// ---------------------------------------------------------------------------

func spPrioCmp(a, b *SubtitleProvider) int {
	return a.prio - b.prio
}

// ---------------------------------------------------------------------------
// subtitle_provider_register (subtitles.c:132-187)
// ---------------------------------------------------------------------------

func (s *System) SubtitleProviderRegister(sp *SubtitleProvider, id string,
	title *propcore.Prop, defaultPrio int, subtype string,
	defaultEnable, defaultAutosel int) {

	sp.id = id
	sp.prio = defaultPrio
	if sp.prio == 0 {
		sp.prio = 1000000
	}

	sp.enabled = defaultEnable
	sp.autosel = defaultAutosel
	if s.sm != nil {
		sp.settings = s.sm.AddDir(s.settingsGroup,
			title, subtype, "", nil, "")
	}

	if sp.settings != nil {
		if pm := sp.settings.Manager(); pm != nil {
			pm.TagSet(sp.settings, "subtitle_providers", sp)
		}
	}

	s.mu.Lock()

	if s.cfg != nil {
		c := s.cfg.GetMap(sp.id)
		if c != nil {
			sp.enabled = int(c.GetU32OrDefault("enabled", uint32(sp.enabled)))
			sp.autosel = int(c.GetU32OrDefault("autosel", uint32(sp.autosel)))
			sp.prio = int(c.GetU32OrDefault("prio", uint32(sp.prio)))
		} else {
			s.cfg.AddMsg(sp.id, htsmsg.NewMap())
		}
	}

	if s.sm != nil && sp.settings != nil {
		// C: SETTING_MUTEX(&subtitle_provider_mutex) — the settings
		// layer invokes the callback under that mutex. Go's settings
		// ignore the mutex tag, so wrap the callback.
		sp.settingEnabled = s.sm.SettingCreate(settingscore.SettingBool,
			sp.settings, 0,
			settingscore.SettingTagValue, sp.enabled,
			settingscore.SettingTagTitle, pstr("Enabled"),
			settingscore.SettingTagCallback, func(opaque any, v any) {
				p, _ := opaque.(*SubtitleProvider)
				vi, _ := v.(int)
				s.mu.Lock()
				s.spSetEnable(p, vi)
				s.mu.Unlock()
			}, sp,
			settingscore.SettingTagMutex, &s.mu)

		sp.settingAutosel = s.sm.SettingCreate(settingscore.SettingBool,
			sp.settings, 0,
			settingscore.SettingTagValue, sp.autosel,
			settingscore.SettingTagTitle, pstr("Automatically select from this source"),
			settingscore.SettingTagCallback, func(opaque any, v any) {
				p, _ := opaque.(*SubtitleProvider)
				vi, _ := v.(int)
				s.mu.Lock()
				s.spSetAutosel(p, vi)
				s.mu.Unlock()
			}, sp,
			settingscore.SettingTagMutex, &s.mu)
	}

	s.numProviders++

	// TAILQ_INSERT_SORTED(&subtitle_providers, sp, sp_link, sp_prio_cmp)
	pos := len(s.providers)
	for i, x := range s.providers {
		if spPrioCmp(sp, x) < 0 {
			pos = i
			break
		}
	}
	s.providers = append(s.providers, nil)
	copy(s.providers[pos+1:], s.providers[pos:])
	s.providers[pos] = sp

	var n *SubtitleProvider
	if pos+1 < len(s.providers) {
		n = s.providers[pos+1]
	}
	if sp.settings != nil {
		var before *propcore.Prop
		if n != nil {
			before = n.settings
		}
		if pm := sp.settings.Manager(); pm != nil {
			pm.Move(sp.settings, before)
		}
	}

	s.mu.Unlock()

	if sp.settings != nil {
		propSetvMetadataInt(sp.settings, "enabled", sp.enabled)
	}
}

// ---------------------------------------------------------------------------
// subtitle_provider_create (subtitles.c:193-202)
// ---------------------------------------------------------------------------

func (s *System) subtitleProviderCreate(id string, title *propcore.Prop,
	defaultPrio int, subtype string, defaultEnable,
	defaultAutosel int) *SubtitleProvider {
	sp := &SubtitleProvider{}
	s.SubtitleProviderRegister(sp, id, title, defaultPrio, subtype,
		defaultEnable, defaultAutosel)
	return sp
}

// ---------------------------------------------------------------------------
// subtitle_provider_unregister (subtitles.c:208-220)
// ---------------------------------------------------------------------------

func (s *System) SubtitleProviderUnregister(sp *SubtitleProvider) {
	s.mu.Lock()
	if sp.settings != nil {
		if pm := sp.settings.Manager(); pm != nil {
			pm.TagSet(sp.settings, "subtitle_providers", nil)
		}
	}
	for i, x := range s.providers {
		if x == sp {
			s.providers = slices.Delete(s.providers, i, i+1)
			break
		}
	}
	s.numProviders--
	if s.sm != nil {
		s.sm.Destroy(sp.settingEnabled)
		s.sm.Destroy(sp.settingAutosel)
	}
	s.mu.Unlock()
	sp.id = ""
	if sp.settings != nil {
		if pm := sp.settings.Manager(); pm != nil {
			pm.Destroy(sp.settings)
		}
	}
}

// ---------------------------------------------------------------------------
// fs_sub_match (subtitles.c:229-254)
//
// video - filename of video (sans extension)
// sub   - URL to subtitle
// ---------------------------------------------------------------------------

func (s *System) fsSubMatch(video, sub string) int {
	// Get last path component of sub to form a filename
	i := strings.LastIndex(sub, "/")
	if i < 0 {
		return 0
	}
	sub = sub[i+1:]

	vl := len(video)
	sl := len(sub)

	if sl > vl && sub[vl] == '.' && strings.EqualFold(sub[:vl], video) {
		return 1
	}

	x := strings.LastIndex(sub, ".")
	if x >= 0 {
		off := x
		if vl > off {
			if (video[off] == '.' || video[off] == ' ') &&
				strings.EqualFold(sub[:off], video[:off]) {
				return 1
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// check_subtitle_file (subtitles.c:260-337)
// ---------------------------------------------------------------------------

func (s *System) checkSubtitleFile(ss *SubScanner, subFilename, subURL,
	videoFilename string, baseScore, matchResult, autosel int,
	lang string) {

	pi := strings.LastIndex(subFilename, ".")
	if pi < 0 {
		return
	}
	postfix := subFilename[pi:]

	if matchResult == -1 {
		baseScore += s.fsSubMatch(videoFilename, subURL)
	} else {
		if s.fsSubMatch(videoFilename, subURL) != matchResult {
			return
		}
	}

	var typ string

	switch {
	case strings.EqualFold(postfix, ".srt") ||
		strings.EqualFold(postfix, ".vtt") ||
		strings.EqualFold(postfix, ".webvtt"):
		// C: if(postfix - sub_filename > 4 && postfix[-4] == '.')
		if pi > 4 && subFilename[pi-4] == '.' {
			if il := misc.IsolangFind(subFilename[pi-3 : pi]); il != nil {
				lang = il.Iso639_2
			}
		}
		typ = "SRT"

	case strings.EqualFold(postfix, ".ass") ||
		strings.EqualFold(postfix, ".ssa"):
		if pi > 4 && subFilename[pi-4] == '.' {
			if il := misc.IsolangFind(subFilename[pi-3 : pi]); il != nil {
				lang = il.Iso639_2
			}
		}
		typ = "ASS / SSA"

	case strings.EqualFold(postfix, ".sub") ||
		strings.EqualFold(postfix, ".txt") ||
		strings.EqualFold(postfix, ".xml") ||
		strings.EqualFold(postfix, ".mpl"):
		if s.Hooks.SubtitlesProbe == nil {
			return
		}
		typ = s.Hooks.SubtitlesProbe(subURL)
		if typ == "" {
			ss.ts.Trace(trace.TRACE_DEBUG, "Subtitles",
				"%s is not a recognized subtitle format", subURL)
			return
		}
		ss.ts.Trace(trace.TRACE_DEBUG, "Subtitles",
			"%s probed as %s", subURL, typ)

	case strings.EqualFold(postfix, ".idx"):
		ss.mutex.Lock()
		if ss.propRoot != nil {
			vobsubProbe(ss.fam, subURL, subFilename, baseScore,
				ss.propRoot, "", autosel)
		}
		ss.mutex.Unlock()
		return

	default:
		return
	}

	ss.mutex.Lock()
	if ss.propRoot != nil {
		mpAddTrack(ss.propRoot, subFilename, subURL, typ, "", lang,
			"", pstr("External file"), baseScore, autosel)
	}
	ss.mutex.Unlock()
}

// mpAddTrack — C: mp_add_track (media_track.c:121-140): mp_add_track_ex
// followed by prop_ref_dec on the returned track.
func mpAddTrack(parent *propcore.Prop, title, url, format, longformat,
	isolang, source string, sourcep *propcore.Prop, basescore,
	autosel int) {
	pm := parent.Manager()
	if pm == nil {
		return
	}
	p := mediacore.MpAddTrackEx(pm, parent, title, url, format,
		longformat, isolang, source, sourcep, basescore, autosel)
	if p != nil {
		pm.RefDec(p)
	}
}

// ---------------------------------------------------------------------------
// fs_sub_scan_dir (subtitles.c:345-410)
//
// url   - directory to scan in
// video - filename of video
// ---------------------------------------------------------------------------

func (s *System) fsSubScanDir(ss *SubScanner, url, video string, descendAll bool,
	level uint, sp1, sp2 *SubtitleProvider, lang string) {

	if level == 0 {
		return
	}

	ss.ts.Trace(trace.TRACE_DEBUG, "Video",
		"Scanning for subs in %s for %s", url, video)

	fd, err := fileaccesscore.FAScanDir(nil, url)
	if fd == nil || err != nil {
		ss.ts.Trace(trace.TRACE_DEBUG, "Video",
			"Unable to scan %s for subtitles: %v", url, err)
		return
	}
	defer fd.Free()

	// C: RB_FOREACH(fde, &fd->fd_entries, fde_link) — the C dir entry
	// set is ordered by filename; iterate sorted keys for the same
	// deterministic order.
	names := make([]string, 0, len(fd.Entries))
	for name := range fd.Entries {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
		fde := fd.Entries[name]

		if ss.stop.Load() {
			break
		}

		filename := fde.Filename

		if fde.Type == fileaccesscore.ContentDir ||
			fde.Type == fileaccesscore.ContentShare {

			if descendAll || strings.EqualFold(filename, "subs") {
				s.fsSubScanDir(ss, fde.URL, video, descendAll,
					level-1, sp1, sp2, lang)

			} else if len(filename) > 5 &&
				strings.EqualFold(filename[:5], "subs-") {
				if il := misc.IsolangFind(filename[5:]); il != nil {
					lang = il.Iso639_2
				}
				s.fsSubScanDir(ss, fde.URL, video, descendAll,
					level-1, sp1, sp2, lang)
			}
			continue
		}

		pi := strings.LastIndex(fde.URL, ".")
		if pi >= 0 && strings.EqualFold(fde.URL[pi:], ".zip") {
			s.fsSubScanDir(ss, "zip://"+fde.URL, video, descendAll,
				level-1, sp1, sp2, "")
			continue
		}

		if s.providerEnabled(sp1) {
			s.checkSubtitleFile(ss, filename, fde.URL, video,
				s.subtitleScore(sp1), 1, s.providerAutosel(sp1), lang)
		}

		if s.providerEnabled(sp2) {
			s.checkSubtitleFile(ss, filename, fde.URL, video,
				s.subtitleScore(sp2), 0, s.providerAutosel(sp2), lang)
		}
	}
}

// ---------------------------------------------------------------------------
// sub_scanner_release / sub_scanner_retain (subtitles.c:416-437)
// ---------------------------------------------------------------------------

func SubScannerRelease(ss *SubScanner) {
	if ss == nil {
		return
	}
	if ss.refcount.Add(-1) != 0 {
		return
	}
	// C: rstr_release(title); rstr_release(imdbid); free(url);
	//    hts_mutex_destroy; free(ss)
	ss.title = ""
	ss.imdbID = ""
	ss.url = ""
}

func SubScannerRetain(ss *SubScanner) {
	if ss == nil {
		return
	}
	ss.refcount.Add(1)
}

// ---------------------------------------------------------------------------
// sub_scanner_thread (subtitles.c:444-507)
// ---------------------------------------------------------------------------

func (ss *SubScanner) subScannerThread() {
	s := ss.sys
	s.mu.Lock()

	cnt := 0
	for _, sp := range s.providers {
		cnt++
		sp.prio = cnt
	}

	v := make([]*SubtitleProvider, 0, cnt)
	for _, sp := range s.providers {
		if sp.enabled == 0 {
			continue
		}
		v = append(v, sp)
		if sp.retain != nil {
			sp.retain(sp)
		}
	}

	s.mu.Unlock()

	// C: fname = mystrdupa(ss->ss_url);
	//    fname = strrchr(fname, '/') ?: fname;
	//    fname++;
	//    dot = strrchr(fname, '.'); if(dot) *dot = 0;
	fname := ss.url
	if i := strings.LastIndex(fname, "/"); i >= 0 {
		fname = fname[i:]
	}
	if len(fname) > 0 {
		fname = fname[1:]
	}
	if i := strings.LastIndex(fname, "."); i >= 0 {
		fname = fname[:i]
	}

	if ss.beFlags&backendVideoNoFSScan == 0 {
		if parent, err := fileaccesscore.FAParent(ss.url); err == nil {
			s.fsSubScanDir(ss, parent, fname, false, 2,
				s.spSameFilename, s.spAnyFilename, "")
		}
	}

	s.mu.Lock()
	if s.centralPath != "" {
		path := s.centralPath
		s.mu.Unlock()

		s.fsSubScanDir(ss, path, fname, true, 2,
			s.spCentralDirSameFilename, s.spCentralDirAnyFilename, "")
	} else {
		s.mu.Unlock()
	}

	for _, sp := range v {
		if sp.query != nil {
			var q *SubScanner
			if !ss.stop.Load() {
				q = ss
			}
			sp.query(sp, q, s.subtitleScore(sp), s.providerAutosel(sp))
		}
	}

	SubScannerRelease(ss)
}

// ---------------------------------------------------------------------------
// sub_scanner_create (subtitles.c:514-556)
// ---------------------------------------------------------------------------

// SubScannerArgs — the video_args_t fields consumed by
// sub_scanner_create. Decoupled from backend/core.VideoArgs because
// pkg/backend/rtmp → pkg/subtitles → pkg/backend/core would cycle.
type SubScannerArgs struct {
	Flags       int
	Title       string // "" mirrors NULL
	IMDB        string
	Filesize    int64
	Year        int
	Season      int
	Episode     int
	HashValid   bool
	OpenSubHash uint64
	SubDBHash   [16]byte
}

func (s *System) SubScannerCreate(url string, proproot *propcore.Prop,
	va *SubScannerArgs, duration int, fam *fileaccesscore.FileAccessManager) *SubScanner {

	if s == nil { // unwired port — C would deref the global
		return nil
	}

	var empty SubScannerArgs
	if va == nil {
		va = &empty
	}

	noscan := va.Title == "" && va.IMDB == "" && !va.HashValid

	fam.TraceSystem().Trace(trace.TRACE_DEBUG, "Subscanner",
		"%s subtitle scan for %s (imdbid:%s) "+
			"year:%d season:%d episode:%d duration:%d opensubhash:%016x",
		map[bool]string{true: "No", false: "Starting"}[noscan],
		orUnknown(va.Title), orUnknown(va.IMDB),
		va.Year, va.Season, va.Episode, duration, va.OpenSubHash)

	if noscan {
		return nil
	}

	ss := &SubScanner{
		url:         url,
		beFlags:     va.Flags,
		title:       va.Title,
		imdbID:      va.IMDB,
		fsize:       uint64(va.Filesize),
		year:        va.Year,
		season:      va.Season,
		episode:     va.Episode,
		duration:    duration,
		fam:         fam,
		ts:          fam.TraceSystem(),
		sys:         s,
		hashValid:   va.HashValid,
		opensubHash: va.OpenSubHash,
	}
	ss.subDBHash = va.SubDBHash
	ss.refcount.Store(2) // one for thread, one for caller
	if proproot != nil {
		if pm := proproot.Manager(); pm != nil {
			pm.RefInc(proproot)
		}
	}
	ss.propRoot = proproot

	// C: hts_thread_create_detached("subscanner", sub_scanner_thread,
	//     ss, THREAD_PRIO_METADATA)
	go ss.subScannerThread()

	return ss
}

func orUnknown(s string) string {
	if s == "" {
		return "<unknown>"
	}
	return s
}

// ---------------------------------------------------------------------------
// sub_scanner_destroy (subtitles.c:561-572)
// ---------------------------------------------------------------------------

func SubScannerDestroy(ss *SubScanner) {
	if ss == nil {
		return
	}
	ss.stop.Store(true)
	ss.mutex.Lock()
	if ss.propRoot != nil {
		if pm := ss.propRoot.Manager(); pm != nil {
			pm.RefDec(ss.propRoot)
		}
		ss.propRoot = nil
	}
	ss.mutex.Unlock()
	SubScannerRelease(ss)
}

// ---------------------------------------------------------------------------
// subtitle_provider_handle_move (subtitles.c:576-597)
// ---------------------------------------------------------------------------

func (s *System) subtitleProviderHandleMove(sp, before *SubtitleProvider) {
	for i, x := range s.providers {
		if x == sp {
			s.providers = slices.Delete(s.providers, i, i+1)
			break
		}
	}

	if before != nil {
		for i, x := range s.providers {
			if x == before {
				s.providers = append(s.providers, nil)
				copy(s.providers[i+1:], s.providers[i:])
				s.providers[i] = sp
				break
			}
		}
	} else {
		s.providers = append(s.providers, sp)
	}

	prio := 0
	for _, p := range s.providers {
		prio++
		p.prio = prio
		if s.cfg != nil {
			c := s.cfg.GetMap(p.id)
			if c != nil {
				c.DeleteField("prio")
				c.AddU32("prio", uint32(p.prio))
			}
		}
	}
	if s.cfg != nil {
		if gs := s.store; gs != nil {
			gs.Save(s.cfg, "subtitleproviders")
		}
	}
}

// ---------------------------------------------------------------------------
// subtitle_settings_group_cb (subtitles.c:602-623)
// ---------------------------------------------------------------------------

func (s *System) subtitleSettingsGroupCb(opaque any, eventType propcore.EventType,
	args ...any) {
	switch eventType {
	case propcore.EventReqMoveChild:
		if len(args) < 3 {
			return
		}
		p1, _ := args[0].(*propcore.Prop)
		p2, _ := args[2].(*propcore.Prop)
		if p1 == nil {
			return
		}
		pm := p1.Manager()
		if pm == nil {
			return
		}
		sp1, _ := pm.TagGet(p1, "subtitle_providers").(*SubtitleProvider)
		var sp2 *SubtitleProvider
		if p2 != nil {
			sp2, _ = pm.TagGet(p2,
				"subtitle_providers").(*SubtitleProvider)
		}
		s.subtitleProviderHandleMove(sp1, sp2)
		pm.Move(p1, p2)
	}
}

// ---------------------------------------------------------------------------
// parse_bgr (subtitles.c:628-647) — canonical version lives in
// subtitles_providers.go as ParseBgr.
// ---------------------------------------------------------------------------

// set_subtitle_color / set_subtitle_shadow_color /
// set_subtitle_outline_color (subtitles.c:650-666)

func (s *System) setSubtitleColor(opaque any, str string) {
	s.settings.color = ParseBgr(str)
}

func (s *System) setSubtitleShadowColor(opaque any, str string) {
	s.settings.shadowColor = ParseBgr(str)
}

func (s *System) setSubtitleOutlineColor(opaque any, str string) {
	s.settings.outlineColor = ParseBgr(str)
}

// set_central_dir (subtitles.c:668-674)
func (s *System) setCentralDir(opaque any, str string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.centralPath = str
}

// ---------------------------------------------------------------------------
// subtitles_init_settings (subtitles.c:681-795)
// ---------------------------------------------------------------------------

func (s *System) subtitlesSetupSettings(pc *propcore.PropConcat) {
	sprop := s.createRoot()
	if pc != nil {
		if pm := sprop.Manager(); pm != nil {
			pc.AddSource(pm.CreateEx(sprop, "nodes", nil, false, false), nil)
		}
	}

	createSep := func(title string) {
		if s.sm != nil {
			s.sm.SettingCreate(settingscore.SettingSeparator, sprop, 0,
				settingscore.SettingTagTitle, pstr(title))
		}
	}

	// ----------------------------------------------------------

	createSep("Central subtitle folder")

	if s.sm != nil {
		s.sm.SettingCreate(settingscore.SettingString, sprop,
			settingscore.SettingsInitialUpdate|settingscore.SettingsDir,
			settingscore.SettingTagTitle, pstr("Path to central folder"),
			settingscore.SettingTagCallback,
			func(opaque any, v any) {
				vs, _ := v.(string)
				s.setCentralDir(nil, vs)
			}, nil,
			settingscore.SettingTagStore, "subtitles", "subtitlefolder")

		createSep("Subtitle size and positioning")

		s.settings.ScalingSetting = s.sm.SettingCreate(
			settingscore.SettingInt, sprop, settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Subtitle size"),
			settingscore.SettingTagValue, 100,
			settingscore.SettingTagRange, 30, 500,
			settingscore.SettingTagStep, 5,
			settingscore.SettingTagUnitCStr, "%",
			settingscore.SettingTagStore, "subtitles", "scale",
			settingscore.SettingTagValueOrigin, "global")

		s.settings.AlignOnVideoSetting = s.sm.SettingCreate(
			settingscore.SettingBool, sprop, settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle,
			pstr("Force subtitles to reside on video frame"),
			settingscore.SettingTagStore, "subtitles", "subonvideoframe",
			settingscore.SettingTagValueOrigin, "global")

		s.sm.SettingCreate(settingscore.SettingMultiOpt, sprop,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Subtitle position"),
			settingscore.SettingTagStore, "subtitles", "align",
			settingscore.SettingTagWriteInt, &s.settings.alignment,
			settingscore.SettingTagOption, "2", pstr("Center"),
			settingscore.SettingTagOption, "1", pstr("Left"),
			settingscore.SettingTagOption, "3", pstr("Right"),
			settingscore.SettingTagOption, "0", pstr("Auto"))

		s.settings.VerticalDisplacementSetting = s.sm.SettingCreate(
			settingscore.SettingInt, sprop, settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle,
			pstr("Subtitle vertical displacement"),
			settingscore.SettingTagRange, -300, 300,
			settingscore.SettingTagStep, 5,
			settingscore.SettingTagUnitCStr, "px",
			settingscore.SettingTagStore, "subtitles", "vdisplace",
			settingscore.SettingTagValueOrigin, "global")

		s.settings.HorizontalDisplacementSetting = s.sm.SettingCreate(
			settingscore.SettingInt, sprop, settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle,
			pstr("Subtitle horizontal displacement"),
			settingscore.SettingTagRange, -300, 300,
			settingscore.SettingTagStep, 5,
			settingscore.SettingTagUnitCStr, "px",
			settingscore.SettingTagStore, "subtitles", "hdisplace",
			settingscore.SettingTagValueOrigin, "global")

		s.sm.SettingCreate(settingscore.SettingString, sprop,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Color"),
			settingscore.SettingTagValue, "FFFFFF",
			settingscore.SettingTagStore, "subtitles", "color",
			settingscore.SettingTagCallback,
			func(opaque any, v any) {
				vs, _ := v.(string)
				s.setSubtitleColor(nil, vs)
			}, nil)

		s.sm.SettingCreate(settingscore.SettingString, sprop,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Shadow color"),
			settingscore.SettingTagValue, "000000",
			settingscore.SettingTagStore, "subtitles", "shadowcolor",
			settingscore.SettingTagCallback,
			func(opaque any, v any) {
				vs, _ := v.(string)
				s.setSubtitleShadowColor(nil, vs)
			}, nil)

		s.sm.SettingCreate(settingscore.SettingInt, sprop,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Shadow offset"),
			settingscore.SettingTagValue, 2,
			settingscore.SettingTagRange, 0, 10,
			settingscore.SettingTagStep, 1,
			settingscore.SettingTagUnitCStr, "px",
			settingscore.SettingTagStore, "subtitles", "shadowcolorsize",
			settingscore.SettingTagWriteInt, &s.settings.shadowDisplacement)

		s.sm.SettingCreate(settingscore.SettingString, sprop,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Outline color"),
			settingscore.SettingTagValue, "000000",
			settingscore.SettingTagStore, "subtitles", "outlinecolor",
			settingscore.SettingTagCallback,
			func(opaque any, v any) {
				vs, _ := v.(string)
				s.setSubtitleOutlineColor(nil, vs)
			}, nil)

		s.sm.SettingCreate(settingscore.SettingInt, sprop,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Outline size"),
			settingscore.SettingTagValue, 1,
			settingscore.SettingTagRange, 0, 4,
			settingscore.SettingTagStep, 1,
			settingscore.SettingTagUnitCStr, "px",
			settingscore.SettingTagStore, "subtitles", "shadowoutlinesize",
			settingscore.SettingTagWriteInt, &s.settings.outlineSize)

		s.sm.SettingCreate(settingscore.SettingBool, sprop,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, pstr("Ignore embedded styling"),
			settingscore.SettingTagStore, "subtitles", "styleoverride",
			settingscore.SettingTagWriteInt, &s.settings.styleOverride)
	}
}

// ---------------------------------------------------------------------------
// subtitles_init_providers (subtitles.c:802-868)
// ---------------------------------------------------------------------------

func (s *System) subtitlesSetupProviders(pc *propcore.PropConcat) {
	// C: sp_cfg = htsmsg_store_load("subtitleproviders") ?:
	//     htsmsg_create_map()
	s.cfg = nil
	if gs := s.store; gs != nil {
		if m, err := gs.Load("subtitleproviders"); err == nil {
			s.cfg = m
		}
	}
	if s.cfg == nil {
		s.cfg = htsmsg.NewMap()
	}

	d := s.createRoot()

	if pm := d.Manager(); pm != nil {
		metadata := pm.CreateEx(d, "metadata", nil, false, false)
		titleProp := pm.CreateEx(metadata, "title", nil, false, false)
		// C: prop_link(_p("Providers for Subtitles"),
		//     prop_create(prop_create(d, "metadata"), "title"))
		pm.Link(pstr("Providers for Subtitles"), titleProp,
			nil, false, false)
		// C: prop_set(d, "type", PROP_SET_STRING, "separator")
		if tp := pm.CreateEx(d, "type", nil, false, false); tp != nil {
			tp.SetString("separator")
		}
	}

	c := s.createRoot()
	s.settingsGroup = c

	var n *propcore.Prop
	if pm := c.Manager(); pm != nil {
		n = pm.CreateEx(c, "nodes", nil, false, false)
	}

	if pc != nil {
		pc.AddSource(n, d)
	}

	if n != nil {
		if pm := n.Manager(); pm != nil {
			// C: prop_subscribe(PROP_TAG_CALLBACK,
			//     subtitle_settings_group_cb, PROP_TAG_MUTEX,
			//     &subtitle_provider_mutex, PROP_TAG_ROOT, n)
			n.Subscribe(func(opaque any,
				eventType propcore.EventType, args ...any) {
				s.mu.Lock()
				s.subtitleSettingsGroupCb(opaque, eventType, args...)
				s.mu.Unlock()
			}, nil)
		}
	}

	// ------------------------------------------------

	s.spEmbedded = s.subtitleProviderCreate("showtime_embedded_subs",
		pstr("Subtitles embedded in video file"),
		400000, "video", 1, 1)

	// ------------------------------------------------

	s.spSameFilename = s.subtitleProviderCreate("showtime_same_filename",
		pstr("Subtitles with matching filename in same folder as video"),
		500000, "subtitle", 1, 1)

	// ------------------------------------------------

	s.spAnyFilename = s.subtitleProviderCreate("showtime_any_filename",
		pstr("Any subtitle in same folder as video"),
		501000, "subtitle", 0, 1)

	// ------------------------------------------------

	s.spCentralDirSameFilename = s.subtitleProviderCreate(
		"showtime_central_dir_same_filename",
		pstr("Subtitles with matching filename in central folder"),
		502000, "subtitle", 1, 1)

	// ------------------------------------------------

	s.spCentralDirAnyFilename = s.subtitleProviderCreate(
		"showtime_central_dir_any_filename",
		pstr("Any subtitle in central folder"),
		503000, "subtitle", 0, 1)

	prio := 0
	for _, sp := range s.providers {
		prio++
		sp.prio = prio
	}
}

// ---------------------------------------------------------------------------
// subtitles_init (subtitles.c:873-887)
// ---------------------------------------------------------------------------

func (s *System) SubtitlesStart() {
	// C: TAILQ_INIT(&subtitle_providers);
	//    hts_mutex_init(&subtitle_provider_mutex);
	s.providers = nil

	var sprop *propcore.Prop
	if s.sm != nil {
		sprop = s.sm.AddDir(nil, pstr("Subtitles"), "subtitle", "",
			pstr("Generic settings for video subtitles"),
			"settings:subtitles")
	}

	var pc *propcore.PropConcat
	if sprop != nil {
		if pm := sprop.Manager(); pm != nil {
			pc = propcore.PropConcatCreate(pm,
				pm.CreateEx(sprop, "nodes", nil, false, false))
		}
	}

	s.subtitlesSetupProviders(pc)
	s.subtitlesSetupSettings(pc)
}

// sortStrings — C's fa_dir iterates entries RB-sorted by filename;
// Go's map needs an explicit ordering pass.
func sortStrings(s []string) {
	slices.Sort(s)
}
