package prop

// Split from prop.go — Fase 4 pure-move refactor.
// No symbol, lock, or event-order changes.

func (pm *PropManager) ReqMove(prop *Prop, before *Prop) {
	pm.ReqMoveSkipme(prop, before, nil)
}

// ReqMoveSkipme — C: prop_req_move0 (prop_core.c:2646-2669). Does NOT
// physically reorder; notifies parent's value subscribers (minus skipme)
// so the owning model/view can perform the move via prop_move0.
// PROP_PROXY props route to prop_proxy_req_move (assert skipme == NULL).
func (pm *PropManager) ReqMoveSkipme(prop *Prop, before *Prop, skipme *Subscription) {
	if prop == nil || prop.parent == nil {
		return
	}
	if prop == before {
		return // C: assert(p != before)
	}
	// C: if(p->hp_type == PROP_PROXY) { assert(skipme == NULL);
	//    prop_proxy_req_move(p, before); return; }
	prop.mu.RLock()
	isProxy := prop.propType == PropTypeProxy
	pc := prop.proxyConn
	prop.mu.RUnlock()
	if isProxy {
		if pc != nil {
			if mover, ok := pc.(ProxyMover); ok {
				mover.ProxyReqMove(prop, before)
			}
		}
		return
	}
	parent := prop.parent
	parent.mu.Lock()

	srcIdx := -1
	dstIdx := -1
	for i, c := range parent.children {
		if c == prop {
			srcIdx = i
		}
		if before != nil && c == before {
			dstIdx = i
		}
	}
	if srcIdx < 0 {
		parent.mu.Unlock()
		return
	}

	// Check if already in position (C: TAILQ_NEXT(p, hp_parent_link) != before)
	alreadyInPosition := false
	if before == nil {
		// Move to end — check if already at end
		alreadyInPosition = (srcIdx == len(parent.children)-1)
	} else if dstIdx >= 0 {
		// Move before 'before' — check if already before it
		alreadyInPosition = (srcIdx+1 == dstIdx)
	}

	if alreadyInPosition {
		parent.mu.Unlock()
		return
	}

	// Collect value subscribers for notification
	parentSubs := make([]*Subscription, len(parent.valueSubs))
	copy(parentSubs, parent.valueSubs)
	parent.mu.Unlock()

	// Fire EventReqMoveChild to parent's subscribers.
	// C: prop_notify_child2(p, parent, before, PROP_REQ_MOVE_CHILD, skipme, 0)
	for _, sub := range parentSubs {
		if sub == skipme {
			continue
		}
		notifySub(sub, EventReqMoveChild, prop, parent, before)
	}
}

// WantMoreChilds requests more children for a property.
// Notifies subscribers that more children are available, enabling lazy loading.
func (pm *PropManager) Findv(p *Prop, names []string) *Prop {
	if p == nil {
		return nil
	}
	// C: while(p->hp_originator != NULL) p = p->hp_originator;
	p = pm.followNoRef(p)
	var c *Prop = p
	for _, n := range names {
		p.mu.RLock()
		if p.propType != PropTypeDir {
			p.mu.RUnlock()
			c = nil
			break
		}
		var found *Prop
		for _, ch := range p.children {
			ch.mu.RLock()
			cn := ch.name
			ch.mu.RUnlock()
			if cn == n {
				found = ch
				break
			}
		}
		p.mu.RUnlock()
		if found == nil {
			c = nil
			break
		}
		// C: while(c->hp_originator != NULL) c = c->hp_originator;
		c = pm.followNoRef(found)
		p = c
	}
	if c != nil {
		pm.RefInc(c)
	}
	return c
}

// seti — C: prop_seti (prop_core.c:5536) — dispatches a typed set on the
// leaf prop per the PROP_SET_* event tag.
func (pm *PropManager) Move(prop *Prop, before *Prop) {
	if prop == nil {
		return
	}
	// C: prop_move_before (prop_core.c:2653-2656):
	//   if(p->hp_type == PROP_PROXY) { prop_proxy_req_move(p, before); return; }
	prop.mu.RLock()
	isProxy := prop.propType == PropTypeProxy
	pc := prop.proxyConn
	prop.mu.RUnlock()
	if isProxy && pc != nil {
		if mover, ok := pc.(ProxyMover); ok {
			mover.ProxyReqMove(prop, before)
			return
		}
	}
	prop.mu.RLock()
	parent := prop.parent
	prop.mu.RUnlock()
	if parent == nil {
		return
	}
	pm.SetParentEx(prop, parent, &SetParentOpaque{Before: before}, "")
}

// Follow resolves a link chain to the final originator prop.
// C: prop_follow (prop_core.c) — walks hp_originator to the end and
// returns a new reference. Go: returns the resolved prop (caller may
// RefInc if a held reference is needed).
// Follow follows the originator chain and returns the terminal prop.
// C: prop_follow (prop_core.c:4891-4901) — returns prop_ref_inc(p);
// the caller owns the reference and must RefDec.
func (pm *PropManager) Subfind(p *Prop, names []string, followSymlinks int,
	allowIndexing int, originChain *[]*Prop) *Prop {

	for i := range names {
		// C: while(follow_symlinks && p->hp_originator != NULL)
		//      { if(origin_chain) origin_chain[ocnum++] = p; p = p->hp_originator }
		for followSymlinks != 0 && p != nil {
			p.mu.RLock()
			o := p.originator
			p.mu.RUnlock()
			if o == nil {
				break
			}
			if originChain != nil {
				*originChain = append(*originChain, p)
			}
			p = o
		}
		if p == nil {
			return nil
		}

		p.mu.Lock()
		if p.propType != PropTypeDir {
			if p.propType != PropTypeVoid {
				// We don't want subscriptions to overwrite real values
				p.mu.Unlock()
				return nil
			}
			// C: VOID -> DIR transition + prop_notify_value
			p.children = make([]*Prop, 0)
			p.childIndex = make(map[string]*Prop)
			p.selectedChild = nil
			p.propType = PropTypeDir
			subs := make([]*Subscription, len(p.valueSubs))
			copy(subs, p.valueSubs)
			p.mu.Unlock()
			for _, sub := range subs {
				notifySub(sub, EventSetDir, p, nil)
			}
			p.mu.Lock()
		}

		var c *Prop
		name := names[i]
		if allowIndexing != 0 && len(name) > 0 && name[0] == '*' {
			// C: "*"N selects the Nth child in TAILQ order
			idx := 0
			for _, ch := range name[1:] {
				if ch < '0' || ch > '9' {
					break
				}
				idx = idx*10 + int(ch-'0')
			}
			for _, ch := range p.children {
				if idx == 0 {
					c = ch
					break
				}
				idx--
			}
			if c == nil {
				p.mu.Unlock()
				return nil
			}
		} else {
			for _, ch := range p.children {
				if ch.name == name {
					c = ch
					break
				}
			}
		}
		p.mu.Unlock()

		if c == nil {
			// C: p = c ?: prop_create0(p, name[0], NULL, 0)
			c = pm.CreateEx(p, name, nil, false, false)
		}
		p = c
	}

	// C: while(follow_symlinks && p->hp_originator != NULL)
	//      { if(origin_chain) origin_chain[ocnum++] = p; p = p->hp_originator }
	for followSymlinks != 0 && p != nil {
		p.mu.RLock()
		o := p.originator
		p.mu.RUnlock()
		if o == nil {
			break
		}
		if originChain != nil {
			*originChain = append(*originChain, p)
		}
		p = o
	}
	return p
}

// PropRootNode — C: prop_root_node_t (prop_core.c:2757)
// A named root used by resolveTree: name[0] may match the prop's own
// name or this alias.
type PropRootNode struct {
	P    *Prop
	Name string
}

// ResolveTree — C: prop_resolve_tree (prop_core.c:2764-2785).
// "global" resolves to prop_global; prv is the PROP_TAG_ROOT_VECTOR
// (matched on prv->name); prl is the named-root list (matched on the
// prop's hp_name or the alias).
func (pm *PropManager) ResolveTree(name string, prl []*PropRootNode,
	prv []PropRoot) *Prop {
	if name == "global" {
		return pm.GetGlobal()
	}
	for i := range prv {
		if prv[i].P != nil && prv[i].Name == name {
			return prv[i].P
		}
	}
	for _, pr := range prl {
		p := pr.P
		if p == nil {
			continue
		}
		if p.GetName() == name {
			return p
		}
		if pr.Name == name {
			return p
		}
	}
	return nil
}

// PropRoot — C: prop_root_t (prop.h:155-158) — { prop_t *p; const char *name }
type PropRoot struct {
	P    *Prop
	Name string
}

// GetByName — C: prop_get_by_name (prop_core.c:2838-2904).
// Resolves names[0] against the root vector + named roots, then walks
// the remaining elements via Subfind (creating missing children).
// If originChain is non-nil, Subfind appends every traversed link-dst
// prop to it (C: origin_chain in the subscribe path).
// Returns a new reference — caller must RefDec.
func (pm *PropManager) GetByName(names []string, followSymlinks int,
	rootVector []PropRoot, originChain *[]*Prop, namedRoots ...*PropRootNode) *Prop {
	if len(names) == 0 {
		return nil
	}
	p := pm.ResolveTree(names[0], namedRoots, rootVector)
	if p == nil || p.propType == PropTypeZombie {
		return nil
	}
	p.mu.RLock()
	isProxy := p.propType == PropTypeProxy
	proxyConn := p.proxyConn
	p.mu.RUnlock()
	if isProxy {
		// C: prop_get_by_name PROP_PROXY branch (prop_core.c:2853-2886):
		// vec = hp_proxy_pfx + remaining names, then
		// prop_proxy_make(ppc, hp_proxy_id, NULL, p, vec); the remote end
		// resolves the path (proxy props have no local children).
		if gb, ok := proxyConn.(ProxyByNamer); ok && gb != nil {
			np := gb.ProxyGetByName(p, names[1:], followSymlinks != 0)
			if np == nil {
				return nil
			}
			// C: p = prop_ref_inc(p) (prop_core.c:2889)
			return pm.RefInc(np)
		}
		return nil
	}
	p = pm.Subfind(p, names[1:], followSymlinks, 1, originChain)
	if p == nil {
		return nil
	}
	return pm.RefInc(p)
}
