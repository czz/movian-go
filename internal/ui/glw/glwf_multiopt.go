package glw

// C: src/ui/glw/glw_view_eval.c — builtin function table (funcvec) and all
// glwf_* handlers. Ported 1:1 from the canonical C implementation.

import (
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// C: multiopt (glw_view_eval.c:6366-6756)
type multioptItem struct {
	miLinkNext *multioptItem
	miLinkPrev **multioptItem
	miTitle    *miscpkg.Rstr
	miValue    *miscpkg.Rstr
	miMark     bool
	miItem     *propcore.Prop
}

type glwfMultioptExtra struct {
	q            *multioptItem // TAILQ head
	qlast        **multioptItem
	settings     *propcore.Prop
	opts         *propcore.Prop
	title        *propcore.Prop
	value        *propcore.Prop
	cur          *multioptItem
	settingSub   *propcore.Subscription
	storage      *propcore.Prop
	storageSub   *propcore.Subscription
	userval      *miscpkg.Rstr
	currentTitle *propcore.Prop
}

func (x *glwfMultioptExtra) forEach(fn func(mi *multioptItem)) {
	for mi := x.q; mi != nil; mi = mi.miLinkNext {
		fn(mi)
	}
}

func (x *glwfMultioptExtra) insertHead(mi *multioptItem) {
	mi.miLinkNext = x.q
	if x.q != nil {
		x.q.miLinkPrev = &mi.miLinkNext
	}
	x.q = mi
	mi.miLinkPrev = &x.q
	if x.qlast == nil || *x.qlast == nil {
		x.qlast = &x.q
		for c := x.q; c.miLinkNext != nil; c = c.miLinkNext {
		}
	}
}

func (x *glwfMultioptExtra) insertAfter(after, mi *multioptItem) {
	mi.miLinkNext = after.miLinkNext
	if mi.miLinkNext != nil {
		mi.miLinkNext.miLinkPrev = &mi.miLinkNext
	} else {
		x.qlast = &after.miLinkNext
	}
	after.miLinkNext = mi
	mi.miLinkPrev = &after.miLinkNext
}

func (x *glwfMultioptExtra) remove(mi *multioptItem) {
	if mi.miLinkNext != nil {
		mi.miLinkNext.miLinkPrev = mi.miLinkPrev
	} else {
		x.qlast = mi.miLinkPrev
	}
	*mi.miLinkPrev = mi.miLinkNext
}

func (x *glwfMultioptExtra) first() *multioptItem { return x.q }

// C: multiopt_item_cycle (glw_view_eval.c:6390)
func multioptItemCycle(x *glwfMultioptExtra) {
	if x.cur == nil {
		return
	}
	x.cur = x.cur.miLinkNext
	if x.cur == nil {
		x.cur = x.q
	}
	if x.cur.miItem != nil {
		glwDeps.pm.Select(x.cur.miItem)
		glwDeps.pm.SetStringEx(x.storage, nil, miscpkg.RstrGet(x.cur.miValue), 0)
		glwDeps.pm.SetStringEx(x.currentTitle, nil, miscpkg.RstrGet(x.cur.miTitle), 0)
	}
}

// C: multiopt_item_cb (glw_view_eval.c:6409)
func multioptItemCb(opaque any, event propcore.EventType, args ...any) {
	x := opaque.(*glwfMultioptExtra)
	switch event {
	case propcore.EventSelectChild:
		c, _ := args[0].(*propcore.Prop)
		var name string
		if c != nil {
			name = glwDeps.pm.GetName(c)
		}
		var mi *multioptItem
		x.forEach(func(m *multioptItem) {
			if miscpkg.RstrGet(m.miValue) == name {
				mi = m
			}
		})
		if mi == nil {
			return
		}
		glwDeps.pm.SetStringEx(x.value, nil, miscpkg.RstrGet(mi.miValue), 0)
		glwDeps.pm.SetStringEx(x.storage, nil, miscpkg.RstrGet(mi.miValue), 0)
		glwDeps.pm.SetStringEx(x.currentTitle, nil, miscpkg.RstrGet(mi.miTitle), 0)
	case propcore.EventExtEvent:
		multioptItemCycle(x)
	}
}

// C: glwf_multiopt_ctor (glw_view_eval.c:6456)
func glwfMultioptCtor(self *Token) {
	x := &glwfMultioptExtra{}
	x.qlast = &x.q
	self.tExtra = x
}

// C: multiopt_item_destroy (glw_view_eval.c:6467)
func multioptItemDestroy(x *glwfMultioptExtra, mi *multioptItem) {
	if x.cur == mi {
		x.cur = mi.miLinkNext
		if x.cur == nil {
			x.cur = x.q
		}
		if x.cur != nil && x.cur.miItem != nil {
			glwDeps.pm.SelectChildPropEx(x.cur.miItem, nil, x.settingSub)
		}
	}
	x.remove(mi)
	miscpkg.RstrRelease(mi.miValue)
	miscpkg.RstrRelease(mi.miTitle)
	if mi.miItem != nil {
		glwDeps.pm.Destroy(mi.miItem)
		glwDeps.pm.RefDec(mi.miItem)
	}
}

// C: glwf_multiopt_dtor (glw_view_eval.c:6490)
func glwfMultioptDtor(gr *glwRoot, self *Token) {
	x, _ := self.tExtra.(*glwfMultioptExtra)
	if x == nil {
		return
	}
	for mi := x.q; mi != nil; {
		n := mi.miLinkNext
		multioptItemDestroy(x, mi)
		mi = n
	}
	glwDeps.pm.RefDec(x.settings)
	glwDeps.pm.RefDec(x.opts)
	glwDeps.pm.RefDec(x.title)
	glwDeps.pm.RefDec(x.value)
	glwDeps.pm.Unsubscribe(x.settingSub)
	glwDeps.pm.RefDec(x.storage)
	glwDeps.pm.Unsubscribe(x.storageSub)
	miscpkg.RstrRelease(x.userval)
	glwDeps.pm.RefDec(x.currentTitle)
}

// C: multiopt_add_link (glw_view_eval.c:6522)
func multioptAddLink(x *glwfMultioptExtra, d *Token, up **multioptItem, after *multioptItem) *multioptItem {
	var mi *multioptItem
	x.forEach(func(m *multioptItem) {
		if miscpkg.RstrGet(d.tURI) == miscpkg.RstrGet(m.miValue) {
			mi = m
		}
	})
	if mi == nil {
		mi = &multioptItem{}
		mi.miValue = miscpkg.RstrDup(d.tURI)
		if after == nil {
			x.insertHead(mi)
		} else {
			x.insertAfter(after, mi)
		}
	} else {
		mi.miMark = false
	}
	if x.userval != nil && miscpkg.RstrGet(x.userval) == miscpkg.RstrGet(mi.miValue) {
		*up = mi
	}
	miscpkg.RstrSet(&mi.miTitle, d.tURITitle)
	return mi
}

// C: multiopt_add_vector (glw_view_eval.c:6553)
func multioptAddVector(x *glwfMultioptExtra, t0 *Token, up **multioptItem, chk int, after **multioptItem) int {
	if chk != 0 {
		for t := t0.child; t != nil; t = t.next {
			if t.tFlags&tokenFSelected != 0 && t.typ != tokenURI {
				return 1
			}
		}
		for t := t0.child; t != nil; t = t.next {
			if t.tFlags&tokenFSelected != 0 && t.typ == tokenURI {
				*after = multioptAddLink(x, t, up, *after)
			}
		}
	}
	for t := t0.child; t != nil; t = t.next {
		if t.typ == tokenURI && t.tFlags&tokenFSelected == 0 {
			*after = multioptAddLink(x, t, up, *after)
		}
	}
	return 0
}

// C: glwf_multiopt (glw_view_eval.c:6581)
func glwfMultiopt(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	x, _ := self.tExtra.(*glwfMultioptExtra)

	if argc < 4 {
		return glwViewSeterr(ec.ei, self, "multiopt(): Invalid number of args")
	}
	dst := resolvePropertyName2(ec, argv[0])
	if dst == nil {
		return -1
	}
	setting := resolvePropertyName2(ec, argv[1])
	if setting == nil {
		return -1
	}
	title := tokenResolve(ec, argv[2])
	if title == nil {
		return -1
	}
	storage := resolvePropertyName2(ec, argv[3])
	if storage == nil {
		return -1
	}

	if setting.tProp != x.settings {
		x.forEach(func(mi *multioptItem) {
			if mi.miItem != nil {
				glwDeps.pm.RefDec(mi.miItem)
				mi.miItem = nil
			}
		})
		if x.settings != nil {
			glwDeps.pm.DestroyChilds(x.settings)
			glwDeps.pm.RefDec(x.settings)
			glwDeps.pm.RefDec(x.opts)
			glwDeps.pm.RefDec(x.title)
			glwDeps.pm.RefDec(x.currentTitle)
			glwDeps.pm.RefDec(x.value)
			x.settings = nil
			x.opts = nil
			x.title = nil
			x.currentTitle = nil
			x.value = nil
		}
		glwDeps.pm.Unsubscribe(x.settingSub)
		x.settingSub = nil
		x.settings = glwDeps.pm.RefInc(setting.tProp)

		if x.settings != nil {
			// C: prop_create_r — caller (x) holds a ref on each of these
			metadata := glwDeps.pm.CreateEx(x.settings, "metadata", nil, false, false)
			x.title = glwDeps.pm.RefInc(glwDeps.pm.CreateEx(metadata, "title", nil, false, false))
			glwDeps.pm.SetVEx(nil, x.settings, "type", "multiopt")
			x.opts = glwDeps.pm.RefInc(glwDeps.pm.CreateEx(x.settings, "options", nil, false, false))
			current := glwDeps.pm.CreateEx(x.settings, "current", nil, false, false)
			x.currentTitle = glwDeps.pm.RefInc(glwDeps.pm.CreateEx(current, "title", nil, false, false))
			x.value = glwDeps.pm.RefInc(glwDeps.pm.CreateEx(x.settings, "value", nil, false, false))

			x.settingSub = x.opts.Subscribe(multioptItemCb, x,
				propcore.SubNoInitialUpdate,
				propcore.SubCourier{C: ec.w.glwRoot.grCourier})
		}
	}

	if storage.tProp != x.storage {
		if x.storage != nil {
			glwDeps.pm.RefDec(x.storage)
		}
		glwDeps.pm.Unsubscribe(x.storageSub)
		x.storageSub = nil
		x.storage = glwDeps.pm.RefInc(storage.tProp)
		if x.storage != nil {
			// C: PROP_SUB_DIRECT_UPDATE — the persisted value must
			// arrive synchronously at subscribe time, before the
			// item list below is built, or multiopt always falls
			// back to the first option (C: glw_view_eval.c:6681).
			x.storageSub = x.storage.Subscribe(
				func(opaque any, ev propcore.EventType, args ...any) {
					if len(args) > 0 {
						if s, ok := args[0].(string); ok {
							miscpkg.RstrSet(&x.userval, miscpkg.RstrAllocStr(s))
						}
					}
				}, x, propcore.SubFlagDirectUpdate,
				propcore.SubCourier{C: ec.w.glwRoot.grCourier})
		}
	}

	if title.typ == tokenRstring {
		glwDeps.pm.SetStringEx(x.title, nil, miscpkg.RstrGet(title.tRstring), 0)
	}

	x.forEach(func(mi *multioptItem) { mi.miMark = true })

	var u *multioptItem
	lpVectors := make([]*Token, 0, 16)
	var after *multioptItem

	for i := uint(4); i < argc; i++ {
		d := tokenResolve(ec, argv[i])
		if d == nil {
			return -1
		}
		switch d.typ {
		case tokenURI:
			after = multioptAddLink(x, d, &u, after)
		case tokenVector:
			if multioptAddVector(x, d, &u, 1, &after) != 0 && len(lpVectors) < 16 {
				lpVectors = append(lpVectors, d)
			}
		}
	}
	for _, d := range lpVectors {
		multioptAddVector(x, d, &u, 0, &after)
	}

	for mi := x.q; mi != nil; {
		n := mi.miLinkNext
		if mi.miMark {
			multioptItemDestroy(x, mi)
		}
		mi = n
	}

	if x.opts != nil {
		x.forEach(func(mi *multioptItem) {
			if mi.miItem == nil {
				q := glwDeps.pm.CreateRoot(miscpkg.RstrGet(mi.miValue))
				glwDeps.pm.SetVEx(nil, q, "title", miscpkg.RstrGet(mi.miTitle))
				mi.miItem = glwDeps.pm.RefInc(q)
				if glwDeps.pm.SetParentEx(q, x.opts, nil, "") != 0 {
					glwDeps.pm.Destroy(q)
					glwDeps.pm.RefDec(mi.miItem)
					mi.miItem = nil
				}
			} else {
				glwDeps.pm.Move(mi.miItem, nil)
			}
		})
	}

	if u != nil {
		x.cur = u
	} else {
		x.cur = x.q
	}
	nitems := 0
	for mi := x.q; mi != nil; mi = mi.miLinkNext {
		nitems++
	}
	if x.cur != nil {
		glwDeps.pm.SetStringEx(x.value, nil, miscpkg.RstrGet(x.cur.miValue), 0)
		glwDeps.pm.SetStringEx(x.currentTitle, nil, miscpkg.RstrGet(x.cur.miTitle), 0)
		glwDeps.pm.SelectChildPropEx(x.cur.miItem, nil, x.settingSub)
	}
	glwDeps.pm.Link(x.value, dst.tProp, nil, false, false)
	ec.dynamicEval |= GLW_VIEW_EVAL_KEEP
	return 0
}
