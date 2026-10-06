// Package notifications is the canonical port of src/notifications.c —
// prop-node-based popup notifications, message/text dialogs, and the
// "news" subtree with htsmsg_store("dismissed_news") persistence.
// C uses file-scope globals; Go keeps them on NotificationManager.
package notifications

import (
	"fmt"
	"sync"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// NotifyType — C: notify_type_t (notifications.h)
type NotifyType int

const (
	NotifyInfo NotifyType = iota
	NotifyWarning
	NotifyError
)

// Message popup flags — C: MESSAGE_POPUP_* (notifications.h)
const (
	MessagePopupRichText = 0x1
	MessagePopupOK       = 0x2
	MessagePopupCancel   = 0x4
)

// NotificationManager — C: the file-scope globals of notifications.c
// (news_mutex, dismissed_news_in/out, notify_prop_entries).
type NotificationManager struct {
	newsMutex         sync.Mutex // C: news_mutex
	dismissedNewsIn   *htsmsg.HTSMsg
	store             *htsmsg.Store // C: global htsmsg_store — injected
	dismissedNewsOut  *htsmsg.HTSMsg
	notifyPropEntries *propcore.Prop
	pm                *propcore.PropManager

	eventMgr *event.EventManager

	calloutSystem *callout.CalloutSystem // C: callout_* globals — injected
	ts            *trace.TraceSystem     // C: trace() global — injected
}

// NewNotificationManager — C: notifications_init (notifications.c:45-56).
// Creates global.notifications + global.notifications.nodes and loads
// htsmsg_store("dismissed_news") from the global store.
func NewNotificationManager(pm *propcore.PropManager,
	store *htsmsg.Store, cs *callout.CalloutSystem, ts *trace.TraceSystem) *NotificationManager {
	nm := &NotificationManager{
		pm:            pm,
		eventMgr:      event.NewEventManager(pm),
		store:         store,
		calloutSystem: cs,
		ts:            ts,
	}

	// C: prop_t *root = prop_create(prop_get_global(), "notifications")
	root := pm.Create("notifications")

	// C: htsmsg_store_load("dismissed_news") ?: htsmsg_create_map()
	if nm.store != nil {
		if msg, err := nm.store.Load("dismissed_news"); err == nil && msg != nil {
			nm.dismissedNewsIn = msg
		}
	}
	if nm.dismissedNewsIn == nil {
		nm.dismissedNewsIn = htsmsg.NewMap()
	}
	nm.dismissedNewsOut = htsmsg.NewMap()

	// C: notify_prop_entries = prop_create(root, "nodes")
	nm.notifyPropEntries = pm.CreateEx(root, "nodes", nil, false, false)

	return nm
}

// Fini — C: notifications_fini (notifications.c:59-67). Saves
// dismissed_news_out to the store and releases it.
func (nm *NotificationManager) Fini() {
	if nm == nil {
		return
	}
	nm.newsMutex.Lock()
	if nm.dismissedNewsOut != nil {
		if nm.store != nil {
			nm.store.Save(nm.dismissedNewsOut, "dismissed_news")
		}
		nm.dismissedNewsOut.Release()
		nm.dismissedNewsOut = nil
	}
	nm.newsMutex.Unlock()

	// C has no callout teardown for notifications — notify_timeout
	// runs on the single global callout list (callout.c). The
	// NotificationManager shares that system; it does not own it.
}

// notifyTimeout — C: notify_timeout (notifications.c:76-82). Destroys
// the notification prop and releases the reference.
func (nm *NotificationManager) notifyTimeout(c *callout.Callout, opaque any) {
	p, _ := opaque.(*propcore.Prop)
	if p == nil {
		return
	}
	nm.pm.Destroy(p)
	p.Release()
}

// NotifyAdd — C: notify_add (notifications.c:88-140). Creates a popup
// prop {text,type,icon?} under root (or notify_prop_entries), and arms
// a timeout when delay != 0 (negative delay returns a retained handle).
func (nm *NotificationManager) NotifyAdd(root *propcore.Prop, notifyType NotifyType, icon string, delay int, format string, args ...any) any {
	if nm == nil {
		return nil
	}

	var typeStr string
	switch notifyType {
	case NotifyInfo:
		typeStr = "info"
	case NotifyWarning:
		typeStr = "warning"
	case NotifyError:
		typeStr = "error"
	default:
		return nil
	}

	// C: tracev(0, tl, "notify", rstr_get(fmt), ap) + vsnprintf
	message := fmt.Sprintf(format, args...)
	tl := trace.TRACE_INFO
	if notifyType == NotifyError {
		tl = trace.TRACE_ERROR
	}
	nm.ts.Trace(tl, "notify", "%s", message)

	// C: p = prop_create_root(NULL)
	p := nm.pm.CreateRoot("")

	// C: prop_set_string(prop_create(p, "text"), msg)
	nm.pm.SetStringEx(nm.pm.CreateEx(p, "text", nil, false, false), nil, message, propcore.StringUTF8)
	// C: prop_set_string(prop_create(p, "type"), typestr)
	nm.pm.SetStringEx(nm.pm.CreateEx(p, "type", nil, false, false), nil, typeStr, propcore.StringUTF8)
	if icon != "" {
		nm.pm.SetStringEx(nm.pm.CreateEx(p, "icon", nil, false, false), nil, icon, propcore.StringUTF8)
	}

	// C: p = prop_ref_inc(p)
	p.Retain()

	// C: if(prop_set_parent(p, root ?: notify_prop_entries)) prop_destroy(p)
	parent := root
	if parent == nil {
		parent = nm.notifyPropEntries
	}
	if nm.pm.SetParentEx(p, parent, nil, "") != 0 {
		nm.pm.Destroy(p)
	}

	if delay != 0 {
		var r *propcore.Prop
		if delay < 0 {
			r = p
			r.Retain()
			delay = -delay
		}
		// C: callout_arm(NULL, notify_timeout, p, delay) — the
		// single global callout list; looked up at arm time like C.
		cs := nm.calloutSystem
		if cs == nil {
			nm.pm.Destroy(p)
			p.Release()
			return nil
		}
		c := &callout.Callout{}
		cs.Arm(c, func(co *callout.Callout, opaque any) {
			nm.notifyTimeout(co, opaque)
		}, p, delay)
		if r != nil {
			return r
		}
	}
	return p
}

// NotifyDestroy — C: notify_destroy (notifications.c:145-150).
func NotifyDestroy(pm *propcore.PropManager, handle any) {
	if handle == nil {
		return
	}
	if p, ok := handle.(*propcore.Prop); ok {
		pm.Destroy(p)
		p.Release()
	}
}

// PopupDisplay — C: popup_display (notifications.c:191-212). Subscribes
// a TRACK_DESTROY eventsink on p.eventSink, reparents p under
// global.popups, and waits on a courier for the UI's event.
func (nm *NotificationManager) PopupDisplay(p *propcore.Prop) *event.Event {
	if nm == nil || p == nil {
		return nil
	}

	pc := propcore.NewCourier("popup") // C: prop_courier_create_waitable()
	var e *event.Event

	r := nm.pm.CreateEx(p, "eventSink", nil, false, false)
	sub := r.Subscribe(func(opaque any, eventType propcore.EventType, args ...any) {
		switch eventType {
		case propcore.EventExtEvent:
			// C: if(*ep) event_release(*ep); atomic_inc; *ep = e
			if len(args) > 0 {
				if ev, ok := args[0].(*event.Event); ok {
					if e != nil {
						e.Release()
					}
					ev.AddRef()
					e = ev
				}
			}
		case propcore.EventDestroyed:
			// C: if(*ep) event_release(*ep);
			//    *ep = event_create_action(ACTION_CANCEL)
			if e != nil {
				e.Release()
			}
			ev := nm.eventMgr.CreateAction(event.ACTION_CANCEL)
			e = &ev.Event
		}
	}, nil, propcore.SubFlagTrackDestroy, propcore.SubCourier{C: pc})

	// C: if(prop_set_parent(p, prop_create(global, "popups"))) abort()
	popups := nm.pm.Create("popups")
	if nm.pm.SetParentEx(p, popups, nil, "") != 0 {
		panic("popup root is a zombie")
	}

	// C: while(e == NULL) prop_courier_wait_and_dispatch(pc)
	for e == nil {
		pc.Wait()
	}

	nm.pm.Unsubscribe(sub)
	pc.Release() // C: prop_courier_destroy — sub's ref released by Unsubscribe
	return e
}

// MessagePopup — C: message_popup (notifications.c:223-278). Builds a
// {type:"message", message, buttons?[{title,action}], cancel?, ok?} prop
// and waits for the popup event; returns OK/CANCEL/btn index.
func (nm *NotificationManager) MessagePopup(message string, flags int, extra []string) int {
	if nm == nil {
		return 0
	}

	// C: p = prop_ref_inc(prop_create_root(NULL))
	p := nm.pm.CreateRoot("")
	p.Retain()

	nm.ts.Trace(trace.TRACE_DEBUG, "Notification", "%s", message)

	nm.pm.SetStringEx(nm.pm.CreateEx(p, "type", nil, false, false), nil, "message", propcore.StringUTF8)
	strType := propcore.StringUTF8
	if flags&MessagePopupRichText != 0 {
		strType = propcore.StringRich
	}
	nm.pm.SetStringEx(nm.pm.CreateEx(p, "message", nil, false, false), nil, message, strType)

	if len(extra) > 0 {
		cnt := 1
		btns := nm.pm.CreateEx(p, "buttons", nil, false, false)
		for _, label := range extra {
			b := nm.pm.CreateRoot("")
			nm.pm.SetStringEx(nm.pm.CreateEx(b, "title", nil, false, false), nil, label, propcore.StringUTF8)
			action := fmt.Sprintf("btn%d", cnt)
			nm.pm.SetStringEx(nm.pm.CreateEx(b, "action", nil, false, false), nil, action, propcore.StringUTF8)
			if nm.pm.SetParentEx(b, btns, nil, "") != 0 {
				panic("button reparent failed")
			}
			cnt++
		}
	}

	if flags&MessagePopupCancel != 0 {
		nm.pm.CreateEx(p, "cancel", nil, false, false).SetInt(1)
	}
	if flags&MessagePopupOK != 0 {
		nm.pm.CreateEx(p, "ok", nil, false, false).SetInt(1)
	}

	e := nm.PopupDisplay(p)
	nm.pm.Destroy(p)
	p.Release()

	var rval int
	switch {
	case e == nil:
		rval = 0
	case e.IsAction(event.ACTION_OK):
		rval = MessagePopupOK
	case e.IsAction(event.ACTION_CANCEL):
		rval = MessagePopupCancel
	default:
		// C: EVENT_DYNAMIC_ACTION && !strncmp(payload, "btn", 3) → atoi(payload+3)
		rval = 0
		if e.GetType() == event.EVENT_DYNAMIC_ACTION &&
			len(e.Payload) >= 3 && e.Payload[:3] == "btn" {
			fmt.Sscanf(e.Payload[3:], "%d", &rval)
		}
	}
	if e != nil {
		e.Release()
	}
	return rval
}

// TextDialog — C: text_dialog (notifications.c:283-323). Builds a
// {type:"textDialog", message, input, cancel?, ok?} popup; on ACTION_OK
// reads the "input" prop into *answer. Returns 0 or -1 on cancel.
func (nm *NotificationManager) TextDialog(message string, answer *string, flags int) int {
	if nm == nil {
		return -1
	}
	if answer != nil {
		*answer = ""
	}

	p := nm.pm.CreateRoot("")
	p.Retain()

	nm.pm.SetStringEx(nm.pm.CreateEx(p, "type", nil, false, false), nil, "textDialog", propcore.StringUTF8)
	strType := propcore.StringUTF8
	if flags&MessagePopupRichText != 0 {
		strType = propcore.StringRich
	}
	nm.pm.SetStringEx(nm.pm.CreateEx(p, "message", nil, false, false), nil, message, strType)
	input := nm.pm.CreateEx(p, "input", nil, false, false)
	if flags&MessagePopupCancel != 0 {
		nm.pm.CreateEx(p, "cancel", nil, false, false).SetInt(1)
	}
	if flags&MessagePopupOK != 0 {
		nm.pm.CreateEx(p, "ok", nil, false, false).SetInt(1)
	}

	e := nm.PopupDisplay(p)

	if e != nil && e.IsAction(event.ACTION_OK) && answer != nil {
		*answer = input.GetString()
	}

	nm.pm.Destroy(p)
	p.Release()
	if e != nil && e.IsAction(event.ACTION_CANCEL) {
		e.Release()
		return -1
	}
	if e != nil {
		e.Release()
	}
	return 0
}

// dismisNews — C: dismis_news (notifications.c:327-336). Records the id
// in dismissed_news_out (persisted) and destroys global.news.<id>.
func (nm *NotificationManager) dismisNews(id string) {
	nm.dismissedNewsOut.AddU32(id, 1)
	if nm.store != nil {
		nm.store.Save(nm.dismissedNewsOut, "dismissed_news")
	}
	root := nm.pm.Create("news")
	nm.pm.DestroyByName(root, id)
}

// newsSink — C: news_sink (notifications.c:340-372). Handles
// PROP_DESTROYED (unsub+release) and PROP_EXT_EVENT dynamic action
// "dismiss" → dismis_news + prop_destroy.
func (nm *NotificationManager) newsSink(opaque any, eventType propcore.EventType, args ...any) {
	p, _ := opaque.(*propcore.Prop)
	if p == nil {
		return
	}
	switch eventType {
	case propcore.EventDestroyed:
		p.Release()
	case propcore.EventExtEvent:
		if len(args) == 0 {
			return
		}
		e, _ := args[0].(*event.Event)
		if e == nil || e.GetType() != event.EVENT_DYNAMIC_ACTION {
			return
		}
		if e.Payload == "dismiss" {
			nm.newsMutex.Lock()
			// C: rstr_t *id = prop_get_string(p, "id", NULL)
			var id string
			if ip := nm.pm.Find(p, "id"); ip != nil {
				id = ip.GetString()
			}
			nm.dismisNews(id)
			nm.newsMutex.Unlock()
			nm.pm.Destroy(p)
		}
	}
}

// AddNewsLocked — C: add_news_locked (notifications.c:379-408). Creates
// global.news.<id> = {message,id,location,caption,action} with a
// TRACK_DESTROY news_sink sub, unless the id was previously dismissed.
func (nm *NotificationManager) addNewsLocked(id, message, location, caption, action string) *propcore.Prop {
	var ret *propcore.Prop
	root := nm.pm.Create("news")

	if nm.dismissedNewsOut != nil {
		// C: htsmsg_get_u32_or_default(dismissed_news_in, id, 0)
		if v, err := nm.dismissedNewsIn.GetU32(id); err == nil && v != 0 {
			nm.dismisNews(id)
		} else {
			p := nm.pm.CreateRoot(id)
			pm := nm.pm
			pm.SetStringEx(pm.CreateEx(p, "message", nil, false, false), nil, message, propcore.StringUTF8)
			pm.SetStringEx(pm.CreateEx(p, "id", nil, false, false), nil, id, propcore.StringUTF8)
			pm.SetStringEx(pm.CreateEx(p, "location", nil, false, false), nil, location, propcore.StringUTF8)
			pm.SetStringEx(pm.CreateEx(p, "caption", nil, false, false), nil, caption, propcore.StringUTF8)
			pm.SetStringEx(pm.CreateEx(p, "action", nil, false, false), nil, action, propcore.StringUTF8)

			p.Retain()
			es := pm.CreateEx(p, "eventSink", nil, false, false)
			es.Subscribe(func(op any, et propcore.EventType, a ...any) {
				nm.newsSink(op, et, a...)
			}, p, propcore.SubFlagTrackDestroy)
			ret = p
			p.Retain()
			if nm.pm.SetParentEx(p, root, nil, "") != 0 {
				nm.pm.Destroy(p)
			}
		}
	}
	return ret
}

// AddNews — C: add_news (notifications.c:414-422).
func (nm *NotificationManager) AddNews(id, message, location, caption string) *propcore.Prop {
	if nm == nil {
		return nil
	}
	nm.newsMutex.Lock()
	p := nm.addNewsLocked(id, message, location, caption, "")
	nm.newsMutex.Unlock()
	return p
}

// NotificationsFini — C: notifications_fini.
func NotificationsFini(nm *NotificationManager) {
	if nm != nil {
		nm.Fini()
	}
}
