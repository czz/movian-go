package prop

import (
	"fmt"
	"slices"
	"strings"
)

// C: src/prop/prop_grouper.c — pg_node_t
type pgNode struct {
	in      *Prop
	out     *Prop
	sub     *nfPathSub
	grouper *propGrouper
	group   *pgGroup
}

// C: pg_group_t
type pgGroup struct {
	name  string
	root  *Prop
	nodes *Prop
	// pgg_entries — pg_nodes in this group (Go slice; C LIST_ENTRY order
	// is insertion-head, which only affects empty-check semantics).
	entries []*pgNode
}

// C: prop_grouper_t (prop_grouper.h)
type propGrouper struct {
	nodes        []*pgNode
	groups       []*pgGroup
	groupingpath []string
	srcsub       *Subscription
	dst          *Prop
	pm           *PropManager
}

// C: PROP_GROUPER_TAKE_DST_OWNERSHIP (prop_grouper.h)
const PropGrouperTakeDstOwnership = 0x1

// groupFind — C: group_find (prop_grouper.c:88)
func (pg *propGrouper) groupFind(name string) *pgGroup {
	for _, pgg := range pg.groups {
		if pgg.name == name {
			return pgg
		}
	}
	pgg := &pgGroup{name: name}
	pg.groups = slices.Insert(pg.groups, 0, pgg)
	pgg.root = pg.pm.CreateEx(pg.dst, "", nil, false, false)
	pg.pm.SetStringEx(pg.pm.CreateEx(pgg.root, "name", nil, false, false),
		nil, name, StringUTF8)
	pgg.nodes = pg.pm.CreateEx(pgg.root, "nodes", nil, false, false)
	return pgg
}

// groupDestroy — C: group_destroy (prop_grouper.c:110)
func (pg *propGrouper) groupDestroy(pgg *pgGroup) {
	pg.pm.Destroy0(pgg.root)
	for i, g := range pg.groups {
		if g == pgg {
			pg.groups = slices.Delete(pg.groups, i, i+1)
			break
		}
	}
}

// nodeUnset — C: node_unset (prop_grouper.c:123)
func (pg *propGrouper) nodeUnset(pgn *pgNode) {
	if pgn.out != nil {
		pg.pm.Destroy0(pgn.out)
	}
	if pgn.group != nil {
		g := pgn.group
		for i, n := range g.entries {
			if n == pgn {
				g.entries = slices.Delete(g.entries, i, i+1)
				break
			}
		}
		if len(g.entries) == 0 {
			pg.groupDestroy(g)
		}
	}
}

// nodeUpdateGroup — C: node_update_group (prop_grouper.c:140)
func (pg *propGrouper) nodeUpdateGroup(pgn *pgNode, group string, hasGroup bool) {
	pg.nodeUnset(pgn)

	if !hasGroup {
		pgn.out = nil
		pgn.group = nil
		return
	}

	pgn.group = pg.groupFind(group)
	pgn.group.entries = slices.Insert(pgn.group.entries, 0, pgn)

	pgn.out = pg.pm.CreateRootEx("", true)
	pg.pm.Link(pgn.in, pgn.out, nil, false, false)
	pg.pm.SetParentEx(pgn.out, pgn.group.nodes, nil, "")
}

// nodeSetGroup — C: node_set_group (prop_grouper.c:164)
func (pg *propGrouper) nodeSetGroup(pgn *pgNode, event EventType, args ...any) {
	var group string
	hasGroup := true
	switch event {
	case EventSetRString, EventSetURI:
		// C: rstr_get(va_arg(ap, rstr_t *))
		if len(args) > 0 {
			group, _ = args[0].(string)
		}
	case EventSetCString:
		if len(args) > 0 {
			group, _ = args[0].(string)
		}
	case EventSetInt:
		if len(args) > 0 {
			group = fmt.Sprintf("%d", args[0])
		}
	case EventSetFloat:
		if len(args) > 0 {
			group = fmt.Sprintf("%f", args[0])
		}
	default:
		hasGroup = false
	}
	pg.nodeUpdateGroup(pgn, group, hasGroup)
}

// addNode — C: pg_add_node (prop_grouper.c:205)
func (pg *propGrouper) addNode(node *Prop) {
	pgn := &pgNode{grouper: pg, in: node}
	pg.nodes = slices.Insert(pg.nodes, 0, pgn)
	pg.pm.TagSet(node, pg, pgn)

	// C: prop_subscribe(PROP_SUB_INTERNAL|DONTLOCK, PROP_TAG_CALLBACK,
	//   node_set_group, pgn, PROP_TAG_NAMED_ROOT, node, "node",
	//   PROP_TAG_NAME_VECTOR, pg->pg_groupingpath, NULL)
	pgn.sub = newNamedPathSub(node, pg.groupingpath, nil,
		func(ev EventType, args ...any) {
			pg.nodeSetGroup(pgn, ev, args...)
		})
}

// delNode — C: pg_del_node (prop_grouper.c:237)
func (pg *propGrouper) delNode(pgn *pgNode) {
	for i, n := range pg.nodes {
		if n == pgn {
			pg.nodes = slices.Delete(pg.nodes, i, i+1)
			break
		}
	}
	pg.nodeUnset(pgn)
	pgn.sub.destroy() // C: prop_unsubscribe0
}

// clear — C: pg_clear (prop_grouper.c:250)
func (pg *propGrouper) clear() {
	for len(pg.nodes) > 0 {
		pgn := pg.nodes[0]
		pg.pm.TagClear(pgn.in, pg)
		pg.delNode(pgn)
	}
}

// srcCb — C: src_cb (prop_grouper.c:265)
func (pg *propGrouper) srcCb(opaque any, event EventType, args ...any) {
	switch event {
	case EventAddChild, EventAddChildBefore:
		pg.addNode(args[0].(*Prop))

	case EventAddChildVector, EventAddChildVectorBefore, EventAddChildVectorDirect:
		// Go wire format is []*Prop (vecProps handles *PropVec too)
		for _, p := range vecProps(args) {
			pg.addNode(p)
		}

	case EventDelChild:
		if pgn, ok := pg.pm.TagClear(args[0].(*Prop), pg).(*pgNode); ok {
			pg.delNode(pgn)
		}

	case EventSetVoid:
		pg.clear()

	case EventReqDeleteVector, EventWantMoreChilds, EventMoveChild,
		EventSetDir, EventReqDelete:
		// C: explicit no-ops

	default:
		panic(fmt.Sprintf("prop_grouper: Cant handle event %d", event)) // C: abort()
	}
}

// PropGrouperCreate — C: prop_grouper_create (prop_grouper.c:310)
func (pm *PropManager) PropGrouperCreate(dst, src *Prop, groupkey string, flags int) *propGrouper {
	pg := &propGrouper{pm: pm}

	if flags&PropGrouperTakeDstOwnership != 0 {
		pg.dst = dst
	} else {
		pg.dst = pm.XrefAddref(dst)
	}

	pg.groupingpath = strings.Split(groupkey, ".") // C: strvec_split

	// C: PROP_SUB_INTERNAL | PROP_SUB_DONTLOCK (prop_grouper.c:323)
	pg.srcsub = pm.Subscribe(src, pg.srcCb, pg, SubFlagInternal|SubFlagDontLock)
	return pg
}

// Destroy — C: prop_grouper_destroy (prop_grouper.c:336)
func (pg *propGrouper) Destroy() {
	pg.clear()
	pg.pm.Unsubscribe(pg.srcsub)
	pg.pm.Destroy0(pg.dst)
}
