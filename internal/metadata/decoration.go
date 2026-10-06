package metadata

// Port of src/metadata/decoration.c — 1:1 strict C-to-Go translation.
//
// The decoration subsystem attaches to a "nodes" directory prop and
// maintains per-item shadow state (deco_item) plus aggregate analysis
// (album / video / contents detection) that is deferred to the deco
// thread via deco_pendings.

import (
	"cmp"
	"fmt"
	"github.com/czz/movian-go/internal/gconf"
	"slices"
	"strings"
	"sync"

	"github.com/czz/movian-go/internal/db/kvstore"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// C: decoration.c:36-38
const (
	DecoModeAuto   = 0
	DecoModeManual = 1
	DecoModeOff    = 2
)

// C: metadata.h:416-418
const (
	DecoFlagsNoAutoDestroy = 0x1
	DecoFlagsRawFilenames  = 0x2
	DecoFlagsNoAutoSorting = 0x4
)

// C: metadata.h:30
const MetadataDurationLimit = 60 * 15

// C: CONTENT_num (metadata.h:77) — array bound for per-type lists
const contentNum = 14

// C: decoration.c:50
const stemHashSize = 503

// C: decoration.c:75-79 — db_pending_flags bits
const (
	dbPendingDeferredAlbumAnalysis = 0x1
	dbPendingDeferredVideoAnalysis = 0x2
	dbPendingDeferredUpdate        = 0x4
	dbPendingDeferredFullAnalysis  = -1 // C: 0xffffffff (int)
)

// C: decoration.c:86-89 — db_contents_mask bits
const (
	dbContentsImages   = 0x1
	dbContentsAlbum    = 0x2
	dbContentsTVSeason = 0x4
	dbContentsTVSeries = 0x8
)

// C: decoration.c:45-48 — module globals
var (
	decoCourier  *propcore.Courier
	decoMutex    sync.Mutex
	decoBrowses  []*DecoBrowse
	decoPendings int

	// C uses global prop system + settings root; Go injects them.
	decoPM *propcore.PropManager
	decoSM *settingscore.SettingsManager
)

// DecoBrowse — C: struct deco_browse (decoration.c:55-107)
type DecoBrowse struct {
	url string

	kvstore *kvstore.KVStore // C: global kvstore_* — injected at create
	mm      *MetadataManager // C: implicit default manager — injected
	gconf   *gconf.T         // C: gconf_t — injected at create (via kvs)

	sub   *propcore.Subscription
	items []*decoItem // C: TAILQ db_items (insertion order kept)

	propContents *propcore.Prop
	propModel    *propcore.Prop

	total int
	types [contentNum]int

	itemsPerCt [contentNum][]*decoItem // C: LIST db_items_per_ct

	stems [stemHashSize][]*decoStem // C: LIST db_stems[hash]

	imdbID string // C: rstr_t *db_imdb_id ("" = NULL)

	pendingFlags int

	pnf DecoNodeFilter

	audioFilter int

	contentsMask int

	currentContents int

	title string // C: rstr_t *db_title

	flags int

	lonelyVideoItem int

	mode int

	settingMode            *settingscore.Setting
	settingMarkAllAsSeen   *settingscore.Setting
	settingMarkAllAsUnseen *settingscore.Setting
	settingErasePlayinfo   *settingscore.Setting

	initiator string // C: rstr_t *db_initiator
}

// DecoNodeFilter is the prop_nf subset used by decoration.
// C: decorated_browse_create takes struct prop_nf *; the Go port has two
// prop_nf implementations (glw/view.NodeFilter and scanner.PropNF).
type DecoNodeFilter interface {
	// C: prop_nf_sort(nf, path, desc, idx, map, passive)
	Sort(path string, desc bool, idx int)
	// C: prop_nf_pred_str_add(nf, path, cmp, val, NULL, mode)
	AddStrPredicate(path, cmp, val, mode string) int
	// C: prop_nf_pred_remove(nf, id)
	RemovePredicate(id int)
	// C: prop_nf_retain / prop_nf_release
	Retain()
	Release()
}

// decoStem — C: deco_stem_t (decoration.c:113-119)
type decoStem struct {
	stem   string
	items  []*decoItem // C: LIST ds_items
	imdbID string      // C: rstr_t *ds_imdb_id
}

// decoItem — C: deco_item_t (decoration.c:125-165)
type decoItem struct {
	db *DecoBrowse

	ds      *decoStem
	postfix string

	root     *propcore.Prop
	metadata *propcore.Prop
	options  *propcore.Prop

	subType *decoSub
	ct      ContentType // C: contenttype_t di_type

	subURL *decoSub
	url    string // C: rstr_t *di_url
	hasURL bool   // C: di_url != NULL

	subFilename *decoSub
	filename    string
	hasFilename bool

	subAlbum *decoSub
	album    string
	hasAlbum bool

	subArtist *decoSub
	artist    string
	hasArtist bool

	subDuration *decoSub
	duration    int

	subSeries *decoSub
	series    string
	hasSeries bool

	subSeason *decoSub
	season    int

	mlv *MetadataLazyVideo
}

// decoSub emulates C's prop_subscribe(PROP_TAG_NAME(...),
// PROP_TAG_NAMED_ROOT root, "node") — a dynamic path subscription that
// follows the child chain under root and delivers the terminal prop's
// value changes. Re-resolves as intermediate children come and go.
type decoSub struct {
	di    *decoItem
	path  []string
	onVal func(di *decoItem, v any) // nil=NULL rstr, string, int

	// nodes[i] = resolved prop for path[i]; watches[i] = subscription on
	// (root if i==0 else nodes[i-1]) watching for child named path[i].
	nodes   []*propcore.Prop
	watches []*propcore.Subscription
	leafSub *propcore.Subscription
}

func propName(p *propcore.Prop) string {
	if p == nil {
		return ""
	}
	return p.GetName()
}

// arm attaches the subscription chain at the given depth under parent.
// C: prop_subfind chain construction in prop_subscribe.
func (s *decoSub) arm(parent *propcore.Prop, depth int) {
	if depth == len(s.path) {
		// Terminal: subscribe to the leaf prop's value.
		s.leafSub = decoCourierSubscribe(parent, func(_ any, ev propcore.EventType, args ...any) {
			if s.di == nil {
				return
			}
			switch ev {
			case propcore.EventSetRString, propcore.EventSetCString:
				if len(args) > 0 {
					if str, ok := args[0].(string); ok {
						s.onVal(s.di, str)
					}
				}
			case propcore.EventSetInt:
				v := 0
				if len(args) > 0 {
					switch n := args[0].(type) {
					case int:
						v = n
					case int64:
						v = int(n)
					}
				}
				s.onVal(s.di, v)
			case propcore.EventSetFloat:
				if len(args) > 0 {
					if f, ok := args[0].(float32); ok {
						s.onVal(s.di, int(f))
					}
				}
			case propcore.EventSetVoid:
				s.onVal(s.di, nil)
			}
		})
		return
	}

	name := s.path[depth]
	idx := depth
	sub := decoCourierSubscribe(parent, func(_ any, ev propcore.EventType, args ...any) {
		switch ev {
		case propcore.EventAddChild:
			if len(args) > 0 {
				if c, ok := args[0].(*propcore.Prop); ok && propName(c) == name {
					s.resolveAt(idx, c)
				}
			}
		case propcore.EventAddChildVector:
			if len(args) > 0 {
				if vec, ok := args[0].([]*propcore.Prop); ok {
					for _, c := range vec {
						if propName(c) == name {
							s.resolveAt(idx, c)
						}
					}
				}
			}
		case propcore.EventDelChild:
			if len(args) > 0 {
				if c, ok := args[0].(*propcore.Prop); ok && idx < len(s.nodes) && s.nodes[idx] == c {
					s.teardownFrom(idx)
				}
			}
		case propcore.EventSetVoid, propcore.EventDestroyed:
			s.teardownFrom(idx)
		}
	})
	for len(s.watches) <= depth {
		s.watches = append(s.watches, nil)
		s.nodes = append(s.nodes, nil)
	}
	s.watches[depth] = sub

	// Resolve an already-existing child.
	if c := parent.GetChild(name); c != nil {
		s.resolveAt(depth, c)
	}
}

// resolveAt records node for path[depth] and arms the next level.
func (s *decoSub) resolveAt(depth int, node *propcore.Prop) {
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
func (s *decoSub) teardownFrom(depth int) {
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
	}
}

// destroy — C: prop_unsubscribe on the chain root.
func (s *decoSub) destroy() {
	if s == nil {
		return
	}
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
	s.di = nil
}

// newDecoSub — C: prop_subscribe(PROP_TAG_NAME(path...),
// PROP_TAG_NAMED_ROOT, root, "node", PROP_TAG_COURIER, deco_courier).
func newDecoSub(di *decoItem, root *propcore.Prop, path []string,
	onVal func(di *decoItem, v any)) *decoSub {
	s := &decoSub{di: di, path: path, onVal: onVal}
	s.arm(root, 0)
	return s
}

// decoPropManager returns the prop manager — C uses the global prop system.
// Falls back to the manager owning p when DecorationStart hasn't run.
func decoPropManager(p *propcore.Prop) *propcore.PropManager {
	if decoPM != nil {
		return decoPM
	}
	return p.Manager()
}

// decoCourierSubscribe subscribes through the deco courier.
// C: PROP_TAG_COURIER, deco_courier.
func decoCourierSubscribe(p *propcore.Prop,
	cb func(opaque any, ev propcore.EventType, args ...any)) *propcore.Subscription {
	if p == nil {
		return nil
	}
	if decoCourier != nil {
		pm := decoPropManager(p)
		return pm.SubscribeWithCourier(p, decoCourier, cb, nil)
	}
	// Direct mode (tests): no courier thread serializes callbacks, so
	// take deco_mutex like the courier dispatch does (db.sub above does
	// the same).
	return p.Subscribe(func(opaque any, ev propcore.EventType, args ...any) {
		decoMutex.Lock()
		cb(opaque, ev, args...)
		decoMutex.Unlock()
	}, nil)
}

// selectIMDBID — C: select_imdb_id (decoration.c:175-185)
func selectIMDBID(di *decoItem) string {
	imdbid := ""
	if di.ds != nil {
		imdbid = di.ds.imdbID
	}
	if imdbid == "" && di.db.lonelyVideoItem != 0 {
		imdbid = di.db.imdbID
	}
	return imdbid
}

// insertVideoMlv — C: insert_video_mlv (decoration.c:190-219)
func insertVideoMlv(di *decoItem) {
	if !di.hasURL || !di.hasFilename {
		return
	}
	db := di.db
	if db.mode == DecoModeOff {
		return
	}
	manual := 0
	if db.mode == DecoModeManual {
		manual = 1
	}
	var fname string
	if db.flags&DecoFlagsRawFilenames != 0 {
		// C: fname = metadata_remove_postfix_rstr(di->di_filename)
		fname = misc.RstrGet(MetadataRemovePostfixRstr(
			misc.RstrAllocStr(di.filename), di.db.gconf))
	} else {
		fname = di.filename
	}
	lonely := 0
	if db.lonelyVideoItem != 0 {
		lonely = 1
	}
	if mm := db.mm; mm != nil {
		di.mlv = mm.MetadataBindVideoInfo(di.url, fname,
			selectIMDBID(di), di.duration, di.root, db.title,
			lonely, 0, -1, -1, -1, manual, db.initiator)
	}
}

// stemAnalysis — C: stem_analysis (decoration.c:225-250)
func stemAnalysis(db *DecoBrowse, ds *decoStem) {
	var video, image *decoItem
	for _, di := range ds.items {
		if di.ct == ContentImage {
			image = di
		}
		if di.ct == ContentVideo {
			video = di
		}
	}
	if video != nil && image != nil {
		pm := decoPM
		p := pm.RefInc(pm.CreateEx(video.metadata, "usericon", nil, false, false))
		p.SetString(image.url)
		pm.RefDec(p)
		// C: prop_set(video->di_metadata, "usericon", PROP_SET_RSTRING, url)
		pm.CreateEx(video.metadata, "usericon", nil, false, false).SetString(image.url)
		// C: prop_set(image->di_root, "hidden", PROP_SET_INT, 1)
		pm.CreateEx(image.root, "hidden", nil, false, false).SetInt(1)
	}
}

// setContents — C: set_contents (decoration.c:255-261)
func setContents(db *DecoBrowse, contents string, cacheok bool) {
	// C: kv_url_opt_set(db->db_url, KVSTORE_DOMAIN_SYS, "contents",
	//                   KVSTORE_SET_STRING, cacheok ? contents : NULL)
	var v any
	if cacheok {
		v = contents
	}
	db.kvstore.UrlOptSet(db.url, kvstore.DomainSys, "contents",
		kvstore.SetString, v)
	db.propContents.SetString(contents)
}

// updateContents — C: update_contents (decoration.c:267-327)
func updateContents(db *DecoBrowse) {
	mask := 0
	if db.mode == DecoModeAuto {
		mask = db.contentsMask
	}

	if mask&dbContentsAlbum == 0 {
		if db.audioFilter != 0 {
			db.pnf.RemovePredicate(db.audioFilter)
			db.audioFilter = 0
		}
	}

	if mask&dbContentsImages != 0 {
		if db.currentContents == dbContentsImages {
			return
		}
		db.currentContents = dbContentsImages
		setContents(db, "images", true)
		if db.flags&DecoFlagsNoAutoSorting == 0 {
			db.pnf.Sort("node.metadata.timestamp", false, 1)
			db.pnf.Sort("", false, 2)
		}
		return
	}

	if mask&dbContentsAlbum != 0 {
		if db.currentContents == dbContentsAlbum {
			return
		}
		db.currentContents = dbContentsAlbum
		setContents(db, "album", true)
		if db.flags&DecoFlagsNoAutoSorting == 0 {
			db.pnf.Sort("node.metadata.track", false, 1)
			db.pnf.Sort("", false, 2)
			if db.audioFilter == 0 {
				db.audioFilter = db.pnf.AddStrPredicate("node.type",
					"neq", "audio", "exclude")
			}
		}
		return
	}

	if mask&dbContentsTVSeason != 0 {
		if db.currentContents == dbContentsTVSeason {
			return
		}
		db.currentContents = dbContentsTVSeason
		setContents(db, "tvseason", false)
		if db.flags&DecoFlagsNoAutoSorting == 0 {
			db.pnf.Sort("node.metadata.episode.number", false, 1)
			db.pnf.Sort("", false, 2)
		}
		return
	}

	db.currentContents = 0
	setContents(db, "", true)
	db.pnf.Sort("", false, 1)
	db.pnf.Sort("", false, 2)
}

// typeAnalysis — C: type_analysis (decoration.c:333-343)
func typeAnalysis(db *DecoBrowse) {
	if db.types[int(ContentImage)]*4 > db.total*3 {
		db.contentsMask |= dbContentsImages
	} else {
		db.contentsMask &^= dbContentsImages
	}
	db.pendingFlags |= dbPendingDeferredUpdate
	decoPendings = 1
}

// decoArtist — C: deco_artist_t (decoration.c:349-353)
type decoArtist struct {
	artist string
	count  int
}

// albumAnalysis — C: album_analysis (decoration.c:371-460)
func albumAnalysis(db *DecoBrowse) {
	v := ""
	artists := map[string]*decoArtist{}
	var artistOrder []*decoArtist
	artistCount := 0
	itemCount := 0

	db.contentsMask &^= dbContentsAlbum

	if !(db.types[int(ContentAudio)] > 1 &&
		db.types[int(ContentVideo)] == 0 &&
		db.types[int(ContentArchive)] == 0 &&
		db.types[int(ContentDir)] == 0 &&
		db.types[int(ContentAlbum)] == 0 &&
		db.types[int(ContentPlugin)] == 0) {
		return
	}

	for _, di := range db.itemsPerCt[int(ContentAudio)] {
		if !di.hasAlbum {
			return // C: goto cleanup
		}
		itemCount++
		if v == "" {
			v = di.album
		} else if v != di.album {
			return
		}
		if !di.hasArtist {
			continue
		}
		da := artists[di.artist]
		if da == nil {
			da = &decoArtist{artist: di.artist, count: 1}
			artists[di.artist] = da
			artistOrder = append(artistOrder, da)
			artistCount++
		} else {
			da.count++
		}
	}

	db.contentsMask |= dbContentsAlbum

	pm := decoPM
	m := pm.CreateEx(db.propModel, "metadata", nil, false, false)
	pm.CreateEx(m, "album_name", nil, false, false).SetString(v)

	if artistCount > 0 {
		slices.SortFunc(artistOrder, func(a, b *decoArtist) int { return cmp.Compare(b.count, a.count) }) // C: qsort desc
		da := artistOrder[0]
		if da.count*2 >= itemCount {
			decoPM.CreateEx(m, "album_name", nil, false, false).SetString(da.artist)
			p := pm.CreateEx(m, "album_art", nil, false, false)
			if mm := db.mm; mm != nil {
				mm.MetadataBindAlbumart(p, da.artist, v)
			}
		}
	}
}

// videoAnalysis — C: video_analysis (decoration.c:466-568)
func videoAnalysis(db *DecoBrowse) {
	reasonableVideoItems := 0
	threshold := MetadataDurationLimit

	for _, di := range db.itemsPerCt[int(ContentVideo)] {
		if di.duration > threshold {
			reasonableVideoItems++
		}
	}
	if reasonableVideoItems <= 1 {
		db.lonelyVideoItem = 1
	} else {
		db.lonelyVideoItem = 0
	}

	for _, di := range db.itemsPerCt[int(ContentVideo)] {
		if di.mlv == nil {
			insertVideoMlv(di)
		} else if mm := db.mm; mm != nil {
			mm.MLVSetDuration(di.mlv, di.duration)
			mm.MLVSetLonely(di.mlv, db.lonelyVideoItem)
			mm.MLVSetIMDBID(di.mlv, selectIMDBID(di))
		}
	}

	series := ""
	seriesOK := false
	season := -1

	for _, di := range db.itemsPerCt[int(ContentVideo)] {
		if !di.hasSeries {
			seriesOK = false
			series = ""
			break
		}
		if !seriesOK && series == "" {
			series = di.series
			seriesOK = true
		} else if series != di.series {
			seriesOK = false
			series = ""
			break
		}
	}
	if !seriesOK {
		series = ""
	}

	if series != "" {
		for _, di := range db.itemsPerCt[int(ContentVideo)] {
			if di.season == 0 {
				season = -1
				break
			}
			if season == -1 {
				season = di.season
			} else if season != di.season {
				season = -1
				break
			}
		}
	}

	var firstVideo *decoItem
	if len(db.itemsPerCt[int(ContentVideo)]) > 0 {
		firstVideo = db.itemsPerCt[int(ContentVideo)][0]
	}

	pm := decoPM
	x := pm.CreateEx(db.propModel, "season", nil, false, false)
	if season != -1 && series != "" && firstVideo != nil {
		db.contentsMask |= dbContentsTVSeason
		y := pm.CreateEx(firstVideo.metadata, "season", nil, false, false)
		pm.Link(y, x, nil, false, false)
	} else {
		pm.Unlink(x)
		db.contentsMask &^= dbContentsTVSeason
	}

	x = pm.CreateEx(db.propModel, "series", nil, false, false)
	if series != "" && firstVideo != nil {
		db.contentsMask |= dbContentsTVSeries
		y := pm.CreateEx(firstVideo.metadata, "series", nil, false, false)
		pm.Link(y, x, nil, false, false)
	} else {
		pm.Unlink(x)
		db.contentsMask &^= dbContentsTVSeries
	}
}

// stemGet — C: stem_get (decoration.c:575-589)
func stemGet(db *DecoBrowse, str string) *decoStem {
	hash := misc.Mystrhash(str) % stemHashSize
	for _, ds := range db.stems[hash] {
		if ds.stem == str {
			return ds
		}
	}
	ds := &decoStem{stem: str}
	db.stems[hash] = slices.Insert(db.stems[hash], 0, ds)
	return ds
}

// stemRelease — C: stem_release (decoration.c:595-604)
func stemRelease(db *DecoBrowse, ds *decoStem) {
	if len(ds.items) != 0 {
		return
	}
	hash := misc.Mystrhash(ds.stem) % stemHashSize
	lst := db.stems[hash]
	for i, x := range lst {
		if x == ds {
			db.stems[hash] = slices.Delete(lst, i, i+1)
			break
		}
	}
}

// diSetURL — C: di_set_url (decoration.c:611-656)
func diSetURL(di *decoItem, v any) {
	db := di.db
	str, has := v.(string)
	di.url = str
	di.hasURL = v != nil && has

	if di.ds != nil {
		for i, x := range di.ds.items {
			if x == di {
				di.ds.items = slices.Delete(di.ds.items, i, i+1)
				break
			}
		}
		stemRelease(db, di.ds)
		di.ds = nil
		di.postfix = ""
	}

	if v == nil || !has {
		return
	}

	s := str
	postfix := ""
	if idx := strings.LastIndexByte(s, '.'); idx >= 0 {
		if !strings.Contains(s[idx:], "/") {
			postfix = s[idx+1:]
			s = s[:idx]
		}
	}

	ds := stemGet(db, s)
	ds.items = slices.Insert(ds.items, 0, di)
	di.ds = ds
	di.postfix = postfix

	if postfix != "" && strings.EqualFold(postfix, "nfo") {
		loadNFO(di)
		di.db.pendingFlags |= dbPendingDeferredVideoAnalysis
		decoPendings = 1
		return
	}
	stemAnalysis(db, ds)
}

// diSetAlbum — C: di_set_album (decoration.c:662-668)
func diSetAlbum(di *decoItem, v any) {
	di.album, di.hasAlbum = v.(string)
	if v == nil {
		di.hasAlbum = false
	}
	di.db.pendingFlags |= dbPendingDeferredAlbumAnalysis
	decoPendings = 1
}

// diSetArtist — C: di_set_artist (decoration.c:674-680)
func diSetArtist(di *decoItem, v any) {
	di.artist, di.hasArtist = v.(string)
	if v == nil {
		di.hasArtist = false
	}
	di.db.pendingFlags |= dbPendingDeferredAlbumAnalysis
	decoPendings = 1
}

// diSetFilename — C: di_set_filename (decoration.c:686-693)
func diSetFilename(di *decoItem, v any) {
	di.filename, di.hasFilename = v.(string)
	if v == nil {
		di.hasFilename = false
	}
	di.db.pendingFlags |= dbPendingDeferredVideoAnalysis
	decoPendings = 1
}

// diSetDuration — C: di_set_duration (decoration.c:699-707)
func diSetDuration(di *decoItem, duration int) {
	di.duration = duration
	if di.ct == ContentVideo {
		di.db.pendingFlags |= dbPendingDeferredVideoAnalysis
		decoPendings = 1
	}
}

// diSetSeries — C: di_set_series (decoration.c:714-720)
func diSetSeries(di *decoItem, v any) {
	di.series, di.hasSeries = v.(string)
	if v == nil {
		di.hasSeries = false
	}
	di.db.pendingFlags |= dbPendingDeferredVideoAnalysis
	decoPendings = 1
}

// diSetSeason — C: di_set_season (decoration.c:727-732)
func diSetSeason(di *decoItem, v int) {
	di.season = v
	di.db.pendingFlags |= dbPendingDeferredVideoAnalysis
	decoPendings = 1
}

// diSetType — C: di_set_type (decoration.c:739-841)
func diSetType(di *decoItem, v any) {
	db := di.db
	str, _ := v.(string)

	if di.subAlbum != nil {
		di.subAlbum.destroy()
		di.album, di.hasAlbum = "", false
		di.subAlbum = nil
	}
	if di.subArtist != nil {
		di.subArtist.destroy()
		di.artist, di.hasArtist = "", false
		di.subArtist = nil
	}
	if di.subDuration != nil {
		di.subDuration.destroy()
		di.duration = 0
		di.subDuration = nil
	}
	if di.subSeries != nil {
		di.subSeries.destroy()
		di.series, di.hasSeries = "", false
		di.subSeries = nil
	}
	if di.subSeason != nil {
		di.subSeason.destroy()
		di.season = 0
		di.subSeason = nil
	}

	db.types[int(di.ct)]--
	di.itemsPerCtRemove(di.ct)

	if v == nil {
		di.ct = ContentUnknown
	} else {
		di.ct = Type2Content(str)
	}
	db.types[int(di.ct)]++
	db.itemsPerCt[int(di.ct)] = append([]*decoItem{di}, db.itemsPerCt[int(di.ct)]...)

	switch di.ct {
	case ContentAudio:
		di.subAlbum = newDecoSub(di, di.root,
			[]string{"metadata", "album"}, diSetAlbum)
		di.subArtist = newDecoSub(di, di.root,
			[]string{"metadata", "artist"}, diSetArtist)
		di.db.pendingFlags |= dbPendingDeferredAlbumAnalysis
		decoPendings = 1

	case ContentVideo:
		di.subDuration = newDecoSub(di, di.root,
			[]string{"metadata", "duration"},
			func(di *decoItem, v any) {
				if n, ok := v.(int); ok {
					diSetDuration(di, n)
				} else {
					diSetDuration(di, 0)
				}
			})
		di.subSeries = newDecoSub(di, di.root,
			[]string{"metadata", "series", "title"}, diSetSeries)
		di.subSeason = newDecoSub(di, di.root,
			[]string{"metadata", "season", "number"},
			func(di *decoItem, v any) {
				if n, ok := v.(int); ok {
					diSetSeason(di, n)
				} else {
					diSetSeason(di, 0)
				}
			})
		di.db.pendingFlags |= dbPendingDeferredVideoAnalysis
		decoPendings = 1
	}

	typeAnalysis(db)
	if di.ds != nil {
		stemAnalysis(db, di.ds)
	}
}

// itemsPerCtRemove — C: LIST_REMOVE(di, di_type_link)
func (di *decoItem) itemsPerCtRemove(ct ContentType) {
	lst := di.db.itemsPerCt[int(ct)]
	for i, x := range lst {
		if x == di {
			di.db.itemsPerCt[int(ct)] = slices.Delete(lst, i, i+1)
			return
		}
	}
}

// decoBrowseAddNode — C: deco_browse_add_node (decoration.c:847-892)
func decoBrowseAddNode(db *DecoBrowse, p *propcore.Prop, before *decoItem) {
	pm := decoPM
	di := &decoItem{db: db}
	di.root = pm.RefInc(p)
	di.metadata = pm.RefInc(pm.CreateEx(p, "metadata", nil, false, false))
	di.options = pm.RefInc(pm.CreateEx(p, "options", nil, false, false))

	db.total++
	di.ct = ContentUnknown
	db.types[int(di.ct)]++
	db.itemsPerCt[int(di.ct)] = append([]*decoItem{di}, db.itemsPerCt[int(di.ct)]...)

	pm.TagSet(p, decoTagKey(db), di)

	if before != nil {
		for i, x := range db.items {
			if x == before {
				db.items = slices.Insert(db.items, i, di)
				break
			}
		}
	} else {
		db.items = append(db.items, di)
	}

	di.subURL = newDecoSub(di, p, []string{"url"}, diSetURL)
	di.subFilename = newDecoSub(di, p, []string{"filename"}, diSetFilename)
	di.subType = newDecoSub(di, p, []string{"type"},
		func(di *decoItem, v any) { diSetType(di, v) })
}

// decoBrowseAddNodes — C: deco_browse_add_nodes (decoration.c:898-907)
func decoBrowseAddNodes(db *DecoBrowse, pv []*propcore.Prop, before *decoItem) {
	for _, p := range pv {
		decoBrowseAddNode(db, p, before)
	}
}

// decoItemDestroy — C: deco_item_destroy (decoration.c:913-947)
func decoItemDestroy(db *DecoBrowse, di *decoItem) {
	pm := decoPM
	if di.ds != nil {
		for i, x := range di.ds.items {
			if x == di {
				di.ds.items = slices.Delete(di.ds.items, i, i+1)
				break
			}
		}
		stemRelease(db, di.ds)
	}
	di.itemsPerCtRemove(di.ct)
	db.types[int(di.ct)]--
	db.total--
	pm.RefDec(di.root)
	pm.RefDec(di.metadata)
	pm.RefDec(di.options)
	if di.subURL != nil {
		di.subURL.destroy()
	}
	if di.subFilename != nil {
		di.subFilename.destroy()
	}
	if di.subType != nil {
		di.subType.destroy()
	}
	if di.subAlbum != nil {
		di.subAlbum.destroy()
	}
	if di.subArtist != nil {
		di.subArtist.destroy()
	}
	if di.subDuration != nil {
		di.subDuration.destroy()
	}
	if di.subSeries != nil {
		di.subSeries.destroy()
	}
	if di.subSeason != nil {
		di.subSeason.destroy()
	}
	for i, x := range db.items {
		if x == di {
			db.items = slices.Delete(db.items, i, i+1)
			break
		}
	}
	if di.mlv != nil {
		if mm := di.db.mm; mm != nil {
			mm.MLVUnbind(di.mlv, 0)
		}
	}
}

// decoBrowseDelNode — C: deco_browse_del_node (decoration.c:951-957)
func decoBrowseDelNode(db *DecoBrowse, di *decoItem) {
	if di == nil {
		return
	}
	db.pendingFlags |= dbPendingDeferredFullAnalysis
	decoItemDestroy(db, di)
	decoPendings = 1
}

// decoBrowseClear — C: deco_browse_clear (decoration.c:963-972)
func decoBrowseClear(db *DecoBrowse) {
	pm := decoPM
	for len(db.items) > 0 {
		di := db.items[0]
		pm.TagClear(di.root, decoTagKey(db))
		decoItemDestroy(db, di)
	}
}

// decoBrowseDestroy — C: deco_browse_destroy (decoration.c:978-996)
func decoBrowseDestroy(db *DecoBrowse) {
	pm := decoPM
	db.pnf.Release()
	decoBrowseClear(db)
	if sm := decoSM; sm != nil {
		sm.Destroy(db.settingMode)
		sm.Destroy(db.settingMarkAllAsSeen)
		sm.Destroy(db.settingMarkAllAsUnseen)
		sm.Destroy(db.settingErasePlayinfo)
	}
	if db.sub != nil {
		db.sub.Unsubscribe()
	}
	pm.RefDec(db.propContents)
	pm.RefDec(db.propModel)
	for i, x := range decoBrowses {
		if x == db {
			decoBrowses = slices.Delete(decoBrowses, i, i+1)
			break
		}
	}
}

// decoBrowseNodeCb — C: deco_browse_node_cb (decoration.c:1002-1056)
func decoBrowseNodeCb(opaque any, event propcore.EventType, args ...any) {
	db := opaque.(*DecoBrowse)
	pm := decoPM

	switch event {
	case propcore.EventAddChild:
		if len(args) > 0 {
			if p, ok := args[0].(*propcore.Prop); ok {
				decoBrowseAddNode(db, p, nil)
			}
		}
	case propcore.EventAddChildBefore:
		if len(args) > 2 {
			p1, _ := args[0].(*propcore.Prop)
			var before *decoItem
			if p2, ok := args[2].(*propcore.Prop); ok {
				before, _ = pm.TagGet(p2, decoTagKey(db)).(*decoItem)
			}
			decoBrowseAddNode(db, p1, before)
		}
	case propcore.EventAddChildVector:
		if len(args) > 0 {
			if pv, ok := args[0].([]*propcore.Prop); ok {
				decoBrowseAddNodes(db, pv, nil)
			}
		}
	case propcore.EventAddChildVectorBefore:
		if len(args) > 1 {
			pv, _ := args[0].([]*propcore.Prop)
			var before *decoItem
			if p2, ok := args[1].(*propcore.Prop); ok {
				before, _ = pm.TagGet(p2, decoTagKey(db)).(*decoItem)
			}
			decoBrowseAddNodes(db, pv, before)
		}
	case propcore.EventDelChild:
		if len(args) > 0 {
			if p, ok := args[0].(*propcore.Prop); ok {
				di, _ := pm.TagClear(p, decoTagKey(db)).(*decoItem)
				decoBrowseDelNode(db, di)
			}
		}
	case propcore.EventSetVoid:
		decoBrowseClear(db)
	case propcore.EventDestroyed:
		decoBrowseDestroy(db)
	}
}

// DecoratedBrowseDestroy — C: decorated_browse_destroy (decoration.c:1064-1070)
func DecoratedBrowseDestroy(db *DecoBrowse) {
	decoMutex.Lock()
	decoBrowseDestroy(db)
	decoMutex.Unlock()
}

// erasePlayinfo — C: erase_playinfo (decoration.c:1075-1088)
func erasePlayinfo(opaque any, _ any) {
	db := opaque.(*DecoBrowse)
	urls := make([]string, 0, db.total)
	for _, di := range db.items {
		urls = append(urls, di.url)
	}
	if mm := db.mm; mm != nil {
		mm.PlayInfoEraseURLs(urls)
	}
}

// markContentAs — C: mark_content_as (decoration.c:1093-1106)
func markContentAs(db *DecoBrowse, contentType ContentType, seen int) {
	numVideos := db.types[int(contentType)]
	urls := make([]string, 0, numVideos)
	for _, di := range db.itemsPerCt[int(contentType)] {
		urls = append(urls, di.url)
	}
	if mm := db.mm; mm != nil {
		mm.PlayInfoMarkURLsAs(urls, seen)
	}
}

// markAllAsSeen — C: mark_all_as_seen (decoration.c:1112-1117)
func markAllAsSeen(opaque any, _ any) {
	markContentAs(opaque.(*DecoBrowse), ContentVideo, 1)
}

// markAllAsUnseen — C: mark_all_as_unseen (decoration.c:1123-1128)
func markAllAsUnseen(opaque any, _ any) {
	markContentAs(opaque.(*DecoBrowse), ContentVideo, 0)
}

// setMode — C: set_mode (decoration.c:1134-1166)
func setMode(opaque any, value any) {
	db := opaque.(*DecoBrowse)
	str, _ := value.(string)
	v := misc.Atoi(str)
	if v == db.mode {
		return
	}
	db.mode = v

	if mm := db.mm; mm != nil {
		for _, di := range db.itemsPerCt[int(ContentVideo)] {
			if di.mlv != nil {
				mm.MLVUnbind(di.mlv, 1)
				di.mlv = nil
			}
		}
	}

	switch v {
	case DecoModeAuto, DecoModeManual:
		decoPendings = 1
		db.pendingFlags |= dbPendingDeferredFullAnalysis
	}
	updateContents(db)
}

// decoTagKey — C uses the db pointer as prop_tag key.
func decoTagKey(db *DecoBrowse) string {
	return fmt.Sprintf("deco:%p", db)
}

// DecoratedBrowseCreate — C: decorated_browse_create (decoration.c:1172-1246)
func DecoratedBrowseCreate(model *propcore.Prop, pnf DecoNodeFilter,
	items *propcore.Prop, title string, flags int, url string,
	initiator string, kvs *kvstore.KVStore, mm *MetadataManager) *DecoBrowse {

	pm := decoPM
	if pm == nil {
		// C uses the global prop system — adopt the items prop's manager.
		decoPM = items.Manager()
		pm = decoPM
	}
	db := &DecoBrowse{
		initiator: initiator,
		url:       url,
		title:     title,
		flags:     flags,
		kvstore:   kvs,
		mm:        mm,
		gconf:     kvs.Gconf(),
	}

	decoMutex.Lock()

	subFlags := 0
	if flags&DecoFlagsNoAutoDestroy == 0 {
		subFlags = propcore.SubFlagTrackDestroy
	}
	if decoCourier != nil {
		db.sub = pm.SubscribeWithCourier(items, decoCourier,
			decoBrowseNodeCb, db, subFlags)
	} else {
		// Direct mode (tests): no courier thread serializes callbacks,
		// so take deco_mutex like the courier dispatch does.
		db.sub = items.Subscribe(func(opaque any,
			ev propcore.EventType, args ...any) {
			decoMutex.Lock()
			decoBrowseNodeCb(opaque, ev, args...)
			decoMutex.Unlock()
		}, db, subFlags)
	}

	pnf.Retain()
	db.pnf = pnf
	decoBrowses = slices.Insert(decoBrowses, 0, db)
	db.propModel = pm.RefInc(model)
	db.propContents = pm.RefInc(pm.CreateEx(model, "contents", nil, false, false))

	options := pm.CreateEx(model, "options", nil, false, false)
	if sm := decoSM; sm != nil {
		db.settingMode = sm.SettingCreate(settingscore.SettingMultiOpt,
			options,
			settingscore.SettingsInitialUpdate|settingscore.SettingsRawNodes,
			settingscore.SettingTagTitle, sm.P("Metadata mode"),
			settingscore.SettingTagKVStore, db.url, "metadatamode",
			settingscore.SettingTagCourier, decoCourier,
			settingscore.SettingTagCallback, setMode, db,
			settingscore.SettingTagOption, "0", sm.P("Automatic"),
			settingscore.SettingTagOption, "1", sm.P("Manual"),
			settingscore.SettingTagOption, "2", sm.P("Off"))

		db.settingMarkAllAsSeen = sm.SettingCreate(settingscore.SettingAction,
			options,
			settingscore.SettingsInitialUpdate|settingscore.SettingsRawNodes,
			settingscore.SettingTagTitle, sm.P("Mark all as seen"),
			settingscore.SettingTagCourier, decoCourier,
			settingscore.SettingTagCallback, markAllAsSeen, db)

		db.settingMarkAllAsUnseen = sm.SettingCreate(settingscore.SettingAction,
			options,
			settingscore.SettingsInitialUpdate|settingscore.SettingsRawNodes,
			settingscore.SettingTagTitle, sm.P("Mark all as unseen"),
			settingscore.SettingTagCourier, decoCourier,
			settingscore.SettingTagCallback, markAllAsUnseen, db)

		db.settingErasePlayinfo = sm.SettingCreate(settingscore.SettingAction,
			options,
			settingscore.SettingsInitialUpdate|settingscore.SettingsRawNodes,
			settingscore.SettingTagTitle, sm.P("Erase all playback info"),
			settingscore.SettingTagCourier, decoCourier,
			settingscore.SettingTagCallback, erasePlayinfo, db)
	}

	// C: kv_url_opt_get_rstr(url, KVSTORE_DOMAIN_SYS, "contents")
	contents := db.kvstore.UrlOptGetString(url, kvstore.DomainSys, "contents")
	db.propContents.SetString(contents)

	decoMutex.Unlock()
	return db
}

// decoThread — C: deco_thread (decoration.c:1252-1292)
func decoThread() {
	for {
		decoMutex.Lock()
		doTimo := 0
		if decoPendings != 0 {
			doTimo = 150
		}
		decoMutex.Unlock()

		// C: prop_courier_wait returns nonzero on timeout.
		timedOut := !decoCourier.WaitReady(doTimo)

		decoMutex.Lock()
		// C: prop_notify_dispatch(&q, 0) runs while holding deco_mutex.
		decoCourier.Poll()
		if timedOut && decoPendings != 0 {
			decoPendings = 0
			for _, db := range decoBrowses {
				if db.pendingFlags != 0 && db.mode == DecoModeAuto {
					if db.pendingFlags&dbPendingDeferredAlbumAnalysis != 0 {
						albumAnalysis(db)
					}
					if db.pendingFlags&dbPendingDeferredVideoAnalysis != 0 {
						videoAnalysis(db)
					}
					db.pendingFlags = 0
					updateContents(db)
				}
			}
		}
		decoMutex.Unlock()
	}
}

// DecorationStart — C: decoration_init (decoration.c:1298-1305).
// C uses global prop manager + settings; Go injects them.
func DecorationStart(pm *propcore.PropManager, sm *settingscore.SettingsManager) {
	decoPM = pm
	decoSM = sm
	decoCourier = propcore.NewCourier("deco")
	go decoThread()
}

// loadNFO — C: load_nfo (decoration.c:1311-1337)
func loadNFO(di *decoItem) {
	fam := di.db.mm.fam
	if fam == nil {
		return
	}
	fh, err := fileaccesscore.FAOpenEx(fam, di.url, 0, nil)
	if err != nil || fh == nil {
		return
	}
	b := fileaccesscore.LoadAndClose(fh)
	if b == nil {
		return
	}
	idx := strings.Index(string(b.Data), "http://www.imdb.com/title/tt")
	if idx >= 0 {
		tt := string(b.Data)[idx+len("http://www.imdb.com/title/"):]
		end := 0
		for end < len(tt) && (tt[end] == 't' || (tt[end] >= '0' && tt[end] <= '9')) {
			end++
		}
		r := tt[:end]
		if di.ds != nil {
			di.ds.imdbID = r
		}
		di.db.imdbID = r
	}
}
