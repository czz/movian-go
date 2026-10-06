package plugins

import (
	"context"
	"fmt"
	"slices"
)

// Start initializes the plugin subsystem using the provided developer plugins.
// C: plugins_init (plugins.c:1410-1447) — synchronous setup:
//   - plugins_view_settings_init, mutex init, setup_root_props
//   - if gconf.plugin_repo: plugin_repo_create
//   - load dev plugins (PLUGIN_LOAD_FORCE | PLUGIN_LOAD_DEBUG)
//   - plguin_repo_load (load repo list from storage)
//
// Does NOT load installed plugins (that's plugins_load_all in swthread).
func Start(ctx context.Context, pm *PluginManager, devPlugins []string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	pm.devPlugins = slices.Clone(devPlugins)

	// C: plugins_view_settings_init() (plugins.c:1412) — first statement of
	// plugins_init; adds the "Preferred views from plugins" concat source
	// to settings_look_and_feel plus one multiopt per view class.
	pm.pluginsViewSettingsSetup()

	// C: plugins_setup_root_props()
	pm.SetupPluginProperties()
	// C: plugin_setup_start_model()/plugin_setup_repo_model() (plugins.c:1241-1242)
	// — real models are built lazily by the plugin backend
	// (backend.go getOrCreatePluginStartModel/getOrCreatePluginRepoModel).
	// C: plugins.c:1245-1257 — "Plugin repositories" settings group
	// (concat + "Subscribe to plugin repository feed" action)
	pm.setupRepoSettings()

	// C: if(gconf.plugin_repo) plugin_repo_create(gconf.plugin_repo,
	// NULL, 0) (plugins.c:1418-1419) — the --plugin-repo CLI flag seeds
	// an extra repo inside plugins_init, before devplugs load.
	if pm.gcfg().PluginRepo != "" {
		pm.AddRepo(pm.gcfg().PluginRepo, "", false)
	}

	// C: plguin_repo_load() — load repo list from storage
	if err := pm.LoadRepos(); err != nil {
		// Warning: Failed to load repo configuration
	}

	// C: for each devplug: plugin_load(path, "dev", ..., PLUGIN_LOAD_FORCE|PLUGIN_LOAD_DEBUG)
	// Dev plugins are loaded by the caller (cmd/movian-go/init.go) to match C's
	// plugins_init which loads them inline.

	return nil
}

// LoadAll loads installed plugins and refreshes repositories.
// C: plugins_load_all (plugins.c:1265-1270) — called from swthread (background):
//   - lock mutex
//   - plugin_load_installed() — scans persistent_path/mrp/installed
//   - unlock mutex
//
// This is the background-thread counterpart to Start's synchronous setup.
func LoadAll(ctx context.Context, pm *PluginManager) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if pm == nil {
		return fmt.Errorf("plugin manager not initialized")
	}

	// C: plugin_load_installed() — scans persistent_path/mrp/installed
	if err := pm.LoadInstalledPlugins(getPersistentPath()); err != nil {
		// Warning: Failed to load installed plugins
	}

	// C: plugins_upgrade_check() — refresh repos, sweep, autoupgrade
	if err := pm.LoadAllRepos(); err != nil {
		// Warning: Failed to refresh repositories
	}

	pm.UpdateServiceVisibility()
	return nil
}

// Fini shuts down the plugin subsystem and releases resources.
func Fini(ctx context.Context, pm *PluginManager) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if pm == nil {
		return nil
	}

	pm.mu.Lock()
	plugins := make([]*Plugin, len(pm.plugins))
	copy(plugins, pm.plugins)
	pm.mu.Unlock()

	for _, pl := range plugins {
		if pl.Loaded {
			pm.UnloadPlugin(pl)
		}
	}

	pm.SaveRepos()
	return nil
}

// ReloadDevPlugin reloads all configured development plugins.
func ReloadDevPlugin(ctx context.Context, pm *PluginManager) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if pm == nil || len(pm.devPlugins) == 0 {
		return nil
	}

	for _, devPlugin := range pm.devPlugins {
		pl := pm.FindPlugin(devPlugin)
		if pl != nil && pl.Loaded {
			if pl.Unload != nil {
				pl.Unload(pl)
			}
			if err := pm.LoadPlugin(devPlugin, "dev", PluginLoadForce|PluginLoadDebug); err != nil {
				pm.ts.Error("plugins", "Failed to reload dev plugin %s: %v\n", devPlugin, err)
			} else {
				pm.ts.Info("plugins", "Reloaded dev plugin: %s\n", devPlugin)
			}
		}
	}
	return nil
}

// UpgradeCheck refreshes repositories and returns number of upgrades available.
func UpgradeCheck(ctx context.Context, pm *PluginManager) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	if pm == nil {
		return 0, fmt.Errorf("plugin manager not initialized")
	}
	if err := pm.LoadAllRepos(); err != nil {
		// Failed to refresh repositories for upgrade check
		return 0, err
	}
	return pm.UpdateGlobalState(), nil
}

// OpenFile opens a plugin package file and prints its title.
func OpenFile(ctx context.Context, pm *PluginManager, page any, url string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if pm == nil {
		return fmt.Errorf("plugin manager not initialized")
	}
	_, err := pm.GetPluginFromZip(url)
	if err != nil {
		// Failed to get plugin from file
		return err
	}
	// Opening plugin
	return nil
}

// Uninstall removes an installed plugin by ID.
func Uninstall(ctx context.Context, pm *PluginManager, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if pm == nil {
		return fmt.Errorf("Plugin manager not initialized")
	}

	pm.mu.RLock()
	var target *Plugin
	for _, pl := range pm.plugins {
		if pl.FQID == id || pl.FQID == id+"@local" {
			target = pl
			break
		}
	}
	pm.mu.RUnlock()

	if target == nil {
		return fmt.Errorf("Plugin not found: %s", id)
	}
	return pm.RemovePlugin(target)
}

// SelectView triggers a plugin view selection.
func SelectView(ctx context.Context, pm *PluginManager, pluginID, filename string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if pm == nil {
		return fmt.Errorf("plugin manager not initialized")
	}
	pm.SelectView(pluginID, filename)
	return nil
}

// ProbeForAutoInstall inspects a buffer for auto-installable plugins and logs matches.
func ProbeForAutoInstall(ctx context.Context, pm *PluginManager, fh any, buf []byte, length int, url string) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	if pm == nil {
		return false, fmt.Errorf("plugin manager not initialized")
	}
	return pm.ProbeForAutoInstall(fh, buf, length, url), nil
}

// CheckPrefixForAutoInstall reports if a URL prefix should trigger auto-install.
func CheckPrefixForAutoInstall(ctx context.Context, pm *PluginManager, url string) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	if pm == nil {
		return false, fmt.Errorf("plugin manager not initialized")
	}
	return pm.CheckPrefixForAutoInstall(url), nil
}
