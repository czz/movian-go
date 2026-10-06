package navigator

import (
	"fmt"
	"slices"
	"strings"

	"github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

// OpenURL opens a URL in the navigator.
// Note: this drops the view parameter. For initial_url dispatch that
// needs to preserve the view (from -v flag), use OpenURLWithView instead.
func (n *Navigator) OpenURL(url string) {
	if n == nil {
		return
	}
	navOpen0(n, url, "", nil, nil, "", "")
}

// OpenURLWithView opens a URL with a specific view, propagating the view
// to navOpen0. This is the Go equivalent of C's nav_eventsink handling
// EVENT_OPENURL (navigator.c:936-941):
//
//	else if(event_is_type(e, EVENT_OPENURL)) {
//	    ou = (event_openurl_t *)e;
//	    if(ou->url != NULL)
//	        nav_open0(nav, ou->url, ou->view, ...);
//	}
//
// Unlike OpenURL() which drops the view, this method preserves it.
func (ns *NavigatorSystem) OpenURLWithView(url, view string) {
	nav := ns.currentNavigator()
	if nav == nil {
		return
	}
	nav.mutex.Lock()

	nav.currentURL = url
	nav.currentView = view

	entry := HistoryEntry{URL: url, View: view}
	if nav.historyPos < len(nav.history)-1 {
		nav.history = nav.history[:nav.historyPos+1]
	}
	nav.history = append(nav.history, entry)
	nav.historyPos = len(nav.history) - 1
	nav.mutex.Unlock()

	// Direct navOpen0 with view preserved (C: nav_open0(nav, ou->url, ou->view, ...))
	navOpen0(nav, url, view, nil, nil, "", "")
}

// navCloseNoLock closes a navigation page without acquiring lock
// This is used internally when the lock is already held
func navCloseNoLock(np *NavPage, withProp bool) {
	nav := np.nav

	// Unsubscribe
	if np.closeSub != nil {
		nav.propManager.Unsubscribe(np.closeSub)
	}
	if np.directCloseSub != nil {
		nav.propManager.Unsubscribe(np.directCloseSub)
	}
	if np.eventSinkSub != nil {
		nav.propManager.Unsubscribe(np.eventSinkSub)
	}
	if np.bookmarkedSub != nil {
		nav.propManager.Unsubscribe(np.bookmarkedSub)
	}
	if np.titlePathSub != nil {
		np.titlePathSub.Unsubscribe()
	}
	if np.iconPathSub != nil {
		np.iconPathSub.Unsubscribe()
	}

	// Remove from current
	if nav.current == np {
		nav.current = nil
	}

	// Remove from history queue
	for i, p := range nav.historyQueue {
		if p == np {
			nav.historyQueue = slices.Delete(nav.historyQueue, i, i+1)
			break
		}
	}

	// Remove from pages
	for i, p := range nav.pages {
		if p == np {
			nav.pages = slices.Delete(nav.pages, i, i+1)
			break
		}
	}

	// C: prop_unlink(np->np_item_model_dst); prop_unlink(np->np_parent_model_dst);
	// (navigator.c:383-384) — unlink model dst props before RefDec.
	if np.itemModelDst != nil {
		np.itemModelDst.Unlink()
	}
	if np.parentModelDst != nil {
		np.parentModelDst.Unlink()
	}

	// Cleanup properties
	if withProp {
		nav.propManager.Destroy(np.propRoot)
		// Use Locked version — caller holds nav.mutex
		navUpdateCangoLocked(nav)
	}

	// RefDec calls — matching C's nav_close and Go's navClose.
	// These were missing from navCloseNoLock, causing ref leaks
	// in all code paths that use navCloseNoLock (navPageCloseSet,
	// navPageDirectCloseSet, GoBack, navOpen, Fini, navCloseAll).
	if np.itemModelSrc != nil {
		nav.propManager.RefDec(np.itemModelSrc)
	}
	if np.itemModelDst != nil {
		nav.propManager.RefDec(np.itemModelDst)
	}
	if np.parentModelSrc != nil {
		nav.propManager.RefDec(np.parentModelSrc)
	}
	if np.parentModelDst != nil {
		nav.propManager.RefDec(np.parentModelDst)
	}
	if np.bookmarkNotifyProp != nil {
		nav.propManager.RefDec(np.bookmarkNotifyProp)
	}
}

// Spawn creates a new navigation context
func (ns *NavigatorSystem) Spawn(pm *propcore.PropManager) *Navigator {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()

	nav := &Navigator{
		history:       make([]HistoryEntry, 0),
		historyPos:    -1,
		pages:         make([]*NavPage, 0),
		pageQueue:     make([]*NavPage, 0),
		historyQueue:  make([]*NavPage, 0),
		ns:            ns,
		propManager:   pm,
		backendSystem: ns.backendSystem,
		routeSystem:   ns.routeSystem,
		store:         ns.store,
	}

	// C: LIST_INSERT_HEAD(&navigators, nav, nav_link) (navigator.c:243)
	ns.navigators = append(ns.navigators, nav)

	// Create property tree
	// C: nav_create — nav->nav_prop_root = prop_create(all_navigators, NULL)
	if ns.allNavigators != nil {
		nav.propRoot = pm.CreateEx(ns.allNavigators, "", nil, false, false)
	} else {
		nav.propRoot = nav.propManager.CreateRootEx("", false)
	}
	nav.propPages = nav.propManager.CreateEx(nav.propRoot, "pages", nil, false, false)
	nav.propCurPage = nav.propManager.CreateEx(nav.propRoot, "currentpage", nil, false, false)
	nav.propCanGoBack = nav.propManager.CreateEx(nav.propRoot, "canGoBack", nil, false, false)
	nav.propCanGoFwd = nav.propManager.CreateEx(nav.propRoot, "canGoForward", nil, false, false)
	nav.propCanGoHome = nav.propManager.CreateEx(nav.propRoot, "canGoHome", nil, false, false)
	nav.propEventSink = nav.propManager.CreateEx(nav.propRoot, "eventSink", nil, false, false)

	// C: nav_create (navigator.c:252-258) — each navigator subscribes
	// nav_eventsink on its own eventSink prop.
	nav.eventSinkSub = ns.propMgr.SubscribeWithCourier(
		nav.propEventSink, ns.navCourier,
		ns.navEventsinkCallback, nav,
		propcore.SubNoInitialUpdate,
	)

	// Set initial state
	nav.propManager.SetIntEx(nav.propCanGoHome, nil, 1)

	// Open home page
	navOpen0(nav, NavHome, "", nil, nil, "", "")

	// C: nav_spawn (navigator.c:302-307) — prop_select(p) marks this
	// navigator as the current one under navigators.nodes.
	if ns.allNavigators != nil {
		pm.Select(nav.propRoot)
	}

	return nav
}

// OpenFull opens a URL with all 6 context fields, matching C's nav_open0.
// itemModel and parentModel are *propcore.Prop references that get linked
// to the new page's previous.itemModel / previous.parentModel.
// how controls page behavior ("beginning", "resume", "continue", etc.).
// parentURL is the URL of the parent page.
func (ns *NavigatorSystem) OpenFull(url, view string, itemModel, parentModel any, how, parentURL string) {
	nav := ns.currentNavigator()
	if nav == nil {
		return
	}
	nav.mutex.Lock()

	nav.currentURL = url
	nav.currentView = view

	entry := HistoryEntry{URL: url, View: view}
	if nav.historyPos < len(nav.history)-1 {
		nav.history = nav.history[:nav.historyPos+1]
	}
	nav.history = append(nav.history, entry)
	nav.historyPos = len(nav.history) - 1
	nav.mutex.Unlock()

	// If we have full context (itemModel/parentModel), use navOpen0 directly
	// to preserve all 6 fields. Otherwise, fall back to navOpen0 with view
	// preserved (C: nav_open0 always receives the view).
	if itemModel != nil || parentModel != nil || how != "" || parentURL != "" {
		var im, pm *propcore.Prop
		if p, ok := itemModel.(*propcore.Prop); ok {
			im = p
		}
		if p, ok := parentModel.(*propcore.Prop); ok {
			pm = p
		}
		navOpen0(nav, url, view, im, pm, how, parentURL)
	} else {
		// C: nav_open0(nav, url, view, NULL, NULL, "", "") — preserve view.
		navOpen0(nav, url, view, nil, nil, "", "")
	}
}

// OpenError opens an error page
// C: nav_open_error (navigator.c:985-993) — creates model props and
// calls prop_ref_dec(model) before returning.
func OpenError(pm *propcore.PropManager, root *propcore.Prop, msg string) error {
	if root == nil {
		return fmt.Errorf("root is nil")
	}

	// Match C's nav_open_error: create model.type, model.loading, model.error
	// and set directClose=1 so the page is closed on back
	// C: prop_t *model = prop_create_r(root, "model") — prop_create_r does
	// prop_create0 (refcount=1) + prop_ref_inc (refcount=2), so the caller
	// holds an extra ref that must be released with prop_ref_dec.
	modelProp := pm.CreateEx(root, "model", nil, false, false)
	pm.RefInc(modelProp) // C: prop_create_r extra ref (refcount=2)
	typeProp := pm.CreateEx(modelProp, "type", nil, false, false)
	pm.SetStringEx(typeProp, nil, "openerror", propcore.StringUTF8)
	loadingProp := pm.CreateEx(modelProp, "loading", nil, false, false)
	pm.SetIntEx(loadingProp, nil, 0)
	errorProp := pm.CreateEx(modelProp, "error", nil, false, false)
	pm.SetStringEx(errorProp, nil, msg, propcore.StringUTF8)

	// Set directClose=1 (matching C's nav_open_error)
	dcProp := pm.CreateEx(root, "directClose", nil, false, false)
	pm.SetIntEx(dcProp, nil, 1)

	// C: prop_ref_dec(model) — release the extra ref from prop_create_r.
	pm.RefDec(modelProp)

	return nil
}

// OpenErrorf opens an error page with formatted message
func OpenErrorf(pm *propcore.PropManager, root *propcore.Prop, format string, args ...any) error {
	if root == nil {
		return fmt.Errorf("root is nil")
	}
	msg := fmt.Sprintf(format, args...)
	return OpenError(pm, root, msg)
}

// Redirect redirects a page to a new URL.
//
// C: nav_redirect(root, url) creates EVENT_REDIRECT with string payload
// and sends it to root's "eventSink" prop via prop_send_ext_event.
// The page_eventsink subscriber receives PROP_EXT_EVENT, checks
// event_is_type(e, EVENT_REDIRECT), and calls page_redirect(np, url).
//
// Go: We send EventExtEvent to the eventSink prop with the URL as payload.
// pageEventsink checks for this and calls pageRedirect.
func Redirect(pm *propcore.PropManager, root *propcore.Prop, url string) {
	if root == nil {
		return
	}

	// Find or create the eventSink child prop
	// C: prop_t *p = prop_create_r(root, "eventSink");
	eventSink := root.FindChild("eventSink")
	if eventSink == nil {
		eventSink = pm.CreateEx(root, "eventSink", nil, false, false)
	}

	// C: event_t *e = event_create_str(EVENT_REDIRECT, url);
	//    prop_send_ext_event(p, e); prop_ref_dec(p); event_release(e);
	e := &event.EventPayload{
		Event: event.Event{
			Type:     event.EVENT_REDIRECT,
			RefCount: 1,
			Payload:  url,
		},
	}
	e.Event.SetConcrete(e)
	pm.SendExtEvent(eventSink, e.AsEvent())
	e.AsEvent().Release()
}

// navOpen0 opens a URL with specific parameters
func navOpen0(nav *Navigator, url string, view string, itemModel *propcore.Prop, parentModel *propcore.Prop, how string, parentURL string) {
	// Don't lock mutex here - let internal functions handle their own locking

	// Create new page
	// Note: itemModelDst, parentModelDst, and bookmarked are NOT created
	// here as roots. They are created in navPageSetupProp as children of
	// prevProp (matching C's prop_create_r with incref=1). Creating them
	// here as roots would leak them (they get overwritten in
	// navPageSetupProp) and cause refcount panics in navCloseNoLock.
	np := &NavPage{
		nav:            nav,
		url:            url,
		parentURL:      parentURL,
		how:            how,
		directClose:    false,
		itemModelSrc:   itemModel,
		parentModelSrc: parentModel,
	}

	if itemModel != nil {
		nav.propManager.RefInc(itemModel)
	}
	if parentModel != nil {
		nav.propManager.RefInc(parentModel)
	}

	nav.mutex.Lock()
	nav.pages = append(nav.pages, np)
	nav.mutex.Unlock()

	navPageSetupProp(np, view)
	navInsertPage(nav, np, itemModel)
	navOpenBackend(np)
}

// navCloseAll closes all pages in a navigator
// Caller must NOT hold nav.mutex — this function acquires it.
// Uses navCloseNoLock internally since we already hold nav.mutex
// (matching C's nav_close_all which calls nav_close without locking,
// assuming the caller holds the lock via PROP_TAG_MUTEX).
// navCloseAll closes all pages in a navigator (test/debug seam).
// C: nav_close_all (navigator.c) — locked wrapper over the canonical body.
func navCloseAll(nav *Navigator, withProp bool) {
	nav.mutex.Lock()
	defer nav.mutex.Unlock()
	navCloseAllLocked(nav, withProp)
}

// navCloseAllLocked — C: nav_close_all body. Caller holds nav.mutex.
func navCloseAllLocked(nav *Navigator, withProp bool) {
	for len(nav.pages) > 0 {
		np := nav.pages[len(nav.pages)-1]
		nav.pages = nav.pages[:len(nav.pages)-1]
		navCloseNoLock(np, withProp)
	}
}

// navOpenBackend opens the backend for a navigation page
func navOpenBackend(np *NavPage) {
	// Create auxiliary data for backend opening
	noba := &navOpenBackendAux{
		propRoot:    np.nav.propManager.RefInc(np.propRoot),
		url:         np.url,
		pm:          np.nav.propManager,
		bs:          np.nav.backendSystem,
		routeSystem: np.nav.routeSystem,
	}

	// C: nav_open_backend creates a detached thread. For trivial backends
	// (page: URLs), backend_page_open is so fast it completes before the
	// next frame. For Go, the goroutine scheduler may not run it before
	// Render(), causing model.type to be empty on the first frame.
	// Fix: for page: URLs, call bs.Open directly (synchronous) to match
	// C's observable behavior where model.type is set before the first frame.
	// We call bs.Open directly rather than navOpenThread to avoid the
	// error-handling path (OpenError → prop notification → deadlock)
	// when called from within a prop notification callback (redirects).
	if strings.HasPrefix(np.url, "page:") && noba.bs != nil {
		if err := noba.bs.Open(noba.propRoot, noba.url, false); err == nil {
			if noba.propRoot != nil {
				noba.pm.RefDec(noba.propRoot)
			}
			return
		}
		// If bs.Open fails, fall through to async path
	}

	// Start backend opening in a goroutine for non-trivial backends
	go navOpenThread(noba)
}

// navOpenBackendAux holds auxiliary data for backend opening
type navOpenBackendAux struct {
	propRoot    *propcore.Prop
	url         string
	pm          *propcore.PropManager
	bs          *core.BackendSystem
	routeSystem *RouteSystem
}

// navOpenThread opens the backend in a goroutine
func navOpenThread(noba *navOpenBackendAux) {
	defer func() {
		if noba.propRoot != nil {
			noba.pm.RefDec(noba.propRoot)
		}
	}()

	if noba.propRoot == nil {
		return
	}

	// Try backend system first (like C's backend_open)
	bs := noba.bs
	if bs != nil {
		err := bs.Open(noba.propRoot, noba.url, false)
		if err == nil {
			return
		}
	}

	// Try JS routes (like C's be_ecmascript backend)
	if noba.routeSystem != nil {
		if noba.routeSystem.OpenURL(noba.propRoot, noba.url) {
			// C's backend_prop.c sets directClose=1 for property-based backends.
			// Route-based pages (movie:, music:, photo:, plugin: etc.) should be
			// closed when navigating back, matching C behavior.
			dcProp := noba.pm.CreateEx(noba.propRoot, "directClose", nil, false, false)
			noba.pm.SetIntEx(dcProp, nil, 1)
			return
		}
	}

	// No handler found — show error page (like C's nav_open_errorf)
	// OpenError sets directClose=1 so the error page is closed on back
	OpenError(noba.pm, noba.propRoot, "No handler for URL")
}
