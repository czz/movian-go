package navigator

import (
	"slices"
	"sync"

	"github.com/czz/movian-go/internal/trace"

	"github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// GetEventManager returns the shared EventManager.
// C has a single global event system; Go uses a single EventManager
// instance passed in via StartNavigator.
func (ns *NavigatorSystem) GetEventManager() *event.EventManager {
	if ns == nil {
		return nil
	}
	return ns.eventMgr
}

// NewNavigatorSystem creates a new navigator system.
func NewNavigatorSystem(pm *propcore.PropManager, ss *service.ServiceSystem) *NavigatorSystem {
	ns := &NavigatorSystem{
		propMgr:       pm,
		serviceSystem: ss,
		routeSystem:   NewRouteSystem(),
	}
	ns.canStartCond = sync.NewCond(&ns.canStartMu)
	return ns
}

// SetTraceSystem injects the trace system (C: trace() global).
func (ns *NavigatorSystem) SetTraceSystem(ts *trace.TraceSystem) {
	ns.ts = ts
	ns.routeSystem.ts = ts
}

// SetStore injects the htsmsg store (C: global htsmsg_store —
// bookmarks persistence). Must run before navigators are created.
func (ns *NavigatorSystem) SetStore(st *htsmsg.Store) { ns.store = st }

// SetKVStore injects the kvstore (C: global kvstore_* — nav persistent prop).
func (ns *NavigatorSystem) SetKVStore(kvs *kvstore.KVStore) { ns.kvstore = kvs }

// SetNotificationManager injects the notification manager (C: notify_add()
// global) used by bookmark add/remove and delete-confirmation popups.
func (ns *NavigatorSystem) SetNotificationManager(nm *notifications.NotificationManager) {
	ns.notificationMgr = nm
}

// SetCanStart signals that the navigator can start processing initial URLs.
// Mirrors C's navigator_can_start() (main.c:209-214):
//
//	static void navigator_can_start(void) {
//	    hts_mutex_lock(&gconf.state_mutex);
//	    gconf.navigator_can_start = 1;
//	    hts_cond_broadcast(&gconf.state_cond);
//	    hts_mutex_unlock(&gconf.state_mutex);
//	}
//
// In C, this is called from swthread after plugins_load_all() completes
// (main.c:271/277/287). In Go, it should be called after plugin initialization.
func (ns *NavigatorSystem) SetCanStart() {
	ns.canStartMu.Lock()
	defer ns.canStartMu.Unlock()
	ns.navigatorCanStart = true
	ns.canStartCond.Broadcast()
}

// DispatchInitialURL dispatches the initial URL as an openurl event,
// blocking until SetCanStart() has been called.
// Mirrors C's nav_create (navigator.c:278-291):
//
//	if(atomic_add_and_fetch(&initial_opened, 1) == 1 &&
//	   gconf.initial_url != NULL) {
//	    hts_mutex_lock(&gconf.state_mutex);
//	    while(gconf.navigator_can_start == 0)
//	        hts_cond_wait(&gconf.state_cond, &gconf.state_mutex);
//	    hts_mutex_unlock(&gconf.state_mutex);
//	    event_t *e = event_create_openurl(.url = gconf.initial_url, .view = gconf.initial_view);
//	    prop_send_ext_event(eventsink, e);
//	}
//
// The URL and view are used as-is (raw strings), matching C semantics.
// This dispatches via the navigator's event sink path (OpenURLWithView),
// which is the Go equivalent of prop_send_ext_event → nav_eventsink → nav_open0.
func (ns *NavigatorSystem) DispatchInitialURL(url, view string) {
	if url == "" {
		return
	}

	// Gate: wait for navigator_can_start (C: navigator.c:282-284)
	ns.canStartMu.Lock()
	for !ns.navigatorCanStart {
		ns.canStartCond.Wait()
	}
	ns.canStartMu.Unlock()

	// Dispatch as openurl event (C: navigator.c:286-290)
	// Go equivalent: OpenURLWithView → navOpen0 (via event sink path)
	ns.OpenURLWithView(url, view)
}

// currentNavigator returns the navigator currently selected in the
// prop tree — global.navigators.current resolves through the
// linkselected link to the navigator the active UI spawned
// (prop_select in Spawn). Falls back to defaultNavigator when no
// navigator has been selected (headless, tests, pre-UI init).
// C: event_dispatch resolves global.navigators.current.eventSink per
// event (event.c:574); nav_open() goes through event_dispatch, so all
// user-visible opens land on the UI's navigator.
func (ns *NavigatorSystem) currentNavigator() *Navigator {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()
	if gp := ns.propMgr.GetGlobal(); gp != nil {
		if root := gp.ResolvePath([]string{"navigators", "current"}, true); root != nil {
			for _, nav := range ns.navigators {
				if nav.propRoot == root {
					return nav
				}
			}
		}
	}
	return ns.defaultNavigator
}

// GetRouteSystem returns the route system for plugin route registration.
func (ns *NavigatorSystem) GetRouteSystem() *RouteSystem {
	return ns.routeSystem
}

// SetBackendSystem sets the backend system for DI
func (ns *NavigatorSystem) SetBackendSystem(bs *core.BackendSystem) {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()
	ns.backendSystem = bs
	for _, nav := range ns.navigators {
		nav.backendSystem = bs
	}
}

// SetSettingsMgr sets the settings manager for bookmark settings integration.
// C: bookmarks_init uses settings_add_dir to create a "Bookmarks" directory
// in the settings tree (navigator.c:1523-1526).
func (ns *NavigatorSystem) SetSettingsMgr(sm settingscore.SettingsProvider) {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()
	ns.settingsMgr = sm
}

// StartNavigator initializes the navigation system.
// em is the shared EventManager — C has a single global event system,
// so Go must use a single EventManager instance passed from the caller.
func (ns *NavigatorSystem) StartNavigator(em *event.EventManager) {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()

	if ns.initialized {
		return
	}
	ns.eventMgr = em
	ns.defaultNavigator = &Navigator{
		history:       make([]HistoryEntry, 0),
		historyPos:    -1,
		pages:         make([]*NavPage, 0),
		pageQueue:     make([]*NavPage, 0),
		historyQueue:  make([]*NavPage, 0),
		ns:            ns,
		propManager:   ns.propMgr,
		backendSystem: ns.backendSystem,
		routeSystem:   ns.routeSystem,
		store:         ns.store,
	}

	// C: LIST_INSERT_HEAD(&navigators, nav, nav_link) (navigator.c:243)
	ns.navigators = append(ns.navigators, ns.defaultNavigator)

	// C: nav_init calls bookmarks_init() first (navigator.c:315-317,
	// ENABLE_BOOKMARKS) — before the navigators prop tree. It creates the
	// "Bookmarks" settings dir and loads bookmarks from storage.
	ns.defaultNavigator.bookmarksSetup()

	// Create $global.navigators property tree
	// In C: nav->nav_prop_root = prop_create(all_navigators, NULL);
	// The navigator propRoot is created AS A CHILD of navigators.nodes,
	// so it appears automatically in the $global.navigators.nodes subtree.
	var allNavigators *propcore.Prop
	globalProp := ns.propMgr.GetGlobal()
	if globalProp != nil {
		navigatorsProp := ns.propMgr.CreateEx(globalProp, "navigators", nil, false, false)
		allNavigators = ns.propMgr.CreateEx(navigatorsProp, "nodes", nil, false, false)
		// prop_linkselected_create(all_navigators, navs, "current", NULL)
		ns.propMgr.LinkselectedCreate(allNavigators, navigatorsProp, "current", "")
	}
	ns.allNavigators = allNavigators

	// Create property tree — propRoot is a child of navigators.nodes (like C)
	if allNavigators != nil {
		ns.defaultNavigator.propRoot = ns.propMgr.CreateEx(allNavigators, "", nil, false, false)
	} else {
		ns.defaultNavigator.propRoot = ns.defaultNavigator.propManager.CreateRootEx("", false)
	}
	ns.defaultNavigator.propPages = ns.defaultNavigator.propManager.CreateEx(ns.defaultNavigator.propRoot, "pages", nil, false, false)
	ns.defaultNavigator.propCurPage = ns.defaultNavigator.propManager.CreateEx(ns.defaultNavigator.propRoot, "currentpage", nil, false, false)
	ns.defaultNavigator.propCanGoBack = ns.defaultNavigator.propManager.CreateEx(ns.defaultNavigator.propRoot, "canGoBack", nil, false, false)
	ns.defaultNavigator.propCanGoFwd = ns.defaultNavigator.propManager.CreateEx(ns.defaultNavigator.propRoot, "canGoForward", nil, false, false)
	ns.defaultNavigator.propCanGoHome = ns.defaultNavigator.propManager.CreateEx(ns.defaultNavigator.propRoot, "canGoHome", nil, false, false)
	ns.defaultNavigator.propEventSink = ns.defaultNavigator.propManager.CreateEx(ns.defaultNavigator.propRoot, "eventSink", nil, false, false)

	// Create nav courier and subscribe nav_eventsink callback to propEventSink.
	// C: navigator.c:258-263
	//   nav->nav_eventsink = prop_subscribe(0,
	//       PROP_TAG_CALLBACK_EVENT, nav_eventsink, nav,
	//       PROP_TAG_MUTEX, &nav_mutex,
	//       PROP_TAG_ROOT, eventsink, NULL);
	// In C, this uses GLOBAL dispatch mode (default). In Go, we use
	// COURIER mode and drain in the render thread via ProcessNavCourier().
	// This is an IMPLEMENTATION DIFFERENCE from C (see Phase 0 audit).
	ns.navCourier = propcore.NewCourier("nav_eventsink")
	ns.navEventSinkSub = ns.propMgr.SubscribeWithCourier(
		ns.defaultNavigator.propEventSink, ns.navCourier,
		ns.navEventsinkCallback, ns.defaultNavigator,
		propcore.SubNoInitialUpdate,
	)

	// Wire the EventManager to route NAV events to this navigator's
	// eventSink prop. C: event_dispatch → event_to_prop(nav.eventSink, e)
	// → prop_send_ext_event. Go: em.Dispatch checks navEventSinkProp
	// and calls SendExtEvent to route NAV events to the nav courier.
	// Without this, NAV events (BACK, HOME, etc.) are never routed
	// to the navigator and GoBack/GoForward are never called.
	if em != nil {
		em.SetNavEventSinkProp(ns.defaultNavigator.propEventSink)
	}

	// Set up dtor_tracker: subscribe with TRACK_DESTROY on propRoot so
	// that if propRoot is destroyed (by any path), the navigator is
	// properly cleaned up — matching C's nav_dtor_tracker.
	// C: nav->nav_dtor_tracker = prop_subscribe(PROP_SUB_TRACK_DESTROY,
	//     PROP_TAG_CALLBACK, nav_dtor_tracker, nav, ...)
	nav := ns.defaultNavigator
	// C: PROP_TAG_MUTEX, &nav_mutex — the dispatch worker holds
	// nav.mutex around the callback.
	nav.dtorTracker = ns.propMgr.Subscribe(nav.propRoot, func(opaque any, event propcore.EventType, args ...any) {
		if event != propcore.EventDestroyed {
			return
		}
		// C: nav_dtor_tracker callback:
		//   prop_unsubscribe(nav->nav_eventsink);
		//   prop_unsubscribe(nav->nav_dtor_tracker);
		//   nav_close_all(nav, 0);
		//   LIST_REMOVE(nav, nav_link);
		//   free(nav);
		if nav.eventSinkSub != nil {
			nav.eventSinkSub.Unsubscribe()
			nav.eventSinkSub = nil
		}
		if nav.dtorTracker != nil {
			nav.dtorTracker.Unsubscribe()
			nav.dtorTracker = nil
		}
		navCloseAllLocked(nav, false)
		// C: LIST_REMOVE(nav, nav_link) — remove from global navigators list.
		// Go: remove from ns.navigators slice.
		ns := nav.ns
		if ns != nil {
			ns.navMutex.Lock()
			for i, n := range ns.navigators {
				if n == nav {
					ns.navigators = slices.Delete(ns.navigators, i, i+1)
					break
				}
			}
			ns.navMutex.Unlock()
		}
	}, nav, propcore.SubNoInitialUpdate, propcore.SubFlagTrackDestroy,
		propcore.SubMutex{Ptr: &nav.mutex})

	// Set initial state
	ns.defaultNavigator.propManager.SetIntEx(ns.defaultNavigator.propCanGoHome, nil, 1)

	// Open home page
	navOpen0(ns.defaultNavigator, NavHome, "", nil, nil, "", "")

	ns.initialized = true
}

// DefaultNavigator returns the default navigator instance
func (ns *NavigatorSystem) DefaultNavigator() *Navigator {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()
	return ns.defaultNavigator
}

// NavEventSinkProp returns the navigator's eventSink prop, used by
// EventManager.Dispatch to send NAV events via SendExtEvent.
// C: prop_get_by_name(PNVEC("nav", "eventSink"), ...)
func (ns *NavigatorSystem) NavEventSinkProp() *propcore.Prop {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()
	if ns.defaultNavigator == nil {
		return nil
	}
	return ns.defaultNavigator.propEventSink
}

// Fini finalizes the navigation system
// C: nav_fini (navigator.c:428-437) — iterates ALL navigators and closes
// all pages in each.
func (ns *NavigatorSystem) Fini() {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()

	// C: LIST_FOREACH(nav, &navigators, nav_link) { nav_close_all(nav, 1); }
	for _, nav := range ns.navigators {
		if nav == nil {
			continue
		}
		// Unsubscribe nav_eventsink courier subscription
		if ns.navEventSinkSub != nil && nav == ns.defaultNavigator {
			ns.navEventSinkSub.Unsubscribe()
			ns.navEventSinkSub = nil
		}
		if ns.navCourier != nil && nav == ns.defaultNavigator {
			ns.navCourier.Release()
			ns.navCourier = nil
		}
		// Unsubscribe dtor_tracker first to prevent callback when we destroy propRoot
		if nav.dtorTracker != nil {
			nav.dtorTracker.Unsubscribe()
			nav.dtorTracker = nil
		}
		if nav.eventSinkSub != nil {
			nav.eventSinkSub.Unsubscribe()
			nav.eventSinkSub = nil
		}
		// Close all pages while holding nav.mutex to prevent races
		nav.mutex.Lock()
		for len(nav.pages) > 0 {
			np := nav.pages[len(nav.pages)-1]
			nav.pages = nav.pages[:len(nav.pages)-1]
			navCloseNoLock(np, true)
		}
		nav.mutex.Unlock()
		nav.propManager.Destroy(nav.propRoot)
		nav.history = nil
		nav.historyPos = -1
	}
	ns.navigators = nil
	ns.defaultNavigator = nil
	ns.initialized = false
}

// GetDefaultNavigator returns the default navigator instance
func (ns *NavigatorSystem) GetDefaultNavigator() *Navigator {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()
	return ns.defaultNavigator
}

// SetDefaultNavigator sets the default navigator instance
// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (ns *NavigatorSystem) SetGconf(g *gconf.T) { ns.gconf = g }

// gcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (ns *NavigatorSystem) gcfg() *gconf.T {
	if ns.gconf == nil {
		ns.gconf = gconf.New()
	}
	return ns.gconf
}

func (ns *NavigatorSystem) SetDefaultNavigator(nav *Navigator) {
	ns.navMutex.Lock()
	defer ns.navMutex.Unlock()
	ns.defaultNavigator = nav
}

// nsSettingsMgr returns the settings provider when present.
func (nav *Navigator) nsSettingsMgr() settingscore.SettingsProvider {
	if nav.ns == nil {
		return nil
	}
	return nav.ns.settingsMgr
}
