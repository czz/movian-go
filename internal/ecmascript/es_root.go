// Canonical 1:1 port of src/ecmascript/es_root.c
//
// Rooted objects live in the global stash "roots" table, keyed by a
// pointer-formatted name. This prevents GC collection and gives a
// pointer->object lookup used to re-enter parked threads and callbacks.
package ecmascript

import (
	"fmt"

	"github.com/czz/movian-go/internal/gaftape"
)

// rootKey — C: snprintf(name, sizeof(name), "%p", ptr)
func rootKey(ptr any) string {
	return fmt.Sprintf("%p", ptr)
}

// EsRootRegister — C: es_root_register (es_root.c:23-40)
func EsRootRegister(ctx *gaftape.Context, objIdx int, ptr any) {
	objIdx = ctx.NormalizeIndex(objIdx)

	ctx.PushGlobalStash()

	ctx.GetPropString(-1, "roots")

	name := rootKey(ptr)

	ctx.Dup(objIdx)

	ctx.PutPropString(-2, name)
	ctx.Pop2()

	ec := EsGet(ctx)
	if ec != nil {
		ec.ecRootedObjects++
	}
}

// EsRootUnregister — C: es_root_unregister (es_root.c:46-59)
func EsRootUnregister(ctx *gaftape.Context, ptr any) {
	ctx.PushGlobalStash()

	ctx.GetPropString(-1, "roots")

	name := rootKey(ptr)

	ctx.DelPropString(-1, name)
	ctx.Pop2()

	ec := EsGet(ctx)
	if ec != nil {
		ec.ecRootedObjects--
	}
}

// EsPushRoot — C: es_push_root (es_root.c:65-75)
func EsPushRoot(ctx *gaftape.Context, ptr any) {
	ctx.PushGlobalStash()
	ctx.GetPropString(-1, "roots")

	name := rootKey(ptr)

	ctx.GetPropString(-1, name)
	ctx.SwapTop(-3)
	ctx.Pop2()
}
