package navigator

import (
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

const (
	// NavHome is the home page URL
	NavHome = "page:home"
)

// Navigator represents the UI navigation system
type Navigator struct {
	root        *propcore.Prop
	currentURL  string
	currentView string
	history     []HistoryEntry
	historyPos  int
	mutex       sync.Mutex
	ns          *NavigatorSystem

	// Property manager
	propManager *propcore.PropManager

	// store — C: global htsmsg_store (bookmarks persist), injected via ns
	store *htsmsg.Store

	// Backend system for opening pages
	backendSystem *core.BackendSystem

	// Route system for JS plugin routes
	routeSystem *RouteSystem

	// Property tree
	propRoot      *propcore.Prop
	propPages     *propcore.Prop
	propCurPage   *propcore.Prop
	propCanGoBack *propcore.Prop
	propCanGoFwd  *propcore.Prop
	propCanGoHome *propcore.Prop
	propEventSink *propcore.Prop

	// Pages
	pages        []*NavPage
	current      *NavPage
	pageQueue    []*NavPage
	historyQueue []*NavPage

	// Bookmarks
	bookmarks       []*Bookmark
	bookmarkNodes   *propcore.Prop
	bookmarkQueries []*BookmarkQuery
	// bookmarkNodesSub subscribes to bookmarkNodes for add/del child events.
	// C: bookmark_queries_callback (navigator.c:1457-1475)
	bookmarkNodesSub *propcore.Subscription

	// dtorTracker subscribes to propRoot with TRACK_DESTROY.
	// C: nav->nav_dtor_tracker — fires when propRoot is destroyed,
	// triggering navigator cleanup (close pages, unsubscribe, remove from list).
	dtorTracker *propcore.Subscription

	// eventSinkSub is the subscription on the eventSink prop.
	// C: nav->nav_eventsink — unsubscribed during dtor_tracker cleanup.
	eventSinkSub *propcore.Subscription
}

// HistoryEntry represents a navigation history entry
type HistoryEntry struct {
	URL  string
	View string
}

// NavPage represents a navigation page
type NavPage struct {
	nav         *Navigator
	propRoot    *propcore.Prop
	url         string
	parentURL   string
	how         string
	directClose bool

	// Models
	itemModelSrc   *propcore.Prop
	itemModelDst   *propcore.Prop
	parentModelSrc *propcore.Prop
	parentModelDst *propcore.Prop

	// Subscriptions
	closeSub       *propcore.Subscription
	eventSinkSub   *propcore.Subscription
	directCloseSub *propcore.Subscription

	// Bookmark support
	bookmarkedSub *propcore.Subscription
	bookmarked    *propcore.Prop
	// C: np_title_sub/np_icon_sub are name-vector subscriptions
	// ("page","model","metadata","title"/"logo") — dynamically re-armed.
	titlePathSub       *navPathSub
	iconPathSub        *navPathSub
	title              string
	icon               string
	bookmarkNotifyProp *propcore.Prop
}

// PageInfo holds info about a navigator page accessible from outside.
type PageInfo struct {
	URL      string
	PropRoot *propcore.Prop
}

// Bookmark represents a bookmark
type Bookmark struct {
	root          *propcore.Prop
	titleSub      *propcore.Subscription
	urlSub        *propcore.Subscription
	typeSub       *propcore.Subscription
	iconSub       *propcore.Subscription
	delReqSub     *propcore.Subscription
	id            string
	title         string
	url           string
	itemType      string
	icon          string
	info          *propcore.Prop
	queries       []*BookmarkQuery
	service       *service.Service
	typeSetting   *settingscore.Setting // C: bm_type_setting
	deleteSetting *settingscore.Setting // C: bm_delete
}

// BookmarkQuery represents a bookmark query
type BookmarkQuery struct {
	key         string
	link        *propcore.Prop
	keySub      *propcore.Subscription
	keyWatchSub *propcore.Subscription // watch on p for "key" child add/del
	bookmark    *Bookmark
	parentProp  *propcore.Prop // the prop that owns this query (for del)
}

// NavigatorSystem manages navigation subsystem without global state
type NavigatorSystem struct {
	defaultNavigator *Navigator
	// navigators is the list of all navigators, matching C's global
	// `navigators` list (navigator.c:243: LIST_INSERT_HEAD(&navigators, ...)).
	navigators    []*Navigator
	navMutex      sync.Mutex
	initialized   bool
	propMgr       *propcore.PropManager
	serviceSystem *service.ServiceSystem
	store         *htsmsg.Store    // C: global htsmsg_store — injected
	kvstore       *kvstore.KVStore // C: global kvstore_* — injected
	backendSystem *core.BackendSystem
	settingsMgr   settingscore.SettingsProvider
	routeSystem   *RouteSystem
	eventMgr      *event.EventManager
	ts            *trace.TraceSystem // C: trace() global — injected

	// gconf — C: gconf_t fields navigator reads (enable_nav_always_close).
	gconf *gconf.T

	// notificationMgr — C: notify_add() global — injected.
	notificationMgr *notifications.NotificationManager

	// navCourier is the courier for NAV event delivery via SendExtEvent.
	// NAV events are sent via pm.SendExtEvent(nav.propEventSink, e) and
	// drained by ProcessNavCourier() in the render thread.
	// C: prop_courier_t used by nav_eventsink subscription (navigator.c:258)
	navCourier      *propcore.Courier
	navEventSinkSub *propcore.Subscription

	// allNavigators is the $global.navigators.nodes prop — C's
	// all_navigators (navigator.c:319). Every nav propRoot is created
	// under it (C: prop_create(all_navigators, NULL) in nav_create).
	allNavigators *propcore.Prop

	// C: gconf.enable_nav_always_close is an int global written by the
	// "navalwaysclose" dev setting — default 0 in C, not true. It lives
	// as the package-level GconfEnableNavAlwaysClose var (bottom of file).

	// wg tracks event consumer goroutines started by StartEventConsumer.
	// WaitConsumers() blocks until all consumer goroutines have exited,
	// ensuring Fini() cannot destroy the navigator while a consumer is
	// still inside processNavEvent().
	wg sync.WaitGroup

	// navigatorCanStart mirrors C's gconf.navigator_can_start (main.c:302).
	// C: set to 1 by navigator_can_start() after plugins_load_all() completes
	// in swthread (main.c:271/277/287). The initial_url dispatch in nav_create
	// (navigator.c:282) blocks on this condition before dispatching.
	// Go: SetCanStart() is called after plugin initialization. DispatchInitialURL()
	// blocks until SetCanStart() has been called.
	navigatorCanStart bool
	canStartCond      *sync.Cond
	canStartMu        sync.Mutex
}
