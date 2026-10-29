package prop

// Typed path lookups: make the C contract explicit in the API.
//
// C prop_subfind conflates three different operations behind flags;
// callers that wanted "read" got "create", which is how mgppCmdSub
// ended up materializing arbitrary subscription paths (and the
// ALT_PATH_D regression). These wrappers declare intent:
//
//   Resolve — read-only, follows links, never creates
//   Ensure  — create-if-missing traversal (C prop_create0 semantics)
//   Follow  — terminal prop through the originator (link) chain
//   ResolveForSubscription — canonical + value + origin chain triple,
//       the pattern every PROP_SUB_NAME-style subscriber needs

// Resolve — read-only path lookup following links. Returns nil when
// any component is missing; never mutates the tree.
func (p *Prop) Resolve(names ...string) *Prop {
	return p.ResolvePath(names, true)
}

// ResolveNoFollow — read-only path lookup, does not traverse links.
func (p *Prop) ResolveNoFollow(names ...string) *Prop {
	return p.ResolvePath(names, false)
}

// Ensure — traversal with create semantics (C: prop_subfind +
// prop_create0): missing children are created, VOID nodes promoted to
// DIR. Use when the tree must exist — NOT for lookups.
func (pm *PropManager) Ensure(root *Prop, names ...string) *Prop {
	return pm.Subfind(root, names, 0, 0, nil)
}

// Follow — already exists in prop_link.go:946 (originator-chain
// terminal + RefInc). This file intentionally does not redeclare it.

// ResolveForSubscription — the canonical+value+origins triple used by
// PROP_SUB_NAME-style subscribers (glw, mgpp, mlp):
//
//	canonical = subfind(root, names, followSymlinks=0) — mirror node
//	value     = subfind(root, names, followSymlinks=1, &origins)
//
// Pass canonical to Subscribe() together with SubValueProp{P: value}
// and SubOrigins{Props: origins} — the origin chain is what lets the
// subscription re-resolve when a link in the path retargets (e.g.
// navigators.current.currentpage on navigation).
func (pm *PropManager) ResolveForSubscription(root *Prop, names []string) (
	canonical *Prop, value *Prop, origins []*Prop) {
	canonical = pm.Subfind(root, names, 0, 0, nil)
	value = pm.Subfind(root, names, 1, 0, &origins)
	return canonical, value, origins
}

// SubscribeResolvedPath — convenience: ResolveForSubscription + the
// standard Subscribe wiring (value prop + origin chain args). extraArgs
// are appended after flags (e.g. SubCourier, SubUserInt).
func (pm *PropManager) SubscribeResolvedPath(root *Prop, names []string,
	flags int, cb func(opaque any, ev EventType, args ...any),
	opaque any, extraArgs ...any) *Subscription {
	canonical, value, origins := pm.ResolveForSubscription(root, names)
	if canonical == nil || value == nil {
		return nil
	}
	subArgs := append([]any{flags}, extraArgs...)
	if value != nil {
		subArgs = append(subArgs, SubValueProp{P: value})
	}
	if len(origins) > 0 {
		subArgs = append(subArgs, SubOrigins{Props: origins})
	}
	return canonical.Subscribe(cb, opaque, subArgs...)
}
