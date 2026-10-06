package upnp

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/api/soap"
	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/metadata"
	navigator "github.com/czz/movian-go/internal/navigator"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// Wired by cmd/movian-go init; nil-safe methods make unwired calls no-ops.

// upnpBrowse is a UPNP browse request.
// C: upnp_browse_t (upnp_browse.c:43-69)
type upnpBrowse struct {
	sys      *System // back-pointer — C reaches globals directly
	run      bool    // C: ub_run
	loadMore bool    // C: ub_load_more

	id         string // C: ub_id
	url        string // C: ub_url
	baseURL    string // C: ub_base_url
	controlURL string // C: ub_control_url
	eventURL   string // C: ub_event_url

	page        *propcore.Prop // C: ub_page
	model       *propcore.Prop // C: ub_model
	nodes       *propcore.Prop // C: ub_nodes
	items       *propcore.Prop // C: ub_items
	loading     *propcore.Prop // C: ub_loading
	typ         *propcore.Prop // C: ub_type
	source      *propcore.Prop // C: ub_source
	directClose *propcore.Prop // C: ub_direct_close
	error       *propcore.Prop // C: ub_error
	title       *propcore.Prop // C: ub_title
	contents    *propcore.Prop // C: ub_contents
	filter      *propcore.Prop // C: ub_filter
	canFilter   *propcore.Prop // C: ub_canFilter

	itemsub *propcore.Subscription // C: ub_itemsub

	loadedEntries int // C: ub_loaded_entries
	totalEntries  int // C: ub_total_entries

	sortsub      *propcore.Subscription // C: ub_sortsub
	sortcriteria string                 // C: ub_sortcriteria
}

// clsToType maps a DIDL class to a Movian type.
// C: cls_to_type (upnp_browse.c:76-82)
func clsToType(cls string) string {
	if cls == "object.container.album.musicAlbum" {
		return "album"
	}
	return "directory"
}

// itemSetStr copies item[id].cdata into prop c[propname].
// C: item_set_str (upnp_browse.c:88-93)
func (s *System) itemSetStr(c *propcore.Prop, item *htsmsg.HTSMsg, propname, id string) string {
	str := item.GetStrMulti(id, "cdata")
	s.propManager.CreateEx(c, propname, nil, false, false).SetString(str)
	return str
}

// itemSetDuration extracts res@duration → meta.duration (seconds).
// C: item_set_duration (upnp_browse.c:99-115)
func (s *System) itemSetDuration(meta *propcore.Prop, item *htsmsg.HTSMsg) {
	for _, f := range item.GetFields() {
		res := f.GetMapByFieldIfName("res")
		if res == nil {
			continue
		}

		str := res.GetStr("duration")
		var h, m, sec int
		if str != "" {
			if n, _ := fmt.Sscanf(str, "%d:%d:%d", &h, &m, &sec); n == 3 {
				s.propManager.CreateEx(meta, "duration", nil, false, false).
					SetFloat(float32(h*3600 + m*60 + sec))
				break
			}
		}
	}
}

// makeAudioItem — C: make_audioItem (upnp_browse.c:121-148)
func (s *System) makeAudioItem(c, m *propcore.Prop, item *htsmsg.HTSMsg) {
	s.propManager.CreateEx(c, "type", nil, false, false).SetString("audio")

	s.itemSetStr(m, item, "title", "title")

	str := s.itemSetStr(m, item, "artist", "artist")
	artist := str

	str = s.itemSetStr(m, item, "album", "album")
	album := str

	if s.itemSetStr(m, item, "album_art", "albumArtURI") == "" {
		if artist != "" && album != "" && s.metadataMgr != nil {
			s.metadataMgr.MetadataBindAlbumart(
				s.propManager.CreateEx(m, "album_art", nil, false, false),
				artist, album)
		}
	}

	if artist != "" && s.metadataMgr != nil {
		s.metadataMgr.MetadataBindArtistpics(
			s.propManager.CreateEx(m, "artist_images", nil, false, false),
			artist)
	}
}

// makeVideoItem — C: make_videoItem (upnp_browse.c:154-166)
func (s *System) makeVideoItem(c, m *propcore.Prop, item *htsmsg.HTSMsg, url string) {
	s.propManager.CreateEx(c, "type", nil, false, false).SetString("video")

	title := item.GetStr("title")

	s.itemSetStr(m, item, "title", "title")
	s.itemSetStr(m, item, "icon", "albumArtURI")

	s.propManager.CreateEx(c, "url", nil, false, false).SetString(url)
	s.propManager.CreateEx(c, "filename", nil, false, false).SetString(title)
}

// makeImageItem — C: make_imageItem (upnp_browse.c:172-178)
func (s *System) makeImageItem(c, m *propcore.Prop, item *htsmsg.HTSMsg) {
	s.propManager.CreateEx(c, "type", nil, false, false).SetString("image")
	s.itemSetStr(m, item, "icon", "albumArtURI")
	s.itemSetStr(m, item, "title", "title")
}

// addItem converts a DIDL <item> into a prop node under root.
// C: add_item (upnp_browse.c:184-232)
func (s *System) addItem(item *htsmsg.HTSMsg, root *propcore.Prop, trackid string,
	trackptr **propcore.Prop, skip *propcore.Subscription, baseurl string) {

	id := item.GetStr("id")
	if id == "" {
		return
	}

	cls := item.GetStr("class")
	if cls == "" {
		return
	}

	url := item.GetStr("res")
	if url == "" {
		return
	}

	c := s.propManager.CreateRootEx("", true)

	m := s.propManager.CreateEx(c, "metadata", nil, false, false)
	s.itemSetDuration(m, item)

	if strings.HasPrefix(cls, "object.item.audioItem") {
		s.propManager.CreateEx(c, "url", nil, false, false).SetString(url)
		s.makeAudioItem(c, m, item)
		s.metadataMgr.PlayInfoBindURLToProp(url, c)
	} else if strings.HasPrefix(cls, "object.item.videoItem") {
		vurl := fmt.Sprintf("%s:%s", baseurl, id)
		s.makeVideoItem(c, m, item, vurl)
		s.metadataMgr.PlayInfoBindURLToProp(url, c)
	} else if strings.HasPrefix(cls, "object.item.imageItem") {
		s.propManager.CreateEx(c, "url", nil, false, false).SetString(url)
		s.makeImageItem(c, m, item)
	} else {
		s.upnpTrace("Cant handle upnp:class %s (%s)", cls, url)
		s.propManager.Destroy(c)
		return
	}

	if s.propManager.SetParentEx(c, root, skip, "") != 0 {
		s.propManager.Destroy(c)
	} else if trackid != "" && trackid == id && *trackptr == nil {
		*trackptr = c
	}
}

// addContainer converts a DIDL <container> into a prop node.
// C: add_container (upnp_browse.c:238-266)
func (s *System) addContainer(item *htsmsg.HTSMsg, root *propcore.Prop,
	baseurl string, skip *propcore.Subscription) {

	id := item.GetStr("id")
	if id == "" {
		return
	}

	url := fmt.Sprintf("%s:%s", baseurl, id)

	c := s.propManager.CreateRootEx("", true)
	s.propManager.CreateEx(c, "url", nil, false, false).SetString(url)

	m := s.propManager.CreateEx(c, "metadata", nil, false, false)

	s.itemSetStr(m, item, "title", "title")

	cls := item.GetStr("class")

	typ := "directory"
	if cls != "" {
		typ = clsToType(cls)
	}
	s.propManager.CreateEx(c, "type", nil, false, false).SetString(typ)

	if s.propManager.SetParentEx(c, root, skip, "") != 0 {
		s.propManager.Destroy(c)
	}
}

// nodesFromMeta walks DIDL-Lite item/container fields into root.
// C: nodes_from_meta (upnp_browse.c:272-291)
func (s *System) nodesFromMeta(meta *htsmsg.HTSMsg, root *propcore.Prop, trackid string,
	trackptr **propcore.Prop, baseurl string, skip *propcore.Subscription) {

	items := meta.GetMap("DIDL-Lite")
	if items == nil {
		return
	}

	for _, f := range items.GetFields() {
		if f.GetName() == "item" {
			if item := f.GetChilds(); item != nil {
				s.addItem(item, root, trackid, trackptr, skip, baseurl)
			}
		} else if baseurl != "" && f.GetName() == "container" {
			if container := f.GetChilds(); container != nil {
				s.addContainer(container, root, baseurl, skip)
			}
		}
	}
}

// browseChildren browses ObjectID's direct children into nodes.
// C: upnp_browse_children (upnp_browse.c:297-345)
func (s *System) browseChildren(uri, id string, nodes *propcore.Prop,
	trackid string, trackptr **propcore.Prop) int {

	if trackptr != nil {
		*trackptr = nil
	}

	in := htsmsg.NewMap()

	in.AddStr("ObjectID", id)
	in.AddStr("BrowseFlag", "BrowseDirectChildren")
	in.AddStr("Filter", "*")
	in.AddU32("StartingIndex", 0)
	in.AddU32("RequestedCount", 0)
	in.AddStr("SortCriteria", "")
	out, rerr := soap.SoapExec(uri, "ContentDirectory", 1, "Browse", in)
	in.Release()
	if rerr != nil {
		s.ts.Trace(trace.TRACE_ERROR, "UPNP",
			"Browse %s via %s -- %v", id, uri, rerr)
		return -1
	}

	if out == nil {
		s.ts.Trace(trace.TRACE_ERROR, "UPNP",
			"Browse %s via %s -- No returned varibles", uri, id)
		return -1
	}

	result := out.GetStr("Result")
	if result == "" {
		s.ts.Trace(trace.TRACE_ERROR, "UPNP",
			"Browse %s via %s -- No returned result", uri, id)
		out.Release()
		return -1
	}

	meta, err := htsmsg.DeserializeXML(result)
	if meta == nil {
		s.ts.Trace(trace.TRACE_ERROR, "UPNP",
			"Browse %s via %s -- XML error %v", uri, id, err)
		out.Release()
		return -1
	}

	s.nodesFromMeta(meta, nodes, trackid, trackptr, "", nil)
	meta.Release()
	out.Release()
	return 0
}

// browseFail — C: browse_fail (upnp_browse.c:351-364)
func (ub *upnpBrowse) browseFail(format string, args ...any) {
	buf := fmt.Sprintf(format, args...)

	ub.error.SetString(buf)
	ub.typ.SetString("openerror")
	ub.loading.SetInt(0)
}

// browseItems browses the next batch of children.
// C: browse_items (upnp_browse.c:371-438)
func (ub *upnpBrowse) browseItems() {
	s := ub.sys
	in := htsmsg.NewMap()

	in.AddStr("ObjectID", ub.id)
	in.AddStr("BrowseFlag", "BrowseDirectChildren")
	in.AddStr("Filter", "*")
	in.AddU32("StartingIndex", uint32(ub.loadedEntries))
	in.AddU32("RequestedCount", 500)
	in.AddStr("SortCriteria", ub.sortcriteria)

	out, rerr := soap.SoapExec(ub.controlURL, "ContentDirectory", 1, "Browse",
		in)
	in.Release()

	if rerr != nil {
		ub.browseFail("%v", rerr)
		return
	}

	if out == nil {
		ub.browseFail("Malformed SOAP response, no returned variabled")
		return
	}

	str := out.GetStr("TotalMatches")
	if str != "" {
		ub.totalEntries, _ = strconv.Atoi(str)
	} else {
		ub.run = false
	}

	str = out.GetStr("NumberReturned")
	if str != "" {
		v, _ := strconv.Atoi(str)
		ub.loadedEntries += v
	} else {
		ub.run = false
	}

	result := out.GetStr("Result")
	if result == "" {
		ub.browseFail("No SOAP result")
		out.Release()
		return
	}

	meta, err := htsmsg.DeserializeXML(result)
	if meta == nil {
		ub.browseFail("Malformed XML: %v", err)
		out.Release()
		return
	}

	s.nodesFromMeta(meta, ub.items, "", nil, ub.baseURL, ub.itemsub)

	ub.sys.upnpTrace("Browsed %d of %d items",
		ub.loadedEntries, ub.totalEntries)

	meta.Release()
	s.propManager.HaveMoreChilds(ub.items,
		ub.loadedEntries < ub.totalEntries)
	out.Release()
}

// destroy — C: ub_destroy (upnp_browse.c:444-470)
func (ub *upnpBrowse) destroy() {
	// C frees strings + prop_ref_dec on each held prop.
	// Go: props are GC'd once the browse is done; subs are unsubscribed
	// by the caller (browse_directory end).
}

// nodeEventSub — C: node_eventsub (upnp_browse.c:476-493)
func nodeEventSub(opaque any, event propcore.EventType, args ...any) {
	ub := opaque.(*upnpBrowse)

	switch event {
	case propcore.EventDestroyed:
		ub.run = false
	case propcore.EventWantMoreChilds:
		ub.loadMore = true
	}
}

// resolve resolves upnp:<uuid>:<svcid>:<id> into device/service.
// C: upnp_browse_resolve (upnp_browse.c:499-570)
func (ub *upnpBrowse) resolve() int {
	s := ub.sys
	url := ub.url

	if !strings.HasPrefix(url, "upnp:") {
		// C: url += strlen("upnp:") — unconditional skip
		return 1
	}
	base := url
	url = url[len("upnp:"):]

	s.mu.Lock()

	var ud *UPNPDevice
	for ud == nil {
		for _, d := range s.devices {
			if d.uuid != "" && strings.HasPrefix(url, d.uuid) {
				ud = d
				break
			}
		}

		if ud != nil {
			break
		}

		// Do SSDP M-SEARCH ?
		if condWaitTimeout(s.deviceCond, &s.mu, 5000) {
			break
		}
	}

	if ud == nil {
		s.mu.Unlock()
		navigator.OpenErrorf(s.propManager, ub.page, "Device not found")
		return 1
	}

	url = url[len(ud.uuid):]
	if len(url) == 0 || url[0] != ':' {
		s.mu.Unlock()
		navigator.OpenErrorf(s.propManager, ub.page, "Malformed URI after device")
		return 1
	}

	url = url[1:]

	var us *UPNPService
	for _, s := range ud.services {
		if s.id != "" && strings.HasPrefix(url, s.id) {
			us = s
			break
		}
	}

	if us == nil {
		s.mu.Unlock()
		navigator.OpenErrorf(s.propManager, ub.page, "Service not found")
		return 1
	}

	url = url[len(us.id):]
	if len(url) == 0 || url[0] != ':' {
		s.mu.Unlock()
		navigator.OpenErrorf(s.propManager, ub.page, "Malformed URI after service")
		return 1
	}

	url = url[1:]

	ub.baseURL = base
	ub.controlURL = us.controlURL
	ub.eventURL = us.eventURL
	ub.id = url

	s.mu.Unlock()
	return 0
}

// setSortOrder — C: set_sort_order (upnp_browse.c:576-607)
func setSortOrder(opaque any, event propcore.EventType, args ...any) {
	ub := opaque.(*upnpBrowse)
	s := ub.sys

	if event != propcore.EventSelectChild {
		return
	}

	var p *propcore.Prop
	for _, a := range args {
		if pp, ok := a.(*propcore.Prop); ok {
			p = pp
			break
		}
	}
	if p == nil {
		return
	}
	val := p.GetName()
	if val != "" {
		switch val {
		case "title":
			ub.sortcriteria = ""
		case "date":
			ub.sortcriteria = "-dc:date"
		case "dateold":
			ub.sortcriteria = "+dc:date"
		}
	}
	// C: kv_url_opt_set(ub->ub_url, KVSTORE_DOMAIN_SYS, "sortorder",
	//                   KVSTORE_SET_STRING, val)
	ub.sys.kvstore.UrlOptSet(ub.url, kvstore.DomainSys, "sortorder",
		kvstore.SetString, val)

	ub.loadedEntries = 0
	ub.loadMore = true
	s.propManager.DestroyChilds(ub.items)
}

// pLink creates a standalone prop holding s (C: _p()) and links it into dst.
// C: prop_link(_p(str), prop_create(dst...))
func (s *System) pLink(str string, dst *propcore.Prop) {
	t := s.propManager.CreateRootEx("", true)
	s.propManager.SetStringEx(t, nil, str, propcore.StringUTF8)
	s.propManager.Link(t, dst, nil, false, true)
}

// addSortOptionType — C: add_sort_option_type (upnp_browse.c:613-661)
func (ub *upnpBrowse) addSortOptionType(model *propcore.Prop,
	pc *propcore.Courier) {
	s := ub.sys

	parent := s.propManager.CreateEx(model, "options", nil, false, false)
	n := s.propManager.CreateRootEx("", true)
	m := s.propManager.CreateEx(n, "metadata", nil, false, false)
	options := s.propManager.CreateEx(n, "options", nil, false, false)

	s.propManager.CreateEx(n, "type", nil, false, false).SetString("multiopt")
	s.propManager.CreateEx(n, "enabled", nil, false, false).SetInt(1)
	s.pLink("Sort on", s.propManager.CreateEx(m, "title", nil, false, false))

	onTitle := s.propManager.CreateRootEx("title", true)
	s.pLink("Filename", s.propManager.CreateEx(onTitle, "title", nil, false, false))
	if s.propManager.SetParentEx(onTitle, options, nil, "") != 0 {
		s.propManager.Destroy(onTitle)
	}

	onDate := s.propManager.CreateRootEx("date", true)
	s.pLink("Date (newest first)", s.propManager.CreateEx(onDate, "title", nil, false, false))
	if s.propManager.SetParentEx(onDate, options, nil, "") != 0 {
		s.propManager.Destroy(onDate)
	}

	onDateold := s.propManager.CreateRootEx("dateold", true)
	s.pLink("Date (oldest first)", s.propManager.CreateEx(onDateold, "title", nil, false, false))
	if s.propManager.SetParentEx(onDateold, options, nil, "") != 0 {
		s.propManager.Destroy(onDateold)
	}

	// C: kv_url_opt_get_rstr(ub->ub_url, KVSTORE_DOMAIN_SYS, "sortorder")
	cur := ub.sys.kvstore.UrlOptGetString(ub.url, kvstore.DomainSys, "sortorder")

	if cur == "date" {
		propcore.ProxySelect(onDate)
		ub.sortcriteria = "-dc:date"
	} else if cur == "dateold" {
		propcore.ProxySelect(onDate)
		ub.sortcriteria = "+dc:date"
	} else {
		propcore.ProxySelect(onTitle)
		ub.sortcriteria = ""
	}

	ub.sortsub = s.propManager.SubscribeWithCourier(options, pc,
		setSortOrder, ub, propcore.SubNoInitialUpdate)

	if s.propManager.SetParentEx(n, parent, nil, "") != 0 {
		s.propManager.Destroy(n)
	}
}

// browseDirectory — C: browse_directory (upnp_browse.c:667-725)
func (ub *upnpBrowse) browseDirectory(title string) {
	s := ub.sys
	ub.typ.SetString("directory")

	// C: pnf = prop_nf_create(ub->ub_nodes, ub->ub_items, ub->ub_filter,
	//                         PROP_NF_AUTODESTROY)
	pnf := propcore.PropNFCreate(ub.nodes, ub.items, ub.filter, propcore.PropNFAutoDestroy)
	ub.canFilter.SetInt(1)

	// C: decorated_browse_create(ub->ub_model, pnf, ub->ub_items, title,
	//    DECO_FLAGS_NO_AUTO_SORTING, ub->ub_url, "UPnP")
	//    then prop_nf_release(pnf) — db owns the filter now.
	metadata.DecoratedBrowseCreate(ub.model, pnf, ub.items, title,
		metadata.DecoFlagsNoAutoSorting, ub.url, "UPnP", ub.sys.kvstore, ub.sys.metadataMgr)

	pc := propcore.NewCourier("upnp")
	ub.addSortOptionType(ub.model, pc)

	ub.run = true
	ub.itemsub = s.propManager.SubscribeWithCourier(ub.items, pc,
		nodeEventSub, ub, propcore.SubFlagTrackDestroy)

	// initial browse
	ub.browseItems()

	ub.loading.SetInt(0)
	for ub.run {
		pc.Wait()

		if ub.loadMore {
			ub.loadMore = false
			ub.browseItems()
		}
	}

	if ub.itemsub != nil {
		ub.itemsub.Unsubscribe()
	}
	if ub.sortsub != nil {
		ub.sortsub.Unsubscribe()
	}

	pc.Destroy()
}

// minidlnaGetSrt probes MiniDLNA caption info.
// C: minidlna_get_srt (upnp_browse.c:731-756)
func minidlnaGetSrt(url string, sublist *htsmsg.HTSMsg) {
	client := httpnet.NewHTTPClient()
	resp, err := client.Do(&httpnet.HTTPRequest{
		URL:    url,
		Method: httpnet.HTTPCmdGet,
		Headers: httpnet.HTTPHeaders{
			{Key: "getCaptionInfo.sec", Value: "1"},
		},
	})
	if err == nil && resp != nil {
		if s := resp.Headers.HTTPHeaderGet("CaptionInfo.sec"); s != "" {
			sub := htsmsg.NewMap()
			sub.AddStr("url", s)
			sub.AddStr("source", "MiniDLNA")
			sub.AddStr("title", "SRT file")
			sub.AddStr("format", "SRT")
			sublist.AddMsg("", sub)
		}
	}
}

// blindSrtCheck probes <file>.srt for subtitles.
// C: blind_srt_check (upnp_browse.c:762-792)
func blindSrtCheck(url string, sublist *htsmsg.HTSMsg) {
	dot := strings.LastIndex(url, ".")
	if dot < 0 {
		return
	}
	srt := url[:dot] + ".srt"

	client := httpnet.NewHTTPClient()
	resp, err := client.Do(&httpnet.HTTPRequest{
		URL:    srt,
		Method: httpnet.HTTPCmdGet,
	})
	if err == nil && resp != nil {
		if s := resp.Headers.HTTPHeaderGet("Content-Type"); s != "" {
			if strings.EqualFold(s, "application/x-srt") {
				sub := htsmsg.NewMap()
				sub.AddStr("url", srt)
				sub.AddStr("source", "HTTP probe")
				sub.AddStr("title", "SRT file")
				sub.AddStr("format", "SRT")
				sublist.AddMsg("", sub)
			}
		}
	}
}

// browseVideoItem builds a videoparams: URL for the item.
// C: browse_video_item (upnp_browse.c:798-876)
func (ub *upnpBrowse) browseVideoItem(item *htsmsg.HTSMsg) {
	var url, mimetype string

	for _, f := range item.GetFields() {
		if f.GetType() != htsmsg.HmfStr {
			continue
		}

		res := f.GetMapByFieldIfName("res")
		if res == nil {
			continue
		}

		pi := res.GetStr("protocolInfo")
		if pi == "" {
			continue
		}

		// C: strtok_r(str, ":") x3 → proto:dlna:contentformat:additionalInfo
		parts := strings.SplitN(pi, ":", 4)
		proto := parts[0]
		if proto != "http-get" {
			continue
		}
		var contentformat, ai string
		if len(parts) > 2 {
			contentformat = parts[2]
		}
		if len(parts) > 3 {
			ai = parts[3]
		}

		if ai == "" || !strings.Contains(ai, "DLNA.ORG_PN=JPEG_TN") {
			url = f.GetStrValue()
			mimetype = contentformat
			break
		}
	}

	if url == "" {
		ub.browseFail("UPNP Video playback: No playable URL")
		return
	}

	title := item.GetStr("title")

	// Construct videoparam JSON blob

	vp := htsmsg.NewMap()
	vp.AddStr("canonicalUrl", url)

	vp.AddU32("no_fs_scan", 1) // Don't try to scan parent directory
	// for subtitles
	if title != "" {
		vp.AddStr("title", title)
	}

	src := htsmsg.NewMap()
	src.AddStr("url", url)
	if mimetype != "" {
		src.AddStr("mimetype", mimetype)
	}

	sources := htsmsg.NewList()
	sources.AddMsg("", src)

	vp.AddMsg("sources", sources)

	subtitles := htsmsg.NewList()

	minidlnaGetSrt(url, subtitles)
	blindSrtCheck(url, subtitles)

	vp.AddMsg("subtitles", subtitles)

	rstr, err := htsmsg.SerializeJSONToRstr(vp, "videoparams:")
	if err == nil {
		ub.source.SetString(rstr)
	}

	ub.directClose.SetInt(1)
	ub.typ.SetString("video")
	ub.loading.SetInt(0)
}

// browseItem — C: browse_item (upnp_browse.c:882-898)
func (ub *upnpBrowse) browseItem(item *htsmsg.HTSMsg, sync_ bool) {
	cls := item.GetStr("class")

	if cls == "" {
		ub.browseFail("Missing <class> in item tag")
		return
	}

	if strings.HasPrefix(cls, "object.item.videoItem") {
		ub.sys.usage.PageOpen(sync_, "UPNP Video")
		ub.browseVideoItem(item)
	} else {
		ub.sys.usage.PageOpen(sync_, "UPNP Unknown-item")
		ub.browseFail("Don't know how to browse %s", cls)
	}
}

// browseContainer — C: browse_container (upnp_browse.c:904-919)
func (ub *upnpBrowse) browseContainer(container *htsmsg.HTSMsg) {
	name := container.GetStr("title")
	cls := container.GetStr("class")

	if name != "" {
		ub.title.SetString(name)
	}

	if cls == "object.container.album.musicAlbum" {
		ub.contents.SetString("albumTracks")
	}

	ub.browseDirectory(name)
}

// browseSelf browses the item's own metadata then item or container.
// C: browse_self (upnp_browse.c:925-981)
func (ub *upnpBrowse) browseSelf(sync_ bool) {
	in := htsmsg.NewMap()

	in.AddStr("ObjectID", ub.id)
	in.AddStr("BrowseFlag", "BrowseMetadata")
	in.AddStr("Filter", "*")
	in.AddU32("StartingIndex", 0)
	in.AddU32("RequestedCount", 1)
	in.AddStr("SortCriteria", "")

	out, rerr := soap.SoapExec(ub.controlURL, "ContentDirectory", 1, "Browse",
		in)
	in.Release()

	if rerr != nil {
		ub.browseFail("%v", rerr)
		return
	}

	if out == nil {
		ub.browseFail("Malformed SOAP response, no returned variabled")
		return
	}

	result := out.GetStr("Result")
	if result == "" {
		out.Release()
		ub.browseFail("No SOAP result")
		return
	}

	meta, err := htsmsg.DeserializeXML(result)
	if meta == nil {
		out.Release()
		ub.browseFail("Malformed XML: %v", err)
		return
	}

	x := meta.GetMapMulti("DIDL-Lite", "container")
	if !sync_ && x != nil {
		ub.sys.usage.PageOpen(sync_, "UPNP Container")
		ub.browseContainer(x)
	} else if x = meta.GetMapMulti("DIDL-Lite", "item"); x != nil {
		ub.browseItem(x, sync_)
	} else {
		ub.browseFail("Browsing something that is neither item nor container")
		ub.sys.usage.PageOpen(sync_, "UPNP bad-item")
	}
	meta.Release()
	out.Release()
}

// beUpnpBrowse is the backend open handler for upnp: URLs.
// C: be_upnp_browse (upnp_browse.c:987-1013) — registered as be_open.
func (s *System) beUpnpBrowse(page any, url string, sync_ bool) error {
	p, ok := page.(*propcore.Prop)
	if !ok || p == nil {
		return fmt.Errorf("upnp: invalid page prop")
	}

	ub := &upnpBrowse{sys: s}
	ub.url = url

	ub.page = s.propManager.RefInc(p)
	ub.source = s.propManager.CreateEx(ub.page, "source", nil, false, false)

	ub.directClose = s.propManager.CreateEx(p, "directClose", nil, false, false)

	ub.model = s.propManager.CreateEx(p, "model", nil, false, false)

	ub.typ = s.propManager.CreateEx(ub.model, "type", nil, false, false)

	ub.contents = s.propManager.CreateEx(ub.model, "contents", nil, false, false)
	ub.error = s.propManager.CreateEx(ub.model, "error", nil, false, false)
	ub.nodes = s.propManager.CreateEx(ub.model, "nodes", nil, false, false)
	ub.items = s.propManager.CreateEx(ub.model, "source", nil, false, false)
	ub.loading = s.propManager.CreateEx(ub.model, "loading", nil, false, false)
	ub.loading.SetInt(1)

	ub.filter = s.propManager.CreateEx(ub.model, "filter", nil, false, false)
	ub.canFilter = s.propManager.CreateEx(ub.model, "canFilter", nil, false, false)

	metadataProp := s.propManager.CreateEx(ub.model, "metadata", nil, false, false)

	ub.title = s.propManager.CreateEx(metadataProp, "title", nil, false, false)

	if ub.resolve() == 0 {
		ub.browseSelf(sync_)
	} else {
		s.usage.PageOpen(sync_, "UPNP bad-route")
	}

	ub.destroy()
	return nil
}

// condWaitTimeout waits on c for at most ms milliseconds.
// Caller must hold m (== c.L); Wait releases it atomically.
// C: hts_cond_wait_timeout — returns true on timeout.
func condWaitTimeout(c *sync.Cond, m *sync.Mutex, ms int) bool {
	timedOut := false
	timer := time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
		m.Lock()
		timedOut = true
		c.Broadcast()
		m.Unlock()
	})
	c.Wait()
	timer.Stop()
	return timedOut
}
