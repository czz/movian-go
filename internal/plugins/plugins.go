//go:build reference

package plugins

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sync"
)

// Plugin represents a loaded plugin
type Plugin struct {
	ID          string
	Name        string
	Version     string
	Author      string
	Description string
	Enabled     bool
	Path        string

	// Plugin hooks
	canHandle func(url string) bool
	openFile  func(page any, url string)

	// Reference counting
	refCount int
	mutex    sync.Mutex

	// Plugin state
	Installed           bool
	InstalledVersion    string
	RepoVersion         string
	AppMinVersion       string
	NewVersionAvailable bool
	AutoUpgrade         bool
}

// PluginManager manages the plugin system
type PluginManager struct {
	plugins     map[string]*Plugin
	devPlugins  []string
	pluginMutex sync.Mutex
	pluginRepos []*PluginRepo
}

// NewPluginManager creates a new plugin manager
func NewPluginManager(devPlugins []string) *PluginManager {
	return &PluginManager{
		plugins:    make(map[string]*Plugin),
		devPlugins: devPlugins,
	}
}

// LoadAll loads all plugins
func (pm *PluginManager) LoadAll() {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	// Scan plugin directories and load all available plugins
	// In a real implementation, this would scan filesystem for .js plugins
	// For now, we just mark as loaded
}

// UpgradeCheck checks for plugin upgrades
func (pm *PluginManager) UpgradeCheck() int {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	// Check for plugin updates from repositories
	// In a real implementation, this would check for plugin updates
	// For now, return 0 (no updates)
	return 0
}

// OpenFile opens a file using the appropriate plugin
func (pm *PluginManager) OpenFile(page any, url string) {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	// Find a plugin that can handle this URL
	for _, plugin := range pm.plugins {
		if plugin.Enabled && plugin.canHandle != nil && plugin.canHandle(url) {
			if plugin.openFile != nil {
				plugin.openFile(page, url)
			}
			return
		}
	}
}

// ReloadDevPlugin reloads the development plugin
func (pm *PluginManager) ReloadDevPlugin() {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	// Reload the development plugin from filesystem
	// In a real implementation, this would reload the development plugin
	// For now, we just mark as reloaded
}

// PropsFromFile loads plugin properties from a file
func (pm *PluginManager) PropsFromFile(prop any, zipfile string) {
	// Load plugin properties from a zip file
	// In a real implementation, this would load plugin properties
	// from a zip file
	// For now, we just mark as loaded
}

// SelectView selects a view from a plugin
func (pm *PluginManager) SelectView(pluginID string, filename string) {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	// Select a specific view from the plugin
	// In a real implementation, this would select a specific view
	// from the plugin
	// For now, we just mark as selected
}

// Uninstall uninstalls a plugin
func (pm *PluginManager) Uninstall(id string) {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	if plugin, exists := pm.plugins[id]; exists {
		// Disable and remove the plugin
		plugin.Enabled = false
		delete(pm.plugins, id)
	}
}

// ProbeForAutoinstall probes a file for auto-installation
func (pm *PluginManager) ProbeForAutoinstall(buf []byte, url string) bool {
	// Check if the file is a plugin that should be auto-installed
	// In a real implementation, this would check if the file
	// is a plugin that should be auto-installed
	// For now, return false (no auto-install)
	return false
}

// CheckPrefixForAutoinstall checks if a URL prefix matches an auto-installable plugin
func (pm *PluginManager) CheckPrefixForAutoinstall(url string) bool {
	// Check if the URL prefix matches a plugin that should be auto-installed
	// In a real implementation, this would check if the URL prefix
	// matches a plugin that should be auto-installed
	// For now, return false (no auto-install)
	return false
}

// Register registers a plugin
func (pm *PluginManager) Register(plugin *Plugin) {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	pm.plugins[plugin.ID] = plugin
}

// Unregister unregisters a plugin
func (pm *PluginManager) Unregister(id string) {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	delete(pm.plugins, id)
}

// GetPlugin returns a plugin by ID
func (pm *PluginManager) GetPlugin(id string) *Plugin {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	return pm.plugins[id]
}

// GetAllPlugins returns all registered plugins
func (pm *PluginManager) GetAllPlugins() []*Plugin {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	result := make([]*Plugin, 0, len(pm.plugins))
	for _, plugin := range pm.plugins {
		result = append(result, plugin)
	}
	return result
}

// EnablePlugin enables a plugin
func (pm *PluginManager) EnablePlugin(id string) bool {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	if plugin, exists := pm.plugins[id]; exists {
		plugin.Enabled = true
		return true
	}
	return false
}

// DisablePlugin disables a plugin
func (pm *PluginManager) DisablePlugin(id string) bool {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	if plugin, exists := pm.plugins[id]; exists {
		plugin.Enabled = false
		return true
	}
	return false
}

// SetCanHandle sets the canHandle callback for a plugin
func (p *Plugin) SetCanHandle(fn func(url string) bool) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.canHandle = fn
}

// SetOpenFile sets the openFile callback for a plugin
func (p *Plugin) SetOpenFile(fn func(page any, url string)) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.openFile = fn
}

// Retain increments the reference count
func (p *Plugin) Retain() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.refCount++
}

// Release decrements the reference count
func (p *Plugin) Release() {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.refCount--
}

// GetRefCount returns the current reference count
func (p *Plugin) GetRefCount() int {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	return p.refCount
}

// GetDefaultManager returns the default plugin manager
func GetDefaultManager() *PluginManager {
	managerMutex.Lock()
	defer managerMutex.Unlock()
	return defaultManager
}

// ==================== PLUGIN REPOSITORY FUNCTIONS ====================

// PluginRepo represents a plugin repository
type PluginRepo struct {
	URL         string
	Title       string
	AutoUpgrade bool
	Initialized bool
}

// PluginCategory represents plugin categories
type PluginCategory int

const (
	PluginCatTV PluginCategory = iota
	PluginCatVideo
	PluginCatMusic
	PluginCatCloud
	PluginCatGLWView
	PluginCatSubtitles
	PluginCatOther
	PluginCatGLWOSK
	PluginCatAudioEngine
)

// originHash generates an MD5 hash of a URL for origin identification
func originHash(url string) string {
	h := md5.New()
	h.Write([]byte(url))
	hash := h.Sum(nil)
	return hex.EncodeToString(hash[:8])
}

// pluginMake creates a new plugin entry
func (pm *PluginManager) pluginMake(id string, origin string) *Plugin {
	fqid := fmt.Sprintf("%s@%s", id, origin)

	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	// Check if plugin already exists
	for _, p := range pm.plugins {
		if p.ID == fqid {
			return p
		}
	}

	plugin := &Plugin{
		ID:      fqid,
		Enabled: false,
	}

	pm.plugins[fqid] = plugin
	return plugin
}

// pluginFind finds a plugin by its fully qualified ID
func (pm *PluginManager) pluginFind(fqid string) *Plugin {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	return pm.plugins[fqid]
}

// isPluginBlacklisted checks if a plugin is blacklisted
func isPluginBlacklisted(id string, version string) (bool, string) {
	// Check blacklist for incompatible plugins
	// Custom backgrounds are now built-in
	if id == "custombg" {
		return true, "Custom backgrounds can now be set in Settings -> Look and Feel"
	}

	// Check version blacklist
	blacklist := map[string]int{
		"oceanus":   2000000, // 2.0.0
		"xperience": 1000000, // 1.0.0
	}

	if minVer, exists := blacklist[id]; exists {
		verInt := parseVersionInt(version)
		if verInt < minVer {
			return true, fmt.Sprintf("Version %s is no longer compatible with Movian", version)
		}
	}

	return false, ""
}

// parseVersionInt parses a version string to an integer
func parseVersionInt(version string) int {
	// Parse version string like "1.2.3" to integer
	// Format: major * 10000000 + minor * 100000 + patch
	var major, minor, patch int
	fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	return major*10000000 + minor*100000 + patch
}

// updateGlobalState updates the global plugin state
func (pm *PluginManager) updateGlobalState() {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	numUpgradable := 0
	for _, p := range pm.plugins {
		if p.NewVersionAvailable {
			numUpgradable++
		}
	}

	// Update global property with upgradeable count
	// This would use prop.Set on global properties
}

// updateState updates the state of a specific plugin
func (pm *PluginManager) updateState(plugin *Plugin) {
	versionDepOK := plugin.AppMinVersion == "" ||
		parseVersionInt(plugin.AppMinVersion) <= pm.getAppVersionInt()

	plugin.NewVersionAvailable = false

	if !plugin.Installed {
		// Not installed
		_ = versionDepOK
	} else if plugin.InstalledVersion == plugin.RepoVersion {
		// Up to date
	} else {
		// Installed with version mismatch
		if plugin.RepoVersion != "" {
			plugin.NewVersionAvailable = true

			repoVer := parseVersionInt(plugin.RepoVersion)
			if plugin.InstalledVersion != "" && repoVer > parseVersionInt(plugin.InstalledVersion) {
				_ = versionDepOK
			}
		}
	}

	// Update plugin status property
	// This would use prop.Set on plugin status property
}

// pluginInstall installs a plugin
func (pm *PluginManager) pluginInstall(plugin *Plugin, packagePath string) error {
	// Install plugin from package
	// This would extract and load the plugin
	plugin.Installed = true
	plugin.InstalledVersion = plugin.RepoVersion
	pm.updateState(plugin)
	pm.updateGlobalState()
	return nil
}

// pluginRemove removes a plugin
func (pm *PluginManager) pluginRemove(plugin *Plugin) {
	plugin.Installed = false
	plugin.InstalledVersion = ""
	pm.updateState(plugin)
	pm.updateGlobalState()
}

// getAppVersionInt returns the app version as an integer
func (pm *PluginManager) getAppVersionInt() int {
	// Return app version as integer
	// In a real implementation, this would return the actual app version
	return 1000000 // Default to 1.0.0
}

// pluginAutoUpgrade performs automatic plugin upgrades
func (pm *PluginManager) pluginAutoUpgrade() {
	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	for _, p := range pm.plugins {
		if p.AutoUpgrade && p.NewVersionAvailable {
			// Perform automatic upgrade
			pm.pluginInstall(p, "")
		}
	}
}

// pluginRepoCreate creates a new plugin repository
func (pm *PluginManager) pluginRepoCreate(url string, title string, load bool) *PluginRepo {
	repo := &PluginRepo{
		URL:         url,
		Title:       title,
		AutoUpgrade: false,
		Initialized: false,
	}

	pm.pluginMutex.Lock()
	defer pm.pluginMutex.Unlock()

	pm.pluginRepos = append(pm.pluginRepos, repo)

	if load {
		// Load plugins from repository
		repo.Initialized = true
	}

	return repo
}

// ==================== EXTENDED PLUGIN STRUCTURE ====================

// ExtendedPlugin extends Plugin with additional fields from plugins.c
type ExtendedPlugin struct {
	Plugin
	FQID                string
	Origin              string
	Package             string
	Title               string
	InstalledVersion    string
	RepoVersion         string
	AppMinVersion       string
	Loaded              bool
	Installed           bool
	CanUpgrade          bool
	AutoUpgrade         bool
	NewVersionAvailable bool
	Mark                bool
	Unload              func(plugin *ExtendedPlugin)
	Views               []*PluginView
	RepoModel           any // prop.Prop
}

// PluginView represents a plugin view
type PluginView struct {
	UITitle   string
	Class     string
	Title     string
	Fullpath  string
	SelectNow bool
	Filename  string
}

// PluginViewEntry represents a plugin view entry
type PluginViewEntry struct {
	View *PluginView
}
