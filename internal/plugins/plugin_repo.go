package plugins

// Plugin Repository Management - Repository loading and management
// This file contains functions for managing plugin repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
)

// RepoError represents a repository error
type RepoError struct {
	Message string
	Network bool // True if this is a network error
}

func (e *RepoError) Error() string {
	return e.Message
}

// LoadRepo loads a repository from a URL
func (pm *PluginManager) LoadRepo(pr *PluginRepo) error {
	// Fetch repo data without holding the lock (network I/O)
	repoData, err := pm.fetchRepo(pr.URL)
	if err != nil {
		if re, ok := err.(*RepoError); ok && re.Network {
			return err
		}
		return err
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Clear auto plugins
	pm.autoPluginClearLocked()

	// Set repo title
	if repoData.Title != "" {
		pr.Title = repoData.Title
		// C: prop_set_string(pr->pr_title, title) (plugins.c:955)
		if pr.TitleProp != nil {
			pm.propManager.SetStringEx(pr.TitleProp, nil, repoData.Title,
				propcore.StringUTF8)
		}
	}

	// Load plugins from repo
	for _, pluginCtrl := range repoData.Plugins {
		if err := pm.loadRepoPlugin(pr, pluginCtrl); err != nil {
			// Skip plugins that fail to load
			_ = err
		}
	}

	pr.Initialized = true
	return nil
}

// fetchRepo fetches repository data from a URL.
// Supports HTTP/HTTPS URLs and local file paths.
// Caller must NOT hold pm.mu.
func (pm *PluginManager) fetchRepo(repoURL string) (*PluginRepoData, error) {
	var data []byte
	var err error

	if strings.HasPrefix(repoURL, "http://") || strings.HasPrefix(repoURL, "https://") {
		data, err = fetchHTTP(repoURL)
	} else {
		data, err = loadFile(repoURL)
	}

	if err != nil {
		return nil, &RepoError{Message: fmt.Sprintf("Unable to load repo %s -- %v", repoURL, err), Network: true}
	}

	var repoData PluginRepoData
	if err := json.Unmarshal(data, &repoData); err != nil {
		return nil, &RepoError{Message: "Malformed JSON in repository"}
	}

	// Validate version
	if repoData.Version != 1 {
		return nil, &RepoError{Message: fmt.Sprintf("Unsupported repository version %d", repoData.Version)}
	}

	// Check for error message
	if repoData.Message != "" {
		return nil, &RepoError{Message: repoData.Message}
	}

	return &repoData, nil
}

// fetchHTTP performs an HTTP GET request and returns the response body.
func fetchHTTP(url string) ([]byte, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return body, nil
}

// loadRepoPlugin loads a plugin from repository data
func (pm *PluginManager) loadRepoPlugin(pr *PluginRepo, ctrl *PluginControl) error {
	// Validate required fields
	if ctrl.ID == "" {
		return fmt.Errorf("Missing plugin ID")
	}
	if ctrl.Version == "" {
		return fmt.Errorf("Missing plugin version")
	}

	// Skip old Spidermonkey plugins
	if ctrl.Type == "javascript" {
		return nil
	}

	// Skip bitcode — VMIR native plugins are deliberately not supported
	// (ENABLE_VMIR=0 by design; ext/vmir + src/np are excluded from the
	// port)
	if ctrl.Type == "bitcode" {
		return nil
	}

	// Check blacklist
	if blacklisted, _ := pm.IsPluginBlacklisted(ctrl.ID, ctrl.Version); blacklisted {
		return nil
	}

	// Validate download URL
	if ctrl.DownloadURL == "" {
		return fmt.Errorf("Missing downloadURL")
	}

	// Resolve package URL
	packageURL, err := resolveRelativeURL(pr.URL, ctrl.DownloadURL)
	if err != nil {
		return fmt.Errorf("Invalid download URL: %w", err)
	}

	// Create origin hash
	origin := originHash(packageURL)

	// Get or create plugin
	pl := pm.makePluginLocked(ctrl.ID, origin)

	// Update plugin data
	pl.Package = packageURL
	pl.Mark = false

	// C: plugin_prop_setup(pm, pl, url) (plugins.c:996) — creates
	// plugin_root_list.<fqid> + fills metadata for repo-listed plugins.
	// C order: prop setup before repo_ver/app_min_version, then update_state.
	pm.pluginPropSetup(ctrl, pl, pr.URL)

	pl.RepoVer = ctrl.Version
	pl.AppMinVersion = ctrl.ShowtimeVersion

	// Set title if not set
	if ctrl.Title != "" {
		pl.Title = ctrl.Title
	}

	// Update state
	pm.UpdateState(pl)

	// Create auto plugin if control data exists
	if ctrl.Control != nil {
		pm.autoPluginCreateFromControlLocked(ctrl.ID, ctrl.Control, pl.Installed)
	}

	return nil
}

// resolveRelativeURL resolves a relative URL against a base URL
func resolveRelativeURL(base, rel string) (string, error) {
	if strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
		return rel, nil
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}

	relURL, err := url.Parse(rel)
	if err != nil {
		return "", err
	}

	resolved := baseURL.ResolveReference(relURL)
	return resolved.String(), nil
}

// LoadAllRepos loads all configured repositories
func (pm *PluginManager) LoadAllRepos() error {
	pm.mu.RLock()
	repos := make([]*PluginRepo, len(pm.repos))
	copy(repos, pm.repos)
	pm.mu.RUnlock()

	// Mark all plugins
	pm.MarkPlugins()

	// Load each repo
	for _, pr := range repos {
		_ = pm.LoadRepo(pr)
	}

	// Sweep plugins not in repos
	pm.SweepPlugins()

	return nil
}

// RefreshRepo refreshes a specific repository
func (pm *PluginManager) RefreshRepo(repoURL string) error {
	pm.mu.RLock()
	var pr *PluginRepo
	for _, r := range pm.repos {
		if r.URL == repoURL {
			pr = r
			break
		}
	}
	pm.mu.RUnlock()

	if pr == nil {
		return fmt.Errorf("Repository not found: %s", repoURL)
	}

	return pm.LoadRepo(pr)
}

// AddRepoFromURL adds a repository from a URL
func (pm *PluginManager) AddRepoFromURL(repoURL string) (*PluginRepo, error) {
	// Validate URL
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		return nil, fmt.Errorf("Invalid repository URL: %s", repoURL)
	}

	// Check if repo already exists
	pm.mu.RLock()
	for _, pr := range pm.repos {
		if pr.URL == repoURL {
			pm.mu.RUnlock()
			return pr, nil
		}
	}
	pm.mu.RUnlock()

	// Add repo
	pr := pm.AddRepo(repoURL, "", false)

	// Load repo to get title
	if err := pm.LoadRepo(pr); err != nil {
		// Remove repo if load fails
		pm.RemoveRepo(repoURL)
		return nil, err
	}

	return pr, nil
}

// RemoveRepoByURL removes a repository by URL
func (pm *PluginManager) RemoveRepoByURL(repoURL string) error {
	if !pm.RemoveRepo(repoURL) {
		return fmt.Errorf("Repository not found: %s", repoURL)
	}
	return nil
}

// SetRepoAutoUpgrade sets the auto-upgrade flag for a repository
func (pm *PluginManager) SetRepoAutoUpgrade(repoURL string, autoUpgrade bool) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, pr := range pm.repos {
		if pr.URL == repoURL {
			pr.AutoUpgrade = autoUpgrade
			return nil
		}
	}

	return fmt.Errorf("Repository not found: %s", repoURL)
}

// GetRepoByURL gets a repository by URL
func (pm *PluginManager) GetRepoByURL(repoURL string) *PluginRepo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, pr := range pm.repos {
		if pr.URL == repoURL {
			return pr
		}
	}
	return nil
}

// SaveRepos — C: plguin_repo_save (plugins.c:1180-1193). Persists a
// list of {url} maps via htsmsg_store_save(m, "pluginrepos").
func (pm *PluginManager) SaveRepos() error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.saveReposLocked()
}

// saveReposLocked — caller holds pm.mu (write). C: plugin_mutex is
// held by callers of plugin_install/plugin_remove.
func (pm *PluginManager) saveReposLocked() error {
	m := htsmsg.NewList()
	for _, pr := range pm.repos {
		e := htsmsg.NewMap()
		e.AddStr("url", pr.URL)
		m.AddMsg("", e)
	}
	store := pm.store
	var err error
	if store != nil {
		err = store.Save(m, "pluginrepos")
	}
	m.Release()
	return err
}

// LoadRepos — C: plguin_repo_load (plugins.c:1196-1208). Iterates the
// stored list and calls plugin_repo_create(url, NULL, 0).
func (pm *PluginManager) LoadRepos() error {
	store := pm.store
	if store == nil {
		return nil
	}
	m, err := store.Load("pluginrepos")
	if err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	for _, f := range m.GetFields() {
		e := f.GetMap()
		if e == nil {
			continue
		}
		url := e.GetStr("url")
		// C: plugin_repo_create(url, NULL, 0)
		pm.AddRepo(url, "", false)
	}
	return nil
}

// periodicUpdateCheck periodically refreshes repos and performs auto-upgrades.
func (pm *PluginManager) periodicUpdateCheck(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = pm.LoadAllRepos()
			upgrades := pm.UpdateGlobalState()
			if upgrades > 0 {
				pm.AutoUpgradePlugins()
			}
		}
	}
}

// Helper functions for storage

// pluginsAddRepoPopup — C: plugins_add_repo_popup (plugins.c:1216-1228).
// Settings action callback ("Subscribe to plugin repository feed"):
// shows a text dialog, then plugin_repo_create(url, NULL, 1) +
// plguin_repo_save(). The event arg is unused (C ignores it too).
func (pm *PluginManager) pluginsAddRepoPopup(opaque any, value any) {
	var url string
	if pm.notifMgr == nil || pm.notifMgr.TextDialog("Enter URL of plugin repository", &url,
		notifications.MessagePopupOK|notifications.MessagePopupCancel) != 0 {
		return
	}
	pm.AddRepo(url, "", true)
	pm.SaveRepos()
}

// setAutoupgrade — C: set_autoupgrade (plugins.c:1305-1314). In C the
// settings subscription dispatches with plugin_mutex held; here the
// callback takes pm.mu at entry — same coverage.
func (pm *PluginManager) setAutoupgrade(pr *PluginRepo, value any) {
	v, _ := value.(int)

	pm.mu.Lock()
	pr.AutoUpgrade = v != 0
	initialized := pr.Initialized
	pm.mu.Unlock()

	// C: if(value && pr->pr_started) { if(!plugin_load_repo(pr))
	//      update_global_state(); }
	if v != 0 && initialized {
		if pm.LoadRepo(pr) == nil {
			pm.UpdateGlobalState()
		}
	}
}

// pluginRepoDelete — C: plugin_repo_delete (plugins.c:1318-1340):
// LIST_REMOVE from plugin_repos; prop_destroy(pr_root); if the triggering
// event carries a navigator (e_nav), send ACTION_NAV_BACK to its eventSink;
// plugin_update_service(); plguin_repo_save().
func (pm *PluginManager) pluginRepoDelete(pr *PluginRepo, value any) {
	// C: LIST_REMOVE(pr, pr_link)
	pm.mu.Lock()
	for i, r := range pm.repos {
		if r == pr {
			pm.repos = slices.Delete(pm.repos, i, i+1)
			break
		}
	}
	pm.mu.Unlock()

	// C: prop_destroy(pr->pr_root)
	if pr.Root != nil && pm.propManager != nil {
		pm.propManager.Destroy(pr.Root)
		pr.Root = nil
	}

	// C: if(e->e_nav != NULL) { prop_send_ext_event(
	//      prop_create_r(e->e_nav, "eventSink"),
	//      event_create_action(ACTION_NAV_BACK)) }
	if e, ok := value.(*event.Event); ok && e != nil && e.Nav != nil &&
		pm.eventManager != nil && pm.propManager != nil {
		be := pm.eventManager.CreateAction(event.ACTION_NAV_BACK).AsEvent()
		es := pm.propManager.CreateEx(e.Nav, "eventSink", nil, false, false)
		if es != nil {
			pm.propManager.SendExtEvent(es, be)
		}
		// C: event_release(be) after prop_send_ext_event
		be.Release()
	}

	// C: plugin_update_service(); plguin_repo_save()
	pm.UpdatePluginService()
	pm.SaveRepos()
}
