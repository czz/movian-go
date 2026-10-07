package navigator

import (
	"sync"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/mathutil"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
)

// navSelect selects a navigation page.
// C: nav_select(nav, np, item_model)
//  1. prop_link(np->np_prop_root, nav->nav_prop_curpage)
//  2. prop_select_ex(np->np_prop_root, item_model, NULL)
//  3. nav->nav_page_current = np
//  4. nav_update_cango(nav)
//
// Note: C uses prop_select_ex (not prop_link) for the second call.
// prop_select_ex fires PROP_SELECT_CHILD to the parent's subscribers
// and sets parent->hp_selected. This is different from prop_link
// which establishes a value mirror.
func navSelect(nav *Navigator, np *NavPage, itemModel *propcore.Prop) {
	nav.propManager.Link(np.propRoot, nav.propCurPage, nil, false, false)
	// C: prop_select_ex(np->np_prop_root, item_model, NULL)
	// C always calls prop_select_ex, even when item_model is NULL.
	// With NULL extra, it still:
	//   1. Notifies parent's subscribers of PROP_SELECT_CHILD
	//   2. Sets parent->hp_selected = p
	// Go: SelectChildProp handles nil extra correctly.
	nav.propManager.SelectChildProp(np.propRoot, itemModel)
	nav.current = np
	// Update currentURL to reflect the selected page's URL.
	// C doesn't have a currentURL field (reads from prop), but Go's
	// GetCurrentURL() reads this field, so it must stay in sync.
	nav.currentURL = np.url
	// Note: currentView is NOT reset here. It is set by Open/OpenFull
	// when a page is opened with a specific view. navSelect is called
	// during page opening (navInsertPage) and during GoBack. In both
	// cases, currentView was already set by the caller.
	// Use Locked version — callers must hold nav.mutex
	navUpdateCangoLocked(nav)
}

// navInsertPage inserts a page into the navigator
func navInsertPage(nav *Navigator, np *NavPage, itemModel *propcore.Prop) {
	// Acquire lock for the entire insert+select operation
	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	// Set parent (matching C's prop_set_parent(np->np_prop_root, nav->nav_prop_pages))
	// C: if(prop_set_parent(np->np_prop_root, nav->nav_prop_pages)) abort();
	// (navigator.c:456-491) — abort if SetParentEx fails.
	if nav.propManager.SetParentEx(np.propRoot, nav.propPages, nil, "") != 0 {
		panic("navInsertPage: SetParentEx failed (C: abort())")
	}

	// Remove future history if current exists
	// C: while((np2 = TAILQ_NEXT(nav->nav_page_current, np_history_link)) != NULL)
	//        nav_close(np2, 1);
	// nav_close removes np2 from the queue itself, so this drains
	// everything after current — one successor at a time.
	if nav.current != nil {
		for {
			next := -1
			for i, p := range nav.historyQueue {
				if p == nav.current {
					next = i + 1
					break
				}
			}
			if next < 0 || next >= len(nav.historyQueue) {
				break
			}
			navCloseNoLock(nav.historyQueue[next], true)
		}
	}

	// Add to history
	nav.historyQueue = append(nav.historyQueue, np)

	navSelect(nav, np, itemModel)

	// Kill off pages that's just previous to this page in history
	// with the same URI. This makes sure the user don't end up with
	// tons of duplicates if holding down some key.
	// (matching C's: while((np2 = TAILQ_PREV(np, ...)) != NULL && !strcmp(np2->np_url, np->np_url)) nav_close(np2, 1))
	for {
		idx := -1
		for i, p := range nav.historyQueue {
			if p == np {
				if i > 0 {
					idx = i - 1
				}
				break
			}
		}
		if idx < 0 {
			break
		}
		if nav.historyQueue[idx].url != np.url {
			break
		}
		// Close the duplicate previous page
		dup := nav.historyQueue[idx]
		navCloseNoLock(dup, true)
	}
}

// navPageSetupProp sets up the property tree for a navigation page
func navPageSetupProp(np *NavPage, view string) {
	np.propRoot = np.nav.propManager.CreateRootEx("", false)

	// C: kv_prop_bind_create(prop_create(np->np_prop_root, "persistent"), np->np_url)
	persistentProp := np.nav.propManager.CreateEx(np.propRoot, "persistent", nil, false, false)
	np.nav.ns.kvstore.PropBindCreate(persistentProp, np.url)

	// Create previous property for parent/item models
	prevProp := np.nav.propManager.CreateEx(np.propRoot, "previous", nil, false, false)

	if np.parentModelSrc != nil {
		// C: prop_create_r(prev, "parentModel") → prop_create_ex(..., incref=1)
		// prop_create_r creates the prop with an extra ref (refCount=2)
		// that nav_close balances with prop_ref_dec. Go's CreateEx always
		// sets refCount=1, so we add a RefInc to match C's incref=1.
		np.parentModelDst = np.nav.propManager.CreateEx(prevProp, "parentModel", nil, false, false)
		np.nav.propManager.RefInc(np.parentModelDst)
		np.nav.propManager.Link(np.parentModelSrc, np.parentModelDst, nil, true, false)
	}

	if np.itemModelSrc != nil {
		// C: prop_create_r(prev, "itemModel") → prop_create_ex(..., incref=1)
		np.itemModelDst = np.nav.propManager.CreateEx(prevProp, "itemModel", nil, false, false)
		np.nav.propManager.RefInc(np.itemModelDst)
		np.nav.propManager.Link(np.itemModelSrc, np.itemModelDst, nil, true, false)
	}

	// Set requested view
	if view != "" {
		viewProp := np.nav.propManager.CreateEx(np.propRoot, "requestedView", nil, false, false)
		np.nav.propManager.SetStringEx(viewProp, nil, view, propcore.StringUTF8)
	}

	// Set how (navigation method)
	howProp := np.nav.propManager.CreateEx(np.propRoot, "how", nil, false, false)
	np.nav.propManager.SetStringEx(howProp, nil, np.how, propcore.StringUTF8)

	// Set URL and parent URL
	urlProp := np.nav.propManager.CreateEx(np.propRoot, "url", nil, false, false)
	np.nav.propManager.SetStringEx(urlProp, nil, np.url, propcore.StringUTF8)
	parentUrlProp := np.nav.propManager.CreateEx(np.propRoot, "parentUrl", nil, false, false)
	np.nav.propManager.SetStringEx(parentUrlProp, nil, np.parentURL, propcore.StringUTF8)

	// Create close, directClose, eventSink, bookmarked properties (matching C)
	closeProp := np.nav.propManager.CreateEx(np.propRoot, "close", nil, false, false)
	directCloseProp := np.nav.propManager.CreateEx(np.propRoot, "directClose", nil, false, false)
	eventSinkProp := np.nav.propManager.CreateEx(np.propRoot, "eventSink", nil, false, false)
	bookmarkedProp := np.nav.propManager.CreateEx(np.propRoot, "bookmarked", nil, false, false)
	np.bookmarked = bookmarkedProp
	// C: prop_set_int(np->np_bookmarked, nav_page_is_bookmarked(np)) (navigator.c:688)
	// Go: set bookmarked from navPageIsBookmarked, matching C.
	bookmarkedVal := 0
	if np.nav.navPageIsBookmarked(np) {
		bookmarkedVal = 1
	}
	np.nav.propManager.SetIntEx(bookmarkedProp, nil, bookmarkedVal)

	// Subscribe to close event — MUST subscribe to the close child property,
	// NOT to propRoot itself (matching C's PROP_TAG_NAME("page", "close"))
	np.closeSub = closeProp.Subscribe(navPageCloseSet, np, 0,
		propcore.SubMutex{Ptr: &np.nav.mutex})

	// Subscribe to direct close event
	// C: nav_page_direct_close_set uses PROP_SUB_NO_INITIAL_UPDATE
	// (navigator.c:668-674). The initial value is ignored; only changes matter.
	np.directCloseSub = directCloseProp.Subscribe(navPageDirectCloseSet, np,
		propcore.SubNoInitialUpdate, propcore.SubMutex{Ptr: &np.nav.mutex})

	// Subscribe to event sink
	np.eventSinkSub = eventSinkProp.Subscribe(pageEventsink, np, 0,
		propcore.SubMutex{Ptr: &np.nav.mutex})

	// C: #if ENABLE_BOOKMARKS — bookmarked, title, icon subscriptions
	// C: navigator.c:685-712

	// Subscribe to bookmarked prop (C: PROP_TAG_CALLBACK_INT, nav_page_bookmarked_set)
	// C: PROP_SUB_NO_INITIAL_UPDATE | PROP_SUB_IGNORE_VOID
	np.bookmarkedSub = bookmarkedProp.Subscribe(navPageBookmarkedSet, np,
		propcore.SubNoInitialUpdate, propcore.SubFlagIgnoreVoid,
		propcore.SubMutex{Ptr: &np.nav.mutex})

	// Subscribe to page.model.metadata.title (C: PROP_TAG_CALLBACK_RSTR, nav_page_title_set)
	// C: PROP_TAG_NAME("page", "model", "metadata", "title") — a dynamic
	// name-vector subscription: the leaf is re-resolved when the backend
	// creates/replaces "model"/"metadata" (page reload, model swap). A
	// static leaf subscribe would silently die on the old prop.
	np.titlePathSub = newNavPathSub(np.nav.propManager, np.propRoot,
		[]string{"model", "metadata", "title"},
		func(ev propcore.EventType, args ...any) {
			navPageTitleSet(np, ev, args...)
		}, &np.nav.mutex)

	// Subscribe to page.model.metadata.logo (C: PROP_TAG_CALLBACK_RSTR, nav_page_icon_set)
	// C: PROP_TAG_NAME("page", "model", "metadata", "logo")
	np.iconPathSub = newNavPathSub(np.nav.propManager, np.propRoot,
		[]string{"model", "metadata", "logo"},
		func(ev propcore.EventType, args ...any) {
			navPageIconSet(np, ev, args...)
		}, &np.nav.mutex)
}

// navPathSub implements C's PROP_TAG_NAME vector subscription for the
// navigator: watches each path level for the child appearing/disappearing
// (EventAddChild/EventDelChild) and attaches a leaf subscription that
// forwards value events (and EventSetVoid on leaf loss) to cb, all under
// the given mutex — the Go equivalent of the C name-vector resolution +
// PROP_TAG_MUTEX dispatch.
type navPathSub struct {
	pm    *propcore.PropManager
	path  []string
	cb    func(propcore.EventType, ...any)
	mutex *sync.Mutex
	arms  []*propcore.Subscription // per-level watch subs
	armed []*propcore.Prop         // resolved node per level
	leaf  *propcore.Subscription   // leaf value sub
	dead  bool
}

// newNavPathSub — C: prop_subscribe(PROP_TAG_NAMED_ROOT, root, "page",
// PROP_TAG_NAME_VECTOR, path, ...). The "page" named root resolves to
// root itself, so path selects children under root.
func newNavPathSub(pm *propcore.PropManager, root *propcore.Prop, path []string,
	cb func(propcore.EventType, ...any), mutex *sync.Mutex) *navPathSub {
	s := &navPathSub{pm: pm, path: path, cb: cb, mutex: mutex}
	s.arm(root, 0)
	return s
}

// arm attaches the watch (or leaf sub) for path[depth] under parent.
func (s *navPathSub) arm(parent *propcore.Prop, depth int) {
	if s.dead || parent == nil {
		return
	}
	if depth == len(s.path) {
		// Terminal: subscribe to the leaf prop's value.
		s.leaf = parent.Subscribe(func(_ any, ev propcore.EventType, args ...any) {
			s.cb(ev, args...)
		}, nil, propcore.SubMutex{Ptr: s.mutex})
		return
	}
	name := s.path[depth]
	idx := depth
	s.arms = append(s.arms, nil)
	s.armed = append(s.armed, nil)
	s.arms[idx] = parent.Subscribe(func(_ any, ev propcore.EventType, args ...any) {
		if len(args) == 0 {
			return
		}
		if ev == propcore.EventAddChildVector || ev == propcore.EventAddChildVectorDirect {
			// Initial-update bulk delivery: last arg is []*Prop.
			if kids, ok := args[len(args)-1].([]*propcore.Prop); ok {
				for _, child := range kids {
					if child != nil && child.GetName() == name {
						s.resolve(idx, child)
					}
				}
			}
			return
		}
		child, ok := args[0].(*propcore.Prop)
		if !ok || child == nil || child.GetName() != name {
			return
		}
		switch ev {
		case propcore.EventAddChild, propcore.EventAddChildBefore:
			s.resolve(idx, child)
		case propcore.EventDelChild:
			s.detach(idx + 1)
			// C: leaf loss delivers PROP_SET_VOID to the subscription.
			s.cb(propcore.EventSetVoid)
		}
	}, nil, propcore.SubMutex{Ptr: s.mutex})
	// Resolve an already-existing child.
	if c := parent.GetChild(name); c != nil {
		s.resolve(idx, c)
	}
}

// resolve arms level idx+1 under node unless already armed there.
func (s *navPathSub) resolve(idx int, node *propcore.Prop) {
	if s.dead || idx < len(s.armed) && s.armed[idx] == node {
		return
	}
	if idx < len(s.armed) && s.armed[idx] != nil {
		s.detach(idx + 1)
	}
	s.armed[idx] = node
	s.arm(node, idx+1)
}

// detach drops all subscriptions from level depth downward.
func (s *navPathSub) detach(depth int) {
	if s.leaf != nil {
		s.leaf.Unsubscribe()
		s.leaf = nil
	}
	for i := depth; i < len(s.arms); i++ {
		if s.arms[i] != nil {
			s.arms[i].Unsubscribe()
		}
	}
	s.arms = s.arms[:depth]
	s.armed = s.armed[:depth]
}

// Unsubscribe releases every level's subscription.
func (s *navPathSub) Unsubscribe() {
	s.dead = true
	s.detach(0)
}

// navPageCloseSet callback for page close event
// C: nav_page_close_set — value arrives through trampoline_int, so
// SET_FLOAT/SET_STRING events count the same as SET_INT; void → 0.
func navPageCloseSet(opaque any, event propcore.EventType, args ...any) {
	np, ok := opaque.(*NavPage)
	if !ok {
		return
	}
	// C: if(!value) return;
	v, deliver := propcore.TrampolineInt(event, args, false)
	if !deliver || v == 0 {
		return
	}
	// C: nav_mutex held by the dispatcher (PROP_TAG_MUTEX)
	nav := np.nav
	if nav.current == np {
		// Select previous page
		for i, p := range nav.historyQueue {
			if p == np && i > 0 {
				navSelect(nav, nav.historyQueue[i-1], nil)
				break
			}
		}
	}
	navCloseNoLock(np, true)
}

// navPageDirectCloseSet callback for direct close event
// C: nav_page_direct_close_set — called with nav_mutex held (via PROP_TAG_MUTEX).
// C receives the value through trampoline_int, which converts any
// SET_INT/SET_FLOAT/SET_STRING event to int (and void to 0) — a float
// "directClose = 1" must set np_direct_close just like an int.
func navPageDirectCloseSet(opaque any, event propcore.EventType, args ...any) {
	np, ok := opaque.(*NavPage)
	if !ok {
		return
	}
	// C: np->np_direct_close = v; if(!v) return;
	v, deliver := propcore.TrampolineInt(event, args, false)
	if !deliver {
		return
	}
	np.directClose = v != 0
	if v == 0 {
		return
	}

	// C: nav_mutex held by the dispatcher (PROP_TAG_MUTEX)
	nav := np.nav

	// If this page is "to the right" in the future stack, close it
	scan := nav.current
	if scan == nil {
		return
	}

	// Find current page in history queue
	currentIdx := -1
	for i, p := range nav.historyQueue {
		if p == scan {
			currentIdx = i
			break
		}
	}

	if currentIdx >= 0 {
		// Check if np is in the future (to the right)
		for i := currentIdx + 1; i < len(nav.historyQueue); i++ {
			if nav.historyQueue[i] == np {
				navCloseNoLock(np, true)
				return
			}
		}
	}
}

// pageEventsink callback for page event sink.
//
// C: page_eventsink (navigator.c:614-624) is registered with
// PROP_TAG_MUTEX, &nav_mutex, so the callback is invoked with nav_mutex
// held. This serializes redirect/reload with other navigator operations.
//
// Go: We acquire nav.mutex before calling pageRedirect/navReloadPage
// to match C's concurrency model and prevent data races on np fields
// (closeSub, propRoot, url, etc.).
func pageEventsink(opaque any, evtType propcore.EventType, args ...any) {
	np, ok := opaque.(*NavPage)
	if !ok {
		return
	}
	nav := np.nav
	if nav == nil {
		return
	}

	// Handle PROP_EXT_EVENT — C's prop_send_ext_event delivers events here.
	// C: page_eventsink (navigator.c:614-624) only handles
	// ACTION_RELOAD_DATA and EVENT_REDIRECT. All other events are
	// silently ignored — they must NOT be forwarded to the event manager.
	if evtType == propcore.EventExtEvent && len(args) > 0 {
		if evt, ok := args[0].(*event.Event); ok {
			switch {
			case evt.Type == event.EVENT_REDIRECT:
				// C: event_is_type(e, EVENT_REDIRECT) →
				//    page_redirect(np, ((event_payload_t *)e)->payload)
				pageRedirect(np, evt.Payload)
			case evt.IsAction(event.ACTION_RELOAD_DATA):
				navReloadPage(np)
			}
			return
		}
		// Check for reload notification (Go NotifySimple path)
		if name, ok := args[0].(string); ok && name == "reload" {
			navReloadPage(np)
			return
		}
	}
}

// pageUnsub unsubscribes from all page subscriptions
func pageUnsub(np *NavPage) {
	if np.closeSub != nil {
		np.closeSub.Unsubscribe()
		np.closeSub = nil
	}
	if np.directCloseSub != nil {
		np.directCloseSub.Unsubscribe()
		np.directCloseSub = nil
	}
	if np.eventSinkSub != nil {
		np.eventSinkSub.Unsubscribe()
		np.eventSinkSub = nil
	}
	if np.bookmarkedSub != nil {
		np.bookmarkedSub.Unsubscribe()
		np.bookmarkedSub = nil
	}
	if np.titlePathSub != nil {
		np.titlePathSub.Unsubscribe()
		np.titlePathSub = nil
	}
	if np.iconPathSub != nil {
		np.iconPathSub.Unsubscribe()
		np.iconPathSub = nil
	}
}

// pageRedirect redirects a page to a new URL
func pageRedirect(np *NavPage, url string) {
	nav := np.nav

	np.nav.ns.ts.Debug("NAVIGATOR", "Following redirect to %s\n", url)

	// If this is the current page, unlink it
	if nav.current == np {
		nav.propCurPage.Unlink()
	}

	// Save the old property root
	oldPropRoot := np.propRoot

	// Unsubscribe from events
	pageUnsub(np)

	// Destroy all children of the property root
	nav.propManager.DestroyChilds(np.propRoot)

	// Update the URL
	np.url = url

	// Re-setup the property tree
	navPageSetupProp(np, "")

	// Set the parent of the new property root
	// prop_set_parent_ex returns 0 on success, 1 on error
	if nav.propManager.SetParentEx(np.propRoot, nav.propPages, oldPropRoot, "") != 0 {
		// nav_prop_pages is a zombie, this is an error
		panic("nav_prop_pages is a zombie")
	}

	// If this is the current page, select it
	if nav.current == np {
		navSelect(nav, np, nil)
	}

	// Destroy the old property root
	nav.propManager.Destroy(oldPropRoot)

	// Open the backend for the new URL
	navOpenBackend(np)
}

// navPageIsBookmarked checks if a page is bookmarked
func (nav *Navigator) navPageIsBookmarked(np *NavPage) bool {
	for _, bm := range nav.bookmarks {
		if bm.url == np.url {
			return true
		}
	}
	return false
}

// navUpdateBookmarked updates bookmarked status for all pages in this navigator.
// C: nav_update_bookmarked (navigator.c:216-227) iterates ALL navigators.
// Use NavigatorSystem.UpdateAllBookmarks for the C-canonical global update.
func (nav *Navigator) navUpdateBookmarked() {
	// Update bookmarked property for all pages in this navigator
	// C: prop_set_int_ex(np->np_bookmarked, np->np_bookmarked_sub, ...) —
	// the page's own subscription is the skipme so the write does not
	// re-fire nav_page_bookmarked_set (navigator.c:220-224).
	for _, np := range nav.pages {
		if np.bookmarked != nil {
			nav.propManager.SetIntEx(np.bookmarked, np.bookmarkedSub,
				mathutil.BoolToInt(nav.navPageIsBookmarked(np)))
		}
	}
}

// navPageBookmarkedSet callback for bookmark toggle
func navPageBookmarkedSet(opaque any, event propcore.EventType, args ...any) {
	np, ok := opaque.(*NavPage)
	if !ok {
		return
	}
	// C: value arrives through trampoline_int; the subscription has
	// PROP_SUB_IGNORE_VOID so non-value events are not delivered.
	value, deliver := propcore.TrampolineInt(event, args, true)
	if !deliver {
		return
	}
	// C: prop_t *p = NULL — notification prop retained per page
	var p *propcore.Prop
	if value != 0 {
		// C: if(nav_page_is_bookmarked(np)) return;
		if np.nav.navPageIsBookmarked(np) {
			return
		}

		title := np.title
		if title == "" {
			title = "<no title>"
		}

		// C: p = notify_add(NULL, NOTIFY_INFO, NULL, -3,
		//     _("Added %s to home page"), title)
		if np.nav.ns.notificationMgr != nil {
			p, _ = np.nav.ns.notificationMgr.NotifyAdd(nil, notifications.NotifyInfo, "", -3,
				"Added %s to home page", title).(*propcore.Prop)
		}
		// C: bookmark_add(title, np->np_url, "other", rstr_get(np->np_icon), NULL)
		np.nav.bookmarkAdd(title, np.url, "other", np.icon, "", true)
	} else {
		// C: LIST_FOREACH(bm, &bookmarks, bm_link) { if url matches:
		//      prop_ref_dec(p); p = notify_add("Removed %s from homepage");
		//      prop_destroy(bm->bm_root); }
		// Removal from nav.bookmarks happens in bookmarkDestroyed via the
		// TRACK_DESTROY subscription (C: prop_destroy → async PROP_DESTROYED
		// → bookmark_destroyed does LIST_REMOVE).
		for _, bm := range np.nav.bookmarks {
			if bm.url == np.url {
				if p != nil {
					np.nav.propManager.RefDec(p)
				}
				if np.nav.ns.notificationMgr != nil {
					p, _ = np.nav.ns.notificationMgr.NotifyAdd(nil, notifications.NotifyInfo, "", -3,
						"Removed %s from homepage", bm.title).(*propcore.Prop)
				}
				np.nav.propManager.Destroy(bm.root)
			}
		}
	}
	// C: bookmarks_save()
	np.nav.bookmarksSave()

	// C: prop_destroy(np->np_bookmark_notify_prop);
	//    prop_ref_dec(np->np_bookmark_notify_prop);
	//    np->np_bookmark_notify_prop = p;
	if np.bookmarkNotifyProp != nil {
		np.nav.propManager.Destroy(np.bookmarkNotifyProp)
		np.nav.propManager.RefDec(np.bookmarkNotifyProp)
	}
	np.bookmarkNotifyProp = p
}

// navPageTitleSet callback for title change
func navPageTitleSet(opaque any, event propcore.EventType, args ...any) {
	np, ok := opaque.(*NavPage)
	if !ok {
		return
	}
	// C: rstr cb — PROP_SET_VOID delivers NULL → np_title cleared.
	if event == propcore.EventSetVoid {
		np.title = ""
		return
	}
	if len(args) > 0 {
		if str, ok := args[0].(string); ok {
			np.title = str
		}
	}
}

// navPageIconSet callback for icon change
// C: nav_page_icon_set (navigator.c:602-606) — rstr_set(&np->np_icon, str)
func navPageIconSet(opaque any, event propcore.EventType, args ...any) {
	np, ok := opaque.(*NavPage)
	if !ok {
		return
	}
	if event == propcore.EventSetVoid {
		np.icon = ""
		return
	}
	if len(args) > 0 {
		if str, ok := args[0].(string); ok {
			np.icon = str
		}
	}
}

// reloadCurrentPage reloads the current page's data.
//
// C-canonical: nav_reload_current (navigator.c:862-874) calls
// nav_reload_page(np) directly — NOT through the page's eventSink.
func (ns *NavigatorSystem) reloadCurrentPage() {
	nav := ns.currentNavigator()
	if nav == nil {
		return
	}
	navReloadCurrent(nav)
}

// navReloadCurrent reloads the navigator's current page.
// C: nav_reload_current (navigator.c:862-874).
// Caller must NOT hold nav.mutex.
func navReloadCurrent(nav *Navigator) {
	nav.mutex.Lock()
	defer nav.mutex.Unlock()

	np := nav.current
	if np == nil {
		return
	}

	// C: nav_reload_page(np) — called directly.
	// navReloadPage requires nav.mutex to be held.
	navReloadPage(np)
}

// navReloadPage reloads a page by destroying its prop tree and re-opening
// the backend.
//
// C: nav_reload_page (navigator.c:838-857):
//  1. page_unsub(np) — unsubscribe all subscriptions
//  2. prop_destroy(np->np_prop_root) — destroy prop root
//  3. mystrset(&np->np_how, "continue") — set how to "continue"
//  4. nav_page_setup_prop(np, NULL) — re-setup prop tree
//  5. prop_set_parent(np->np_prop_root, nav->nav_prop_pages) — re-parent
//  6. nav_select(nav, np, NULL) — re-select
//  7. nav_open_backend(np) — re-open backend
func navReloadPage(np *NavPage) {
	nav := np.nav
	if nav == nil {
		return
	}

	np.nav.ns.ts.Debug("NAVIGATOR", "Reloading %s", np.url)

	// 1. Unsubscribe all subscriptions
	pageUnsub(np)

	// 2. Destroy the prop root (C: prop_destroy(np->np_prop_root))
	//    This destroys all children and the prop itself
	nav.propManager.Destroy(np.propRoot)

	// 3. Set how to "continue" (preserves navigation state)
	np.how = "continue"

	// 4. Re-setup the prop tree (creates new propRoot)
	navPageSetupProp(np, "")

	// 5. Re-parent to nav.propPages
	if nav.propManager.SetParentEx(np.propRoot, nav.propPages, nil, "") != 0 {
		panic("nav_prop_pages is a zombie during reload")
	}

	// 6. Re-select the page
	// C: nav_select(nav, np, NULL) — always called, no condition
	navSelect(nav, np, nil)

	// 7. Re-open the backend
	navOpenBackend(np)
}
