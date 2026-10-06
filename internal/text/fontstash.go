package text

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	eventpkg "github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// C: src/text/fontstash.c — user-installed font registry.

// C: typedef struct font
type fontStashFont struct {
	sys           *System // C: implicit global fontstash state
	title         string
	status        *propcore.Prop // C: f_status
	propInstalled *propcore.Prop // C: f_prop_installed
	propMainfont  *propcore.Prop // C: f_prop_mainfont
	propCondfont  *propcore.Prop // C: f_prop_condfont
	propSubfont   *propcore.Prop // C: f_prop_subfont
	installedPath string         // C: rstr_t *f_installed_path
	propModel     *propcore.Prop // C: f_prop_model
}

// fsState — fontstash subsystem state. C file-scope statics of
// fontstash.c (store, font_mutex, fonts list, font_prop_*,
// installed_root, browse_nodes). Lives on System.sys.fs.
type fsState struct {
	store         *htsmsg.HTSMsg   // C: static htsmsg_t *store
	hts           *htsmsg.Store    // store manager for save
	mu            sync.Mutex       // C: static hts_mutex_t font_mutex
	fonts         []*fontStashFont // C: static struct font_list fonts
	propMain      *propcore.Prop   // C: font_prop_main
	propCond      *propcore.Prop   // C: font_prop_cond
	propSubs      *propcore.Prop   // C: font_prop_subs
	installedRoot *propcore.Prop   // C: fontstash_installed_root
	browseNodes   *propcore.Prop   // C: fontstash_browse_nodes
	pm            *propcore.PropManager
	fam           *facore.FileAccessManager
	sm            *settingscore.SettingsManager
	nm            *notifications.NotificationManager // C: notify_add global
	gconf         *gconf.T                           // C: gconf_t — injected
	fontSubs      string                             // C: static char font_subs[256]
}

// FontstashSetGconf injects the process gconf (C: gconf_t).
func (sys *System) FontstashSetGconf(g *gconf.T) { sys.fs.gconf = g }

// fsGcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (sys *System) fsGcfg() *gconf.T {
	if sys.fs.gconf == nil {
		sys.fs.gconf = gconf.New()
	}
	return sys.fs.gconf
}

// FontSubs — C: font_subs — path of the selected subtitle font,
// read by subtitles/sub_ass.c and subtitles/video_overlay.c.
func (sys *System) FontSubs() string { return sys.fs.fontSubs }

// C: const char *fontclasses[3]
var fontclasses = [3]string{"mainfont", "condfont", "subfont"}

// C: static font_t *font_find(const char *title)
func (sys *System) fontFind(title string) *fontStashFont {
	for _, f := range sys.fs.fonts {
		if strings.EqualFold(title, f.title) {
			return f
		}
	}

	f := &fontStashFont{sys: sys}
	f.title = title
	f.status = sys.fs.pm.CreateRoot("")
	f.propInstalled = sys.fs.pm.CreateEx(f.status, "installed", nil, false, false)
	f.propMainfont = sys.fs.pm.CreateEx(f.status, "mainfont", nil, false, false)
	f.propCondfont = sys.fs.pm.CreateEx(f.status, "condfont", nil, false, false)
	f.propSubfont = sys.fs.pm.CreateEx(f.status, "subfont", nil, false, false)
	// C: LIST_INSERT_HEAD(&fonts, f, f_link)
	sys.fs.fonts = slices.Insert(sys.fs.fonts, 0, f)
	return f
}

// C: static void clear_font_prop(int which)
func (sys *System) clearFontProp(which int) {
	for _, f := range sys.fs.fonts {
		switch which {
		case 0:
			f.propMainfont.SetInt(0)
		case 1:
			f.propCondfont.SetInt(0)
		case 2:
			f.propSubfont.SetInt(0)
		}
	}
}

// C: static void font_install(font_t *f, const char *url)
func (sys *System) fontInstall(f *fontStashFont, url string) {
	if f.installedPath != "" {
		return
	}

	path := fmt.Sprintf("file://%s/installedfonts/%s",
		sys.fsGcfg().PersistentPath, f.title)

	if err := facore.FACopy(sys.fs.fam, path, url); err != nil {
		if sys.fs.nm != nil {
			sys.fs.nm.NotifyAdd(nil, notifications.NotifyError, "", 5,
				"Unable to install font: %s", err.Error())
		}
		return
	}

	f.installedPath = path
	f.propInstalled.SetInt(1)
	sys.fontMakeInstalled(f)
}

// C: static void use_font(font_t *f, const char *url)
func (sys *System) useFont(f *fontStashFont, url string) {
	tmp := fmt.Sprintf("Use font %s for", f.title)

	msgs := []string{"User interface", "Narrow text", "Subtitles"}

	r := 0
	if sys.fs.nm != nil {
		r = sys.fs.nm.MessagePopup(tmp, notifications.MessagePopupCancel, msgs)
	}

	if r == notifications.MessagePopupCancel {
		return
	}

	sys.fontInstall(f, url)

	switch r {
	case 1:
		sys.clearFontProp(0)
		sys.fs.store.DeleteField("mainfont")
		sys.fs.store.AddStr("mainfont", f.title)
		sys.fs.pm.SetStringEx(sys.fs.propMain, nil, f.installedPath, propcore.StringUTF8)
		f.propMainfont.SetInt(1)
	case 2:
		sys.clearFontProp(1)
		sys.fs.store.DeleteField("condfont")
		sys.fs.store.AddStr("condfont", f.title)
		sys.fs.pm.SetStringEx(sys.fs.propCond, nil, f.installedPath, propcore.StringUTF8)
		f.propCondfont.SetInt(1)
	case 3:
		sys.clearFontProp(2)
		sys.fs.store.DeleteField("subfont")
		sys.fs.store.AddStr("subfont", f.title)
		sys.fs.pm.SetStringEx(sys.fs.propSubs, nil, f.installedPath, propcore.StringUTF8)
		sys.fs.fontSubs = f.installedPath
		f.propSubfont.SetInt(1)
	}

	sys.fs.hts.Save(sys.fs.store, "fontstash")
}

// C: static void font_event(void *opaque, prop_event_t event, ...)
func (f *fontStashFont) fontEvent(event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		// C: s = va_arg(ap, prop_sub_t *); prop_unsubscribe(s);
		if len(args) > 1 {
			if s, ok := args[1].(*propcore.Subscription); ok {
				s.Unsubscribe()
			}
		}

	case propcore.EventExtEvent:
		// C: e = va_arg(ap, event_t *);
		//   event_is_type(e, EVENT_DYNAMIC_ACTION)
		if len(args) > 1 {
			if ep := eventpkg.BaseEvent(args[1]); ep != nil &&
				ep.Type == eventpkg.EVENT_DYNAMIC_ACTION {
				if install := mystrbegins(ep.Payload, "use:"); install != "" {
					f.sys.useFont(f, install)
				}
			}
		}
	}
}

// C: mystrbegins — returns remainder if str starts with prefix, else NULL
func mystrbegins(str, prefix string) string {
	if strings.HasPrefix(str, prefix) {
		return str[len(prefix):]
	}
	return ""
}

// C: static void font_make_installed(font_t *f)
func (sys *System) fontMakeInstalled(f *fontStashFont) {
	p := sys.fs.pm.CreateRoot("")
	f.propModel = p

	sys.fs.pm.SetVEx(nil, p, "type", "font")
	// C: prop_setv(p, "metadata", "title", NULL, PROP_SET_STRING, f->f_title)
	md := sys.fs.pm.CreateEx(p, "metadata", nil, false, false)
	sys.fs.pm.SetVEx(nil, md, "title", f.title)
	// C: prop_set(p, "url", PROP_SET_RSTRING, f->f_installed_path)
	sys.fs.pm.SetVEx(nil, p, "url", f.installedPath)
	sys.fs.pm.Link(f.status, sys.fs.pm.CreateEx(p, "status", nil, false, false), nil, false, false)
	sys.fs.pm.SetParentEx(p, sys.fs.installedRoot, nil, "")

	p.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		f.fontEvent(event, args...)
	}, f, propcore.SubFlagTrackDestroy, propcore.SubFlagSingleton)
}

// FontstashPropsFromTitle — C: fontstash_props_from_title
func (sys *System) FontstashPropsFromTitle(prop *propcore.Prop, url, title string) {
	f := sys.fontFind(title)

	sys.fs.pm.Link(f.status, sys.fs.pm.CreateEx(prop, "status", nil, false, false), nil, false, false)

	prop.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		f.fontEvent(event, args...)
	}, f, propcore.SubFlagTrackDestroy, propcore.SubFlagSingleton)
}

// C: static void reset_font(int id)
func (sys *System) resetFont(id int) {
	sys.fs.mu.Lock()
	sys.clearFontProp(id)
	sys.fs.store.DeleteField(fontclasses[id])
	switch id {
	case 0:
		sys.fs.pm.SetVoidEx(sys.fs.propMain, nil)
	case 1:
		sys.fs.pm.SetVoidEx(sys.fs.propCond, nil)
	case 2:
		sys.fs.pm.SetVoidEx(sys.fs.propSubs, nil)
		sys.fs.fontSubs = ""
	}
	sys.fs.hts.Save(sys.fs.store, "fontstash")
	sys.fs.mu.Unlock()
}

// FontstashStart — C: fontstash_init (INITME INIT_GROUP_GRAPHICS)
func (sys *System) FontstashStart(pm *propcore.PropManager, fam *facore.FileAccessManager,
	sm *settingscore.SettingsManager, bs *backendcore.BackendSystem,
	hts *htsmsg.Store, nm *notifications.NotificationManager) {
	sys.fs.pm = pm
	sys.fs.fam = fam
	sys.fs.sm = sm
	sys.fs.nm = nm
	sys.fs.hts = hts

	// C: BE_REGISTER(fontstash) (fontstash.c:455) — static registration
	// independent of fontstash_init's early return on fa_scandir failure.
	bs.Register(&backendcore.Backend{
		CanHandle: fontstashCanHandle,
		Open:      sys.fontstashOpenURL,
	})

	fonts := pm.CreateEx(pm.GetGlobal(), "fonts", nil, false, false)

	sys.fs.propMain = pm.CreateEx(fonts, "main", nil, false, false)
	sys.fs.propCond = pm.CreateEx(fonts, "condensed", nil, false, false)
	sys.fs.propSubs = pm.CreateEx(fonts, "subs", nil, false, false)
	sys.fs.installedRoot = pm.CreateEx(fonts, "installed", nil, false, false)

	sys.fs.browseNodes = pm.CreateRoot("")

	pc := propcore.PropConcatCreate(pm, sys.fs.browseNodes)

	top := pm.CreateRoot("")
	sm.CreateActionProp(top, "Reset main font to default", "",
		func(opaque any, value any) { sys.resetFont(0) }, nil, settingscore.SettingsRawNodes)
	sm.CreateActionProp(top, "Reset narrow font to default", "",
		func(opaque any, value any) { sys.resetFont(1) }, nil, settingscore.SettingsRawNodes)
	sm.CreateActionProp(top, "Reset subtitle font to default", "",
		func(opaque any, value any) { sys.resetFont(2) }, nil, settingscore.SettingsRawNodes)

	x := pm.CreateRoot("")
	// C: prop_nf_create(x, fontstash_installed_root, NULL, PROP_NF_AUTODESTROY)
	pn := propcore.PropNFCreate(x, sys.fs.installedRoot, nil, propcore.PropNFAutoDestroy)
	// C: prop_nf_sort(pn, "node.metadata.title", 0, 0, NULL, 1)
	pn.SortEx("node.metadata.title", false, 0, nil, true)
	pn.Release()

	pc.AddSource(x, nil)

	pc.AddSource(top, sm.MakeSep(sm.P("Defaults")))

	var err error
	if sys.fs.store, err = hts.Load("fontstash"); err != nil || sys.fs.store == nil {
		sys.fs.store = htsmsg.NewMap()
	}

	p := pm.CreateRoot("")

	// C: prop_concat_add_source(gconf.settings_look_and_feel, prop_create(p, "nodes"), NULL)
	if lnf := sm.LookAndFeel(); lnf != nil {
		lnf.AddSource(pm.CreateEx(p, "nodes", nil, false, false), nil)
	}

	sm.AddUrl(p, sm.P("Fonts"), "", "", nil, "fontstash:", 0)

	path := fmt.Sprintf("file://%s/installedfonts", sys.fsGcfg().PersistentPath)

	fd, err := facore.FAScanDir(fam, path)
	if err != nil || fd == nil {
		return
	}

	mainfont := sys.fs.store.GetStr("mainfont")
	condfont := sys.fs.store.GetStr("condfont")
	subfont := sys.fs.store.GetStr("subfont")

	// C: RB_FOREACH(fde, &fd->fd_entries, fde_link) — entries sorted by name
	names := make([]string, 0, len(fd.Entries))
	for name := range fd.Entries {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		fde := fd.Entries[name]
		f := sys.fontFind(fde.Filename)
		f.installedPath = fde.URL
		f.propInstalled.SetInt(1)

		if mainfont != "" && f.title == mainfont {
			sys.fs.pm.SetStringEx(sys.fs.propMain, nil, f.installedPath, propcore.StringUTF8)
			f.propMainfont.SetInt(1)
		}

		if condfont != "" && f.title == condfont {
			sys.fs.pm.SetStringEx(sys.fs.propCond, nil, f.installedPath, propcore.StringUTF8)
			f.propCondfont.SetInt(1)
		}

		if subfont != "" && f.title == subfont {
			sys.fs.pm.SetStringEx(sys.fs.propSubs, nil, f.installedPath, propcore.StringUTF8)
			sys.fs.fontSubs = f.installedPath
			f.propSubfont.SetInt(1)
		}
		sys.fontMakeInstalled(f)
	}
	fd.Free()
}

// C: static int fontstash_canhandle(const char *url)
func fontstashCanHandle(url string) int {
	if strings.HasPrefix(url, "fontstash:") {
		return 1
	}
	return 0
}

// C: static int fontstash_open_url(prop_t *page, const char *url, int sync)
func (sys *System) fontstashOpenURL(page any, url string, sync bool) error {
	pp, _ := page.(*propcore.Prop)
	if pp == nil {
		return nil
	}
	m := sys.fs.pm.CreateEx(pp, "model", nil, false, false)
	md := sys.fs.pm.CreateEx(m, "metadata", nil, false, false)
	sys.fs.pm.SetVEx(nil, m, "type", "directory")

	sys.fs.pm.Link(sys.fs.sm.P("Installed fonts"), sys.fs.pm.CreateEx(md, "title", nil, false, false), nil, false, false)
	sys.fs.pm.Link(sys.fs.browseNodes, sys.fs.pm.CreateEx(m, "nodes", nil, false, false), nil, false, false)
	return nil
}
