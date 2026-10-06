// Package core — canonical port of src/video/video_playback.c.
//
// Go placement follows the established precedent (fa_audio.c / fa_video.c
// live in this package although C has them in src/fileaccess/): the
// orchestration needs backend_canhandle / backend_open / backend_normalize /
// backend_play_video, which live here, and C's src/video/ layering cannot
// be reproduced without an import cycle.
package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	event "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: main.h:190
const AppExitStandby = 10 // APP_EXIT_STANDBY

// C: video_settings.h:36-38
const (
	VideoResumeNo  = 0 // VIDEO_RESUME_NO
	VideoResumeYes = 1 // VIDEO_RESUME_YES
	VideoResumeAsk = 2 // VIDEO_RESUME_ASK
)

// video_playback_create reaches the BackendSystem through
// mp.Sys.Owner (C's implicit globals: the backend list + the prop
// system, both owned by the BackendSystem wired at init).

// ---------------------------------------------------------------------------
// vsource — C: vsource_t / struct vsource_list (video_playback.c:64-156)
// ---------------------------------------------------------------------------

// VSource — C: vsource_t (video_playback.c:72-79)
type VSource struct {
	URL      string // vs_url
	Mimetype string // vs_mimetype
	Bitrate  int    // vs_bitrate
	Flags    int    // vs_flags
}

// VSourceList — C: LIST_HEAD(vsource_list, vsource), sorted desc by bitrate.
type VSourceList []*VSource

// vsCmp — C: vs_cmp (video_playback.c:88-92) — descending bitrate.
func vsCmp(a, b *VSource) int {
	return b.Bitrate - a.Bitrate
}

// vsourceInsert — C: vsource_insert (video_playback.c:98-115)
// LIST_INSERT_SORTED: insert before first entry with vs_cmp(entry,vs) > 0.
func vsourceInsert(list *VSourceList, bs *BackendSystem, url, mimetype string,
	bitrate, flags int) {

	// C: backend_canhandle(url) — non-retained nil-check; Go's CanHandle
	// retains, so release the reference immediately.
	be := bs.CanHandle(url)
	if be == nil {
		return
	}
	bs.Release(be)

	vs := &VSource{URL: url, Mimetype: mimetype, Bitrate: bitrate, Flags: flags}

	l := *list
	i := len(l)
	for j, e := range l {
		if vsCmp(e, vs) > 0 {
			i = j
			break
		}
	}
	l = append(l, nil)
	copy(l[i+1:], l[i:])
	l[i] = vs
	*list = l
}

// vsourceDup — C: vsource_dup (video_playback.c:129-137)
func vsourceDup(src *VSource) *VSource {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

// vsourceCleanup — C: vsource_cleanup (video_playback.c:143-153)
func vsourceCleanup(list *VSourceList) {
	*list = nil
}

// ---------------------------------------------------------------------------
// video_queue — C: video_queue_t / video_queue_entry_t (video_playback.c:40-787)
// ---------------------------------------------------------------------------

// VideoQueueEntry — C: video_queue_entry_t (video_playback.c:44-53)
type VideoQueueEntry struct {
	root    *propcore.Prop // vqe_root
	url     string         // vqe_url
	typ     string         // vqe_type
	urlSub  *vqLeafSub     // vqe_url_sub
	typeSub *vqLeafSub     // vqe_type_sub
	vq      *VideoQueue    // vqe_vq
}

// VideoQueue — C: struct video_queue (video_playback.c:58-62)
type VideoQueue struct {
	// mu — C: static HTS_MUTEX_DECL(video_queue_mutex) (video_playback.c:37);
	// per-queue in Go (each vq's callbacks only touch its own state).
	mu          sync.Mutex
	nodeSub     *propcore.Subscription // vq_node_sub (model-level watch)
	nodesLeaf   *propcore.Subscription // armed "nodes" child subscription
	entries     []*VideoQueueEntry     // vq_entries (TAILQ → ordered slice)
	currentProp *propcore.Prop         // vq_current_prop
	current     *VideoQueueEntry       // vq_current
	mp          *mediacore.MediaPipe   // vq_mp
	pm          *propcore.PropManager
}

// vqEntryDestroy — C: vq_entry_destroy (video_playback.c:434-444)
// Caller holds vq.mu.
func vqEntryDestroy(vq *VideoQueue, vqe *VideoQueueEntry) {
	if vqe.urlSub != nil {
		vqe.urlSub.unsubscribe()
	}
	if vqe.typeSub != nil {
		vqe.typeSub.unsubscribe()
	}
	for i, e := range vq.entries {
		if e == vqe {
			vq.entries = slices.Delete(vq.entries, i, i+1)
			break
		}
	}
	if vq.pm != nil && vqe.root != nil {
		vq.pm.RefDec(vqe.root)
	}
}

// vqeByTag — C: prop_tag_get(p, vq) (video_playback.c usage)
func vqeByTag(vq *VideoQueue, p *propcore.Prop) *VideoQueueEntry {
	if vq.pm == nil || p == nil {
		return nil
	}
	v, _ := vq.pm.TagGet(p, "videoQueueEntry").(*VideoQueueEntry)
	return v
}

// vqUpdateMetadata — C: vq_update_metadata (video_playback.c:450-478)
// Caller holds vq.mu.
func vqUpdateMetadata(vq *VideoQueue) {
	pm := vq.pm
	if pm == nil {
		return
	}

	// Forward: next entry after current whose type is video/tvchannel.
	idx := -1
	if vq.current != nil {
		for i, e := range vq.entries {
			if e == vq.current {
				idx = i
				break
			}
		}
	}
	canFwd := false
	if idx >= 0 {
		for _, e := range vq.entries[idx+1:] {
			if e.typ == "video" || e.typ == "tvchannel" {
				canFwd = true
				break
			}
		}
	}
	pm.SetIntEx(vq.mp.PropCanSkipForward, nil, misc.BoolToInt(canFwd))

	// Backward: previous entry before current.
	canBwd := false
	if idx > 0 {
		for i := idx - 1; i >= 0; i-- {
			e := vq.entries[i]
			if e.typ == "video" || e.typ == "tvchannel" {
				canBwd = true
				break
			}
		}
	}
	pm.SetIntEx(vq.mp.PropCanSkipBackward, nil, misc.BoolToInt(canBwd))
}

// vqUpdateCurrent — C: vq_update_current (video_playback.c:484-503)
// Caller holds vq.mu.
func vqUpdateCurrent(vq *VideoQueue) {
	var vqe *VideoQueueEntry
	if vq.currentProp != nil && vq.pm != nil {
		for _, e := range vq.entries {
			if vq.pm.Compare(e.root, vq.currentProp) {
				vqe = e
				break
			}
		}
	}
	vq.current = vqe
	vqUpdateMetadata(vq)
}

// vqeSetURL — C: vqe_set_url (video_playback.c:509-513)
// Caller holds vq.mu.
func vqeSetURL(vqe *VideoQueueEntry, str string, has bool) {
	if has {
		vqe.url = str
	} else {
		vqe.url = ""
	}
	vqUpdateMetadata(vqe.vq)
}

// vqeSetType — C: vqe_set_type (video_playback.c:519-524)
// Caller holds vq.mu.
func vqeSetType(vqe *VideoQueueEntry, str string, has bool) {
	if has {
		vqe.typ = str
	} else {
		vqe.typ = ""
	}
	vqUpdateMetadata(vqe.vq)
}

// vqAddNode — C: vq_add_node (video_playback.c:530-563)
// Caller holds vq.mu.
func vqAddNode(vq *VideoQueue, p *propcore.Prop, before *VideoQueueEntry) {
	vqe := &VideoQueueEntry{vq: vq}

	// C: prop_tag_set(p, vq, vqe)
	if vq.pm != nil {
		vq.pm.TagSet(p, "videoQueueEntry", vqe)
		vqe.root = vq.pm.RefInc(p)
	} else {
		vqe.root = p
	}

	// C: vqe_url_sub — PROP_TAG_NAME("self","url") + PROP_TAG_CALLBACK_RSTR
	// + PROP_TAG_MUTEX video_queue_mutex. The named-root "self" resolves to
	// p itself; the leaf is p.url. If the leaf is absent the subscription
	// is armed lazily on the node's child events. Callbacks run under
	// vq.mu (C: PROP_TAG_MUTEX).
	vqe.urlSub = vq.subscribeChildString(p, "url", func(s string, has bool) {
		vqeSetURL(vqe, s, has)
	})

	// C: vqe_type_sub — PROP_TAG_NAME("self","type")
	vqe.typeSub = vq.subscribeChildString(p, "type", func(s string, has bool) {
		vqeSetType(vqe, s, has)
	})

	if before != nil {
		for i, e := range vq.entries {
			if e == before {
				vq.entries = append(vq.entries, nil)
				copy(vq.entries[i+1:], vq.entries[i:])
				vq.entries[i] = vqe
				vqUpdateCurrent(vq)
				return
			}
		}
	}
	vq.entries = append(vq.entries, vqe)
	vqUpdateCurrent(vq)
}

// vqLeafSub — a named-path leaf subscription (node watch + armed leaf).
// C: a single prop_subscribe whose named-path machinery owns both levels;
// prop_unsubscribe tears the whole thing down.
type vqLeafSub struct {
	node  *propcore.Subscription
	leaf  *propcore.Subscription
	child *propcore.Prop
}

func (s *vqLeafSub) unsubscribe() {
	if s == nil {
		return
	}
	if s.leaf != nil {
		s.leaf.Unsubscribe()
		s.leaf = nil
	}
	if s.node != nil {
		s.node.Unsubscribe()
		s.node = nil
	}
	s.child = nil
}

// subscribeChildString subscribes to a named leaf under p, delivering the
// string value (or !has on void/destroy). Mirrors the C named-path
// subscription PROP_TAG_NAME("self", name) + PROP_TAG_CALLBACK_RSTR: the
// leaf is re-resolved as children appear/disappear.
//
// The caller holds vq.mu; all subscription callbacks re-acquire
// it and the initial value is seeded inline (C delivers the initial update
// under PROP_TAG_MUTEX — SubNoInitialUpdate + inline seed is equivalent).
func (vq *VideoQueue) subscribeChildString(p *propcore.Prop, name string,
	cb func(s string, has bool)) *vqLeafSub {

	s := &vqLeafSub{}

	arm := func(c *propcore.Prop) {
		if s.leaf != nil {
			s.leaf.Unsubscribe()
		}
		s.child = c
		s.leaf = c.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
			vq.mu.Lock()
			str, has := vqEventString(ev, a)
			cb(str, has)
			vq.mu.Unlock()
		}, nil, propcore.SubNoInitialUpdate)
	}

	s.node = p.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
		vq.mu.Lock()
		defer vq.mu.Unlock()
		switch ev {
		case propcore.EventAddChild, propcore.EventAddChildBefore,
			propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
			if c := resolveChild(name, a); c != nil {
				arm(c)
				cb(c.GetString(), true)
			}
		case propcore.EventDelChild:
			if c := resolveChild(name, a); c != nil && c == s.child {
				if s.leaf != nil {
					s.leaf.Unsubscribe()
					s.leaf = nil
					s.child = nil
				}
				cb("", false)
			}
		case propcore.EventSetVoid, propcore.EventDestroyed:
			if s.leaf != nil {
				s.leaf.Unsubscribe()
				s.leaf = nil
				s.child = nil
			}
			cb("", false)
		}
	}, nil, propcore.SubNoInitialUpdate)

	if c := p.GetChild(name); c != nil {
		arm(c)
		cb(c.GetString(), true)
	} else {
		// C: an unresolved leaf delivers PROP_SET_VOID
		cb("", false)
	}
	return s
}

// vqEventString extracts the string value from a prop event (C: the rstring
// delivered to a PROP_TAG_CALLBACK_RSTR callback).
func vqEventString(ev propcore.EventType, args []any) (string, bool) {
	switch ev {
	case propcore.EventSetVoid, propcore.EventDestroyed:
		return "", false
	}
	for _, a := range args {
		switch v := a.(type) {
		case string:
			return v, true
		case *propcore.Prop:
			// value props carry the prop itself
			continue
		}
	}
	return "", false
}

// vqMoveNode — C: vq_move_node (video_playback.c:569-579)
// Caller holds vq.mu.
func vqMoveNode(vq *VideoQueue, vqe, before *VideoQueueEntry) {
	for i, e := range vq.entries {
		if e == vqe {
			vq.entries = slices.Delete(vq.entries, i, i+1)
			break
		}
	}
	if before != nil {
		for i, e := range vq.entries {
			if e == before {
				vq.entries = append(vq.entries, nil)
				copy(vq.entries[i+1:], vq.entries[i:])
				vq.entries[i] = vqe
				return
			}
		}
	}
	vq.entries = append(vq.entries, vqe)
}

// vqAddNodes — C: vq_add_nodes (video_playback.c:585-589)
// Caller holds vq.mu.
func vqAddNodes(vq *VideoQueue, pv *propcore.PropVec, before *VideoQueueEntry) {
	for i := range pv.Len() {
		vqAddNode(vq, pv.Get(i), before)
	}
}

// vqDelNode — C: vq_del_node (video_playback.c:595-601)
// Caller holds vq.mu.
func vqDelNode(vq *VideoQueue, vqe *VideoQueueEntry) {
	if vqe == nil {
		return
	}
	if vqe == vq.current {
		vq.current = nil
	}
	vqEntryDestroy(vq, vqe)
	vqUpdateMetadata(vq)
}

// vqClear — C: vq_clear (video_playback.c:607-618)
// Caller holds vq.mu.
func vqClear(vq *VideoQueue) {
	for len(vq.entries) > 0 {
		vqe := vq.entries[0]
		if vq.pm != nil {
			vq.pm.TagClear(vqe.root, "videoQueueEntry")
		}
		vqEntryDestroy(vq, vqe)
	}
	vq.current = nil
	vqUpdateMetadata(vq)
}

// vqEntriesCallback — C: vq_entries_callback (video_playback.c:624-688)
// Caller holds vq.mu (C: PROP_TAG_MUTEX).
func vqEntriesCallback(vq *VideoQueue, ev propcore.EventType, args ...any) {
	get := func(i int) *propcore.Prop {
		if i < len(args) {
			p, _ := args[i].(*propcore.Prop)
			return p
		}
		return nil
	}
	getVec := func(i int) *propcore.PropVec {
		if i < len(args) {
			pv, _ := args[i].(*propcore.PropVec)
			return pv
		}
		return nil
	}

	switch ev {
	case propcore.EventAddChild:
		vqAddNode(vq, get(0), nil)

	case propcore.EventAddChildBefore:
		vqAddNode(vq, get(0), vqeByTag(vq, get(1)))

	case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		if pv := getVec(0); pv != nil {
			vqAddNodes(vq, pv, nil)
		} else if i := args0Slice(args, 0); i != nil {
			for _, p := range i {
				vqAddNode(vq, p, nil)
			}
		}

	case propcore.EventDelChild:
		p1 := get(0)
		var vqe *VideoQueueEntry
		if vq.pm != nil {
			if v, ok := vq.pm.TagClear(p1, "videoQueueEntry").(*VideoQueueEntry); ok {
				vqe = v
			}
		}
		vqDelNode(vq, vqe)

	case propcore.EventMoveChild:
		vqMoveNode(vq, vqeByTag(vq, get(0)), vqeByTag(vq, get(1)))

	case propcore.EventSetDir, propcore.EventWantMoreChilds,
		propcore.EventHaveMoreChildsYes, propcore.EventHaveMoreChildsNo,
		propcore.EventSuggestFocus, propcore.EventDestroyed:
		// C: ignored events

	case propcore.EventSetVoid:
		vqClear(vq)
	}
}

// args0Slice handles EventAddChildVector deliveries that pass []*Prop
// instead of *PropVec.
func args0Slice(args []any, i int) []*propcore.Prop {
	if i < len(args) {
		v, _ := args[i].([]*propcore.Prop)
		return v
	}
	return nil
}

// VideoQueueCreate — C: video_queue_create (video_playback.c:693-705)
// Subscribes model.nodes — PROP_TAG_NAME("self","nodes") under named-root
// model: "self" resolves to model, then the "nodes" child is re-resolved
// as it appears/disappears.
func VideoQueueCreate(pm *propcore.PropManager, model *propcore.Prop,
	mp *mediacore.MediaPipe) *VideoQueue {

	vq := &VideoQueue{mp: mp, pm: pm}
	if model == nil {
		return vq
	}

	var nodesProp *propcore.Prop

	// arm attaches the entries subscription to a resolved "nodes" child.
	// Caller holds vq.mu (or is in the create path before any
	// concurrent access). The leaf sub uses SubNoInitialUpdate; the
	// current children are seeded as a synthetic ADD_CHILD_VECTOR so the
	// initial population flows through vq_entries_callback exactly like
	// C's initial update (which also runs under video_queue_mutex).
	arm := func(c *propcore.Prop) {
		if vq.nodesLeaf != nil {
			vq.nodesLeaf.Unsubscribe()
		}
		nodesProp = c
		vq.nodesLeaf = c.Subscribe(
			func(_ any, ev propcore.EventType, a ...any) {
				vq.mu.Lock()
				vqEntriesCallback(vq, ev, a...)
				vq.mu.Unlock()
			}, nil, propcore.SubNoInitialUpdate)

		if children := c.GetChildren(); len(children) > 0 {
			pv := propcore.PropVecCreate(len(children))
			for _, ch := range children {
				propcore.PropVecAppend(pv, ch)
			}
			vqAddNodes(vq, pv, nil)
			propcore.PropVecRelease(pv)
		}
	}

	// Watch model for the "nodes" child (re-resolves on add/del/void).
	vq.nodeSub = model.Subscribe(
		func(_ any, ev propcore.EventType, a ...any) {
			vq.mu.Lock()
			defer vq.mu.Unlock()
			switch ev {
			case propcore.EventAddChild, propcore.EventAddChildBefore,
				propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
				if c := resolveChild("nodes", a); c != nil {
					arm(c)
				}
			case propcore.EventDelChild:
				if c := resolveChild("nodes", a); c != nil && c == nodesProp {
					if vq.nodesLeaf != nil {
						vq.nodesLeaf.Unsubscribe()
						vq.nodesLeaf = nil
						nodesProp = nil
					}
					vqClear(vq)
				}
			case propcore.EventSetVoid, propcore.EventDestroyed:
				if vq.nodesLeaf != nil {
					vq.nodesLeaf.Unsubscribe()
					vq.nodesLeaf = nil
					nodesProp = nil
				}
				vqClear(vq)
			}
		}, nil, propcore.SubNoInitialUpdate)

	if c := model.GetChild("nodes"); c != nil {
		arm(c)
	}
	return vq
}

// VideoQueueSetCurrent — C: video_queue_set_current (video_playback.c:710-719)
func VideoQueueSetCurrent(vq *VideoQueue, item *propcore.Prop) {
	vq.mu.Lock()
	defer vq.mu.Unlock()
	if vq.pm != nil && vq.currentProp != nil {
		vq.pm.RefDec(vq.currentProp)
	}
	if vq.pm != nil && item != nil {
		vq.currentProp = vq.pm.RefInc(item)
	} else {
		vq.currentProp = item
	}
	vqUpdateCurrent(vq)
}

// VideoQueueDestroy — C: video_queue_destroy (video_playback.c:725-734)
func VideoQueueDestroy(vq *VideoQueue) {
	if vq == nil {
		return
	}
	vq.mu.Lock()
	defer vq.mu.Unlock()
	if vq.nodeSub != nil {
		vq.nodeSub.Unsubscribe()
	}
	if vq.nodesLeaf != nil {
		vq.nodesLeaf.Unsubscribe()
		vq.nodesLeaf = nil
	}
	if vq.pm != nil && vq.currentProp != nil {
		vq.pm.RefDec(vq.currentProp)
	}
	vqClear(vq)
}

// VideoQueueFindNext — C: video_queue_find_next (video_playback.c:740-785)
// Returns a followed prop (caller RefDec's) or nil.
func VideoQueueFindNext(vq *VideoQueue, current *propcore.Prop, reverse bool,
	wrap bool) *propcore.Prop {

	if current == nil {
		return nil
	}
	vq.mu.Lock()

	idx := -1
	for i, e := range vq.entries {
		if vq.pm != nil && vq.pm.Compare(e.root, current) {
			idx = i
			break
		}
	}

	var found *VideoQueueEntry
	if idx >= 0 {
		step := 1
		if reverse {
			step = -1
		}
		for i := idx + step; i >= 0 && i < len(vq.entries); i += step {
			e := vq.entries[i]
			if e.typ != "" {
				if e.typ != "video" && e.typ != "tvchannel" {
					continue
				}
			}
			found = e
			break
		}
	}

	var p *propcore.Prop
	if found != nil && vq.pm != nil {
		p = vq.pm.Follow(found.root)
	}
	vq.mu.Unlock()
	return p
}

// ---------------------------------------------------------------------------
// play_video — C: play_video (video_playback.c:159-433)
// ---------------------------------------------------------------------------

// propGetStringPath — C: prop_get_string(p, path...) resolving child names.
func propGetStringPath(p *propcore.Prop, path ...string) string {
	if p == nil {
		return ""
	}
	c := p.ResolvePath(path, true)
	if c == nil {
		return ""
	}
	return c.GetString()
}

// playVideo — C: play_video (video_playback.c:159-433)
// Returns the unconsumed event (for EVENT_REOPEN / playback events) or nil
// on error with errbuf set.
func playVideo(bs *BackendSystem, pm *propcore.PropManager, url string,
	mp *mediacore.MediaPipe, flags, priority int,
	vq *VideoQueue, parentURL, parentTitle string, origin *propcore.Prop,
	resumeMode int, loadRequestTimestamp int64) (*mediacore.MediaEvent, error) {

	if parentURL == "" {
		parentURL = ""
	}

	va := &VideoArgs{
		Episode:              -1,
		Season:               -1,
		Origin:               origin,
		Priority:             priority,
		ResumeMode:           resumeMode,
		LoadRequestTimestamp: loadRequestTimestamp,
	}

	var vsources VSourceList

	mediacore.MpReset(mp, pm)
	if pm != nil {
		pm.SetIntEx(pm.CreateMulti(mp.PropRoot, "loading"), nil, 1)
	}

	var e *mediacore.MediaEvent
	var m *htsmsg.HTSMsg
	var lastErr error // C: errbuf — last write wins
	canonicalURL := ""

	if !strings.HasPrefix(url, "videoparams:") {

		be := bs.CanHandle(url)
		if be == nil || be.PlayVideo == nil {
			p := pm.CreateRootEx("", true)
			if bs.Open(p, url, true) != nil {
				pm.Destroy(p)
				return nil, errors.New("No backend for URL")
			}

			typ := propGetStringPath(p, "model", "type")
			source := ""
			sourceSet := false // C: source != NULL

			if typ == "video" {
				bs.traceSystem.Trace(trace.TRACE_DEBUG, "vp",
					"Page %s is a video page. Waiting for source\n", url)

				pc := propcore.NewCourier("vp")

				// C: s1 — PROP_TAG_SET_RSTR &source on
				// PROP_TAG_NAME("self","source") under named-root p
				s1 := newCourierPathSub(pm, p, pc, []string{"source"},
					func(s string, has bool) {
						if has {
							source = s
							sourceSet = true
						} else {
							source = ""
							sourceSet = false
						}
					})

				// C: s2 — PROP_TAG_SET_RSTR &type on
				// PROP_TAG_NAME("self","model","type") under named-root p
				s2 := newCourierPathSub(pm, p, pc,
					[]string{"model", "type"},
					func(s string, has bool) {
						if has {
							typ = s
						} else {
							typ = ""
						}
					})

				deadline := time.Now().Add(10 * time.Second) // 10,000,000 us

				// C: while(type && type=="video" && source==NULL && now<deadline)
				for typ == "video" && !sourceSet &&
					time.Now().Before(deadline) {
					pc.WaitTimed(1000)
					pc.Poll()
				}

				s1.unsubscribe()
				s2.unsubscribe()
				pc.Destroy()

				if sourceSet {
					// C: recursion — note the C code passes
					// (parent_title, parent_url) swapped in argument order
					e, perr := playVideo(bs, pm, source, mp, flags, priority,
						vq, parentTitle, parentURL,
						origin, resumeMode, loadRequestTimestamp)
					pm.Destroy(p)
					return e, perr
				}
			}

			if typ == "openerror" {
				errStr := propGetStringPath(p, "model", "error")
				title := propGetStringPath(p, "model", "metadata", "title")

				if mp.PropMetadata != nil {
					pm.SetStringEx(
						pm.CreateMulti(mp.PropMetadata, "title"),
						nil, title, propcore.StringType(propcore.PropStrUTF8))
				}
				if errStr == "" {
					errStr = "Unable to open URL"
				}
				pm.Destroy(p)
				return nil, errors.New(errStr)
			}
			pm.Destroy(p)
			return nil, fmt.Errorf(
				"Page model for '%s' does not provide sufficient data", url)
		}

		va.CanonicalURL = url
		canonicalURL = url
		va.ParentTitle = parentTitle
		va.ParentURL = parentURL
		va.Flags = flags | BackendVideoSetTitle

		var perr error
		e0, perr := be.PlayVideo(url, mp, vq, &vsources, va)
		e = toMediaEvent(e0)
		if be != nil {
			bs.Release(be)
		}
		if perr != nil {
			lastErr = perr
		}

	} else {

		url = url[len("videoparams:"):]
		var err error
		m, err = htsmsg.DeserializeJSON(url)
		if m == nil || err != nil {
			return nil, errors.New("Invalid JSON")
		}

		canonicalURL = m.GetStr("canonicalUrl")

		// Metadata

		if str := m.GetStr("title"); str != "" {
			if mp.PropMetadata != nil {
				pm.SetStringEx(
					pm.CreateMulti(mp.PropMetadata, "title"),
					nil, str, propcore.StringType(propcore.PropStrUTF8))
			}
			va.Title = str
		} else {
			flags |= BackendVideoSetTitle
		}

		if str := m.GetStr("icon"); str != "" {
			if mp.PropMetadata != nil {
				pm.SetStringEx(
					pm.CreateMulti(mp.PropMetadata, "icon"),
					nil, str, propcore.StringType(propcore.PropStrUTF8))
			}
		}

		if u32, uerr := m.GetU32("year"); uerr == nil {
			if mp.PropMetadata != nil {
				pm.SetIntEx(
					pm.CreateMulti(mp.PropMetadata, "year"),
					nil, int(u32))
			}
			va.Year = int(u32)
		}
		if u32, uerr := m.GetU32("season"); uerr == nil {
			if mp.PropMetadata != nil {
				pm.SetIntEx(
					pm.CreateMulti(mp.PropMetadata, "season"),
					nil, int(u32))
			}
			va.Season = int(u32)
		}
		if u32, uerr := m.GetU32("episode"); uerr == nil {
			if mp.PropMetadata != nil {
				pm.SetIntEx(
					pm.CreateMulti(mp.PropMetadata, "episode"),
					nil, int(u32))
			}
			va.Episode = int(u32)
		}

		if str := m.GetStr("imdbid"); str != "" {
			va.IMDB = str
		}

		// Sources

		sources := m.GetList("sources")
		if sources == nil {
			return nil, errors.New("No sources list in JSON parameters")
		}

		for _, f := range sources.GetFields() {
			src := f.GetMap()
			if src == nil {
				continue
			}
			srcURL := src.GetStr("url")
			mimetype := src.GetStr("mimetype")
			bitrate := int(int32(src.GetU32OrDefault("bitrate", 0xFFFFFFFF))) // C: -1
			if srcURL == "" {
				continue
			}
			vsourceInsert(&vsources, bs, srcURL, mimetype, bitrate,
				BackendVideoNoFSScan)
		}

		if len(vsources) == 0 {
			vsourceCleanup(&vsources)
			return nil, errors.New("No players found for sources")
		}

		// Subtitles

		if subs := m.GetList("subtitles"); subs != nil {
			for _, f := range subs.GetFields() {
				sub := f.GetMap()
				if sub == nil {
					continue
				}
				title := sub.GetStr("title")
				subURL := sub.GetStr("url")
				lang := sub.GetStr("language")
				source := sub.GetStr("source")

				mediacore.MpAddTrack(pm, mp.PropSubtitleTracks,
					title, subURL, "", "", lang, source, nil, 90000, 1)
			}
		}

		// Quality choices — each entry {label, url} becomes a child of
		// metadata.qualities so the video page can offer a quality
		// menu that re-opens the backend URL (C: not present upstream;
		// generic so any backend may provide it).

		if quals := m.GetList("qualities"); quals != nil && mp.PropMetadata != nil {
			qp := pm.CreateMulti(mp.PropMetadata, "qualities")
			for _, f := range quals.GetFields() {
				qm := f.GetMap()
				if qm == nil {
					continue
				}
				label := qm.GetStr("label")
				qurl := qm.GetStr("url")
				if qurl == "" {
					continue
				}
				c := pm.CreateRootEx("", true)
				pm.SetStringEx(pm.CreateMulti(c, "title"),
					nil, label, propcore.StringType(propcore.PropStrUTF8))
				pm.SetStringEx(pm.CreateMulti(c, "url"),
					nil, qurl, propcore.StringType(propcore.PropStrUTF8))
				if pm.SetParentEx(c, qp, nil, "") != 0 {
					pm.Destroy(c)
				}
			}
		}

		// Check if we should disable filesystem scanning (subtitles)
		if m.GetU32OrDefault("no_fs_scan", 0) != 0 {
			flags |= BackendVideoNoFSScan
		}

		// Subtitle scanning can be turned off completely
		if m.GetU32OrDefault("no_subtitle_scan", 0) != 0 {
			flags |= BackendVideoNoSubtitleScan
		}

		vs := vsources[0]

		if canonicalURL == "" {
			canonicalURL = vs.URL
		}

		bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video", "Playing %s", vs.URL)

		vsd := vsourceDup(vs)

		va.CanonicalURL = canonicalURL
		va.Flags = flags | vsd.Flags
		va.Mimetype = vsd.Mimetype
		va.ParentTitle = parentTitle
		va.ParentURL = parentURL

		var perr error
		var e0 any
		e0, perr = bs.PlayVideo(vsd.URL, mp, vq, &vsources, va)
		e = toMediaEvent(e0)
		if perr != nil {
			lastErr = perr
		}
	}

	for e != nil {

		if meIsType(e, event.EVENT_REOPEN) {

			if len(vsources) == 0 {
				lastErr = errors.New("No alternate video sources")
				e = nil
				break
			}

			vs := vsourceDup(vsources[0])
			bs.traceSystem.Trace(trace.TRACE_DEBUG, "Video", "Playing %s", vs.URL)

			va.CanonicalURL = canonicalURL
			va.Flags = flags | vs.Flags
			va.Mimetype = vs.Mimetype

			var perr error
			var e0 any
			e0, perr = bs.PlayVideo(vs.URL, mp, vq, &vsources, va)
			e = toMediaEvent(e0)
			if perr != nil {
				lastErr = perr
			}
		} else {
			break
		}
	}

	vsourceCleanup(&vsources)
	return e, lastErr
}

// courierPathSub — C: prop_subscribe(PROP_TAG_NAMED_ROOT, root, "self",
// PROP_TAG_NAME(path...), PROP_TAG_SET_RSTR, &dst, PROP_TAG_COURIER, pc).
//
// Re-resolves each path segment as children appear/disappear, delivering
// the leaf's string value (or !has on void) through the courier so all
// callbacks run serialized inside the wait loop's dispatch.
type courierPathSub struct {
	pm      *propcore.PropManager
	courier *propcore.Courier
	cb      func(s string, has bool)
	path    []string

	watches  []*propcore.Subscription // per-level watch on the parent
	nodes    []*propcore.Prop         // resolved node per level
	leaf     *propcore.Subscription
	leafNode *propcore.Prop
	dead     bool
}

func newCourierPathSub(pm *propcore.PropManager, root *propcore.Prop,
	courier *propcore.Courier, path []string,
	cb func(s string, has bool)) *courierPathSub {

	s := &courierPathSub{pm: pm, courier: courier, path: path, cb: cb}
	s.arm(root, 0)
	return s
}

// arm attaches the watch for path[depth] under parent.
func (s *courierPathSub) arm(parent *propcore.Prop, depth int) {
	if s.dead || parent == nil {
		return
	}
	if depth == len(s.path) {
		// Terminal: subscribe to the leaf prop's value.
		s.leafNode = parent
		s.leaf = s.pm.SubscribeWithCourier(parent, s.courier,
			func(_ any, ev propcore.EventType, a ...any) {
				str, has := vqEventString(ev, a)
				s.cb(str, has)
			}, nil)
		return
	}

	name := s.path[depth]
	d := depth
	sub := s.pm.SubscribeWithCourier(parent, s.courier,
		func(_ any, ev propcore.EventType, a ...any) {
			s.watchEvent(d, name, ev, a)
		}, nil)
	for len(s.watches) <= d {
		s.watches = append(s.watches, nil)
		s.nodes = append(s.nodes, nil)
	}
	s.watches[d] = sub
	s.nodes[d] = parent

	// Resolve an already-existing child.
	if c := parent.GetChild(name); c != nil {
		s.arm(c, d+1)
	} else {
		// C: an unresolved leaf delivers PROP_SET_VOID
		s.cb("", false)
	}
}

// resolveChild finds a named child in a child-delivery event arg set.
func resolveChild(name string, a []any) *propcore.Prop {
	for _, x := range a {
		switch v := x.(type) {
		case *propcore.Prop:
			if v != nil && v.GetName() == name {
				return v
			}
		case *propcore.PropVec:
			for i := range v.Len() {
				if c := v.Get(i); c != nil && c.GetName() == name {
					return c
				}
			}
		case []*propcore.Prop:
			for _, c := range v {
				if c != nil && c.GetName() == name {
					return c
				}
			}
		}
	}
	return nil
}

// watchEvent handles node events at a watch level.
func (s *courierPathSub) watchEvent(depth int, name string,
	ev propcore.EventType, a []any) {
	if s.dead {
		return
	}
	switch ev {
	case propcore.EventAddChild, propcore.EventAddChildBefore,
		propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		if c := resolveChild(name, a); c != nil {
			s.teardownFrom(depth + 1)
			s.arm(c, depth+1)
		}
	case propcore.EventDelChild:
		if c := resolveChild(name, a); c != nil {
			s.teardownFrom(depth + 1)
			s.cb("", false)
		}
	case propcore.EventSetVoid, propcore.EventDestroyed:
		s.teardownFrom(depth + 1)
		s.cb("", false)
	}
}

// teardownFrom unsubscribes the leaf and all watches at depth >= d.
func (s *courierPathSub) teardownFrom(d int) {
	if s.leaf != nil {
		s.leaf.Unsubscribe()
		s.leaf = nil
		s.leafNode = nil
	}
	for i := d; i < len(s.watches); i++ {
		if s.watches[i] != nil {
			s.watches[i].Unsubscribe()
			s.watches[i] = nil
			s.nodes[i] = nil
		}
	}
}

// unsubscribe — C: prop_unsubscribe.
func (s *courierPathSub) unsubscribe() {
	s.dead = true
	s.teardownFrom(0)
}

// meIsType — C: event_is_type
func meIsType(e *mediacore.MediaEvent, t event.EventType) bool {
	return e != nil && e.Type == int(t)
}

// toMediaEvent wraps a backend's raw event return in a MediaEvent.
func toMediaEvent(v any) *mediacore.MediaEvent {
	switch ev := v.(type) {
	case nil:
		return nil
	case *mediacore.MediaEvent:
		return ev
	case *event.Event:
		if ev == nil {
			return nil
		}
		return &mediacore.MediaEvent{Type: int(ev.Type), Data: ev.Concrete()}
	case interface{ AsEvent() *event.Event }:
		be := ev.AsEvent()
		if be == nil {
			return nil
		}
		return &mediacore.MediaEvent{Type: int(be.Type), Data: ev}
	}
	return nil
}

// ---------------------------------------------------------------------------
// video_player_idle — C: video_player_idle (video_playback.c:788-978)
// ---------------------------------------------------------------------------

// videoPlayerIdle — C: video_player_idle (video_playback.c:788-978)
// The per-mp playback orchestration loop. Consumes mp events, drives
// play_video, and handles EOF/continuous-play/skip track advancement.
func videoPlayerIdle(mp *mediacore.MediaPipe, bs *BackendSystem,
	pm *propcore.PropManager) {

	run := true
	var e *mediacore.MediaEvent
	errStr := "" // C: char errbuf[256] — persists across iterations
	errprop := pm.RefInc(pm.CreateEx(mp.PropRoot, "error", nil, true, false))
	var vq *VideoQueue
	playFlagsPermanent := 0
	playPriority := 0
	playURL := ""
	forceContinuous := false
	var itemModel *propcore.Prop
	parentURL := ""
	parentTitle := ""
	var loadRequestTimestamp int64

	const (
		resumeNo = iota
		resumeAsGlobalSetting
		resumeYes
	)
	resumeCtrl := resumeNo

	for run {

		if playURL != "" {
			errprop.SetVoid()

			playFlags := playFlagsPermanent

			resumeMode := VideoResumeNo
			switch resumeCtrl {
			case resumeNo:
			case resumeAsGlobalSetting:
				if bs.videoSettings != nil {
					resumeMode, _ = bs.videoSettings()
				}
			case resumeYes:
				resumeMode = VideoResumeYes
			}

			resumeCtrl = resumeNo // For next item during continuous play

			bs.traceSystem.Trace(trace.TRACE_DEBUG, "vp",
				"Playing '%s'%s%s, resume:%s%s",
				playURL,
				map[bool]string{true: ", primary"}[playFlags&BackendVideoPrimary != 0],
				map[bool]string{true: ", no-audio"}[playFlags&BackendVideoNoAudio != 0],
				map[int]string{VideoResumeYes: "yes",
					VideoResumeNo: "no"}[resumeModeOr(resumeMode)],
				// C: resume_ctrl was already reset above, so the
				// ternary always yields " (overridden)" upstream.
				map[bool]string{false: " (overridden)"}[resumeCtrl == resumeAsGlobalSetting])

			if mp.PropMetadata != nil {
				pm.SetVoidEx(pm.CreateMulti(mp.PropMetadata, "title"), nil)
			}
			if vq != nil {
				VideoQueueSetCurrent(vq, itemModel)
			}
			var perr error
			e, perr = playVideo(bs, pm, playURL, mp, playFlags, playPriority,
				vq, parentURL, parentTitle,
				itemModel, resumeMode, loadRequestTimestamp)
			if perr != nil {
				errStr = perr.Error()
				bs.traceSystem.Trace(trace.TRACE_DEBUG, "vp",
					"playVideo failed for %s: %s", playURL, errStr)
			}
			mediacore.MpBumpEpoch(mp)
			pm.SetIntEx(pm.CreateMulti(mp.PropRoot, "loading"), nil, 0)
			if e == nil {
				errprop.SetString(errStr)
			} else {
				if mp.PropMetadata != nil {
					pm.SetVoidEx(mp.PropMetadata.ResolvePath(
						[]string{"title"}, false), nil)
				}
			}
		}

		if e == nil {
			bs.traceSystem.Trace(trace.TRACE_DEBUG, "vp", "Waiting for event")
			playURL = ""
			e = mediacore.MpDequeueEvent(mp)
			if e == nil {
				break
			}
		}
		loadRequestTimestamp = eventTimestamp(e)

		if meIsType(e, event.EVENT_EOF) && mp.AutoStandby != 0 {
			if bs.appShutdown != nil {
				bs.appShutdown(AppExitStandby)
			}
			e = nil
			break
		}

		if meIsType(e, event.EVENT_PLAY_URL) {

			forceContinuous = false
			errprop.SetVoid()

			ep := playURLEvent(e)
			playFlagsPermanent = 0
			if ep != nil && ep.Primary {
				playFlagsPermanent |= BackendVideoPrimary
			}
			if ep != nil && ep.NoAudio {
				playFlagsPermanent |= BackendVideoNoAudio
			}
			if ep != nil {
				playPriority = ep.Priority
			}

			if pm != nil && itemModel != nil {
				pm.RefDec(itemModel)
				itemModel = nil
			}
			if vq != nil {
				VideoQueueDestroy(vq)
				vq = nil
			}

			parentURL = ""
			if ep != nil && ep.ParentURL != "" {
				parentURL = bs.Normalize(ep.ParentURL)
			}

			parentTitle = ""

			var epParentModel *propcore.Prop
			var epItemModel *propcore.Prop
			if ep != nil {
				epParentModel, _ = ep.ParentModel.(*propcore.Prop)
				epItemModel, _ = ep.ItemModel.(*propcore.Prop)
			}

			if epParentModel != nil {
				x := pm.Follow(epParentModel)
				parentTitle = propGetStringPath(x, "metadata", "title")
				pm.RefDec(x)
			}

			itemModel = pm.RefInc(epItemModel)
			if epParentModel != nil && itemModel != nil {
				vq = VideoQueueCreate(pm, epParentModel, mp)
			} else {
				vq = nil
			}
			if ep != nil {
				playURL = ep.URL
			} else {
				playURL = ""
			}

			resumeCtrl = resumeAsGlobalSetting

			if ep != nil && ep.How != "" {
				switch ep.How {
				case "beginning":
					resumeCtrl = resumeNo
				case "resume":
					resumeCtrl = resumeYes
				case "continuous":
					forceContinuous = true
				}
			}

		} else if meIsType(e, event.EVENT_EXIT) {
			e = nil
			break

		} else if meIsType(e, event.EVENT_EOF) ||
			mediacoreIsAction(e, event.ACTION_SKIP_FORWARD) ||
			mediacoreIsAction(e, event.ACTION_SKIP_BACKWARD) {

			// Try to figure out which track to play next
			var next *propcore.Prop

			skp := mediacoreIsAction(e, event.ACTION_SKIP_FORWARD) ||
				mediacoreIsAction(e, event.ACTION_SKIP_BACKWARD)

			continuousPlayback := 0
			if bs.videoSettings != nil {
				_, continuousPlayback = bs.videoSettings()
			}
			if vq != nil && (continuousPlayback != 0 || forceContinuous || skp) {
				next = VideoQueueFindNext(vq, itemModel,
					mediacoreIsAction(e, event.ACTION_SKIP_BACKWARD), false)
			}

			if pm != nil && itemModel != nil {
				pm.RefDec(itemModel)
			}
			itemModel = nil

			playURL = ""

			if next != nil {
				// C: play_url = prop_get_string(next, "url", NULL)
				playURL = propGetStringPath(next, "url")
				itemModel = next
				pm.SuggestFocus(itemModel)
			}
			if playURL == "" && mp.PropPlayStatus != nil {
				mp.PropPlayStatus.SetString("stop")
			}
		}

		e = nil
	}

	if vq != nil {
		VideoQueueDestroy(vq)
	}
	if pm != nil && itemModel != nil {
		pm.RefDec(itemModel)
	}
	pm.RefDec(errprop)
	mediacore.MpShutdown(mp)
	mediacore.MpRelease(mp)
}

// resumeModeOr clamps the C switch default ("ask user") for the trace.
func resumeModeOr(m int) int {
	if m == VideoResumeYes || m == VideoResumeNo {
		return m
	}
	return VideoResumeAsk
}

// playURLEvent extracts the EventPlayURL payload from a MediaEvent.
func playURLEvent(e *mediacore.MediaEvent) *event.EventPlayURL {
	switch v := e.Data.(type) {
	case *event.EventPlayURL:
		return v
	}
	return nil
}

// mediacoreIsAction — C: event_is_action on a MediaEvent.
func mediacoreIsAction(e *mediacore.MediaEvent, at event.ActionType) bool {
	if e == nil {
		return false
	}
	if c, ok := e.Data.(interface{ IsAction(event.ActionType) bool }); ok {
		return c.IsAction(at)
	}
	return false
}

// eventTimestamp — C: e->e_timestamp.
func eventTimestamp(e *mediacore.MediaEvent) int64 {
	if e == nil {
		return 0
	}
	if v, ok := e.Data.(interface{ AsEvent() *event.Event }); ok {
		if be := v.AsEvent(); be != nil {
			return be.Timestamp
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// video_playback_create / destroy — C: video_playback.c:981-997
// ---------------------------------------------------------------------------

// VideoPlaybackCreate — C: video_playback_create (video_playback.c:981-987)
// hts_thread_create_detached("video player", video_player_idle,
// mp_retain(mp), THREAD_PRIO_DEMUXER).
func VideoPlaybackCreate(mp *mediacore.MediaPipe) {
	bs, _ := mp.Sys.Owner.(*BackendSystem)
	if bs == nil || bs.GetPropManager() == nil {
		return
	}
	mediacore.MpRetain(mp)
	go videoPlayerIdle(mp, bs, bs.GetPropManager())
}

// VideoPlaybackDestroy — C: video_playback_destroy (video_playback.c:993-998)
func VideoPlaybackDestroy(mp *mediacore.MediaPipe) {
	mediacore.MpEnqueueEvent(mp, &mediacore.MediaEvent{
		Type: int(event.EVENT_EXIT),
		Data: &event.Event{Type: event.EVENT_EXIT},
	})
}
