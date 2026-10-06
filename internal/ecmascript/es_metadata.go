// Canonical 1:1 port of src/ecmascript/es_metadata.c — videoMetadataBind
// and bindPlayInfo.
package ecmascript

import (
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/metadata"
	misc "github.com/czz/movian-go/internal/misc"
)

// ---------------------------------------------------------------------------
// es_mlv_t — C: es_metadata.c:29-32
// ---------------------------------------------------------------------------

// C: es_mlv_t
type esMLV struct {
	super *ESResource
	mlv   *metadata.MetadataLazyVideo
}

// esMLVDestroy — C: es_mlv_destroy (es_metadata.c:40-45)
func esMLVDestroy(eres *ESResource) {
	em := eres.Data.(*esMLV)
	esEnv.metadata.MLVUnbind(em.mlv, 0)
	EsResourceUnlink(eres)
}

// C: es_resource_mlv (es_metadata.c:52-56)
var esResourceMLV = &ESResourceClass{
	ErcName:    "mlv",
	ErcDestroy: esMLVDestroy,
}

// esVideoMetadataBindGaf — C: es_video_metadata_bind_duk (es_metadata.c:62-98)
func esVideoMetadataBindGaf(ctx *gaftape.Context) int {
	root := esStpropGet(ctx, 0)
	urlstr := ctx.SafeToString(1)
	ec := EsGet(ctx)
	em := &esMLV{}
	em.super = EsResourceCreate(ec, esResourceMLV, 0, em).(*ESResource)

	url := urlstr
	var title string
	filename := EsPropToRstr(ctx, 2, "filename")
	year := EsPropToInt(ctx, 2, "year", -1)

	if filename != nil {
		// Raw filename case
		// C: title = metadata_remove_postfix_rstr(filename)
		title = misc.RstrGet(metadata.MetadataRemovePostfixRstr(
			misc.RstrAllocStr(*filename), esGconf()))
	} else {
		if t := EsPropToRstr(ctx, 2, "title"); t != nil {
			title = *t
		}
	}

	season := EsPropToInt(ctx, 2, "season", -1)
	episode := EsPropToInt(ctx, 2, "episode", -1)
	var imdb string
	if i := EsPropToRstr(ctx, 2, "imdb"); i != nil {
		imdb = *i
	}
	duration := EsPropToInt(ctx, 2, "duration", -1)

	em.mlv = esEnv.metadata.MetadataBindVideoInfo(
		url, title, imdb, duration, root, "", 0, 0,
		year, season, episode, 0, ec.ecID)

	EsResourcePush(ctx, em.super)
	return 1
}

// esBindPlayInfo — C: es_bind_play_info (es_metadata.c:105-111)
func esBindPlayInfo(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	url := ctx.ToString(1)
	esEnv.metadata.PlayInfoBindURLToProp(url, p)
	return 0
}

// ---------------------------------------------------------------------------
// fnlist_metadata — C: es_metadata.c:116-121
// ---------------------------------------------------------------------------

var esFnlistMetadata = []gaftape.FunctionListEntry{
	{Key: "videoMetadataBind", Value: esVideoMetadataBindGaf, Nargs: 3},
	{Key: "bindPlayInfo", Value: esBindPlayInfo, Nargs: 2},
}

// C: ES_MODULE("metadata", fnlist_metadata)
func registerEsMetadata() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "metadata",
		Functions: esFnlistMetadata,
	})
}
