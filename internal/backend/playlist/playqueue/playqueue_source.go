// Package playqueue is a strict C-to-Go port of src/playqueue.c.
//
// C's playqueue is a process-global singleton (static lists, one
// media_pipe, one player thread). In Go, the PlayQueue struct carries the
// C file-scope globals as fields; NewPlayQueue performs playqueue_init.
package playqueue

import (
	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

// watchNamedChild — C: prop_subscribe(PROP_TAG_NAME(root, name),
// PROP_TAG_CALLBACK_*, cb, PROP_TAG_NAMED_ROOT, node, root,
// PROP_TAG_MUTEX, &playqueue_mutex).
// cb receives leaf value events; runs under pq.mu — held by the dispatch
// worker (C: the subscription's hps_lock = playqueue_mutex is acquired
// around the callback by prop_dispatch_one).
func (pq *PlayQueue) watchNamedChild(node *propcore.Prop, name string,
	cb func(ev propcore.EventType, args []any)) *pqNamedChildSub {

	w := &pqNamedChildSub{}
	var leafChild *propcore.Prop

	arm := func(c *propcore.Prop) {
		leafChild = c
		w.setLeaf(c.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
			// C: callback runs with playqueue_mutex held by the dispatch
			// worker — invoke the body directly (mutex serialization
			// replaces the old unlocked-dispatch dq deferral).
			cb(ev, a)
		}, nil, propcore.SubMutex{Ptr: &pq.mu}))
	}

	// Parent watch: re-arm when the named child appears/disappears.
	// Subscribing the parent first gives the initial child enumeration
	// via AddChild events (Go props deliver existing children as
	// initial add notifications). C: runs with playqueue_mutex held by
	// the dispatch worker — invoke the body directly.
	w.parent = node.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
		{
			var child *propcore.Prop
			if len(a) > 0 {
				child, _ = a[0].(*propcore.Prop)
			}
			switch ev {
			case propcore.EventAddChild, propcore.EventAddChildBefore:
				if child != nil && child.GetName() == name {
					arm(child)
				}
			case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
				for _, x := range a {
					switch vec := x.(type) {
					case []*propcore.Prop:
						for _, c := range vec {
							if c.GetName() == name {
								arm(c)
							}
						}
					case *propcore.PropVec:
						for i := range vec.Len() {
							if c := vec.Get(i); c != nil && c.GetName() == name {
								arm(c)
							}
						}
					}
				}
			case propcore.EventDelChild, propcore.EventDestroyed, propcore.EventSetVoid:
				w.mu.Lock()
				leaf := w.leaf
				w.mu.Unlock()
				if leaf != nil && (ev != propcore.EventDelChild || child == leafChild) {
					w.setLeaf(nil)
					leafChild = nil
					// C: lock already held by the dispatcher.
					cb(propcore.EventSetVoid, nil)
				}
			}
		}
	}, nil, propcore.SubMutex{Ptr: &pq.mu})
	return w
}

func (w *pqNamedChildSub) setLeaf(l *propcore.Subscription) {
	w.mu.Lock()
	old := w.leaf
	w.leaf = l
	w.mu.Unlock()
	if old != nil {
		old.Unsubscribe()
	}
}

// Unsubscribe — C: prop_unsubscribe on the whole named-path sub.
func (w *pqNamedChildSub) Unsubscribe() {
	if w == nil {
		return
	}
	w.mu.Lock()
	p, l := w.parent, w.leaf
	w.parent, w.leaf = nil, nil
	w.mu.Unlock()
	if p != nil {
		p.Unsubscribe()
	}
	if l != nil {
		l.Unsubscribe()
	}
}

// pqeUnsubscribe — C: pqe_unsubscribe
func (pq *PlayQueue) pqeUnsubscribe(pqe *PlayQueueEntry) {
	if pqe.urlsub != nil {
		pqe.urlsub.Unsubscribe()
		pqe.urlsub = nil
	}
	if pqe.typesub != nil {
		pqe.typesub.Unsubscribe()
		pqe.typesub = nil
	}
}

// pqeRemoveFromSourcequeue — C: pqe_remove_from_sourcequeue
// Caller must hold pq.mu.
func (pq *PlayQueue) pqeRemoveFromSourcequeue(pqe *PlayQueueEntry) {
	pq.sourceEntries = removePQE(pq.sourceEntries, pqe)
	pq.pqeUnsubscribe(pqe)
	if pqe.originator != nil {
		pq.pm.RefDec(pqe.originator)
		pqe.originator = nil
	}
	pq.pqeUnref(pqe)
}

// pqRenumber — C: pq_renumber. Renumbers pqe_index starting after the
// entry preceding `pqe` in the linear list (or from 1 at the head).
// Caller must hold pq.mu.
func (pq *PlayQueue) pqRenumber(pqe *PlayQueueEntry) {
	idx := -1
	if pqe != nil {
		for i, x := range pq.entries {
			if x == pqe {
				idx = i - 1 // start at predecessor
				break
			}
		}
	}
	num := 1
	if idx >= 0 {
		num = pq.entries[idx].index + 1
		idx++
	} else {
		idx = 0
	}
	for ; idx < len(pq.entries); idx++ {
		pq.entries[idx].index = num
		num++
	}
}

// pqeRemoveFromGlobalqueue — C: pqe_remove_from_globalqueue
// Caller must hold pq.mu.
func (pq *PlayQueue) pqeRemoveFromGlobalqueue(pqe *PlayQueueEntry) {
	var next *PlayQueueEntry
	for i, x := range pq.entries {
		if x == pqe && i+1 < len(pq.entries) {
			next = pq.entries[i+1]
		}
	}
	pq.pm.Unparent(pqe.node)
	pq.length--
	pq.entries = removePQE(pq.entries, pqe)
	pq.shuffled = removePQE(pq.shuffled, pqe)
	pqe.linked = false
	pq.pqeUnref(pqe)
	if next != nil {
		pq.pqRenumber(next)
	}
	pq.updatePQMeta()
}

// playqueueClear — C: playqueue_clear. Caller must hold pq.mu.
func (pq *PlayQueue) playqueueClear() {
	if pq.model != nil {
		if pq.mp != nil && pq.mp.PropModel != nil {
			meta := pq.pm.CreateEx(pq.mp.PropModel, "metadata", nil, false, false)
			if meta != nil {
				pq.pm.Unlink(meta)
			}
		}
		pq.pm.Destroy(pq.model)
		pq.model = nil
	}

	if pq.sourceSub != nil {
		pq.sourceSub.Unsubscribe()
		pq.sourceSub = nil
	}

	for len(pq.sourceEntries) > 0 {
		pq.pqeRemoveFromSourcequeue(pq.sourceEntries[0])
	}
	for len(pq.entries) > 0 {
		pq.pqeRemoveFromGlobalqueue(pq.entries[0])
	}

	if pq.startme != nil {
		pq.pm.RefDec(pq.startme)
		pq.startme = nil
	}
}

// pqeInsertShuffled — C: pqe_insert_shuffled (LFG shuffle).
// Caller must hold pq.mu.
func (pq *PlayQueue) pqeInsertShuffled(pqe *PlayQueueEntry) {
	// C: shuffle_lfg = shuffle_lfg * 1664525 + 1013904223 (int32 wrap)
	pq.shuffleLfg = int(int32(pq.shuffleLfg)*1664525 + 1013904223)
	pq.length++
	// C: v = (unsigned)shuffle_lfg % playqueue_length; walk v steps
	// through shuffled TAILQ; insert before where walk ends, else tail.
	v := int(uint32(pq.shuffleLfg) % uint32(pq.length))
	insert := len(pq.shuffled)
	for i := range pq.shuffled {
		if v == 0 {
			insert = i
			break
		}
		v--
	}
	pq.shuffled = insertPQE(pq.shuffled, pqe, insert)
}

// sourceSetURL — C: source_set_url. Runs under pq.mu.
func (pq *PlayQueue) sourceSetURL(pqe *PlayQueueEntry, ev propcore.EventType, args []any) {
	var str string
	ok := false
	switch ev {
	case propcore.EventSetRString, propcore.EventSetCString, propcore.EventSetURI:
		if len(args) > 0 {
			str, ok = args[0].(string)
		}
	}
	if !ok {
		return // C: if(str == NULL) return
	}
	pqe.url = str
	if pqe.startme != 0 {
		how := event.EVENT_PLAYQUEUE_JUMP
		if pqe.startme == 2 {
			how = event.EVENT_PLAYQUEUE_JUMP_AND_PAUSE
		}
		pq.pqePlay(pqe, how)
		pqe.startme = 0
	}
}

// sourceSetType — C: source_set_type. Runs under pq.mu.
func (pq *PlayQueue) sourceSetType(pqe *PlayQueueEntry, ev propcore.EventType, args []any) {
	var str string
	ok := false
	switch ev {
	case propcore.EventSetRString, propcore.EventSetCString, propcore.EventSetURI:
		if len(args) > 0 {
			str, ok = args[0].(string)
		}
	}
	if !ok {
		return
	}
	// C: playable = !strcmp(str, "audio") || "track" || "station"
	pqe.playable = str == "audio" || str == "track" || str == "station"
}

// findSourceEntryByProp — C: find_source_entry_by_prop.
// Caller must hold pq.mu.
func (pq *PlayQueue) findSourceEntryByProp(p *propcore.Prop) *PlayQueueEntry {
	for _, pqe := range pq.sourceEntries {
		if pqe.originator == p {
			return pqe
		}
	}
	return nil
}

// addFromSource — C: add_from_source. Caller must hold pq.mu.
func (pq *PlayQueue) addFromSource(p *propcore.Prop, before *PlayQueueEntry) {
	pqe := &PlayQueueEntry{}
	pqe.refcount.Store(1)
	pqe.originator = pq.pm.RefInc(p)

	if pq.startme != nil {
		q := pq.pm.Follow(p)
		if q == pq.startme {
			pqe.startme = 1
			if pq.startPaused {
				pqe.startme = 2
			}
			pq.pm.RefDec(pq.startme)
			pq.startme = nil
			pq.startPaused = false
		}
		if q != nil {
			pq.pm.RefDec(q)
		}
	}

	// We assume it's playable until we know better (see source_set_type)
	pqe.playable = true

	if before != nil {
		pq.sourceEntries = insertBeforePQE(pq.sourceEntries, pqe, before)
	} else {
		pq.sourceEntries = append(pq.sourceEntries, pqe)
	}

	// C: p = prop_ref_inc(p) — the link below consumes a ref
	pq.pm.RefInc(p)

	pqe.node = pq.pm.CreateRoot("")
	pq.pm.Link(p, pqe.node, nil, false, false)

	pqe.urlsub = pq.watchNamedChild(p, "url",
		func(ev propcore.EventType, a []any) {
			pq.sourceSetURL(pqe, ev, a)
		})
	pqe.typesub = pq.watchNamedChild(p, "type",
		func(ev propcore.EventType, a []any) {
			pq.sourceSetType(pqe, ev, a)
		})

	pqeRef(pqe) // Ref for global queue

	pqe.linked = true
	if before != nil {
		pq.entries = insertBeforePQE(pq.entries, pqe, before)
	} else {
		pq.entries = append(pq.entries, pqe)
	}
	pq.pqRenumber(pqe)

	pq.pqeInsertShuffled(pqe)
	pq.updatePQMeta()

	var beforeNode *propcore.Prop
	if before != nil {
		beforeNode = before.node
	}
	pq.pm.SetParentEx(pqe.node, pq.nodes,
		&propcore.SetParentOpaque{Before: beforeNode}, "")
}

// delFromSource — C: del_from_source. Caller must hold pq.mu.
func (pq *PlayQueue) delFromSource(pqe *PlayQueueEntry) {
	pq.pqeRemoveFromSourcequeue(pqe)
	if pqe.linked {
		pq.pqeRemoveFromGlobalqueue(pqe)
	}
}

// moveTrack — C: move_track. Caller must hold pq.mu.
func (pq *PlayQueue) moveTrack(pqe *PlayQueueEntry, before *PlayQueueEntry) {
	pq.length-- // pqe_insert_shuffled() will increase it

	pq.sourceEntries = removePQE(pq.sourceEntries, pqe)
	pq.entries = removePQE(pq.entries, pqe)
	pq.shuffled = removePQE(pq.shuffled, pqe)

	if before != nil {
		pq.sourceEntries = insertBeforePQE(pq.sourceEntries, pqe, before)
		pq.entries = insertBeforePQE(pq.entries, pqe, before)
	} else {
		pq.sourceEntries = append(pq.sourceEntries, pqe)
		pq.entries = append(pq.entries, pqe)
	}

	pq.pqRenumber(nil)
	pq.pqeInsertShuffled(pqe)

	var beforeNode *propcore.Prop
	if before != nil {
		beforeNode = before.node
	}
	pq.pm.Move(pqe.node, beforeNode)

	pq.updatePQMeta()
}

// siblingsPopulate — C: siblings_populate. Runs under pq.mu.
func (pq *PlayQueue) siblingsPopulate(ev propcore.EventType, args []any) {
	propAt := func(i int) *propcore.Prop {
		if i < len(args) {
			p, _ := args[i].(*propcore.Prop)
			return p
		}
		return nil
	}
	switch ev {
	case propcore.EventAddChild:
		pq.addFromSource(propAt(0), nil)

	case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		for _, a := range args {
			switch vec := a.(type) {
			case []*propcore.Prop:
				for _, p := range vec {
					pq.addFromSource(p, nil)
				}
			case *propcore.PropVec:
				for i := range vec.Len() {
					pq.addFromSource(vec.Get(i), nil)
				}
			}
		}

	case propcore.EventAddChildBefore:
		p := propAt(0)
		before := pq.findSourceEntryByProp(propAt(len(args) - 1))
		pq.addFromSource(p, before)

	case propcore.EventSetDir, propcore.EventSetVoid:
		// no-op

	case propcore.EventDelChild:
		if pqe := pq.findSourceEntryByProp(propAt(0)); pqe != nil {
			pq.delFromSource(pqe)
		}

	case propcore.EventMoveChild:
		pqe := pq.findSourceEntryByProp(propAt(0))
		if pqe != nil {
			pq.moveTrack(pqe, pq.findSourceEntryByProp(propAt(len(args)-1)))
		}

	case propcore.EventReqDeleteVector, propcore.EventWantMoreChilds,
		propcore.EventHaveMoreChildsYes, propcore.EventHaveMoreChildsNo,
		propcore.EventExtEvent, propcore.EventReqMoveChild,
		propcore.EventReqDelete, propcore.EventSelectChild:
		// no-op
	}
}

// LoadWithSource — C: playqueue_load_with_source.
func (pq *PlayQueue) LoadWithSource(track, source any, flags int) {
	trackP, _ := track.(*propcore.Prop)
	sourceP, _ := source.(*propcore.Prop)
	if sourceP == nil {
		return
	}

	pq.dispatch(func() {
		pq.mu.Lock()
		defer pq.mu.Unlock()

		for _, pqe := range pq.entries {
			if trackP != nil && pq.pm.Compare(trackP, pqe.originator) {
				pq.pqePlay(pqe, event.EVENT_PLAYQUEUE_JUMP)
				return
			}
		}

		pq.playqueueClear()

		if flags&PQNoSkip == 0 {
			// C: pq->pq_startme = prop_follow(trackP) — single ref.
			pq.startme = pq.pm.Follow(trackP)
			pq.startPaused = flags&PQPaused != 0
		}

		// C: prop_subscribe(PROP_TAG_NAME("self","nodes"), CALLBACK,
		// siblings_populate, NAMED_ROOT source "self", MUTEX playqueue_mutex)
		pq.sourceSub = pq.watchNamedChild(sourceP, "nodes",
			func(ev propcore.EventType, a []any) {
				pq.siblingsPopulate(ev, a)
			})

		pq.model = pq.pm.XrefAddref(sourceP)

		// C: prop_link(prop_create(source,"metadata"),
		//              prop_create(mp->mp_prop_model,"metadata"))
		if pq.mp != nil && pq.mp.PropModel != nil {
			srcMeta := pq.pm.CreateEx(sourceP, "metadata", nil, false, false)
			mpMeta := pq.pm.CreateEx(pq.mp.PropModel, "metadata", nil, false, false)
			if srcMeta != nil && mpMeta != nil {
				pq.pm.Link(srcMeta, mpMeta, nil, false, false)
			}
		}
	})
}
