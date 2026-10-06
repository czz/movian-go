package plugins

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/trace"
	"slices"
	"sync"

	backendprop "github.com/czz/movian-go/internal/backend/prop"
	"github.com/czz/movian-go/internal/event"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/usage"
	"github.com/czz/movian-go/internal/version"
)

// PluginType represents the category of a plugin
type PluginType int

const (
	PluginCatTV PluginType = iota
	PluginCatVideo
	PluginCatMusic
	PluginCatCloud
	PluginCatGLWView
	PluginCatSubtitles
	PluginCatOther
	PluginCatGLWOSK
	PluginCatAudioEngine
	PluginCatNum
)

var pluginTypeNames = map[PluginType]string{
	PluginCatTV:          "tv",
	PluginCatVideo:       "video",
	PluginCatMusic:       "music",
	PluginCatCloud:       "cloud",
	PluginCatOther:       "other",
	PluginCatGLWView:     "glwview",
	PluginCatGLWOSK:      "glwosk",
	PluginCatAudioEngine: "audioengine",
	PluginCatSubtitles:   "subtitles",
}

// String returns a human readable representation for the plugin type.
func (pt PluginType) String() string {
	if name, ok := pluginTypeNames[pt]; ok {
		return name
	}
	return "other"
}

// ParsePluginType converts a string representation into a PluginType value.
func ParsePluginType(s string) PluginType {
	for pt, name := range pluginTypeNames {
		if name == s {
			return pt
		}
	}
	return PluginCatOther
}

// PluginLoadFlags represents flags that affect plugin loading.
type PluginLoadFlags int

const (
	PluginLoadForce PluginLoadFlags = 1 << iota
	PluginLoadAsInstalled
	PluginLoadByUser
	PluginLoadDebug
)

// Plugin describes a Movian plugin and its runtime state.
type Plugin struct {
	// Identification
	FQID    string
	Origin  string
	Package string
	Title   string

	// Versioning
	InstVer         string
	RepoVer         string
	AppMinVersion   string
	NewVersionAvail bool

	// State
	Loaded      bool
	Installed   bool
	CanUpgrade  bool
	AutoUpgrade bool
	Mark        bool

	// Optional unload callback invoked when the plugin is unloaded
	Unload func(*Plugin)

	// Views associated with this plugin
	Views []*PluginViewEntry

	// Status exported to the property system
	Status *PluginStatus

	// statusProp — C: pl->pl_status (plugins.c:105) — detached prop root
	// holding the status fields linked by plugin nodes and service pages.
	statusProp *propcore.Prop

	// Repository model for property backing
	RepoModel any
}

// PluginStatus mirrors the structure used by the legacy property system.
type PluginStatus struct {
	CanInstall       bool
	CanUninstall     bool
	CanUpgrade       bool
	CantUpgrade      bool
	Installed        bool
	Loaded           bool
	InRepo           bool
	NewVersionAvail  bool
	StatusText       string
	InstalledVersion string
	AvailableVersion string
	MinVersion       string
}

// PluginRepo represents a plugin repository (e.g. online source).
// C: plugin_repo_t (plugins.c:126-131)
type PluginRepo struct {
	URL         string         // C: pr_url
	Title       string         // cached repo title
	AutoUpgrade bool           // C: pr_autoupgrade
	Initialized bool           // C: pr_started
	Root        *propcore.Prop // C: pr_root — node under plugin_repos_settings
	TitleProp   *propcore.Prop // C: pr_title — metadata.title prop (live)
}

// PluginView represents an exposed GLW view belonging to a plugin.
type PluginView struct {
	Plugin    *Plugin
	UIType    string
	Class     string
	Title     string
	File      string
	SelectNow bool
	Fullpath  string
}

// PluginViewEntry wraps a PluginView.
// C: plugin_view_entry_t (plugins.c:1835-1841)
type PluginViewEntry struct {
	View        *PluginView    // Go-side plugin.json metadata
	TypeProp    *propcore.Prop // C: pve_type_prop — under $global.glw.views.<type>.<class>
	SettingProp *propcore.Prop // C: pve_setting_prop — nil for the "default" entry
	Key         string         // C: pve_key
	Filename    string         // C: pve_filename
}

// pluginView — C: plugin_view_t (plugins.c:1844-1851). Groups all view
// entries of a given uitype+class and owns the multiopt setting.
type pluginView struct {
	typ     string                // C: pv_type
	class   string                // C: pv_class
	s       *settingscore.Setting // C: pv_s
	entries []*PluginViewEntry    // C: pv_entries (LIST_INSERT_HEAD → prepend)
}

// PluginControl reflects the schema of plugin.json.
type PluginControl struct {
	ID              string               `json:"id"`
	Type            string               `json:"type"`
	Title           string               `json:"title"`
	Version         string               `json:"version"`
	File            string               `json:"file,omitempty"`
	APIVersion      int                  `json:"apiversion,omitempty"`
	Description     string               `json:"description,omitempty"`
	Synopsis        string               `json:"synopsis,omitempty"`
	Author          string               `json:"author,omitempty"`
	Category        string               `json:"category,omitempty"`
	Icon            string               `json:"icon,omitempty"`
	DownloadURL     string               `json:"downloadURL,omitempty"`
	ShowtimeVersion string               `json:"showtimeVersion,omitempty"`
	Debug           bool                 `json:"debug,omitempty"`
	MemorySize      int                  `json:"memory-size,omitempty"`
	StackSize       int                  `json:"stack-size,omitempty"`
	GLWViews        []*PluginViewControl `json:"glwviews,omitempty"`
	Entitlements    *PluginEntitlements  `json:"entitlements,omitempty"`
	Control         map[string]any       `json:"control,omitempty"`
}

// PluginViewControl is the view descriptor contained in plugin.json.
type PluginViewControl struct {
	UIType string `json:"uitype,omitempty"`
	Class  string `json:"class"`
	Title  string `json:"title"`
	File   string `json:"file"`
	Select bool   `json:"select,omitempty"`
}

// PluginEntitlements mirrors the permission flags available to plugins.
type PluginEntitlements struct {
	BypassFileACLRead  bool `json:"bypassFileACLRead"`
	BypassFileACLWrite bool `json:"bypassFileACLWrite"`
}

// PluginRepoData is the structure returned by repository manifests.
type PluginRepoData struct {
	Version int              `json:"version"`
	Title   string           `json:"title,omitempty"`
	Message string           `json:"message,omitempty"`
	Plugins []*PluginControl `json:"plugins,omitempty"`
}

// BlacklistEntry represents a plugin/version pair that should be blocked.
type BlacklistEntry struct {
	ID      string
	Version int
}

// PluginManager hosts the plugin lifecycle, repositories and GLW views.
type PluginManager struct {
	gconf *gconf.T // C: gconf_t — injected
	mu    sync.RWMutex

	ts *trace.TraceSystem // C: trace() global — injected

	usage    *usage.Reporter                    // C: usage_event global (usage.c)
	notifMgr *notifications.NotificationManager // C: notify_add global
	store    *htsmsg.Store                      // C: global htsmsg_store

	plugins []*Plugin
	repos   []*PluginRepo
	views   []*PluginView

	// C: plugin_mutex (plugins.c:62) — guards plugin_views/pv_entries and
	// is the PROP_TAG_MUTEX of the view multiopt subscriptions.
	pluginMutex sync.Mutex
	// C: plugin_views (plugins.c:75) — LIST of plugin_view_t.
	pluginViews []*pluginView

	pluginRootList      any
	pluginReposSettings any

	devPlugins    []string
	storagePrefix string

	persistentPath string

	blacklist   []*BlacklistEntry
	autoPlugins map[string]*AutoPlugin

	nativeMgr *NativePluginManager
	esMgr     *ECMAScriptPluginManager

	// Property system for native/prop binding
	propManager     *propcore.PropManager
	propPageManager *backendprop.PropPageManager // C: backend_prop.c proppages static — injected

	// Route registrar for native/route binding (set via SetRouteRegistrar)
	routeRegistrar RouteRegistrar

	// Service system for creating the "Plugins" service tile
	serviceSystem *service.ServiceSystem
	pluginService *service.Service

	// File access manager for zip:// protocol support
	faManager *fileaccesscore.FileAccessManager

	// Settings manager for the "Plugin repositories" group
	// (C: plugins.c calls setting_get_dir/settings_create_action directly)
	settingsMgr *settingscore.SettingsManager

	// Event manager — needed to send ACTION_NAV_BACK on repo delete
	// (C: plugins.c uses the global event_create_action)
	eventManager *event.EventManager
}

// SetTraceSystem injects the trace system (C: trace() global).
// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (pm *PluginManager) SetGconf(g *gconf.T) { pm.gconf = g }

// gcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (pm *PluginManager) gcfg() *gconf.T {
	if pm == nil || pm.gconf == nil {
		return gconf.New()
	}
	return pm.gconf
}

func (pm *PluginManager) SetTraceSystem(ts *trace.TraceSystem) { pm.ts = ts }

// RouteRegistrar is the interface for registering URL routes (implemented by NavigatorSystem's RouteSystem).
type RouteRegistrar interface {
	CreateRoute(pattern string, callback func(page any, url string, matches []string)) error
	TestURL(url string) bool
}

// AutoPlugin tracks automatically installable plugins defined by repositories.
type AutoPlugin struct {
	ID        string
	Installed bool
	Control   map[string]any
}

// NewPluginManager allocates and initializes a plugin manager instance.
func NewPluginManager(u *usage.Reporter, nm *notifications.NotificationManager,
	store *htsmsg.Store) *PluginManager {
	pm := &PluginManager{
		usage:           u,
		notifMgr:        nm,
		store:           store,
		plugins:         make([]*Plugin, 0),
		repos:           make([]*PluginRepo, 0),
		views:           make([]*PluginView, 0),
		storagePrefix:   "mrp",
		autoPlugins:     make(map[string]*AutoPlugin),
		nativeMgr:       NewNativePluginManager(),
		esMgr:           NewECMAScriptPluginManager(u),
		propPageManager: backendprop.NewPropPageManager(),
	}
	pm.setupBlacklist()
	return pm
}

// SetPropManager injects the property manager for native/prop binding.
// SetPropPageManager injects the prop page manager (C: backend_prop.c
// proppages static list).
func (pm *PluginManager) SetPropPageManager(ppm *backendprop.PropPageManager) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.propPageManager = ppm
}

func (pm *PluginManager) SetPropManager(pmgr *propcore.PropManager) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.propManager = pmgr
	if pm.esMgr != nil {
		pm.esMgr.SetPropManager(pmgr)
	}
}

// GetPropManager returns the property manager.
func (pm *PluginManager) GetPropManager() *propcore.PropManager {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.propManager
}

// SetRouteRegistrar injects the route registrar for native/route binding.
func (pm *PluginManager) SetRouteRegistrar(rr RouteRegistrar) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.routeRegistrar = rr
	if pm.esMgr != nil {
		pm.esMgr.SetRouteRegistrar(rr)
	}
}

// SetServiceSystem injects the service system for Service.create() binding.
func (pm *PluginManager) SetServiceSystem(ss *service.ServiceSystem) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.serviceSystem = ss
	if pm.esMgr != nil {
		pm.esMgr.SetServiceSystem(ss)
	}
}

// SetFileAccessManager injects the file access manager for zip:// protocol support.
func (pm *PluginManager) SetFileAccessManager(fam *fileaccesscore.FileAccessManager) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.faManager = fam
	if pm.esMgr != nil {
		pm.esMgr.SetFileAccessManager(fam)
	}
}

// SetSettingsManager injects the settings manager for the
// "Plugin repositories" group under General settings
// (C: plugins_setup_root_props uses setting_get_dir directly).
func (pm *PluginManager) SetSettingsManager(sm *settingscore.SettingsManager) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.settingsMgr = sm
}

// SetEventManager injects the event manager — used to send
// ACTION_NAV_BACK after a repo is deleted from its settings page
// (C: plugin_repo_delete uses the global event_create_action).
func (pm *PluginManager) SetEventManager(em *event.EventManager) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.eventManager = em
}

// UpdatePluginService creates or destroys the "Plugins" service tile based on
// whether any plugins or plugin repos are loaded. Mirrors C Movian's
// plugin_update_service() in plugins.c.
func (pm *PluginManager) UpdatePluginService() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.serviceSystem == nil {
		return
	}

	hasPlugins := len(pm.plugins) > 0 || len(pm.esMgr.GetAll()) > 0

	if hasPlugins && pm.pluginService == nil {
		pm.pluginService = pm.serviceSystem.ServiceCreate(
			"showtime:plugin", "Plugins", "plugin:start", "plugin", "", false, true, service.SvcOriginSystem)
	} else if !hasPlugins && pm.pluginService != nil {
		pm.serviceSystem.ServiceDestroy(pm.pluginService)
		pm.pluginService = nil
	}
}

func (pm *PluginManager) setupBlacklist() {
	pm.blacklist = []*BlacklistEntry{
		{ID: "oceanus", Version: encodeVersion(2, 0, 0)},
		{ID: "xperience", Version: encodeVersion(1, 0, 0)},
	}
}

// IsInstalled returns whether the plugin is installed.
func (p *Plugin) IsInstalled() bool { return p.Installed }

// FindPlugin locates a plugin by its fully qualified identifier (id@origin).
func (pm *PluginManager) FindPlugin(fqid string) *Plugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, pl := range pm.plugins {
		if pl.FQID == fqid {
			return pl
		}
	}
	return nil
}

// MakePlugin fetches an existing plugin or creates a brand new record.
func (pm *PluginManager) MakePlugin(id, origin string) *Plugin {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.makePluginLocked(id, origin)
}

func (pm *PluginManager) makePluginLocked(id, origin string) *Plugin {
	fqid := fmt.Sprintf("%s@%s", id, origin)
	for _, pl := range pm.plugins {
		if pl.FQID == fqid {
			return pl
		}
	}

	pl := &Plugin{
		FQID:   fqid,
		Origin: origin,
		Status: &PluginStatus{},
		Views:  make([]*PluginViewEntry, 0),
	}

	// C: pl->pl_status = prop_create_root(NULL) (plugin_make, plugins.c:250)
	if pm.propManager != nil {
		pl.statusProp = pm.propManager.CreateRootEx("", false)
	}

	pm.plugins = append(pm.plugins, pl)
	return pl
}

// IsPluginBlacklisted checks whether a plugin/version should be forbidden.
func (pm *PluginManager) IsPluginBlacklisted(id, version string) (bool, string) {
	if id == "custombg" {
		return true, "Custom backgrounds can now be set in Settings -> Look and Feel"
	}

	verInt := parseVersionInt(version)
	for _, entry := range pm.blacklist {
		if entry.ID == id && verInt < entry.Version {
			return true, fmt.Sprintf("Version %s is no longer compatible with Movian", version)
		}
	}
	return false, ""
}

// UpdateState recalculates the state flags exposed for a plugin.
func (pm *PluginManager) UpdateState(pl *Plugin) {
	pl.Status.CanInstall = false
	pl.Status.CanUninstall = false
	pl.Status.CanUpgrade = false
	pl.Status.CantUpgrade = false
	pl.Status.NewVersionAvail = false

	versionDepOK := pl.AppMinVersion == "" || parseVersionInt(pl.AppMinVersion) <= getCurrentAppVersion()

	if !pl.Installed {
		if !versionDepOK {
			pl.Status.StatusText = "Not installable"
			pl.Status.MinVersion = pl.AppMinVersion
		} else {
			pl.Status.StatusText = "Not installed"
			pl.Status.CanInstall = true
		}
	} else if pl.InstVer == pl.RepoVer {
		pl.Status.StatusText = "Up to date"
		pl.Status.CanUninstall = true
	} else {
		pl.Status.StatusText = "Installed"
		pl.Status.CanUninstall = true

		if pl.RepoVer != "" {
			pl.Status.NewVersionAvail = true
			repoVer := parseVersionInt(pl.RepoVer)
			if pl.InstVer != "" && repoVer > parseVersionInt(pl.InstVer) {
				if !versionDepOK {
					pl.Status.StatusText = "Not upgradable"
					pl.Status.MinVersion = pl.AppMinVersion
					pl.Status.CantUpgrade = true
				} else {
					pl.Status.StatusText = "Upgradable"
					pl.Status.CanUpgrade = true
				}
			} else {
				pl.Status.StatusText = "Installed version higher than available"
			}
		}
	}

	pl.CanUpgrade = pl.Status.CanUpgrade
	pl.Status.Installed = pl.Installed
	pl.Status.Loaded = pl.Loaded
	pl.Status.InstalledVersion = pl.InstVer
	pl.Status.AvailableVersion = pl.RepoVer

	// C: update_state (plugins.c:295-362) — mirror the state onto
	// pl->pl_status so plugin nodes/services observe it via prop links.
	if pl.statusProp != nil {
		pmg := pm.propManager
		if !versionDepOK {
			minver := pmg.CreateEx(pl.statusProp, "minver", nil, false, false)
			pmg.SetStringEx(minver, nil, pl.Status.MinVersion, propcore.StringUTF8)
		} else {
			minver := pmg.CreateEx(pl.statusProp, "minver", nil, false, false)
			pmg.SetVoidEx(minver, nil)
		}
		pmg.SetIntEx(pmg.CreateEx(pl.statusProp, "canInstall", nil, false, false), nil, misc.BoolToInt(pl.Status.CanInstall))
		pmg.SetIntEx(pmg.CreateEx(pl.statusProp, "canUninstall", nil, false, false), nil, misc.BoolToInt(pl.Status.CanUninstall))
		pmg.SetIntEx(pmg.CreateEx(pl.statusProp, "canUpgrade", nil, false, false), nil, misc.BoolToInt(pl.Status.CanUpgrade))
		pmg.SetIntEx(pmg.CreateEx(pl.statusProp, "cantUpgrade", nil, false, false), nil, misc.BoolToInt(pl.Status.CantUpgrade))
		pmg.SetIntEx(pmg.CreateEx(pl.statusProp, "installed", nil, false, false), nil, misc.BoolToInt(pl.Installed))
		pmg.SetStringEx(pmg.CreateEx(pl.statusProp, "statustxt", nil, false, false), nil, pl.Status.StatusText, propcore.StringUTF8)
		pmg.SetIntEx(pmg.CreateEx(pl.statusProp, "loaded", nil, false, false), nil, misc.BoolToInt(pl.Loaded))
		pmg.SetStringEx(pmg.CreateEx(pl.statusProp, "installedVersion", nil, false, false), nil, pl.InstVer, propcore.StringUTF8)
		pmg.SetStringEx(pmg.CreateEx(pl.statusProp, "availableVersion", nil, false, false), nil, pl.RepoVer, propcore.StringUTF8)
	}
}

// MarkPlugins marks every plugin, preparing for a repo sweep.
func (pm *PluginManager) MarkPlugins() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, pl := range pm.plugins {
		pl.Mark = true
	}
}

// SweepPlugins clears the mark for plugins that were touched during repo load.
func (pm *PluginManager) SweepPlugins() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, pl := range pm.plugins {
		pl.Status.InRepo = !pl.Mark
		// C: prop_set(pl->pl_status, "inRepo", PROP_SET_INT, !pl->pl_mark)
		// (plugin_sweep, plugins.c:921)
		if pl.statusProp != nil && pm.propManager != nil {
			inRepo := 0
			if !pl.Mark {
				inRepo = 1
			}
			pm.propManager.SetIntEx(
				pm.propManager.CreateEx(pl.statusProp, "inRepo", nil, false, false),
				nil, inRepo)
		}
		if pl.Mark {
			pl.RepoModel = nil
			pl.Mark = false
		}
	}
}

// UpdateGlobalState reports the number of plugins with updates available.
func (pm *PluginManager) UpdateGlobalState() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	count := 0
	for _, pl := range pm.plugins {
		if pl.Status.NewVersionAvail {
			count++
		}
	}
	return count
}

// GetPlugins returns a copy of the plugin slice for safe iteration.
func (pm *PluginManager) GetPlugins() []*Plugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	out := make([]*Plugin, len(pm.plugins))
	copy(out, pm.plugins)
	return out
}

// GetRepos returns a copy of the configured repositories.
func (pm *PluginManager) GetRepos() []*PluginRepo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	out := make([]*PluginRepo, len(pm.repos))
	copy(out, pm.repos)
	return out
}

// AddRepo registers a repository and optionally loads it.
// C: plugin_repo_create (plugins.c:1343-1399) — runs with plugin_mutex held:
//
//	LIST_INSERT_HEAD(&plugin_repos)
//	pr_root = prop_create_r(plugin_repos_settings, NULL)
//	metadata.title = title ?: url; type="directory"; subtype="plugins"
//	url = backend_prop_make(model); model{type:"settings", metadata.title=LINK,
//	    nodes: info + separator + bool autoupgrade + separator + delete action}
//	if(load) plugin_load_repo(pr)
//	pr_started = 1; plugin_update_service()
//
// Go note: LoadRepo runs outside pm.mu (network I/O + internal locking);
// C's plugin_mutex is pm.mu here. The settings callbacks take pm.mu
// themselves at entry — equivalent coverage to C's SETTING_MUTEX.
func (pm *PluginManager) AddRepo(url, title string, load bool) *PluginRepo {
	pr := &PluginRepo{
		URL:         url,
		Title:       title,
		AutoUpgrade: false,
		Initialized: false,
	}

	pm.mu.Lock()

	// C: LIST_INSERT_HEAD(&plugin_repos, pr, pr_link)
	pm.repos = slices.Insert(pm.repos, 0, pr)

	pmg := pm.propManager
	var m *propcore.Prop
	if pmg != nil {
		if rs, ok := pm.pluginReposSettings.(*propcore.Prop); ok && rs != nil {
			// C: pr->pr_root = prop_create_r(plugin_repos_settings, NULL)
			root := pmg.CreateEx(rs, "", nil, false, false)
			pr.Root = root

			// C: pr->pr_title = prop_ref_inc(prop_create_multi(pr->pr_root,
			//    "metadata", "title", NULL)); prop_set_string(pr_title, title ?: url)
			md := pmg.CreateEx(root, "metadata", nil, false, false)
			titleProp := pmg.CreateEx(md, "title", nil, false, false)
			pr.TitleProp = titleProp
			t := title
			if t == "" {
				t = url
			}
			pmg.SetStringEx(titleProp, nil, t, propcore.StringUTF8)

			// C: prop_set(pr->pr_root, "type", "directory");
			//     prop_set(pr->pr_root, "subtype", "plugins")
			pmg.SetStringEx(pmg.CreateEx(root, "type", nil, false, false), nil,
				"directory", propcore.StringUTF8)
			pmg.SetStringEx(pmg.CreateEx(root, "subtype", nil, false, false), nil,
				"plugins", propcore.StringUTF8)

			// C: m = prop_create(pr->pr_root, "model");
			//     prop_set(pr->pr_root, "url", PROP_ADOPT_RSTRING,
			//              backend_prop_make(m, NULL))
			m = pmg.CreateEx(root, "model", nil, false, false)
			pageURL := pm.propPageManager.BackendPropMake(pmg, m, "")
			pmg.SetStringEx(pmg.CreateEx(root, "url", nil, false, false), nil,
				pageURL, propcore.StringUTF8)

			// C: prop_set(m, "type", "settings");
			//     prop_set(prop_create(m,"metadata"), "title", PROP_SET_LINK, pr_title)
			pmg.SetStringEx(pmg.CreateEx(m, "type", nil, false, false), nil,
				"settings", propcore.StringUTF8)
			mmd := pmg.CreateEx(m, "metadata", nil, false, false)
			pmg.Link(titleProp, pmg.CreateEx(mmd, "title", nil, false, false), nil, false, false)

			// C: info node — prop_setv(info, "type", "info") +
			//     description = fmt("URL: %s", pr->pr_url)
			nodes := pmg.CreateEx(m, "nodes", nil, false, false)
			info := pmg.CreateEx(nodes, "", nil, false, false)
			pmg.SetStringEx(pmg.CreateEx(info, "type", nil, false, false), nil,
				"info", propcore.StringUTF8)
			pmg.SetStringEx(pmg.CreateEx(info, "description", nil, false, false), nil,
				fmt.Sprintf("URL: %s", url), propcore.StringUTF8)
		}
	}
	pm.mu.Unlock()

	// C: the setting_create calls below run under plugin_mutex — but their
	// callbacks are invoked with it already held (SETTING_MUTEX). Go's
	// callbacks take pm.mu at entry, so the SettingCreate calls must run
	// OUTSIDE pm.mu: the bool's SETTINGS_INITIAL_UPDATE dispatch would
	// otherwise self-deadlock (setAutoupgrade re-locks pm.mu).
	if m != nil && pm.settingsMgr != nil {
		// C: setting_create(SETTING_SEPARATOR, m, 0, NULL)
		pm.settingsMgr.SettingCreate(settingscore.SettingSeparator, m, 0, 0)

		// C: setting_create(SETTING_BOOL, m, SETTINGS_INITIAL_UPDATE,
		//     SETTING_STORE("pluginconf", "autoupgrade"),
		//     SETTING_TITLE(_p("Automatically upgrade plugins")),
		//     SETTING_VALUE(1), SETTING_KVSTORE(url, "autoupgrade"),
		//     SETTING_CALLBACK(set_autoupgrade, pr),
		//     SETTING_MUTEX(&plugin_mutex), NULL)
		pm.settingsMgr.SettingCreate(settingscore.SettingBool, m,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagStore, "pluginconf", "autoupgrade",
			settingscore.SettingTagTitle,
			pm.settingsMgr.P("Automatically upgrade plugins"),
			settingscore.SettingTagValue, 1,
			settingscore.SettingTagKVStore, url, "autoupgrade",
			settingscore.SettingTagCallback,
			func(opaque, value any) {
				if p, ok := opaque.(*PluginRepo); ok {
					pm.setAutoupgrade(p, value)
				}
			}, pr,
			0)

		// C: setting_create(SETTING_SEPARATOR, m, 0, NULL)
		pm.settingsMgr.SettingCreate(settingscore.SettingSeparator, m, 0, 0)

		// C: setting_create(SETTING_ACTION, m, 0,
		//     SETTING_TITLE(_p("Stop subscribing to repository feed")),
		//     SETTING_CALLBACK(plugin_repo_delete, pr),
		//     SETTING_MUTEX(&plugin_mutex), NULL)
		pm.settingsMgr.SettingCreate(settingscore.SettingAction, m, 0,
			settingscore.SettingTagTitle,
			pm.settingsMgr.P("Stop subscribing to repository feed"),
			settingscore.SettingTagCallback,
			func(opaque, value any) {
				if p, ok := opaque.(*PluginRepo); ok {
					pm.pluginRepoDelete(p, value)
				}
			}, pr,
			0)
	}

	// C: if(load) plugin_load_repo(pr) — called under plugin_mutex in C;
	// Go LoadRepo does network I/O unlocked then locks internally.
	if load {
		pm.LoadRepo(pr)
	}

	// C: pr->pr_started = 1 — set AFTER the settings creation like C,
	// so the bool's initial-update callback sees it unset and skips
	// plugin_load_repo (plugins.c:1310).
	pm.mu.Lock()
	pr.Initialized = true
	pm.mu.Unlock()

	// C: plugin_update_service()
	pm.UpdatePluginService()
	return pr
}

// RemoveRepo removes a repository by URL.
func (pm *PluginManager) RemoveRepo(url string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for i, pr := range pm.repos {
		if pr.URL == url {
			pm.repos = slices.Delete(pm.repos, i, i+1)
			return true
		}
	}
	return false
}

// AutoPluginClear resets the auto plugin table.
func (pm *PluginManager) AutoPluginClear() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.autoPluginClearLocked()
}

// autoPluginClearLocked is the lock-free internal version.
// C: autoplugin_clear (plugins.c) — called with autoplugin_mutex held.
func (pm *PluginManager) autoPluginClearLocked() {
	pm.autoPlugins = make(map[string]*AutoPlugin)
}

// AutoPluginCreateFromControl registers auto install metadata for a plugin.
func (pm *PluginManager) AutoPluginCreateFromControl(id string, control map[string]any, installed bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.autoPluginCreateFromControlLocked(id, control, installed)
}

// autoPluginCreateFromControlLocked is the lock-free internal version.
// C: autoplugin_create_from_control (plugins.c) — called with autoplugin_mutex held.
func (pm *PluginManager) autoPluginCreateFromControlLocked(id string, control map[string]any, installed bool) {
	pm.autoPlugins[id] = &AutoPlugin{ID: id, Installed: installed, Control: control}
}

// AutoPluginSetInstalled toggles the installed flag for an auto plugin.
func (pm *PluginManager) AutoPluginSetInstalled(id string, installed bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.autoPluginSetInstalledLocked(id, installed)
}

// autoPluginSetInstalledLocked is the lock-free internal version.
func (pm *PluginManager) autoPluginSetInstalledLocked(id string, installed bool) {
	if ap, ok := pm.autoPlugins[id]; ok {
		ap.Installed = installed
	}
}

// GetAutoPlugin fetches metadata for an auto plugin.
func (pm *PluginManager) GetAutoPlugin(id string) *AutoPlugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.autoPlugins[id]
}

func encodeVersion(major, minor, patch int) int {
	return major*10000000 + minor*100000 + patch
}

func originHash(url string) string {
	hash := md5.Sum([]byte(url))
	return hex.EncodeToString(hash[:8])
}

func parseVersionInt(version string) int {
	major, minor, patch := 0, 0, 0
	fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	return encodeVersion(major, minor, patch)
}

// getCurrentAppVersion returns the running app version.
// C: plugins.c:306 — app_get_version_int()
func getCurrentAppVersion() int {
	return int(version.AppGetVersionInt())
}
