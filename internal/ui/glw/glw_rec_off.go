//go:build !(glwrec && cgo)

package glw

import (
	eventpkg "github.com/czz/movian-go/internal/event"
)

// C: struct glw_rec — exists only under CONFIG_GLW_REC; the gr_rec pointer
// field in glw_root_t is unconditional so the type must exist either way.
type GlwRec struct{}

// glwRecPostScene — C: the CONFIG_GLW_REC block of glw_post_scene
// (glw.c:767-772) is compiled out when the feature is disabled.
func glwRecPostScene(gr *glwRoot) {}

// glwRecIsRecordAction — C: the ACTION_RECORD_UI branch of
// glw_dispatch_event (glw.c:2441-2445) is compiled out without
// CONFIG_GLW_REC; the event falls through.
func glwRecIsRecordAction(e *eventpkg.Event) bool { return false }

// glwRecToggle — C: static void glw_rec_toggle (glw.c:2395-2408) is
// compiled out without CONFIG_GLW_REC.
func glwRecToggle(gr *glwRoot) {}
