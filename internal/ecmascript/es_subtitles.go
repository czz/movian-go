// Canonical 1:1 port of src/ecmascript/es_subtitles.c — subtitle provider
// resource, esp_query invocation, addProvider/addItem/getLanguages.
package ecmascript

import (
	"fmt"

	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/i18n"
	mediacore "github.com/czz/movian-go/internal/media/core"
	misc "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/subtitles"
)

// esEnv.i18n — C: i18n_subtitle_lang() uses the global i18n state.

// EsSetI18N wires the i18n instance.
func EsSetI18N(i *i18n.I18N) { esEnv.i18n = i }

// ---------------------------------------------------------------------------
// es_sp_t — C: es_subtitles.c:29-34
// ---------------------------------------------------------------------------

// C: es_sp_t
type esSP struct {
	super *ESResource

	id    string
	sp    subtitles.SubtitleProvider
	title *propcore.Prop
}

// esSPDestroy — C: es_sp_destroy (es_subtitles.c:42-54)
func esSPDestroy(eres *ESResource) {
	esp := eres.Data.(*esSP)

	EsRootUnregister(eres.erCtx.ecGaf, eres)

	if esEnv.subSys != nil {
		esEnv.subSys.SubtitleProviderUnregister(&esp.sp)
	}
	esEnv.propPM.Destroy(esp.title)

	EsResourceUnlink(eres)
}

// esSPInfo — C: es_sp_info (es_subtitles.c:60-64)
func esSPInfo(eres *ESResource) string {
	esp := eres.Data.(*esSP)
	return "subtitleprovider " + esp.id
}

// C: es_resource_sp (es_subtitles.c:70-75)
var esResourceSP = &ESResourceClass{
	ErcName:    "subtitleprovider",
	ErcDestroy: esSPDestroy,
	ErcInfo:    esSPInfo,
}

// espRetain — C: esp_retain (es_subtitles.c:82-86)
func espRetain(sp *subtitles.SubtitleProvider) {
	esp := sp.Opaque().(*esSP)
	esResourceRetain(esp.super)
}

// esSetRstr — C: es_set_rstr (es_subtitles.c:89-98)
func esSetRstr(ctx *gaftape.Context, objIdx int, key string, val *string) {
	if val == nil {
		return
	}

	objIdx = ctx.NormalizeIndex(objIdx)
	ctx.PushString(*val)
	ctx.PutPropString(objIdx, key)
}

// esSetStr — C: es_set_str (es_subtitles.c:101-110)
func esSetStr(ctx *gaftape.Context, objIdx int, key string, val string) {
	if val == "" {
		return
	}

	objIdx = ctx.NormalizeIndex(objIdx)
	ctx.PushString(val)
	ctx.PutPropString(objIdx, key)
}

// esSetInt — C: es_set_int (es_subtitles.c:113-119)
func esSetInt(ctx *gaftape.Context, objIdx int, key string, val int) {
	objIdx = ctx.NormalizeIndex(objIdx)
	ctx.PushInt(val)
	ctx.PutPropString(objIdx, key)
}

// esSetDouble — C: es_set_double (es_subtitles.c:122-128)
func esSetDouble(ctx *gaftape.Context, objIdx int, key string, val float64) {
	objIdx = ctx.NormalizeIndex(objIdx)
	ctx.PushNumber(val)
	ctx.PutPropString(objIdx, key)
}

// espQuery — C: esp_query (es_subtitles.c:134-190)
func espQuery(sp *subtitles.SubtitleProvider, ss *subtitles.SubScanner,
	score int, autosel int) {
	esp := sp.Opaque().(*esSP)
	ec := esp.super.erCtx

	ctx := EsContextBegin(ec)

	if ctx != nil && ss != nil {
		// C: usage_event("Subtitle search", 1, USAGE_SEG("responder", ec->ec_id))
		ec.usage.Event("Subtitle search", 1, "responder", ec.ecID)

		EsPushRoot(ctx, esp)

		esPushNativeObj(ctx, esNativeProp, esEnv.propPM.RefInc(ss.PropRoot()))

		ctx.PushObject()

		// C: es_set_rstr — NULL means skip; Go's SubScanner stores "".
		if t := ss.GetTitle(); t != "" {
			esSetRstr(ctx, -1, "title", &t)
		}
		if i := ss.GetIMdbID(); i != "" {
			esSetRstr(ctx, -1, "imdb", &i)
		}

		if ss.Season() > 0 {
			esSetInt(ctx, -1, "season", ss.Season())
		}

		if ss.Year() > 0 {
			esSetInt(ctx, -1, "year", ss.Year())
		}

		if ss.Episode() > 0 {
			esSetInt(ctx, -1, "episode", ss.Episode())
		}

		if ss.Fsize() > 0 {
			esSetDouble(ctx, -1, "filesize", float64(ss.Fsize()))
		}

		if ss.Duration() > 0 {
			esSetInt(ctx, -1, "duration", ss.Duration())
		}

		if ss.HashValid() {
			esSetStr(ctx, -1, "opensubhash",
				fmt.Sprintf("%016x", ss.OpensubHash()))

			var hexbuf [33]byte
			h := ss.SubDBHash()
			misc.Bin2hex(hexbuf[:32], 32, h[:], 16)
			esSetStr(ctx, -1, "subdbhash", string(hexbuf[:32]))
		}

		ctx.PushInt(score)
		ctx.PushBoolean(autosel != 0)

		rc := ctx.PCall(4)
		if rc != 0 {
			EsDumpErr(ctx)
		}

		ctx.Pop()

	}
	EsResourceRelease(esp.super)
	EsContextEnd(ec, 1, ctx)
}

// esSubtitleProviderAdd — C: es_subtitleprovideradd (es_subtitles.c:197-220)
func esSubtitleProviderAdd(ctx *gaftape.Context) int {
	id := ctx.ToString(1)
	title := ctx.ToString(2)

	ec := EsGet(ctx)
	esp := &esSP{}
	esp.super = EsResourceCreate(ec, esResourceSP, 1, esp).(*ESResource)
	esp.id = id
	EsRootRegister(ctx, 0, esp) // Register callback function

	esp.sp.SetQuery(espQuery)
	esp.sp.SetRetain(espRetain)
	esp.sp.SetOpaque(esp)

	esp.title = esEnv.propPM.CreateRoot("")
	esEnv.propPM.SetStringEx(esp.title, nil, title, propcore.StringUTF8)

	if esEnv.subSys != nil {
		esEnv.subSys.SubtitleProviderRegister(&esp.sp, id, esp.title, 0,
			"plugin", 1, 1)
	}
	EsResourcePush(ctx, esp.super)
	return 1
}

// esSubtitleAddItem — C: es_subtitleadditem (es_subtitles.c:227-246)
func esSubtitleAddItem(ctx *gaftape.Context) int {
	proproot := esStpropGet(ctx, 0)

	url := ctx.ToString(1)
	title := ctx.ToString(2)
	language := ctx.GetString(3)
	format := ctx.GetString(4)
	source := ctx.GetString(5)
	score := int(ctx.GetNumber(6))
	var autosel int
	if ctx.GetBoolean(7) {
		autosel = 1
	}

	mediacore.MpAddTrack(esEnv.propPM, proproot,
		title, url, format, "", language, source, nil,
		score, autosel)
	return 0
}

// esGetSubtitleLanguages — C: es_getsubtitlelanguages (es_subtitles.c:252-267)
func esGetSubtitleLanguages(ctx *gaftape.Context) int {
	idx := 0
	ctx.PushArray()

	for i := range 3 {
		var lang string
		if esEnv.i18n != nil {
			lang = esEnv.i18n.SubtitleLang(uint(i))
		}
		if lang != "" {
			ctx.PushString(lang)
			ctx.PutPropIndex(-2, idx)
			idx++
		}
	}
	return 1
}

// ---------------------------------------------------------------------------
// fnlist_subtitle — C: es_subtitles.c:271-276
// ---------------------------------------------------------------------------

var esFnlistSubtitle = []gaftape.FunctionListEntry{
	{Key: "addProvider", Value: esSubtitleProviderAdd, Nargs: 3},
	{Key: "addItem", Value: esSubtitleAddItem, Nargs: 8},
	{Key: "getLanguages", Value: esGetSubtitleLanguages, Nargs: 0},
}

// C: ES_MODULE("subtitle", fnlist_subtitle)
func registerEsSubtitles() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "subtitle",
		Functions: esFnlistSubtitle,
	})
}
