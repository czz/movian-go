// Package upnp is a 1:1 Go port of src/upnp/*.c and src/networking/ssdp.c.
//
// This file ports upnp.h (shared types) + upnp.c (device lifecycle,
// description, introspection, backend registration).
package upnp

import (
	"crypto/rand"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/czz/movian-go/internal/db/kvstore"

	"github.com/czz/movian-go/internal/app"
	backend "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/backend/playlist/playqueue"
	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/metadata"
	misc "github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/networking/ifaddr"
	"github.com/czz/movian-go/internal/networking/ssdp"
	propcore "github.com/czz/movian-go/internal/prop"
	service "github.com/czz/movian-go/internal/service"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
	"github.com/czz/movian-go/internal/version"
)

// UPNPServiceType — C: upnp_service_type_t (upnp.h:42-46)
type UPNPServiceType int

const (
	UPNPServiceUnknown           UPNPServiceType = -1 // C: UPNP_SERVICE_UNKNOWN
	UPNPServiceContentDirectory1 UPNPServiceType = 0  // C: UPNP_SERVICE_CONTENT_DIRECTORY_1
	UPNPServiceContentDirectory2 UPNPServiceType = 1  // C: UPNP_SERVICE_CONTENT_DIRECTORY_2
)

// UPNPService represents a remote service.
// C: upnp_service_t (upnp.h:52-72)
type UPNPService struct {
	device *UPNPDevice // C: us_device

	id string // C: us_id

	typ UPNPServiceType // C: us_type (upnp_service_type_t)

	eventURL   string // C: us_event_url
	controlURL string // C: us_control_url
	localURL   string // C: us_local_url
	iconURL    string // C: us_icon_url

	settings       *propcore.Prop    // C: us_settings
	settingEnabled *settings.Setting // C: us_setting_enabled
	settingTitle   *settings.Setting // C: us_setting_title
	settingType    *settings.Setting // C: us_setting_type

	service *service.Service // C: us_service
}

// UPNPDevice represents a remote device.
// C: upnp_device_t (upnp.h:80-94)
type UPNPDevice struct {
	sys              *System // back-pointer — C reaches subsystem globals directly
	url              string  // C: ud_url
	uuid             string  // C: ud_uuid
	friendlyName     string  // C: ud_friendlyName
	manufacturer     string  // C: ud_manufacturer
	modelDescription string  // C: ud_modelDescription
	modelNumber      string  // C: ud_modelNumber
	icon             string  // C: ud_icon

	services []*UPNPService // C: ud_services (LIST_HEAD)

	interesting bool // C: ud_interesting
}

// UPNPServiceMethod is a method exposed by a local service.
// C: upnp_service_method_t (upnp.h:100-105)
type UPNPServiceMethod struct {
	name string // C: usm_name
	fn   func(hc *httpnet.HTTPConnection, in *htsmsg.HTSMsg,
		myhost string, myport int) *htsmsg.HTSMsg // C: usm_fn
}

// UPNPSubscription is an event subscription.
// C: upnp_subscription_t (upnp.h:111-122)
type UPNPSubscription struct {
	callback string            // C: us_callback
	uuid     string            // C: us_uuid
	seq      int               // C: us_seq
	expire   int64             // C: us_expire (time_t, -1 = infinite)
	service  *UPNPLocalService // C: us_service

	myport int    // C: us_myport
	myhost string // C: us_myhost
}

// UPNPLocalService is a locally-advertised UPnP service.
// C: upnp_local_service_t (upnp.h:128-136)
type UPNPLocalService struct {
	sys           *System                                                               // back-pointer — C reaches globals directly
	name          string                                                                // C: uls_name
	version       int                                                                   // C: uls_version
	subscriptions []*UPNPSubscription                                                   // C: uls_subscriptions
	generateProps func(svc *UPNPLocalService, myhost string, myport int) *htsmsg.HTSMsg // C: uls_generate_props
	notifyTimer   *callout.Callout                                                      // C: uls_notifytimer (callout)
	methods       []UPNPServiceMethod                                                   // C: uls_methods[]
}

// System holds the UPnP subsystem state.
// C: upnp_lock, upnp_device_cond, upnp_devices, upnp_uuid globals
// (upnp.c:40-44), plus the subsystem references upnp_init wires up —
// in C those are globals read directly; here they are injected.
type System struct {
	usage      *usage.Reporter // C: usage_page_open global (usage.c)
	mu         sync.Mutex      // C: upnp_lock
	deviceCond *sync.Cond      // C: upnp_device_cond
	devices    []*UPNPDevice   // C: upnp_devices (LIST_HEAD)
	uuid       string          // C: upnp_uuid

	// gconf — C: gconf_t fields (system_name, enable_upnp_debug).
	gconf *gconf.T

	// AVTransport published state — C: static upnp_current_*
	// (upnp_avtransport.c:31-50). Bound to prop callbacks by pointer.
	currentURL             string
	currentTitle           string
	currentAlbum           string
	currentAlbumArt        string
	currentArtist          string
	currentPlaystatus      string
	currentPlaystatusSet   bool // C: NULL tracking for playstatus
	currentType            string
	currentTrackDuration   int
	currentTrackTime       int
	currentTotalTracks     int
	currentTrack           int
	currentShuffle         int
	currentRepeat          int
	currentCanSkipBackward int
	currentCanSkipForward  int
	currentCanSeek         int
	currentCanPause        int
	currentCanStop         int

	flushTimer *callout.Callout // C: static callout_t upnp_flush_timer

	// The three local services — C: static upnp_local_service_t objects
	// (upnp_connectionmanager.c, upnp_renderingcontrol.c, upnp_avtransport.c)
	cm  *UPNPLocalService
	rc  *UPNPLocalService
	avt *UPNPLocalService

	// Injected subsystem references — only those read at runtime
	// (C: file-scope globals). Start-only deps stay NewSystem params.
	serviceSystem   *service.ServiceSystem
	ts              *trace.TraceSystem // C: trace() global — injected
	settingsManager *settings.SettingsManager
	store           *htsmsg.Store    // C: global htsmsg_store — injected
	kvstore         *kvstore.KVStore // C: global kvstore_* — injected
	propManager     *propcore.PropManager
	eventManager    *event.EventManager
	playQueue       *playqueue.PlayQueue
	metadataMgr     *metadata.MetadataManager
	netIfMgr        *ifaddr.NetIfAddrManager
	calloutSystem   *callout.CalloutSystem

	// ssdp — C: the ssdp.c module (file-statics + ssdp_init/fini).
	// Owns discovery/advertisement; calls back into s.addDevice /
	// s.delDevice — C: extern upnp_add_device/upnp_del_device.
	ssdp *ssdp.Server
}

// gcfg.EnableUpnpDebug — C: gconf.enable_upnp_debug. Written by the "Debug
// UPNP" dev setting via SetEnableUpnpDebug (addDevBoolCallback — same
// pattern as trace.SetEnableMetadataDebug).

// upnpTrace — C: UPNP_TRACE (upnp.h:27-31)
// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (s *System) SetGconf(g *gconf.T) { s.gconf = g }

// gcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (s *System) gcfg() *gconf.T {
	if s.gconf == nil {
		s.gconf = gconf.New()
	}
	return s.gconf
}

func (s *System) upnpTrace(format string, args ...any) {
	if s.gcfg().EnableUpnpDebug.Load() {
		s.ts.Trace(trace.TRACE_DEBUG, "UPNP", format, args...)
	}
}

// devFind finds a device by URL.
// C: dev_find (upnp.c:50-59)
func (s *System) devFind(url string) *UPNPDevice {
	for _, ud := range s.devices {
		if url == ud.url {
			return ud
		}
	}
	return nil
}

// destroy destroys a service.
// C: us_destroy (upnp.c:66-82)
func (us *UPNPService) destroy() {
	ud := us.device
	s := us.device.sys
	// LIST_REMOVE(us, us_link) — remove from ud.services
	for i, svc := range ud.services {
		if svc == us {
			ud.services = slices.Delete(ud.services, i, i+1)
			break
		}
	}

	// setting_destroy + prop_destroy + service_destroy.
	// Fields are nil'ed after destroy: in C free(us) makes a second
	// call impossible without UB; here a double-destroy would re-enter
	// the subsystems on live handles — nil makes it a clean no-op.
	if us.settingEnabled != nil {
		s.settingsManager.Destroy(us.settingEnabled)
		us.settingEnabled = nil
	}
	if us.settingTitle != nil {
		s.settingsManager.Destroy(us.settingTitle)
		us.settingTitle = nil
	}
	if us.settingType != nil {
		s.settingsManager.Destroy(us.settingType)
		us.settingType = nil
	}
	if us.settings != nil {
		s.propManager.Destroy(us.settings)
		us.settings = nil
	}
	if us.service != nil {
		s.serviceSystem.ServiceDestroy(us.service)
		us.service = nil
	}
}

// destroy destroys a device and all its services.
// C: dev_destroy (upnp.c:88-102)
func (ud *UPNPDevice) destroy() {
	s := ud.sys
	for len(ud.services) > 0 {
		ud.services[0].destroy()
	}
	for i, d := range s.devices {
		if d == ud {
			s.devices = slices.Delete(s.devices, i, i+1)
			break
		}
	}
}

// describeService emits a <service> element.
// C: describe_service (upnp.c:109-120)
func describeService(out *strings.Builder, stype string, ver int) {
	fmt.Fprintf(out,
		"<service>"+
			"<serviceType>urn:schemas-upnp-org:service:%s:%d</serviceType>"+
			"<serviceId>urn:upnp-org:serviceId:%s</serviceId>"+
			"<SCPDURL>/upnp/%s/scpd.xml</SCPDURL>"+
			"<controlURL>/upnp/%s/control</controlURL>"+
			"<eventSubURL>/upnp/%s/subscribe</eventSubURL>"+
			"</service>",
		stype, ver, stype, stype, stype, stype)
}

// sendDevDescription serves the device description XML.
// C: send_dev_description (upnp.c:127-170)
func (s *System) sendDevDescription(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	var out strings.Builder

	fmt.Fprintf(&out,
		"<?xml version=\"1.0\" encoding=\"utf-8\"?>"+
			"<root xmlns=\"urn:schemas-upnp-org:device-1-0\">"+
			"<specVersion>"+
			"<major>1</major>"+
			"<minor>0</minor>"+
			"</specVersion>"+
			"<device>"+
			"<dlna:X_DLNADOC xmlns:dlna=\"urn:schemas-dlna-org:device-1-0\">DMR-1.50</dlna:X_DLNADOC>"+
			"<dlna:X_DLNADOC xmlns:dlna=\"urn:schemas-dlna-org:device-1-0\">M-DMR-1.50</dlna:X_DLNADOC>"+
			"<deviceType>urn:schemas-upnp-org:device:MediaRenderer:2</deviceType>"+
			"<friendlyName>%s</friendlyName>"+
			"<manufacturer>czz78</manufacturer>"+
			"<modelDescription>"+app.AppNameUser+" Media Center</modelDescription>"+
			"<modelName>"+app.AppNameUser+" Media Center</modelName>"+
			"<modelNumber>%s</modelNumber>"+
			"<manufacturerURL>https://movian-go.czz78.com/</manufacturerURL>"+
			"<modelURL>https://movian-go.czz78.com/</modelURL>"+
			"<UDN>uuid:%s</UDN>"+
			"<UPC/>"+
			"<presentationURL>/</presentationURL>"+
			"<serviceList>",
		s.gcfg().SystemName,
		version.AppVersion(),
		s.uuid)

	describeService(&out, "ConnectionManager", 2)
	describeService(&out, "RenderingControl", 2)
	describeService(&out, "AVTransport", 2)

	out.WriteString("</serviceList></device></root>")

	return hc.HTTPSendReply(0, "text/xml", "", "", 0, []byte(out.String()))
}

// sendScpd serves an SCPD document.
// C: send_avt_scpd / send_rc_scpd / send_cm_scpd (upnp.c:177-219)
func sendScpd(scpd string) httpnet.HTTPCallback {
	return func(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
		return hc.HTTPSendReply(0, "text/xml", "", "", 0, []byte(scpd))
	}
}

// PQPaused — C: PQ_PAUSED (playqueue.h:23)
const PQPaused = 0x1

// NewSystem allocates and initializes the UPnP subsystem: loads/generates
// the device UUID, registers HTTP endpoints (description, SCPDs, control,
// subscribe), starts SSDP, and initializes the event subsystem.
// C: upnp_init (upnp.c:226-287) — the allocation itself mirrors C's
// statically-allocated globals.
func NewSystem(httpServerPort int, srv *httpnet.HTTPServer,
	ss *service.ServiceSystem, sm *settings.SettingsManager,
	pm *propcore.PropManager, bs *backend.BackendSystem,
	em *event.EventManager, pq *playqueue.PlayQueue,
	mm *metadata.MetadataManager,
	nim *ifaddr.NetIfAddrManager,
	cs *callout.CalloutSystem,
	store *htsmsg.Store, kvs *kvstore.KVStore,
	ts *trace.TraceSystem) *System {

	s := &System{ts: ts}
	// C: hts_cond_init(&upnp_device_cond, &upnp_lock)
	s.deviceCond = sync.NewCond(&s.mu)
	s.flushTimer = &callout.Callout{}

	s.serviceSystem = ss
	s.settingsManager = sm
	s.propManager = pm
	s.eventManager = em
	s.playQueue = pq
	s.metadataMgr = mm
	s.netIfMgr = nim
	s.calloutSystem = cs
	s.store = store
	s.kvstore = kvs

	var conf *htsmsg.HTSMsg
	if store != nil {
		conf, _ = store.Load("upnp")
	}

	var u string
	if conf != nil {
		u = conf.GetStr("uuid")
	}
	if u != "" {
		s.uuid = u
	} else {
		var d [20]byte
		var uuid string

		if conf == nil {
			conf = htsmsg.NewMap()
		}

		rand.Read(d[:]) // C: arch_get_random_bytes(d, sizeof(d))

		uuid = fmt.Sprintf(
			"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-"+
				"%02x%02x%02x%02x%02x%02x",
			d[0x0], d[0x1], d[0x2], d[0x3],
			d[0x4], d[0x5], d[0x6], d[0x7],
			d[0x8], d[0x9], d[0xa], d[0xb],
			d[0xc], d[0xd], d[0xe], d[0xf])

		s.uuid = uuid
		conf.AddStr("uuid", uuid)
		if store != nil {
			store.Save(conf, "upnp")
		}
	}
	if conf != nil {
		conf.Release()
	}

	// The three local services — C: static upnp_local_service_t literals.
	// methods tables are bound here (not in literals): method values need
	// the allocated service, and handler bodies reference it (init cycle).
	s.cm = &UPNPLocalService{
		sys:           s,
		name:          "ConnectionManager",
		version:       2,
		generateProps: cmGenerateProps,
	}
	s.cm.methods = []UPNPServiceMethod{
		{"GetProtocolInfo", s.cm.cmGetProtocolInfo},
		{"GetCurrentConnectionInfo", s.cm.cmGetCurrentConnectionInfo},
		{"GetCurrentConnectionIDs", s.cm.cmGetCurrentConnectionIDs},
	}
	s.rc = &UPNPLocalService{
		sys:           s,
		name:          "RenderingControl",
		version:       2,
		generateProps: rcGenerateProps,
	}
	s.rc.methods = []UPNPServiceMethod{
		{"SetMute", s.rc.rcSetMute},
		{"SetVolume", s.rc.rcSetVolume},
		{"SelectPreset", s.rc.rcSelectPreset},
		{"ListPresets", s.rc.rcListPresets},
	}
	s.avt = &UPNPLocalService{
		sys:           s,
		name:          "AVTransport",
		version:       2,
		generateProps: avtGenerateProps,
	}

	s.upnpAVTransportStart()

	srv.HTTPPathAdd("/upnp/description.xml", nil, s.sendDevDescription, true)
	srv.HTTPPathAdd("/upnp/AVTransport/scpd.xml", nil, sendScpd(avt_scpd), true)
	srv.HTTPPathAdd("/upnp/ConnectionManager/scpd.xml", nil, sendScpd(cm_scpd), true)
	srv.HTTPPathAdd("/upnp/RenderingControl/scpd.xml", nil, sendScpd(rc_scpd), true)

	srv.HTTPPathAdd("/upnp/ConnectionManager/control",
		s.cm, upnpControl, true)
	srv.HTTPPathAdd("/upnp/RenderingControl/control",
		s.rc, upnpControl, true)
	srv.HTTPPathAdd("/upnp/AVTransport/control",
		s.avt, upnpControl, true)

	srv.HTTPPathAdd("/upnp/ConnectionManager/subscribe",
		s.cm, upnpSubscribe, true)
	srv.HTTPPathAdd("/upnp/RenderingControl/subscribe",
		s.rc, upnpSubscribe, true)
	srv.HTTPPathAdd("/upnp/AVTransport/subscribe",
		s.avt, upnpSubscribe, true)

	s.ssdp = ssdp.NewServer(s.uuid, httpServerPort, s.netIfMgr,
		s.addDevice, s.delDevice, s.ts) // C: ssdp_init (ssdp.c:499-505)

	s.upnpEventStart()

	// C: BE_REGISTER(upnp) — backend be_upnp {be_init, be_canhandle, be_open}
	s.usage = bs.Usage()

	if bs != nil {
		bs.Register(&backend.Backend{
			Start:     s.beUpnpStart,
			CanHandle: s.beUpnpCanhandle,
			Open:      s.beUpnpBrowse,
		})
	}

	return s
}

// svcTypeTab maps service-type URNs to upnp_service_type_t.
// C: svctype_tab (upnp.c:293-296)
var svcTypeTab = map[string]UPNPServiceType{
	"ContentDirectory:1": UPNPServiceContentDirectory1,
	"ContentDirectory:2": UPNPServiceContentDirectory2,
}

// upnpSvcstrToType parses a serviceType URN into a service type.
// C: upnp_svcstr_to_type (upnp.c:301-309)
func upnpSvcstrToType(str string) UPNPServiceType {
	const prefix = "urn:schemas-upnp-org:service:"
	if !strings.HasPrefix(str, prefix) {
		return UPNPServiceUnknown
	}
	str = str[len(prefix):]
	if v, ok := svcTypeTab[str]; ok {
		return v
	}
	return UPNPServiceUnknown
}

// findService finds a service by id on a device.
// C: service_find (upnp.c:316-324)
func (ud *UPNPDevice) findService(id string) *UPNPService {
	for _, us := range ud.services {
		if us.id == id {
			return us
		}
	}
	return nil
}

// serviceGuess finds a service whose control URL host:port matches url.
// C: upnp_service_guess (upnp.c:330-365)
// Caller must hold s.mu — the C version likewise requires upnp_lock
// to be held (its only caller, upnp_avtransport.c:197, locks first);
// taking the lock inside would self-deadlock the existing caller.
func (s *System) serviceGuess(url string) *UPNPService {
	var proto1, hostname1 [16]byte
	var port1 int

	misc.UrlSplit(proto1[:], 16, nil, 0,
		hostname1[:], 16, &port1, nil, 0, url)

	proto1s := misc.CStr(proto1[:])
	if port1 == -1 && strings.EqualFold(proto1s, "http") {
		port1 = 80
	}

	for _, ud := range s.devices {
		for _, us := range ud.services {
			var proto2, hostname2 [16]byte
			var port2 int

			misc.UrlSplit(proto2[:], 16, nil, 0,
				hostname2[:], 16, &port2, nil, 0, us.controlURL)

			if port2 == -1 && strings.EqualFold(misc.CStr(proto2[:]), "http") {
				port2 = 80
			}

			if proto1s == misc.CStr(proto2[:]) &&
				misc.CStr(hostname1[:]) == misc.CStr(hostname2[:]) &&
				port1 == port2 {
				return us
			}
		}
	}
	return nil
}

// removeBadChars replaces ':' and '/' with '_'.
// C: remove_bad_chars (upnp.c:371-378)
func removeBadChars(s string) string {
	r := []byte(s)
	for i := range r {
		if r[i] == ':' || r[i] == '/' {
			r[i] = '_'
		}
	}
	return string(r)
}

// addContentDirectory wires a ContentDirectory service into the settings
// tree + service registry. C: add_content_directory (upnp.c:385-435)
func (us *UPNPService) addContentDirectory(hostname string, port int) {
	s := us.device.sys
	ud := us.device

	svcid := fmt.Sprintf("upnp/upnp:%s:%s:0", ud.uuid, us.id)
	us.localURL = svcid[5:]
	svcid = removeBadChars(svcid)

	title := ud.friendlyName
	if title == "" {
		title = "UPnP content directory"
	}

	modelNumber := ud.modelNumber
	if modelNumber == "" {
		modelNumber = "Unknown version"
	}
	buf := fmt.Sprintf("%s (%s) on %s:%d", title, modelNumber, hostname, port)
	us.settings = s.settingsManager.AddDirCStr(s.settingsManager.SD(), title, "",
		us.iconURL, buf, "")

	us.service = s.serviceSystem.ServiceCreate(svcid, "", us.localURL, "",
		us.iconURL, true, false, service.SvcOriginDiscovered)

	root := us.service.Root()

	us.settingEnabled =
		s.settingsManager.SettingCreate(settings.SettingBool, us.settings, settings.SettingsInitialUpdate,
			settings.SettingTagTitle, s.settingsManager.P("Enabled on home screen"),
			settings.SettingTagValue, 0,
			settings.SettingTagWriteProp, s.propManager.CreateEx(root, "enabled", nil, false, false),
			settings.SettingTagStore, svcid, "enabled",
		)

	us.settingTitle =
		s.settingsManager.SettingCreate(settings.SettingString, us.settings,
			settings.SettingsInitialUpdate,
			settings.SettingTagTitle, s.settingsManager.P("Name"),
			settings.SettingTagValue, title,
			settings.SettingTagWriteProp, s.propManager.CreateEx(root, "title", nil, false, false),
			settings.SettingTagStore, svcid, "title",
		)

	contents := "server"

	us.settingType =
		s.settingsManager.SettingCreate(settings.SettingString, us.settings, settings.SettingsInitialUpdate,
			settings.SettingTagTitle, s.settingsManager.P("Type"),
			settings.SettingTagValue, contents,
			settings.SettingTagWriteProp, s.propManager.CreateEx(root, "type", nil, false, false),
			settings.SettingTagStore, svcid, "type",
		)
}

// introspectService parses a <service> element into a UPNPService.
// C: introspect_service (upnp.c:442-491)
func (ud *UPNPDevice) introspectService(svc *htsmsg.HTSMsg) {
	id := svc.GetStr("serviceId")
	typestr := svc.GetStr("serviceType")
	eURL := svc.GetStr("eventSubURL")
	cURL := svc.GetStr("controlURL")

	if id == "" || typestr == "" || eURL == "" || cURL == "" {
		return
	}
	stype := upnpSvcstrToType(typestr)
	if stype == UPNPServiceUnknown {
		return
	}

	// findService returns non-nil only for duplicate serviceId entries
	// in a single description XML (introspect runs once per device via
	// ud.interesting). C re-registers in that case too — quirk kept 1:1.
	us := ud.findService(id)
	if us == nil {
		us = &UPNPService{}
		us.device = ud
		us.id = id
		ud.services = append(ud.services, us)
	}
	us.typ = stype

	var proto [16]byte
	var hostname [128]byte
	var path [256]byte
	var port int

	misc.UrlSplit(proto[:], 16, nil, 0, hostname[:], 128, &port,
		path[:], 256, ud.url)

	us.eventURL = misc.UrlResolveRelative(misc.CStr(proto[:]), misc.CStr(hostname[:]),
		port, misc.CStr(path[:]), eURL)

	us.controlURL = misc.UrlResolveRelative(misc.CStr(proto[:]), misc.CStr(hostname[:]),
		port, misc.CStr(path[:]), cURL)

	if ud.icon != "" {
		us.iconURL = misc.UrlResolveRelative(misc.CStr(proto[:]), misc.CStr(hostname[:]),
			port, misc.CStr(path[:]), ud.icon)
	} else {
		us.iconURL = ""
	}

	switch us.typ {
	case UPNPServiceContentDirectory1, UPNPServiceContentDirectory2:
		us.addContentDirectory(misc.CStr(hostname[:]), port)
	default:
	}
}

// deviceGetIcon picks the best icon from the device iconList.
// C: device_get_icon (upnp.c:498-542)
func deviceGetIcon(dev *htsmsg.HTSMsg) string {
	iconlist := dev.GetMap("iconList")
	if iconlist == nil {
		return ""
	}

	best := ""
	bestscore := 0

	for _, f := range iconlist.GetFields() {
		if f.GetName() != "icon" {
			continue
		}
		icon := f.GetMap()
		if icon == nil {
			continue
		}

		mimetype := icon.GetStr("mimetype")
		url := icon.GetStr("url")

		if mimetype == "" || url == "" {
			continue
		}

		var score int
		if mimetype == "image/png" {
			score = 2
		} else if mimetype == "image/jpeg" {
			score = 1
		} else {
			continue
		}

		width, _ := strconv.Atoi(orZero(icon.GetStr("width")))
		height, _ := strconv.Atoi(orZero(icon.GetStr("height")))
		score += width * height

		if score > bestscore {
			best = url
			bestscore = score
		}
	}
	return best
}

// orZero returns s, or "0" if empty (C: htsmsg_get_str(...) ?: "0").
func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// introspect fetches the device description XML and populates ud.
// C: introspect_device (upnp.c:550-615)
func (ud *UPNPDevice) introspect() {
	client := httpnet.NewHTTPClient()
	resp, err := client.Get(ud.url)
	if err != nil {
		ud.sys.ts.Trace(trace.TRACE_INFO, "UPNP",
			"Unable to introspect %s -- %v", ud.url, err)
		return
	}
	if resp == nil || len(resp.Body) == 0 {
		ud.sys.ts.Trace(trace.TRACE_INFO, "UPNP",
			"Unable to introspect %s -- empty body", ud.url)
		return
	}

	m, err := htsmsg.DeserializeXMLBuf(resp.Body)
	if m == nil || err != nil {
		ud.sys.ts.Trace(trace.TRACE_INFO, "UPNP",
			"Unable to introspect %s XML -- %v", ud.url, err)
		return
	}

	dev := m.GetMapMulti("root", "device")
	if dev == nil {
		m.Release()
		return
	}

	uuid := dev.GetStr("UDN")
	if uuid == "" {
		m.Release()
		return
	}

	ud.uuid = uuid

	if v := dev.GetStr("friendlyName"); v != "" {
		ud.friendlyName = v
	}
	if v := dev.GetStr("manufacturer"); v != "" {
		ud.manufacturer = v
	}
	if v := dev.GetStr("modelDescription"); v != "" {
		ud.modelDescription = v
	}
	if v := dev.GetStr("modelNumber"); v != "" {
		ud.modelNumber = v
	}

	if v := deviceGetIcon(dev); v != "" {
		ud.icon = v
	}

	svclist := dev.GetMap("serviceList")
	if svclist == nil {
		ud.sys.ts.Trace(trace.TRACE_INFO, "UPNP",
			"Unable to introspect %s -- No services", ud.url)
	} else {
		for _, f := range svclist.GetFields() {
			if f.GetName() != "service" {
				continue
			}
			svc := f.GetMap()
			if svc != nil {
				ud.introspectService(svc)
			}
		}
	}
	m.Release()
}

// addDevice registers a discovered device URL.
// C: upnp_add_device (upnp.c:622-644)
func (s *System) addDevice(url, typ string, maxage int) {
	s.mu.Lock()

	ud := s.devFind(url)
	if ud == nil {
		ud = &UPNPDevice{sys: s}
		ud.url = url
		s.devices = append(s.devices, ud)
	}

	if typ == "urn:schemas-upnp-org:service:ContentDirectory:1" ||
		typ == "urn:schemas-upnp-org:service:ContentDirectory:2" {
		if !ud.interesting {
			ud.interesting = true
			// C: introspect_device runs a blocking HTTP fetch while
			// upnp_lock is held — the lock is ud's lifetime guard
			// against a concurrent upnp_del_device/dev_destroy.
			// Do NOT move outside the lock without refcounting ud.
			ud.introspect()
		}
	}
	s.deviceCond.Broadcast()

	s.mu.Unlock()
}

// delDevice removes a device by URL.
// C: upnp_del_device (upnp.c:651-659)
func (s *System) delDevice(url string) {
	s.mu.Lock()
	if ud := s.devFind(url); ud != nil {
		ud.destroy()
	}
	s.mu.Unlock()
}

// beUpnpCanhandle returns 1 for "upnp:" URLs.
// C: be_upnp_canhandle (upnp.c:666-671)
func (s *System) beUpnpCanhandle(url string) int {
	if strings.HasPrefix(url, "upnp:") {
		return 1
	}
	return 0
}

// SsdpShutdown is the exported shutdown hook (C: INITME fini ssdp_shutdown).
func (s *System) SsdpShutdown() { s.ssdp.Shutdown() }

// beUpnpStart initializes the backend (mu/deviceCond already set up by
// NewSystem — C does hts_mutex_init/hts_cond_init here).
// C: be_upnp_init (upnp.c:678-683)
func (s *System) beUpnpStart() error {
	return nil
}
