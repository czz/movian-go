package plugins

// Plugin Installation and Removal - Plugin install/uninstall functionality
// This file contains functions for installing and removing plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
)

// InstallPlugin installs a plugin
func (pm *PluginManager) InstallPlugin(pl *Plugin, packageURL string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// C: plugins.c:1517-1519 — usage event before the package fallback;
	// "source" is File only when a package path was passed explicitly.
	source := "Repo"
	if packageURL != "" {
		source = "File"
	}
	event := "Plugin install"
	if pl.CanUpgrade {
		event = "Plugin upgrade"
	}
	pm.usage.Event(event, 1, "plugin", pl.FQID, "source", source)

	// Use plugin's package URL if not provided
	if packageURL == "" {
		packageURL = pl.Package
		if packageURL == "" {
			return fmt.Errorf("No package URL specified")
		}
	}

	// Download plugin package — C: plugin_install unlocks plugin_mutex
	// around fa_load (plugins.c:1536-1540); mirror that so the network
	// fetch doesn't hold the plugin lock.
	pm.mu.Unlock()
	tempPath, err := pm.downloadPlugin(packageURL)
	pm.mu.Lock()
	if err != nil {
		return fmt.Errorf("Failed to download plugin: %w", err)
	}
	defer os.Remove(tempPath)

	// Validate the bundle before touching persistent storage — C checks
	// the PK magic up front and later resolves the zip path on the
	// installed file.
	if _, err := resolveZipPath(tempPath); err != nil {
		return fmt.Errorf("Failed to resolve zip path: %w", err)
	}

	// C: plugin_unload(pl) (plugins.c:1567) — unload the old instance
	// before overwriting the installed archive.
	pm.unloadPluginInternal(pl)

	// Install to persistent storage first — C writes
	// installed/<fqid>.zip and only then resolves + loads from that
	// permanent path, so Plugin.path (and assets like the service icon)
	// keep pointing at a file that still exists after the temp download
	// is removed.
	installedPath, err := pm.installToStorage(pl, tempPath)
	if err != nil {
		return fmt.Errorf("Failed to install plugin: %w", err)
	}

	// C: #ifdef STOS — arch_sync_path(path) after fa_write/fa_close
	//    (plugins.c:1610-1611). mgosSyncPath is syncfs under -tags mgos.
	mgosSyncPath(installedPath)

	// C: plugin_resolve_zip_path(path) (plugins.c:1612)
	zipPath, err := resolveZipPath(installedPath)
	if err != nil {
		return fmt.Errorf("Failed to resolve zip path: %w", err)
	}

	// C: plugin_load(zippath, ..., PLUGIN_LOAD_FORCE |
	// PLUGIN_LOAD_AS_INSTALLED | PLUGIN_LOAD_BY_USER) (plugins.c:1621) —
	// AS_INSTALLED runs plugin_prop_setup + pl_installed=1 +
	// autoplugin_set_installed + update_state inside plugin_load.
	if err := pm.loadPluginLocked(zipPath, pl.Origin,
		PluginLoadForce|PluginLoadAsInstalled|PluginLoadByUser); err != nil {
		return fmt.Errorf("Failed to load plugin: %w", err)
	}

	// Save repos configuration — locked variant
	pm.saveReposLocked()
	return nil
}

// RemovePlugin removes an installed plugin
func (pm *PluginManager) RemovePlugin(pl *Plugin) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if !pl.Installed {
		return fmt.Errorf("Plugin is not installed")
	}

	// C: plugins.c:1486 — usage_event("Plugin remove", plugin=fqid)
	pm.usage.Event("Plugin remove", 1, "plugin", pl.FQID)

	// Unload plugin
	pm.unloadPluginInternal(pl)

	// Remove from storage
	if err := pm.removeFromStorage(pl); err != nil {
		return fmt.Errorf("Failed to remove plugin from storage: %w", err)
	}

	// Mark as not installed
	pl.Installed = false
	pl.InstVer = ""
	pl.Loaded = false
	pm.autoPluginSetInstalledLocked(pl.FQID, false)
	pm.UpdateState(pl)

	// Save repos configuration
	pm.saveReposLocked()
	return nil
}

// downloadPlugin downloads a plugin package
func (pm *PluginManager) downloadPlugin(packageURL string) (string, error) {
	// In real implementation, this would use the file access layer
	// to download the file with progress reporting

	// Create temp file
	tempFile, err := os.CreateTemp("", "plugin-*.zip")
	if err != nil {
		return "", err
	}
	defer tempFile.Close()

	// Download from packageURL
	resp, err := http.Get(packageURL)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// Write to temp file
	_, err = io.Copy(tempFile, resp.Body)
	if err != nil {
		return "", fmt.Errorf("write failed: %w", err)
	}

	return tempFile.Name(), nil
}

// installToStorage installs a plugin to persistent storage
func (pm *PluginManager) installToStorage(pl *Plugin, sourcePath string) (string, error) {
	// Create install directory
	persistentPath := getPersistentPath()
	installDir := filepath.Join(persistentPath, pm.storagePrefix, "installed")

	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", err
	}

	// Create destination filename
	destFilename := fmt.Sprintf("%s.zip", pl.FQID)
	destPath := filepath.Join(installDir, destFilename)

	// Copy file
	if err := copyFile(sourcePath, destPath); err != nil {
		return "", err
	}

	return destPath, nil
}

// removeFromStorage removes a plugin from persistent storage
func (pm *PluginManager) removeFromStorage(pl *Plugin) error {
	persistentPath := getPersistentPath()
	installDir := filepath.Join(persistentPath, pm.storagePrefix, "installed")

	// Try multiple possible filenames
	possibleNames := []string{
		fmt.Sprintf("%s.zip", pl.FQID),
		fmt.Sprintf("%s@%s.zip", pl.FQID, pl.Origin),
	}

	for _, name := range possibleNames {
		path := filepath.Join(installDir, name)
		if err := os.Remove(path); err == nil {
			return nil
		}
	}

	return fmt.Errorf("Plugin file not found in storage")
}

// UpgradePlugin upgrades a plugin to the latest version
func (pm *PluginManager) UpgradePlugin(pl *Plugin) error {
	if !pl.CanUpgrade {
		return fmt.Errorf("Plugin cannot be upgraded")
	}

	// Install new version
	if err := pm.InstallPlugin(pl, ""); err != nil {
		return err
	}

	return nil
}

// UpgradeAllPlugins upgrades all upgradable plugins
func (pm *PluginManager) UpgradeAllPlugins() error {
	upgradable := pm.GetUpgradablePlugins()

	for _, pl := range upgradable {
		_ = pm.UpgradePlugin(pl)
	}

	return nil
}

// AutoUpgradePlugins performs automatic upgrade for plugins with auto-upgrade enabled
func (pm *PluginManager) AutoUpgradePlugins() {
	pm.mu.RLock()
	plugins := make([]*Plugin, len(pm.plugins))
	copy(plugins, pm.plugins)
	pm.mu.RUnlock()

	for _, pl := range plugins {
		if !pl.CanUpgrade || !pl.AutoUpgrade {
			continue
		}

		_ = pm.UpgradePlugin(pl)
	}

	pm.UpdateGlobalState()
}

// SetPluginAutoUpgrade sets the auto-upgrade flag for a plugin
func (pm *PluginManager) SetPluginAutoUpgrade(fqid string, autoUpgrade bool) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	var pl *Plugin
	for _, p := range pm.plugins {
		if p.FQID == fqid {
			pl = p
			break
		}
	}
	if pl == nil {
		return fmt.Errorf("Plugin not found: %s", fqid)
	}

	pl.AutoUpgrade = autoUpgrade
	return nil
}

// GetPluginFromZip extracts plugin information from a zip file
func (pm *PluginManager) GetPluginFromZip(zipfile string) (*Plugin, error) {
	// Resolve zip path
	zipPath, err := resolveZipPath(zipfile)
	if err != nil {
		return nil, fmt.Errorf("Unable to open %s -- Not a valid plugin archive", zipfile)
	}

	// Load plugin.json
	pluginJSONPath := fmt.Sprintf("%s/plugin.json", zipPath)
	data, err := loadFile(pluginJSONPath)
	if err != nil {
		return nil, fmt.Errorf("Unable to open %s -- %w", pluginJSONPath, err)
	}

	// Parse control file
	var ctrl PluginControl
	if err := json.Unmarshal(data, &ctrl); err != nil {
		return nil, fmt.Errorf("Unable to parse plugin.json: %w", err)
	}

	if ctrl.ID == "" {
		return nil, fmt.Errorf("Missing plugin ID")
	}

	// Create plugin
	pm.mu.Lock()
	pl := pm.makePluginLocked(ctrl.ID, "local")
	pm.mu.Unlock()

	// Set plugin data
	if ctrl.Title != "" {
		pl.Title = ctrl.Title
	}
	pl.InstVer = ctrl.Version
	pl.Package = zipfile

	// Update state
	pm.UpdateState(pl)

	return pl, nil
}

// ProbeForAutoInstall probes a file for auto-installable plugins
func (pm *PluginManager) ProbeForAutoInstall(fh any, buf []byte, len int, url string) bool {
	// Check if the file is a plugin and if it should be auto-installed
	// ZIP file signature check
	if len < 4 {
		return false
	}

	// ZIP file signature
	if buf[0] == 0x50 && buf[1] == 0x4B && buf[2] == 0x03 && buf[3] == 0x04 {
		return true
	}

	return false
}

// CheckPrefixForAutoInstall checks if a URL prefix should trigger auto-install
func (pm *PluginManager) CheckPrefixForAutoInstall(url string) bool {
	// Check if the URL prefix is configured for auto-install
	return false
}

// Helper functions

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}

func getPersistentPath() string {
	if v := globalPersistentPath.Load(); v != nil {
		return v.(string)
	}
	return "/tmp/movian"
}

var globalPersistentPath atomic.Value // string — C: gconf.persistent_path

// Wired by cmd/movian-go init; nil-safe methods make unwired calls no-ops.

// SetPersistentPath sets the persistent storage path for plugins.
func (pm *PluginManager) SetPersistentPath(path string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.persistentPath = path
	globalPersistentPath.Store(path)
}

// InstallPluginByID installs a plugin by its FQID.
func (pm *PluginManager) InstallPluginByID(id string) error {
	pl := pm.FindPlugin(id)
	if pl == nil {
		return fmt.Errorf("Plugin not found: %s", id)
	}
	return pm.InstallPlugin(pl, "")
}

// UninstallPluginByID removes a plugin by its FQID.
func (pm *PluginManager) UninstallPluginByID(id string) error {
	pl := pm.FindPlugin(id)
	if pl == nil {
		return fmt.Errorf("Plugin not found: %s", id)
	}
	return pm.RemovePlugin(pl)
}
