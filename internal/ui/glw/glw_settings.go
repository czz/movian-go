package glw

// C: src/ui/glw/glw_settings.c — canonical 1:1 port.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	eventpkg "github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/metadata"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: glw_settings_t (glw_settings.h:23-48)
type glwSettingsT struct {
	gsSize                int
	gsUnderscanH          int
	gsUnderscanV          int
	gsWrap                int
	gsMapMouseWheelToKeys int

	gsScreensaverDelay int
	gsBingImage        int

	gsSettingSize             *settingscore.Setting
	gsSettingUnderscanV       *settingscore.Setting
	gsSettingUnderscanH       *settingscore.Setting
	gsSettingWrap             *settingscore.Setting
	gsSettingWheelMapping     *settingscore.Setting
	gsSettingCustomBg         *settingscore.Setting
	gsSettingSkin             *settingscore.Setting // extension, no C counterpart
	gsSettingScreensaverTimer *settingscore.Setting
	gsSettingBingImage        *settingscore.Setting
	gsSettingUserImages       *settingscore.Setting
	gsSettingPerImageTimeout  *settingscore.Setting

	gsSettings *propcore.Prop

	// C: static screensaver items/userImages + mutex (glw_settings.c)
	gsScreensaverItems *propcore.Prop
	gsScreensaverMu    sync.Mutex
	gsUserImages       string
}

// C: screensaver_item_t
type screensaverItem struct {
	url  string
	info string
	root *propcore.Prop
}

// C: screensaver_scanner_t
type screensaverScanner struct {
	numScanned int
	maxOutput  int
	rng        miscpkg.Prng
	output     []screensaverItem
}

// GlwSettingsSetDeps wires the global deps before GlwSettingsStart
// (C: gconf.settings_look_and_feel + setting_create + fa_load/fa_scandir
// implicit globals — folded into glwDeps, the glw dep seam).
func GlwSettingsSetDeps(sm *settingscore.SettingsManager, fam *facore.FileAccessManager) {
	glwDeps.settingsSM = sm
	glwDeps.fam = fam
}

// screensaverEmitItems — C: screensaver_emit_items (glw_settings.c:59)
func screensaverEmitItems(ss *screensaverScanner) {
	for i := range ss.numScanned {
		r := int(miscpkg.PrngGet(&ss.rng)) % ss.numScanned
		ss.output[i], ss.output[r] = ss.output[r], ss.output[i]
	}

	glwDeps.pm.DestroyChilds(glwDeps.settings.gsScreensaverItems)

	for i := range ss.numScanned {
		si := &ss.output[i]
		item := glwDeps.pm.CreateRootEx("", false)
		glwDeps.pm.SetVEx(nil, item, "url", si.url)
		glwDeps.pm.SetVEx(nil, item, "info", si.info)
		if glwDeps.pm.SetParentEx(item, glwDeps.settings.gsScreensaverItems, nil, "") != 0 {
			glwDeps.pm.Destroy(item)
		} else {
			si.root = item
		}
	}
}

// screensaverAddItem — C: screensaver_add_item (glw_settings.c:89)
// Reservoir sampling: keep maxOutput items from a stream of unknown length.
func screensaverAddItem(url, info string, ss *screensaverScanner) {
	if ss.numScanned < ss.maxOutput {
		ss.output[ss.numScanned].url = url
		ss.output[ss.numScanned].info = info
		ss.numScanned++
		if ss.numScanned == ss.maxOutput {
			screensaverEmitItems(ss)
		}
		return
	}

	j := int(miscpkg.PrngGet(&ss.rng)) % ss.numScanned
	if j < ss.maxOutput {
		ss.output[j].url = url
		ss.output[j].info = info
	}
	ss.numScanned++
}

// bingImages — C: bing_images (glw_settings.c:115)
func bingImages(ss *screensaverScanner) {
	url := "http://www.bing.com/HPImageArchive.aspx?format=js&n=8"
	b, err := facore.FALoad2(glwDeps.fam, url, &facore.FALoadArgs{
		Flags: facore.FaDisableAuth | facore.FaCompression | facore.FaNoCookies,
	})
	if err != nil || b == nil {
		msg := "load failed"
		if err != nil {
			msg = err.Error()
		}
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "Screensaver",
			"Unable to load images -- %s", msg)
		return
	}

	doc, err := htsmsg.DeserializeJSON(string(b.Data[:b.Size]))
	if err != nil || doc == nil {
		return
	}
	list := doc.GetList("images")
	if list != nil {
		for _, f := range list.GetFields() {
			m := f.GetMap()
			if m == nil {
				continue
			}
			s := m.GetStr("url")
			if s == "" {
				continue
			}
			screensaverAddItem("http://www.bing.com"+s, m.GetStr("copyright"), ss)
		}
	}
	doc.Release()
}

// isProbablyImage — C: is_probably_image (glw_settings.c:165)
func isProbablyImage(filename string) bool {
	i := strings.LastIndexByte(filename, '.')
	if i < 0 {
		return false
	}
	e := strings.ToLower(filename[i+1:])
	// webp is a Go extension — upstream C list lacks it.
	return e == "png" || e == "bmp" || e == "gif" || e == "jpg" ||
		e == "jpeg" || e == "webp"
}

// screensaverLoadPath — C: screensaver_load_path (glw_settings.c:182)
func screensaverLoadPath(ss *screensaverScanner, path string) {
	fd, _ := facore.FAScandir(glwDeps.fam, path, 0)
	if fd == nil {
		return
	}
	defer fd.Free()

	if fd.Count <= 0 {
		return
	}

	// C iterates an RB-tree; order is irrelevant here — the scan is a
	// reservoir sample over all reachable entries.
	entries := make([]*facore.DirEntry, 0, fd.Count)
	for _, fde := range fd.Entries {
		entries = append(entries, fde)
	}

	skip := int(miscpkg.PrngGet(&ss.rng)) % fd.Count
	for i := range fd.Count {
		fde := entries[(skip+i)%len(entries)]
		if metadata.ContentDirish(metadata.ContentType(fde.Type)) {
			screensaverLoadPath(ss, fde.URL)
		} else if isProbablyImage(fde.URL) {
			screensaverAddItem(fde.URL, "", ss)
		}
	}
}

// screensaverLoadUserImages — C: screensaver_load_user_images (glw_settings.c:221)
func screensaverLoadUserImages(ss *screensaverScanner) {
	glwDeps.settings.gsScreensaverMu.Lock()
	path := glwDeps.settings.gsUserImages
	glwDeps.settings.gsScreensaverMu.Unlock()
	if path == "" {
		return
	}
	glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Scanning %s for screen saver images", path)
	screensaverLoadPath(ss, path)
}

// setupScreensaverItemsLoad — C: init_screensaver_items_load (glw_settings.c:243)
func setupScreensaverItemsLoad(opaque any, event propcore.EventType, args ...any) {
	if event != propcore.EventSubscriptionMonitorActive {
		return
	}

	var ss screensaverScanner
	miscpkg.PrngSeed2(&ss.rng) // C: prng_init2(&ss.rng)
	ss.numScanned = 0
	ss.maxOutput = 200
	ss.output = make([]screensaverItem, ss.maxOutput)

	if glwDeps.settings.gsBingImage != 0 {
		bingImages(&ss)
	}
	screensaverLoadUserImages(&ss)

	if ss.numScanned < ss.maxOutput {
		screensaverEmitItems(&ss)
	} else {
		// Don't overwrite first item as it's already displaying
		for i := 1; i < ss.maxOutput; i++ {
			si := &ss.output[i]
			glwDeps.pm.SetVEx(nil, si.root, "url", si.url)
			glwDeps.pm.SetVEx(nil, si.root, "info", si.info)
		}
	}
}

// setupScreensaverItems — C: init_screensaver_items (glw_settings.c:284)
func setupScreensaverItems() {
	glwDeps.settings.gsScreensaverItems = glwDeps.pm.CreateMultiPath(glwDeps.pm.GetGlobal(),
		"glw", "screensaver", "items")
	glwDeps.pm.Subscribe(glwDeps.settings.gsScreensaverItems, setupScreensaverItemsLoad, nil,
		propcore.SubFlagSubscriptionMonitor)
}

// setScreensaverImageFolder — C: set_screensaver_image_folder (glw_settings.c:300)
func setScreensaverImageFolder(opaque any, value any) {
	glwDeps.settings.gsScreensaverMu.Lock()
	defer glwDeps.settings.gsScreensaverMu.Unlock()
	if str, ok := value.(string); ok {
		glwDeps.settings.gsUserImages = str
	}
}

// glwDefaultSkin — the path glwStart4 falls back to when gconf.Skin
// is empty (root.go:266-269). Extension helper for the skin multiopt.
func glwDefaultSkin() string {
	if glwDeps.appDataroot == nil {
		return "flat" // C: SHOWTIME_GLW_DEFAULT_SKIN
	}
	return fmt.Sprintf("%s/glwskins/%s",
		glwDeps.appDataroot(), "flat")
}

// glwScanSkins — extension, no C counterpart. Lists skin directories
// (dirs containing universe.view) in the bundled <dataroot>/glwskins
// and in the user dir <persistent>/glwskins so dropped-in skins are
// selectable without a restart. Returns (optionID, title) pairs;
// optionID is the full skin path.
func glwScanSkins() [][2]string {
	var out [][2]string
	var dirs [][2]string
	if glwDeps.appDataroot != nil {
		dirs = append(dirs,
			[2]string{facore.FAPathjoin(glwDeps.appDataroot(), "glwskins"), ""})
	}
	if glwDeps.gconf != nil && glwDeps.gconf.PersistentPath != "" {
		dirs = append(dirs,
			[2]string{facore.FAPathjoin(glwDeps.gconf.PersistentPath, "glwskins"), " (user)"})
	}
	for _, d := range dirs {
		if strings.Contains(d[0], "://") {
			// Scheme URL (bundle://, persistent://): the dataroot is
			// not a filesystem path on bundled builds — scan through
			// fileaccess like C's fa_scandir. Option id is the URL.
			if glwDeps.fam == nil {
				continue
			}
			fd, err := facore.FAScandir(glwDeps.fam, d[0], 0)
			if err != nil || fd == nil {
				continue
			}
			for _, e := range fd.Entries {
				if e.Type != facore.ContentDir {
					continue
				}
				st, err := facore.Stat(glwDeps.fam,
					facore.FAPathjoin(e.URL, "universe.view"))
				if err != nil || st == nil || st.Type != facore.ContentFile {
					continue
				}
				out = append(out, [2]string{e.URL, e.Filename + d[1]})
			}
			fd.Free()
			continue
		}
		entries, err := os.ReadDir(d[0])
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(d[0], e.Name())
			if _, err := os.Stat(filepath.Join(p, "universe.view")); err != nil {
				continue
			}
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			out = append(out, [2]string{p, e.Name() + d[1]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][1] < out[j][1] })
	return out
}

// glwSkinUsable — extension helper: a skin dir is selectable only when
// it contains universe.view. Scheme URLs go through fileaccess
// (bundle:// has no filesystem presence on bundled builds).
func glwSkinUsable(skin string) bool {
	if strings.Contains(skin, "://") {
		if glwDeps.fam == nil {
			return false
		}
		st, err := facore.Stat(glwDeps.fam,
			facore.FAPathjoin(skin, "universe.view"))
		return err == nil && st != nil && st.Type == facore.ContentFile
	}
	_, err := os.Stat(filepath.Join(skin, "universe.view"))
	return err == nil
}

// glwSkinSwitch — extension, no C counterpart. Multiopt callback:
// resolves the option id ("default" → default skin path), stores it
// in gconf.Skin (so it survives restarts and drives glwStart4 when it
// fires during initial update, before the UI starts), and hot-reloads
// the running universe through the canonical ACTION_RELOAD_UI path
// (glw_inject_event → glw_dispatch_event → glw_load_universe).
func glwSkinSwitch(opaque any, value any) {
	skin, _ := value.(string)
	if skin == "default" {
		skin = ""
	} else if !glwSkinUsable(skin) {
		glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
			"Skin %s has no universe.view — keeping current", skin)
		return
	}
	glwDeps.gconf.Skin = skin

	gr := glwDeps.activeRoot
	if gr == nil {
		return // initial update before glwStart4 — gconf.Skin is enough
	}
	if skin == "" {
		skin = glwDefaultSkin()
	}
	gr.grSkin = skin
	glwDeps.pm.SetVEx(nil, gr.grPropUi, "skin.path", skin)
	e := glwDeps.em.CreateAction(eventpkg.ACTION_RELOAD_UI).AsEvent()
	glwInjectEvent(gr, e)
}

// setCustomBg — C: set_custom_bg (glw_settings.c:311)
func setCustomBg(opaque any, value any) {
	glw := glwDeps.pm.CreateEx(glwDeps.pm.GetGlobal(), "glw", nil, false, false)
	str, _ := value.(string)
	if str == "" {
		glwDeps.pm.SetVEx(nil, glw, "background", nil) // C: PROP_SET_STRING, NULL → void-ish
		return
	}
	glwDeps.pm.SetVEx(nil, glw, "background", str)
}

// GlwSettingsAdjSize — C: glw_settings_adj_size (glw_settings.c:326)
func GlwSettingsAdjSize(delta int) {
	sm := glwDeps.settingsSM
	if sm == nil {
		return
	}
	if delta == 0 {
		sm.SettingSet(glwDeps.settings.gsSettingSize, settingscore.SettingInt, 0)
	} else {
		sm.AddInt(glwDeps.settings.gsSettingSize, delta)
	}
}

// GlwSettingsStart — C: glw_settings_init (glw_settings.c:339)
func GlwSettingsStart() {
	sm := glwDeps.settingsSM
	if sm == nil {
		return
	}

	glwDeps.settings.gsSettings = glwDeps.pm.CreateRootEx("", false)
	sm.LookAndFeel().AddSource(
		glwDeps.pm.CreateEx(glwDeps.settings.gsSettings, "nodes", nil, false, false), nil)

	s := glwDeps.settings.gsSettings

	glwDeps.settings.gsSettingSize = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Font and icon size"),
		settingscore.SettingTagRange, -10, 30,
		settingscore.SettingTagUnitCStr, "px",
		settingscore.SettingTagWriteInt, &glwDeps.settings.gsSize,
		settingscore.SettingTagStore, "glw", "size")

	glwDeps.settings.gsSettingUnderscanH = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Interface horizontal shrink"),
		settingscore.SettingTagRange, -100, 100,
		settingscore.SettingTagUnitCStr, "px",
		settingscore.SettingTagWriteInt, &glwDeps.settings.gsUnderscanH,
		settingscore.SettingTagStore, "glw", "underscan_h")

	glwDeps.settings.gsSettingUnderscanV = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Interface vertical shrink"),
		settingscore.SettingTagRange, -100, 100,
		settingscore.SettingTagUnitCStr, "px",
		settingscore.SettingTagWriteInt, &glwDeps.settings.gsUnderscanV,
		settingscore.SettingTagStore, "glw", "underscan_v")

	glwDeps.settings.gsSettingWrap = sm.SettingCreate(settingscore.SettingBool, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Wrap when reaching beginning/end of lists"),
		settingscore.SettingTagValue, 1,
		settingscore.SettingTagWriteInt, &glwDeps.settings.gsWrap, // C: SETTING_WRITE_BOOL
		settingscore.SettingTagStore, "glw", "wrap")

	// __linux__ only (glw_settings.c:384-391)
	glwDeps.settings.gsSettingWheelMapping = sm.SettingCreate(settingscore.SettingBool, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Emulate Up/Down buttons with mouse wheel"),
		settingscore.SettingTagStore, "glw", "map_mouse_wheel_to_keys",
		settingscore.SettingTagWriteInt, &glwDeps.settings.gsMapMouseWheelToKeys)

	// Extension (no C counterpart — upstream had only --skin): runtime
	// skin multiopt. Options = glwScanSkins() ("default" = flat);
	// selection is applied live via ACTION_RELOAD_UI and persisted.
	// The options are passed via SETTING_OPTION_LIST so they exist
	// when the multiopt's canonical store restore runs inside
	// setting_create — restoring a late-AddOption'd value would not
	// work (s_pending_value only remembers initial_str, not curstr).
	var opts []string
	matched := false
	flagSkin := glwDeps.gconf.Skin
	flagAbs := flagSkin
	if !strings.Contains(flagSkin, "://") {
		if abs, err := filepath.Abs(flagSkin); err == nil {
			flagAbs = abs
		}
	}
	for _, sk := range glwScanSkins() {
		if flagSkin != "" && (flagSkin == sk[0] || flagAbs == sk[0]) {
			matched = true
		}
		opts = append(opts, sk[0], sk[1])
	}

	glwDeps.settings.gsSettingSkin = sm.SettingCreate(settingscore.SettingMultiOpt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Theme"),
		settingscore.SettingTagStore, "glw", "skin",
		settingscore.SettingTagCallback, glwSkinSwitch, nil,
		settingscore.SettingTagOption, "default", sm.P("Default"),
		settingscore.SettingTagOptionList, opts)

	if flagSkin != "" {
		// --skin given: make it the selected option (adds it when it
		// is not a scanned dir) — the flag wins over the stored value
		// and is persisted like any other selection.
		if !matched {
			glwDeps.settings.gsSettingSkin.AddOption(flagAbs, filepath.Base(flagAbs), false)
			flagSkin = flagAbs
		}
		sm.SettingSet(glwDeps.settings.gsSettingSkin, settingscore.SettingMultiOpt, flagSkin)
	}

	sm.CreateSeparatorProp(s, sm.P("Background")) // C: settings_create_separator(s, _p("Background"))

	glwDeps.settings.gsSettingCustomBg = sm.SettingCreate(settingscore.SettingString, s,
		settingscore.SettingsInitialUpdate|settingscore.SettingsFile,
		settingscore.SettingTagTitle, sm.P("Custom background image"),
		settingscore.SettingTagStore, "glw", "custom_bg",
		settingscore.SettingTagCallback, setCustomBg)

	sm.CreateSeparatorProp(s, sm.P("Screensaver"))

	glwDeps.settings.gsSettingScreensaverTimer = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Screensaver delay"),
		settingscore.SettingTagValue, 10,
		settingscore.SettingTagRange, 0, 60,
		settingscore.SettingTagZeroText, sm.P("Off"),
		settingscore.SettingTagUnitCStr, "min",
		settingscore.SettingTagWriteInt, &glwDeps.settings.gsScreensaverDelay,
		settingscore.SettingTagStore, "glw", "screensaver")

	glwDeps.settings.gsSettingBingImage = sm.SettingCreate(settingscore.SettingBool, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Use Bing images of the day"),
		settingscore.SettingTagValue, 1,
		settingscore.SettingTagWriteInt, &glwDeps.settings.gsBingImage,
		settingscore.SettingTagStore, "glw", "bingimageoftheday")

	glwDeps.settings.gsSettingUserImages = sm.SettingCreate(settingscore.SettingString, s,
		settingscore.SettingsInitialUpdate|settingscore.SettingsDir,
		settingscore.SettingTagTitle, sm.P("Folder for screensaver images"),
		settingscore.SettingTagCallback, setScreensaverImageFolder,
		settingscore.SettingTagStore, "glw", "userscreensaverimages",
		settingscore.SettingTagMutex, &glwDeps.settings.gsScreensaverMu)

	id := glwDeps.pm.CreateMultiPath(glwDeps.pm.GetGlobal(), "glw", "screensaver", "imageDuration")

	glwDeps.settings.gsSettingPerImageTimeout = sm.SettingCreate(settingscore.SettingInt, s,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Seconds per image"),
		settingscore.SettingTagWriteProp, id,
		settingscore.SettingTagValue, 15,
		settingscore.SettingTagRange, 5, 60,
		settingscore.SettingTagStore, "glw", "screensaverimageduration")
	glwDeps.pm.RefDec(id)

	glwp := glwDeps.pm.CreateEx(glwDeps.pm.GetGlobal(), "glw", nil, false, false)
	osk := glwDeps.pm.CreateEx(glwp, "osk", nil, false, false)
	glwDeps.kvstore.PropBindCreate(osk, "showtime:glw:osk")

	setupScreensaverItems()
}

// GlwSettingsFini — C: glw_settings_fini (glw_settings.c:455)
func GlwSettingsFini() {
	sm := glwDeps.settingsSM
	if sm == nil {
		return
	}
	sm.Destroy(glwDeps.settings.gsSettingUserImages)
	sm.Destroy(glwDeps.settings.gsSettingScreensaverTimer)
	sm.Destroy(glwDeps.settings.gsSettingPerImageTimeout)
	sm.Destroy(glwDeps.settings.gsSettingBingImage)
	sm.Destroy(glwDeps.settings.gsSettingUnderscanV)
	sm.Destroy(glwDeps.settings.gsSettingUnderscanH)
	sm.Destroy(glwDeps.settings.gsSettingSize)
	sm.Destroy(glwDeps.settings.gsSettingWrap)
	sm.Destroy(glwDeps.settings.gsSettingWheelMapping)
	sm.Destroy(glwDeps.settings.gsSettingSkin)
	sm.Destroy(glwDeps.settings.gsSettingCustomBg)
	glwDeps.pm.Destroy(glwDeps.settings.gsSettings)
}
