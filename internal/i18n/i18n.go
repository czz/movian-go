package i18n

import (
	charset "github.com/czz/movian-go/internal/charset"
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/czz/movian-go/internal/trace"

	"github.com/czz/movian-go/internal/app"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/settings"
)

// C: main.h:283-286 — gconf.time_format_system values.
const (
	TimeFormatUnset = 0
	TimeFormat24    = 1
	TimeFormat12    = 2
)

// C: gconf.time_format_system (main.h:289) — gcfg.TimeFormatSystem.
// Set by Android coreInit from Java's is24HourFormat; 0 on desktop
// means "no system default" (the "System default" option is hidden
// upstream: SETTING_OPTION(gconf.time_format_system ? "0" : NULL, ...)).

// I18N represents the internationalization system
type I18N struct {
	ts *trace.TraceSystem // C: trace() global — injected

	// updateClockProps — C: callout_update_clock_props (callout.c),
	// invoked by set_timezone after tzset(). Wired via SetUpdateClockProps.
	updateClockProps func()

	langAudio      [3]string
	langSubtitle   [3]string
	defaultCharset *charset.Charset // C: const charset_t *default_charset

	mutex sync.RWMutex

	initialized    bool
	currentLang    string
	availableLangs []Language

	settingsMgr *settings.SettingsManager
	httpServer  *httpnet.HTTPServer
	pm          *propcore.PropManager

	// fam — C: implicit global fileaccess manager; injected so
	// .lang scans/loads work on bundled dataroots (bundle:// has
	// no filesystem presence). Nil → os.ReadDir/os.ReadFile fallback.
	fam *facore.FileAccessManager

	// gconf — C: gconf_t fields i18n reads/writes
	// (ignore_the_prefix binding, time_format_system). Injected.
	gconf *gconf.T

	// pluginLangDirs — registered <plugin>/lang directories.
	// Go extension: upstream C (i18n.c::set_language) loaded only
	// <dataroot>/lang/<lang>.lang; plugins had no translation
	// support. Their files merge into the same NLS table.
	pluginLangDirs []string
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (i *I18N) SetGconf(g *gconf.T) { i.gconf = g }

// SetFileAccessManager injects the fileaccess manager (C: the global
// fap registry — nls_init scans <dataroot>/lang via fa_scandir).
func (i *I18N) SetFileAccessManager(fam *facore.FileAccessManager) {
	i.fam = fam
}

// gcfg — C: gconf_t field reads; nil-safe for unwired/test paths.
func (i *I18N) gcfg() *gconf.T {
	if i.gconf == nil {
		i.gconf = gconf.New()
	}
	return i.gconf
}

// SetTraceSystem injects the trace system (C: trace() global).
func (i *I18N) SetTraceSystem(ts *trace.TraceSystem) { i.ts = ts }

// Language represents a language option
type Language struct {
	ID   string
	Name string
}

// Charset — C: charset_t from misc/str.h (defined in misc package).
type Charset = charset.Charset

// NewI18N creates a new i18n system instance
// SetUpdateClockProps wires callout_update_clock_props (C: callout.c —
// invoked by set_timezone after tzset).
func (i *I18N) SetUpdateClockProps(fn func()) { i.updateClockProps = fn }

func NewI18N(settingsMgr *settings.SettingsManager, pm *propcore.PropManager) *I18N {
	return &I18N{
		langAudio:      [3]string{"", "", ""},
		langSubtitle:   [3]string{"", "", ""},
		initialized:    false,
		currentLang:    "en",
		availableLangs: []Language{{ID: "none", Name: "English (default)"}},
		settingsMgr:    settingsMgr,
		pm:             pm,
	}
}

// Start initializes the i18n system
// C: i18n_init (i18n.c:88) — does NOT hold nls_mutex; setting callbacks
// (set_language → nls_clear) lock it internally. Holding it here deadlocks
// the initial-update callback on the same thread.
func (i *I18N) Start() {
	if i.initialized {
		return
	}

	// Initialize language arrays
	for idx := range i.langAudio {
		i.langAudio[idx] = ""
		i.langSubtitle[idx] = ""
	}
	i.defaultCharset = nil // C: "auto" -> NULL
	i.currentLang = "en"

	// Create settings directory only if settings manager is available
	var s *propcore.Prop
	if i.settingsMgr != nil {
		s = i.settingsMgr.AddDir(nil, i.settingsMgr.P("Languages"), "i18n", "", i.settingsMgr.P("Preferred languages"), "settings:i18n")
		i.nlsSetup(s)
	}

	i.initialized = true
}

// Fini finalizes the i18n system
func (i *I18N) Fini() {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	if !i.initialized {
		return
	}

	i.initialized = false
}

// setLang sets a language code
// CurrentLang returns the current UI language — C: gconf.lang.
func (i *I18N) CurrentLang() string {
	return i.currentLang
}

func (i *I18N) setLang(opaque any, str string) {
	s, ok := opaque.(*string)
	if !ok {
		return
	}
	if str == "" {
		*s = ""
		return
	}

	if len(str) > 3 {
		*s = str[:3]
	} else {
		*s = str
	}
}

// setDefaultCharset sets the default character set
func (i *I18N) setDefaultCharset(opaque any, str string) {
	// C: i18n.c set_default_charset — "auto"/"" -> NULL, else charset_get()
	if str == "" || str == "auto" {
		i.defaultCharset = nil
		return
	}
	cs := charset.CharsetGet(str)
	if cs != nil {
		i.defaultCharset = cs
	}
}

// setTimezone — C: set_timezone (i18n.c:77-83), #ifdef STOS.
// setenv("TZ", ...); tzset(); callout_update_clock_props().
func (i *I18N) setTimezone(opaque any, timezone string) {
	os.Setenv("TZ", timezone) // C: setenv("TZ", timezone, 1)
	// C: tzset() — Go's time.Local caches the zone on first use; the
	//    mgos path runs before any clock formatting so the env wins.
	if i.updateClockProps != nil {
		i.updateClockProps()
	}
}

// nlsSetup initializes the NLS system
func (i *I18N) nlsSetup(parent *propcore.Prop) {
	// If parent is nil, skip settings creation
	if parent == nil {
		return
	}

	// Scan language directory
	langDir := facore.FAPathjoin(i.getDataRoot(), "lang")

	// Create language options
	langs := []Language{{ID: "none", Name: "English (default)"}}

	// Scan for .lang files — C: fa_scandir(buf2) works on any fap
	// scheme; os.ReadDir only sees real dirs.
	type langFile struct{ name, url string }
	var files []langFile
	if i.fam != nil && strings.Contains(langDir, "://") {
		if fd, err := facore.FAScandir(i.fam, langDir, 0); err == nil && fd != nil {
			for _, e := range fd.Entries {
				files = append(files, langFile{e.Filename, e.URL})
			}
			fd.Free()
		}
	} else if entries, err := os.ReadDir(langDir); err == nil {
		for _, entry := range entries {
			files = append(files, langFile{
				entry.Name(), filepath.Join(langDir, entry.Name())})
		}
	}
	for _, f := range files {
		name := f.name
		if strings.HasSuffix(name, ".lang") && !strings.HasSuffix(name, "~") {
			langID := strings.TrimSuffix(name, ".lang")
			language, native := i.nlsLangMetadata(f.url)
			if language != "" && native != "" {
				displayName := fmt.Sprintf("%s (%s)", native, language)
				langs = append(langs, Language{ID: langID, Name: displayName})
			}
		}
	}

	i.availableLangs = langs

	// Create language setting
	i.settingsMgr.SettingCreate(
		settings.SettingMultiOpt,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Language"),
		settings.SettingTagStore, "i18n", "language",
		settings.SettingTagCallback, i.setLanguage, nil,
		settings.SettingTagOptionList, i.langsToOptionList(langs),
	)

	// C: #ifdef STOS (i18n.c:97-105) — timezone setting between nls_init
	//    and the timeformat multiopt.
	if mgosI18n {
		i.settingsMgr.SettingCreate(
			settings.SettingString,
			parent,
			settings.SettingsInitialUpdate,
			settings.SettingTagTitle, i.settingsMgr.P("Timezone"),
			settings.SettingTagCallback, i.setTimezone, nil,
			settings.SettingTagStore, "i18n", "timezone",
		)
	}

	// Create time format setting
	i.settingsMgr.SettingCreate(
		settings.SettingMultiOpt,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Time format"),
		settings.SettingTagStore, "i18n", "timeformat",
		// C: SETTING_OPTION(gconf.time_format_system ? "0" : NULL, ...)
		// — option absent unless the platform reports a system format.
		settings.SettingTagOption, i.systemTimeFormatOption(), i.settingsMgr.P("System default"),
		settings.SettingTagOption, "1", i.settingsMgr.P("24 Hour"),
		settings.SettingTagOption, "2", i.settingsMgr.P("12 Hour"),
	)

	// Create info text
	i.settingsMgr.CreateInfo(parent, "", i.settingsMgr.P("Language codes should be configured as three character ISO codes, example (eng, swe, fra)"))

	// Create audio language settings
	i.settingsMgr.SettingCreate(
		settings.SettingString,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Primary audio language code"),
		settings.SettingTagCallback, i.setLang, &i.langAudio[0],
		settings.SettingTagStore, "i18n", "audio1",
	)

	i.settingsMgr.SettingCreate(
		settings.SettingString,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Secondary audio language code"),
		settings.SettingTagCallback, i.setLang, &i.langAudio[1],
		settings.SettingTagStore, "i18n", "audio2",
	)

	i.settingsMgr.SettingCreate(
		settings.SettingString,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Tertiary audio language code"),
		settings.SettingTagCallback, i.setLang, &i.langAudio[2],
		settings.SettingTagStore, "i18n", "audio3",
	)

	// Create subtitle language settings
	i.settingsMgr.SettingCreate(
		settings.SettingString,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Primary subtitle language code"),
		settings.SettingTagCallback, i.setLang, &i.langSubtitle[0],
		settings.SettingTagStore, "i18n", "subtitle1",
	)

	i.settingsMgr.SettingCreate(
		settings.SettingString,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Secondary subtitle language code"),
		settings.SettingTagCallback, i.setLang, &i.langSubtitle[1],
		settings.SettingTagStore, "i18n", "subtitle2",
	)

	i.settingsMgr.SettingCreate(
		settings.SettingString,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Tertiary subtitle language code"),
		settings.SettingTagCallback, i.setLang, &i.langSubtitle[2],
		settings.SettingTagStore, "i18n", "subtitle3",
	)

	// Create default charset setting
	charsetOpts := []string{"auto", "Auto"}
	for idx := 0; ; idx++ {
		cs := i.CharsetGetIdx(idx)
		if cs == nil {
			break
		}
		charsetOpts = append(charsetOpts, cs.ID, cs.Title)
	}
	i.settingsMgr.SettingCreate(
		settings.SettingMultiOpt,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Default character set"),
		settings.SettingTagStore, "i18n", "default_charset",
		settings.SettingTagCallback, i.setDefaultCharset, nil,
		settings.SettingTagOptionList, charsetOpts,
	)

	// C: setting_create(SETTING_BOOL, s, SETTINGS_INITIAL_UPDATE,
	//   SETTING_TITLE(_p("Ignore 'The' at beginning of words when sorting")),
	//   SETTING_STORE("i18n", "skipthe"),
	//   SETTING_WRITE_BOOL(&gconf.ignore_the_prefix), NULL)
	// (i18n.c:180-185)
	i.settingsMgr.SettingCreate(
		settings.SettingBool,
		parent,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, i.settingsMgr.P("Ignore 'The' at beginning of words when sorting"),
		settings.SettingTagStore, "i18n", "skipthe",
		settings.SettingTagWriteInt, &i.gcfg().IgnoreThePrefix,
	)
}

// langsToOptionList converts language list to option list
// systemTimeFormatOption — C: gconf.time_format_system ? "0" : NULL
// (i18n.c:110). NULL removes the option upstream; "" matches the Go
// SettingTagOption encoding of "absent".
func (i *I18N) systemTimeFormatOption() string {
	if i.gcfg().TimeFormatSystem != 0 {
		return "0"
	}
	return ""
}

func (i *I18N) langsToOptionList(langs []Language) []string {
	opts := make([]string, 0, len(langs)*2)
	for _, lang := range langs {
		opts = append(opts, lang.ID, lang.Name)
	}
	return opts
}

// setLanguage sets the current language
// setLanguage — C: set_language (i18n.c:534-553). Does NOT hold nls_mutex
// across the body: nls_clear/nls_load_lang lock it internally per-operation.
func (i *I18N) setLanguage(opaque any, str string) {
	i.nlsClear()

	if str == "none" {
		i.currentLang = "en"
	} else {
		i.currentLang = str
		langPath := facore.FAPathjoin(
			facore.FAPathjoin(i.getDataRoot(), "lang"), str+".lang")
		if err := i.nlsLoadLang(langPath); err != nil {
			i.ts.Error("i18n", "Failed to load language: %v\n", err)
		}
	}
	i.loadPluginLangs()

	// C: memcpy(iso639_1, gconf.lang, 3); iso639_1[2] = 0;
	//    prop_setv(prop_get_global(), "i18n", "iso639_1", NULL,
	//              PROP_SET_STRING, iso639_1)
	iso := i.currentLang
	if len(iso) > 2 {
		iso = iso[:2]
	}
	if g := i.pm.GetGlobal(); g != nil {
		if i18n := i.pm.CreateEx(g, "i18n", nil, false, false); i18n != nil {
			i.pm.SetVEx(nil, i18n, "iso639_1", iso)
		}
	}
}

// RegisterPluginLangDir — Go extension: registers a plugin's lang/
// directory so every setLanguage also loads <dir>/<lang>.lang into the
// shared NLS table (same format, same store as dataroot files).
// Missing files are ignored. If a language is already active the
// matching file is loaded immediately.
func (i *I18N) RegisterPluginLangDir(dir string) {
	if i == nil || dir == "" {
		return
	}
	i.mutex.Lock()
	for _, d := range i.pluginLangDirs {
		if d == dir {
			i.mutex.Unlock()
			return
		}
	}
	i.pluginLangDirs = append(i.pluginLangDirs, dir)
	cur := i.currentLang
	i.mutex.Unlock()

	if cur != "" {
		i.loadPluginLangFiles(dir, cur)
	}
}

// loadPluginLangFiles loads a plugin's translations for language cur
// with English as intermediate fallback: en.lang is loaded first, then
// <cur>.lang overwrites it, so untranslated keys fall back to English
// instead of the untranslated id.
func (i *I18N) loadPluginLangFiles(dir, cur string) {
	if cur != "en" {
		i.nlsLoadLangQuiet(facore.FAPathjoin(dir, "en.lang"))
	}
	p := facore.FAPathjoin(dir, cur+".lang")
	if err := i.nlsLoadLang(p); err != nil && i.ts != nil {
		// Missing translation for the current language is normal —
		// only log unexpected read/parse failures.
		if _, serr := os.Stat(p); serr == nil {
			i.ts.Error("i18n", "Failed to load plugin language %s: %v\n", p, err)
		}
	}
}

// nlsLoadLangQuiet loads a .lang file ignoring a missing file (the
// common case for optional fallback layers) — errors on existing
// files still surface.
func (i *I18N) nlsLoadLangQuiet(p string) {
	if err := i.nlsLoadLang(p); err != nil {
		if _, serr := os.Stat(p); serr != nil {
			return // file simply absent
		}
		if i.ts != nil {
			i.ts.Error("i18n", "Failed to load plugin language %s: %v\n", p, err)
		}
	}
}

// loadPluginLangs loads plugin translations for every registered
// plugin lang dir. Called by setLanguage after the dataroot file.
func (i *I18N) loadPluginLangs() {
	i.mutex.RLock()
	dirs := append([]string(nil), i.pluginLangDirs...)
	cur := i.currentLang
	i.mutex.RUnlock()
	for _, d := range dirs {
		i.loadPluginLangFiles(d, cur)
	}
}

// findscore finds the score for a language code
func (i *I18N) findscore(str string, vec [3]string) int {
	if str == "" || str[0] == 0 {
		return 0
	}

	for idx := range 3 {
		if vec[idx] != "" && strings.EqualFold(vec[idx], str) {
			return 100000 * (3 - idx)
		}
	}
	return 0
}

// AudioScore returns the audio language score
func (i *I18N) AudioScore(str string) int {
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	return i.findscore(str, i.langAudio)
}

// SubtitleScore returns the subtitle language score
func (i *I18N) SubtitleScore(str string) int {
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	return i.findscore(str, i.langSubtitle)
}

// SubtitleLang returns the subtitle language at index
func (i *I18N) SubtitleLang(num uint) string {
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	if num < 3 && i.langSubtitle[num] != "" {
		return i.langSubtitle[num]
	}
	return ""
}

// GetDefaultCharset returns the default character set
// C: const charset_t *i18n_get_default_charset(void)
func (i *I18N) GetDefaultCharset() *charset.Charset {
	if i == nil {
		return nil
	}
	i.mutex.RLock()
	defer i.mutex.RUnlock()
	return i.defaultCharset
}

// GetProp returns the prop for a given string key.
// C: nls_get_prop — delegates to pkg/nls, the canonical string store
// shared with the view parser's _() tokens.
func (i *I18N) GetProp(stringKey string) *propcore.Prop {
	return nls.GetProp(stringKey)
}

// CharsetGetIdx returns the charset at the given index
// C: charset_get_idx (misc/str.c)
func (i *I18N) CharsetGetIdx(idx int) *Charset {
	return charset.CharsetGetIdx(uint(idx))
}

// CharsetGet returns the charset with the given ID
// C: charset_get (misc/str.c)
func (i *I18N) CharsetGet(id string) *Charset {
	return charset.CharsetGet(id)
}

// GetString returns the translated string — C: nls_get_rstring.
func (i *I18N) GetString(stringKey string) string {
	return nls.GetRString(stringKey)
}

// GetStringP returns the translated string with plural support —
// C: nls_get_rstringp.
func (i *I18N) GetStringP(stringKey, singular string, val int) string {
	return nls.GetRStringP(stringKey, singular, val)
}

// nlsClear clears all NLS strings — C: nls_clear (i18n.c:423-436).
// Delegates to pkg/nls, the single canonical store.
func (i *I18N) nlsClear() {
	nls.Clear()
}

// nlsLoadFromData loads translations from data.
// C: nls_load_from_data (i18n.c:440-510) — parses id:/msg:/msg[N]: lines;
// values go to the canonical store via nls.LoadTranslation.
func (i *I18N) nlsLoadFromData(data string) {
	// Skip UTF-8 BOM
	data = strings.TrimPrefix(data, "\xef\xbb\xbf")

	lines := strings.Split(data, "\n")
	var key string
	haveKey := false
	loaded := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "id:") {
			key = deescapeCStyle(strings.TrimSpace(line[3:]))
			haveKey = true
			continue
		}

		if !haveKey {
			continue
		}

		if strings.HasPrefix(line, "msg:") {
			value := deescapeCStyle(strings.TrimSpace(line[4:]))
			if value != "" {
				nls.LoadTranslation(key, 0, value)
				loaded++
			}
			continue
		}

		if strings.HasPrefix(line, "msg[") {
			// Parse msg[index]: value
			idxStr := line[4:]
			idxEnd := strings.Index(idxStr, "]")
			if idxEnd == -1 {
				continue
			}
			idx, err := strconv.Atoi(idxStr[:idxEnd])
			if err != nil {
				continue
			}

			colonIdx := strings.Index(idxStr[idxEnd:], ":")
			if colonIdx == -1 {
				continue
			}
			value := deescapeCStyle(strings.TrimSpace(idxStr[idxEnd+colonIdx+1:]))
			if value != "" {
				nls.LoadTranslation(key, idx, value)
				loaded++
			}
		}
	}
}

// readLangData — C: fa_load. Reads a .lang file from any fap scheme
// (bundle:// has no filesystem presence); plain paths use os.ReadFile.
func (i *I18N) readLangData(url string) ([]byte, error) {
	if i.fam != nil && strings.Contains(url, "://") {
		buf, err := facore.FALoad2(i.fam, url, nil)
		if err != nil {
			return nil, err
		}
		if buf == nil {
			return nil, fmt.Errorf("empty load")
		}
		return buf.Data[:buf.Size], nil
	}
	return os.ReadFile(url)
}

// nlsLoadLang loads a language file
func (i *I18N) nlsLoadLang(path string) error {
	data, err := i.readLangData(path)
	if err != nil {
		return fmt.Errorf("unable to load %s: %w", path, err)
	}
	i.nlsLoadFromData(string(data))
	return nil
}

// nlsLangMetadata extracts language metadata from a file
func (i *I18N) nlsLangMetadata(path string) (language, native string) {
	data, err := i.readLangData(path)
	if err != nil {
		return "", ""
	}

	// Skip UTF-8 BOM
	dataStr := strings.TrimPrefix(string(data), "\xef\xbb\xbf")

	lines := strings.SplitSeq(dataStr, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "language:") {
			language = strings.TrimSpace(line[9:])
		}

		if strings.HasPrefix(line, "native:") {
			native = strings.TrimSpace(line[7:])
		}

		if language != "" && native != "" {
			break
		}
	}

	return language, native
}

// deescapeCStyle de-escapes C-style strings
func deescapeCStyle(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				result.WriteByte('\n')
				i += 2
			case 't':
				result.WriteByte('\t')
				i += 2
			case 'r':
				result.WriteByte('\r')
				i += 2
			case '"':
				result.WriteByte('"')
				i += 2
			case '\\':
				result.WriteByte('\\')
				i += 2
			default:
				result.WriteByte(s[i])
				i++
			}
		} else {
			result.WriteByte(s[i])
			i++
		}
	}
	return result.String()
}

// getDataRoot returns the data root directory.
// Equivalent to C's app_dataroot().
func (i *I18N) getDataRoot() string {
	return app.AppDataRoot()
}

// SetHTTPServer sets the HTTP server for upload translation support
func (i *I18N) SetHTTPServer(server *httpnet.HTTPServer) {
	i.httpServer = server
	if server != nil {
		// Register the endpoint after the HTTP server becomes available.
		server.HTTPPathAdd("/api/translation", i, i.uploadTranslationHandler, true)
	}
}

// uploadTranslationHandler handles HTTP upload of translation files
func (i *I18N) uploadTranslationHandler(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	if method != httpnet.HTTPCmdPost {
		// Return upload form for GET requests
		html := `<!DOCTYPE html>
<html>
<head>
	<title>Upload Translation</title>
</head>
<body>
	<h1>Upload Translation File</h1>
	<form method="POST" enctype="multipart/form-data">
		<p>Select .lang file to upload:</p>
		<input type="file" name="langfile" accept=".lang">
		<br><br>
		<input type="submit" name="submit" value="Submit">
	</form>
</body>
</html>`
		return hc.HTTPSendReply(http.StatusOK, "text/html", "", "", 0, []byte(html))
	}

	// Handle POST - parse multipart form
	contentType := hc.HTTPArgGetHdr("Content-Type")
	if contentType == "" {
		return hc.HTTPError(http.StatusBadRequest, "Missing Content-Type header")
	}

	// Get POST data
	size := 0
	postData := hc.HTTPGetPostData(&size, false)
	if postData == nil || size == 0 {
		return hc.HTTPError(http.StatusBadRequest, "No POST data")
	}

	// Parse multipart form
	reader := multipart.NewReader(bytes.NewReader(postData), contentType[strings.Index(contentType, "boundary=")+9:])

	var langFileData []byte
	var filename string

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return hc.HTTPError(http.StatusBadRequest, "Failed to parse multipart form: %v", err)
		}

		if part.FormName() == "langfile" {
			filename = part.FileName()
			if filename == "" {
				part.Close()
				continue
			}

			// Read file data
			var buf bytes.Buffer
			if _, err := io.Copy(&buf, part); err != nil {
				part.Close()
				return hc.HTTPError(http.StatusBadRequest, "Failed to read file: %v", err)
			}
			langFileData = buf.Bytes()
		}
		part.Close()
	}

	if langFileData == nil {
		return hc.HTTPError(http.StatusBadRequest, "No lang file uploaded")
	}

	// Validate filename has .lang extension
	if !strings.HasSuffix(filename, ".lang") {
		return hc.HTTPError(http.StatusBadRequest, "File must have .lang extension")
	}

	// Extract language ID from filename
	langID := strings.TrimSuffix(filename, ".lang")
	if langID == "" {
		return hc.HTTPError(http.StatusBadRequest, "Invalid filename")
	}

	// Save file to lang directory
	langDir := filepath.Join(i.getDataRoot(), "lang")
	if err := os.MkdirAll(langDir, 0755); err != nil {
		return hc.HTTPError(http.StatusInternalServerError, "Failed to create lang directory: %v", err)
	}

	langPath := filepath.Join(langDir, filename)
	if err := os.WriteFile(langPath, langFileData, 0644); err != nil {
		return hc.HTTPError(http.StatusInternalServerError, "Failed to save file: %v", err)
	}

	// Reload translations — nlsClear/nlsLoadLang lock i.mutex internally;
	// holding it here would deadlock (same fix as setLanguage).
	i.nlsClear()

	// Load new language file
	if err := i.nlsLoadLang(langPath); err != nil {
		return hc.HTTPError(http.StatusInternalServerError, "Failed to load translation: %v", err)
	}

	// Update current language
	i.currentLang = langID

	// Return success response
	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<title>Translation Uploaded</title>
</head>
<body>
	<h1>Translation Uploaded Successfully</h1>
	<p>Language: %s</p>
	<p>File: %s</p>
	<p><a href="/api/translation">Upload another translation</a></p>
</body>
</html>`, langID, filename)
	return hc.HTTPSendReply(http.StatusOK, "text/html", "", "", 0, []byte(html))
}
