package plugins

// Plugin Views System - Plugin view management
// This file contains functions for managing plugin views

import (
	"fmt"
	"slices"
	"strings"

	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// pluginsViewSettingsSetup — C: plugins_view_settings_init (plugins.c:1904-1929).
// Adds the "Preferred views from plugins" concat source to
// gconf.settings_look_and_feel and one multiopt setting per view class.
func (pm *PluginManager) pluginsViewSettingsSetup() {
	p := pm.propManager.CreateRoot("")

	// C: prop_concat_add_source(gconf.settings_look_and_feel,
	//   prop_create(p, "nodes"), makesep(_p("Preferred views from plugins")))
	if lnf := pm.settingsMgr.LookAndFeel(); lnf != nil {
		lnf.AddSource(pm.propManager.CreateEx(p, "nodes", nil, false, false),
			pm.settingsMgr.MakeSep(pm.settingsMgr.P("Preferred views from plugins")))
	}

	pm.addViewType(p, "standard", "background", pm.settingsMgr.P("Background"))
	pm.addViewType(p, "standard", "loading", pm.settingsMgr.P("Loading screen"))
	pm.addViewType(p, "standard", "screensaver", pm.settingsMgr.P("Screen saver"))
	pm.addViewType(p, "standard", "home", pm.settingsMgr.P("Home page"))
	pm.addViewType(p, "standard", "osk", pm.settingsMgr.P("On Screen Keyboards"))

	// C: settings_create_separator(p, _p("Browsing"))
	pm.settingsMgr.CreateSeparatorProp(p, pm.settingsMgr.P("Browsing"))

	pm.addViewType(p, "standard", "tracks", pm.settingsMgr.P("Audio tracks"))
	pm.addViewType(p, "standard", "album", pm.settingsMgr.P("Album"))
	pm.addViewType(p, "standard", "albums", pm.settingsMgr.P("List of albums"))
	pm.addViewType(p, "standard", "artist", pm.settingsMgr.P("Artist"))
	pm.addViewType(p, "standard", "tvchannels", pm.settingsMgr.P("TV channels"))
	pm.addViewType(p, "standard", "images", pm.settingsMgr.P("Images"))
	pm.addViewType(p, "standard", "movies", pm.settingsMgr.P("Movies"))
}

// pvsCb — C: pvs_cb (plugins.c:1855-1869). Multiopt callback: selects the
// glw.views type prop whose entry key equals the chosen option id.
func (pm *PluginManager) pvsCb(pv *pluginView, str string) {
	for _, pve := range pv.entries {
		if pve.Key == str {
			if pve.TypeProp != nil {
				pm.propManager.Select(pve.TypeProp) // C: prop_select
			}
			return
		}
	}
}

// addViewType — C: add_view_type (plugins.c:1873-1898).
func (pm *PluginManager) addViewType(p *propcore.Prop, typ, class string, title *propcore.Prop) {
	pv := &pluginView{typ: typ, class: class}
	// C: LIST_INSERT_HEAD(&plugin_views, pv, pv_link)
	pm.pluginViews = slices.Insert(pm.pluginViews, 0, pv)

	id := typ + "-" + class
	// C: setting_create(SETTING_MULTIOPT, p, SETTINGS_INITIAL_UPDATE,
	//   SETTING_TITLE(title), SETTING_STORE("selectedviews", id),
	//   SETTING_CALLBACK(pvs_cb, pv), SETTING_OPTION("default", _p("Default")),
	//   SETTING_MUTEX(&plugin_mutex), NULL)
	pv.s = pm.settingsMgr.SettingCreate(settingscore.SettingMultiOpt, p,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, title,
		settingscore.SettingTagStore, "selectedviews", id,
		settingscore.SettingTagCallback, func(opaque any, value any) {
			if str, ok := value.(string); ok {
				pm.pvsCb(pv, str)
			}
		}, pv,
		settingscore.SettingTagOption, "default", pm.settingsMgr.P("Default"),
		settingscore.SettingTagMutex, &pm.pluginMutex,
	)

	pve := &PluginViewEntry{Key: "default"}
	// C: r = prop_create(prop_create(prop_get_global(), "glw"), "views");
	//    r = prop_create(prop_create(r, type), class);
	global := pm.propManager.GetGlobal()
	r := pm.propManager.CreateEx(global, "glw", nil, false, false)
	r = pm.propManager.CreateEx(r, "views", nil, false, false)
	r = pm.propManager.CreateEx(r, typ, nil, false, false)
	r = pm.propManager.CreateEx(r, class, nil, false, false)
	// C: pve->pve_type_prop = prop_create_r(r, NULL)
	pve.TypeProp = pm.propManager.CreateEx(r, "", nil, false, false)
	// C: LIST_INSERT_HEAD(&pv->pv_entries, pve, pve_type_link)
	pm.pluginMutex.Lock()
	pv.entries = slices.Insert(pv.entries, 0, pve)
	pm.pluginMutex.Unlock()
}

// pluginsViewAdd — C: plugins_view_add (plugins.c:1935-1972).
// Caller holds plugin_mutex (pm.mu).
func (pm *PluginManager) pluginsViewAdd(pl *Plugin, typ, class, title, path string, selectNow bool, filename string) {
	view := &PluginView{
		Plugin:    pl,
		UIType:    typ,
		Class:     class,
		Title:     title,
		File:      filename,
		SelectNow: selectNow,
		Fullpath:  path,
	}
	pve := &PluginViewEntry{
		View:     view,
		Key:      path,
		Filename: filename,
	}

	global := pm.propManager.GetGlobal()
	r := pm.propManager.CreateEx(global, "glw", nil, false, false)
	r = pm.propManager.CreateEx(r, "views", nil, false, false)
	r = pm.propManager.CreateEx(r, typ, nil, false, false)
	r = pm.propManager.CreateEx(r, class, nil, false, false)

	var pv *pluginView
	for _, v := range pm.pluginViews {
		if v.class == class && v.typ == typ {
			pv = v
			break
		}
	}

	// C: pve->pve_type_prop = prop_create_r(r, path);
	//    prop_set_uri(pve->pve_type_prop, title, path)
	pve.TypeProp = pm.propManager.CreateEx(r, path, nil, false, false)
	pm.propManager.SetURIEx(pve.TypeProp, nil, title, path)

	// C: LIST_INSERT_HEAD(&pl->pl_views, pve, pve_plugin_link)
	pl.Views = slices.Insert(pl.Views, 0, pve)
	pm.views = slices.Insert(pm.views, 0, view)

	if pv != nil {
		// C: pve->pve_setting_prop = setting_add_option(pv->pv_s, path, title, select_now)
		pve.SettingProp = pv.s.AddOption(path, title, selectNow)
		// C: LIST_INSERT_HEAD(&pv->pv_entries, pve, pve_type_link)
		// pv.entries is read by pvsCb while pluginMutex is held (hps_lock).
		pm.pluginMutex.Lock()
		pv.entries = slices.Insert(pv.entries, 0, pve)
		pm.pluginMutex.Unlock()
	}
}

// GetViews returns all views
func (pm *PluginManager) GetViews() []*PluginView {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	views := make([]*PluginView, len(pm.views))
	copy(views, pm.views)
	return views
}

// GetViewsByPlugin returns all views for a specific plugin
func (pm *PluginManager) GetViewsByPlugin(pluginFQID string) []*PluginView {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*PluginView, 0)
	for _, view := range pm.views {
		if view.Plugin.FQID == pluginFQID {
			result = append(result, view)
		}
	}
	return result
}

// GetViewsByClass returns all views of a specific class
func (pm *PluginManager) GetViewsByClass(class string) []*PluginView {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*PluginView, 0)
	for _, view := range pm.views {
		if view.Class == class {
			result = append(result, view)
		}
	}
	return result
}

// GetViewsByUIType returns all views of a specific UI type
func (pm *PluginManager) GetViewsByUIType(uiType string) []*PluginView {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*PluginView, 0)
	for _, view := range pm.views {
		if view.UIType == uiType {
			result = append(result, view)
		}
	}
	return result
}

// SelectView — C: plugin_select_view (plugins.c:1976-1999). Selects the
// setting option of the view entry whose filename matches, under plugin_mutex.
func (pm *PluginManager) SelectView(pluginFQID, filename string) {
	pm.ts.Debug("PLUGINS", "Selecting view %s in plugin %s\n", filename, pluginFQID)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	var pl *Plugin
	for _, p := range pm.plugins {
		if p.FQID == pluginFQID {
			pl = p
			break
		}
	}
	if pl != nil {
		for _, pve := range pl.Views {
			if pve.Filename == filename && pve.SettingProp != nil {
				pm.propManager.Select(pve.SettingProp) // C: prop_select
			}
		}
	}
}

// GetViewByPluginAndFile returns a view by plugin and filename
func (pm *PluginManager) GetViewByPluginAndFile(pluginFQID, filename string) *PluginView {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	pl := pm.FindPlugin(pluginFQID)
	if pl == nil {
		return nil
	}

	for _, entry := range pl.Views {
		if entry.View.File == filename {
			return entry.View
		}
	}

	return nil
}

// GetSelectableViews returns all views that should be selected
func (pm *PluginManager) GetSelectableViews() []*PluginView {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*PluginView, 0)
	for _, view := range pm.views {
		if view.SelectNow {
			result = append(result, view)
		}
	}
	return result
}

// GetStandardViews returns all standard UI type views
func (pm *PluginManager) GetStandardViews() []*PluginView {
	return pm.GetViewsByUIType("standard")
}

// ViewInfo represents information about a view
type ViewInfo struct {
	PluginFQID  string
	PluginTitle string
	Class       string
	Title       string
	File        string
	UIType      string
	Fullpath    string
}

// GetViewInfo returns detailed information about all views
func (pm *PluginManager) GetViewInfo() []ViewInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]ViewInfo, 0, len(pm.views))
	for _, view := range pm.views {
		info := ViewInfo{
			PluginFQID:  view.Plugin.FQID,
			PluginTitle: view.Plugin.Title,
			Class:       view.Class,
			Title:       view.Title,
			File:        view.File,
			UIType:      view.UIType,
			Fullpath:    view.Fullpath,
		}
		result = append(result, info)
	}
	return result
}

// GetViewClasses returns all unique view classes
func (pm *PluginManager) GetViewClasses() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	classSet := make(map[string]struct{})
	for _, view := range pm.views {
		classSet[view.Class] = struct{}{}
	}

	classes := make([]string, 0, len(classSet))
	for class := range classSet {
		classes = append(classes, class)
	}

	return classes
}

// GetUITypes returns all unique UI types
func (pm *PluginManager) GetUITypes() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	typeSet := make(map[string]struct{})
	for _, view := range pm.views {
		typeSet[view.UIType] = struct{}{}
	}

	types := make([]string, 0, len(typeSet))
	for uiType := range typeSet {
		types = append(types, uiType)
	}

	return types
}

// SearchViews searches views by query
func (pm *PluginManager) SearchViews(query string) []*PluginView {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	queryLower := strings.ToLower(query)
	result := make([]*PluginView, 0)

	for _, view := range pm.views {
		titleLower := strings.ToLower(view.Title)
		classLower := strings.ToLower(view.Class)
		pluginTitleLower := strings.ToLower(view.Plugin.Title)

		if strings.Contains(titleLower, queryLower) ||
			strings.Contains(classLower, queryLower) ||
			strings.Contains(pluginTitleLower, queryLower) {
			result = append(result, view)
		}
	}

	return result
}

// GetViewCount returns the total number of views
func (pm *PluginManager) GetViewCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.views)
}

// GetViewCountByPlugin returns the number of views for a specific plugin
func (pm *PluginManager) GetViewCountByPlugin(pluginFQID string) int {
	return len(pm.GetViewsByPlugin(pluginFQID))
}

// GetViewCountByClass returns the number of views for a specific class
func (pm *PluginManager) GetViewCountByClass(class string) int {
	return len(pm.GetViewsByClass(class))
}

// OpenView opens a view for display
func (pm *PluginManager) OpenView(view *PluginView) error {
	// In real implementation, this would open the view in the UI
	pm.ts.Debug("plugins", "Opening view: %s (class: %s)\n", view.Title, view.Class)
	return nil
}

// CloseView closes a view
func (pm *PluginManager) CloseView(view *PluginView) error {
	// In real implementation, this would close the view in the UI
	pm.ts.Debug("plugins", "Closing view: %s\n", view.Title)
	return nil
}

// ReloadView reloads a view
func (pm *PluginManager) ReloadView(view *PluginView) error {
	// In real implementation, this would reload the view
	pm.ts.Debug("plugins", "Reloading view: %s\n", view.Title)
	return nil
}

// ValidateView validates a view's configuration
func (pm *PluginManager) ValidateView(view *PluginView) error {
	if view.Class == "" {
		return fmt.Errorf("View class is required")
	}
	if view.Title == "" {
		return fmt.Errorf("View title is required")
	}
	if view.File == "" {
		return fmt.Errorf("View file is required")
	}
	if view.Fullpath == "" {
		return fmt.Errorf("View fullpath is required")
	}
	return nil
}
