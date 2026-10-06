// Package htsp is a 1:1 port of src/backend/htsp/htsp.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package htsp

import (
	"slices"

	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	navcore "github.com/czz/movian-go/internal/navigator"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: event_link_recstate (htsp.c:412-421)
func eventLinkRecstate(pm *propcore.PropManager, event *propcore.Prop,
	dvritem *propcore.Prop) {
	rs := pm.CreateMulti(event, "recstate")
	dvritemrs := pm.CreateMulti(dvritem, "recstate")

	pm.Link(dvritemrs, rs, nil, false, false)
	pm.RefDec(rs)
	pm.RefDec(dvritemrs)
}

// C: update_events (htsp.c:427-544)
func updateEvents(hc *htspConnection, metadata *propcore.Prop, id uint32, next uint32) {
	pm := hc.sys.pm
	list := pm.CreateEx(metadata, "list", nil, false, false)
	currentEvent := pm.CreateEx(metadata, "current", nil, false, false)
	nextEvent := pm.CreateEx(metadata, "next", nil, false, false)
	linkstate := 0

	if id == 0 {
		if next == 0 {
			// No events at all
			pm.DestroyChilds(list)
			return
		}

		id = next
		linkstate = 1
	}

	m := htsmsg.NewMap()
	m.AddStr("method", "getEvents")
	m.AddU32("eventId", id)
	m.AddU32("numFollowing", epgTail)

	var events *htsmsg.HTSMsg

	m = htspReqreply(hc, m)
	if m != nil {
		events = m.GetList("events")
	}

	pm.MarkChilds(list)

	if events != nil {
		cnt := 0
		for range events.GetFields() {
			cnt++
		}

		ordervec := make([]*propcore.Prop, cnt)

		cnt = 0
		for _, f := range events.GetFields() {
			if f.GetType() != htsmsg.HmfMap {
				continue
			}

			em := f.GetMap()
			var u32 uint32

			v, err := em.GetU32("eventId")
			if err != nil {
				continue
			}
			u32 = v

			eventId := snprintf(32, "%d", u32)
			e := pm.CreateEx(list, eventId, nil, false, false)
			pm.Unmark(e)
			ordervec[cnt] = e
			cnt++

			pm.SetVEx(nil, e, "type", "event")
			m2 := pm.CreateEx(e, "metadata", nil, false, false)
			title, ok := htsmsgGetStr(em, "title")
			propSetStr(pm, m2, "title", title, ok)
			desc, ok := htsmsgGetStr(em, "description")
			propSetStr(pm, m2, "description", desc, ok)

			sub, ok := htsmsgGetStr(em, "subtitle")
			propSetStr(pm, m2, "subtitle", sub, ok)

			if v, err := em.GetU32("start"); err == nil {
				pm.SetVEx(nil, m2, "start", int(v))
			}

			if v, err := em.GetU32("stop"); err == nil {
				pm.SetVEx(nil, m2, "stop", int(v))
			}

			if v, err := em.GetU32("dvrId"); err == nil {
				dvrpropname := snprintf(32, "%d", v)
				dvritem := pm.CreateMulti(hc.dvrNodes, dvrpropname)
				eventLinkRecstate(pm, e, dvritem)
				pm.RefDec(dvritem)
			}

			pm.SetVEx(nil, m2, "isCurrent", misc.BoolToInt(linkstate == 0))
			pm.SetVEx(nil, m2, "isNext", misc.BoolToInt(linkstate == 1))

			switch linkstate {
			case 0:
				pm.Link(e, currentEvent, nil, false, false)
			case 1:
				pm.Link(e, nextEvent, nil, false, false)
			}
			linkstate++
		}

		pm.DestroyMarkedChilds(list)

		if cnt > 0 {
			pm.Move(ordervec[cnt-1], nil)
			for i := cnt - 2; i >= 0; i-- {
				pm.Move(ordervec[i], ordervec[i+1])
			}
		}
	} else {
		pm.DestroyMarkedChilds(list)
	}

	switch linkstate {
	case 0:
		pm.Unlink(currentEvent)
		// FALLTHRU
		fallthrough
	case 1:
		pm.Unlink(nextEvent)
	}
}

// C: htsp_channel_get (htsp.c:550-586)
func htspChannelGet(hc *htspConnection, id int, create int) *htspChannel {
	pm := hc.sys.pm
	for _, ch := range hc.channels {
		if ch.id == id {
			return ch
		}
	}

	if create == 0 {
		return nil
	}

	ch := &htspChannel{}

	txt := snprintf(256, "%d", id)
	p := pm.CreateRoot(txt)
	ch.root = p

	txt = snprintf(256, "htsp://%s:%d/channel/%d",
		hc.hostname, hc.port, id)
	pm.SetVEx(nil, ch.root, "url", txt)

	txt = snprintf(256, "htsp://%s:%d/events/%d",
		hc.hostname, hc.port, id)
	pm.SetVEx(nil, ch.root, "eventUrl", txt)

	m := pm.CreateEx(p, "metadata", nil, false, false)
	ch.propIcon = pm.CreateEx(m, "icon", nil, false, false)
	ch.propTitle = pm.CreateEx(m, "title", nil, false, false)
	ch.propChannelNumber = pm.CreateEx(m, "channelNumber", nil, false, false)
	ch.propEvents = pm.CreateEx(m, "events", nil, false, false)

	pm.SetStringEx(pm.CreateEx(ch.root, "type", nil, false, false),
		nil, "tvchannel", propcore.StringUTF8)

	hc.channels = slices.Insert(hc.channels, 0, ch)
	ch.id = id
	return ch
}

// C: htsp_channelAddUpdate (htsp.c:592-644)
func htspChannelAddUpdate(hc *htspConnection, m *htsmsg.HTSMsg, create int) {
	pm := hc.sys.pm

	id, err := m.GetU32("channelId")
	if err != nil {
		return
	}

	title, titleOK := htsmsgGetStr(m, "channelName")
	icon, iconOK := htsmsgGetStr(m, "channelIcon")
	chnum := int(m.GetS32OrDefault("channelNumber", 0))

	hc.metaMutex.Lock()

	var ch *htspChannel
	if create != 0 {
		ch = htspChannelGet(hc, int(id), 1)
		if pm.SetParentEx(ch.root, hc.channelsNodes, nil, "") != 0 {
			panic("prop_set_parent failed") // C: abort()
		}
	} else {
		ch = htspChannelGet(hc, int(id), 0)
		if ch == nil {
			hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP",
				"Got update for unknown channel %d", id)
			hc.metaMutex.Unlock()
			return
		}
	}

	hc.metaMutex.Unlock()

	if iconOK {
		pm.SetStringEx(ch.propIcon, nil, icon, propcore.StringUTF8)
	}
	if titleOK {
		ch.title = title // C: mystrset(&ch->ch_title, title)
		pm.SetStringEx(ch.propTitle, nil, title, propcore.StringUTF8)
	}

	if chnum > 0 {
		pm.SetIntEx(ch.propChannelNumber, nil, chnum)
	}

	id2, err := m.GetU32("eventId")
	if err != nil {
		id2 = 0
	}
	next, err := m.GetU32("nextEventId")
	if err != nil {
		next = 0
	}
	updateEvents(hc, ch.propEvents, id2, next)
}

// C: channel_destroy (htsp.c:650-657)
func channelDestroy(hc *htspConnection, ch *htspChannel) {
	hc.sys.pm.Destroy(ch.root)
	for i, x := range hc.channels {
		if x == ch {
			hc.channels = slices.Delete(hc.channels, i, i+1)
			break
		}
	}
}

// C: htsp_channelDelete (htsp.c:663-678)
func htspChannelDelete(hc *htspConnection, m *htsmsg.HTSMsg) {
	id, err := m.GetU32("channelId")
	if err != nil {
		return
	}

	hc.metaMutex.Lock()
	defer hc.metaMutex.Unlock()

	if ch := htspChannelGet(hc, int(id), 0); ch != nil {
		channelDestroy(hc, ch)
	}

}

// C: channel_delete_all (htsp.c:684-690)
func channelDeleteAll(hc *htspConnection) {
	for len(hc.channels) > 0 {
		channelDestroy(hc, hc.channels[0])
	}
}

// C: tag_compar (htsp.c:696-700)
func tagCompar(a, b *htspTag, ignoreThe int) int {
	return misc.Dictcmp(a.title, b.title, ignoreThe)
}

// C: htsp_tagAddUpdate (htsp.c:706-828)
func htspTagAddUpdate(hc *htspConnection, m *htsmsg.HTSMsg, create int) {
	pm := hc.sys.pm
	ignoreThe := pm.IgnoreThePrefix()

	id, idOK := htsmsgGetStr(m, "tagId")
	if !idOK {
		return
	}

	title, titleOK := htsmsgGetStr(m, "tagName")

	hc.metaMutex.Lock()

	var ht *htspTag
	var n *htspTag
	if create != 0 {
		ht = &htspTag{
			id:    id,
			title: title, // C: strdup(title ?: "")
		}

		// C: LIST_INSERT_SORTED(&hc->hc_tags, ht, ht_link, tag_compar, htsp_tag_t)
		idx := len(hc.tags)
		for i, o := range hc.tags {
			if tagCompar(ht, o, ignoreThe) <= 0 {
				idx = i
				break
			}
		}
		hc.tags = append(hc.tags, nil)
		copy(hc.tags[idx+1:], hc.tags[idx:])
		hc.tags[idx] = ht
		if idx+1 < len(hc.tags) {
			n = hc.tags[idx+1]
		}

		ht.root = pm.CreateRoot(id)

		txt := snprintf(200, "htsp://%s:%d/tag/%s",
			hc.hostname, hc.port, id)

		pm.SetVEx(nil, ht.root, "url", txt)
		pm.SetVEx(nil, ht.root, "type", "directory")

		ht.channels = pm.CreateEx(ht.root, "channels", nil, false, false)
		ht.nodes = pm.CreateEx(ht.root, "nodes", nil, false, false)

		nf := propcore.PropNFCreate(ht.nodes, ht.channels, nil,
			propcore.PropNFAutoDestroy)

		propcore.PropNFSort(nf, "node.metadata.channelNumber", false, 0,
			nil, false)
		nf.Release()

		var before *propcore.Prop
		if n != nil {
			before = n.root
		}
		if pm.SetParentEx(ht.root, hc.tagsNodes,
			&propcore.SetParentOpaque{Before: before}, "") != 0 {
			panic("prop_set_parent_ex failed") // C: abort()
		}
	} else {
		for _, t := range hc.tags {
			if t.id == id {
				ht = t
				break
			}
		}

		if ht == nil {
			hc.sys.ts.Trace(trace.TRACE_ERROR, "HTSP",
				"Got update for unknown tag %s", id)
			hc.metaMutex.Unlock()
			return
		}

		if titleOK {
			ht.title = title // C: mystrset(&ht->ht_title, title)

			// C: LIST_REMOVE + LIST_INSERT_SORTED
			for i, x := range hc.tags {
				if x == ht {
					hc.tags = slices.Delete(hc.tags, i, i+1)
					break
				}
			}
			idx := len(hc.tags)
			for i, o := range hc.tags {
				if tagCompar(ht, o, ignoreThe) <= 0 {
					idx = i
					break
				}
			}
			hc.tags = append(hc.tags, nil)
			copy(hc.tags[idx+1:], hc.tags[idx:])
			hc.tags[idx] = ht
			if idx+1 < len(hc.tags) {
				n = hc.tags[idx+1]
			}
			var nroot *propcore.Prop
			if n != nil {
				nroot = n.root
			}
			pm.Move(ht.root, nroot)
		}
	}

	metadata := pm.CreateEx(ht.root, "metadata", nil, false, false)

	propSetStr(pm, metadata, "title", title, titleOK)
	icon, iconOK := htsmsgGetStr(m, "tagIcon")
	propSetStr(pm, metadata, "icon", icon, iconOK)
	pm.SetVEx(nil, metadata, "titledIcon",
		int(m.GetU32OrDefault("tagTitledIcon", 0)))

	members := m.GetList("members")
	if members == nil {
		hc.metaMutex.Unlock()
		return
	}

	pm.MarkChilds(ht.channels)

	for _, f := range members.GetFields() {
		if f.GetType() != htsmsg.HmfS64 {
			continue
		}

		s64 := f.GetS64Value()
		txt := snprintf(200, "%d", s64)
		ch := pm.CreateEx(ht.channels, txt, nil, false, false)

		pm.Unmark(ch)

		url := snprintf(512, "htsp://%s:%d/channel/%d",
			hc.hostname, hc.port, s64)
		pm.SetVEx(nil, ch, "url", url)

		url = snprintf(512, "htsp://%s:%d/events/%d",
			hc.hostname, hc.port, s64)
		pm.SetVEx(nil, ch, "eventUrl", url)

		pm.SetVEx(nil, ch, "type", "tvchannel")

		orig := pm.CreateEx(hc.channelsNodes, txt, nil, false, false)

		pm.Link(pm.CreateEx(orig, "metadata", nil, false, false),
			pm.CreateEx(ch, "metadata", nil, false, false),
			nil, false, false)
	}

	pm.DestroyMarkedChilds(ht.channels)

	hc.metaMutex.Unlock()
}

// C: tag_destroy (htsp.c:834-842)
func tagDestroy(hc *htspConnection, ht *htspTag) {
	for i, x := range hc.tags {
		if x == ht {
			hc.tags = slices.Delete(hc.tags, i, i+1)
			break
		}
	}
	hc.sys.pm.Destroy(ht.root)
}

// C: htsp_tagDelete (htsp.c:847-866)
func htspTagDelete(hc *htspConnection, m *htsmsg.HTSMsg) {
	id, idOK := htsmsgGetStr(m, "tagId")
	if !idOK {
		return
	}

	hc.metaMutex.Lock()
	defer hc.metaMutex.Unlock()

	var ht *htspTag
	for _, t := range hc.tags {
		if t.id == id {
			ht = t
			break
		}
	}

	if ht != nil {
		tagDestroy(hc, ht)
	}

}

// C: tag_delete_all (htsp.c:872-879)
func tagDeleteAll(hc *htspConnection) {
	for len(hc.tags) > 0 {
		tagDestroy(hc, hc.tags[0])
	}
}

// C: dvr_entry_create (htsp.c:885-950)
func dvrEntryCreate(hc *htspConnection, m *htsmsg.HTSMsg, linkToEvent int) {
	pm := hc.sys.pm

	id, err := m.GetS64("id")
	if err != nil {
		return
	}

	idstr := snprintf(64, "%d", id)

	item := pm.CreateMulti(hc.dvrNodes, idstr)

	txt := snprintf(1024, "htsp://%s:%d/dvr/%s",
		hc.hostname, hc.port, idstr)
	pm.SetVEx(nil, item, "url", txt)

	pm.SetVEx(nil, item, "type", "event")

	rs := pm.CreateMulti(item, "recstate")
	state, ok := htsmsgGetStr(m, "state")
	propSetStr(pm, rs, "state", state, ok)

	if s64, err := m.GetS64("dataSize"); err == nil {
		pm.SetVEx(nil, rs, "dataSize", float32(s64))
	} else {
		pm.SetVEx(nil, rs, "dataSize", nil)
	}

	meta := pm.CreateMulti(item, "metadata")

	title, ok := htsmsgGetStr(m, "title")
	propSetStr(pm, meta, "title", title, ok)

	subtitle, ok := htsmsgGetStr(m, "subtitle")
	propSetStr(pm, meta, "subtitle", subtitle, ok)

	if u32, err := m.GetU32("start"); err == nil {
		pm.SetVEx(nil, meta, "start", int(u32))
	}

	if u32, err := m.GetU32("stop"); err == nil {
		pm.SetVEx(nil, meta, "stop", int(u32))
	}

	if linkToEvent != 0 {
		if eventID, err := m.GetS64("eventId"); err == nil {
			if channelID, err := m.GetU32("channel"); err == nil {
				idstr2 := snprintf(64, "%d", eventID)

				hc.metaMutex.Lock()
				var ch *htspChannel
				for _, c := range hc.channels {
					if c.id == int(channelID) {
						ch = c
						break
					}
				}
				if ch != nil {
					// C: prop_find(ch->ch_prop_events, "list", idstr, NULL)
					ev := ch.propEvents.ResolvePath(
						[]string{"list", idstr2}, true)
					if ev != nil {
						eventLinkRecstate(pm, ev, item)
					}
				}
				hc.metaMutex.Unlock()
			}
		}
	}

	pm.RefDec(meta)
	pm.RefDec(item)
	pm.RefDec(rs)
}

// C: htsp_dvrEntryUpdate (htsp.c:956-960)
func htspDvrEntryUpdate(hc *htspConnection, m *htsmsg.HTSMsg) {
	dvrEntryCreate(hc, m, 0)
}

// C: htsp_dvrEntryAdd (htsp.c:964-968)
func htspDvrEntryAdd(hc *htspConnection, m *htsmsg.HTSMsg) {
	dvrEntryCreate(hc, m, 1)
}

// C: htsp_dvrEntryDelete (htsp.c:975-984)
func htspDvrEntryDelete(hc *htspConnection, m *htsmsg.HTSMsg) {
	id, err := m.GetS64("id")
	if err != nil {
		return
	}
	idstr := snprintf(64, "%d", id)

	hc.sys.pm.DestroyByName(hc.dvrNodes, idstr)
}

// C: make_root_model (htsp.c:1220-1259)
func makeRootModel(hc *htspConnection) {
	pm := hc.sys.pm
	hc.rootModel = pm.CreateRoot("")
	meta := pm.CreateMulti(hc.rootModel, "metadata")

	pm.SetVEx(nil, hc.rootModel, "type", "directory")
	pm.Link(hc.serverName, pm.CreateEx(meta, "title", nil, false, false),
		nil, false, false)

	nodes := pm.CreateMulti(hc.rootModel, "nodes")
	pc := propcore.PropConcatCreate(pm, nodes)
	pc.AddSource(hc.tagsNodes, nil)

	extranodes := pm.CreateMulti(hc.rootModel, "extranodes")

	recording := pm.CreateMulti(extranodes, "")
	pm.SetVEx(nil, recording, "type", "directory")
	txt := snprintf(256, "htsp://%s:%d/recordings",
		hc.hostname, hc.port)
	pm.SetVEx(nil, recording, "url", txt)

	tmp := pm.CreateMultiPath(recording, "metadata", "title")
	pm.Link(nls.GetProp("Recorded shows"), tmp, nil, false, false)
	pm.RefDec(tmp)
	pm.RefDec(recording)

	d := pm.CreateRoot("")
	tmp = pm.CreateMultiPath(d, "metadata", "title")
	pm.Link(nls.GetProp("Recorder"), tmp, nil, false, false)
	pm.SetVEx(nil, d, "type", "separator")
	pm.RefDec(tmp)
	pc.AddSource(extranodes, d)

	pc.Release()

	pm.RefDec(nodes)
	pm.RefDec(meta)
}

// C: make_model (htsp.c:1396-1417)
func (sys *System) makeModel(parent *propcore.Prop, title *propcore.Prop,
	nodes *propcore.Prop, contents string) {
	pm := sys.pm
	model := pm.CreateEx(parent, "model", nil, false, false)

	pm.SetVEx(nil, model, "type", "directory")

	pnf := propcore.PropNFCreate(
		pm.CreateEx(model, "nodes", nil, false, false),
		nodes,
		pm.CreateEx(model, "filter", nil, false, false),
		propcore.PropNFAutoDestroy)
	pm.SetVEx(nil, model, "canFilter", 1)
	pm.SetVEx(nil, model, "contents", contents)

	pnf.Release()

	meta := pm.CreateEx(model, "metadata", nil, false, false)
	pm.Link(title, pm.CreateEx(meta, "title", nil, false, false),
		nil, false, false)
}

// C: make_model2 (htsp.c:1423-1444)
func (sys *System) makeModel2(parent *propcore.Prop, sourcemodel *propcore.Prop,
	contents string) {
	pm := sys.pm
	model := pm.CreateEx(parent, "model", nil, false, false)

	pm.SetVEx(nil, model, "type", "directory")

	pnf := propcore.PropNFCreate(
		pm.CreateEx(model, "nodes", nil, false, false),
		pm.CreateEx(sourcemodel, "nodes", nil, false, false),
		pm.CreateEx(model, "filter", nil, false, false),
		propcore.PropNFAutoDestroy)
	pm.SetVEx(nil, model, "canFilter", 1)
	pm.SetVEx(nil, model, "contents", contents)

	pnf.Release()

	meta := pm.CreateEx(model, "metadata", nil, false, false)
	pm.Link(pm.CreateEx(
		pm.CreateEx(sourcemodel, "metadata", nil, false, false),
		"title", nil, false, false),
		pm.CreateEx(meta, "title", nil, false, false),
		nil, false, false)
}

// C: make_event_model (htsp.c:1452-1494)
func (sys *System) makeEventModel(page *propcore.Prop, hc *htspConnection, chidstr string) int {
	pm := hc.sys.pm

	hc.metaMutex.Lock()

	ch := htspChannelGet(hc, misc.Atoi(chidstr), 0)
	if ch == nil {
		hc.metaMutex.Unlock()
		navcore.OpenErrorf(pm, page, "No such channel")
		return 1
	}

	model := pm.CreateMulti(page, "model")
	metadata := pm.CreateMulti(model, "metadata")
	sourcelist := pm.CreateMulti(ch.propEvents, "list")
	sourcemeta := pm.CreateMulti(ch.root, "metadata")

	pm.SetVEx(nil, model, "type", "directory")

	pnf := propcore.PropNFCreate(
		pm.CreateEx(model, "nodes", nil, false, false),
		sourcelist,
		pm.CreateEx(model, "filter", nil, false, false),
		propcore.PropNFAutoDestroy)

	pm.SetVEx(nil, model, "canFilter", 1)
	pm.SetVEx(nil, model, "contents", "events")

	pnf.Release()

	pm.Link(pm.CreateEx(sourcemeta, "title", nil, false, false),
		pm.CreateEx(metadata, "title", nil, false, false),
		nil, false, false)

	pm.Link(pm.CreateEx(sourcemeta, "icon", nil, false, false),
		pm.CreateEx(metadata, "icon", nil, false, false),
		nil, false, false)

	hc.metaMutex.Unlock()

	pm.RefDec(sourcemeta)
	pm.RefDec(sourcelist)
	pm.RefDec(model)
	pm.RefDec(metadata)
	return 0
}
