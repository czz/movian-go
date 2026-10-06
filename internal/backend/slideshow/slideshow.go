// Package slideshow is a 1:1 port of src/backend/slideshow/slideshow.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package slideshow

import (
	"slices"
	"strconv"
	"unsafe"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// C: TAILQ_HEAD(slideshow_item_queue, slideshow_item)
// The queue is a slice; TAILQ_* operations are the index helpers below.

// C: typedef struct slideshow (slideshow.c:39-64)
type Slideshow struct {
	ssLockmgr misc.Lockmgr // C: ss_lockmgr — MUST be the first member:
	// C relies on ss == &ss->ss_lockmgr (PROP_TAG_MUTEX/lockmgr opaque).

	ssNodeSub  *ssSub // C: ss_node_sub
	ssEventSub *ssSub // C: ss_event_sub
	ssHoldSub  *ssSub // C: ss_hold_sub

	ssModel *propcore.Prop // C: ss_model
	ssNodes *propcore.Prop // C: ss_nodes

	ssSpeed     int            // C: ss_speed — seconds between switches
	ssSpeedProp *propcore.Prop // C: ss_speed_prop

	ssItems []*slideshowItem // C: struct slideshow_item_queue ss_items

	ssStart *slideshowItem // C: ss_start

	ssCurrent *slideshowItem // C: ss_current

	ssLoaded bool // C: ss_loaded — if we don't get any updates for 1 second we set this
	ssHold   int  // C: ss_hold — hold by UI

	ssCallout callout.Callout // C: ss_callout

	ssSpeedSetting *settingscore.Setting // C: ss_speed_setting

	// Go-only context: C uses the global prop manager and settings store.
	pm *propcore.PropManager
	sm *settingscore.SettingsManager
	cs *callout.CalloutSystem // C: callout_* globals — injected
}

// C: typedef struct slideshow_item (slideshow.c:70-85)
// ssi_link is implicit in the ss_items slice.
type slideshowItem struct {
	ssiOutputRoot *propcore.Prop // C: ssi_output_root

	ssiIsImage int        // C: ssi_is_image
	ssiUrl     *misc.Rstr // C: ssi_url

	ssiSource *propcore.Prop // C: ssi_source

	ssiSubType *ssSub // C: ssi_sub_type
	ssiSubUrl  *ssSub // C: ssi_sub_url

	ssiSs *Slideshow // C: ssi_ss
}

// ssSub mirrors one C prop_sub_t created by prop_subscribe(): C attaches a
// subscription to BOTH the canonical prop (path resolved without following
// links — only ever delivers PROP_DESTROYED, and only under
// PROP_SUB_TRACK_DESTROY) and the value prop (link-resolved leaf — delivers
// all other events). Go splits the two attachment points into two
// Subscriptions; together they are one C prop_sub_t.
type ssSub struct {
	value     *propcore.Subscription // C: hps_value_prop side
	canonical *propcore.Subscription // C: hps_canonical_prop side
}

// slideshowPropLockmgr is the lock manager for slideshow subscriptions.
// C: PROP_TAG_LOCKMGR, lockmgr_handler + PROP_TAG_MUTEX, ss — subs are
// PROP_SUB_DISPATCH_MODE_GLOBAL: notifications run on the global dispatch
// workers holding ss->ss_lockmgr.
var slideshowPropLockmgr = &propcore.Lockmgr{Fn: func(ptr any, op propcore.LockmgrOp) int {
	return misc.LockmgrHandler(ptr.(*misc.Lockmgr), int(op))
}}

// slideshowLockmgrFn — C: lockmgr_handler passed to callout_arm_managed.
// The callout passes its opaque (ss) as the lockmgr ptr; ss == &ss->ss_lockmgr
// because ss_lockmgr is the first member of slideshow_t.
type slideshowLockmgrFn struct{}

func (slideshowLockmgrFn) Lock(opaque any) {
	misc.LockmgrHandler(&opaque.(*Slideshow).ssLockmgr, misc.LOCKMGR_LOCK)
}
func (slideshowLockmgrFn) Unlock(opaque any) {
	misc.LockmgrHandler(&opaque.(*Slideshow).ssLockmgr, misc.LOCKMGR_UNLOCK)
}
func (slideshowLockmgrFn) Retain(opaque any) {
	misc.LockmgrHandler(&opaque.(*Slideshow).ssLockmgr, misc.LOCKMGR_RETAIN)
}
func (slideshowLockmgrFn) Release(opaque any) {
	misc.LockmgrHandler(&opaque.(*Slideshow).ssLockmgr, misc.LOCKMGR_RELEASE)
}

// TAILQ operations on ss_items (C: sys/queue.h macros)

// C: TAILQ_FIRST(&ss->ss_items)
func tailqFirst(ss *Slideshow) *slideshowItem {
	if len(ss.ssItems) == 0 {
		return nil
	}
	return ss.ssItems[0]
}

// C: TAILQ_LAST(&ss->ss_items, slideshow_item_queue)
func tailqLast(ss *Slideshow) *slideshowItem {
	if len(ss.ssItems) == 0 {
		return nil
	}
	return ss.ssItems[len(ss.ssItems)-1]
}

func tailqIndex(ss *Slideshow, ssi *slideshowItem) int {
	for i, x := range ss.ssItems {
		if x == ssi {
			return i
		}
	}
	return -1
}

// C: TAILQ_NEXT(ssi, ssi_link)
func tailqNext(ss *Slideshow, ssi *slideshowItem) *slideshowItem {
	i := tailqIndex(ss, ssi)
	if i < 0 || i+1 >= len(ss.ssItems) {
		return nil
	}
	return ss.ssItems[i+1]
}

// C: TAILQ_PREV(ssi, slideshow_item_queue, ssi_link)
func tailqPrev(ss *Slideshow, ssi *slideshowItem) *slideshowItem {
	i := tailqIndex(ss, ssi)
	if i <= 0 {
		return nil
	}
	return ss.ssItems[i-1]
}

// C: TAILQ_INSERT_TAIL(&ss->ss_items, ssi, ssi_link)
func tailqInsertTail(ss *Slideshow, ssi *slideshowItem) {
	ss.ssItems = append(ss.ssItems, ssi)
}

// C: TAILQ_INSERT_BEFORE(before, ssi, ssi_link)
func tailqInsertBefore(ss *Slideshow, before, ssi *slideshowItem) {
	i := tailqIndex(ss, before)
	if i < 0 {
		i = len(ss.ssItems)
	}
	ss.ssItems = append(ss.ssItems, nil)
	copy(ss.ssItems[i+1:], ss.ssItems[i:])
	ss.ssItems[i] = ssi
}

// C: TAILQ_REMOVE(&ss->ss_items, ssi, ssi_link)
func tailqRemove(ss *Slideshow, ssi *slideshowItem) {
	i := tailqIndex(ss, ssi)
	if i < 0 {
		return
	}
	ss.ssItems = slices.Delete(ss.ssItems, i, i+1)
}

// propSubfind — C: prop_subfind (prop_core.c:2682-2740).
// For each name component: if followLinks, walk the originator chain
// (C: while(follow_symlinks && p->hp_originator) p = p->hp_originator);
// a non-DIR prop is converted from VOID to DIR, otherwise subfind fails;
// the named child is found or created via prop_create0.
func (ss *Slideshow) propSubfind(root *propcore.Prop, path []string, followLinks bool) *propcore.Prop {
	p := root
	for _, name := range path {
		if p == nil {
			return nil
		}
		if followLinks {
			for {
				o := p.GetOriginator()
				if o == nil {
					break
				}
				p = o
			}
		}
		if p.GetPropType() != propcore.PropTypeDir {
			if p.GetPropType() != propcore.PropTypeVoid {
				return nil // C: return NULL
			}
			ss.pm.MakeDir(p) // C: TAILQ_INIT(&p->hp_childs); p->hp_type = PROP_DIR; prop_notify_value(p)
		}
		c := p.GetChild(name) // own children; p's originator is already nil
		if c == nil {
			// C: prop_create0(p, name, NULL, 0)
			c = ss.pm.CreateEx(p, name, nil, false, false)
			if c == nil {
				return nil
			}
		}
		p = c
	}
	if p != nil && followLinks {
		// C: trailing originator walk (prop_core.c:2734-2738)
		for {
			o := p.GetOriginator()
			if o == nil {
				break
			}
			p = o
		}
	}
	return p
}

// propSubscribe — C: prop_subscribe(flags, PROP_TAG_NAME(names...),
// PROP_TAG_NAMED_ROOT(root, alias), PROP_TAG_MUTEX ss,
// PROP_TAG_LOCKMGR lockmgr_handler, cb) — prop_core.c:3130-3210.
//
// The value prop is resolved with prop_subfind(follow_symlinks=1), the
// canonical prop with prop_subfind(follow_symlinks=0) from the named root.
// Notifications are enqueued on the slideshow courier (C: GLOBAL dispatch)
// and the callback runs under ss's lockmgr lock (C: PROP_TAG_MUTEX +
// PROP_TAG_LOCKMGR wrapping each invocation).
func (ss *Slideshow) propSubscribe(root *propcore.Prop, path []string,
	trackDestroy bool,
	cb func(ev propcore.EventType, args []any)) *ssSub {

	// C: name[0] is the named-root alias — resolve the rest from root.
	names := path[1:]

	// C: value = prop_subfind(nroot, name + 1, 1)
	value := ss.propSubfind(root, names, true)

	sub := &ssSub{}

	// C: PROP_TAG_MUTEX, ss + PROP_TAG_LOCKMGR, lockmgr_handler — the
	// subscription's lock is retained/released by prop_subscribe itself
	// (C: hps_lockmgr LOCKMGR_RETAIN/RELEASE) and held during dispatch.
	subArgs := []any{
		propcore.SubMutex{Ptr: &ss.ssLockmgr},
		propcore.SubLockmgr{L: slideshowPropLockmgr},
	}

	if value != nil {
		// C: value-side subscription — all events but PROP_DESTROYED,
		// which on the value prop means silent detach under TRACK_DESTROY.
		flags := slices.Insert(subArgs, 0)
		if trackDestroy {
			flags = append(flags, propcore.SubFlagTrackDestroy)
		}
		sub.value = value.Subscribe(
			func(opaque any, ev propcore.EventType, args ...any) {
				if trackDestroy && ev == propcore.EventDestroyed {
					return // C: value side detaches silently
				}
				cb(ev, args)
			}, nil, flags...)
	}

	if trackDestroy {
		// C: canonical = prop_subfind(nroot, name + 1, 0) — resolve on the
		// canonical chain only (no link following); missing nodes are
		// created. CreateEx find-or-creates among the node's OWN children,
		// which is exactly the canonical (non-linked) side.
		canonical := root
		for _, name := range names {
			if canonical == nil {
				break
			}
			if canonical.GetPropType() != propcore.PropTypeDir &&
				canonical.GetPropType() != propcore.PropTypeVoid {
				canonical = nil // C: prop_subfind returns NULL
				break
			}
			canonical = ss.pm.CreateEx(canonical, name, nil, false, false)
		}

		if canonical != nil {
			sub.canonical = canonical.Subscribe(
				func(opaque any, ev propcore.EventType, args ...any) {
					// C: canonical subs only ever deliver PROP_DESTROYED.
					if ev != propcore.EventDestroyed {
						return
					}
					cb(ev, args)
				}, nil,
				append(slices.Insert(subArgs, 0),
					propcore.SubFlagTrackDestroy, propcore.SubNoInitialUpdate)...)
		} else {
			// C: if(canonical == NULL && flags & TRACK_DESTROY)
			//      prop_notify_destroyed(s) — fire immediately.
			misc.LockmgrHandler(&ss.ssLockmgr, misc.LOCKMGR_LOCK)
			cb(propcore.EventDestroyed, nil)
			misc.LockmgrHandler(&ss.ssLockmgr, misc.LOCKMGR_UNLOCK)
		}
	}
	return sub
}

// propUnsubscribe — C: prop_unsubscribe (prop_core.c:3568+).
// Detaches both sides; pending queued notifications are skipped because the
// subscriptions are deactivated (C: hps_zombie). The lockmgr reference
// taken at subscribe time is released by subRefDec (C: LOCKMGR_RELEASE).
func (ss *Slideshow) propUnsubscribe(sub *ssSub) {
	if sub == nil {
		return
	}
	if sub.value != nil {
		sub.value.Unsubscribe()
	}
	if sub.canonical != nil {
		sub.canonical.Unsubscribe()
	}
}

// C: trampoline_rstr (prop_core.c) — PROP_TAG_CALLBACK_RSTR.
// PROP_SET_RSTRING/PROP_SET_CSTRING/PROP_SET_URI deliver the string value;
// anything else (void) delivers NULL.
func trampolineRstr(ev propcore.EventType, args []any) *misc.Rstr {
	switch ev {
	case propcore.EventSetRString, propcore.EventSetCString:
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				return misc.RstrAllocStr(s)
			}
		}
		return nil
	case propcore.EventSetURI:
		// C: cb(opaque, p->hp_uri_title) — title rstring
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				return misc.RstrAllocStr(s)
			}
		}
		return nil
	default:
		return nil // C: cb(opaque, NULL)
	}
}

// C: trampoline_int (prop_core.c) — PROP_TAG_CALLBACK_INT.
// INT/FLOAT pass numerically; RSTRING/CSTRING go through atoi; anything else
// (including void and destroyed) delivers 0.
func trampolineInt(ev propcore.EventType, args []any) int {
	switch ev {
	case propcore.EventSetInt:
		if len(args) > 0 {
			if v, ok := args[0].(int); ok {
				return v
			}
		}
		return 0
	case propcore.EventSetFloat:
		if len(args) > 0 {
			switch v := args[0].(type) {
			case float32:
				return int(v)
			case float64:
				return int(v)
			}
		}
		return 0
	case propcore.EventSetRString, propcore.EventSetCString:
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				return misc.Atoi(s)
			}
		}
		return 0
	default:
		return 0
	}
}

// C: slideshow_release (slideshow.c:96-103)
// lm is &ss->ss_lockmgr; ss_lockmgr sits at offset 0 so lm == ss.
func slideshowRelease(lm *misc.Lockmgr) {
	ss := (*Slideshow)(unsafe.Pointer(lm))
	if misc.LockmgrRelease(&ss.ssLockmgr) != 0 {
		return
	}
	// C: free(ss)
}

// C: slideshow_advance (slideshow.c:110-148)
func slideshowAdvance(ss *Slideshow, reverse int) {
	ssi := ss.ssCurrent

	if ssi == nil {
		if reverse != 0 {
			ssi = tailqLast(ss)
		} else {
			ssi = tailqFirst(ss)
		}

		ss.ssCurrent = ssi
		if ssi == nil {
			return
		}
	}

	for {
		if reverse != 0 {
			ssi = tailqPrev(ss, ssi)
			if ssi == nil {
				ssi = tailqLast(ss)
			}
		} else {
			ssi = tailqNext(ss, ssi)
			if ssi == nil {
				ssi = tailqFirst(ss)
			}
		}

		if ssi == ss.ssCurrent {
			return // Wrapped around, don't advance
		}

		if ssi.ssiOutputRoot != nil {
			ss.ssCurrent = ssi
			ss.pm.SelectChildProp(ssi.ssiOutputRoot, nil) // C: prop_select
			ss.pm.SuggestFocus(ssi.ssiSource)             // C: prop_suggest_focus
			return
		}
	}
}

// C: slideshow_item_destroy (slideshow.c:155-175)
func slideshowItemDestroy(ssi *slideshowItem, noAdvance int) {
	ss := ssi.ssiSs
	if ss.ssStart == ssi {
		ss.ssStart = nil
	}

	if ss.ssCurrent == ssi {
		if noAdvance == 0 {
			slideshowAdvance(ss, 0)
		}
		ss.ssCurrent = nil
	}

	tailqRemove(ss, ssi)
	ss.pm.Destroy(ssi.ssiOutputRoot)   // C: prop_destroy
	misc.RstrRelease(ssi.ssiUrl)       // C: rstr_release
	ss.propUnsubscribe(ssi.ssiSubType) // C: prop_unsubscribe
	ss.propUnsubscribe(ssi.ssiSubUrl)  // C: prop_unsubscribe
	ss.pm.RefDec(ssi.ssiOutputRoot)    // C: prop_ref_dec
	ss.pm.RefDec(ssi.ssiSource)        // C: prop_ref_dec
	// C: free(ssi)
}

// C: slideshow_clear (slideshow.c:181-193)
// Remove all items, if 'all' is set we also remove the start item
func slideshowClear(ss *Slideshow, all int) {
	for ssi := tailqFirst(ss); ssi != nil; {
		next := tailqNext(ss, ssi) // C: next = TAILQ_NEXT(ssi, ssi_link)
		if all == 0 && ssi == ss.ssStart {
			ssi = next
			continue
		}
		ss.pm.TagClear(ssi.ssiSource, ss) // C: prop_tag_clear
		slideshowItemDestroy(ssi, all)
		ssi = next
	}
}

// C: slideshow_destroy (slideshow.c:200-212)
func slideshowDestroy(ss *Slideshow) {
	// C: setting_destroy(ss->ss_speed_setting) — sm is nil only in
	// environments without a settings manager (C's store is a global).
	if ss.sm != nil {
		ss.sm.Destroy(ss.ssSpeedSetting)
	}
	slideshowClear(ss, 1)
	ss.propUnsubscribe(ss.ssNodeSub)  // C: prop_unsubscribe
	ss.propUnsubscribe(ss.ssEventSub) // C: prop_unsubscribe
	ss.propUnsubscribe(ss.ssHoldSub)  // C: prop_unsubscribe
	ss.pm.Destroy(ss.ssModel)         // C: prop_destroy0
	ss.pm.RefDec(ss.ssModel)          // C: prop_ref_dec
	ss.pm.Destroy(ss.ssNodes)         // C: prop_destroy0
	ss.pm.RefDec(ss.ssNodes)          // C: prop_ref_dec
	ss.pm.RefDec(ss.ssSpeedProp)      // C: prop_ref_dec
	// C: callout_disarm(&ss->ss_callout)
	if cs := ss.cs; cs != nil {
		cs.Disarm(&ss.ssCallout)
	}
	if ss.ssStart != nil {
		panic("slideshow: ss_start != NULL at destroy") // C: assert(ss->ss_start == NULL)
	}
}

// C: ss_timer (slideshow.c:219-228)
func ssTimer(c *callout.Callout, opaque any) {
	ss := opaque.(*Slideshow)
	if !ss.ssLoaded {
		ss.ssLoaded = true
	} else {
		slideshowAdvance(ss, 0)
	}
	slideshowArm(ss)
}

// C: slideshow_arm (slideshow.c:236-243)
func slideshowArm(ss *Slideshow) {
	if ss.ssHold != 0 {
		return
	}
	delta := int64(1000000)
	if ss.ssLoaded {
		delta = int64(ss.ssSpeed) * 1000000
	}
	// C: callout_arm_managed(&ss->ss_callout, ss_timer, ss, delta, lockmgr_handler)
	if cs := ss.cs; cs != nil {
		cs.ArmManaged(&ss.ssCallout, ssTimer, ss, delta, slideshowLockmgrFn{}, "", 0)
	}
}

// C: ssi_get_before (slideshow.c:248-257)
func ssiGetBefore(ssi *slideshowItem) *propcore.Prop {
	before := tailqNext(ssi.ssiSs, ssi)
	for before != nil && before.ssiOutputRoot == nil {
		before = tailqNext(ssi.ssiSs, before)
	}
	if before != nil {
		return before.ssiOutputRoot
	}
	return nil
}

// C: ssi_update_order (slideshow.c:263-267)
func ssiUpdateOrder(ssi *slideshowItem) {
	ssi.ssiSs.pm.Move(ssi.ssiOutputRoot, ssiGetBefore(ssi)) // C: prop_move
}

// C: ssi_update_output (slideshow.c:273-316)
func ssiUpdateOutput(ssi *slideshowItem) {
	ss := ssi.ssiSs
	if ssi.ssiIsImage != 0 && ssi.ssiUrl != nil {

		if ss.ssStart != nil && misc.RstrEq(ss.ssStart.ssiUrl, ssi.ssiUrl) != 0 {

			// Got the initial item, steal it
			ssi.ssiOutputRoot = ss.ssStart.ssiOutputRoot
			if ss.ssStart.ssiOutputRoot == nil {
				panic("slideshow: start item output_root == NULL") // C: assert
			}
			ss.ssStart.ssiOutputRoot = nil

			if ss.ssCurrent == ss.ssStart {
				ss.ssCurrent = ssi
			}

			slideshowItemDestroy(ss.ssStart, 0)
		}

		if ssi.ssiOutputRoot == nil {
			// C: prop_ref_inc(prop_create_root(NULL))
			ssi.ssiOutputRoot = ss.pm.RefInc(ss.pm.CreateRootEx("", false))
			ss.pm.SetVEx(nil, ssi.ssiOutputRoot, "type", "image")                 // C: prop_set PROP_SET_STRING
			ss.pm.SetVEx(nil, ssi.ssiOutputRoot, "url", misc.RstrGet(ssi.ssiUrl)) // C: prop_set PROP_SET_RSTRING
			if ss.pm.SetParentEx(ssi.ssiOutputRoot, ss.ssNodes,
				ssiGetBefore(ssi), "") != 0 {
				ss.pm.RefDec(ssi.ssiOutputRoot)
				ssi.ssiOutputRoot = nil
			}
		} else {
			ss.pm.SetVEx(nil, ssi.ssiOutputRoot, "url", misc.RstrGet(ssi.ssiUrl)) // C: prop_set PROP_SET_RSTRING
		}

	} else {

		if ssi.ssiOutputRoot == nil {
			return
		}

		ss.pm.Destroy(ssi.ssiOutputRoot) // C: prop_destroy
		ssi.ssiOutputRoot = nil
	}
}

// C: ssi_set_url (slideshow.c:321-329)
func ssiSetUrl(opaque any, rstr *misc.Rstr) {
	ssi := opaque.(*slideshowItem)
	if misc.RstrEq(ssi.ssiUrl, rstr) != 0 {
		return
	}
	misc.RstrSet(&ssi.ssiUrl, rstr) // C: rstr_set
	ssiUpdateOutput(ssi)
}

// C: ssi_set_type (slideshow.c:335-344)
func ssiSetType(opaque any, rstr *misc.Rstr) {
	ssi := opaque.(*slideshowItem)
	isImage := 0
	if misc.RstrGet(rstr) == "image" { // C: !strcmp(rstr_get(rstr) ?: "", "image")
		isImage = 1
	}
	if ssi.ssiIsImage == isImage {
		return
	}
	ssi.ssiIsImage = isImage
	ssiUpdateOutput(ssi)
}

// C: slideshow_item_add (slideshow.c:350-388)
func slideshowItemAdd(ss *Slideshow, p *propcore.Prop, before *slideshowItem) {
	ssi := &slideshowItem{} // C: calloc(1, sizeof(slideshow_item_t))
	ssi.ssiSs = ss

	ss.pm.TagSet(p, ss, ssi) // C: prop_tag_set(p, ss, ssi)

	// C: prop_subscribe(0, PROP_TAG_NAME("self", "url"),
	//     PROP_TAG_CALLBACK_RSTR, ssi_set_url, ssi,
	//     PROP_TAG_LOCKMGR, lockmgr_handler, PROP_TAG_MUTEX, ss,
	//     PROP_TAG_NAMED_ROOT, p, "self", NULL)
	ssi.ssiSubUrl = ss.propSubscribe(p, []string{"self", "url"}, false,
		func(ev propcore.EventType, args []any) {
			r := trampolineRstr(ev, args)
			ssiSetUrl(ssi, r)
			misc.RstrRelease(r) // C: trampoline releases its temp rstr
		})

	// C: prop_subscribe(0, PROP_TAG_NAME("self", "type"),
	//     PROP_TAG_CALLBACK_RSTR, ssi_set_type, ssi, ...)
	ssi.ssiSubType = ss.propSubscribe(p, []string{"self", "type"}, false,
		func(ev propcore.EventType, args []any) {
			r := trampolineRstr(ev, args)
			ssiSetType(ssi, r)
			misc.RstrRelease(r)
		})

	if before != nil {
		tailqInsertBefore(ss, before, ssi)
	} else {
		tailqInsertTail(ss, ssi)
	}

	ssi.ssiSource = ss.pm.RefInc(p) // C: prop_ref_inc(p)

	if !ss.ssLoaded && ss.ssHold == 0 {
		// C: callout_arm_managed(&ss->ss_callout, ss_timer, ss, 1000000, lockmgr_handler)
		if cs := ss.cs; cs != nil {
			cs.ArmManaged(&ss.ssCallout, ssTimer, ss, 1000000, slideshowLockmgrFn{}, "", 0)
		}
	}
}

// C: slideshow_item_addv (slideshow.c:394-400)
func slideshowItemAddv(ss *Slideshow, pv []*propcore.Prop, before *slideshowItem) {
	for _, p := range pv { // C: prop_vec_get(pv, i)
		slideshowItemAdd(ss, p, before)
	}
}

// C: slideshow_item_del (slideshow.c:406-410)
func slideshowItemDel(ss *Slideshow, ssi *slideshowItem) {
	slideshowItemDestroy(ssi, 0)
}

// C: slideshow_item_move (slideshow.c:416-428)
func slideshowItemMove(ss *Slideshow, ssi, before *slideshowItem) {
	tailqRemove(ss, ssi)
	if before != nil {
		tailqInsertBefore(ss, before, ssi)
	} else {
		tailqInsertTail(ss, ssi)
	}
	if ssi.ssiOutputRoot != nil {
		ssiUpdateOrder(ssi)
	}
}

// C: slideshow_nodes (slideshow.c:435-501)
func slideshowNodes(ss *Slideshow, ev propcore.EventType, args []any) {
	var p1, p2 *propcore.Prop

	switch ev {

	case propcore.EventAddChild:
		// C: PROP_ADD_CHILD — va_arg prop_t *child
		p1, _ = args[0].(*propcore.Prop)
		slideshowItemAdd(ss, p1, nil)

	case propcore.EventAddChildBefore:
		// C: p1 = child, p2 = before-sibling (hpn_prop, hpn_prop_extra)
		p1, _ = args[0].(*propcore.Prop)
		p2, _ = args[2].(*propcore.Prop)
		b, _ := ss.pm.TagGet(p2, ss).(*slideshowItem) // C: prop_tag_get(p2, ss)
		slideshowItemAdd(ss, p1, b)

	case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		slideshowItemAddv(ss, vecProps(args), nil)

	case propcore.EventAddChildVectorBefore:
		pv := vecProps(args)
		p2, _ = args[len(args)-1].(*propcore.Prop)
		b, _ := ss.pm.TagGet(p2, ss).(*slideshowItem) // C: prop_tag_get
		slideshowItemAddv(ss, pv, b)

	case propcore.EventDelChild:
		p1, _ = args[0].(*propcore.Prop)
		ssi, _ := ss.pm.TagClear(p1, ss).(*slideshowItem) // C: prop_tag_clear
		slideshowItemDel(ss, ssi)

	case propcore.EventMoveChild:
		// C: p1 = child, p2 = before-sibling
		p1, _ = args[0].(*propcore.Prop)
		p2, _ = args[2].(*propcore.Prop)
		ssi, _ := ss.pm.TagGet(p1, ss).(*slideshowItem)
		var b *slideshowItem
		if p2 != nil {
			b, _ = ss.pm.TagGet(p2, ss).(*slideshowItem) // C: p2 ? prop_tag_get(p2, ss) : NULL
		}
		slideshowItemMove(ss, ssi, b)

	case propcore.EventSetDir, propcore.EventWantMoreChilds:
		// C: break

	case propcore.EventSetVoid:
		slideshowClear(ss, 0)

	case propcore.EventDestroyed:
		slideshowDestroy(ss)

	case propcore.EventHaveMoreChildsYes, propcore.EventHaveMoreChildsNo,
		propcore.EventSuggestFocus, propcore.EventSelectChild:
		// C: break

	default:
		// C: TRACE(TRACE_ERROR, "Slideshow", "Unhandleded prop event %d", event); abort();
		panic("Slideshow: Unhandleded prop event " + strconv.Itoa(int(ev)))
	}
}

// vecProps extracts a child vector from notification args.
// Go delivers []*Prop or *PropVec; C delivers prop_vec_t *.
func vecProps(args []any) []*propcore.Prop {
	for _, a := range args {
		switch v := a.(type) {
		case []*propcore.Prop:
			return v
		case *propcore.PropVec:
			out := make([]*propcore.Prop, 0, v.Len())
			for i := range v.Len() {
				out = append(out, v.Get(i))
			}
			return out
		}
	}
	return nil
}

// C: update_speed (slideshow.c:508-513)
func updateSpeed(ss *Slideshow) {
	ss.pm.SetIntEx(ss.ssSpeedProp, nil, ss.ssSpeed) // C: prop_set_int
	if ss.ssLoaded && ss.ssHold == 0 {
		// C: callout_rearm(&ss->ss_callout, ss->ss_speed * 1000000)
		if cs := ss.cs; cs != nil {
			cs.Rearm(&ss.ssCallout, int64(ss.ssSpeed)*1000000)
		}
	}
}

// C: slideshow_eventsink (slideshow.c:520-552)
func slideshowEventsink(ss *Slideshow, e *event.Event) {
	if e.IsAction(event.ACTION_INCR) {
		ss.ssSpeed = min(ss.ssSpeed+2, 7) // C: MIN(ss->ss_speed + 2, 7)
		updateSpeed(ss)
		tmp := strconv.Itoa(ss.ssSpeed) // C: snprintf(tmp, sizeof(tmp), "%d", ss->ss_speed)
		// C: setting_set(ss->ss_speed_setting, SETTING_MULTIOPT, tmp)
		ss.sm.SettingSet(ss.ssSpeedSetting, settingscore.SettingMultiOpt, tmp)
		return
	}

	if e.IsAction(event.ACTION_DECR) {
		ss.ssSpeed = max(ss.ssSpeed-2, 3) // C: MAX(ss->ss_speed - 2, 3)
		updateSpeed(ss)
		tmp := strconv.Itoa(ss.ssSpeed)
		// C: setting_set(ss->ss_speed_setting, SETTING_MULTIOPT, tmp)
		ss.sm.SettingSet(ss.ssSpeedSetting, settingscore.SettingMultiOpt, tmp)
		return
	}

	if e.IsAction(event.ACTION_LEFT) ||
		e.IsAction(event.ACTION_SEEK_BACKWARD) {
		slideshowAdvance(ss, 1)
		slideshowArm(ss)
	}

	if e.IsAction(event.ACTION_RIGHT) ||
		e.IsAction(event.ACTION_SEEK_FORWARD) {
		slideshowAdvance(ss, 0)
		slideshowArm(ss)
	}
}

// C: slideshow_set_hold (slideshow.c:559-568) — PROP_TAG_CALLBACK_INT
func slideshowSetHold(ss *Slideshow, x int) {
	ss.ssHold = x
	if ss.ssHold != 0 {
		// C: callout_disarm(&ss->ss_callout)
		if cs := ss.cs; cs != nil {
			cs.Disarm(&ss.ssCallout)
		}
	} else {
		slideshowArm(ss)
	}
}

// C: slideshow_set_speed (slideshow.c:574-579) — SETTING_CALLBACK
func slideshowSetSpeed(ss *Slideshow, v string) {
	ss.ssSpeed = misc.Atoi(v) // C: misc.Atoi(v)
	updateSpeed(ss)
}

// C: be_slideshow_open (slideshow.c:586-661)
func beSlideshowOpen(page *propcore.Prop, url string, syncFlag int,
	pm *propcore.PropManager, sm *settingscore.SettingsManager,
	cs *callout.CalloutSystem) int {
	url = url[len("slideshow:"):] // C: url += strlen("slideshow:")
	ss := &Slideshow{}            // C: calloc(1, sizeof(slideshow_t))
	ss.pm = pm
	ss.sm = sm
	ss.cs = cs

	ss.ssSpeed = 5
	// C: prop_create_multi(page, "slideshow", "speed", NULL)
	ss.ssSpeedProp = pm.CreateMultiPath(page, "slideshow", "speed")
	pm.SetIntEx(ss.ssSpeedProp, nil, ss.ssSpeed) // C: prop_set_int

	// C: prop_create_r — prop_create_ex + prop_ref_inc
	ss.ssModel = pm.RefInc(pm.CreateEx(page, "model", nil, false, false))
	ss.ssNodes = pm.RefInc(pm.CreateEx(ss.ssModel, "nodes", nil, false, false))

	// C: prop_set(ss->ss_model, "type", PROP_SET_STRING, "slideshow")
	pm.SetVEx(nil, ss.ssModel, "type", "slideshow")

	ssi := &slideshowItem{} // C: calloc(1, sizeof(slideshow_item_t))

	// C: prop_create_r(ss->ss_nodes, NULL)
	ssi.ssiOutputRoot = pm.RefInc(pm.CreateEx(ss.ssNodes, "", nil, false, false))
	pm.SetVEx(nil, ssi.ssiOutputRoot, "type", "image") // C: PROP_SET_STRING
	pm.SetVEx(nil, ssi.ssiOutputRoot, "url", url)      // C: PROP_SET_STRING
	pm.SelectChildProp(ssi.ssiOutputRoot, nil)         // C: prop_select

	ssi.ssiIsImage = 1
	ssi.ssiUrl = misc.RstrAllocStr(url) // C: rstr_alloc(url)
	ss.ssStart = ssi
	ss.ssCurrent = ssi
	ssi.ssiSs = ss
	ss.ssItems = nil // C: TAILQ_INIT(&ss->ss_items)
	tailqInsertTail(ss, ssi)

	// C: lockmgr_init(&ss->ss_lockmgr, &slideshow_release)
	misc.LockmgrSetup(&ss.ssLockmgr, slideshowRelease)

	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY,
	//     PROP_TAG_CALLBACK, slideshow_nodes, ss,
	//     PROP_TAG_LOCKMGR, lockmgr_handler, PROP_TAG_MUTEX, ss,
	//     PROP_TAG_NAMED_ROOT, page, "page",
	//     PROP_TAG_NAME("page", "previous", "parentModel", "nodes"), NULL)
	ss.ssNodeSub = ss.propSubscribe(page,
		[]string{"page", "previous", "parentModel", "nodes"}, true,
		func(ev propcore.EventType, args []any) {
			slideshowNodes(ss, ev, args)
		})

	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY,
	//     PROP_TAG_CALLBACK_EVENT, slideshow_eventsink, ss, ...,
	//     PROP_TAG_NAME("page", "slideshow", "eventSink"), NULL)
	ss.ssEventSub = ss.propSubscribe(page,
		[]string{"page", "slideshow", "eventSink"}, true,
		func(ev propcore.EventType, args []any) {
			// C: trampoline_event — only PROP_EXT_EVENT reaches the callback
			if ev != propcore.EventExtEvent {
				return
			}
			if len(args) > 0 {
				if e, ok := args[0].(*event.Event); ok {
					slideshowEventsink(ss, e)
				}
			}
		})

	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY,
	//     PROP_TAG_CALLBACK_INT, slideshow_set_hold, ss, ...,
	//     PROP_TAG_NAME("page", "slideshow", "hold"), NULL)
	ss.ssHoldSub = ss.propSubscribe(page,
		[]string{"page", "slideshow", "hold"}, true,
		func(ev propcore.EventType, args []any) {
			slideshowSetHold(ss, trampolineInt(ev, args))
		})

	// C: prop_t *opts = prop_create_r(ss->ss_model, "options")
	opts := pm.RefInc(pm.CreateEx(ss.ssModel, "options", nil, false, false))

	// C: setting_create(SETTING_MULTIOPT, opts,
	//     SETTINGS_INITIAL_UPDATE | SETTINGS_RAW_NODES,
	//     SETTING_TITLE(_p("Slideshow speed")), SETTING_VALUE("5"),
	//     SETTING_LOCKMGR(lockmgr_handler), SETTING_MUTEX(ss),
	//     SETTING_CALLBACK(slideshow_set_speed, ss),
	//     SETTING_OPTION("3", _p("3 seconds")),
	//     SETTING_OPTION("5", _p("5 seconds")),
	//     SETTING_OPTION("7", _p("7 seconds")), NULL)
	//
	// C: SETTING_LOCKMGR(lockmgr_handler) + SETTING_MUTEX(ss) — the value
	// subscription dispatches on the global queue holding ss's lockmgr;
	// the callback body assumes the lock is already held (C: slideshow.c).
	// sm may be nil in environments without a settings manager (tests);
	// C's setting store is a global that is always present.
	if sm != nil {
		ss.ssSpeedSetting = sm.SettingCreate(settingscore.SettingMultiOpt, opts,
			settingscore.SettingsInitialUpdate|settingscore.SettingsRawNodes,
			settingscore.SettingTagTitle, sm.P("Slideshow speed"),
			settingscore.SettingTagValue, "5",
			settingscore.SettingTagLockMgr, slideshowPropLockmgr,
			settingscore.SettingTagMutex, &ss.ssLockmgr,
			settingscore.SettingTagCallback,
			func(opaque any, value any) {
				s, _ := value.(string)
				slideshowSetSpeed(ss, s)
			}, ss,
			settingscore.SettingTagOption, "3", sm.P("3 seconds"),
			settingscore.SettingTagOption, "5", sm.P("5 seconds"),
			settingscore.SettingTagOption, "7", sm.P("7 seconds"),
			0)
	}

	slideshowRelease(&ss.ssLockmgr) // C: slideshow_release(ss)
	return 0
}

// C: be_slideshow_canhandle (slideshow.c:669-672)
func beSlideshowCanhandle(url string) int {
	if len(url) >= len("slideshow:") && url[:len("slideshow:")] == "slideshow:" {
		return 1
	}
	return 0
}

// CanHandle — C: be_slideshow.canhandle (slideshow.c:678)
func CanHandle(url string) int {
	return beSlideshowCanhandle(url)
}

// Open — C: be_slideshow.open → be_slideshow_open (slideshow.c:678).
// Returns 0 on success like the C function.
func Open(page *propcore.Prop, url string, syncFlag int,
	pm *propcore.PropManager, sm *settingscore.SettingsManager,
	cs *callout.CalloutSystem) int {
	return beSlideshowOpen(page, url, syncFlag, pm, sm, cs)
}
