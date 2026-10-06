package glw

// Canonical port of src/ui/glw/glw_keyintercept.c — the `keyintercept`
// widget class (mirrors a bound string property into a codepoint
// buffer; the event-handling half is dead code in C — #if 0 — and
// intentionally not ported).

import (
	"unsafe"

	propcore "github.com/czz/movian-go/internal/prop"
)

const kiBufLen = 64

// C: glw_keyintercept_t
type glwKeyintercept struct {
	w Glw

	buf    [kiBufLen]int
	buflen int

	sub  *propcore.Subscription
	prop *propcore.Prop
}

// C: updatestr + ki_handle_event — dead code under #if 0 in C
// (glw_keyintercept.c:37-88); intentionally not ported.

// C: ki_unbind
func kiUnbind(ki *glwKeyintercept) {
	if ki.sub != nil {
		glwDeps.pm.Unsubscribe(ki.sub)
		ki.sub = nil
	}

	if ki.prop != nil {
		glwDeps.pm.RefDec(ki.prop)
		ki.prop = nil
	}
}

// C: glw_keyintercept_layout
func glwKeyinterceptLayout(w *Glw, rc *glwRctx) {
	c := w.glwChilds.tqhFirst
	if c != nil {
		glwLayout0(c, rc)
	}
}

// C: glw_keyintercept_callback
func glwKeyinterceptCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalDestroy:
		kiUnbind((*glwKeyintercept)(unsafe.Pointer(w)))
	case glwSignalChildConstraintsChanged:
		c, _ := extra.(*Glw)
		glwCopyConstraints(w, c)
		return 1
	}
	return 0
}

// C: glw_keyintercept_render
func glwKeyinterceptRender(w *Glw, rc *glwRctx) {
	if c := w.glwChilds.tqhFirst; c != nil {
		glwRender0(c, rc)
	}
}

// C: prop_callback
func kiPropCallback(opaque any, event propcore.EventType,
	args ...any) {
	ki := (*glwKeyintercept)(opaque.(unsafe.Pointer))

	switch event {
	case propcore.EventSetVoid:
		ki.buflen = 0

	case propcore.EventSetRString:
		str := strFromArg(args[0])

		ki.buflen = 0
		for _, c := range str {
			if ki.buflen >= kiBufLen {
				break
			}
			ki.buf[ki.buflen] = int(c)
			ki.buflen++
		}

	case propcore.EventValueProp:
		glwDeps.pm.RefDec(ki.prop)
		ki.prop = glwDeps.pm.RefInc(args[0].(*propcore.Prop))
	}
}

// C: bind_to_property
func kiBindToProperty(w *Glw, scope *glwScope, pname []string) {
	ki := (*glwKeyintercept)(unsafe.Pointer(w))
	kiUnbind(ki)

	ki.sub = glwPropSubscribeTags(
		propcore.SubFlagDirectUpdate|propcore.SubFlagSendValueProp,
		propTagName, pname,
		propTagCallback, kiPropCallback, unsafe.Pointer(ki),
		propTagCourier, w.glwRoot.grCourier,
		propTagRootVector, scope.gsRoots[:scope.gsNumRoots],
		propTagNamedRoot, w.glwRoot.grPropUi, "ui",
		propTagNamedRoot, w.glwRoot.grPropNav, "nav")
}

// C: glw_class_t glw_keyintercept
var glwKeyinterceptClass = &glwClass{
	gcName:         "keyintercept",
	gcInstanceSize: int(unsafe.Sizeof(glwKeyintercept{})),
	gcNew: func(parent *Glw) *Glw {
		ki := &glwKeyintercept{}
		return &ki.w
	},
	gcLayout:         glwKeyinterceptLayout,
	gcRender:         glwKeyinterceptRender,
	gcSignalHandler:  glwKeyinterceptCallback,
	gcBindToProperty: kiBindToProperty,
}

func registerKeyintercept() {
	glwRegisterClass(glwKeyinterceptClass)
}
