// Canonical 1:1 port of src/ecmascript/es_hook.c — hook resources and
// es_hook_invoke.
package ecmascript

import (
	"slices"

	"github.com/czz/movian-go/internal/gaftape"
)

// ---------------------------------------------------------------------------
// es_hook_t — C: es_hook.c:25-29
// ---------------------------------------------------------------------------

// C: es_hook_t
type esHook struct {
	super  *ESResource
	ehType string
}

// esHookDestroy — C: es_hook_destroy (es_hook.c:40-55)
func esHookDestroy(eres *ESResource) {
	eh := eres.Data.(*esHook)

	EsRootUnregister(eres.erCtx.ecGaf, eres)

	sys := eres.erCtx.sys
	sys.hookMu.Lock()
	for i, x := range sys.hookList {
		if x == eh {
			sys.hookList = slices.Delete(sys.hookList, i, i+1)
			break
		}
	}
	sys.hookNum--
	sys.hookMu.Unlock()

	EsResourceUnlink(eh.super)
}

// esHookInfo — C: es_hook_info (es_hook.c:61-66)
func esHookInfo(eres *ESResource) string {
	eh := eres.Data.(*esHook)
	return eh.ehType
}

// C: es_resource_hook (es_hook.c:72-77)
var esResourceHook = &ESResourceClass{
	ErcName:    "hook",
	ErcDestroy: esHookDestroy,
	ErcInfo:    esHookInfo,
}

// EsHookInvoke — C: es_hook_invoke (es_hook.c:83-115)
func EsHookInvoke(typ string,
	pushArgs func(gaf *gaftape.Context, opaque any) int,
	opaque any) int {

	// First create an array with all matching sys.hookList
	sys := esEnv.sys
	sys.hookMu.Lock()
	var v []*esHook
	for _, eh := range sys.hookList {
		if eh.ehType == typ {
			v = append(v, eh)
			esResourceRetain(eh.super)
		}
	}
	sys.hookMu.Unlock()

	for _, eh := range v {
		ec := eh.super.erCtx
		ctx := EsContextBegin(ec)

		EsPushRoot(ctx, eh)
		r := pushArgs(ctx, opaque)
		rc := ctx.PCall(r)
		if rc != 0 {
			EsDumpErr(ctx)
		}

		ctx.Pop()

		EsContextEnd(ec, 1, ctx)
		EsResourceRelease(eh.super)
	}
	return 0
}

// esHookRegister — C: es_hook_register (es_hook.c:121-138)
func esHookRegister(ctx *gaftape.Context) int {
	typ := ctx.SafeToString(0)
	ec := EsGet(ctx)

	eh := &esHook{}
	eh.super = EsResourceCreate(ec, esResourceHook, 1, eh).(*ESResource)
	eh.ehType = typ

	sys := ec.sys
	sys.hookMu.Lock()
	sys.hookList = slices.Insert(sys.hookList, 0, eh) // LIST_INSERT_HEAD
	sys.hookNum++
	sys.hookMu.Unlock()

	EsRootRegister(ctx, 1, eh)

	EsResourcePush(ctx, eh.super)
	return 1
}

// ---------------------------------------------------------------------------
// fnlist_hook — C: es_hook.c:144-148
// ---------------------------------------------------------------------------

var esFnlistHook = []gaftape.FunctionListEntry{
	{Key: "register", Value: esHookRegister, Nargs: 2},
}

// C: ES_MODULE("hook", fnlist_hook)
func registerEsHook() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "hook",
		Functions: esFnlistHook,
	})
}
