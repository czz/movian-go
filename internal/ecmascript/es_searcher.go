// Canonical 1:1 port of src/ecmascript/es_searcher.c — ecmascript_search.
package ecmascript

import (
	"github.com/czz/movian-go/internal/gaftape"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// searcher_aux_t — C: es_searcher.c:23-27
// ---------------------------------------------------------------------------

// C: searcher_aux_t
type searcherAux struct {
	model   *propcore.Prop
	query   string
	loading *propcore.Prop
}

// searcherPushArgs — C: searcher_push_args (es_searcher.c:34-42)
func searcherPushArgs(gaf *gaftape.Context, opaque any) int {
	sa := opaque.(*searcherAux)
	esStpropPush(gaf, sa.model)
	gaf.PushString(sa.query)
	esStpropPush(gaf, sa.loading)
	return 3
}

// EcmascriptSearch — C: ecmascript_search (es_searcher.c:48-52)
func EcmascriptSearch(model *propcore.Prop, query string, loading *propcore.Prop) {
	sa := &searcherAux{model: model, query: query, loading: loading}
	EsHookInvoke("searcher", searcherPushArgs, sa)
}
