package prop

import (
	"fmt"
	"slices"
)

// C: src/prop/prop_window.c — prop_window_node_t
type propWindowNode struct {
	in  *Prop
	out *Prop
	pos int
}

// C: prop_window_t (prop_window.h)
type propWindow struct {
	srcsub    *Subscription
	dst       *Prop
	winStart  int
	winLength int
	count     int
	queue     []*propWindowNode // C: TAILQ (insertion order)
	pm        *PropManager
}

// C: PROP_WINDOW_TAKE_DST_OWNERSHIP (prop_window.h)
const PropWindowTakeDstOwnership = 0x1

// clear — C: pw_clear (prop_window.c:70)
func (pw *propWindow) clear() {
	for _, pwn := range pw.queue {
		if pwn.out != nil {
			pw.pm.Destroy0(pwn.out)
		}
		pw.pm.TagClear(pwn.in, pw)
	}
	pw.queue = nil
}

// addChild — C: add_child (prop_window.c:88)
func (pw *propWindow) addChild(p *Prop, before *Prop) {
	pwn := &propWindowNode{in: p, pos: pw.count}
	pw.queue = append(pw.queue, pwn)
	pw.pm.TagSet(p, pw, pwn)

	if pw.count >= pw.winStart && pw.count < pw.winStart+pw.winLength {
		pwn.out = pw.pm.CreateRootEx("", true)
		pw.pm.Link(p, pwn.out, nil, false, false)
		pw.pm.SetParentEx(pwn.out, pw.dst, nil, "")
	} else {
		pwn.out = nil
	}
	pw.count++
}

// delChild — C: del_child (prop_window.c:127)
func (pw *propWindow) delChild(pwn *propWindowNode) {
	for i, n := range pw.queue {
		if n == pwn {
			pw.queue = slices.Delete(pw.queue, i, i+1)
			break
		}
	}
	if pwn.out != nil {
		pw.pm.Destroy0(pwn.out)
	}
}

// srcCb — C: src_cb (prop_window.c:139)
func (pw *propWindow) srcCb(opaque any, event EventType, args ...any) {
	switch event {
	case EventAddChild:
		pw.addChild(args[0].(*Prop), nil)

	case EventAddChildVector, EventAddChildVectorDirect:
		// Go wire format is []*Prop (vecProps handles *PropVec too)
		for _, p := range vecProps(args) {
			pw.addChild(p, nil)
		}

	case EventMoveChild:
		// C: move_child() is an empty stub — nothing happens.
		// (prop_window.c:115-121 is deliberately unimplemented)

	case EventSetDir:
		// C: no-op

	case EventDelChild:
		if pwn, ok := pw.pm.TagClear(args[0].(*Prop), pw).(*propWindowNode); ok {
			pw.delChild(pwn)
		}

	case EventSetVoid:
		pw.clear()

	case EventHaveMoreChildsYes, EventHaveMoreChildsNo,
		EventWantMoreChilds, EventSelectChild:
		// C: no-ops

	default:
		panic(fmt.Sprintf("prop_window can't handle event %d", event)) // C: abort()
	}
}

// PropWindowCreate — C: prop_window_create (prop_window.c:197)
func (pm *PropManager) PropWindowCreate(dst, src *Prop, start, length uint, flags int) *propWindow {
	pw := &propWindow{pm: pm, winStart: int(start), winLength: int(length)}

	if flags&PropWindowTakeDstOwnership != 0 {
		pw.dst = dst
	} else {
		pw.dst = pm.XrefAddref(dst)
	}

	// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_window.c:213)
	pw.srcsub = pm.Subscribe(src, pw.srcCb, pw, SubFlagInternal|SubFlagDontLock)
	return pw
}

// Destroy — C: prop_window_destroy (prop_window.c:226)
func (pw *propWindow) Destroy() {
	pw.clear()
	pw.pm.Unsubscribe(pw.srcsub)
	pw.pm.Destroy0(pw.dst)
}
