// Canonical 1:1 port of src/ecmascript/es_route.c — route resources,
// route matching, ecmascript_openuri, backendOpen.
package ecmascript

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/gaftape"
	misc "github.com/czz/movian-go/internal/misc"
	navcore "github.com/czz/movian-go/internal/navigator"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// es_route_t — C: es_route.c:30-36
// ---------------------------------------------------------------------------

// C: es_route_t
type esRoute struct {
	super     *ESResource
	erPattern string
	erRegex   misc.HtsRegex
	erPrio    int
}

// Wired by cmd/movian-go init; nil-safe methods make unwired calls no-ops.

// esRouteDestroy — C: es_route_destroy (es_route.c:47-62)
func esRouteDestroy(eres *ESResource) {
	er := eres.Data.(*esRoute)

	esDebug(eres.erCtx, "Route %s removed", er.erPattern)

	EsRootUnregister(eres.erCtx.ecGaf, eres)

	sys := eres.erCtx.sys
	sys.routeMu.Lock()
	for i, x := range sys.routeList {
		if x == er {
			sys.routeList = slices.Delete(sys.routeList, i, i+1)
			break
		}
	}
	sys.routeMu.Unlock()

	misc.HtsRegfree(&er.erRegex)

	EsResourceUnlink(er.super)
}

// esRouteInfo — C: es_route_info (es_route.c:68-72)
func esRouteInfo(eres *ESResource) string {
	er := eres.Data.(*esRoute)
	return er.erPattern + " (prio:" + strconv.Itoa(er.erPrio) + ")"
}

// C: es_resource_route (es_route.c:78-83)
var esResourceRoute = &ESResourceClass{
	ErcName:    "route",
	ErcDestroy: esRouteDestroy,
	ErcInfo:    esRouteInfo,
}

// erCmp — C: er_cmp (es_route.c:89-93) — descending prio.
func erCmp(a, b *esRoute) int {
	return b.erPrio - a.erPrio
}

// esRouteCreate — C: es_route_create (es_route.c:100-150)
func esRouteCreate(ctx *gaftape.Context) int {
	str := ctx.SafeToString(0)

	if !strings.HasPrefix(str, "^") {
		str = "^" + str
	}

	ec := EsGet(ctx)

	sys := ec.sys
	sys.routeMu.Lock()

	var er *esRoute
	for _, x := range sys.routeList {
		if x.erPattern == str {
			er = x
			break
		}
	}

	if er != nil {
		sys.routeMu.Unlock()
		ctx.Error(gaftape.GAF_ERR_ERROR, "Route %s already exist", str)
	}

	er = &esRoute{}
	er.super = EsResourceAlloc(esResourceRoute, er)
	var errmsg string
	if misc.HtsRegcomp(&er.erRegex, str, &errmsg) != 0 {
		sys.routeMu.Unlock()
		ctx.Error(gaftape.GAF_ERR_ERROR,
			"Invalid regular expression for route %s -- %s", str, errmsg)
	}

	er.erPattern = str

	esDebug(ec, "Route %s added", er.erPattern)

	// C: strcspn(str, "()[]*?+$") ?: INT32_MAX
	i := strings.IndexAny(str, "()[]*?+$")
	if i == -1 {
		i = len(str)
	}
	if i == 0 {
		er.erPrio = math.MaxInt32
	} else {
		er.erPrio = i
	}

	// LIST_INSERT_SORTED — descending prio
	inserted := false
	for i, x := range sys.routeList {
		if erCmp(er, x) < 0 {
			sys.routeList = slices.Insert(sys.routeList, i, er)
			inserted = true
			break
		}
	}
	if !inserted {
		sys.routeList = append(sys.routeList, er)
	}

	EsResourceLink(er.super, ec, 1)

	sys.routeMu.Unlock()

	EsRootRegister(ctx, 1, er)

	EsResourcePush(ctx, er.super)
	return 1
}

// esRouteTest — C: es_route_test (es_route.c:156-177)
func esRouteTest(ctx *gaftape.Context) int {
	str := ctx.SafeToString(0)

	sys := EsGet(ctx).sys
	sys.routeMu.Lock()

	var er *esRoute
	var matches [8]misc.HtsRegmatch

	for _, x := range sys.routeList {
		if misc.HtsRegexec(&x.erRegex, str, 8, matches[:]) == 0 {
			er = x
			break
		}
	}

	ctx.PushBoolean(er != nil)

	sys.routeMu.Unlock()

	return 1
}

// EcmascriptOpenuri — C: ecmascript_openuri (es_route.c:183-244)
func EcmascriptOpenuri(page *propcore.Prop, url string, sync int) int {
	var matches [8]misc.HtsRegmatch

	sys := esEnv.sys
	sys.routeMu.Lock()

	var er *esRoute
	for _, x := range sys.routeList {
		if misc.HtsRegexec(&x.erRegex, url, 8, matches[:]) == 0 {
			er = x
			break
		}
	}

	if er == nil {
		sys.routeMu.Unlock()
		return 1
	}

	esResourceRetain(er.super)

	ec := er.super.erCtx

	sys.routeMu.Unlock()

	ctx := EsContextBegin(ec)

	if ctx == nil {
		EsContextEnd(ec, 1, ctx)
		EsResourceRelease(er.super)
		return 1
	}

	EsPushRoot(ctx, er)

	esStpropPush(ctx, page)

	ctx.PushBoolean(sync != 0)

	arrayIdx := ctx.PushArray()

	esDebug(ec, "Opening route %s", er.erPattern)
	// C: usage_page_open(sync, ec->ec_id)
	ec.usage.PageOpen(sync != 0, ec.ecID)

	for i := 1; i < 8; i++ {
		if matches[i].RmSo == -1 {
			break
		}

		esDebug(ec, "  Page argument %d : %.*s", i,
			matches[i].RmEo-matches[i].RmSo, url[matches[i].RmSo:])

		ctx.PushLstring(url[matches[i].RmSo:], matches[i].RmEo-matches[i].RmSo)
		ctx.PutPropIndex(arrayIdx, i-1)
	}

	rc := ctx.PCall(3)
	if rc != 0 {
		if ctx.IsString(-1) {
			navcore.OpenError(esEnv.propPM, page, ctx.ToString(-1))
		} else {
			ctx.GetPropString(-1, "message")
			navcore.OpenError(esEnv.propPM, page, ctx.ToString(-1))
			ctx.Pop()
		}
		EsDumpErr(ctx)
	}
	ctx.Pop()

	EsContextEnd(ec, 1, ctx)
	EsResourceRelease(er.super)

	return 0
}

// esBackendOpen — C: es_backend_open (es_route.c:250-259)
func esBackendOpen(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	url := ctx.RequireString(1)
	syncv := ctx.RequireBoolean(2)
	if esEnv.backendSystem != nil &&
		esEnv.backendSystem.Open(p, url, syncv) != nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "No handler for URL")
	}
	return 0
}

// ---------------------------------------------------------------------------
// fnlist_route — C: es_route.c:266-271
// ---------------------------------------------------------------------------

var esFnlistRoute = []gaftape.FunctionListEntry{
	{Key: "create", Value: esRouteCreate, Nargs: 2},
	{Key: "backendOpen", Value: esBackendOpen, Nargs: 3},
	{Key: "test", Value: esRouteTest, Nargs: 1},
}

// C: ES_MODULE("route", fnlist_route)
func registerEsRoute() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "route",
		Functions: esFnlistRoute,
	})
}
