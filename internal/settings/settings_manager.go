package settings

import (
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/trace"
	"slices"
	"sync"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/backend/prop"
	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
)

// SettingsManager manages the settings system
type SettingsManager struct {
	settingsList []*Setting
	mutex        sync.Mutex
	pm           *propcore.PropManager
	ts           *trace.TraceSystem // C: trace() global — injected

	// Property tree references
	settingsModel *propcore.Prop
	settingsNodes *propcore.Prop

	// C: gconf.settings_apps / settings_sd / settings_network /
	//     settings_look_and_feel (main.h struct gconf)
	apps        *propcore.Prop
	sd          *propcore.Prop
	network     *propcore.Prop
	lookAndFeel *propcore.PropConcat

	// Directory cache for SettingGetDir
	dirCache map[string]*propcore.Prop

	// store — C: the global htsmsg_store. Injected via SetStore.
	store *htsmsg.Store

	// devSettingsStart — C: init_dev_settings() link-time call from
	// settings init (import-cycle seam).
	devSettingsStart func(sm *SettingsManager, settingsDev *propcore.Prop)

	// avahiUpdateHostname — C: avahi_update_hostname() under
	// STOS && ENABLE_AVAHI (settings.c:1327). Provider: pkg/sd.
	avahiUpdateHostname func()
	// kvstore — C: the global kvstore_* functions (kvstore.c).
	kvstore         *kvstore.KVStore
	propPageManager *prop.PropPageManager // C: backend_prop.c proppages static — injected

	// gconf — C: gconf_t fields settings writes (system_name). Injected.
	gconf *gconf.T
}

// SetTraceSystem injects the trace system (C: trace() global).
func (sm *SettingsManager) SetTraceSystem(ts *trace.TraceSystem) { sm.ts = ts }

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (sm *SettingsManager) SetGconf(g *gconf.T) { sm.gconf = g }

// Gconf — C: gconf_t as read by settings consumers.
func (sm *SettingsManager) Gconf() *gconf.T { return sm.gcfg() }

// gcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (sm *SettingsManager) gcfg() *gconf.T {
	if sm.gconf == nil {
		sm.gconf = gconf.New()
	}
	return sm.gconf
}

// LookAndFeel returns the concat for the "Look and feel" settings nodes.
// C: gconf.settings_look_and_feel (prop_concat_t *).
// SetStore injects the htsmsg store (C: global htsmsg_store used by
// setting writeback/readback paths).
func (sm *SettingsManager) SetStore(st *htsmsg.Store) { sm.store = st }

// SetKVStore injects the kvstore handle (C: global kvstore_* funcs).
func (sm *SettingsManager) SetKVStore(kvs *kvstore.KVStore) { sm.kvstore = kvs }

// SetPropPageManager injects the prop page manager (C: the file-static
// proppages list in backend_prop.c — settings_init calls
// backend_prop_make before backend_init, so it cannot be owned by bs).
func (sm *SettingsManager) SetPropPageManager(ppm *prop.PropPageManager) {
	sm.propPageManager = ppm
}

// Store returns the injected store (nil until wired).
func (sm *SettingsManager) Store() *htsmsg.Store { return sm.store }

func (sm *SettingsManager) LookAndFeel() *propcore.PropConcat {
	return sm.lookAndFeel
}

// Apps returns the "apps" settings prop. C: gconf.settings_apps.
func (sm *SettingsManager) Apps() *propcore.Prop { return sm.apps }

// SD returns the "sd" (discovered sources) settings prop. C: gconf.settings_sd.
func (sm *SettingsManager) SD() *propcore.Prop { return sm.sd }

// Network returns the "network" settings dir prop. C: gconf.settings_network.
func (sm *SettingsManager) Network() *propcore.Prop { return sm.network }

// P returns the nls prop tracking the given string's translation.
// C: _p(s) = nls_get_prop(s) — the prop updates live when the loaded
// language changes, so all titles created via SETTING_TITLE(_p(...))
// re-translate without re-subscribing.
func (sm *SettingsManager) P(title string) *propcore.Prop {
	return nls.GetProp(title)
}

// NewSettingsManager creates a new settings manager
// SetDevSettingsStart wires init_dev_settings (C: direct call at tail of
// settings init — lives in settings/dev to avoid an import cycle).
func (sm *SettingsManager) SetDevSettingsStart(
	fn func(*SettingsManager, *propcore.Prop)) {
	sm.devSettingsStart = fn
}

// SetAvahiUpdateHostname wires avahi_update_hostname (C: STOS &&
// ENABLE_AVAHI — settings.c:1327). Provider: pkg/sd (avahi tag).
func (sm *SettingsManager) SetAvahiUpdateHostname(fn func()) {
	sm.avahiUpdateHostname = fn
}

func NewSettingsManager(pm *propcore.PropManager) *SettingsManager {
	return &SettingsManager{
		settingsList:    make([]*Setting, 0),
		pm:              pm,
		propPageManager: prop.NewPropPageManager(),
	}
}

// Create creates a new setting
func (sm *SettingsManager) Create(settingType int, parent *Setting, flags int, title string) *Setting {
	s := &Setting{
		settingType: settingType,
		flags:       flags,
		parent:      parent,
		title:       title,
		mgr:         sm,
	}

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if flags&SettingsFirst != 0 {
		sm.settingsList = slices.Insert(sm.settingsList, 0, s)
	} else {
		sm.settingsList = append(sm.settingsList, s)
	}

	return s
}

// createLeaf creates a leaf setting
// This is a helper used internally for action settings
func (sm *SettingsManager) createLeaf(parent *propcore.Prop, title *propcore.Prop, settingType string, valueName string, flags int) *Setting {
	rootProp := sm.settingAdd(parent, title, settingType, flags)
	_ = rootProp

	s := &Setting{
		settingType: sm.settingTypeFromString(settingType),
		flags:       flags,
		title:       sm.propToString(title),
		mgr:         sm,
	}

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	if flags&SettingsFirst != 0 {
		sm.settingsList = slices.Insert(sm.settingsList, 0, s)
	} else {
		sm.settingsList = append(sm.settingsList, s)
	}

	return s
}

// settingTypeFromString converts a string type to int setting type
func (sm *SettingsManager) settingTypeFromString(s string) int {
	switch s {
	case "action":
		return SettingAction
	case "bool":
		return SettingBool
	case "int":
		return SettingInt
	case "string":
		return SettingString
	case "multiopt":
		return SettingMultiOpt
	case "separator":
		return SettingSeparator
	case "info":
		return SettingInfo
	default:
		return SettingInt
	}
}

// propToString converts a *propcore.Prop to string (helper for title extraction)
func (sm *SettingsManager) propToString(p *propcore.Prop) string {
	if p == nil {
		return ""
	}
	// Try to get string value from prop
	val := sm.pm.GetString(p, "")
	if val != "" {
		return val
	}
	return ""
}

// Start initializes the settings property tree
// Persistence is handled at construction time (NewSettingsManagerWithDB)
func (sm *SettingsManager) Start() {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Initialize settings list
	sm.settingsList = make([]*Setting, 0)

	// Create settings under global property tree
	globalProp := sm.pm.GetGlobal()

	settingsRoot := sm.pm.CreateEx(globalProp, "settings", nil, false, false)
	sm.settingsModel = sm.pm.CreateEx(settingsRoot, "model", nil, false, false)
	// C: settings_nodes = prop_create_root(NULL) — standalone root filtered
	// into model.nodes via prop_nf + prop_concat (settings.c:1346-1366)
	sm.settingsNodes = sm.pm.CreateRoot("")

	// Set type property
	typeProp := sm.pm.CreateEx(sm.settingsModel, "type", nil, false, false)
	sm.pm.SetStringEx(typeProp, nil, "settings", propcore.StringUTF8)

	// Set metadata.title on settings_model (matching C's set_title2(settings_model, _p("Global settings")))
	metadataProp := sm.pm.CreateEx(sm.settingsModel, "metadata", nil, false, false)
	titleProp := sm.pm.CreateEx(metadataProp, "title", nil, false, false)
	sm.pm.SetStringEx(titleProp, nil, "Global settings", propcore.StringUTF8)

	// C: s1 = prop_create_root(NULL);
	//     pnf = prop_nf_create(s1, settings_nodes, NULL, PROP_NF_AUTODESTROY);
	//     prop_nf_sort(pnf, "node.metadata.title", 0, 0, NULL, 1);
	s1 := sm.pm.CreateRoot("")
	pnf := propcore.PropNFCreate(s1, sm.settingsNodes, nil, propcore.PropNFAutoDestroy)
	pnf.SortEx("node.metadata.title", false, 0, nil, true)

	// C: gconf.settings_apps / gconf.settings_sd
	sm.apps = sm.pm.CreateEx(settingsRoot, "apps", nil, false, false)
	sm.sd = sm.pm.CreateEx(settingsRoot, "sd", nil, false, false)

	// C: pc = prop_concat_create(prop_create(settings_model, "nodes"));
	pc := propcore.PropConcatCreate(sm.pm, sm.pm.CreateEx(sm.settingsModel, "nodes", nil, false, false))

	// C: prop_concat_add_source(pc, s1, NULL);
	pc.AddSource(s1, nil)

	// About section — C: settings.c:1368-1377
	n := sm.pm.CreateRoot("")
	sm.AddUrl(n, sm.P("About"), "about", "", nil, "page:about", SettingsRawNodes)

	d := sm.pm.CreateRoot("")
	sm.pm.SetVEx(nil, d, "type", "separator")
	pc.AddSource(n, d)

	// Applications and plugins section — C: settings.c:1380-1387
	n = sm.pm.CreateEx(sm.apps, "nodes", nil, false, false)
	d = sm.pm.CreateRoot("")
	sm.setTitle2(d, sm.P("Applications and installed plugins"))
	sm.pm.SetVEx(nil, d, "type", "separator")
	pc.AddSource(n, d)

	// Discovered sources section — C: settings.c:1390-1396
	d = sm.pm.CreateRoot("")
	sm.setTitle2(d, sm.P("Discovered media sources"))
	sm.pm.SetVEx(nil, d, "type", "separator")
	n = sm.pm.CreateEx(sm.sd, "nodes", nil, false, false)
	pc.AddSource(n, d)

	// Network settings directory — C: gconf.settings_network
	sm.network = sm.AddDir(nil, sm.P("Network settings"), "network", "", sm.P("Network services, etc"), "settings:network")

	// Configurable system name — C: settings.c:1405-1412
	// setting_create(SETTING_STRING, gconf.settings_network,
	//   SETTINGS_INITIAL_UPDATE, SETTING_TITLE(_p("System name")),
	//   SETTING_VALUE(APPNAME), SETTING_CALLBACK(set_system_name, NULL),
	//   SETTING_STORE("netinfo", "sysname"))
	sm.SettingCreate(SettingString, sm.network, SettingsInitialUpdate,
		SettingTagTitle, sm.P("System name"),
		SettingTagValue, app.AppName,
		SettingTagCallback, func(opaque any, value any) {
			if str, ok := value.(string); ok {
				// C: set_system_name — gconf.system_name + global.app.systemname
				sm.gcfg().SystemName = str
				if global := sm.pm.GetGlobal(); global != nil {
					if ap := sm.pm.CreateEx(global, "app", nil, false, false); ap != nil {
						if sn := sm.pm.CreateEx(ap, "systemname", nil, false, false); sn != nil {
							sm.pm.SetStringEx(sn, nil, str, propcore.StringUTF8)
						}
					}
				}
				// C: #if STOS && ENABLE_AVAHI — avahi_update_hostname()
				//    (settings.c:1327-1330)
				if sm.avahiUpdateHostname != nil {
					sm.avahiUpdateHostname()
				}
			}
		}, nil,
		SettingTagStore, "netinfo", "sysname",
	)

	// Look and feel settings directory — C: gconf.settings_look_and_feel
	lnf := sm.AddDir(nil, sm.P("Look and feel"), "display", "", sm.P("Fonts and user interface styling"), "settings:lookandfeel")
	sm.lookAndFeel = propcore.PropConcatCreate(sm.pm, sm.pm.CreateEx(lnf, "nodes", nil, false, false))

	// Developer settings, only available via its URI
	// C: gconf.settings_dev = settings_add_dir(prop_create_root(NULL),
	//   _p("Developer settings"), NULL, NULL,
	//   _p("Settings useful for developers"), "settings:dev")
	// (settings.c:1417-1420). The parent is a detached root in C — the dir
	// does NOT appear in the settings menu, only via the settings:dev URI.
	settingsDev := sm.AddDir(sm.pm.CreateRootEx("", false),
		sm.P("Developer settings"), "", "", sm.P("Settings useful for developers"),
		"settings:dev")

	// C: prop_t *r = setting_add(gconf.settings_dev, NULL, "info", 0);
	//     prop_set_string(prop_create(r, "description"),
	//       "Settings for developers. If you don't know what this is,
	//        don't touch it");  (settings.c:1422-1425)
	r := sm.settingAdd(settingsDev, nil, "info", 0)
	if r != nil {
		sm.pm.SetStringEx(sm.pm.CreateEx(r, "description", nil, false, false),
			nil,
			"Settings for developers. If you don't know what this is, don't touch it",
			propcore.StringUTF8)
	}

	// C: init_dev_settings() — dev bools (binreplace/omnigrade/
	// navalwaysclose/...), netlogdest, "Debug log filtering" bools.
	// Lives in pkg/settings/dev (imported for side effect) because the
	// flag-owning packages import settings/core — a direct reference
	// would create an import cycle.
	if sm.devSettingsStart != nil {
		sm.devSettingsStart(sm, settingsDev)
	}
}

// Fini finalizes the settings system
func (sm *SettingsManager) Fini() {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	sm.settingsModel = nil
	sm.settingsNodes = nil
}

// GetModel returns the settings model
func (sm *SettingsManager) GetModel() *propcore.Prop {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	return sm.settingsModel
}

// GetNodes returns the settings nodes
func (sm *SettingsManager) GetNodes() *propcore.Prop {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	return sm.settingsNodes
}

// AddDir adds a directory setting
func (sm *SettingsManager) AddDir(parent *propcore.Prop, title *propcore.Prop, subtype string, icon string, shortdesc *propcore.Prop, url string) *propcore.Prop {
	p := sm.settingAdd(parent, title, "settings", 0)

	if shortdesc != nil {
		// C: prop_setv(p, "metadata", "shortdesc", NULL, PROP_SET_LINK, shortdesc)
		metadataProp := sm.pm.CreateEx(p, "metadata", nil, false, false)
		shortdescProp := sm.pm.CreateEx(metadataProp, "shortdesc", nil, false, false)
		sm.pm.Link(shortdesc, shortdescProp, nil, false, false)
	}

	sm.settingsAddDirSup(p, url, icon, subtype)
	return p
}

// AddDirCStr adds a directory setting with C string title
func (sm *SettingsManager) AddDirCStr(parent *propcore.Prop, title string, subtype string, icon string, shortdesc string, url string) *propcore.Prop {
	p := sm.settingAddCStr(parent, title, "settings", 0)

	if shortdesc != "" {
		// Set metadata.shortdesc
		metadataProp := sm.pm.CreateEx(p, "metadata", nil, false, false)
		shortdescProp := sm.pm.CreateEx(metadataProp, "shortdesc", nil, false, false)
		sm.pm.SetStringEx(shortdescProp, nil, shortdesc, propcore.StringUTF8)
	}

	sm.settingsAddDirSup(p, url, icon, subtype)
	return p
}

// AddUrl adds a URL setting
func (sm *SettingsManager) AddUrl(parent *propcore.Prop, title *propcore.Prop, subtype string, icon string, shortdesc *propcore.Prop, url string, flags int) {
	p := sm.settingAdd(parent, title, "settings", flags)

	if shortdesc != nil {
		// C: prop_setv(p, "metadata", "shortdesc", NULL, PROP_SET_LINK, shortdesc)
		metadataProp := sm.pm.CreateEx(p, "metadata", nil, false, false)
		shortdescProp := sm.pm.CreateEx(metadataProp, "shortdesc", nil, false, false)
		sm.pm.Link(shortdesc, shortdescProp, nil, false, false)
	}

	// C: prop_set(p, "url", PROP_SET_STRING, url)  (settings.c:218)
	sm.pm.SetStringEx(sm.pm.CreateEx(p, "url", nil, false, false),
		nil, url, propcore.StringUTF8)

	// Set subtype
	subtypeProp := sm.pm.CreateEx(p, "subtype", nil, false, false)
	sm.pm.SetStringEx(subtypeProp, nil, subtype, propcore.StringUTF8)

	if icon != "" {
		// Set metadata.icon
		metadataProp := sm.pm.CreateEx(p, "metadata", nil, false, false)
		iconProp := sm.pm.CreateEx(metadataProp, "icon", nil, false, false)
		sm.pm.SetStringEx(iconProp, nil, icon, propcore.StringUTF8)
	}
}

// CreateInfo creates an info setting
func (sm *SettingsManager) CreateInfo(parent *propcore.Prop, image string, description *propcore.Prop) {
	r := sm.settingAdd(parent, nil, "info", 0)
	// Set description
	if description != nil {
		// C: prop_set(r, "description", PROP_SET_LINK, description)
		descProp := sm.pm.CreateEx(r, "description", nil, false, false)
		if descProp != nil {
			sm.pm.Link(description, descProp, nil, false, false)
		}
	}
	if image != "" {
		// Set image
		imageProp := sm.pm.CreateEx(r, "image", nil, false, false)
		if imageProp != nil {
			sm.pm.SetStringEx(imageProp, nil, image, propcore.StringUTF8)
		}
	}
}

// CreateSeparatorProp creates a separator setting
func (sm *SettingsManager) CreateSeparatorProp(parent *propcore.Prop, caption *propcore.Prop) {
	sm.settingAdd(parent, caption, "separator", 0)
}

// CreateBoundString creates a bound string setting
func (sm *SettingsManager) CreateBoundString(parent *propcore.Prop, title *propcore.Prop, value *propcore.Prop) {
	p := sm.settingAdd(parent, title, "string", 0)
	sm.pm.SetPropEx(p, nil, value)
}

// AddInt adds an int setting
func (sm *SettingsManager) AddInt(s *Setting, delta int) {
	s.AddInt(delta)
}
