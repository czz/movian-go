package service

import (
	"fmt"
	"github.com/czz/movian-go/internal/trace"
	"slices"
	"strings"
	"sync"

	propcore "github.com/czz/movian-go/internal/prop"
)

// Service status types
const (
	SvcStatusOK         = 0
	SvcStatusAuthNeeded = 1
	SvcStatusNoHandler  = 2
	SvcStatusFail       = 3
	SvcStatusScanning   = 4
)

// Service origin types
const (
	SvcOriginSystem     = 0
	SvcOriginBookmark   = 1
	SvcOriginDiscovered = 2
	SvcOriginApp        = 3
	SvcOriginMedia      = 4
)

// Service represents a service
type Service struct {
	mu             sync.Mutex
	id             string
	root           *propcore.Prop // prop_t
	propType       *propcore.Prop // prop_t
	propStatus     *propcore.Prop // prop_t
	propStatusTxt  *propcore.Prop // prop_t
	url            string
	title          string
	typ            string
	icon           string
	ref            int
	zombie         bool
	doProbe        bool
	needProbe      bool
	enabled        bool
	origin         int
	status         int
	settings       *propcore.Prop         // prop_t — s_settings
	settingEnabled any                    // setting_t — s_setting_enabled
	settingTitle   any                    // setting_t — s_setting_title
	settingType    any                    // setting_t — s_setting_type
	reqDelSub      *propcore.Subscription // prop_sub_t — s_req_del_sub
}

// ServiceSystem manages service subsystem without global state
type ServiceSystem struct {
	mutex           sync.Mutex
	ts              *trace.TraceSystem // C: trace() global — injected
	cond            *sync.Cond
	globalServices  *propcore.Prop // $global.services
	allServices     *propcore.Prop // $global.services.all
	enabledServices *propcore.Prop // $global.services.enabled
	stableServices  *propcore.Prop // $global.services.stable
	discoveredNodes *propcore.Prop // $global.services.discovered
	services        []*Service
	initialized     bool
	propMgr         *propcore.PropManager
	settingsHooks   *ServiceSettingsHooks // C: gconf.settings_sd / setting_* access
	backendHooks    *ServiceBackendHooks  // C: backend_probe / be_normalize
}

// SetTraceSystem injects the trace system (C: trace() global).
func (ss *ServiceSystem) SetTraceSystem(ts *trace.TraceSystem) { ss.ts = ts }

// ServiceBackendHooks — the backend seams used by seturl
// (backend_canhandle + be_normalize) and service_probe_loop
// (backend_probe). service cannot import backend/core (cycle:
// backend/core → service for discovered_nodes), so cmd/movian-go injects
// closures bound to BackendSystem.
type ServiceBackendHooks struct {
	// C: backend_probe(url, txt, sizeof(txt), 0) → backend_probe_result_t
	Probe func(url string, timeoutMs int) (int, error)
	// C: backend_canhandle + be->be_normalize in seturl
	NormalizeURL func(url string) string
}

// SetBackendHooks injects the backend seams used by seturl and
// service_probe_loop (C: global backend_probe/backend_canhandle calls).
func (ss *ServiceSystem) SetBackendHooks(h *ServiceBackendHooks) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.backendHooks = h
}

// ServiceSettingsHooks — the subset of the settings subsystem used by
// service_create_managed / service_destroy. service cannot import
// settings/core (cycle: settings/core → db → fileaccess/core → arch →
// service), so cmd/movian-go injects closures bound to settingscore —
// same seam as FASettingsStartHook. setting_t is opaque here.
type ServiceSettingsHooks struct {
	SD     func() *propcore.Prop // gconf.settings_sd
	P      func(string) *propcore.Prop
	AddDir func(parent, title *propcore.Prop, subtype, icon string,
		url string) *propcore.Prop
	// C: setting_create(SETTING_BOOL, s, SETTINGS_INITIAL_UPDATE,
	//   SETTING_TITLE, SETTING_VALUE, SETTING_WRITE_PROP, SETTING_STORE)
	CreateBoundBool func(parent, title *propcore.Prop, value int,
		writeProp *propcore.Prop, store, key string) any
	// C: setting_create(SETTING_STRING, ...) — same tags, string value
	CreateBoundString func(parent, title *propcore.Prop, value string,
		writeProp *propcore.Prop, store, key string) any
	// C: setting_set(s, SETTING_BOOL, v)
	SetBool func(s any, v int)
	// C: setting_destroy(s)
	Destroy func(s any)
}

// SetSettingsHooks injects the settings seams used by
// ServiceCreateManaged (C: gconf.settings_sd global).
func (ss *ServiceSystem) SetSettingsHooks(h *ServiceSettingsHooks) {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.settingsHooks = h
}

// NewServiceSystem creates a new service system
func NewServiceSystem(pm *propcore.PropManager) *ServiceSystem {
	return &ServiceSystem{
		propMgr: pm,
	}
}

// Start initializes the service system
func (ss *ServiceSystem) Start() {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()

	if ss.initialized {
		return
	}
	ss.cond = sync.NewCond(&ss.mutex)
	ss.services = make([]*Service, 0)

	// Create $global.services property tree
	globalProp := ss.propMgr.GetGlobal()
	ss.globalServices = ss.propMgr.CreateEx(globalProp, "services", nil, false, false)
	ss.allServices = ss.propMgr.CreateEx(ss.globalServices, "all", nil, false, false)
	ss.enabledServices = ss.propMgr.CreateEx(ss.globalServices, "enabled", nil, false, false)
	ss.stableServices = ss.propMgr.CreateEx(ss.globalServices, "stable", nil, false, false)
	ss.discoveredNodes = ss.propMgr.CreateEx(ss.globalServices, "discovered", nil, false, false)

	// Create $global.clipboard property tree for clipboard functionality
	clipboardProp := ss.propMgr.CreateEx(globalProp, "clipboard", nil, false, false)
	_ = ss.propMgr.CreateEx(clipboardProp, "copyprogress", nil, false, false)

	// Create $global.system property tree for system information
	systemProp := ss.propMgr.CreateEx(globalProp, "system", nil, false, false)
	cpuinfoProp := ss.propMgr.CreateEx(systemProp, "cpuinfo", nil, false, false)
	_ = ss.propMgr.CreateEx(cpuinfoProp, "cpus", nil, false, false)

	// C: service.c:118-145 — enabled/stable/discovered are live views
	// over all_services, not copies:
	//
	//   enabled    = prop_nf_create(enabled, all) + pred node.enabled==0 EXCLUDE
	//   tmp        = prop_create_root(NULL)
	//                prop_nf_create(tmp, all) + same pred
	//   stable     = prop_create(gs,"stable")
	//                prop_reorder_create(stable, tmp, 0, "allSourcesOrder")
	//   discovered = prop_nf_create(discovered, all) + pred
	//                node.origin!="discovered" EXCLUDE
	pnf := propcore.PropNFCreate(ss.enabledServices, ss.allServices, nil, 0)
	propcore.PropNFPredIntAdd(pnf, "node.enabled",
		propcore.PropNFCmpEq, 0, nil, propcore.PropNFModeExclude)

	tmp := ss.propMgr.CreateRootEx("", false)
	pnf = propcore.PropNFCreate(tmp, ss.allServices, nil, 0)
	propcore.PropNFPredIntAdd(pnf, "node.enabled",
		propcore.PropNFCmpEq, 0, nil, propcore.PropNFModeExclude)
	ss.propMgr.PropReorderCreate(ss.stableServices, tmp, 0, "allSourcesOrder")

	pnf = propcore.PropNFCreate(ss.discoveredNodes, ss.allServices, nil, 0)
	propcore.PropNFPredStrAdd(pnf, "node.origin",
		propcore.PropNFCmpNeq, "discovered", nil, propcore.PropNFModeExclude)

	// Create system services. In C Movian, only "Local network" and "Settings"
	// are created in C; the rest are registered by JavaScript plugins.
	type svcDef struct{ id, title, url, typ, icon string }
	// C: service.c:107-113 creates discovered and settings with icon=NULL
	// (void prop). The home.view translate() table maps type→icon instead.
	// Go must match: leave icon empty so the prop is void and ?? triggers.
	sysSvcs := []svcDef{
		{"showtime:discovered", "Local network", "discovered:", "network", ""},
		{"showtime:settings", "Settings", "settings:", "setting", ""},
	}
	for _, d := range sysSvcs {
		ss.serviceCreate0(d.id, "", d.title, d.url, d.typ, d.icon, false, true, SvcOriginSystem)
	}

	ss.initialized = true
}

// Destroy destroys the service system
func (ss *ServiceSystem) Destroy() {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()

	// C: while((s = LIST_FIRST(&services)) != NULL) service_destroy(s)
	// serviceDestroyLocked removes the head via LIST_REMOVE — drain.
	for len(ss.services) > 0 {
		ss.serviceDestroyLocked(ss.services[0])
	}
	ss.initialized = false
}

// ServiceDestroy destroys a service
func (ss *ServiceSystem) ServiceDestroy(s *Service) {
	if s == nil {
		return
	}
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.serviceDestroyLocked(s)
}

// serviceDestroyLocked destroys a service assuming the mutex is already held
// C: service_destroy (service.c:153-178)
func (ss *ServiceSystem) serviceDestroyLocked(s *Service) {
	// C: prop_destroy(s->s_root);
	if s.root != nil {
		ss.propMgr.Destroy(s.root)
		s.root = nil
	}

	// C: if(s->s_setting_enabled != NULL) setting_destroy(...); (×3)
	if ss.settingsHooks != nil {
		if s.settingEnabled != nil {
			ss.settingsHooks.Destroy(s.settingEnabled)
			s.settingEnabled = nil
		}
		if s.settingTitle != nil {
			ss.settingsHooks.Destroy(s.settingTitle)
			s.settingTitle = nil
		}
		if s.settingType != nil {
			ss.settingsHooks.Destroy(s.settingType)
			s.settingType = nil
		}
	}

	// C: prop_destroy(s->s_settings);
	if s.settings != nil {
		ss.propMgr.Destroy(s.settings)
		s.settings = nil
	}

	// C: if(s->s_req_del_sub != NULL) prop_unsubscribe(s->s_req_del_sub);
	if s.reqDelSub != nil {
		s.reqDelSub.Unsubscribe()
		s.reqDelSub = nil
	}

	// C: LIST_REMOVE(s, s_link);
	for i, svc := range ss.services {
		if svc == s {
			ss.services = slices.Delete(ss.services, i, i+1)
			break
		}
	}

	s.zombie = true
	s.ref--
	if s.ref == 0 {
		// In a full implementation, this would free resources
	}
}

// ServiceCreate creates a service
func (ss *ServiceSystem) ServiceCreate(id, title, url, typ, icon string, probe, enabled bool, origin int) *Service {
	return ss.serviceCreate0(id, title, "", url, typ, icon, probe, enabled, origin)
}

// ServiceCreatep creates a service with prop title
func (ss *ServiceSystem) ServiceCreatep(id string, ptitle any, url, typ, icon string, probe, enabled bool, origin int) *Service {
	return ss.serviceCreate0(id, "", ptitle, url, typ, icon, probe, enabled, origin)
}

// ServiceCreateManaged creates a managed service
// C: service_create_managed (service.c:326-381) — creates a settings dir
// under gconf.settings_sd with Enabled/Name/Type settings bound to the
// service root props and persisted under "managed_service2/<id>".
func (ss *ServiceSystem) ServiceCreateManaged(id, title, url, typ, icon string, probe, enabled bool, origin int) *Service {
	s := ss.serviceCreate0(id, title, "", url, typ, icon, probe, enabled, origin)

	if s != nil {
		s.mu.Lock()
		s.title = title
		s.mu.Unlock()

		ss.mutex.Lock()
		hooks := ss.settingsHooks
		ss.mutex.Unlock()

		if hooks != nil {
			// C: snprintf(store, sizeof(store), "managed_service2/%s", id);
			store := fmt.Sprintf("managed_service2/%s", id)

			// C: s->s_settings = settings_add_dir(gconf.settings_sd,
			//     prop_create(s->s_root, "title"), type, icon, NULL, NULL);
			s.settings = hooks.AddDir(hooks.SD(),
				ss.propMgr.CreateEx(s.root, "title", nil, false, false),
				typ, icon, "")

			// C: SETTING_TITLE(_p("Enabled on home screen")),
			//    SETTING_VALUE(enabled),
			//    SETTING_WRITE_PROP(prop_create(s->s_root, "enabled")),
			//    SETTING_STORE(store, "enabled")
			enabledInt := 0
			if enabled {
				enabledInt = 1
			}
			s.settingEnabled = hooks.CreateBoundBool(s.settings,
				hooks.P("Enabled on home screen"), enabledInt,
				ss.propMgr.CreateEx(s.root, "enabled", nil, false, false),
				store, "enabled")

			// C: SETTING_TITLE(_p("Name")), SETTING_VALUE(title),
			//    SETTING_WRITE_PROP(prop_create(s->s_root, "title")),
			//    SETTING_STORE(store, "title")
			s.settingTitle = hooks.CreateBoundString(s.settings,
				hooks.P("Name"), title,
				ss.propMgr.CreateEx(s.root, "title", nil, false, false),
				store, "title")

			// C: SETTING_TITLE(_p("Type")), SETTING_VALUE(type),
			//    SETTING_WRITE_PROP(prop_create(s->s_root, "type")),
			//    SETTING_STORE(store, "type")
			s.settingType = hooks.CreateBoundString(s.settings,
				hooks.P("Type"), typ,
				ss.propMgr.CreateEx(s.root, "type", nil, false, false),
				store, "type")

			// C: prop_set(s->s_root, "deleteText", PROP_SET_LINK,
			//     _p("Remove from homepage"));
			deleteText := ss.propMgr.CreateEx(s.root, "deleteText", nil, false, false)
			ss.propMgr.Link(hooks.P("Remove from homepage"), deleteText, nil, false, false)
		}

		// C: s->s_req_del_sub = prop_subscribe(0,
		//     PROP_TAG_CALLBACK, service_req_del_callback, s,
		//     PROP_TAG_MUTEX, &service_mutex, PROP_TAG_ROOT, s->s_root, NULL);
		s.reqDelSub = s.root.Subscribe(
			func(opaque any, event propcore.EventType, args ...any) {
				// C: if(event == PROP_REQ_DELETE)
				//      setting_set(s->s_setting_enabled, SETTING_BOOL, 0);
				if event == propcore.EventReqDelete &&
					s.settingEnabled != nil && hooks != nil {
					hooks.SetBool(s.settingEnabled, 0)
				}
			}, s, propcore.SubMutex{Ptr: &ss.mutex})
	}

	return s
}

// serviceCreate0 internal service creation
func (ss *ServiceSystem) serviceCreate0(id, title string, ptitle any, url, typ, icon string, probe, enabled bool, origin int) *Service {

	// Resolve effective title from ptitle if title is empty
	effectiveTitle := title
	if effectiveTitle == "" {
		if str, ok := ptitle.(string); ok {
			effectiveTitle = str
		}
	}

	s := &Service{
		id:        id,
		title:     effectiveTitle,
		typ:       typ,
		icon:      icon,
		ref:       1,
		zombie:    false,
		doProbe:   probe,
		needProbe: probe,
		enabled:   enabled,
		origin:    origin,
		status:    SvcStatusOK,
	}

	// Create property tree for this service under $global.services.all
	// In C, prop_create_root(id) creates a NEW prop each time even with the same id.
	// In Go, CreateEx returns the existing child for duplicate names.
	// To match C behavior, append a unique suffix when a service with the same id already exists.
	rootName := id
	if existing := ss.allServices.FindChild(rootName); existing != nil {
		// Append URL to make it unique
		rootName = id + ":" + url
	}
	s.root = ss.propMgr.CreateEx(ss.allServices, rootName, nil, false, false)

	// C: seturl(s, url) — backend_canhandle + be_normalize
	ss.seturl(s, url)

	// Set URL
	urlProp := ss.propMgr.CreateEx(s.root, "url", nil, false, false)
	ss.propMgr.SetStringEx(urlProp, nil, url, propcore.StringUTF8)

	// C: prop_t *t = prop_create(p, "title");
	//    if(ptitle) prop_link(ptitle, t); else prop_set_string(t, title);
	titleProp := ss.propMgr.CreateEx(s.root, "title", nil, false, false)
	if tp, ok := ptitle.(*propcore.Prop); ok && tp != nil {
		ss.propMgr.Link(tp, titleProp, nil, false, false)
	} else {
		ss.propMgr.SetStringEx(titleProp, nil, effectiveTitle, propcore.StringUTF8)
	}

	// C: prop_link(t, prop_create(metadata, "title")) — metadata.title
	// is a LINK to the title prop, so it follows title updates.
	metadataProp := ss.propMgr.CreateEx(s.root, "metadata", nil, false, false)
	metadataTitleProp := ss.propMgr.CreateEx(metadataProp, "title", nil, false, false)
	ss.propMgr.Link(titleProp, metadataTitleProp, nil, false, false)

	// Set icon
	iconProp := ss.propMgr.CreateEx(s.root, "icon", nil, false, false)
	if icon != "" {
		ss.propMgr.SetStringEx(iconProp, nil, icon, propcore.StringUTF8)
	}

	// Set enabled
	enabledProp := ss.propMgr.CreateEx(s.root, "enabled", nil, false, false)
	if enabled {
		ss.propMgr.SetIntEx(enabledProp, nil, 1)
	} else {
		ss.propMgr.SetIntEx(enabledProp, nil, 0)
	}

	// C: prop_set_string(s->s_prop_type, type) — unconditional
	s.propType = ss.propMgr.CreateEx(s.root, "type", nil, false, false)
	ss.propMgr.SetStringEx(s.propType, nil, typ, propcore.StringUTF8)

	// C: prop_set_string(s->s_prop_status, "ok") — string, not int
	s.propStatus = ss.propMgr.CreateEx(s.root, "status", nil, false, false)
	ss.propMgr.SetStringEx(s.propStatus, nil, "ok", propcore.StringUTF8)

	// C: s->s_prop_status_txt = prop_create(p, "statustxt") — no value
	s.propStatusTxt = ss.propMgr.CreateEx(s.root, "statustxt", nil, false, false)

	// Set origin
	originProp := ss.propMgr.CreateEx(s.root, "origin", nil, false, false)
	ss.propMgr.SetStringEx(originProp, nil, OriginToString(origin), propcore.StringUTF8)

	// C: LIST_INSERT_HEAD(&services, s, s_link)
	ss.services = slices.Insert(ss.services, 0, s)

	if ss.cond != nil {
		ss.cond.Signal()
	}

	return s
}

// seturl — C: seturl (service.c:197-209): if the backend can handle the
// URL and provides be_normalize, store the normalized URL.
func (ss *ServiceSystem) seturl(s *Service, url string) {
	if ss.backendHooks != nil && ss.backendHooks.NormalizeURL != nil {
		s.url = ss.backendHooks.NormalizeURL(url)
	} else {
		s.url = url
	}
}

// ServiceSetType sets the service type
func ServiceSetType(s *Service, typ string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.typ = typ
}

// ServiceSetTitle sets the service title
func ServiceSetTitle(s *Service, title string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.title = title
}

// ServiceSetIcon sets the service icon
func ServiceSetIcon(s *Service, icon string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.icon = icon
}

// ServiceSetURL sets the service URL
// C: service_set_url (service.c:461-470)
func (ss *ServiceSystem) ServiceSetURL(s *Service, url string) {
	if s == nil {
		return
	}
	// C: prop_set(s->s_root, "url", PROP_SET_RSTRING, url)
	urlProp := ss.propMgr.CreateEx(s.root, "url", nil, false, false)
	ss.propMgr.SetStringEx(urlProp, nil, url, propcore.StringUTF8)

	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.seturl(s, url)
	// C: service_reprobe(s)
	if s.doProbe {
		s.needProbe = true
		if ss.cond != nil {
			ss.cond.Signal()
		}
	}
}

// ServiceGetURL returns the service URL
func ServiceGetURL(s *Service) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func ServiceGetTitle(s *Service) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title
}

func ServiceGetIcon(s *Service) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.icon
}

func ServiceGetID(s *Service) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

// ServiceSetEnabled sets whether the service is enabled
func ServiceSetEnabled(s *Service, v bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = v
}

// ServiceSetStatus sets the service status
func ServiceSetStatus(s *Service, status int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

// ServiceGetStatusProp returns the status property
func ServiceGetStatusProp(s *Service) any {
	if s == nil {
		return nil
	}
	return s.propStatus
}

// ServiceGetStatusTxtProp returns the status text property
func ServiceGetStatusTxtProp(s *Service) any {
	if s == nil {
		return nil
	}
	return s.propStatusTxt
}

// OriginToString converts origin to string
func OriginToString(origin int) string {
	switch origin {
	case SvcOriginSystem:
		return "system"
	case SvcOriginBookmark:
		return "bookmark"
	case SvcOriginDiscovered:
		return "discovered"
	case SvcOriginApp:
		return "app"
	case SvcOriginMedia:
		return "media"
	default:
		return "unknown"
	}
}

// StatusToString converts status to string
func StatusToString(status int) string {
	switch status {
	case SvcStatusOK:
		return "ok"
	case SvcStatusAuthNeeded:
		return "auth"
	case SvcStatusNoHandler:
		return "nohandler"
	case SvcStatusFail:
		return "fail"
	case SvcStatusScanning:
		return "scanning"
	default:
		return "unknown"
	}
}

// GetAllServices returns all services
func (ss *ServiceSystem) GetAllServices() []*Service {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()

	result := make([]*Service, len(ss.services))
	copy(result, ss.services)
	return result
}

// Root returns the service's root prop.
// C: service_t.s_root (service.h) — the prop node holding url/title/
// enabled/type children written by SETTING_WRITE_PROP.
func (s *Service) Root() *propcore.Prop { return s.root }

// GetServiceByID returns a service by ID
func (ss *ServiceSystem) GetServiceByID(id string) *Service {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()

	for _, s := range ss.services {
		if s.id == id {
			return s
		}
	}
	return nil
}

// GetEnabledServices returns all enabled services
func (ss *ServiceSystem) GetEnabledServices() []*Service {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()

	result := make([]*Service, 0)
	for _, s := range ss.services {
		if s.enabled {
			result = append(result, s)
		}
	}
	return result
}

// GetEnabledServicesProp returns the enabled services property node
func (ss *ServiceSystem) GetEnabledServicesProp() *propcore.Prop {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	ss.ts.Debug("SERVICE", "GetEnabledServicesProp() called, enabledServices: %v", ss.enabledServices != nil)
	return ss.enabledServices
}

// GetDiscoveredNodesProp returns the discovered nodes property ($global.services.discovered).
// This is linked to page.model.nodes when the discovered: backend opens a page,
// matching C's discovered_open_url (src/service.c:557).
func (ss *ServiceSystem) GetDiscoveredNodesProp() *propcore.Prop {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()
	return ss.discoveredNodes
}

// GetServicesByOrigin returns services by origin
func (ss *ServiceSystem) GetServicesByOrigin(origin int) []*Service {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()

	result := make([]*Service, 0)
	for _, s := range ss.services {
		if s.origin == origin {
			result = append(result, s)
		}
	}
	return result
}

// CleanupID cleans up an ID string
func CleanupID(id string) string {
	id = strings.ReplaceAll(id, "/", "")
	id = strings.ReplaceAll(id, ":", "")
	id = strings.ReplaceAll(id, ".", "")
	return id
}

// StartProbeLoop starts the service probe loop
func (ss *ServiceSystem) StartProbeLoop() {
	go ss.probeLoop()
}

// probeLoop is the service probe loop
func (ss *ServiceSystem) probeLoop() {
	ss.mutex.Lock()
	defer ss.mutex.Unlock()

	for {
		// Find a service that needs probing
		var s *Service
		for _, svc := range ss.services {
			if svc.needProbe {
				s = svc
				break
			}
		}

		if s == nil {
			ss.cond.Wait()
			continue
		}

		s.needProbe = false
		s.ref++

		// C: prop_set_string(s->s_prop_status,
		//     val2str(SVC_STATUS_SCANNING, status_tab))
		ss.propMgr.SetStringEx(s.propStatus, nil, "scanning",
			propcore.StringUTF8)

		var st int
		var txt string // C: char txt[256] — errbuf filled by backend_probe

		if s.url == "" {
			st = SvcStatusFail
		} else {
			url := s.url
			// backend_probe() can take a lot of time so we unlock
			ss.mutex.Unlock()
			if ss.backendHooks != nil && ss.backendHooks.Probe != nil {
				var perr error
				st, perr = ss.backendHooks.Probe(url, 0)
				if perr != nil {
					txt = perr.Error()
				}
			} else {
				st = SvcStatusNoHandler
			}
			ss.mutex.Lock()
		}

		s.status = st

		if !s.zombie {
			// C: prop_set_string(s->s_prop_status, val2str(st, ...))
			ss.propMgr.SetStringEx(s.propStatus, nil,
				StatusToString(st), propcore.StringUTF8)
			if st != SvcStatusOK {
				// C: prop_set_string(s->s_prop_status_txt, txt)
				ss.propMgr.SetStringEx(s.propStatusTxt, nil,
					txt, propcore.StringUTF8)
			} else {
				// C: prop_set_void(s->s_prop_status_txt)
				ss.propMgr.SetVoidEx(s.propStatusTxt, nil)
			}
		}

		s.ref--
		if s.ref == 0 && s.zombie {
			// C: free(s) — Go GC handles it
		}
	}
}
