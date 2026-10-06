// Canonical 1:1 port of src/ecmascript/es_service.c — service resources,
// create/enable, delete-req uninstall.
package ecmascript

import (
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
)

// esEnv.serviceSystem — C: service_create() uses the global service system.

// EsSetServiceSystem wires the service system.
func EsSetServiceSystem(ss *service.ServiceSystem) { esEnv.serviceSystem = ss }

// esEnv.pluginUninstall — C: plugin_uninstall() under ENABLE_PLUGINS.
// Wired by the plugin layer at init; nil = plugin support not linked.

// EsSetPluginUninstall wires the plugin uninstall hook.
func EsSetPluginUninstall(f func(id string)) { esEnv.pluginUninstall = f }

// EsSetPluginSelectView wires plugin_select_view (C: plugins.c:1975 —
// called from es_misc.c:303). Provider: ctx.pluginManager.
func EsSetPluginSelectView(fn func(pluginID, filename string)) {
	esEnv.pluginSelectView = fn
}

// ---------------------------------------------------------------------------
// es_service_t — C: es_service.c:27-35
// ---------------------------------------------------------------------------

// C: es_service_t
type esService struct {
	super *ESResource

	s         *service.Service
	title     string
	url       string
	enabled   bool
	deleteSub *propcore.Subscription
}

// esServiceDestroy — C: es_service_destroy (es_service.c:41-55)
func esServiceDestroy(er *ESResource) {
	es := er.Data.(*esService)
	if es.s == nil {
		return
	}

	esEnv.propPM.Unsubscribe(es.deleteSub)
	esEnv.serviceSystem.ServiceDestroy(es.s)
	es.s = nil
	EsResourceUnlink(er)
}

// esServiceInfo — C: es_service_info (es_service.c:61-67)
func esServiceInfo(eres *ESResource) string {
	es := eres.Data.(*esService)
	en := "No"
	if es.enabled {
		en = "Yes"
	}
	return "'" + es.title + "' => '" + es.url + "' (enabled:" + en + ")"
}

// C: es_resource_service (es_service.c:74-79)
var esResourceService = &ESResourceClass{
	ErcName:    "service",
	ErcDestroy: esServiceDestroy,
	ErcInfo:    esServiceInfo,
}

// uninstallPlugin — C: uninstall_plugin (es_service.c:84-90)
func uninstallPlugin(aux any) {
	if esEnv.pluginUninstall != nil {
		esEnv.pluginUninstall(aux.(string))
	}
}

// esServiceDeleteReq — C: es_service_delete_req (es_service.c:96-103)
func esServiceDeleteReq(opaque any, event propcore.EventType) {
	es := opaque.(*esService)

	if event == propcore.EventReqDelete {
		esEnv.tasks.Run(uninstallPlugin, es.super.erCtx.ecID)
	}
}

// esServiceCreate — C: es_service_create (es_service.c:109-147)
func esServiceCreate(ctx *gaftape.Context) int {
	ec := EsGet(ctx)

	es := &esService{}
	es.super = EsResourceCreate(ec, esResourceService, 1, es).(*ESResource)

	svcid := ctx.SafeToString(0)
	title := ctx.SafeToString(1)
	url := ctx.SafeToString(2)
	typ := ctx.SafeToString(3)
	enabled := ctx.ToBoolean(4)
	var icon string
	if ctx.IsString(5) {
		icon = ctx.ToString(5)
	}

	es.title = title
	es.url = url

	es.s = esEnv.serviceSystem.ServiceCreate(svcid,
		title, url, typ, icon, false, enabled,
		service.SvcOriginApp)
	es.enabled = enabled

	if ec.ecFlags&ECMASCRIPT_PLUGIN != 0 {
		// C: prop_set(es->s->s_root, "deleteText", PROP_SET_LINK,
		//   _p("Uninstall")) — links the child to the nls prop.
		dt := esEnv.propPM.CreateEx(es.s.Root(), "deleteText", nil, false, false)
		esEnv.propPM.Link(nls.GetProp("Uninstall"), dt, nil, false, false)

		// C: prop_subscribe(0, PROP_TAG_ROOT es->s->s_root,
		//   PROP_TAG_LOCKMGR ecmascript_context_lockmgr,
		//   PROP_TAG_MUTEX ec, PROP_TAG_CALLBACK es_service_delete_req es,
		//   NULL) — the lockmgr applies ec_mutex around the callback.
		es.deleteSub = es.s.Root().Subscribe(
			func(opaque any, et propcore.EventType,
				args ...any) {
				ec.ecMutex.Lock()
				defer ec.ecMutex.Unlock()
				esServiceDeleteReq(opaque, et)
			}, es)
	}

	EsResourcePush(ctx, es.super)
	return 1
}

// esServiceEnable — C: es_service_enable (es_service.c:154-172)
func esServiceEnable(ctx *gaftape.Context) int {
	r := EsResourceGet(ctx, 0, esResourceService)
	es := r.(*ESResource).Data.(*esService)

	if es.s == nil {
		return 0
	}

	if ctx.IsBoolean(1) {
		es.enabled = ctx.RequireBoolean(1)
		service.ServiceSetEnabled(es.s, es.enabled)
		return 0
	}

	ctx.PushBoolean(es.enabled)
	return 1
}

// ---------------------------------------------------------------------------
// fnlist_service — C: es_service.c:176-180
// ---------------------------------------------------------------------------

var esFnlistService = []gaftape.FunctionListEntry{
	{Key: "create", Value: esServiceCreate, Nargs: 6},
	{Key: "enable", Value: esServiceEnable, Nargs: 2},
}

// C: ES_MODULE("service", fnlist_service)
func registerEsService() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "service",
		Functions: esFnlistService,
	})
}
