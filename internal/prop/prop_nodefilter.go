package prop

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/czz/movian-go/internal/misc"
)

// prop_nodefilter.c — strict C-to-Go port.
// C: src/prop/prop_nodefilter.c (1412 lines)

const nfMaxSortKeys = 4 // C: MAX_SORT_KEYS

// Sort key types (C: nfnode_t.sortkey_type)
const (
	nfSortkeyNone  = 0 // C: SORTKEY_NONE
	nfSortkeyRstr  = 1 // C: SORTKEY_RSTR
	nfSortkeyInt   = 2 // C: SORTKEY_INT
	nfSortkeyFloat = 3 // C: SORTKEY_FLOAT
	nfSortkeyCstr  = 4 // C: SORTKEY_CSTR
	nfSortkeyVoid  = 5 // C: SORTKEY_VOID
)

// PropNFSortStrmap maps a string sort value to an integer ordering key.
// C: prop_nf_sort_strmap_t
type PropNFSortStrmap struct {
	Str string
	Val int
}

// PropNF comparison operators.
// C: prop_nf_cmp_t
type PropNFCmp int

const (
	PropNFCmpEq  PropNFCmp = iota // C: PROP_NF_CMP_EQ
	PropNFCmpNeq                  // C: PROP_NF_CMP_NEQ
)

// PropNF predicate modes.
// C: prop_nf_mode_t
type PropNFMode int

const (
	PropNFModeInclude PropNFMode = iota // C: PROP_NF_MODE_INCLUDE
	PropNFModeExclude                   // C: PROP_NF_MODE_EXCLUDE
)

// PropNF flags.
// C: PROP_NF_TAKE_DST_OWNERSHIP / PROP_NF_AUTODESTROY
const (
	PropNFTakeDstOwnership = 0x1
	PropNFAutoDestroy      = 0x2
)

// nfSortmap — C: sortmap_t (NULL-terminated array of {str,val}).
type nfSortmap struct {
	str string
	val int
}

// nfnPred — C: nfn_pred_t (per-node predicate instance).
type nfnPred struct {
	sub  *nfPathSub  // C: nfnp_sub
	conf *propNFPred // C: nfnp_conf
	nfn  *nfnode     // C: nfnp_nfn
	set  bool        // C: nfnp_set
}

// nfSortkeyVal — C: union sk[MAX_SORT_KEYS] {rstr_t*, const char*, int, float}.
type nfSortkeyVal struct {
	s string  // rstr/cstr
	i int     // int
	f float32 // float
}

// nfnode — C: nfnode_t.
type nfnode struct {
	in  *Prop // C: nfn->in
	out *Prop // C: nfn->out

	multisub *Subscription // C: multisub

	preds []*nfnPred // C: nfn->preds

	nf       *PropNF
	inserted bool

	// seq is a monotonically increasing identity assigned at add time.
	// C uses pointer order (a < b) as the deterministic tie-break in
	// nf_egress_cmp; seq plays that role without an O(n) lookup.
	seq uint64

	sortkeyType [nfMaxSortKeys]int
	sortsub     [nfMaxSortKeys]*nfPathSub
	sk          [nfMaxSortKeys]nfSortkeyVal
}

// propNFPred — C: prop_nf_pred_t.
type propNFPred struct {
	path      []string      // C: pnp_path (strvec)
	cf        PropNFCmp     // C: pnp_cf
	mode      PropNFMode    // C: pnp_mode
	enableSub *Subscription // C: pnp_enable_sub
	id        int           // C: pnp_id

	enabled bool // C: pnp_enabled

	str *string // C: pnp_str (NULL => int pred)
	i   int     // C: pnp_int

	nf *PropNF // C: pnp_nf
}

// PropNF — C: prop_nf_t.
type PropNF struct {
	refcount atomic.Int32 // C: pnf_refcount
	flags    int

	predTally int // C: pred_tally

	nodecount int
	sorted    bool
	src       *Prop
	dst       *Prop
	srcsub    *Subscription
	dstsub    *Subscription

	filtersub *Subscription

	in  []*nfnode // C: nf->in (insertion order, mirrors source order)
	out []*nfnode // C: nf->out_queue / out_tree (egress order)

	filter    string
	hasFilter bool // C: nf->filter != NULL

	sortkey           [nfMaxSortKeys]string
	hasSortkey        [nfMaxSortKeys]bool // C: sortkey[i] != NULL
	sortmap           [nfMaxSortKeys][]nfSortmap
	preds             []*propNFPred // C: nf->preds
	pendingHaveMore   EventType     // C: pending_have_more
	sortorder         [nfMaxSortKeys]int
	sortHideOnMissing [nfMaxSortKeys]bool

	// nodes maps source prop -> nfnode (C: prop_tag_set/get/clear(node, nf))
	nodes map[*Prop]*nfnode

	// nodeSeq hands out nfn.seq values (C: pointer-order identity).
	nodeSeq uint64

	pm *PropManager

	// Serialization — C relies on the global prop_mutex serializing all
	// nodefilter state. Go delivers prop notifications unlocked, so nf
	// callbacks funnel through this dispatch queue instead: the first
	// entrant becomes the drainer and runs queued transitions in FIFO
	// order; callbacks invoked re-entrantly (from inside a transition,
	// e.g. dst->src request translation or mirror lifecycle events) are
	// queued and run right after the current transition completes.
	mu    sync.Mutex
	busy  bool
	queue []func()
}

// dispatch runs f serialized against all other nf state transitions.
// C: all callers hold prop_mutex (prop_nodefilter.c throughout).
func (nf *PropNF) dispatch(f func()) {
	nf.mu.Lock()
	nf.queue = append(nf.queue, f)
	if nf.busy {
		nf.mu.Unlock()
		return
	}
	nf.busy = true
	nf.mu.Unlock()
	for {
		nf.mu.Lock()
		if len(nf.queue) == 0 {
			nf.busy = false
			nf.mu.Unlock()
			return
		}
		fn := nf.queue[0]
		nf.queue = nf.queue[1:]
		nf.mu.Unlock()
		fn()
	}
}

// ---------------------------------------------------------------------------
// nfPathSub — dynamic path subscription.
// Equivalent of C's prop_subscribe(PROP_TAG_NAMED_ROOT, root, "node",
// PROP_TAG_NAME_VECTOR, path): path[0]=="node" resolves to root itself
// (named-root self-alias), subsequent segments resolve direct children.
// Re-resolves as the tree mutates; the leaf prop's value events are
// delivered to cb.
// ---------------------------------------------------------------------------
type nfPathSub struct {
	nf      *PropNF
	root    *Prop
	path    []string // path without the leading "node" component
	cb      func(ev EventType, args ...any)
	nodes   []*Prop
	watches []*Subscription
	leafSub *Subscription
	dead    bool
	// noInitial maps to C's PROP_SUB_NO_INITIAL_UPDATE on the leaf sub.
	noInitial bool
	// dispatch runs callbacks on the owner's serialized context
	// (C: PROP_SUB courier/dispatch-group semantics).
	dispatch func(f func())
}

// PathSub is the exported handle for a named-path subscription.
// C: prop_sub_t of PROP_SUB_NAME kind (prop_core.c).
type PathSub = nfPathSub

// SubscribePath — C: prop_subscribe(flags,
// PROP_TAG_NAME_VECTOR, path, PROP_TAG_CALLBACK, cb) — resolves a
// name-vector under root dynamically, re-arming intermediate levels as
// children appear/disappear. noInitial maps to PROP_SUB_NO_INITIAL_UPDATE.
func SubscribePath(root *Prop, path []string, noInitial bool, cb func(ev EventType, args ...any)) *PathSub {
	s := &nfPathSub{
		root:      root,
		cb:        cb,
		noInitial: noInitial,
		dispatch:  func(f func()) { f() },
	}
	if len(path) > 0 && path[0] == "node" {
		s.path = path[1:]
	} else {
		s.path = path
	}
	s.arm(root, 0)
	return s
}

// Unsubscribe destroys the whole path chain.
// C: prop_unsubscribe (prop_core.c).
func (s *nfPathSub) Unsubscribe() { s.destroy() }

func newNFPathSub(nf *PropNF, root *Prop, path []string, cb func(ev EventType, args ...any)) *nfPathSub {
	return newNamedPathSub(root, path, nf.dispatch, cb)
}

// newNamedPathSub — C: prop_subscribe(PROP_TAG_NAMED_ROOT, root, "node",
// PROP_TAG_NAME_VECTOR, path) — resolves a name-vector under a named
// root, re-arming intermediate levels as children appear/disappear.
// dispatch serializes callbacks (nil → synchronous).
func newNamedPathSub(root *Prop, path []string, dispatch func(func()), cb func(ev EventType, args ...any)) *nfPathSub {
	if dispatch == nil {
		dispatch = func(f func()) { f() }
	}
	s := &nfPathSub{root: root, cb: cb, dispatch: dispatch}
	// C: PROP_TAG_NAMED_ROOT, root, "node" — "node" resolves to root.
	if len(path) > 0 && path[0] == "node" {
		s.path = path[1:]
	} else {
		s.path = path
	}
	s.arm(root, 0)
	return s
}

// arm attaches the watch for path[depth] under parent.
func (s *nfPathSub) arm(parent *Prop, depth int) {
	if s.dead {
		return
	}
	if depth == len(s.path) {
		// Terminal: subscribe to the leaf prop's value.
		// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_nodefilter.c:543)
		flags := SubFlagInternal | SubFlagDontLock
		if s.noInitial {
			flags |= SubNoInitialUpdate
		}
		s.leafSub = parent.Subscribe(func(_ any, ev EventType, args ...any) {
			s.dispatch(func() {
				if !s.dead {
					s.cb(ev, args...)
				}
			})
		}, nil, flags)
		return
	}

	name := s.path[depth]
	idx := depth
	// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_nodefilter.c:550)
	sub := parent.Subscribe(func(_ any, ev EventType, args ...any) {
		s.dispatch(func() { s.watchEvent(idx, name, ev, args) })
	}, nil, SubFlagInternal|SubFlagDontLock)
	for len(s.watches) <= depth {
		s.watches = append(s.watches, nil)
		s.nodes = append(s.nodes, nil)
	}
	s.watches[depth] = sub

	// Resolve an already-existing child.
	if c := parent.GetChild(name); c != nil {
		s.resolveAt(depth, c)
	} else {
		// C: a name-vector subscription whose leaf does not resolve
		// delivers PROP_SET_VOID (prop_build_notify on a void target).
		s.deliverVoid()
	}
}

// deliverVoid notifies the cb that the leaf currently resolves to void.
// Runs through nf.dispatch like all other leaf events; skipped if the
// leaf resolved in the meantime.
func (s *nfPathSub) deliverVoid() {
	if s.dead {
		return
	}
	s.dispatch(func() {
		if !s.dead && s.leafSub == nil {
			s.cb(EventSetVoid)
		}
	})
}

// watchEvent handles child add/del events at a non-leaf path level.
// Runs inside nf.dispatch.
func (s *nfPathSub) watchEvent(idx int, name string, ev EventType, args []any) {
	if s.dead {
		return
	}
	switch ev {
	case EventAddChild, EventAddChildBefore:
		if len(args) > 0 {
			if c, ok := args[0].(*Prop); ok && c.GetName() == name {
				s.resolveAt(idx, c)
			}
		}
	case EventAddChildVector, EventAddChildVectorDirect:
		if len(args) > 0 {
			switch vec := args[0].(type) {
			case []*Prop:
				for _, c := range vec {
					if c.GetName() == name {
						s.resolveAt(idx, c)
					}
				}
			case *PropVec:
				for i := range vec.Len() {
					if c := vec.Get(i); c != nil && c.GetName() == name {
						s.resolveAt(idx, c)
					}
				}
			}
		}
	case EventDelChild:
		if len(args) > 0 {
			if c, ok := args[0].(*Prop); ok && idx < len(s.nodes) && s.nodes[idx] == c {
				s.teardownFrom(idx)
			}
		}
	case EventSetVoid, EventDestroyed:
		s.teardownFrom(idx)
	}
}

// resolveAt records the node for path[depth] and arms the next level.
func (s *nfPathSub) resolveAt(depth int, node *Prop) {
	if s.dead {
		return
	}
	for len(s.nodes) <= depth {
		s.nodes = append(s.nodes, nil)
	}
	if s.nodes[depth] == node {
		return
	}
	s.teardownFrom(depth)
	s.nodes = append(s.nodes, node)
	s.arm(node, depth+1)
}

// teardownFrom unsubscribes everything at and below depth.
func (s *nfPathSub) teardownFrom(depth int) {
	for i := len(s.nodes) - 1; i >= depth; i-- {
		s.nodes[i] = nil
	}
	s.nodes = s.nodes[:min(depth, len(s.nodes))]
	for i := len(s.watches) - 1; i > depth; i-- {
		if s.watches[i] != nil {
			s.watches[i].Unsubscribe()
		}
	}
	s.watches = s.watches[:min(depth+1, len(s.watches))]
	if s.leafSub != nil {
		s.leafSub.Unsubscribe()
		s.leafSub = nil
		// C: losing the leaf prop delivers PROP_SET_VOID to the sub.
		s.deliverVoid()
	}
}

// destroy unsubscribes the whole chain.
// C: prop_unsubscribe0
func (s *nfPathSub) destroy() {
	if s == nil {
		return
	}
	s.dead = true
	for _, w := range s.watches {
		if w != nil {
			w.Unsubscribe()
		}
	}
	s.watches = nil
	s.nodes = nil
	if s.leafSub != nil {
		s.leafSub.Unsubscribe()
		s.leafSub = nil
	}
}

// ---------------------------------------------------------------------------
// sortmap
// ---------------------------------------------------------------------------

// sortmapCreate — C: sortmap_create (copies NULL-terminated strmap array).
func sortmapCreate(src []PropNFSortStrmap) []nfSortmap {
	if src == nil {
		return nil
	}
	out := make([]nfSortmap, 0, len(src))
	for _, s := range src {
		out = append(out, nfSortmap{str: s.Str, val: s.Val})
		if s.Str == "" {
			break // C: NULL terminator
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// predicates
// ---------------------------------------------------------------------------

// evalPreds evaluates all predicates for a node.
// Returns true if the node should be filtered out.
// C: eval_preds
func evalPreds(nfn *nfnode) bool {
	for _, nfnp := range nfn.preds {
		if !nfnp.set {
			continue
		}
		pnp := nfnp.conf
		if pnp.mode == PropNFModeInclude {
			if !pnp.enabled {
				return true
			}
		} else {
			if pnp.enabled {
				return true
			}
		}
	}
	return false
}

// filterstr — C: filterstr (case-insensitive substring match)
func filterstr(s, q string) bool {
	return misc.Mystrstr(s, q) != ""
}

// nfFiltercheck — C: nf_filtercheck (recursive filter over prop value)
func nfFiltercheck(p *Prop, q string) bool {
	// C: while(p->hp_originator != NULL) p = p->hp_originator;
	for {
		p.mu.RLock()
		o := p.originator
		p.mu.RUnlock()
		if o == nil {
			break
		}
		p = o
	}

	p.mu.RLock()
	pt := p.propType
	val := p.value
	children := p.children
	p.mu.RUnlock()

	switch pt {
	case PropTypeString:
		// C: PROP_RSTRING / PROP_CSTRING both hold a string here
		if s, ok := val.(string); ok {
			return filterstr(s, q)
		}
	case PropTypeURI:
		if uv, ok := val.(URIValue); ok {
			return filterstr(uv.Title, q)
		}
	case PropTypeDir:
		for _, c := range children {
			if nfFiltercheck(c, q) {
				return true
			}
		}
	}
	return false
}

// nfEgressCmp — C: nf_egress_cmp (sort comparator over MAX_SORT_KEYS).
func nfEgressCmp(a, b *nfnode) int {
	nf := a.nf
	for i := range nfMaxSortKeys {
		if !nf.hasSortkey[i] {
			continue
		}
		if a.sortkeyType[i] != b.sortkeyType[i] {
			return a.sortkeyType[i] - b.sortkeyType[i]
		}
		var r int
		switch a.sortkeyType[i] {
		case nfSortkeyRstr, nfSortkeyCstr:
			r = misc.Dictcmp(a.sk[i].s, b.sk[i].s, a.nf.pm.IgnoreThePrefix())
		case nfSortkeyInt:
			r = a.sk[i].i - b.sk[i].i
		case nfSortkeyFloat:
			if a.sk[i].f < b.sk[i].f {
				r = -1
			} else if a.sk[i].f > b.sk[i].f {
				r = 1
			}
		}
		if r != 0 {
			return r * nf.sortorder[i]
		}
	}
	// C: return a < b ? -1 : 1 (pointer order) — seq preserves the same
	// deterministic total order without an O(n) scan per comparison.
	if a.seq < b.seq {
		return -1
	}
	return 1
}

func indexOfNFN(l []*nfnode, n *nfnode) int {
	for i, x := range l {
		if x == n {
			return i
		}
	}
	return -1
}

// nfInsertNode inserts a node according to the sorting criteria.
// Optionally moves the output node if it's created.
// C: nf_insert_node
func nfInsertNode(nf *PropNF, nfn *nfnode) {
	if nfn.inserted {
		nf.out = removeNFN(nf.out, nfn)
	}

	nfn.inserted = true

	if nf.sorted {
		// C: RB_INSERT_SORTED(&nf->out_tree, nfn, out_tree_link,
		// nf_egress_cmp) — O(log n) tree insert; binary search is the
		// slice equivalent.
		idx := sort.Search(len(nf.out), func(i int) bool {
			return nfEgressCmp(nfn, nf.out[i]) < 0
		})
		nf.out = insertNFNAt(nf.out, idx, nfn)
	} else {
		// Insert before the next *inserted* node following nfn in nf.in.
		var b *nfnode
		pos := indexOfNFN(nf.in, nfn)
		for i := pos + 1; i < len(nf.in); i++ {
			if nf.in[i].inserted {
				b = nf.in[i]
				break
			}
		}
		if b != nil {
			bi := indexOfNFN(nf.out, b)
			nf.out = insertNFNAt(nf.out, bi, nfn)
		} else {
			nf.out = append(nf.out, nfn)
		}
	}

	if nfn.out == nil {
		return
	}

	// Find next node after nfn in egress order that has an out prop.
	b := nextOutNode(nf, nfn)
	var before *Prop
	if b != nil {
		before = b.out
	}
	// C: prop_move0(nfn->out, b ? b->out : NULL, nf->dstsub)
	nf.pm.SetParentEx(nfn.out, nf.dst, &SetParentOpaque{Before: before, Skipme: nf.dstsub}, "")
}

func nextOutNode(nf *PropNF, nfn *nfnode) *nfnode {
	pos := indexOfNFN(nf.out, nfn)
	for i := pos + 1; i >= 0 && i < len(nf.out); i++ {
		if nf.out[i].out != nil {
			return nf.out[i]
		}
	}
	return nil
}

func removeNFN(l []*nfnode, n *nfnode) []*nfnode {
	for i, x := range l {
		if x == n {
			return slices.Delete(l, i, i+1)
		}
	}
	return l
}

func insertNFNAt(l []*nfnode, idx int, n *nfnode) []*nfnode {
	l = append(l, nil)
	copy(l[idx+1:], l[idx:])
	l[idx] = n
	return l
}

// nfUpdateEgress updates the node in the egress property tree.
// C: nf_update_egress
func nfUpdateEgress(nf *PropNF, nfn *nfnode) {
	en := true

	// If sorting is enabled but this node doesn't have a key, hide it
	for i := range nfMaxSortKeys {
		if nf.hasSortkey[i] && nfn.sortkeyType[i] == nfSortkeyNone &&
			nf.sortHideOnMissing[i] {
			en = false
		}
	}

	// Check filtering
	if en && nf.hasFilter && !nfFiltercheck(nfn.in, nf.filter) {
		en = false
	}

	if evalPreds(nfn) {
		en = false
	}

	if en == (nfn.out != nil) {
		return
	}

	if en {
		// C: nfn->out = prop_make(nfn->in->hp_name, 1, NULL)
		nfn.out = NewStandaloneProp(nfn.in.GetName())
		// C: prop_link0(nfn->in, nfn->out, NULL, 0, 0)
		nf.pm.Link(nfn.in, nfn.out, nil, false, false)

		b := nextOutNode(nf, nfn)
		var before *Prop
		if b != nil {
			before = b.out
		}
		// C: prop_set_parent0(nfn->out, nf->dst, b ? b->out : NULL, nf->dstsub)
		nf.pm.SetParentEx(nfn.out, nf.dst, &SetParentOpaque{Before: before, Skipme: nf.dstsub}, "")
	} else {
		// C: prop_destroy0(nfn->out)
		nf.pm.Destroy(nfn.out)
		nfn.out = nil
	}
}

// nfMultiFilter — C: nf_multi_filter
func nfMultiFilter(opaque any, ev EventType, args ...any) {
	nfn := opaque.(*nfnode)
	nfUpdateEgress(nfn.nf, nfn)
}

// nfUpdateMultisub — C: nf_update_multisub
func nfUpdateMultisub(nf *PropNF, nfn *nfnode) {
	if nf.hasFilter == (nfn.multisub != nil) {
		return
	}

	if nf.hasFilter {
		// C: PROP_SUB_INTERNAL | PROP_SUB_MULTI | PROP_SUB_DONTLOCK
		// (prop_nodefilter.c:461)
		nfn.multisub = nfn.in.Subscribe(func(o any, ev EventType, a ...any) {
			nf.dispatch(func() { nfMultiFilter(o, ev, a...) })
		}, nfn, SubFlagMulti|SubFlagInternal|SubFlagDontLock)
	} else {
		nfn.multisub.Unsubscribe()
		nfn.multisub = nil
	}
}

// nfnpUpdateStr — C: nfnp_update_str (CALLBACK_STRING trampoline output)
func nfnpUpdateStr(nfnp *nfnPred, str string) {
	pnp := nfnp.conf
	nfn := nfnp.nfn

	s := false
	switch pnp.cf {
	case PropNFCmpEq:
		s = str == *pnp.str
	case PropNFCmpNeq:
		s = str != *pnp.str
	}
	if nfnp.set == s {
		return
	}
	nfnp.set = s
	nfUpdateEgress(nfn.nf, nfn)
}

// nfnpUpdateInt — C: nfnp_update_int (CALLBACK_INT trampoline output)
func nfnpUpdateInt(nfnp *nfnPred, val int) {
	pnp := nfnp.conf
	nfn := nfnp.nfn

	s := false
	switch pnp.cf {
	case PropNFCmpEq:
		s = val == pnp.i
	case PropNFCmpNeq:
		s = val != pnp.i
	}
	if nfnp.set == s {
		return
	}
	nfnp.set = s
	nfUpdateEgress(nfn.nf, nfn)
}

// nfnInsertPred — C: nfn_insert_pred
func nfnInsertPred(nf *PropNF, nfn *nfnode, pnp *propNFPred) {
	nfnp := &nfnPred{conf: pnp, nfn: nfn}
	nfn.preds = slices.Insert(nfn.preds, 0, nfnp) // LIST_INSERT_HEAD

	if pnp.str != nil {
		// C: PROP_TAG_CALLBACK_STRING trampoline
		nfnp.sub = newNFPathSub(nf, nfn.in, pnp.path, func(ev EventType, args ...any) {
			switch ev {
			case EventSetRString, EventSetCString:
				if len(args) > 0 {
					if s, ok := args[0].(string); ok {
						nfnpUpdateStr(nfnp, s)
						return
					}
				}
				nfnpUpdateStr(nfnp, "")
			case EventSetURI:
				if len(args) > 0 {
					if s, ok := args[0].(string); ok {
						nfnpUpdateStr(nfnp, s)
						return
					}
				}
				nfnpUpdateStr(nfnp, "")
			default:
				// C: !(flags & PROP_SUB_IGNORE_VOID) -> cb(opaque, NULL)
				nfnpUpdateStr(nfnp, "")
			}
		})
	} else {
		// C: PROP_TAG_CALLBACK_INT trampoline
		nfnp.sub = newNFPathSub(nf, nfn.in, pnp.path, func(ev EventType, args ...any) {
			switch ev {
			case EventSetInt:
				if len(args) > 0 {
					switch v := args[0].(type) {
					case int:
						nfnpUpdateInt(nfnp, v)
					case int64:
						nfnpUpdateInt(nfnp, int(v))
					}
					return
				}
				nfnpUpdateInt(nfnp, 0)
			case EventSetFloat:
				if len(args) > 0 {
					if f, ok := args[0].(float32); ok {
						nfnpUpdateInt(nfnp, int(f))
						return
					}
					if f, ok := args[0].(float64); ok {
						nfnpUpdateInt(nfnp, int(f))
						return
					}
				}
				nfnpUpdateInt(nfnp, 0)
			case EventSetRString, EventSetCString:
				v := 0
				if len(args) > 0 {
					if s, ok := args[0].(string); ok {
						v, _ = strconv.Atoi(strings.TrimSpace(s))
					}
				}
				nfnpUpdateInt(nfnp, v)
			default:
				nfnpUpdateInt(nfnp, 0)
			}
		})
	}
}

// nfnpDestroy — C: nfnp_destroy
func nfnpDestroy(nfn *nfnode, nfnp *nfnPred) {
	for i, x := range nfn.preds {
		if x == nfnp {
			nfn.preds = slices.Delete(nfn.preds, i, i+1)
			break
		}
	}
	nfnp.sub.destroy()
}

// nfnInsertPreds — C: nfn_insert_preds
func nfnInsertPreds(nf *PropNF, nfn *nfnode) {
	for _, pnp := range nf.preds {
		nfnInsertPred(nf, nfn, pnp)
	}
}

// nfSetSortkeyX — C: nf_set_sortkey_x
func nfSetSortkeyX(x int, nfn *nfnode, ev EventType, args []any) {
	nf := nfn.nf

	switch ev {
	case EventSetRString, EventSetURI, EventSetCString:
		var s string
		if len(args) > 0 {
			s, _ = args[0].(string)
		}
		m := nf.sortmap[x]
		if m != nil {
			// C: for(; map->str != NULL; map++) if(!strcmp(map->str, s)) break;
			//     nfn->sk[x].i = map->val;
			// The terminating entry's val is the default.
			for _, e := range m {
				nfn.sk[x].i = e.val
				if e.str == "" || e.str == s {
					break
				}
			}
			nfn.sortkeyType[x] = nfSortkeyInt
		} else {
			nfn.sk[x].s = s
			nfn.sortkeyType[x] = nfSortkeyRstr
		}
	case EventSetInt:
		if len(args) > 0 {
			switch v := args[0].(type) {
			case int:
				nfn.sk[x].i = v
			case int64:
				nfn.sk[x].i = int(v)
			}
		}
		nfn.sortkeyType[x] = nfSortkeyInt
	case EventSetFloat:
		if len(args) > 0 {
			switch v := args[0].(type) {
			case float32:
				nfn.sk[x].f = v
			case float64:
				nfn.sk[x].f = float32(v)
			}
		}
		nfn.sortkeyType[x] = nfSortkeyFloat
	default:
		nfn.sortkeyType[x] = nfSortkeyVoid
	}
	nfInsertNode(nf, nfn)
	nfUpdateEgress(nf, nfn)
}

// nfUpdateOrderX — C: nf_update_order_x
func nfUpdateOrderX(nf *PropNF, nfn *nfnode, x int) {
	if nfn.sortsub[x] != nil {
		nfn.sortsub[x].destroy()
		nfn.sortsub[x] = nil
	}

	if !nf.hasSortkey[x] {
		nfn.sortkeyType[x] = nfSortkeyNone
		nfInsertNode(nf, nfn)
	} else {
		// C: PROP_TAG_NAMESTR splits on '.' — the path is a name vector.
		path := strings.Split(nf.sortkey[x], ".")
		nfn.sortsub[x] = newNFPathSub(nf, nfn.in, path, func(ev EventType, args ...any) {
			nfSetSortkeyX(x, nfn, ev, args)
		})
	}
}

// nfUpdateOrderAll — C: nf_update_order_all
func nfUpdateOrderAll(nf *PropNF, nfn *nfnode) {
	for i := range nfMaxSortKeys {
		nfUpdateOrderX(nf, nfn, i)
	}
}

// nfAddNode — C: nf_add_node
func nfAddNode(nf *PropNF, node *Prop, b *nfnode) {
	// A DEL_CHILD/destroy may have been dispatched before this add:
	// never create a mirror for an already-destroyed source prop.
	if node == nil || node.IsDestroyed() {
		return
	}
	nfn := &nfnode{nf: nf, in: node}
	nfn.seq = nf.nodeSeq
	nf.nodeSeq++

	// C: prop_tag_set(node, nf, nfn)
	if nf.nodes == nil {
		nf.nodes = make(map[*Prop]*nfnode)
	}
	nf.nodes[node] = nfn

	nf.nodecount++

	if b != nil {
		bi := indexOfNFN(nf.in, b)
		nf.in = insertNFNAt(nf.in, bi, nfn)
	} else {
		nf.in = append(nf.in, nfn)
	}

	nfUpdateMultisub(nf, nfn)
	nfnInsertPreds(nf, nfn)
	nfUpdateOrderAll(nf, nfn)
	nfUpdateEgress(nf, nfn)
}

// nfAddNodes — C: nf_add_nodes (vector add)
func nfAddNodes(nf *PropNF, nodes []*Prop, b *nfnode) {
	for _, p := range nodes {
		// Never mirror already-destroyed source props (see nfAddNode).
		if p == nil || p.IsDestroyed() {
			continue
		}
		nf.nodecount++
		nfn := &nfnode{nf: nf, in: p}
		nfn.seq = nf.nodeSeq
		nf.nodeSeq++

		if nf.nodes == nil {
			nf.nodes = make(map[*Prop]*nfnode)
		}
		nf.nodes[p] = nfn

		if b != nil {
			bi := indexOfNFN(nf.in, b)
			nf.in = insertNFNAt(nf.in, bi, nfn)
		} else {
			nf.in = append(nf.in, nfn)
		}

		nfUpdateMultisub(nf, nfn)
		nfnInsertPreds(nf, nfn)
		nfUpdateOrderAll(nf, nfn)
		nfUpdateEgress(nf, nfn)
	}
}

// nfDelNode — C: nf_del_node
func nfDelNode(nf *PropNF, nfn *nfnode) {
	nf.nodecount--
	nf.in = removeNFN(nf.in, nfn)
	nf.out = removeNFN(nf.out, nfn)

	if nfn.out != nil {
		nf.pm.Destroy(nfn.out)
	}

	if nfn.multisub != nil {
		nfn.multisub.Unsubscribe()
	}

	for i := range nfMaxSortKeys {
		if nfn.sortsub[i] != nil {
			nfn.sortsub[i].destroy()
		}
	}

	for len(nfn.preds) > 0 {
		nfnpDestroy(nfn, nfn.preds[0])
	}
}

// nfMoveNode — C: nf_move_node
func nfMoveNode(nf *PropNF, nfn *nfnode, b *nfnode) {
	nf.in = removeNFN(nf.in, nfn)

	if b != nil {
		bi := indexOfNFN(nf.in, b)
		nf.in = insertNFNAt(nf.in, bi, nfn)
	} else {
		nf.in = append(nf.in, nfn)
	}
	nfInsertNode(nf, nfn)
}

// nfFindNode — C: nf_find_node (prop_tag_get)
func nfFindNode(nf *PropNF, node *Prop) *nfnode {
	if node == nil {
		return nil
	}
	return nf.nodes[node]
}

// nfDestroyPred — C: nf_destroy_pred
func nfDestroyPred(nf *PropNF, pnp *propNFPred) {
	for i, x := range nf.preds {
		if x == pnp {
			nf.preds = slices.Delete(nf.preds, i, i+1)
			break
		}
	}
	if pnp.enableSub != nil {
		pnp.enableSub.Unsubscribe()
	}
}

// nfDestroyPreds — C: nf_destroy_preds
func nfDestroyPreds(nf *PropNF) {
	for len(nf.preds) > 0 {
		nfDestroyPred(nf, nf.preds[0])
	}
}

// nfClear — C: nf_clear
func nfClear(nf *PropNF) {
	for len(nf.in) > 0 {
		nfn := nf.in[0]
		delete(nf.nodes, nfn.in) // C: prop_tag_clear(nfn->in, nf)
		nfDelNode(nf, nfn)
	}
}

// propNFRelease0 — C: prop_nf_release0 (lock held by caller)
func propNFRelease0(pnf *PropNF) {
	if pnf.refcount.Add(-1) > 0 {
		return
	}

	if pnf.srcsub != nil {
		pnf.srcsub.Unsubscribe()
	}

	if pnf.flags&PropNFAutoDestroy == 0 {
		nfClear(pnf)
	}

	if pnf.dstsub != nil {
		pnf.dstsub.Unsubscribe()
	}
	if pnf.dst != nil {
		pnf.pm.Destroy(pnf.dst)
	}

	if pnf.filtersub != nil {
		pnf.filtersub.Unsubscribe()
	}

	nfDestroyPreds(pnf)
}

// nfSuggestFocus — C: nf_suggest_focus
func nfSuggestFocus(nf *PropNF, nfn *nfnode) {
	if nfn != nil && nfn.out != nil {
		nf.pm.SuggestFocus(nfn.out)
	}
}

// propNFSrcCb — C: prop_nf_src_cb
func propNFSrcCb(opaque any, ev EventType, args ...any) {
	nf := opaque.(*PropNF)
	nf.dispatch(func() { propNFSrcCb0(nf, ev, args) })
}

// propNFSrcCb0 — C: nf_source_cb body. Runs inside nf.dispatch.
func propNFSrcCb0(nf *PropNF, ev EventType, args []any) {
	// Events queued before release must not create new mirrors.
	if nf.refcount.Load() <= 0 {
		return
	}

	propAt := func(i int) *Prop {
		if i < len(args) {
			if p, ok := args[i].(*Prop); ok {
				return p
			}
		}
		return nil
	}

	switch ev {
	case EventAddChild:
		// C: nf_add_node(nf, va_arg(ap, prop_t *), NULL)
		nfAddNode(nf, propAt(0), nil)

	case EventAddChildBefore:
		// C: nf_add_node(nf, P, nf_find_node(nf, before))
		nfAddNode(nf, propAt(0), nfFindNode(nf, propAt(2)))

	case EventAddChildVector, EventAddChildVectorDirect:
		nfAddNodes(nf, vecProps(args), nil)

	case EventAddChildVectorBefore:
		nfAddNodes(nf, vecProps(args), nfFindNode(nf, propAt(len(args)-1)))

	case EventDelChild:
		// C: nf_del_node(nf, prop_tag_clear(node, nf))
		nfn := nfFindNode(nf, propAt(0))
		delete(nf.nodes, propAt(0))
		if nfn != nil {
			nfDelNode(nf, nfn)
		}

	case EventMoveChild:
		// C: nf_move_node(nf, nf_find_node(p), nf_find_node(q))
		p := nfFindNode(nf, propAt(0))
		q := nfFindNode(nf, propAt(2))
		nfMoveNode(nf, p, q)

	case EventSetDir:
		// no-op

	case EventSetVoid:
		nfClear(nf)

	case EventReqDeleteVector, EventReqDelete:
		// no-op

	case EventDestroyed:
		if nf.srcsub != nil {
			nf.srcsub.Unsubscribe()
		}
		nf.srcsub = nil
		propNFRelease0(nf)

	case EventHaveMoreChildsYes, EventHaveMoreChildsNo:
		if !nf.hasFilter {
			nf.pm.HaveMoreChilds0(nf.dst, ev == EventHaveMoreChildsYes)
		} else {
			nf.pendingHaveMore = ev
		}

	case EventWantMoreChilds, EventReqMoveChild, EventSelectChild:
		// no-op

	case EventSuggestFocus:
		nfSuggestFocus(nf, nfFindNode(nf, propAt(0)))

	default:
		// C: printf("Unhandled event %d\n", event); abort()
	}
}

// vecProps extracts a []*Prop from vector event args.
func vecProps(args []any) []*Prop {
	for _, a := range args {
		switch v := a.(type) {
		case []*Prop:
			return v
		case *PropVec:
			out := make([]*Prop, 0, v.Len())
			for i := range v.Len() {
				out = append(out, v.Get(i))
			}
			return out
		}
	}
	return nil
}

// nfTranslateDelMulti — C: nf_translate_del_multi
func nfTranslateDelMulti(nf *PropNF, in []*Prop) {
	out := make([]*Prop, 0, len(in))
	for _, p := range in {
		for p.GetOriginator() != nil {
			p = p.GetOriginator()
		}
		out = append(out, p)
	}

	// C: prop_notify_childv(out, nf->src, PROP_REQ_DELETE_VECTOR, nf->srcsub, NULL)
	notifySubsSkipme(nf.src, EventReqDeleteVector, nf.srcsub, nil, out)
}

// nfTranslateReqMoveChild — C: nf_translate_req_move_child
func nfTranslateReqMoveChild(nf *PropNF, p, before *Prop) {
	for i := range nfMaxSortKeys {
		if nf.hasSortkey[i] {
			return
		}
	}
	if nf.hasFilter {
		return
	}
	for _, pnp := range nf.preds {
		if pnp.enabled {
			return
		}
	}

	p = p.GetOriginator()
	if before != nil {
		before = before.GetOriginator()
	}
	// C: prop_notify_child2(p, nf->src, before, PROP_REQ_MOVE_CHILD, nf->srcsub, 0)
	notifySubsSkipme(nf.src, EventReqMoveChild, nf.srcsub, p, before)
}

// notifySubsSkipme sends an event to all of p's value subs except skipme.
// C: prop_notify_childv / prop_notify_child2 with skipme.
func notifySubsSkipme(p *Prop, ev EventType, skipme *Subscription, prop *Prop, args ...any) {
	if p == nil {
		return
	}
	p.mu.RLock()
	subs := make([]*Subscription, len(p.valueSubs))
	copy(subs, p.valueSubs)
	p.mu.RUnlock()
	for _, s := range subs {
		if s == skipme {
			continue
		}
		notifySub(s, ev, prop, args...)
	}
}

// propNFDstCb — C: prop_nf_dst_cb
func propNFDstCb(opaque any, ev EventType, args ...any) {
	nf := opaque.(*PropNF)
	nf.dispatch(func() { propNFDstCb0(nf, ev, args) })
}

// propNFDstCb0 — C: nf_dst_cb body. Runs inside nf.dispatch.
func propNFDstCb0(nf *PropNF, ev EventType, args []any) {
	if nf.refcount.Load() <= 0 {
		return
	}

	propAt := func(i int) *Prop {
		if i < len(args) {
			if p, ok := args[i].(*Prop); ok {
				return p
			}
		}
		return nil
	}

	switch ev {
	case EventReqDeleteVector:
		nfTranslateDelMulti(nf, vecProps(args))

	case EventDestroyed:
		// C: abort() — dst destroyed while nf alive is a bug
		panic("prop_nf: dst destroyed while filter alive")

	case EventReqMoveChild:
		nfTranslateReqMoveChild(nf, propAt(0), propAt(2))

	case EventWantMoreChilds:
		if nf.srcsub != nil {
			nf.pm.WantMoreChilds(nf.srcsub)
		}
	}
}

// nfFilterEvent — C: filterprop value cb trampoline (CALLBACK_STRING).
// Runs inside nf.dispatch.
func nfFilterEvent(nf *PropNF, ev EventType, args []any) {
	switch ev {
	case EventSetRString, EventSetCString, EventSetURI:
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				nfSetFilter(nf, s)
				return
			}
		}
		nfSetFilter(nf, "")
	default:
		nfSetFilter(nf, "")
	}
}

// nfEnableEvent — C: enable-prop value cb trampoline (CALLBACK_INT).
// Runs inside nf.dispatch.
func nfEnableEvent(pnp *propNFPred, ev EventType, args []any) {
	switch ev {
	case EventSetInt:
		if len(args) > 0 {
			switch v := args[0].(type) {
			case int:
				pnpSetEnable(pnp, v)
				return
			case int64:
				pnpSetEnable(pnp, int(v))
				return
			}
		}
		pnpSetEnable(pnp, 0)
	case EventSetFloat:
		if len(args) > 0 {
			switch f := args[0].(type) {
			case float32:
				pnpSetEnable(pnp, int(f))
				return
			case float64:
				pnpSetEnable(pnp, int(f))
				return
			}
		}
		pnpSetEnable(pnp, 0)
	case EventSetRString, EventSetCString:
		v := 0
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				v, _ = strconv.Atoi(strings.TrimSpace(s))
			}
		}
		pnpSetEnable(pnp, v)
	default:
		pnpSetEnable(pnp, 0)
	}
}

// nfSetFilter — C: nf_set_filter (CALLBACK_STRING on the filter prop)
func nfSetFilter(nf *PropNF, str string) {
	if str == "" {
		nf.filter = ""
		nf.hasFilter = false
	} else {
		nf.filter = str
		nf.hasFilter = true
	}

	// C: if(nf->filter == NULL && nf->pending_have_more)
	if !nf.hasFilter && nf.pendingHaveMore != 0 {
		nf.pm.HaveMoreChilds0(nf.dst, nf.pendingHaveMore == EventHaveMoreChildsYes)
		nf.pendingHaveMore = 0
	}

	for _, nfn := range nf.in {
		nfUpdateMultisub(nf, nfn)
		nfUpdateEgress(nf, nfn)
	}
}

// PropNFCreate creates a node filter mirroring src into dst.
// C: prop_nf_create (prop_nodefilter.c:1137)
func PropNFCreate(dst, src, filter *Prop, flags int) *PropNF {
	nf := &PropNF{
		flags: flags,
		nodes: make(map[*Prop]*nfnode),
	}
	nf.refcount.Store(1)
	if flags&PropNFAutoDestroy != 0 {
		nf.refcount.Add(1)
	}

	nf.pm = src.Manager()
	if nf.pm == nil && dst != nil {
		nf.pm = dst.Manager()
	}

	if flags&PropNFTakeDstOwnership != 0 {
		nf.dst = dst
	} else if nf.pm != nil {
		nf.dst = nf.pm.XrefAddref(dst)
	} else {
		nf.dst = dst
	}
	nf.src = src

	if filter != nil {
		// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_nodefilter.c:1166)
		nf.filtersub = filter.Subscribe(func(_ any, ev EventType, args ...any) {
			nf.dispatch(func() { nfFilterEvent(nf, ev, args) })
		}, nf, SubFlagInternal|SubFlagDontLock)
	}

	// Subscribe first, then publish the handles under nf.mu so that
	// callbacks dispatched from other goroutines (a shared source/dst
	// prop can fire another nf's subs) observe them with proper
	// happens-before ordering.
	// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_nodefilter.c:1171) —
	// no NO_INITIAL_UPDATE; the initial value event lands in dst_cb's
	// default case (no-op).
	dstsub := dst.Subscribe(propNFDstCb, nf, SubFlagInternal|SubFlagDontLock)
	nf.mu.Lock()
	nf.dstsub = dstsub
	nf.mu.Unlock()

	// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_nodefilter.c:1176)
	srcArgs := []any{SubFlagInternal | SubFlagDontLock}
	if flags&PropNFAutoDestroy != 0 {
		srcArgs = append(srcArgs, SubFlagTrackDestroy)
	}
	srcsub := src.Subscribe(propNFSrcCb, nf, srcArgs...)
	nf.mu.Lock()
	nf.srcsub = srcsub
	nf.mu.Unlock()

	return nf
}

// PropNFRelease releases a reference on the filter.
// C: prop_nf_release
func PropNFRelease(pnf *PropNF) {
	pnf.dispatch(func() { propNFRelease0(pnf) })
}

// PropNFRetain retains a reference on the filter.
// C: prop_nf_retain
func PropNFRetain(pnf *PropNF) *PropNF {
	pnf.refcount.Add(1)
	return pnf
}

// pnpSetEnable — C: pnp_set_enable
func pnpSetEnable(pnp *propNFPred, v int) {
	nf := pnp.nf

	if pnp.enabled == (v != 0) {
		return
	}
	pnp.enabled = v != 0
	if nf == nil {
		return
	}
	for _, nfn := range nf.in {
		nfUpdateEgress(nf, nfn)
	}
}

// propNFPredAdd — C: prop_nf_pred_add
func propNFPredAdd(nf *PropNF, path string, cf PropNFCmp, enable *Prop,
	mode PropNFMode, pnp *propNFPred) int {
	var id int
	nf.dispatch(func() { id = propNFPredAdd0(nf, path, cf, enable, mode, pnp) })
	return id
}

// propNFPredAdd0 — C: prop_nf_pred_add body. Runs inside nf.dispatch.
func propNFPredAdd0(nf *PropNF, path string, cf PropNFCmp, enable *Prop,
	mode PropNFMode, pnp *propNFPred) int {
	nf.predTally++
	pnp.id = nf.predTally
	pnp.path = strings.Split(path, ".")
	pnp.cf = cf
	pnp.mode = mode
	pnp.nf = nf
	nf.preds = slices.Insert(nf.preds, 0, pnp) // LIST_INSERT_HEAD

	if enable != nil {
		// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_nodefilter.c:1258)
		pnp.enableSub = enable.Subscribe(func(_ any, ev EventType, args ...any) {
			nf.dispatch(func() { nfEnableEvent(pnp, ev, args) })
		}, pnp, SubFlagInternal|SubFlagDontLock)
	} else {
		pnp.enabled = true
	}

	for _, nfn := range nf.in {
		nfnInsertPred(nf, nfn, pnp)
	}
	return pnp.id
}

// PropNFPredStrAdd adds a string predicate.
// C: prop_nf_pred_str_add
func PropNFPredStrAdd(nf *PropNF, path string, cf PropNFCmp, str string,
	enable *Prop, mode PropNFMode) int {
	pnp := &propNFPred{str: &str}
	return propNFPredAdd(nf, path, cf, enable, mode, pnp)
}

// PropNFPredIntAdd adds an integer predicate.
// C: prop_nf_pred_int_add
func PropNFPredIntAdd(nf *PropNF, path string, cf PropNFCmp, value int,
	enable *Prop, mode PropNFMode) int {
	pnp := &propNFPred{i: value}
	return propNFPredAdd(nf, path, cf, enable, mode, pnp)
}

// PropNFPredRemove removes a predicate by id.
// C: prop_nf_pred_remove
func PropNFPredRemove(nf *PropNF, id int) {
	nf.dispatch(func() { propNFPredRemove0(nf, id) })
}

// propNFPredRemove0 — C: prop_nf_pred_remove body. Runs inside nf.dispatch.
func propNFPredRemove0(nf *PropNF, id int) {
	if id == 0 {
		return
	}

	var pnp *propNFPred
	for _, x := range nf.preds {
		if x.id == id {
			pnp = x
			break
		}
	}

	if pnp != nil {
		for _, nfn := range nf.in {
			var nfnp *nfnPred
			for _, x := range nfn.preds {
				if x.conf == pnp {
					nfnp = x
					break
				}
			}
			if nfnp != nil {
				nfnpDestroy(nfn, nfnp)
			}
			nfUpdateEgress(nf, nfn)
		}
		nfDestroyPred(nf, pnp)
	}
}

// chkSorted — C: chksorted
func chkSorted(nf *PropNF) bool {
	for i := range nfMaxSortKeys {
		if nf.hasSortkey[i] {
			return true
		}
	}
	return false
}

// PropNFSort sets a sort key.
// C: prop_nf_sort
func PropNFSort(nf *PropNF, path string, desc bool, idx uint,
	m []PropNFSortStrmap, hideOnMissing bool) {
	nf.dispatch(func() { propNFSort0(nf, path, desc, idx, m, hideOnMissing) })
}

// propNFSort0 — C: prop_nf_sort body. Runs inside nf.dispatch.
func propNFSort0(nf *PropNF, path string, desc bool, idx uint,
	m []PropNFSortStrmap, hideOnMissing bool) {
	mfac := 1
	if desc {
		mfac = -1
	}

	if nf.hasSortkey[idx] {
		if path != "" && path == nf.sortkey[idx] && nf.sortorder[idx] == mfac {
			return
		}
	} else {
		if path == "" {
			return
		}
	}

	nf.sortmap[idx] = sortmapCreate(m)

	if path != "" {
		nf.sortkey[idx] = path
		nf.hasSortkey[idx] = true
		nf.sortorder[idx] = mfac
		nf.sortHideOnMissing[idx] = hideOnMissing
	} else {
		nf.sortkey[idx] = ""
		nf.hasSortkey[idx] = false
		nf.sortorder[idx] = 0
		nf.sortHideOnMissing[idx] = false
	}

	if nf.sorted != chkSorted(nf) {
		// Filter switched to sorted mode
		for _, nfn := range nf.in {
			nfn.inserted = false
		}
		nf.sorted = !nf.sorted
		nf.out = nil
	}

	for _, nfn := range nf.in {
		nfUpdateOrderX(nf, nfn, int(idx))
	}
}

// ---------------------------------------------------------------------------
// Method wrappers (Go conveniences matching the package-level C functions)
// ---------------------------------------------------------------------------

// Dst returns the filter's destination prop.
// C: nf->dst
func (nf *PropNF) Dst() *Prop { return nf.dst }

// Src returns the filter's source prop.
// C: nf->src
func (nf *PropNF) Src() *Prop { return nf.src }

// Sort — C: prop_nf_sort with NULL map and hide_on_missing=0.
func (nf *PropNF) Sort(path string, desc bool, idx int) {
	PropNFSort(nf, path, desc, uint(idx), nil, false)
}

// SortEx — C: prop_nf_sort with full arguments.
func (nf *PropNF) SortEx(path string, desc bool, idx int, m []PropNFSortStrmap, hideOnMissing bool) {
	PropNFSort(nf, path, desc, uint(idx), m, hideOnMissing)
}

// AddStrPredicate — C: prop_nf_pred_str_add; cmp is "eq"/"neq",
// mode is "include"/"exclude".
func (nf *PropNF) AddStrPredicate(path, cmp, val, mode string) int {
	cf := PropNFCmpEq
	if cmp == "neq" {
		cf = PropNFCmpNeq
	}
	md := PropNFModeExclude
	if mode == "include" {
		md = PropNFModeInclude
	}
	return PropNFPredStrAdd(nf, path, cf, val, nil, md)
}

// AddIntPredicate — C: prop_nf_pred_int_add; cmp is "eq"/"neq",
// mode is "include"/"exclude".
func (nf *PropNF) AddIntPredicate(path, cmp string, val int, mode string) int {
	cf := PropNFCmpEq
	if cmp == "neq" {
		cf = PropNFCmpNeq
	}
	md := PropNFModeExclude
	if mode == "include" {
		md = PropNFModeInclude
	}
	return PropNFPredIntAdd(nf, path, cf, val, nil, md)
}

// RemovePredicate — C: prop_nf_pred_remove.
func (nf *PropNF) RemovePredicate(id int) { PropNFPredRemove(nf, id) }

// Retain — C: prop_nf_retain.
func (nf *PropNF) Retain() { PropNFRetain(nf) }

// Release — C: prop_nf_release.
func (nf *PropNF) Release() { propNFRelease0(nf) }

// Released reports whether the filter has been fully released.
// C: nf->refcount <= 0 (after prop_nf_release drops the last ref).
func (nf *PropNF) Released() bool { return nf.refcount.Load() <= 0 }
