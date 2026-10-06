package plugins

// Plugin Loader - Plugin loading and unloading functionality
// This file contains functions for loading, unloading, and managing plugins

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/czz/movian-go/internal/ecmascript"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

// LoadError represents a plugin loading error
type LoadError struct {
	Message string
}

func (e *LoadError) Error() string {
	return e.Message
}

// LoadPlugin loads a plugin from a URL
func (pm *PluginManager) LoadPlugin(url, origin string, flags PluginLoadFlags) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.loadPluginLocked(url, origin, flags)
}

// loadPluginLocked — C: plugin_load (plugins.c:611). Caller holds
// pm.mu (C: plugin_mutex): the function unlocks/relocks internally
// only around the ecmascript load, exactly like C does around
// ecmascript_plugin_load (plugins.c:725-728).
func (pm *PluginManager) loadPluginLocked(url, origin string, flags PluginLoadFlags) error {
	// Load plugin.json
	ctrlFile := fmt.Sprintf("%s/plugin.json", url)
	ctrlData, err := loadFileWithFA(ctrlFile, pm.faManager)
	if err != nil {
		return &LoadError{Message: fmt.Sprintf("Unable to load %s -- %v", ctrlFile, err)}
	}

	// Parse control file
	var ctrl PluginControl
	if err := json.Unmarshal(ctrlData, &ctrl); err != nil {
		return &LoadError{Message: fmt.Sprintf("Unable to parse control file %s -- %v", ctrlFile, err)}
	}

	// Validate required fields
	if ctrl.Type == "" {
		return &LoadError{Message: fmt.Sprintf("Missing \"type\" element in control file %s", ctrlFile)}
	}
	if ctrl.ID == "" {
		return &LoadError{Message: fmt.Sprintf("Missing \"id\" element in control file %s", ctrlFile)}
	}

	// Get or create plugin
	pl := pm.makePluginLocked(ctrl.ID, origin)

	// Check blacklist
	if ctrl.Version != "" {
		if blacklisted, reason := pm.IsPluginBlacklisted(ctrl.ID, ctrl.Version); blacklisted {
			title := ctrl.Title
			if title == "" {
				title = ctrl.ID
			}
			return &LoadError{Message: fmt.Sprintf("Plugin %s has been uninstalled - %s", title, reason)}
		}
	}

	// Check if already loaded
	if flags&PluginLoadForce == 0 && pl.Loaded {
		return &LoadError{Message: fmt.Sprintf("Plugin \"%s\" already loaded", pl.FQID)}
	}

	// Unload if already loaded
	pm.unloadPluginInternal(pl)

	// Load based on type
	var loadErr error
	switch ctrl.Type {
	case "views":
		// Views-only plugin, no special loading needed
		loadErr = nil
	case "ecmascript":
		loadErr = pm.loadECMAScriptPlugin(pl, url, &ctrl, flags)
	case "bitcode":
		loadErr = pm.loadBitcodePlugin(pl, url, &ctrl, flags)
	default:
		if flags&PluginLoadByUser != 0 {
			return &LoadError{Message: fmt.Sprintf("Unknown type \"%s\" in control file %s", ctrl.Type, ctrlFile)}
		}
		// Keep unknown types for potential upgrades
		loadErr = nil
	}

	if loadErr != nil {
		return loadErr
	}

	// Load bundled views
	if err := pm.loadPluginViews(pl, url, &ctrl, flags); err != nil {
		return err
	}

	// Set plugin state
	if flags&PluginLoadAsInstalled != 0 {
		// C: plugin_prop_setup(ctrl, pl, url) (plugins.c:986) — creates the
		// plugin_root_list.<fqid> node before pl_installed flips, matching C
		// order (update_state at the tail publishes installed=1 on pl_status).
		pm.pluginPropSetup(&ctrl, pl, url)
		pl.Installed = true
		pl.InstVer = ctrl.Version
		pm.autoPluginSetInstalledLocked(pl.FQID, true)
	}

	if ctrl.Title != "" {
		pl.Title = ctrl.Title
	}

	pl.Loaded = true
	pm.UpdateState(pl)

	return nil
}

// unloadPluginInternal unloads a plugin (internal, assumes lock held)
func (pm *PluginManager) unloadPluginInternal(pl *Plugin) {
	if pl.Unload != nil {
		pl.Unload(pl)
		pl.Unload = nil
	}

	pm.unloadPluginViews(pl)
}

// UnloadPlugin unloads a plugin
func (pm *PluginManager) UnloadPlugin(pl *Plugin) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.unloadPluginInternal(pl)
	pl.Loaded = false
	pm.UpdateState(pl)
}

// loadECMAScriptPlugin loads an ECMAScript plugin
func (pm *PluginManager) loadECMAScriptPlugin(pl *Plugin, url string, ctrl *PluginControl, flags PluginLoadFlags) error {
	if ctrl.File == "" {
		return &LoadError{Message: fmt.Sprintf("Missing \"file\" element in control file")}
	}

	fullpath := url + "/" + ctrl.File
	version := ctrl.APIVersion
	if version == 0 {
		version = 1
	}

	pflags := 0
	if ctrl.Debug || flags&PluginLoadDebug != 0 {
		pflags |= ECMAScriptDebug
	}

	if ctrl.Entitlements != nil {
		if ctrl.Entitlements.BypassFileACLRead {
			pflags |= ECMAScriptFileBypassACLRead
		}
		if ctrl.Entitlements.BypassFileACLWrite {
			pflags |= ECMAScriptFileBypassACLWrite
		}
	}

	pm.mu.Unlock()
	err := pm.esMgr.Load(pl.FQID, fullpath, version, string(ctrlData(ctrl)), pflags)
	pm.mu.Lock()

	if err != nil {
		return err
	}

	pl.Unload = func(p *Plugin) {
		pm.esMgr.Unload(p.FQID)
	}

	return nil
}

// loadBitcodePlugin loads a bitcode (native) plugin
func (pm *PluginManager) loadBitcodePlugin(pl *Plugin, url string, ctrl *PluginControl, flags PluginLoadFlags) error {
	if ctrl.File == "" {
		return &LoadError{Message: fmt.Sprintf("Missing \"file\" element in control file")}
	}

	fullpath := url + "/" + ctrl.File
	version := ctrl.APIVersion
	if version == 0 {
		version = 1
	}

	memorySize := ctrl.MemorySize
	if memorySize == 0 {
		memorySize = 4096
	}

	stackSize := ctrl.StackSize
	if stackSize == 0 {
		stackSize = 64
	}

	pm.mu.Unlock()
	err := pm.nativeMgr.Load(pl.FQID, fullpath, version, memorySize*1024, stackSize*1024)
	pm.mu.Lock()

	if err != nil {
		return err
	}

	pl.Unload = func(p *Plugin) {
		pm.nativeMgr.Unload(p.FQID)
	}

	return nil
}

// loadPluginViews loads bundled views from a plugin
func (pm *PluginManager) loadPluginViews(pl *Plugin, url string, ctrl *PluginControl, flags PluginLoadFlags) error {
	for _, viewCtrl := range ctrl.GLWViews {
		if viewCtrl.Class == "" || viewCtrl.Title == "" || viewCtrl.File == "" {
			continue
		}

		uiType := viewCtrl.UIType
		if uiType == "" {
			uiType = "standard"
		}

		fullpath := url + "/" + viewCtrl.File
		selectNow := viewCtrl.Select
		if !selectNow && flags&PluginLoadByUser != 0 {
			selectNow = true
		}

		pm.pluginsViewAdd(pl, uiType, viewCtrl.Class, viewCtrl.Title, fullpath, selectNow, viewCtrl.File)
	}

	return nil
}

// unloadPluginViews — C: plugin_unload_views (plugins.c:2003-2018).
// Destroys each entry's setting option + type prop and detaches it from
// the plugin's view list. Caller holds plugin_mutex (pm.mu).
func (pm *PluginManager) unloadPluginViews(pl *Plugin) {
	for _, pve := range pl.Views {
		if pve.SettingProp != nil {
			// C: prop_destroy(pve->pve_setting_prop) +
			//    LIST_REMOVE(pve, pve_type_link)
			pm.propManager.Destroy(pve.SettingProp)
			pm.pluginMutex.Lock()
			for _, pv := range pm.pluginViews {
				for i, e := range pv.entries {
					if e == pve {
						pv.entries = slices.Delete(pv.entries, i, i+1)
						break
					}
				}
			}
			pm.pluginMutex.Unlock()
		}
		if pve.TypeProp != nil {
			// C: prop_destroy(pve->pve_type_prop); prop_ref_dec(...)
			pm.propManager.Destroy(pve.TypeProp)
		}
		if pve.View != nil {
			for i, v := range pm.views {
				if v == pve.View {
					pm.views = slices.Delete(pm.views, i, i+1)
					break
				}
			}
		}
	}
	pl.Views = nil
}

// LoadInstalledPlugins loads all installed plugins
func (pm *PluginManager) LoadInstalledPlugins(persistentPath string) error {
	installedPath := filepath.Join(persistentPath, pm.storagePrefix, "installed")

	entries, err := scanDirectory(installedPath)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		// Parse origin from filename
		origin := ""
		if idx := strings.Index(entry.Name, "@"); idx >= 0 {
			origin = entry.Name[idx+1:]
			if idx2 := strings.Index(origin, "."); idx2 >= 0 {
				origin = origin[:idx2]
			}
		}

		// Resolve zip path (find plugin.json inside zip)
		zipPath, err := resolveZipPath(entry.Path)
		if err != nil {
			pm.ts.Error("plugins", "Unable to resolve zip %s: %v\n", entry.Path, err)
			continue
		}

		if err := pm.LoadPlugin(zipPath, origin, PluginLoadAsInstalled); err != nil {
			// Log error but continue
			pm.ts.Error("plugins", "Unable to load %s: %v\n", entry.Path, err)
		}
	}

	return nil
}

// RemovePluginFromManager removes a plugin from the manager (internal)
func (pm *PluginManager) RemovePluginFromManager(pl *Plugin) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Unload if loaded
	if pl.Loaded {
		pm.unloadPluginInternal(pl)
	}

	// Remove from list
	for i, p := range pm.plugins {
		if p == pl {
			pm.plugins = slices.Delete(pm.plugins, i, i+1)
			break
		}
	}
}

// GetPluginByFQID gets a plugin by its fully qualified ID
func (pm *PluginManager) GetPluginByFQID(fqid string) *Plugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, pl := range pm.plugins {
		if pl.FQID == fqid {
			return pl
		}
	}
	return nil
}

// GetPluginsByCategory gets all plugins of a specific category
func (pm *PluginManager) GetPluginsByCategory(category PluginType) []*Plugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*Plugin, 0)
	for _, pl := range pm.plugins {
		// In real implementation, this would check the plugin's category
		// For now, return all plugins
		result = append(result, pl)
	}
	return result
}

// GetInstalledPlugins gets all installed plugins
func (pm *PluginManager) GetInstalledPlugins() []*Plugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*Plugin, 0)
	for _, pl := range pm.plugins {
		if pl.Installed {
			result = append(result, pl)
		}
	}
	return result
}

// GetUpgradablePlugins gets all plugins that can be upgraded
func (pm *PluginManager) GetUpgradablePlugins() []*Plugin {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]*Plugin, 0)
	for _, pl := range pm.plugins {
		if pl.CanUpgrade {
			result = append(result, pl)
		}
	}
	return result
}

// Helper functions

func loadFile(path string) ([]byte, error) {
	return loadFileWithFA(path, nil)
}

// loadFileWithFA loads a file, using the FileAccessManager for zip:// URLs
// and falling back to os.ReadFile for regular filesystem paths.
func loadFileWithFA(path string, fam *fileaccesscore.FileAccessManager) ([]byte, error) {
	if fam != nil && strings.HasPrefix(path, "zip://") {
		handle, err := fileaccesscore.Open(fam, path)
		if err != nil {
			return nil, err
		}
		defer handle.Close()
		return io.ReadAll(handle)
	}
	return os.ReadFile(path)
}

func ctrlData(ctrl *PluginControl) []byte {
	// In real implementation, this would return the raw control data
	data, _ := json.Marshal(ctrl)
	return data
}

func scanDirectory(path string) ([]DirEntry, error) {
	// In real implementation, this would use the file access layer
	// For now, use os.ReadDir as a simple implementation
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	result := make([]DirEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, DirEntry{
			Name: entry.Name(),
			Path: filepath.Join(path, entry.Name()),
		})
	}
	return result, nil
}

type DirEntry struct {
	Name string
	Path string
}

// resolveZipPath opens a zip archive in memory and returns a zip:// URL
// pointing to the directory containing plugin.json. Mirrors C Movian's
// plugin_resolve_zip_path() in plugins.c.
func resolveZipPath(zipfile string) (string, error) {
	r, err := zip.OpenReader(zipfile)
	if err != nil {
		return "", fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	zipURL := "zip://" + zipfile

	// Check if plugin.json is at the root of the zip
	for _, f := range r.File {
		if f.Name == "plugin.json" {
			return zipURL, nil
		}
	}

	// Check if plugin.json is inside a subdirectory
	for _, f := range r.File {
		if id, rest, ok := strings.Cut(f.Name, "/"); ok && rest == "plugin.json" {
			return zipURL + "/" + id, nil
		}
	}

	return "", fmt.Errorf("plugin.json not found in archive")
}

// ECMAScript plugin flags — aliases of the canonical ecmascript
// ECMASCRIPT_* constants (ecmascript.h).
const (
	ECMAScriptDebug              = ecmascript.ECMASCRIPT_DEBUG
	ECMAScriptFileBypassACLRead  = ecmascript.ECMASCRIPT_FILE_BYPASS_ACL_READ
	ECMAScriptFileBypassACLWrite = ecmascript.ECMASCRIPT_FILE_BYPASS_ACL_WRITE
)
