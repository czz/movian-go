package glw

// C: src/ui/glw/glw_scope.c — canonical 1:1 port.

import (
	backendcore "github.com/czz/movian-go/internal/backend/core"
)

// C: glw_scope_t *glw_scope_dup (glw_scope.c:29-46)
func glwScopeDup(src *glwScope, retainMask int) *glwScope {
	o := &glwScope{}

	*o = *src // C: memcpy(o, src, sizeof(glw_scope_t))
	o.gsBackend = backendcore.BackendRetain(src.gsBackend)
	if src.gsEvent != nil {
		src.gsEvent.AddRef() // C: event_addref
	}
	o.gsEvent = src.gsEvent

	for i := range src.gsNumRoots {
		if (1<<uint(i))&retainMask == 0 {
			o.gsRoots[i].P = glwDeps.pm.RefInc(o.gsRoots[i].P)
		}
	}
	o.gsRefcount = 1
	return o
}

// C: glw_scope_t *glw_scope_create (glw_scope.c:50-65)
func glwScopeCreate() *glwScope {
	o := &glwScope{} // C: calloc

	o.gsRoots[glwRootSelf].Name = "self"
	o.gsRoots[glwRootParent].Name = "parent"
	o.gsRoots[glwRootView].Name = "view"
	o.gsRoots[glwRootArgs].Name = "args"
	o.gsRoots[glwRootClone].Name = "clone"
	o.gsRoots[glwRootCore].Name = "core"
	o.gsRoots[glwRootParentview].Name = "parentview"
	o.gsNumRoots = glwRootStatic
	o.gsRefcount = 1
	return o
}

// C: void glw_scope_release (glw_scope.c:67-82)
func glwScopeRelease(gs *glwScope) {
	gs.gsRefcount--
	if gs.gsRefcount != 0 {
		return
	}

	backendcore.BackendRelease(gs.gsBackend)

	gs.gsEvent.Release() // C: event_release — nil-safe via method

	for i := range gs.gsNumRoots {
		glwDeps.pm.RefDec(gs.gsRoots[i].P)
	}
	// C: free(gs) — GC
}

// C: glw_scope_t *glw_scope_retain (glw_scope.c:85-90)
func glwScopeRetain(gs *glwScope) *glwScope {
	gs.gsRefcount++
	return gs
}
