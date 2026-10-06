// Canonical 1:1 port of src/ecmascript/es_native_obj.c
//
// Native object wrapping: objects carry a hidden "\xffptr" pointer property
// and a finalizer function whose magic cell is the native class id.
package ecmascript

import (
	"github.com/czz/movian-go/internal/gaftape"
)

// C: #define PTRNAME "\xff""ptr"
const ptrname = "\xffptr"

// C: ES_NATIVE_CLASS(resource, &es_resource_release) — ecmascript.c:31
var esNativeResource = &EcmascriptNativeClass{
	Name:    "resource",
	Release: func(ptr any) { EsResourceRelease(ptr.(*ESResource)) },
}

func registerEsNativeObj() {
	ecmascriptRegisterNativeClass(esNativeResource)
}

// ---------------------------------------------------------------------------
// ecmascript_register_native_class — C: es_native_obj.c:30-38
// ---------------------------------------------------------------------------

func ecmascriptRegisterNativeClass(c *EcmascriptNativeClass) {
	// C: static int idgen — first nil slot; classes are never
	// unregistered, so the first free slot equals the C counter.
	for id := range nativeClasses {
		if nativeClasses[id] == nil {
			c.ID = id
			nativeClasses[id] = c
			return
		}
	}
	// C: assert(idgen < ARRAYSIZE(native_classes))
	panic("ecmascript: native class table full")
}

// ---------------------------------------------------------------------------
// es_get_native_obj — C: es_native_obj.c:44-69
// ---------------------------------------------------------------------------

func esGetNativeObj(ctx *gaftape.Context, objIdx int,
	wantedType *EcmascriptNativeClass) any {
	ctx.GetFinalizer(objIdx)

	if !ctx.IsFunction(-1) {
		ctx.Error(gaftape.GAF_ERR_ERROR,
			"Object is not of type %s (no finalizer)", wantedType.Name)
	}

	currentType := ctx.GetMagic(-1)

	ctx.Pop()

	if currentType != wantedType.ID {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Object is not of typ %s",
			wantedType.Name)
	}

	ctx.GetPropString(objIdx, ptrname)
	if !ctx.IsPointer(-1) {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Object missing ptr")
	}

	r := ctx.GetPointer(-1)
	ctx.Pop()
	return r
}

// ---------------------------------------------------------------------------
// es_get_native_obj_nothrow — C: es_native_obj.c:74-107
// ---------------------------------------------------------------------------

func esGetNativeObjNothrow(ctx *gaftape.Context, objIdx int,
	wantedType *EcmascriptNativeClass) any {
	if !ctx.IsObject(objIdx) {
		return nil
	}

	if ctx.IsNull(objIdx) {
		return nil
	}

	ctx.GetFinalizer(objIdx)

	if !ctx.IsFunction(-1) {
		return nil
	}

	currentType := ctx.GetMagic(-1)

	ctx.Pop()

	if currentType != wantedType.ID {
		return nil
	}

	ctx.GetPropString(objIdx, ptrname)
	if !ctx.IsPointer(-1) {
		return nil
	}

	r := ctx.GetPointer(-1)
	ctx.Pop()
	return r
}

// ---------------------------------------------------------------------------
// es_native_finalizer — C: es_native_obj.c:113-134
// ---------------------------------------------------------------------------

func esNativeFinalizer(ctx *gaftape.Context) int {
	typ := ctx.GetCurrentMagic()
	// assert(type < ECMASCRIPT_MAX_NATIVE_CLASSES)

	ctx.GetPropString(0, ptrname)

	if ctx.IsPointer(-1) {
		ptr := ctx.GetPointer(-1)
		ctx.DelPropString(0, ptrname)
		nativeClasses[typ].Release(ptr)

		ctx.Pop()
		ec := EsGet(ctx)
		if ec != nil {
			ec.ecNativeInstances[typ]--
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// es_push_native_obj — C: es_native_obj.c:140-152
// ---------------------------------------------------------------------------

func esPushNativeObj(ctx *gaftape.Context, class *EcmascriptNativeClass,
	ptr any) int {
	objIdx := ctx.PushObject()

	ctx.PushPointer(ptr)
	ctx.PutPropString(objIdx, ptrname)

	ctx.PushCFunction(esNativeFinalizer, 1)
	ctx.SetMagic(-1, class.ID)
	ctx.SetFinalizer(objIdx)

	ec := EsGet(ctx)
	if ec != nil {
		ec.ecNativeInstances[class.ID]++
	}

	return objIdx
}

// EsPushNativeObj — exported es_push_native_obj.
func EsPushNativeObj(ctx *gaftape.Context, class *EcmascriptNativeClass,
	ptr any) int {
	return esPushNativeObj(ctx, class, ptr)
}

// EsGetNativeObj — exported es_get_native_obj.
func EsGetNativeObj(ctx *gaftape.Context, objIdx int,
	wantedType *EcmascriptNativeClass) any {
	return esGetNativeObj(ctx, objIdx, wantedType)
}

// EsGetNativeObjNothrow — exported es_get_native_obj_nothrow.
func EsGetNativeObjNothrow(ctx *gaftape.Context, objIdx int,
	wantedType *EcmascriptNativeClass) any {
	return esGetNativeObjNothrow(ctx, objIdx, wantedType)
}

// ---------------------------------------------------------------------------
// ecmascript_native_class_name — C: es_native_obj.c:158-164
// ---------------------------------------------------------------------------

// EcmascriptNativeClassName — C: ecmascript_native_class_name
func EcmascriptNativeClassName(id int) string {
	// assert(id < ECMASCRIPT_MAX_NATIVE_CLASSES)
	if nativeClasses[id] == nil {
		return ""
	}
	return nativeClasses[id].Name
}
