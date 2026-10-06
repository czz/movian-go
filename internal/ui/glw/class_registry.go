package glw

// C: src/ui/glw/glw.c — canonical 1:1 port.
//
// Global singletons used by C (event_mgr, the prop context, gconf.*,
// runcontrol) map to package-level seams configured at init time, matching
// the established convention (GconfClipboardSet, EsSetPropDeps, ...).

import (
	"fmt"

	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: static LIST_HEAD(, glw_class) glw_classes (glw.c:2786)
var glwClasses = struct{ lhFirst *glwClass }{}

// C: const glw_class_t *glw_class_find_by_name (glw.c:2792-2804)
func glwClassFindByName(name string) *glwClass {
	for gc := glwClasses.lhFirst; gc != nil; gc = gc.gcLinkNext {
		if gc.gcName == name {
			break
		}
		if gc.gcName2 != "" && gc.gcName2 == name {
			break
		}
	}
	var gc *glwClass
	for gc = glwClasses.lhFirst; gc != nil; gc = gc.gcLinkNext {
		if gc.gcName == name || (gc.gcName2 != "" && gc.gcName2 == name) {
			break
		}
	}
	return gc
}

// C: void glw_register_class (glw.c:2809-2814)
func glwRegisterClass(gc *glwClass) {
	if gc.gcLayout == nil {
		panic("glw_register_class: gc_layout == NULL")
	}
	gc.gcLinkNext = glwClasses.lhFirst
	glwClasses.lhFirst = gc
}

// C: const char *glw_get_name (glw.c:2820-2840)
func glwGetName(w *Glw) string {
	extra := ""
	gc := w.glwClass
	if w == w.glwRoot.grUniverse {
		return "Universe"
	}

	if gc.gcGetIdentity != nil {
		var tmp [512]byte
		extra = gc.gcGetIdentity(w, tmp[:])
	}

	if extra == "" && gc.gcGetText != nil {
		extra = gc.gcGetText(w)
	}

	sp := ""
	if extra != "" {
		sp = " "
	}
	return fmt.Sprintf("%s @ %s:%d%s%s",
		gc.gcName,
		miscpkg.RstrGet(w.glwFile),
		w.glwLine,
		sp,
		extra)
}
