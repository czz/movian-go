package glw

// C: src/ui/glw/glw_event.h + src/ui/glw/glw_event.c — canonical 1:1 port.
//
// C's event-map subtypes embed glw_event_map_t as the first member and cast
// the base pointer back to the subtype. The Go port does the same via
// unsafe.Pointer (Map is always the first field, so addresses coincide).

import (
	"fmt"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// C: typedef struct glw_event_map (glw_event.h:21-37)
type GlwEventMap struct {
	gemLinkNext *GlwEventMap // C: LIST_ENTRY gem_link
	gemLinkPrev **GlwEventMap

	gemFilter *miscpkg.Rstr // C: gem_filter

	gemFire func(w *Glw, gem *GlwEventMap, src *eventpkg.Event) // C: gem_fire
	gemDtor func(gr *glwRoot, gem *GlwEventMap)                 // C: gem_dtor

	gemID int // C: gem_id

	gemEarly int8 // C: gem_early (intercepts on event descent)
	gemFinal int8 // C: gem_final

	gemFile *miscpkg.Rstr // C: gem_file
	gemLine int           // C: gem_line
}

// ---------------------------------------------------------------------------
// C: static void glw_event_map_send_to_widget (glw_event.c:41-50)
func glwEventMapSendToWidget(w *Glw, src *eventpkg.Event, origin *eventpkg.Event) {
	var clone *eventpkg.Event
	if src != nil {
		clone = src.Clone()
	}
	if clone == nil {
		glwEventToWidget(w, src)
		return
	}
	clone.ApplyMetadata(origin)
	glwEventToWidget(w, clone)
	clone.Release()
}

// C: static void glw_event_map_destroy (glw_event.c:55-62)
func glwEventMapDestroy(gr *glwRoot, gem *GlwEventMap) {
	miscpkg.RstrRelease(gem.gemFilter)
	miscpkg.RstrRelease(gem.gemFile)
	gem.gemDtor(gr, gem)
}

// ---------------------------------------------------------------------------
// C: typedef struct glw_event_external (glw_event.c:30-34)
type glwEventExternal struct {
	Map GlwEventMap     // C: glw_event_map_t map
	e   *eventpkg.Event // C: event_t *e
}

// C: static void glw_event_map_external_dtor (glw_event.c:69-74)
func glwEventMapExternalDtor(gr *glwRoot, gem *GlwEventMap) {
	gee := (*glwEventExternal)(unsafe.Pointer(gem))
	gee.e.Release()
	// C: free(gee) — GC
}

// C: static void glw_event_map_external_fire (glw_event.c:79-84)
func glwEventMapExternalFire(w *Glw, gem *GlwEventMap, src *eventpkg.Event) {
	gee := (*glwEventExternal)(unsafe.Pointer(gem))
	glwEventMapSendToWidget(w, gee.e, src)
}

// C: glw_event_map_t *glw_event_map_external_create (glw_event.c:87-95)
func glwEventMapExternalCreate(e *eventpkg.Event) *GlwEventMap {
	gee := &glwEventExternal{} // C: calloc
	gee.e = e
	gee.Map.gemDtor = glwEventMapExternalDtor
	gee.Map.gemFire = glwEventMapExternalFire
	return &gee.Map
}

// ---------------------------------------------------------------------------
// C: typedef struct glw_event_playTrack (glw_event.c:98-105)
type glwEventPlayTrack struct {
	Map GlwEventMap // C: glw_event_map_t map

	track  *propcore.Prop // C: track
	source *propcore.Prop // C: source
	mode   int            // C: mode
}

// C: static void glw_event_map_playTrack_dtor (glw_event.c:107-116)
func glwEventMapPlayTrackDtor(gr *glwRoot, gem *GlwEventMap) {
	g := (*glwEventPlayTrack)(unsafe.Pointer(gem))
	glwDeps.pm.RefDec(g.track)
	if g.source != nil {
		glwDeps.pm.RefDec(g.source)
	}
	// C: free(g) — GC
}

// C: static void glw_event_map_playTrack_fire (glw_event.c:121-129)
func glwEventMapPlayTrackFire(w *Glw, gem *GlwEventMap, src *eventpkg.Event) {
	g := (*glwEventPlayTrack)(unsafe.Pointer(gem))
	e := glwDeps.em.CreatePlayTrack(g.track, g.source, g.mode)
	e.ApplyMetadata(src)
	glwEventToWidget(w, &e.Event)
	e.Release()
}

// C: glw_event_map_t *glw_event_map_playTrack_create (glw_event.c:132-143)
func glwEventMapPlayTrackCreate(track, source *propcore.Prop, mode int) *GlwEventMap {
	g := &glwEventPlayTrack{} // C: calloc
	g.track = glwDeps.pm.RefInc(track)
	g.source = glwDeps.pm.RefInc(source)
	g.mode = mode
	g.Map.gemDtor = glwEventMapPlayTrackDtor
	g.Map.gemFire = glwEventMapPlayTrackFire
	return &g.Map
}

// ---------------------------------------------------------------------------
// C: typedef struct glw_event_propref (glw_event.c:147-152)
type glwEventPropref struct {
	Map    GlwEventMap    // C: glw_event_map_t map
	prop   *propcore.Prop // C: prop
	target *propcore.Prop // C: target
}

// C: static void glw_event_map_propref_dtor (glw_event.c:158-166)
func glwEventMapProprefDtor(gr *glwRoot, gem *GlwEventMap) {
	g := (*glwEventPropref)(unsafe.Pointer(gem))
	glwDeps.pm.RefDec(g.prop)
	glwDeps.pm.RefDec(g.target)
	// C: free(g) — GC
}

// C: static void glw_event_map_propref_fire (glw_event.c:171-185)
func glwEventMapProprefFire(w *Glw, gem *GlwEventMap, src *eventpkg.Event) {
	g := (*glwEventPropref)(unsafe.Pointer(gem))

	e := glwDeps.em.CreateProp(eventpkg.EVENT_PROPREF, g.prop)

	if g.target != nil {
		e.AttachNav(w.glwRoot.grPropNav) // C: e->e_nav = prop_ref_inc(...)
		glwDeps.pm.SendExtEvent(g.target, &e.Event)
	} else {
		e.ApplyMetadata(src)
		glwEventToWidget(w, &e.Event)
	}
	e.Release()
}

// C: glw_event_map_t *glw_event_map_propref_create (glw_event.c:188-197)
func glwEventMapProprefCreate(prop, target *propcore.Prop) *GlwEventMap {
	g := &glwEventPropref{} // C: calloc
	g.prop = glwDeps.pm.RefInc(prop)
	g.target = glwDeps.pm.RefInc(target)
	g.Map.gemDtor = glwEventMapProprefDtor
	g.Map.gemFire = glwEventMapProprefFire
	return &g.Map
}

// ---------------------------------------------------------------------------
// C: typedef struct glw_event_deliverEvent (glw_event.c:202-207)
type glwEventDeliverEvent struct {
	Map    GlwEventMap     // C: glw_event_map_t map
	target *propcore.Prop  // C: target
	event  *eventpkg.Event // C: event
}

// C: static void glw_event_map_deliverEvent_dtor (glw_event.c:210-220)
func glwEventMapDeliverEventDtor(gr *glwRoot, gem *GlwEventMap) {
	de := (*glwEventDeliverEvent)(unsafe.Pointer(gem))
	if de.event != nil {
		de.event.Release()
	}
	glwDeps.pm.RefDec(de.target)
	// C: free(de) — GC
}

// C: static void glw_event_map_deliverEvent_fire (glw_event.c:225-251)
func glwEventMapDeliverEventFire(w *Glw, gem *GlwEventMap, src *eventpkg.Event) {
	de := (*glwEventDeliverEvent)(unsafe.Pointer(gem))
	if de.event == nil {
		if src != nil {
			glwTrace("Event-map at %s:%d relayed source event '%s'",
				miscpkg.RstrGet(gem.gemFile),
				gem.gemLine,
				glwDeps.em.Sprint(src))
			glwDeps.pm.SendExtEvent(de.target, src)
		} else {
			glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW",
				"Event-map at %s:%d failed -- No source event to relay",
				miscpkg.RstrGet(gem.gemFile),
				gem.gemLine)
		}
		return
	}
	glwTrace("Event-map at %s:%d relayed event '%s'",
		miscpkg.RstrGet(gem.gemFile),
		gem.gemLine,
		glwDeps.em.Sprint(de.event))
	glwDeps.pm.SendExtEvent(de.target, de.event)
}

// C: glw_event_map_t *glw_event_map_deliverEvent_create (glw_event.c:254-264)
func glwEventMapDeliverEventCreate(target *propcore.Prop, event *eventpkg.Event) *GlwEventMap {
	de := &glwEventDeliverEvent{} // C: calloc
	de.target = glwDeps.pm.RefInc(target)
	de.event = event
	de.Map.gemDtor = glwEventMapDeliverEventDtor
	de.Map.gemFire = glwEventMapDeliverEventFire
	return &de.Map
}

// ---------------------------------------------------------------------------
// C: void glw_event_map_add (glw_event.c:269-282)
func glwEventMapAdd(w *Glw, gem *GlwEventMap) {
	for o := w.glwEventMaps.lhFirst; o != nil; o = o.gemLinkNext {
		if o.gemID == gem.gemID {
			// C: LIST_REMOVE(o, gem_link)
			if o.gemLinkNext != nil {
				o.gemLinkNext.gemLinkPrev = o.gemLinkPrev
			}
			*o.gemLinkPrev = o.gemLinkNext
			glwEventMapDestroy(w.glwRoot, o)
			break
		}
	}
	// C: LIST_INSERT_HEAD(&w->glw_event_maps, gem, gem_link)
	gem.gemLinkNext = w.glwEventMaps.lhFirst
	if gem.gemLinkNext != nil {
		gem.gemLinkNext.gemLinkPrev = &gem.gemLinkNext
	}
	w.glwEventMaps.lhFirst = gem
	gem.gemLinkPrev = &w.glwEventMaps.lhFirst
}

// C: void glw_event_map_remove_by_id (glw_event.c:287-299)
func glwEventMapRemoveByID(w *Glw, id int) {
	for o := w.glwEventMaps.lhFirst; o != nil; o = o.gemLinkNext {
		if o.gemID == id {
			// C: LIST_REMOVE(o, gem_link)
			if o.gemLinkNext != nil {
				o.gemLinkNext.gemLinkPrev = o.gemLinkPrev
			}
			*o.gemLinkPrev = o.gemLinkNext
			glwEventMapDestroy(w.glwRoot, o)
			return
		}
	}
	fmt.Printf("Remove by id failed to remove %d\n", id)
}

// ---------------------------------------------------------------------------
// C: static glw_t *glw_event_find_target2 (glw_event.c:335-352)
func glwEventFindTarget2(w *Glw, forbidden *Glw, id string) *Glw {
	if w.glwIdRstr != nil && miscpkg.RstrGet(w.glwIdRstr) == id {
		return w
	}

	if w.glwClass.gcFlags&glwNavigationSearchBoundary != 0 {
		return nil
	}

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		if c == forbidden {
			continue
		}
		if r := glwEventFindTarget2(c, nil, id); r != nil {
			return r
		}
	}
	return nil
}

// C: static glw_t *glw_event_find_target (glw_event.c:357-371)
func glwEventFindTarget(w *Glw, id string) *Glw {
	if r := glwEventFindTarget2(w, nil, id); r != nil {
		return r
	}

	for w.glwParent != nil {
		if r := glwEventFindTarget2(w.glwParent, w, id); r != nil {
			return r
		}
		w = w.glwParent
	}
	return nil
}

// C: glw_t *glw_find_neighbour (glw_event.c:374-378)
func glwFindNeighbour(w *Glw, id string) *Glw {
	return glwEventFindTarget(w, id)
}

// ---------------------------------------------------------------------------
// C: typedef struct glw_event_internal (glw_event.c:381-389)
type glwEventInternal struct {
	Map GlwEventMap // C: glw_event_map_t map

	target string              // C: char *target
	event  eventpkg.ActionType // C: action_type_t event
	uc     int                 // C: uc
}

// C: static void glw_event_map_internal_dtor (glw_event.c:398-405)
func glwEventMapInternalDtor(gr *glwRoot, gem *GlwEventMap) {
	// C: free(g->target); free(g) — GC
}

// C: static void glw_event_map_internal_fire (glw_event.c:411-439)
func glwEventMapInternalFire(w *Glw, gem *GlwEventMap, src *eventpkg.Event) {
	g := (*glwEventInternal)(unsafe.Pointer(gem))
	var e *eventpkg.Event

	if g.uc != 0 {
		e = &glwDeps.em.CreateInt(eventpkg.EVENT_UNICODE, g.uc).Event
	} else {
		e = &glwDeps.em.CreateAction(g.event).Event
	}
	e.AttachNav(w.glwRoot.grPropNav) // C: e->e_nav = prop_ref_inc(...)

	if g.target != "" {
		t := glwEventFindTarget(w, g.target)
		if t == nil {
			glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW", "Targeted widget %s not found", g.target)
		} else {
			if !glwSendEvent2(t, e) {
				glwBubbleEvent2(t, e)
			}
		}
	} else {
		e.ApplyMetadata(src)
		glwEventToWidget(w, e)
	}
	e.Release()
}

// C: glw_event_map_t *glw_event_map_internal_create (glw_event.c:442-456)
func glwEventMapInternalCreate(target string, event eventpkg.ActionType, uc int) *GlwEventMap {
	g := &glwEventInternal{} // C: calloc
	if target != "" {
		g.target = target // C: target ? strdup(target) : NULL
	}
	g.event = event
	g.uc = uc
	g.Map.gemDtor = glwEventMapInternalDtor
	g.Map.gemFire = glwEventMapInternalFire
	return &g.Map
}

// C: static int gem_dispatch (glw_event.c:458-473)
func gemDispatch(w *Glw, gem *GlwEventMap, e *eventpkg.Event, early int8) int {
	earlyStr := "ascent"
	if early != 0 {
		earlyStr = "descent"
	}
	finalStr := "no"
	if gem.gemFinal != 0 {
		finalStr = "yes"
	}
	glwTrace("Event '%s' intercepted by event-map '%s' at %s:%d "+
		"during %s final=%s",
		glwDeps.em.Sprint(e),
		miscpkg.RstrGet(gem.gemFilter),
		miscpkg.RstrGet(gem.gemFile),
		gem.gemLine,
		earlyStr, finalStr)

	gem.gemFire(w, gem, e)

	return int(gem.gemFinal)
}

// C: int glw_event_map_intercept (glw_event.c:478-530)
func glwEventMapIntercept(w *Glw, e *eventpkg.Event, early int8) int {
	for gem := w.glwEventMaps.lhFirst; gem != nil; gem = gem.gemLinkNext {
		var str string

		if gem.gemEarly != early {
			continue
		}

		switch e.Type {
		case eventpkg.EVENT_OPENURL:
			str = "navOpen"

		case eventpkg.EVENT_PLAYTRACK:
			str = "playTrackFromSource"

		case eventpkg.EVENT_ACTION_VECTOR:
			// C: event_action_vector_t *eav = (event_action_vector_t *)e
			eav := (*eventpkg.EventActionVector)(unsafe.Pointer(e))
			for i := range len(eav.Actions) {
				if miscpkg.PatternMatch(glwDeps.em.ActionCode2Str(eav.Actions[i]),
					miscpkg.RstrGet(gem.gemFilter)) != 0 {
					return gemDispatch(w, gem, e, early)
				}
			}
			continue

		case eventpkg.EVENT_DYNAMIC_ACTION:
			// C: event_payload_t *ep = (event_payload_t *)e
			ep := (*eventpkg.EventPayload)(unsafe.Pointer(e))
			str = ep.Payload

		case eventpkg.EVENT_PROP_ACTION:
			// C: event_prop_action_t *epa = (event_prop_action_t *)e
			epa := (*eventpkg.EventPropAction)(unsafe.Pointer(e))
			str = epa.Action

		default:
			continue
		}

		if miscpkg.PatternMatch(str, miscpkg.RstrGet(gem.gemFilter)) != 0 {
			return gemDispatch(w, gem, e, early)
		}
	}
	return 0
}

// C: int glw_event_glw_action (glw_event.c:535-553)
func glwEventGlwAction(w *Glw, action string) int {
	for gem := w.glwEventMaps.lhFirst; gem != nil; gem = gem.gemLinkNext {
		if miscpkg.RstrGet(gem.gemFilter) == action {
			gem.gemFire(w, gem, nil)
			return int(gem.gemFinal)
		}
	}
	return 0
}
