package plugins

// Plugin Property System Integration - Property system integration for plugins
// This file contains functions for integrating with the property system

import (
	"fmt"
	"strings"

	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// PluginProps represents plugin properties
type PluginProps struct {
	Type             string
	Title            string
	Category         string
	Description      string
	Synopsis         string
	Author           string
	Version          string
	Icon             string
	CanInstall       bool
	CanUninstall     bool
	CanUpgrade       bool
	CantUpgrade      bool
	Installed        bool
	Loaded           bool
	InRepo           bool
	StatusText       string
	InstalledVersion string
	AvailableVersion string
	MinVersion       string
	Package          string
}

// GetPluginProps returns properties for a plugin
func (pm *PluginManager) GetPluginProps(pl *Plugin) *PluginProps {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	props := &PluginProps{
		Type:             "plugin",
		Title:            pl.Title,
		CanInstall:       pl.Status.CanInstall,
		CanUninstall:     pl.Status.CanUninstall,
		CanUpgrade:       pl.Status.CanUpgrade,
		CantUpgrade:      pl.Status.CantUpgrade,
		Installed:        pl.Status.Installed,
		Loaded:           pl.Status.Loaded,
		InRepo:           pl.Status.InRepo,
		StatusText:       pl.Status.StatusText,
		InstalledVersion: pl.Status.InstalledVersion,
		AvailableVersion: pl.Status.AvailableVersion,
		MinVersion:       pl.Status.MinVersion,
		Package:          pl.Package,
	}

	return props
}

// GetAllPluginProps returns properties for all plugins
func (pm *PluginManager) GetAllPluginProps() map[string]*PluginProps {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	props := make(map[string]*PluginProps)
	for _, pl := range pm.plugins {
		props[pl.FQID] = pm.GetPluginProps(pl)
	}
	return props
}

// SetupPluginProperties sets up the property system for plugins
func (pm *PluginManager) SetupPluginProperties() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pm.propManager == nil {
		return
	}

	global := pm.propManager.GetGlobal()

	// C: plugin_root_list = prop_create(prop_create(global, "plugins"), "nodes")
	// (plugins_setup_root_props, plugins.c:1234-1239)
	pluginsProp := pm.propManager.CreateEx(global, "plugins", nil, false, false)
	pm.pluginRootList = pm.propManager.CreateEx(pluginsProp, "nodes", nil, false, false)

	pluginStart := pm.propManager.CreateEx(global, "plugin", nil, false, false)
	pm.propManager.CreateEx(pluginStart, "start", nil, false, false)
	pm.propManager.CreateEx(pluginStart, "repo", nil, false, false)

	pm.propManager.CreateEx(pluginStart, "repos", nil, false, false)
	pm.pluginReposSettings = pluginStart
}

// setupRepoSettings wires the "Plugin repositories" group under
// General settings.
// C: plugins.c:1245-1257 (tail of plugins_setup_root_props):
//
//	dir = setting_get_dir("general:plugins")
//	pc  = prop_concat_create(prop_create(dir, "nodes"))
//	plugin_repos_settings = prop_create_root(NULL)
//	prop_concat_add_source(pc, plugin_repos_settings, NULL)
//	add = prop_create_root(NULL)
//	prop_concat_add_source(pc, add, NULL)
//	settings_create_action(add, _p("Subscribe to plugin repository feed"),
//	    "add", plugins_add_repo_popup, NULL, SETTINGS_RAW_NODES, NULL)
//
// Must be called after SetSettingsManager/SetPropManager; invoked from
// plugins.Start outside pm.mu (C runs it without plugin_mutex).
func (pm *PluginManager) setupRepoSettings() {
	pm.mu.RLock()
	pmg := pm.propManager
	sm := pm.settingsMgr
	pm.mu.RUnlock()
	if pmg == nil || sm == nil {
		return
	}

	dir := sm.SettingGetDir("general:plugins")
	if dir == nil {
		return
	}
	nodes := pmg.CreateEx(dir, "nodes", nil, false, false)
	pc := propcore.PropConcatCreate(pmg, nodes)

	// C: plugin_repos_settings — dynamic list of subscribed repos
	repos := pmg.CreateRootEx("", false)
	pc.AddSource(repos, nil)

	// C: add — holds the "Subscribe to plugin repository feed" action
	add := pmg.CreateRootEx("", false)
	pc.AddSource(add, nil)
	sm.CreateActionProp(add, "Subscribe to plugin repository feed", "add",
		pm.pluginsAddRepoPopup, nil, settingscore.SettingsRawNodes)

	pm.mu.Lock()
	pm.pluginReposSettings = repos
	pm.mu.Unlock()
}

// GetPropertyRootList returns the plugin root list property
func (pm *PluginManager) GetPropertyRootList() any {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.pluginRootList
}

// GetPropertyReposSettings returns the plugin repos settings property
func (pm *PluginManager) GetPropertyReposSettings() any {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.pluginReposSettings
}

// UpdateServiceVisibility updates the visibility of the plugin service
func (pm *PluginManager) UpdateServiceVisibility() {
	pm.UpdatePluginService()
}

// SubscribeToPluginEvents subscribes to events for a plugin.
// C: plugins.c:421-425 — prop_subscribe(TRACK_DESTROY | SINGLETON, plugin_event, pl)
//
// C behavior:
//   - Subscribe with TRACK_DESTROY | SINGLETON on the plugin's prop
//   - plugin_event receives DESTROYED (cleanup) and EXT_EVENT (install/upgrade/uninstall)
//   - SINGLETON prevents duplicate (plugin_event, pl) subscriptions
//
// Go: We create a real prop subscription with TRACK_DESTROY | SINGLETON.
// The callback handles EventDestroyed (unsubscribe) and EventExtEvent
// (install/upgrade/uninstall actions).
func (pm *PluginManager) SubscribeToPluginEvents(pl *Plugin, prop *propcore.Prop, propMgr *propcore.PropManager) *propcore.Subscription {
	if prop == nil || propMgr == nil {
		return nil
	}

	var sub *propcore.Subscription
	sub = prop.Subscribe(
		func(opaque any, event propcore.EventType, args ...any) {
			pm.pluginEvent(pl, sub, event, args...)
		},
		pl,
		propcore.SubNoInitialUpdate,
		propcore.SubFlagTrackDestroy,
		propcore.SubFlagSingleton,
	)
	return sub
}

// pluginEvent is the callback for plugin prop subscriptions.
// C: plugins.c:371-404 — plugin_event
//
// C behavior:
//
//	PROP_DESTROYED: prop_unsubscribe(s)
//	PROP_EXT_EVENT: if EVENT_DYNAMIC_ACTION:
//	  "install:<path>" → plugin_install(pl, path)
//	  "upgrade"        → plugin_install(pl, NULL)
//	  "uninstall"      → plugin_remove(pl)
func (pm *PluginManager) pluginEvent(pl *Plugin, sub *propcore.Subscription, event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventDestroyed:
		// C: prop_unsubscribe(s)
		if sub != nil {
			sub.Unsubscribe()
		}
	case propcore.EventExtEvent:
		// C: event_is_type(e, EVENT_DYNAMIC_ACTION) →
		//   mystrbegins(payload, "install:") → plugin_install(pl, install ?: NULL)
		//   "upgrade"   → plugin_install(pl, NULL)
		//   "uninstall" → plugin_remove(pl)
		if len(args) == 0 {
			return
		}
		// C: event_is_type(e, EVENT_DYNAMIC_ACTION) →
		//   ((event_payload_t *)e)->payload. The ext event arrives
		// flattened as *Event; BaseEvent + the base Payload field is
		// the port's canonical cast-back (see Event.Payload).
		ep := eventpkg.BaseEvent(args[0])
		if ep == nil || ep.Type != eventpkg.EVENT_DYNAMIC_ACTION {
			return
		}
		if install, found := strings.CutPrefix(ep.Payload, "install:"); found {
			// C: *install ? install : NULL — empty payload → NULL →
			// plugin_install uses pl->pl_package (InstallPlugin: "" → pl.Package)
			_ = pm.InstallPlugin(pl, install)
		} else if ep.Payload == "upgrade" {
			_ = pm.InstallPlugin(pl, "")
		} else if ep.Payload == "uninstall" {
			_ = pm.RemovePlugin(pl)
		}
	}
}

// catnames — C: static struct strtab catnames[] (plugins.c:61-71).
// str2val/val2str normalize a category string to its canonical name;
// unknown categories map to "other" (val2str of an unknown val → NULL).
var catnames = map[string]struct{}{
	"tv": {}, "video": {}, "music": {}, "cloud": {},
	"other": {}, "glwview": {}, "glwosk": {},
	"audioengine": {}, "subtitles": {},
}

// pluginPropSetup — C: plugin_prop_setup (plugins.c:549-562).
// Creates plugin_root_list.<fqid>, sets type=plugin, fills it, and (for
// repo-listed plugins, basepath=="") records it as pl->pl_repo_model.
func (pm *PluginManager) pluginPropSetup(ctrl *PluginControl, pl *Plugin, basepath string) {
	rootList, ok := pm.pluginRootList.(*propcore.Prop)
	if pm.propManager == nil || !ok || rootList == nil {
		return
	}
	p := pm.propManager.CreateEx(rootList, pl.FQID, nil, false, false)

	// C: mystrset(&pl->pl_title, htsmsg_get_str(pm, "title") ?: pl->pl_fqid)
	pl.Title = ctrl.Title
	if pl.Title == "" {
		pl.Title = pl.FQID
	}
	pm.propManager.SetStringEx(pm.propManager.CreateEx(p, "type", nil, false, false),
		nil, "plugin", propcore.StringUTF8)

	pm.pluginFillProp(ctrl, p, basepath, pl)

	// C: if(basepath == NULL) { pl->pl_repo_model = prop_ref_inc(p); }
	if basepath == "" {
		pl.RepoModel = p
	}
}

// pluginFillProp — C: plugin_fill_prop (plugins.c:410-452).
func (pm *PluginManager) pluginFillProp(ctrl *PluginControl, p *propcore.Prop,
	basepath string, pl *Plugin) {
	propMgr := pm.propManager
	if p == nil || propMgr == nil {
		return
	}

	title := ctrl.Title
	if title == "" {
		title = pl.FQID
	}
	// C: cat = val2str(str2val(cat, catnames), catnames)
	cat := ctrl.Category
	if _, ok := catnames[cat]; !ok {
		cat = "other"
	}

	// C: prop_subscribe(TRACK_DESTROY | SINGLETON, plugin_event, pl, ROOT, p)
	pm.SubscribeToPluginEvents(pl, p, propMgr)

	// C: prop_link(pl->pl_status, prop_create_r(p, "status"))
	if pl.statusProp != nil {
		propMgr.Link(pl.statusProp,
			propMgr.CreateEx(p, "status", nil, false, false), nil, false, false)
	}

	metadata := propMgr.CreateEx(p, "metadata", nil, false, false)
	propMgr.SetStringEx(propMgr.CreateEx(metadata, "title", nil, false, false),
		nil, title, propcore.StringUTF8)
	propMgr.SetStringEx(propMgr.CreateEx(metadata, "category", nil, false, false),
		nil, cat, propcore.StringUTF8)
	propMgr.SetStringEx(propMgr.CreateEx(metadata, "description", nil, false, false),
		nil, ctrl.Description, propcore.StringRich)
	propMgr.SetStringEx(propMgr.CreateEx(metadata, "synopsis", nil, false, false),
		nil, ctrl.Synopsis, propcore.StringUTF8)
	propMgr.SetStringEx(propMgr.CreateEx(metadata, "author", nil, false, false),
		nil, ctrl.Author, propcore.StringUTF8)
	propMgr.SetStringEx(propMgr.CreateEx(metadata, "version", nil, false, false),
		nil, ctrl.Version, propcore.StringUTF8)

	icon := ctrl.Icon
	if icon != "" {
		iconURL := icon
		if !strings.HasPrefix(icon, "http://") && !strings.HasPrefix(icon, "https://") {
			if strings.HasPrefix(basepath, "http://") || strings.HasPrefix(basepath, "https://") {
				iconURL = misc.UrlResolveRelativeFromBase(basepath, icon)
			} else {
				iconURL = fmt.Sprintf("%s/%s", basepath, icon)
			}
		}
		propMgr.SetStringEx(propMgr.CreateEx(metadata, "icon", nil, false, false),
			nil, iconURL, propcore.StringUTF8)
	}
}
