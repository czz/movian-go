package sd

import (
	"fmt"
	"runtime/cgo"
	"slices"

	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// C: service_class_t (src/sd/sd.h)
const (
	ServiceHtsp   = iota // C: SERVICE_HTSP
	ServiceWebdav        // C: SERVICE_WEBDAV
)

// C: service_instance_t (src/sd/sd.h)
// LIST_ENTRY(service_instance) si_link — emulated by slice position in the
// owning backend's `services` list.
type serviceInstance struct {
	id             string            // C: si_id
	opaque         any               // C: si_opaque
	url            string            // C: si_url
	settings       *propcore.Prop    // C: si_settings
	settingEnabled *settings.Setting // C: si_setting_enabled
	settingTitle   *settings.Setting // C: si_setting_title
	settingType    *settings.Setting // C: si_setting_type
	probe          int               // C: si_probe
	enabled        int               // C: si_enabled
	service        *service.Service  // C: si_service
	// Go-only: cgo handle used when this instance is passed as void* userdata
	// to a C discovery API (Go pointers cannot cross the cgo boundary).
	handle cgo.Handle
}

// System — the service-discovery instance. Holds what sd.c + the
// avahi/bonjour backends read as file-statics and the injected process
// deps (C's implicit globals: gconf.settings_sd, the service system,
// the prop system, trace()).
type System struct {
	serviceSystem *service.ServiceSystem
	settingsMgr   *settings.SettingsManager
	propMgr       *propcore.PropManager
	ts            *trace.TraceSystem // C: trace() global — injected
	gconf         *gconf.T           // C: gconf_t — injected

	avahi   *avahiDaemon   // C: avahi.c statics — nil unless linux+cgo
	bonjour *bonjourDaemon // C: bonjour.c statics — nil unless darwin
}

// NewSystem creates the service-discovery context — C: the sd.c file
// statics, owned by main.
func NewSystem() *System { return &System{} }

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (s *System) SetGconf(g *gconf.T) { s.gconf = g }

/**
 * C: si_find
 */
func siFind(services []*serviceInstance, id string) *serviceInstance {
	for _, si := range services {
		if si.id == id {
			return si
		}
	}
	return nil
}

/**
 * C: si_destroy — LIST_REMOVE is done against the owning backend list
 * (C embeds si_link so LIST_REMOVE needs no list head).
 */
func (s *System) siDestroy(services *[]*serviceInstance, si *serviceInstance) {
	if si.service != nil {
		s.serviceSystem.ServiceDestroy(si.service)
	}

	s.settingsMgr.Destroy(si.settingEnabled)
	s.settingsMgr.Destroy(si.settingTitle)
	s.settingsMgr.Destroy(si.settingType)

	s.propMgr.Destroy(si.settings)

	// C: LIST_REMOVE(si, si_link)
	for i, s := range *services {
		if s == si {
			*services = slices.Delete((*services), i, i+1)
			break
		}
	}

	if si.handle != 0 {
		si.handle.Delete()
	}

	// C: free(si->si_id); free(si->si_url); free(si) — GC
}

/**
 * C: sd_add_service (static)
 */
func (s *System) sdAddService(si *serviceInstance, title, url,
	contents string, probe int, description string) {
	si.probe = probe

	if si.settings != nil {
		return
	}

	si.url = url // C: si->si_url = strdup(url)

	// C: snprintf(store, sizeof(store), "sd/%s", url); str_cleanup(store + 3, "/:")
	store := []byte("sd/" + url)
	misc.StrCleanup(store[3:], "/:")

	// C: settings_add_dir_cstr(gconf.settings_sd, title, NULL, NULL, description, NULL)
	si.settings = s.settingsMgr.AddDirCStr(s.settingsMgr.SD(),
		title, "", "", description, "")

	// C: service_create(si->si_id, NULL, si->si_url, NULL, NULL,
	//                   si->si_probe, 0, SVC_ORIGIN_DISCOVERED)
	si.service = s.serviceSystem.ServiceCreate(si.id, "", si.url, "", "",
		si.probe != 0, false, service.SvcOriginDiscovered)

	r := si.service.Root() // C: si->si_service->s_root

	si.settingEnabled =
		s.settingsMgr.SettingCreate(settings.SettingBool, si.settings, settings.SettingsInitialUpdate,
			settings.SettingTagTitle, s.settingsMgr.P("Enabled on home screen"),
			settings.SettingTagValue, 0,
			settings.SettingTagWriteProp, s.propMgr.CreateEx(r, "enabled", nil, false, false),
			settings.SettingTagStore, string(store), "enabled")

	si.settingTitle =
		s.settingsMgr.SettingCreate(settings.SettingString, si.settings,
			settings.SettingsInitialUpdate,
			settings.SettingTagTitle, s.settingsMgr.P("Name"),
			settings.SettingTagValue, title,
			settings.SettingTagWriteProp, s.propMgr.CreateEx(r, "title", nil, false, false),
			settings.SettingTagStore, string(store), "title")

	si.settingType =
		s.settingsMgr.SettingCreate(settings.SettingString, si.settings, settings.SettingsInitialUpdate,
			settings.SettingTagTitle, s.settingsMgr.P("Type"),
			settings.SettingTagValue, contents,
			settings.SettingTagWriteProp, s.propMgr.CreateEx(r, "type", nil, false, false),
			settings.SettingTagStore, string(store), "type")
}

/**
 * HTSP service creator
 * C: sd_add_service_htsp
 */
func (s *System) sdAddServiceHtsp(si *serviceInstance, name, host string,
	port int) {
	url := fmt.Sprintf("htsp://%s:%d", host, port)
	buf := fmt.Sprintf("Tvheadend TV streaming server on %s", host)
	s.sdAddService(si, name, url, "tv", 0, buf)
}

/**
 * Webdav service creator
 * C: sd_add_service_webdav
 */
func (s *System) sdAddServiceWebdav(si *serviceInstance, name, host string,
	port int, path, contents string) {
	// C: path == NULL || path[0] != '/' ? "/" : ""  — "" is Go's NULL+empty
	sep := ""
	if path == "" || path[0] != '/' {
		sep = "/"
	}
	url := fmt.Sprintf("webdav://%s:%d%s%s", host, port, sep, path)
	buf := fmt.Sprintf("WEBDAV share on %s:%d%s%s", host, port, sep, path)
	s.sdAddService(si, name, url, contents, 1, buf)
}

/**
 * C: sd_init — calls the enabled discovery backends
 * (avahi under CONFIG_AVAHI, bonjour under CONFIG_BONJOUR).
 */
func (s *System) sdStart() {
	s.avahiStart()
	s.bonjourStart()
}

// Start initializes the service discovery system and stores the C deps
// (service system, settings manager, prop manager) that sd.c reads
// directly. C: sd_init
func (s *System) Start(ss *service.ServiceSystem,
	sm *settings.SettingsManager, pm *propcore.PropManager,
	ts *trace.TraceSystem) {
	s.serviceSystem = ss
	s.settingsMgr = sm
	s.propMgr = pm
	s.ts = ts
	s.sdStart()
}
