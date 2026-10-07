package navigator

import (
	"slices"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// bookmarksSetup initializes the bookmark system
// C: bookmarks_init (navigator.c:1518-1577) — creates "Bookmarks" settings
// directory, sets mayadd=1, subscribes to bookmarkNodes, loads bookmarks
// from storage, subscribes to bookmark_queries and bookmark_eventsink.
func (nav *Navigator) bookmarksSetup() {
	// Load bookmarks from storage
	nav.bookmarks = make([]*Bookmark, 0)
	nav.bookmarkQueries = make([]*BookmarkQuery, 0)

	// C: settings_add_dir(NULL, "Bookmarks", "bookmark", NULL,
	//     "Add and remove items on homepage", "settings:bookmarks")
	// Creates a "Bookmarks" directory in the settings tree.
	if nav.ns != nil && nav.ns.settingsMgr != nil {
		bookmarkRoot := nav.ns.settingsMgr.AddDir(nil, nav.ns.settingsMgr.P("Bookmarks"),
			"bookmark", "", nav.ns.settingsMgr.P("Add and remove items on homepage"),
			"settings:bookmarks")
		if bookmarkRoot != nil {
			// C: bookmark_nodes = prop_create(root, "nodes")
			nav.bookmarkNodes = nav.propManager.CreateEx(bookmarkRoot, "nodes", nil, false, false)
			// C: prop_set(root, "mayadd", PROP_SET_INT, 1)
			mayaddProp := nav.propManager.CreateEx(bookmarkRoot, "mayadd", nil, false, false)
			nav.propManager.SetIntEx(mayaddProp, nil, 1)
		}
	}
	if nav.bookmarkNodes == nil {
		// Fallback if no SettingsManager — create standalone root
		nav.bookmarkNodes = nav.propManager.CreateRootEx("", false)
	}

	// C: prop_subscribe(0, PROP_TAG_CALLBACK, bookmarks_callback, NULL,
	//     PROP_TAG_ROOT, bookmark_nodes, PROP_TAG_MUTEX, &nav_mutex, NULL)
	// (navigator.c:1531-1536)
	nav.bookmarkNodesSub = nav.bookmarkNodes.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		switch event {
		case propcore.EventReqNewChild:
			// C: PROP_REQ_NEW_CHILD → bookmark_add("New bookmark", "none:", "other", NULL, NULL)
			nav.bookmarkAdd("New bookmark", "none:", "other", "", "", true)
		case propcore.EventReqDeleteVector:
			// C: PROP_REQ_DELETE_VECTOR → prop_vec_destroy_entries
			if len(args) > 0 {
				if pv, ok := args[0].(*propcore.PropVec); ok && pv != nil {
					propcore.PropVecDestroyEntries(pv)
				}
			}
		}
	}, nil, propcore.SubMutex{Ptr: &nav.mutex})

	// Load existing bookmarks from storage
	// C: htsmsg_store_load("bookmarks") legacy migration, then
	// htsmsg_store_load("bookmarks2") (navigator.c:1539-1564)
	nav.bookmarksLoad()

	// C: prop_subscribe(0, PROP_TAG_CALLBACK, bookmark_queries_callback, NULL,
	//     PROP_TAG_NAME("global","bookmarks","queries"),
	//     PROP_TAG_MUTEX, &nav_mutex, NULL) (navigator.c:1566-1570)
	if q := nav.propManager.GetByName([]string{"global", "bookmarks", "queries"}, 0, nil, nil); q != nil {
		q.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
			switch event {
			case propcore.EventAddChild, propcore.EventAddChildBefore:
				if len(args) > 0 {
					if child, ok := args[0].(*propcore.Prop); ok && child != nil {
						nav.bookmarkQueryAdd(child)
					}
				}
			case propcore.EventDelChild:
				if len(args) > 0 {
					if child, ok := args[0].(*propcore.Prop); ok && child != nil {
						nav.bookmarkQueryDel(child)
					}
				}
			}
		}, nil, propcore.SubMutex{Ptr: &nav.mutex})
		nav.propManager.RefDec(q)
	}

	// C: prop_subscribe(0, PROP_TAG_CALLBACK_EVENT, bookmark_eventsink, NULL,
	//     PROP_TAG_NAME("global","bookmarks","eventSink"),
	//     PROP_TAG_MUTEX, &nav_mutex, NULL) (navigator.c:1572-1576)
	if es := nav.propManager.GetByName([]string{"global", "bookmarks", "eventSink"}, 0, nil, nil); es != nil {
		es.Subscribe(func(opaque any, eventType propcore.EventType, args ...any) {
			nav.bookmarkEventsink(eventType, args)
		}, nil, propcore.SubMutex{Ptr: &nav.mutex})
		nav.propManager.RefDec(es)
	}
}

// bookmarkEventsink toggles a bookmark for the page referenced by an
// EVENT_PROPREF event.
// C: bookmark_eventsink (navigator.c:1482-1515)
func (nav *Navigator) bookmarkEventsink(eventType propcore.EventType, args []any) {
	if eventType != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	ep, ok := event.ConcreteOf(args[0]).(*event.EventProp)
	if !ok || ep.Type != event.EVENT_PROPREF {
		return
	}
	p, _ := ep.P.(*propcore.Prop)
	if p == nil {
		return
	}
	// C: prop_get_string(ep->p, "url", NULL) /
	//    prop_get_string(ep->p, "metadata", "title", NULL) /
	//    prop_get_string(ep->p, "metadata", "icon", NULL)
	var url, title, icon string
	if c := p.GetChild("url"); c != nil {
		url = c.GetString()
	}
	if md := p.GetChild("metadata"); md != nil {
		if c := md.GetChild("title"); c != nil {
			title = c.GetString()
		}
		if c := md.GetChild("icon"); c != nil {
			icon = c.GetString()
		}
	}

	var bm *Bookmark
	for _, b := range nav.bookmarks {
		if b.url == url {
			bm = b
			break
		}
	}

	if bm != nil {
		// C: notify_add("Removed bookmark: %s") + prop_destroy(bm->bm_root)
		if nav.ns.notificationMgr != nil {
			nav.ns.notificationMgr.NotifyAdd(nil, notifications.NotifyInfo, "", 3, "Removed bookmark: %s", bm.title)
		}
		nav.propManager.Destroy(bm.root)
	} else {
		// C: bookmark_add(title, url, "other", icon, url) + bookmarks_save()
		//    + notify_add("Added new bookmark: %s")
		nav.bookmarkAdd(title, url, "other", icon, url, true)
		nav.bookmarksSave()
		if nav.ns.notificationMgr != nil {
			nav.ns.notificationMgr.NotifyAdd(nil, notifications.NotifyInfo, "", 3, "Added new bookmark: %s", title)
		}
	}
}

// bookmarkLoad — C: bookmark_load (navigator.c:1378-1385).
// Reads the five string fields from the stored map and calls
// bookmark_add.
func (nav *Navigator) bookmarkLoad(o *htsmsg.HTSMsg) {
	nav.bookmarkAdd(o.GetStr("title"), o.GetStr("url"),
		o.GetStr("svctype"), o.GetStr("icon"), o.GetStr("id"), false)
}

// bookmarksLoad loads bookmarks from the persistent htsmsg store.
// C: load path of bookmarks_init (navigator.c:1539-1564) —
// "bookmarks" is the legacy map-with-"nodes" format (migrated to
// "bookmarks2" then removed); "bookmarks2" is the current list format.
func (nav *Navigator) bookmarksLoad() {
	store := nav.store
	if store == nil {
		return
	}

	if m, err := store.Load("bookmarks"); err == nil && m != nil {
		// C: legacy "bookmarks" store — map with "nodes" sub-map
		if n := m.GetMap("nodes"); n != nil {
			for _, f := range n.GetFields() {
				o := f.GetMap()
				if o == nil {
					continue
				}
				if p := o.GetMap("model"); p != nil {
					o = p
				}
				nav.bookmarkLoad(o)
			}
		}
		m.Release()
		// C: bookmarks_save(); htsmsg_store_remove("bookmarks");
		nav.bookmarksSave()
		if nav.store != nil {
			nav.store.Remove("bookmarks")
		}
	} else if m, err := store.Load("bookmarks2"); err == nil && m != nil {
		// C: "bookmarks2" — list of bookmark maps
		for _, f := range m.GetFields() {
			o := f.GetMap()
			if o == nil {
				continue
			}
			nav.bookmarkLoad(o)
		}
		m.Release()
	}
}

// bookmarkAdd adds a new bookmark.
// C: bookmark_add (navigator.c:1222-1350) — NULL title/url aborts;
// LIST_INSERT_HEAD(&bookmarks) prepends; the bookmark root carries a
// metadata prop subtree (title/url/svctype/icon) whose value-change subs
// write back into bm_* and the service, a SVC_ORIGIN_BOOKMARK service,
// status/statustxt links, a TRACK_DESTROY teardown subscription, a
// delete-request subscription on the service root, and a settings page
// ("model") built from bound strings + a multiopt type setting + a
// delete action. Finally prop_set_parent into bookmark_nodes, then optionally
// nav_update_bookmarked before bookmark_link_queries. updatePages is false
// during initial restore because StartNavigator holds navMutex.
func (nav *Navigator) bookmarkAdd(title string, url string, itemType string, icon string, id string, updatePages bool) {
	if title == "" || url == "" {
		return
	}
	bm := &Bookmark{
		title:    title,
		url:      url,
		itemType: itemType,
		icon:     icon,
		id:       id,
	}

	// C: LIST_INSERT_HEAD(&bookmarks, bm, bm_link)
	nav.bookmarks = slices.Insert(nav.bookmarks, 0, bm)

	// C: prop_t *p = prop_create_root(NULL); bm->bm_root = prop_ref_inc(p)
	p := nav.propManager.CreateRootEx("", false)
	bm.root = nav.propManager.RefInc(p)

	// C: prop_set(p, "type", PROP_SET_STRING, "settings")
	nav.propManager.SetStringEx(nav.propManager.CreateEx(p, "type", nil, false, false),
		nil, "settings", propcore.StringUTF8)

	// C: bm->bm_title_sub = add_prop(p, "title", bm->bm_title, bm, bm_set_title); ...
	bm.titleSub = nav.addBookmarkProp(p, "title", title, bm, nav.bmSetTitle)
	bm.urlSub = nav.addBookmarkProp(p, "url", url, bm, nav.bmSetURL)
	bm.typeSub = nav.addBookmarkProp(p, "svctype", itemType, bm, nav.bmSetType)
	bm.iconSub = nav.addBookmarkProp(p, "icon", icon, bm, nav.bmSetIcon)

	// C: if(id == NULL) bm->bm_id = get_random_string(); else rstr_alloc(id)
	if bm.id == "" {
		r := misc.GetRandomString()
		bm.id = misc.RstrGet(r)
		misc.RstrRelease(r)
	}

	// C: bm->bm_service = service_create(rstr_get(bm->bm_id), title, url,
	//     type, icon, 1, 1, SVC_ORIGIN_BOOKMARK)
	if nav.ns != nil && nav.ns.serviceSystem != nil {
		bm.service = nav.ns.serviceSystem.ServiceCreate(bm.id, title, url,
			itemType, icon, true, true, service.SvcOriginBookmark)
	}

	if bm.service != nil {
		// C: prop_link(service_get_status_prop(bm->bm_service),
		//     prop_create(p, "status")); same for statustxt
		if sp, ok := service.ServiceGetStatusProp(bm.service).(*propcore.Prop); ok && sp != nil {
			sp.Link(nav.propManager.CreateEx(p, "status", nil, false, false))
		}
		if sp, ok := service.ServiceGetStatusTxtProp(bm.service).(*propcore.Prop); ok && sp != nil {
			sp.Link(nav.propManager.CreateEx(p, "statustxt", nil, false, false))
		}
	}

	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY | PROP_SUB_NO_INITIAL_UPDATE,
	//     PROP_TAG_CALLBACK, bookmark_destroyed, bm, PROP_TAG_ROOT, p,
	//     PROP_TAG_MUTEX, &nav_mutex, NULL)
	p.Subscribe(nav.bookmarkDestroyed, bm,
		propcore.SubFlagTrackDestroy|propcore.SubNoInitialUpdate,
		propcore.SubMutex{Ptr: &nav.mutex})

	if bm.service != nil {
		// C: prop_set(bm->bm_service->s_root, "deleteText",
		//     PROP_SET_LINK, _p("Remove bookmark"))
		if nav.ns != nil && nav.ns.settingsMgr != nil {
			dt := nav.propManager.CreateEx(bm.service.Root(), "deleteText", nil, false, false)
			nav.propManager.Link(nav.ns.settingsMgr.P("Remove bookmark"), dt, nil, false, false)
		}
		// C: bm->bm_del_req_sub = prop_subscribe(PROP_SUB_TRACK_DESTROY,
		//     PROP_TAG_CALLBACK, bookmark_delete_request, bm,
		//     PROP_TAG_ROOT, bm->bm_service->s_root,
		//     PROP_TAG_MUTEX, &nav_mutex, NULL)
		bm.delReqSub = bm.service.Root().Subscribe(nav.bookmarkDeleteRequest, bm,
			propcore.SubFlagTrackDestroy, propcore.SubMutex{Ptr: &nav.mutex})
	}

	// Construct the settings page (C: navigator.c:1298-1342)
	m := nav.propManager.CreateEx(p, "model", nil, false, false)

	// C: prop_set(p, "url", PROP_ADOPT_RSTRING, backend_prop_make(m, NULL))
	var pageURL string
	if nav.ns != nil && nav.ns.backendSystem != nil {
		pageURL = nav.ns.backendSystem.GetPropPageManager().BackendPropMake(nav.propManager, m, "")
	}
	nav.propManager.SetStringEx(nav.propManager.CreateEx(p, "url", nil, false, false),
		nil, pageURL, propcore.StringUTF8)

	md := nav.propManager.CreateEx(p, "metadata", nil, false, false)

	// C: prop_set(m, "type", PROP_SET_STRING, "settings");
	//    prop_set(m, "subtype", PROP_SET_STRING, "bookmark")
	nav.propManager.SetStringEx(nav.propManager.CreateEx(m, "type", nil, false, false),
		nil, "settings", propcore.StringUTF8)
	nav.propManager.SetStringEx(nav.propManager.CreateEx(m, "subtype", nil, false, false),
		nil, "bookmark", propcore.StringUTF8)

	// C: prop_link(prop_create(md, "title"),
	//     prop_create(prop_create(m, "metadata"), "title"))
	nav.propManager.Link(nav.propManager.CreateEx(md, "title", nil, false, false),
		nav.propManager.CreateEx(nav.propManager.CreateEx(m, "metadata", nil, false, false),
			"title", nil, false, false), nil, false, false)

	sm := nav.nsSettingsMgr()
	if sm != nil {
		// C: settings_create_bound_string(m, _p("Title"), prop_create(md, "title")); URL; Icon
		sm.CreateBoundString(m, sm.P("Title"), nav.propManager.CreateEx(md, "title", nil, false, false))
		sm.CreateBoundString(m, sm.P("URL"), nav.propManager.CreateEx(md, "url", nil, false, false))
		sm.CreateBoundString(m, sm.P("Icon"), nav.propManager.CreateEx(md, "icon", nil, false, false))

		// C: bm->bm_type_setting = setting_create(SETTING_MULTIOPT, m,
		//     SETTINGS_INITIAL_UPDATE, SETTING_TITLE(_p("Type")),
		//     SETTING_VALUE(type), SETTING_OPTION..., SETTING_CALLBACK(
		//     change_type, bm), SETTING_MUTEX(&nav_mutex), NULL)
		bm.typeSetting = sm.SettingCreate(settingscore.SettingMultiOpt, m,
			settingscore.SettingsInitialUpdate,
			settingscore.SettingTagTitle, sm.P("Type"),
			settingscore.SettingTagValue, itemType,
			settingscore.SettingTagOption, "other", sm.P("Other"),
			settingscore.SettingTagOption, "music", sm.P("Music"),
			settingscore.SettingTagOption, "video", sm.P("Video"),
			settingscore.SettingTagOption, "tv", sm.P("TV"),
			settingscore.SettingTagOption, "photos", sm.P("Photos"),
			settingscore.SettingTagCallback, nav.changeType, bm,
			settingscore.SettingTagMutex, &nav.mutex)

		// C: bm->bm_info = prop_create_root(NULL);
		//    settings_create_info(m, NULL,
		//      service_get_statustxt_prop(bm->bm_service))
		bm.info = nav.propManager.CreateRootEx("", false)
		var statustxt *propcore.Prop
		if bm.service != nil {
			statustxt, _ = service.ServiceGetStatusTxtProp(bm.service).(*propcore.Prop)
		}
		sm.CreateInfo(m, "", statustxt)

		// C: bm->bm_delete = setting_create(SETTING_ACTION, m, 0,
		//     SETTING_TITLE(_p("Delete")), SETTING_CALLBACK(bm_delete, bm),
		//     SETTING_MUTEX(&nav_mutex), NULL)
		bm.deleteSetting = sm.SettingCreate(settingscore.SettingAction, m, 0,
			settingscore.SettingTagTitle, sm.P("Delete"),
			settingscore.SettingTagCallback, nav.bmDelete, bm,
			settingscore.SettingTagMutex, &nav.mutex)
	}

	// C: prop_link(prop_create(md, "url"), prop_create(md, "shortdesc"))
	nav.propManager.Link(nav.propManager.CreateEx(md, "url", nil, false, false),
		nav.propManager.CreateEx(md, "shortdesc", nil, false, false), nil, false, false)

	// C: if(prop_set_parent(p, bookmark_nodes)) abort();
	if nav.bookmarkNodes != nil {
		nav.propManager.SetParentEx(p, nav.bookmarkNodes, nil, "")
	}

	if updatePages {
		nav.ns.UpdateAllBookmarks()
	}
	// C: bookmark_link_queries(bm)
	nav.bookmarkLinkQueries(bm)
}

// addBookmarkProp — C: add_prop (navigator.c:1204-1218).
// Creates metadata/name under the bookmark root, initializes it to
// value, and subscribes (NO_INITIAL_UPDATE + RSTR callback + nav_mutex)
// so later edits write back via cb.
func (nav *Navigator) addBookmarkProp(parent *propcore.Prop, name string, value string,
	bm *Bookmark, cb func(*Bookmark, string)) *propcore.Subscription {
	p := nav.propManager.CreateEx(nav.propManager.CreateEx(parent, "metadata", nil, false, false),
		name, nil, false, false)
	if value != "" {
		nav.propManager.SetStringEx(p, nil, value, propcore.StringUTF8)
	}
	return p.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		if (event == propcore.EventSetRString || event == propcore.EventSetCString) &&
			len(args) > 0 {
			if str, ok := args[0].(string); ok {
				cb(bm, str)
			}
		}
	}, bm, propcore.SubNoInitialUpdate, propcore.SubMutex{Ptr: &nav.mutex})
}

// bmSetTitle — C: bm_set_title (navigator.c:1117-1125).
func (nav *Navigator) bmSetTitle(bm *Bookmark, str string) {
	bm.title = str
	if bm.service != nil {
		service.ServiceSetTitle(bm.service, str)
	}
	nav.bookmarksSave()
}

// bmSetURL — C: bm_set_url (navigator.c:1132-1142).
func (nav *Navigator) bmSetURL(bm *Bookmark, str string) {
	nav.bookmarkUnlinkQueries(bm)
	bm.url = str
	if bm.service != nil && nav.ns != nil && nav.ns.serviceSystem != nil {
		nav.ns.serviceSystem.ServiceSetURL(bm.service, str)
	}
	nav.bookmarksSave()
	nav.ns.UpdateAllBookmarks()
	nav.bookmarkLinkQueries(bm)
}

// bmSetType — C: bm_set_type (navigator.c:1149-1155).
func (nav *Navigator) bmSetType(bm *Bookmark, str string) {
	bm.itemType = str
	if bm.service != nil {
		service.ServiceSetType(bm.service, str)
	}
	nav.bookmarksSave()
}

// bmSetIcon — C: bm_set_icon (navigator.c:1162-1168).
func (nav *Navigator) bmSetIcon(bm *Bookmark, str string) {
	bm.icon = str
	if bm.service != nil {
		service.ServiceSetIcon(bm.service, str)
	}
	nav.bookmarksSave()
}

// bmDelete — C: bm_delete (navigator.c:1225-1230).
func (nav *Navigator) bmDelete(opaque any, _ any) {
	if bm, ok := opaque.(*Bookmark); ok {
		nav.propManager.Destroy(bm.root)
	}
}

// changeType — C: change_type (navigator.c:1198-1201).
func (nav *Navigator) changeType(opaque any, str string) {
	bm, _ := opaque.(*Bookmark)
	if bm == nil {
		return
	}
	// C: prop_setv(bm->bm_root, "metadata", "svctype", NULL,
	//     PROP_SET_STRING, string)
	nav.propManager.SetStringEx(
		nav.propManager.CreateEx(
			nav.propManager.CreateEx(bm.root, "metadata", nil, false, false),
			"svctype", nil, false, false),
		nil, str, propcore.StringUTF8)
}

// bookmarkDeleteRequest — C: bookmark_delete_request (navigator.c:1110-1113
// area: PROP_REQ_DELETE on the service root destroys the bookmark root).
func (nav *Navigator) bookmarkDeleteRequest(opaque any, event propcore.EventType, args ...any) {
	if event != propcore.EventReqDelete {
		return
	}
	if bm, ok := opaque.(*Bookmark); ok {
		nav.propManager.Destroy(bm.root)
	}
}

// bookmarkUnlinkQueries — C: bookmark_unlink_queries (navigator.c:1047-1056).
func (nav *Navigator) bookmarkUnlinkQueries(bm *Bookmark) {
	for _, bq := range bm.queries {
		bq.bookmark = nil
		bq.link.Unlink()
	}
	bm.queries = bm.queries[:0]
}

// bookmarkLinkQueries — C: bookmark_link_queries (navigator.c:1063-1073).
func (nav *Navigator) bookmarkLinkQueries(bm *Bookmark) {
	for _, bq := range nav.bookmarkQueries {
		if bq.key == bm.url && bq.bookmark == nil {
			bq.bookmark = bm
			bm.queries = append(bm.queries, bq)
			bm.root.Link(bq.link)
		}
	}
}

// bookmarkDestroyed — C: bookmark_destroyed (navigator.c:1080-1140).
// Full teardown on PROP_DESTROYED of the bookmark root: unlink queries,
// drop all subs, destroy the service + settings, LIST_REMOVE, release
// the bm ref on root, destroy bm_info, save, unsubscribe the
// notification's own sub, update bookmarked flags.
func (nav *Navigator) bookmarkDestroyed(opaque any, event propcore.EventType, args ...any) {
	if event != propcore.EventDestroyed {
		return
	}
	bm, _ := opaque.(*Bookmark)
	if bm == nil {
		return
	}

	nav.bookmarkUnlinkQueries(bm)

	var s *propcore.Subscription
	if len(args) > 0 {
		s, _ = args[0].(*propcore.Subscription)
	}

	if bm.titleSub != nil {
		bm.titleSub.Unsubscribe()
	}
	if bm.urlSub != nil {
		bm.urlSub.Unsubscribe()
	}
	if bm.typeSub != nil {
		bm.typeSub.Unsubscribe()
	}
	if bm.iconSub != nil {
		bm.iconSub.Unsubscribe()
	}
	if bm.delReqSub != nil {
		bm.delReqSub.Unsubscribe()
	}

	if bm.service != nil && nav.ns != nil && nav.ns.serviceSystem != nil {
		nav.ns.serviceSystem.ServiceDestroy(bm.service)
	}
	sm := nav.nsSettingsMgr()
	if bm.typeSetting != nil && sm != nil {
		sm.Destroy(bm.typeSetting)
	}
	if bm.deleteSetting != nil && sm != nil {
		sm.Destroy(bm.deleteSetting)
	}

	// C: LIST_REMOVE(bm, bm_link)
	for i, b := range nav.bookmarks {
		if b == bm {
			nav.bookmarks = slices.Delete(nav.bookmarks, i, i+1)
			break
		}
	}
	// C: prop_ref_dec(bm->bm_root)
	nav.propManager.RefDec(bm.root)

	// C: prop_destroy(bm->bm_info)
	if bm.info != nil {
		nav.propManager.Destroy(bm.info)
	}

	nav.bookmarksSave()
	if s != nil {
		s.Unsubscribe()
	}
	nav.ns.UpdateAllBookmarks()
}

// bookmarksSave saves bookmarks to the persistent htsmsg store.
// C: bookmarks_save (navigator.c:1019-1040) — builds a list of maps
// {id,title,svctype,url[,icon]} (skipping incomplete entries) and
// htsmsg_store_save(m, "bookmarks2").
func (nav *Navigator) bookmarksSave() {
	store := nav.store
	if store == nil {
		return
	}
	m := htsmsg.NewList()
	for _, bm := range nav.bookmarks {
		// C: if(bm->bm_title == NULL || bm->bm_url == NULL ||
		//       bm->bm_type == NULL) continue;
		if bm.title == "" || bm.url == "" || bm.itemType == "" {
			continue
		}
		b := htsmsg.NewMap()
		b.AddStr("id", bm.id)
		b.AddStr("title", bm.title)
		b.AddStr("svctype", bm.itemType)
		b.AddStr("url", bm.url)
		if bm.icon != "" {
			b.AddStr("icon", bm.icon)
		}
		m.AddMsg("", b)
	}
	// C: htsmsg_store_save(m, "bookmarks2"); htsmsg_release(m);
	store.Save(m, "bookmarks2")
	m.Release()
}

// UpdateAllBookmarks updates bookmarked status across all navigators.
// C: nav_update_bookmarked (navigator.c:216-227) — LIST_FOREACH(navigators)
// then TAILQ_FOREACH(nav_pages) setting np_bookmarked via prop_set_int_ex.
func (ns *NavigatorSystem) UpdateAllBookmarks() {
	ns.navMutex.Lock()
	// C: LIST_FOREACH(nav, &navigators, nav_link)
	navs := make([]*Navigator, len(ns.navigators))
	copy(navs, ns.navigators)
	// Also include defaultNavigator if not already in list
	if ns.defaultNavigator != nil {
		found := slices.Contains(navs, ns.defaultNavigator)
		if !found {
			navs = append(navs, ns.defaultNavigator)
		}
	}
	ns.navMutex.Unlock()

	for _, nav := range navs {
		nav.navUpdateBookmarked()
	}
}

// bookmarkQueryAdd creates a new bookmark query for a prop.
// C: bookmark_query_add (navigator.c:1417-1433)
//
//	Creates BookmarkQuery, subscribes to "node.key" path,
//	creates "value" prop on p for linking to bookmark root.
func (nav *Navigator) bookmarkQueryAdd(p *propcore.Prop) {
	if p == nil {
		return
	}
	bq := &BookmarkQuery{
		link:       nav.propManager.CreateEx(p, "value", nil, false, false),
		parentProp: p,
	}
	nav.bookmarkQueries = append(nav.bookmarkQueries, bq)

	// Subscribe to "key" for the query key.
	// C: PROP_TAG_NAMED_ROOT, p, "node" + PROP_TAG_NAME("node","key") —
	// "node" resolves to p itself (named-root convention), so the leaf is
	// p's child "key". The C name-vector subscription re-arms dynamically
	// when "key" appears/disappears; replicate with a watch on p for
	// child add/del plus a leaf value sub.
	attachKey := func(kp *propcore.Prop) {
		if bq.keySub != nil {
			bq.keySub.Unsubscribe()
		}
		bq.keySub = kp.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
			if event == propcore.EventSetRString || event == propcore.EventSetCString {
				if len(args) > 0 {
					if key, ok := args[0].(string); ok {
						nav.bookmarkQuerySetKey(bq, key)
					}
				}
			} else if event == propcore.EventSetVoid {
				nav.bookmarkQuerySetKey(bq, "")
			}
		}, nil, propcore.SubMutex{Ptr: &nav.mutex})
		if key := kp.GetString(); key != "" {
			nav.bookmarkQuerySetKey(bq, key)
		}
	}
	if kp := p.GetChild("key"); kp != nil {
		attachKey(kp)
	}
	// Dynamic re-arm: child "key" added/removed later (C name-vector
	// resolution does the same inside the prop machinery).
	bq.keyWatchSub = p.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		if len(args) == 0 {
			return
		}
		child, ok := args[0].(*propcore.Prop)
		if !ok || child == nil || child.GetName() != "key" {
			return
		}
		switch event {
		case propcore.EventAddChild, propcore.EventAddChildBefore:
			attachKey(child)
		case propcore.EventDelChild:
			if bq.keySub != nil {
				bq.keySub.Unsubscribe()
				bq.keySub = nil
			}
			nav.bookmarkQuerySetKey(bq, "")
		}
	}, nil, propcore.SubMutex{Ptr: &nav.mutex})
}

// bookmarkQuerySetKey links a bookmark query to a matching bookmark by URL.
// C: bookmark_query_set_key (navigator.c:1389-1410)
//
//	Sets bq->bq_key, finds matching bookmark by URL, links/unlinks prop.
func (nav *Navigator) bookmarkQuerySetKey(bq *BookmarkQuery, key string) {
	bq.key = key
	// Remove from old bookmark's query list
	if bq.bookmark != nil {
		for i, q := range bq.bookmark.queries {
			if q == bq {
				bq.bookmark.queries = slices.Delete(bq.bookmark.queries, i, i+1)
				break
			}
		}
		bq.bookmark = nil
	}
	// Find matching bookmark by URL
	var bm *Bookmark
	for _, b := range nav.bookmarks {
		if b.url == key {
			bm = b
			break
		}
	}
	bq.bookmark = bm
	if bm != nil {
		bm.queries = append(bm.queries, bq)
		// Link bookmark root to query's "value" prop
		// C: prop_link(bm->bm_root, bq->bq_link)
		bm.root.Link(bq.link)
	} else {
		// C: prop_unlink(bq->bq_link)
		bq.link.Unlink()
	}
}

// bookmarkQueryDel removes a bookmark query.
// C: bookmark_query_del (navigator.c:1440-1449)
func (nav *Navigator) bookmarkQueryDel(p *propcore.Prop) {
	for i, bq := range nav.bookmarkQueries {
		if bq.parentProp == p {
			// Unsubscribe key subs
			if bq.keySub != nil {
				bq.keySub.Unsubscribe()
			}
			if bq.keyWatchSub != nil {
				bq.keyWatchSub.Unsubscribe()
			}
			// Remove from bookmark's query list
			if bq.bookmark != nil {
				for j, q := range bq.bookmark.queries {
					if q == bq {
						bq.bookmark.queries = slices.Delete(bq.bookmark.queries, j, j+1)
						break
					}
				}
			}
			// Unlink
			bq.link.Unlink()
			nav.propManager.RefDec(bq.link)
			// Remove from global list
			nav.bookmarkQueries = slices.Delete(nav.bookmarkQueries, i, i+1)
			return
		}
	}
}
