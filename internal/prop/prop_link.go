package prop

// Split from prop.go — Fase 4 pure-move refactor.
// No symbol, lock, or event-order changes.

import (
	"slices"
	"unsafe"
)

const (
	LinkSoft             = 0 // C: PROP_LINK_SOFT — no xref
	LinkXrefed           = 1 // C: PROP_LINK_XREFED — increment src xref
	LinkXrefedIfOrphaned = 2 // C: PROP_LINK_XREFED_IF_ORPHANED — xref only if src has no parent
)

// PropertyType represents the type of a property.
// Moved from types.go (which was dead code — parallel Property/PropertyEvaluator
// implementation never used in production).
func (pm *PropManager) propFollowAndUnlink(dst *Prop, src *Prop, skipme *Subscription, origin string, prependvec []*Prop) {
	src.mu.RLock()
	srcOrig := src.originator
	src.mu.RUnlock()

	if srcOrig != nil {
		pm.propFollowAndUnlink(dst, srcOrig, skipme, origin,
			append(prependvec, src))
		return
	}

	if pm.searchForLinkage(src, dst) {
		pm.restoreAndDescend(dst, src, skipme, origin, dst, prependvec)
	}
}

// searchForLinkage — C: search_for_linkage (prop_core.c:4513-4554).
// Returns true if any subscription in src's subtree has `link` in its
// origin chain (hps_origin or hps_pots).
func (pm *PropManager) searchForLinkage(src *Prop, link *Prop) bool {
	// C: while(src->hp_originator != NULL) src = src->hp_originator
	src.mu.RLock()
	o := src.originator
	src.mu.RUnlock()
	for o != nil {
		src = o
		src.mu.RLock()
		o = src.originator
		src.mu.RUnlock()
	}

	src.mu.RLock()
	for _, s := range src.valueSubs {
		if subHasOrigin(s, link) {
			src.mu.RUnlock()
			return true
		}
	}
	isDir := src.propType == PropTypeDir
	children := make([]*Prop, len(src.children))
	copy(children, src.children)
	src.mu.RUnlock()

	if !isDir {
		return false
	}

	for _, c := range children {
		if c.name == "" {
			continue
		}
		if pm.searchForLinkage(c, link) {
			return true
		}
	}
	return false
}

// restoreAndDescend — C: restore_and_descend (prop_core.c:4560-4634).
// Moves subs whose origin chain contains brokenLink from src back to dst,
// then descends src's named children looking for deeper subs that also
// traversed the broken link, creating matching children on dst.
func (pm *PropManager) restoreAndDescend(dst *Prop, src *Prop, skipme *Subscription, origin string, brokenLink *Prop, prependvec []*Prop) {
	// C: for(s = LIST_FIRST(&src->hp_value_subscriptions); s; s = next)
	src.mu.Lock()
	var matched []*Subscription
	var remaining []*Subscription
	for _, s := range src.valueSubs {
		if subHasOrigin(s, brokenLink) {
			matched = append(matched, s)
		} else {
			remaining = append(remaining, s)
		}
	}
	src.valueSubs = remaining
	src.subs = make([]*Subscription, 0, len(src.canonicalSubs)+len(src.valueSubs))
	src.subs = append(src.subs, src.canonicalSubs...)
	src.subs = append(src.subs, src.valueSubs...)
	src.mu.Unlock()

	var decRefs []*Prop
	for _, s := range matched {
		// C: remove broken_link from origins (prop_ref_dec_locked + clear)
		decRefs = append(decRefs, removeSubOrigin(s, brokenLink)...)
		pm.retargetSubscription(dst, s, skipme, origin)
		// C: prepend_origins(s, prependvec, prependveclen)
		prependOrigins(s, prependvec)
	}
	for _, p := range decRefs {
		pm.RefDec(p)
	}

	// C: if(src->hp_type != PROP_DIR) return
	src.mu.RLock()
	srcIsDir := src.propType == PropTypeDir
	srcChildren := make([]*Prop, len(src.children))
	copy(srcChildren, src.children)
	src.mu.RUnlock()
	if !srcIsDir {
		return
	}

	// C: TAILQ_FOREACH(c, &src->hp_childs, hp_parent_link) — descend into
	// each named child; resolve its originator chain to the real child,
	// and restore subs there if any traversed broken_link.
	for _, c := range srcChildren {
		if c.name == "" {
			continue
		}

		// C: prop_t *s = c; while(s->hp_originator != NULL) s = s->hp_originator
		s := c
		for {
			s.mu.RLock()
			so := s.originator
			s.mu.RUnlock()
			if so == nil {
				break
			}
			s = so
		}

		if !pm.searchForLinkage(s, brokenLink) {
			continue
		}

		// C: prop_t *z = prop_create0(dst, c->hp_name, NULL,
		//                             c->hp_flags & PROP_NAME_NOT_ALLOCATED)
		z := pm.CreateEx(dst, c.name, nil, false, false)

		// C: if(c->hp_type == PROP_DIR) prop_make_dir(z, skipme, origin)
		c.mu.RLock()
		cIsDir := c.propType == PropTypeDir
		c.mu.RUnlock()
		if cIsDir {
			pm.MakeDir(z)
		}

		pm.restoreAndDescend(z, s, skipme, origin, brokenLink, prependvec)
	}
}

// prependOrigin — C: prepend_origin (prop_core.c:4406-4438).
// Adds `prepend` to the subscription's origin set (single hps_origin or
// the hps_pots list head). Each stored origin holds a prop ref.
func prependOrigin(s *Subscription, prepend *Prop) {
	if s.hpsOrigin == nil {
		s.hpsOrigin = prepend
		prepend.manager.RefInc(prepend)
		return
	}

	if !s.hpsMultipleOrigins {
		if prepend == s.hpsOrigin {
			return
		}
		// C: existing hps_origin becomes the tail pot (its ref transfers)
		s.hpsPots = []*Prop{s.hpsOrigin}
		s.hpsOrigin = nil
		s.hpsMultipleOrigins = true
	} else {
		if slices.Contains(s.hpsPots, prepend) {
			return
		}
	}

	// C: LIST_INSERT_HEAD equivalent — prepend at head of pots
	prepend.manager.RefInc(prepend)
	s.hpsPots = slices.Insert(s.hpsPots, 0, prepend)
}

// prependOrigins — C: prepend_origins (prop_core.c:4444-4449).
func prependOrigins(s *Subscription, vec []*Prop) {
	for _, p := range vec {
		prependOrigin(s, p)
	}
}

// subHasOrigin — C: the origin checks in search_for_linkage /
// restore_and_descend — true if `link` is in the sub's origin set.
func subHasOrigin(s *Subscription, link *Prop) bool {
	if s.hpsOrigin == nil && !s.hpsMultipleOrigins {
		return false
	}
	if !s.hpsMultipleOrigins {
		return s.hpsOrigin == link
	}
	return slices.Contains(s.hpsPots, link)
}

// removeSubOrigin removes one occurrence of `link` from the sub's origin
// set (C: the pot/origin removal in restore_and_descend, prop_core.c:4588-4625).
// Returns props whose refs must be released by the caller.
func removeSubOrigin(s *Subscription, link *Prop) (decRefs []*Prop) {
	if !s.hpsMultipleOrigins {
		if s.hpsOrigin != link {
			return nil
		}
		decRefs = append(decRefs, s.hpsOrigin)
		s.hpsOrigin = nil
		return decRefs
	}

	idx := -1
	for i, pot := range s.hpsPots {
		if pot == link {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil
	}
	decRefs = append(decRefs, s.hpsPots[idx])
	s.hpsPots = slices.Delete(s.hpsPots, idx, idx+1)

	// C: if only one pot remains, transform into single ref
	if len(s.hpsPots) == 1 {
		s.hpsOrigin = s.hpsPots[0]
		s.hpsPots = nil
		s.hpsMultipleOrigins = false
	}
	return decRefs
}

// releaseSubOriginRefs drops all origin refs held by the subscription
// (C: prop_ref_dec_locked(s->hps_origin) + pot frees on sub destroy).
func releaseSubOriginRefs(s *Subscription) {
	if s.hpsOrigin != nil {
		s.hpsOrigin.manager.RefDec(s.hpsOrigin)
		s.hpsOrigin = nil
	}
	for _, pot := range s.hpsPots {
		pot.manager.RefDec(pot)
	}
	s.hpsPots = nil
	s.hpsMultipleOrigins = false
}

// GetFlags gets property flags
func (pm *PropManager) Link(source *Prop, target *Prop, opaque any, fromSubscriptions bool, canBeAnonymous bool) int {
	return pm.linkInternal(source, target, opaque, fromSubscriptions, canBeAnonymous, LinkSoft)
}

// LinkHard establishes a hard link with xref.
// C: prop_link_hard(src, dst, skipme) — hard = PROP_LINK_XREFED.
func (pm *PropManager) LinkHard(source *Prop, target *Prop, opaque any, hard int) int {
	return pm.linkInternal(source, target, opaque, false, false, hard)
}
func (pm *PropManager) linkInternal(source *Prop, target *Prop, opaque any, fromSubscriptions bool, canBeAnonymous bool, hard int) int {
	if source == nil || target == nil {
		return -1
	}
	if source == target {
		return -1
	}

	// Extract skipme subscription from opaque
	var skipme *Subscription
	if opaque != nil {
		if sub, ok := opaque.(*Subscription); ok {
			skipme = sub
		}
	}

	// Check for zombie/destroyed props (C: PROP_ZOMBIE check)
	source.mu.RLock()
	srcDestroyed := source.destroyed
	srcProxy := source.propType == PropTypeProxy
	source.mu.RUnlock()
	target.mu.RLock()
	dstDestroyed := target.destroyed
	dstProxy := target.propType == PropTypeProxy
	target.mu.RUnlock()
	if srcDestroyed || dstDestroyed {
		return -1
	}

	// C: prop_link_exl (prop_core.c:4838-4846) —
	// both PROP_PROXY → prop_proxy_link(src, dst); mixed → abort().
	if srcProxy && dstProxy {
		// C: prop_proxy_link is a printf("not implemeted") stub upstream.
		if pm.logCallback != nil {
			pm.logCallback("prop_proxy_link not implemeted")
		}
		return 0
	}
	if srcProxy || dstProxy {
		// C: prints both trees then abort()s.
		panic("Linking a proxied property with a non-proxied one is mind-boggling difficult")
	}

	// If target already has an originator, unlink it first.
	// C: prop_unlink0(dst, skipme, "prop_link()/unlink", &pnq)
	target.mu.Lock()
	if target.originator == source {
		// Duplicate link is a NOP (C: "Linking against itself again, this is a NOP")
		target.mu.Unlock()
		return 0
	}
	prevOriginator := target.originator
	target.mu.Unlock()

	if prevOriginator != nil {
		pm.unlinkInternal(target, nil, "prop_link()/relink")
	}

	// Now establish the link.
	// Lock in consistent order to avoid deadlocks.
	first, second := source, target
	if uintptr(unsafe.Pointer(source)) > uintptr(unsafe.Pointer(target)) {
		first, second = target, source
	}
	first.mu.Lock()
	second.mu.Lock()

	// Re-check destroyed after acquiring locks
	if source.destroyed || target.destroyed {
		first.mu.Unlock()
		second.mu.Unlock()
		return -1
	}

	// Re-check originator (may have changed during unlock/relock)
	if target.originator == source {
		first.mu.Unlock()
		second.mu.Unlock()
		return 0 // duplicate link is NOP
	}
	if target.originator != nil {
		oldOrigin := target.originator
		target.originator = nil
		for i, t := range oldOrigin.targets {
			if t == target {
				oldOrigin.targets = slices.Delete(oldOrigin.targets, i, i+1)
				break
			}
		}
	}

	// Set up the link (C: dst->hp_originator = src; LIST_INSERT_HEAD(&src->hp_targets, dst, ...))
	target.linkedTo = source
	target.originator = source
	source.targets = append(source.targets, target)

	// C: if hard == PROP_LINK_XREFED || (hard == PROP_LINK_XREFED_IF_ORPHANED && src->hp_parent == NULL)
	// C:   dst->hp_flags |= PROP_XREFED_ORIGINATOR; src->hp_xref++
	if hard == LinkXrefed || (hard == LinkXrefedIfOrphaned && source.parent == nil) {
		target.flags |= FlagXrefedOrigin
		if source.xref < 255 {
			source.xref++
		}
	}

	// Release locks for recursive relink (will re-acquire as needed)
	source.mu.Unlock()
	target.mu.Unlock()

	// C: relink_subscriptions(src, dst, skipme, "relink_tree()", &dst, 1)
	// (prop_core.c:4771) — dst itself is the first prepended origin, so
	// every moved sub records the link dst in its origin chain. Without
	// it, search_for_linkage(src, dst) cannot find the subs on unlink
	// and they would never be restored to dst's own values.
	pm.relinkSubscriptionsRecursive(source, target, skipme, []*Prop{target})

	return 0
}

// relinkSubscriptionsRecursive moves all valueSubs from dst to src,
// and recursively relinks children. C: relink_subscriptions (prop_core.c:4456).
// prependvec accumulates the chain of link-dst props traversed on the src
// side (C: prop_t **prependvec) — each moved sub gets them prepended to
// its origin set via prepend_origins.
func (pm *PropManager) relinkSubscriptionsRecursive(src *Prop, dst *Prop, skipme *Subscription, prependvec []*Prop) {
	if src == nil || dst == nil || src == dst {
		return
	}

	// C: if(src->hp_originator != NULL) — the source is itself linked;
	// the real value lives on its originator, so retarget there instead
	// (prop_core.c:4458-4469). The traversed link (src) is prepended to
	// prependvec so moved subs record it in their origin chain.
	src.mu.RLock()
	orig := src.originator
	src.mu.RUnlock()
	if orig != nil {
		pm.relinkSubscriptionsRecursive(orig, dst, skipme,
			append(prependvec, src))
		return
	}

	// C: while((s = LIST_FIRST(&dst->hp_value_subscriptions)) != NULL)
	//    retarget_subscription(src, s, skipme, origin, NULL);
	//    prepend_origins(s, prependvec, prependveclen);
	dst.mu.Lock()
	subsToMove := make([]*Subscription, len(dst.valueSubs))
	copy(subsToMove, dst.valueSubs)
	dstChildren := make([]*Prop, len(dst.children))
	copy(dstChildren, dst.children)
	dst.mu.Unlock()

	for _, sub := range subsToMove {
		pm.retargetSubscription(src, sub, skipme, "prop_link()/relink")
		prependOrigins(sub, prependvec)
	}

	// C: if(dst->hp_type != PROP_DIR) return
	dst.mu.RLock()
	dstIsDir := dst.propType == PropTypeDir
	dst.mu.RUnlock()
	if !dstIsDir {
		return
	}

	// C: switch(src->hp_type) { DIR: break; VOID: prop_make_dir(src);
	//    default: return; }
	src.mu.RLock()
	srcType := src.propType
	src.mu.RUnlock()
	switch srcType {
	case PropTypeDir:
	case PropTypeVoid:
		pm.MakeDir(src)
	default:
		return
	}

	// Recursively relink named children (C: prop_create0 + prop_make_dir)
	for _, dstChild := range dstChildren {
		if dstChild.name == "" {
			continue // C: if(c->hp_name == NULL) continue
		}
		srcChild := src.GetChild(dstChild.name)
		if srcChild == nil {
			srcChild = pm.CreateEx(src, dstChild.name, nil, false, false)
		}
		if srcChild == nil {
			continue
		}
		dstChild.mu.RLock()
		dstType := dstChild.propType
		dstChild.mu.RUnlock()
		if dstType == PropTypeDir {
			pm.MakeDir(srcChild)
		}
		pm.relinkSubscriptionsRecursive(srcChild, dstChild, skipme, prependvec)
	}
}

// TagSet sets a tag on a property
// TagSet sets a tag on a property, supporting multiple tags per prop.
// C: prop_tag_set (prop_tags.c:83) — prepends to hp_tags linked list.
// TagSet sets a tag on a property. C: prop_tag_set (prop_tags.c:83-94)
// C: always mallocs a new node and prepends it — duplicate keys ARE allowed.
// TagGet returns the first (most recently set) matching value.
func (p *Prop) SetSelectedChild(child *Prop) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.selectedChild = child
}

// SetPropType sets the property type
func ProxyWantMoreChilds(s *Subscription) {
	if s == nil {
		return
	}
	if s.proxyConn != nil {
		if w, ok := s.proxyConn.(ProxyWantMorer); ok {
			w.ProxyWantMoreChilds(s)
			return
		}
	}
	if s.prop == nil {
		return
	}
	p := s.prop
	if p.linkedTo != nil {
		p.linkedTo.mu.RLock()
		subs := make([]*Subscription, len(p.linkedTo.subs))
		copy(subs, p.linkedTo.subs)
		p.linkedTo.mu.RUnlock()
		for _, sub := range subs {
			notifySub(sub, EventWantMoreChilds, nil)
		}
	}
}

// ProxySelect selects a prop: C prop_select(p) = prop_select_ex(p,NULL,NULL)
// (prop_core.c:4946) — notifies the PARENT's subs via PROP_SELECT_CHILD with
// the child as arg and sets parent->hp_selected. PROP_PROXY props delegate to
// prop_proxy_select (handled inside SelectChildPropEx).
func ProxySelect(p *Prop) {
	if p == nil || p.manager == nil {
		return
	}
	p.manager.SelectChildPropEx(p, nil, nil)
}

// PropProxyUnsubscribe unsubscribes from a proxy property
func ProxyUnsubscribe(pm *PropManager, s *Subscription) {
	if s == nil {
		return
	}
	s.Unsubscribe()
}

// ProxyBackend represents a proxy backend type
type ProxyBackend struct {
	// fallback
}

// ProxySetter is implemented by proxy connections to forward value
// changes to a remote peer. This avoids a circular dependency between
// pkg/prop/core and pkg/proxy.
// C: prop_proxy_set_int/float/string/void/uri in prop_proxy.c
// C: there is no prop_proxy_set_uri — prop_set_uri_exl on a PROP_PROXY is
// refused by prop_clean, so ProxySetter has no URI member.
type ProxySetter interface {
	ProxySetInt(prop *Prop, v int)
	ProxySetFloat(prop *Prop, v float32)
	ProxySetString(prop *Prop, v string, strType StringType)
	ProxySetVoid(prop *Prop)
}

// ProxyCreator is implemented by proxy connections to delegate child
// creation to the proxy target.
// C: prop_proxy_create (prop_proxy.c:313-330) — creates a child prop on
// the remote peer, appending the name to the parent's proxy prefix.
type ProxyCreator interface {
	CreateChild(parent *Prop, name string, opaque any, canBeAnonymous, fromSubscriptions bool) *Prop
}

// ProxySubscriber is implemented by proxy connections to forward
// subscriptions to the remote peer.
// C: prop_proxy_subscribe (prop_proxy.c:1227-1278) — registers the sub on
// ppc_subs, assigns hps_proxy_subid, sends STPP_CMD_SUBSCRIBE.
type ProxySubscriber interface {
	ProxySubscribe(s *Subscription, value *Prop)
}

// ProxyUnsubscriber is implemented by proxy connections to tear down
// remote subscriptions.
// C: prop_proxy_unsubscribe (prop_proxy.c:1284-1298) — destroys the sub's
// value prop and prop tree, sends STPP_CMD_UNSUBSCRIBE.
type ProxyUnsubscriber interface {
	ProxyUnsubscribe(s *Subscription)
}

// ProxySelector is implemented by proxy connections to forward
// prop_select to the remote peer.
// C: prop_proxy_select (prop_proxy.c:1419-1427) — sends STPP_CMD_SELECT.
type ProxySelector interface {
	ProxySelect(p *Prop)
}

// ProxyMover is implemented by proxy connections to forward
// prop_move_before to the remote peer.
// C: prop_proxy_req_move (prop_proxy.c:1184-1201) — sends STPP_CMD_REQ_MOVE.
type ProxyMover interface {
	ProxyReqMove(p, before *Prop)
}

// ProxyWantMorer is implemented by proxy connections to forward
// prop_want_more_childs to the remote peer.
// C: prop_proxy_want_more_childs (prop_proxy.c:1392-1400) — sends
// STPP_CMD_WANT_MORE_CHILDS.
type ProxyWantMorer interface {
	ProxyWantMoreChilds(s *Subscription)
}

// ProxyExtEventSender is implemented by proxy connections to forward
// external events to a remote peer.
// C: prop_proxy_send_event (prop_proxy.c)
type ProxyExtEventSender interface {
	ProxySendExtEvent(prop *Prop, e ExtEvent)
}

// ProxyDestroyer is implemented by proxy connections to clean up proxy
// resources when the prop is destroyed.
// C: prop_proxy_destroy (prop_proxy.c:336-360) — destroys owned props,
// releases the proxy connection, removes owner sub link, frees prefix.
type ProxyDestroyer interface {
	ProxyDestroy(prop *Prop)
}

// ProxyByNamer is implemented by proxy connections to resolve a named
// path below a PROP_PROXY node.
// C: the PROP_PROXY branch of prop_get_by_name (prop_core.c:2853-2886) —
// builds vec = hp_proxy_pfx + names, makes an owned proxy node under the
// parent's remote id, and sets PROP_PROXY_FOLLOW_SYMLINK when requested.
type ProxyByNamer interface {
	ProxyGetByName(p *Prop, names []string, followSymlinks bool) *Prop
}

// ProxyAdder is implemented by proxy connections to forward
// prop_add_int to the remote peer.
// C: prop_add_int_ex PROP_PROXY branch (prop_core.c:4020-4030) →
// prop_proxy_add_int (prop_proxy.c:1366-1371 — a "not implemeted" stub).
type ProxyAdder interface {
	ProxyAddInt(prop *Prop, v int)
}

// ProxyToggler is implemented by proxy connections to forward
// prop_toggle_int to the remote peer.
// C: prop_toggle_int_ex PROP_PROXY branch (prop_core.c:4073-4078) →
// prop_proxy_toggle_int (prop_proxy.c:1374-1387).
type ProxyToggler interface {
	ProxyToggleInt(prop *Prop)
}

// ValueVoid represents a void value
func (pm *PropManager) SetPropEx(parent *Prop, name any, child *Prop) {
	if parent == nil || child == nil {
		return
	}
	if name != nil && name != "" {
		if str, ok := name.(string); ok {
			child.name = str
		}
	}
	parent.AddChild(child)
}

// GetSubscriptionStats returns (totalSubscriptions, activeSubscriptions) across the entire prop tree
func (pm *PropManager) SelectChild(prop *Prop, name string) *Prop {
	if prop == nil {
		return nil
	}
	child := prop.GetChild(name)

	prop.mu.Lock()
	subs := make([]*Subscription, len(prop.valueSubs))
	copy(subs, prop.valueSubs)
	prop.mu.Unlock()

	// C order (prop_core.c:5002-5005): prop_notify_child2 runs BEFORE
	// hp_selected = c — synchronous (INTERNAL/direct) callbacks see the
	// previous selection. Runs even when c == NULL.
	for _, sub := range subs {
		notifySub(sub, EventSelectChild, child, prop)
	}

	prop.mu.Lock()
	prop.selectedChild = child
	prop.mu.Unlock()

	return child
}

// SelectChildProp selects a specific child property (not by name).
// This matches C's prop_select_ex(p, extra, skipme) where p is the
// child prop to select.
func (pm *PropManager) SelectChildProp(child *Prop, extra *Prop) {
	pm.SelectChildPropEx(child, extra, nil)
}

// SelectChildPropEx is C's prop_select_ex: skipme's subscription is
// excluded from the PROP_SELECT_CHILD notification (prop_core.c:4948).
func (pm *PropManager) SelectChildPropEx(child *Prop, extra *Prop, skipme *Subscription) {
	if child == nil {
		return
	}
	// C: if(p->hp_type == PROP_ZOMBIE) return; (prop_core.c:4952-4955)
	child.mu.RLock()
	destroyed := child.destroyed
	child.mu.RUnlock()
	if destroyed {
		return
	}
	// C: prop_select_ex (prop_core.c:4957-4960):
	//   if(p->hp_type == PROP_PROXY) { prop_proxy_select(p); return; }
	child.mu.RLock()
	isProxy := child.propType == PropTypeProxy
	pc := child.proxyConn
	child.mu.RUnlock()
	if isProxy && pc != nil {
		if sel, ok := pc.(ProxySelector); ok {
			sel.ProxySelect(child)
			return
		}
	}
	parent := child.parent
	if parent == nil {
		return
	}

	// Fire EventSelectChild to parent's value subscribers
	parent.mu.Lock()
	subs := make([]*Subscription, len(parent.valueSubs))
	copy(subs, parent.valueSubs)
	parent.mu.Unlock()

	// C order (prop_core.c:4964-4965): prop_notify_child2 BEFORE
	// parent->hp_selected = p — direct callbacks observe the old
	// selection during notification.
	for _, sub := range subs {
		if sub == skipme {
			continue // C: hps != skipme
		}
		notifySub(sub, EventSelectChild, child, parent, extra)
	}

	parent.mu.Lock()
	parent.selectedChild = child // C: parent->hp_selected = p
	parent.mu.Unlock()
}

// UnselectChild clears the parent's selection and notifies
// PROP_SELECT_CHILD with a NULL child.
// C: prop_unselect_ex (prop_core.c:4979-4990).
func (pm *PropManager) UnselectChild(parent *Prop) {
	if parent == nil {
		return
	}
	parent.mu.RLock()
	isDir := parent.propType == PropTypeDir
	parent.mu.RUnlock()
	if !isDir {
		return
	}
	parent.mu.Lock()
	subs := make([]*Subscription, len(parent.valueSubs))
	copy(subs, parent.valueSubs)
	parent.mu.Unlock()

	// C: prop_notify_child2(NULL, parent, NULL, PROP_SELECT_CHILD, ...)
	// — child arg is nil (args[0]=nil marks unselect).
	for _, sub := range subs {
		notifySub(sub, EventSelectChild, nil, nil, parent)
	}

	parent.mu.Lock()
	parent.selectedChild = nil
	parent.mu.Unlock()
}

// ToggleInt toggles the int value of a property.
// C: prop_toggle_int_ex(p, NULL) (prop_core.c:4067-4099) — PROXY forwards
// to prop_proxy_toggle_int, ZOMBIE returns, non-INT coerces (FLOAT→INT
// in-place, other types prop_clean then hp_int=0 + type=INT), then
// hp_int = !hp_int + prop_set_epilogue.
func (pm *PropManager) Select(p *Prop) {
	pm.SelectChildPropEx(p, nil, nil)
}

// RequestNewChild fires PROP_REQ_NEW_CHILD on a DIR/VOID prop.
// C: prop_request_new_child (prop_core.c:5143-5154).
func (p *Prop) Link(target *Prop) {
	if p == nil || target == nil || p == target {
		return
	}
	if p.manager != nil {
		p.manager.Link(p, target, nil, false, false)
	}
}

// Unlink unlinks this property from its linked source.
// C: prop_unlink0 (prop_core.c:4693) — restores target's subscriptions and value.
// Go: Delegates to PropManager.unlinkInternal for full C-canonical semantics.
func (p *Prop) Unlink() {
	if p == nil {
		return
	}
	if p.manager != nil {
		p.manager.unlinkInternal(p, nil, "Prop.Unlink")
	}
}

// PropVec returns a property vector
type linkselectedPriv struct {
	current *Prop
	target  *Prop
	name    *Prop
	dir     *Prop
}

// LinkselectedCreate creates a linkselected property
// C: prop_linkselected_create (prop_linkselected.c:114)
// C uses prop_create_r(p, target) which calls prop_create_ex(..., incref=1),
// giving the returned prop ref=2 (1 from creation + 1 from prop_ref_inc).
// The extra ref is dropped in the DESTROYED callback via prop_ref_dec.
// Go: CreateEx returns ref=1, so we must RefInc to match C's prop_create_r.
func (pm *PropManager) LinkselectedCreate(dir *Prop, p *Prop, target string, name string) {
	lp := &linkselectedPriv{dir: dir}

	lp.target = pm.CreateEx(p, target, nil, false, false)
	if lp.target != nil {
		pm.RefInc(lp.target) // C: prop_create_r incref=1
	}
	if name != "" {
		lp.name = pm.CreateEx(p, name, nil, false, false)
		if lp.name != nil {
			pm.RefInc(lp.name) // C: prop_create_r incref=1
		}
	}

	// Subscribe to events on dir
	pm.Subscribe(dir, func(opaque any, eventType EventType, args ...any) {
		lp.eventCallback(opaque, eventType, args...)
	}, lp, SubFlagTrackDestroy)
}

// eventCallback handles events for linkselected
func (lp *linkselectedPriv) eventCallback(opaque any, eventType EventType, args ...any) {
	switch eventType {
	case EventDestroyed:
		// Cleanup
		if lp.target != nil {
			lp.target.Release()
		}
		if lp.name != nil {
			lp.name.Release()
		}
		// Unsubscribe is handled by TRACK_DESTROY

	case EventSelectChild:
		if len(args) > 0 {
			// C: set(lp, va_arg(ap, prop_t *)) — a NULL child
			// (prop_unselect_ex) unlinks lp_target via
			// prop_link(NULL, dst).
			child, _ := args[0].(*Prop)
			lp.set(child)
		}

	case EventAddChild:
		// C: child_added(lp, c, flags) where flags = gen_add_flags(c, parent)
		// = (c == parent->hp_selected ? PROP_ADD_SELECTED : 0).
		// Go EventAddChild args are (child, parent).
		if len(args) > 1 {
			child, _ := args[0].(*Prop)
			parent, _ := args[1].(*Prop)
			if child != nil && parent != nil && child == parent.getSelectedChild() {
				lp.set(child)
			}
		}

	case EventAddChildBefore:
		// Go EventAddChildBefore args are (child, parent, before).
		if len(args) > 2 {
			child, _ := args[0].(*Prop)
			parent, _ := args[1].(*Prop)
			if child != nil && parent != nil && child == parent.getSelectedChild() {
				lp.set(child)
			}
		}

	case EventAddChildVector, EventAddChildVectorDirect:
		// Vector delivery carries no flags (C sends it only when
		// hp_selected == NULL), but a selection may have been made
		// between snapshot and delivery — re-check against the dir.
		sel := lp.dir.getSelectedChild()
		if sel == nil {
			break
		}
		for _, a := range args {
			switch vec := a.(type) {
			case []*Prop:
				for _, c := range vec {
					if c == sel {
						lp.set(c)
					}
				}
			case *PropVec:
				for i := range vec.Len() {
					if c := vec.Get(i); c == sel {
						lp.set(c)
					}
				}
			}
		}

	case EventDelChild:
		if len(args) > 0 {
			if child, ok := args[0].(*Prop); ok && child == lp.current {
				if lp.target != nil {
					lp.target.Unlink()
				}
				lp.current = nil
				if lp.name != nil {
					lp.name.SetVoid()
				}
			}
		}
	}
}

// GetSelectedChild returns the currently selected child (C: p->hp_selected).
func (p *Prop) GetSelectedChild() *Prop {
	return p.getSelectedChild()
}

// getSelectedChild returns the currently selected child (C: p->hp_selected).
func (p *Prop) getSelectedChild() *Prop {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.selectedChild
}

// set sets the current property and links it to target
// C: prop_link(lp->lp_current, lp->lp_target) — lp_target mirrors current.
func (lp *linkselectedPriv) set(p *Prop) {
	lp.current = p
	if lp.target != nil {
		if p != nil {
			p.Link(lp.target)
		} else {
			// C: prop_link(NULL, lp_target) → prop_unlink_exl(lp_target)
			lp.target.Unlink()
		}
	}

	if lp.name == nil {
		return
	}

	if p != nil {
		name := p.GetName()
		lp.name.SetString(name)
	} else {
		// C: prop_set_rstring(lp->lp_name, NULL) → PROP_SET_VOID
		lp.name.SetVoid()
	}
}
func (pm *PropManager) Follow(p *Prop) *Prop {
	for p != nil {
		p.mu.RLock()
		o := p.originator
		p.mu.RUnlock()
		if o == nil {
			return pm.RefInc(p)
		}
		p = o
	}
	return nil
}

// Compare reports whether two props resolve to the same originator.
// C: prop_compare (prop_core.c) — follows both hp_originator chains
// without taking references.
func (pm *PropManager) Compare(a, b *Prop) bool {
	return pm.followNoRef(a) == pm.followNoRef(b)
}

// followNoRef resolves the originator chain without acquiring a reference
// (C: the internal traversal inside prop_follow before prop_ref_inc).
func (pm *PropManager) followNoRef(p *Prop) *Prop {
	for p != nil {
		p.mu.RLock()
		o := p.originator
		p.mu.RUnlock()
		if o == nil {
			return p
		}
		p = o
	}
	return nil
}

// ---------------------------------------------------------------------------
// C: prop_subfind (prop_core.c:2682-2747) — walk a name vector under p,
// following hp_originator chains when followSymlinks, converting VOID
// props to DIR, creating missing children via prop_create0, and
// supporting "*"N positional indexing when allowIndexing.
// If originChain is non-nil, every link-dst prop traversed while following
// an originator is appended to it (C: prop_t **origin_chain).
