package prop

import (
	"fmt"
	"slices"
)

// C: src/prop/prop_concat.c — concatenates children of multiple source props
// into a single destination prop, preserving per-source ordering.

// C: typedef struct prop_concat_source
type propConcatSource struct {
	pc     *PropConcat
	srcsub *Subscription
	first  *Prop
	header *Prop
	count  int
	flags  int
	index  int
}

// C: struct prop_concat
type PropConcat struct {
	queue      []*propConcatSource // C: TAILQ pc_queue
	dst        *Prop
	dstsub     *Subscription
	indexTally int
	refcount   int
	pm         *PropManager
}

// tagKey returns the prop-tag key C uses for (prop, pcs) pairs.
// C uses the pcs pointer directly as the tag key; Go tags are keyed by string.
func pcsTagKey(pcs *propConcatSource) string {
	return fmt.Sprintf("pcs_%p", pcs)
}

// C: static void prop_concat_release0(prop_concat_t *pc)
func (pc *PropConcat) release0() {
	pc.refcount--
	if pc.refcount > 0 {
		return
	}
	pc.pm.RefDec(pc.dst)
	pc.dstsub.Unsubscribe()
}

// C: static prop_t *find_next_out(prop_concat_source_t *pcs)
// Iterates sources AFTER pcs; returns pcs_header if set, else pcs_first.
func (pc *PropConcat) findNextOut(pcs *propConcatSource) *Prop {
	seen := false
	for _, s := range pc.queue {
		if !seen {
			if s == pcs {
				seen = true
			}
			continue
		}
		if s.first != nil {
			if s.header != nil {
				return s.header
			}
			return s.first
		}
	}
	return nil
}

// C: static void pcs_destroy(prop_concat_source_t *pcs)
func (pcs *propConcatSource) destroy() {
	pc := pcs.pc
	// assert(pcs->pcs_count == 0); assert(pcs->pcs_first == NULL)
	for i, s := range pc.queue {
		if s == pcs {
			pc.queue = slices.Delete(pc.queue, i, i+1)
			break
		}
	}
	pcs.srcsub.Unsubscribe()
	if pcs.header != nil {
		pc.pm.Destroy0(pcs.header)
	}
	pc.release0()
}

// C: static void add_child(prop_concat_source_t *pcs, prop_concat_t *pc,
//
//	prop_t *p)
func (pc *PropConcat) addChild(pcs *propConcatSource, p *Prop) {
	out := NewStandaloneProp("") // C: prop_make(NULL, 0, NULL)
	pc.pm.TagSet(p, pcsTagKey(pcs), out)
	pc.pm.TagSet(out, pcsTagKey(pcs), p)
	pc.pm.Link(p, out, nil, false, false)

	pc.pm.SetParentEx(out, pc.dst, pc.findNextOut(pcs), "")

	if pcs.count == 0 {
		pcs.first = out
		if pcs.header != nil {
			pc.pm.SetParentEx(pcs.header, pc.dst, out, "")
		}
	}
	pcs.count++
}

// C: static void src_cb(void *opaque, prop_event_t event, ...)
func (pcs *propConcatSource) srcCb(event EventType, args ...any) {
	pc := pcs.pc
	switch event {
	case EventAddChild:
		p, _ := args[0].(*Prop)
		pc.addChild(pcs, p)

	case EventAddChildBefore:
		// Go notifySub: args = [child, parent, nextSibling]
		p, _ := args[0].(*Prop)
		out := NewStandaloneProp("")
		pc.pm.TagSet(p, pcsTagKey(pcs), out)
		pc.pm.TagSet(out, pcsTagKey(pcs), p)
		pc.pm.Link(p, out, nil, false, false)

		var q *Prop
		if len(args) > 2 {
			q, _ = args[2].(*Prop)
		}
		var before *Prop
		if q != nil {
			before, _ = pc.pm.TagGet(q, pcsTagKey(pcs)).(*Prop)
		}
		pc.pm.SetParentEx(out, pc.dst, before, "")

		pcs.count++

	case EventAddChildVector, EventAddChildVectorDirect:
		if len(args) > 0 {
			if pv, ok := args[0].([]*Prop); ok {
				for _, p := range pv {
					pc.addChild(pcs, p)
				}
			}
		}

	case EventDelChild:
		p, _ := args[0].(*Prop)
		out, _ := pc.pm.TagClear(p, pcsTagKey(pcs)).(*Prop)
		pc.pm.TagClear(out, pcsTagKey(pcs))
		// before = TAILQ_NEXT(out, hp_parent_link)
		var before *Prop
		if out != nil {
			if par := out.GetParent(); par != nil {
				ch := par.GetChildren()
				for i, c := range ch {
					if c == out && i+1 < len(ch) {
						before = ch[i+1]
						break
					}
				}
			}
		}
		pc.pm.Destroy0(out)

		pcs.count--
		if pcs.count == 0 {
			pcs.first = nil
			if pcs.header != nil {
				pc.pm.SetParentEx(pcs.header, nil, nil, "")
			}
		} else if pcs.first == out {
			pcs.first = before
		}

	case EventMoveChild:
		// Go notifySub: args = [prop, newParent, before]
		p, _ := args[0].(*Prop)
		var q *Prop
		if len(args) > 2 {
			q, _ = args[2].(*Prop)
		}
		out, _ := pc.pm.TagGet(p, pcsTagKey(pcs)).(*Prop)
		var before *Prop
		if q != nil {
			before, _ = pc.pm.TagGet(q, pcsTagKey(pcs)).(*Prop)
		} else {
			before = pc.findNextOut(pcs)
		}
		// C: prop_move0 — same parent reorder (fires EventMoveChild)
		pc.pm.SetParentEx(out, pc.dst, before, "")

	case EventSelectChild, EventSetVoid, EventSetDir,
		EventReqDeleteVector, EventHaveMoreChildsYes, EventHaveMoreChildsNo,
		EventWantMoreChilds, EventReqDelete:
		// C: no-op cases
		break

	case EventDestroyed:
		pcs.destroy()

	default:
		// C: printf("Cant handle event %d\n", event); abort();
		panic(fmt.Sprintf("prop_concat: can't handle event %d", event))
	}
}

// C: static void req_move(prop_concat_t *pc, prop_t *p, prop_t *b)
func (pc *PropConcat) reqMove(p *Prop, b *Prop) {
	for _, ps := range pc.queue {
		pp, _ := pc.pm.TagGet(p, pcsTagKey(ps)).(*Prop)
		if pp == nil {
			continue
		}

		if b == nil {
			// C: prop_req_move0(pp, NULL, ps->pcs_srcsub)
			pc.pm.ReqMoveSkipme(pp, nil, ps.srcsub)
			return
		}

		var bs *propConcatSource
		var bb *Prop
		for i, s2 := range pc.queue {
			if b == s2.header {
				bb = nil
				if i > 0 {
					bs = pc.queue[i-1] // TAILQ_PREV
				}
				break
			}
			bb, _ = pc.pm.TagGet(b, pcsTagKey(s2)).(*Prop)
			if bb != nil {
				bs = s2
				break
			}
		}

		if bs == nil || bs.index < ps.index {
			// prop_req_move0(pp, TAILQ_FIRST(&pp->hp_parent->hp_childs), ...)
			var first *Prop
			if par := pp.GetParent(); par != nil {
				if ch := par.GetChildren(); len(ch) > 0 {
					first = ch[0]
				}
			}
			// C: prop_req_move0(pp, TAILQ_FIRST(&pp->hp_parent->hp_childs),
			//   ps->pcs_srcsub)
			pc.pm.ReqMoveSkipme(pp, first, ps.srcsub)
			return
		}

		if bs.index > ps.index {
			// C: prop_req_move0(pp, NULL, ps->pcs_srcsub)
			pc.pm.ReqMoveSkipme(pp, nil, ps.srcsub)
			return
		}

		// C: prop_req_move0(pp, bb, ps->pcs_srcsub)
		pc.pm.ReqMoveSkipme(pp, bb, ps.srcsub)
		return
	}
}

// C: static void dst_cb(void *opaque, prop_event_t event, ...)
func (pc *PropConcat) dstCb(event EventType, args ...any) {
	switch event {
	case EventReqMoveChild:
		// Go notifySub: args = [prop, parent, before]
		var p1, p2 *Prop
		p1, _ = args[0].(*Prop)
		if len(args) > 2 {
			p2, _ = args[2].(*Prop)
		}
		pc.reqMove(p1, p2)

	case EventDestroyed:
		pc.release0()

	default:
		break
	}
}

// C: void prop_concat_add_source(prop_concat_t *pc, prop_t *src,
//
//	prop_t *header)
func (pc *PropConcat) AddSource(src *Prop, header *Prop) {
	pcs := &propConcatSource{}
	pcs.header = header
	pcs.pc = pc

	pc.refcount++
	pc.queue = append(pc.queue, pcs) // TAILQ_INSERT_TAIL

	// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_concat.c:320)
	pcs.srcsub = src.Subscribe(func(opaque any, event EventType, args ...any) {
		pcs.srcCb(event, args...)
	}, pcs, SubFlagInternal|SubFlagDontLock)

	pcs.index = pc.indexTally
	pc.indexTally++
}

// C: prop_concat_t *prop_concat_create(prop_t *dst)
func PropConcatCreate(pm *PropManager, dst *Prop) *PropConcat {
	pc := &PropConcat{}
	pc.pm = pm
	pc.dst = pm.RefInc(dst)
	pc.refcount = 2 // one for subscription, one for caller

	// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_concat.c:345)
	pc.dstsub = dst.Subscribe(func(opaque any, event EventType, args ...any) {
		pc.dstCb(event, args...)
	}, pc, SubFlagInternal|SubFlagDontLock)

	return pc
}

// C: void prop_concat_release(prop_concat_t *pc)
func (pc *PropConcat) Release() {
	pc.release0()
}
