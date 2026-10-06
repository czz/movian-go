//go:build linux || darwin || windows

package arch

// linux_main.go — canonical port of src/arch/linux/linux_main.c:
// linux_ui_t, ui_wanted/ui_current, switch_ui, linux_global_eventsink,
// arch_stop_req and mainloop.
//
// GTK REMOVED (2026-10-04): upstream linux_main.c pumps the loop with
// gtk_main_iteration() and wakes it via g_main_context_wakeup() through
// glibcourier's GSource. The GTK2 frontend (ui_gu, src/ui/gu/*) was
// deleted — GTK2 is EOL since 2020 and the GLW skin renders everything
// GU used to provide (popups/dialogs included, via the theme's
// cloner($prop.popups) → popups/*.view). The loop here pumps the
// process UI courier directly with prop_courier_wait_and_dispatch
// semantics (Courier.Wait): block until a notification is enqueued,
// then dispatch — the same wakeup/dispatch contract gtk_main_iteration
// provided, without GLib.

import (
	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

// uiCourier — C: prop_courier_t *glibcourier (linux_main.c:34).
// The prop courier pumped by the UI mainloop. Unlike C's GSource-based
// glib_courier, Mainloop blocks on this courier directly (Wait), so no
// external main-context wakeup is needed.
var uiCourier *propcore.Courier

// pm/em — the process-global prop manager and event manager, wired by
// UILoopStart (C resolves them through globals at link time).
var (
	pm *propcore.PropManager
	em *event.EventManager
)

// UILoopStart — C: the glibcourier part of linux_main.c main()
// (glibcourier = glib_courier_create(...) + prop_subscribe on
// "global.eventSink" with PROP_TAG_COURIER). Creates the waitable
// courier the mainloop pumps and stores the process managers.
func UILoopStart(propMgr *propcore.PropManager, eventMgr *event.EventManager) {
	pm = propMgr
	em = eventMgr
	// C: glibcourier = prop_courier_create_waitable("glibcourier")
	uiCourier = propcore.NewCourier("uicourier")
}

// LinuxUI — C: linux_ui_t (arch.h). start() creates the UI and returns
// opaque aux state; stop() tears it down and returns the navigator root
// prop to hand to the next UI (or destroy).
type LinuxUI struct {
	Start func(nav *propcore.Prop) any
	Stop  func(aux any) *propcore.Prop
}

// uiWanted/uiCurrent — C: static linux_ui_t *ui_wanted, *ui_current
// (linux_main.c:58-59). C defaults ui_wanted to &ui_glw; with ui_gu
// removed, the second UI is only present when two GLW backends are
// registered (e.g. x11 + glfw), otherwise F12 is a no-op.
var (
	uiWanted  *LinuxUI
	uiCurrent *LinuxUI
	uiOther   *LinuxUI
	uiRunning bool // C: static int running
)

// UILoopStopReq — C: arch_stop_req (linux_main.c:64-69).
// running = 0 + wake the mainloop (C used g_main_context_wakeup;
// Courier.Stop broadcasts the wait cond).
func UILoopStopReq() int {
	uiRunning = false
	if uiCourier != nil {
		uiCourier.Stop()
	}
	return 0
}

// WakeMainloop nudges the blocked mainloop so freshly queued work
// outside the prop system (C: linux_webpopup_check jobs dispatched
// with g_main_context_wakeup) is drained on the next iteration.
func WakeMainloop() {
	if uiCourier != nil {
		uiCourier.Wake()
	}
}

// WebpopupCheck/WebpopupActive — the C mainloop's
// linux_webpopup_check() slot (linux_main.c:110) + its activity
// signal. Wired by the caller (cmd/movian-go) to ui.WpMainCheck /
// ui.WpActive: a direct import would cycle ui→fileaccess→arch→ui.
// nil = feature absent (the C CONFIG_WEBPOPUP=0 loop).
var (
	WebpopupCheck  func()
	WebpopupActive func() bool
)

// switchUI — C: switch_ui (linux_main.c:75-83).
func switchUI() {
	if uiOther == nil {
		return
	}
	if uiCurrent == uiOther {
		uiWanted = uiCurrent
	} else {
		uiWanted = uiOther
	}
	// C bug note: upstream flips between &ui_glw and &ui_gu; here the
	// "other" UI is the registered secondary backend.
}

// linuxGlobalEventsink — C: linux_global_eventsink
// (linux_main.c:120-125).
func linuxGlobalEventsink(et propcore.EventType, args ...any) {
	// PROP_TAG_CALLBACK_EVENT delivers the dispatched event.
	if len(args) == 0 {
		return
	}
	e, ok := args[0].(*event.Event)
	if !ok || e == nil {
		return
	}
	if e.IsAction(event.ACTION_SWITCH_UI) {
		switchUI()
	}
}

// SubscribeGlobalEventsink — C: the prop_subscribe on
// "global.eventSink" in linux_main.c main():183-189.
func SubscribeGlobalEventsink() {
	if pm == nil || uiCourier == nil {
		return
	}
	// C: prop_get_by_name(PNVEC("global", "eventSink"), 1, NULL, NULL)
	sink := pm.GetByName([]string{"global", "eventSink"}, 1, nil, nil)
	if sink == nil {
		return
	}
	pm.SubscribeWithCourier(sink, uiCourier,
		func(opaque any, et propcore.EventType,
			args ...any) {
			linuxGlobalEventsink(et, args...)
		}, nil)
	pm.RefDec(sink)
}

// Mainloop — C: mainloop (linux_main.c:86-114). Runs the UI loop,
// hot-swapping wanted/alternate UIs when uiWanted changes (F12 →
// ACTION_SWITCH_UI via the global event sink). Per iteration the loop
// blocks on the UI courier (prop_courier_wait_and_dispatch) — the
// exact slot gtk_main_iteration() filled in C.
func Mainloop(wanted, other *LinuxUI) {
	var aux any

	uiWanted = wanted
	uiOther = other
	uiRunning = true // C: running = 1

	for uiRunning {
		if uiCurrent != uiWanted {
			var nav *propcore.Prop
			if uiCurrent != nil {
				nav = uiCurrent.Stop(aux)
			} else {
				nav = nil
			}
			uiCurrent = uiWanted
			aux = uiCurrent.Start(nav)
		}

		// C: #if ENABLE_WEBPOPUP linux_webpopup_check()
		// (linux_main.c:110) — GTK/webkit require the process main
		// thread, so queued webpopup jobs run here; nil when the
		// feature is absent.
		if WebpopupCheck != nil {
			WebpopupCheck()
		}

		// C: gtk_main_iteration() — replaced by blocking on the UI
		// courier: wait until a notification arrives, then dispatch
		// (prop_courier_wait_and_dispatch, prop_core.c:5304). While
		// webview work is alive the wait stays bounded so
		// WebpopupCheck keeps pumping the GTK main context.
		if uiCourier != nil {
			if WebpopupActive != nil && WebpopupActive() {
				uiCourier.WaitTimed(30)
			} else {
				uiCourier.Wait()
			}
		}
	}

	if uiCurrent != nil {
		nav := uiCurrent.Stop(aux)
		if nav != nil {
			pm.Destroy(nav) // C: prop_destroy(nav)
		}
	}
}
