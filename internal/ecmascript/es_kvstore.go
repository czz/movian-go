// Canonical 1:1 port of src/ecmascript/es_kvstore.c
//
// Per-URL key/value store access from JS ("native/kvstore").
package ecmascript

import (
	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/misc"
)

// ---------------------------------------------------------------------------
// es_kvstore_get_domain — C: es_kvstore.c:27-35
// ---------------------------------------------------------------------------

func esKvstoreGetDomain(ctx *gaftape.Context, validx int) int {
	domain := ctx.ToString(validx)
	if domain == "plugin" {
		return kvstore.DomainPlugin
	}
	ctx.Error(gaftape.GAF_ERR_ERROR, "Unknown domain %s", domain)
	return 0
}

// ---------------------------------------------------------------------------
// es_kvstore_get_string — C: es_kvstore.c:41-53
// ---------------------------------------------------------------------------

func esKvstoreGetString(ctx *gaftape.Context) int {
	r, ok := esEnv.kvstore.UrlOptGetStringOK(ctx.ToString(0),
		esKvstoreGetDomain(ctx, 1),
		ctx.ToString(2))
	if !ok {
		return 0
	}
	ctx.PushString(r)
	return 1
}

// ---------------------------------------------------------------------------
// es_kvstore_get_int — C: es_kvstore.c:59-69
// ---------------------------------------------------------------------------

func esKvstoreGetInt(ctx *gaftape.Context) int {
	def := int64(0)
	if ctx.IsNumber(3) {
		def = int64(ctx.GetNumber(3))
	}
	r := esEnv.kvstore.UrlOptGetInt64(ctx.ToString(0),
		esKvstoreGetDomain(ctx, 1),
		ctx.ToString(2), def)

	ctx.PushNumber(float64(r))
	return 1
}

// ---------------------------------------------------------------------------
// es_kvstore_get_bool — C: es_kvstore.c:75-84
// ---------------------------------------------------------------------------

func esKvstoreGetBool(ctx *gaftape.Context) int {
	r := esEnv.kvstore.UrlOptGetInt(ctx.ToString(0),
		esKvstoreGetDomain(ctx, 1),
		ctx.ToString(2),
		misc.BoolToInt(ctx.ToBoolean(3)))

	ctx.PushBoolean(r != 0)
	return 1
}

// ---------------------------------------------------------------------------
// es_kvstore_set — C: es_kvstore.c:90-111
// ---------------------------------------------------------------------------

func esKvstoreSet(ctx *gaftape.Context) int {
	url := ctx.ToString(0)
	domain := esKvstoreGetDomain(ctx, 1)
	key := ctx.ToString(2)

	if ctx.IsBoolean(3) {
		esEnv.kvstore.UrlOptSet(url, domain, key, kvstore.SetInt,
			misc.BoolToInt(ctx.GetBoolean(3)))
	} else if ctx.IsNumber(3) {
		esEnv.kvstore.UrlOptSet(url, domain, key, kvstore.SetInt64,
			int64(ctx.GetNumber(3)))
	} else if ctx.IsObjectCoercible(3) {
		esEnv.kvstore.UrlOptSet(url, domain, key, kvstore.SetString,
			ctx.GetString(3))
	} else {
		esEnv.kvstore.UrlOptSet(url, domain, key, kvstore.SetVoid, nil)
	}
	return 0
}

// ---------------------------------------------------------------------------
// fnlist + module — C: es_kvstore.c:117-129
// ---------------------------------------------------------------------------

var fnlistKvstore = []gaftape.FunctionListEntry{
	{Key: "getString", Value: esKvstoreGetString, Nargs: 3},
	{Key: "getInteger", Value: esKvstoreGetInt, Nargs: 4},
	{Key: "getBoolean", Value: esKvstoreGetBool, Nargs: 4},
	{Key: "set", Value: esKvstoreSet, Nargs: 4},
}

// ES_MODULE("kvstore", fnlist_kvstore)
func registerEsKvstore() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "kvstore",
		Functions: fnlistKvstore,
	})
}
