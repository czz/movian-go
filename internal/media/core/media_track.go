// Package core — canonical port of src/media/media_track.c
//
// The media track manager watches a "tracks" property directory
// (mp_prop_audio_tracks / mp_prop_subtitle_tracks) and maintains:
//   - mtm_tracks: linear TAILQ of media_track_t
//   - mtm_sorted_tracks: RB tree sorted by score (mirrored as prop nodes
//     under mtm_sorted_nodes)
//   - auto-selection (mtm_suggest) honoring basescore + isolang score and
//     the "autosel" flag
//   - user selection persistence via kvstore url options
//   - the external-subtitle loader thread
//
// C serializes all of this under mp->mp_mutex via PROP_TAG_MUTEX +
// PROP_TAG_LOCKMGR mp_lockmgr on every subscription: the dispatch worker
// holds mp_mutex around each callback. mtm.dispatch is kept on top as
// the mtm-state serializer for non-callback entry points (same pattern
// as PlayQueue).

package core

import (
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// MediaTrackManagerType — C: mtm_type enum
type MediaTrackManagerType int

const (
	// C: MEDIA_TRACK_MANAGER_AUDIO
	MediaTrackManagerAudio MediaTrackManagerType = iota
	// C: MEDIA_TRACK_MANAGER_SUBTITLES
	MediaTrackManagerSubtitles
)

// LangScorer — C: i18n_audio_score / i18n_subtitle_score operate on the
// global i18n state. Wired via SetLangScorer (init.go → i18n instance).
type LangScorer interface {
	AudioScore(str string) int
	SubtitleScore(str string) int
	GetDefaultCharset() *misc.Charset
}

// DefaultCharsetSrc — C: i18n_get_default_charset() (i18n.c), resolved
// through the per-instance LangScorer (i18n instance). Nil until wired.
func (ms *MediaSystem) DefaultCharsetSrc() misc.CharsetDefaultSrc { return ms.LangScorer }

// mediaTrack — C: media_track_t
type mediaTrack struct {
	subURL       *namedChildSub // C: mt_sub_url
	url          string         // C: mt_url
	subIsolang   *namedChildSub // C: mt_sub_isolang
	subBasescore *namedChildSub // C: mt_sub_basescore
	subAutosel   *namedChildSub // C: mt_sub_autosel

	mtm  *MediaTrackMgr // C: mt_mtm
	root *propcore.Prop // C: mt_root

	sortedNode *propcore.Prop // C: mt_sorted_node (non-nil ⟺ in sorted set)

	autosel      int // C: mt_autosel
	isolangScore int // C: mt_isolang_score
	baseScore    int // C: mt_base_score
	finalScore   int // C: mt_final_score

	id uint64 // Go: deterministic tiebreak for C's pointer compare
}

// MediaTrackMgr — C: media_track_mgr_t
type MediaTrackMgr struct {
	sortedNodes *propcore.Prop // C: mtm_sorted_nodes
	selector    *propcore.Prop // C: mtm_selector (NULL in C — no-op)

	nodeSub    *propcore.Subscription // C: mtm_node_sub
	currentSub *propcore.Subscription // C: mtm_current_sub
	urlSub     *propcore.Subscription // C: mtm_url_sub

	tracks       []*mediaTrack // C: mtm_tracks (TAILQ)
	sortedTracks []*mediaTrack // C: mtm_sorted_tracks (RB tree)

	suggestedTrack *mediaTrack // C: mtm_suggested_track
	current        *mediaTrack // C: mtm_current
	mp             *MediaPipe  // C: mtm_mp

	currentURL   string // C: mtm_current_url
	canonicalURL string // C: mtm_canonical_url
	userPref     string // C: mtm_user_pref (rstr)
	hasUserPref  bool

	mtype   MediaTrackManagerType // C: mtm_type
	userSet bool                  // C: mtm_user_set

	nextID uint64 // Go: allocation counter for the pointer-order tiebreak

	// Serial dispatch — C: all callbacks run under mp->mp_mutex. See
	// PlayQueue.dispatch for the pattern.
	dqMu   sync.Mutex
	dqBusy bool
	dq     []func()
}

// dispatch — serialize all manager state transitions. Nested property
// notifications enqueue and drain after the current section.
// lockArgs — C: PROP_TAG_MUTEX, mp + PROP_TAG_LOCKMGR, mp_lockmgr on
// every media-track subscription. Nil when there is no media pipe.
func (mtm *MediaTrackMgr) lockArgs() []any {
	if mtm.mp == nil {
		return nil
	}
	return []any{
		propcore.SubMutex{Ptr: mtm.mp},
		propcore.SubLockmgr{L: mtm.mp.Sys.PropLockmgr},
	}
}

func (mtm *MediaTrackMgr) dispatch(fn func()) {
	mtm.dqMu.Lock()
	if mtm.dqBusy {
		mtm.dq = append(mtm.dq, fn)
		mtm.dqMu.Unlock()
		return
	}
	mtm.dqBusy = true
	mtm.dqMu.Unlock()
	fn()
	for {
		mtm.dqMu.Lock()
		if len(mtm.dq) == 0 {
			mtm.dqBusy = false
			mtm.dqMu.Unlock()
			return
		}
		q := mtm.dq
		mtm.dq = nil
		mtm.dqMu.Unlock()
		for _, f := range q {
			f()
		}
	}
}

// ---------------------------------------------------------------------------
// mp_add_track* — C: mp_add_trackr / mp_add_track_ex / mp_add_track /
// mp_add_track_off (media_track.c:40-149). The canonical versions already
// exist as MpAddTrack/MpAddTrackOff in lifecycle.go.
// ---------------------------------------------------------------------------

// MpAddTrackR — C: mp_add_trackr. Creates the track node with all
// canonical fields: url, format, longformat, source (link or rstring),
// isolang → language+isolang, title, basescore, autosel.
// Returns the created track prop (caller holds the ref; see C prop_ref_inc).
func MpAddTrackR(pm *propcore.PropManager, parent *propcore.Prop,
	title, url, format, longformat, isolang, source string,
	sourcep *propcore.Prop, basescore, autosel int) *propcore.Prop {

	if pm == nil {
		return nil
	}
	p := pm.CreateRoot("")
	if p == nil {
		return nil
	}
	retval := pm.RefInc(p) // C: retval = prop_ref_inc(p)
	s := pm.CreateEx(p, "source", nil, false, false)

	if up := pm.CreateEx(p, "url", nil, false, false); up != nil {
		up.SetString(url)
	}
	if fp := pm.CreateEx(p, "format", nil, false, false); fp != nil {
		fp.SetString(format)
	}
	if lp := pm.CreateEx(p, "longformat", nil, false, false); lp != nil {
		lp.SetString(longformat)
	}

	if sourcep != nil && s != nil {
		pm.Link(sourcep, s, nil, false, false)
	} else if s != nil {
		s.SetString(source)
	}

	if isolang != "" {
		if il := misc.IsolangFind(isolang); il != nil {
			if l := pm.CreateEx(p, "language", nil, false, false); l != nil {
				l.SetString(il.Fullname)
			}
			if l := pm.CreateEx(p, "isolang", nil, false, false); l != nil {
				l.SetString(il.Iso639_2)
			}
		}
	}

	if tp := pm.CreateEx(p, "title", nil, false, false); tp != nil {
		tp.SetString(title)
	}
	if bp := pm.CreateEx(p, "basescore", nil, false, false); bp != nil {
		bp.SetInt(basescore)
	}
	if ap := pm.CreateEx(p, "autosel", nil, false, false); ap != nil {
		ap.SetInt(autosel)
	}

	if parent != nil {
		if pm.SetParentEx(p, parent, nil, "") != 0 {
			pm.Destroy(p)
		}
	}
	return retval
}

// MpAddTrackEx — C: mp_add_track_ex (rstr wrapping is a no-op in Go —
// strings are interned by the runtime).
func MpAddTrackEx(pm *propcore.PropManager, parent *propcore.Prop,
	title, url, format, longformat, isolang, source string,
	sourcep *propcore.Prop, basescore, autosel int) *propcore.Prop {
	return MpAddTrackR(pm, parent, title, url, format, longformat,
		isolang, source, sourcep, basescore, autosel)
}

// ---------------------------------------------------------------------------
// Track manager
// ---------------------------------------------------------------------------

// mtmEventType — C: mtm_event_type
func (mtm *MediaTrackMgr) mtmEventType() event.EventType {
	switch mtm.mtype {
	case MediaTrackManagerAudio:
		return event.EVENT_SELECT_AUDIO_TRACK
	case MediaTrackManagerSubtitles:
		return event.EVENT_SELECT_SUBTITLE_TRACK
	default:
		return 0
	}
}

// mtmSendSubUnload — C: mtm_send_sub_unload
func (mtm *MediaTrackMgr) mtmSendSubUnload() {
	mp := mtm.mp
	mb := MediaBufAllocLocked(mp, 0)
	if mb == nil {
		return
	}
	mb.DataType = int(MBCtrlExtSubtitle)
	mb.DataOpaque = nil // C: mb->mb_data = NULL
	MbEnq(mp, mp.Video, mb)
}

// sortedInsert — C: RB_INSERT_SORTED(&mtm_sorted_tracks, mt,
// mt_sorted_link, track_score_cmp). track_score_cmp: final_score desc,
// then url strcmp asc, then stable id (C: pointer compare).
func (mtm *MediaTrackMgr) sortedInsert(mt *mediaTrack) {
	pos := len(mtm.sortedTracks)
	for i, x := range mtm.sortedTracks {
		if trackScoreCmp(mt, x) < 0 {
			pos = i
			break
		}
	}
	mtm.sortedTracks = insertMT(mtm.sortedTracks, mt, pos)
}

// sortedNext — C: RB_NEXT(mt, mt_sorted_link)
func (mtm *MediaTrackMgr) sortedNext(mt *mediaTrack) *mediaTrack {
	for i, x := range mtm.sortedTracks {
		if x == mt {
			if i+1 < len(mtm.sortedTracks) {
				return mtm.sortedTracks[i+1]
			}
			return nil
		}
	}
	return nil
}

// trackScoreCmp — C: track_score_cmp
func trackScoreCmp(a, b *mediaTrack) int {
	if a.finalScore != b.finalScore {
		return b.finalScore - a.finalScore
	}
	if x := strings.Compare(a.url, b.url); x != 0 {
		return x
	}
	// C: return a < b ? 1 : -1 (pointer order — arbitrary but stable)
	if a.id == b.id {
		return 0
	}
	if a.id < b.id {
		return 1
	}
	return -1
}

// mtResort — C: mt_resort. Re-inserts mt into the sorted set and moves its
// mirror node before the next entry's node under mtm_sorted_nodes.
func (mtm *MediaTrackMgr) mtResort(mt *mediaTrack) {
	if mt.sortedNode != nil {
		mtm.sortedTracks = removeMT(mtm.sortedTracks, mt)
	}

	mtm.sortedInsert(mt)

	next := mtm.sortedNext(mt)

	pm := mtm.pm()
	if mt.sortedNode == nil {
		mt.sortedNode = pm.CreateRoot("")
		if mt.sortedNode == nil {
			return
		}
		pm.Link(mt.root, mt.sortedNode, nil, false, false)

		var beforeNode *propcore.Prop
		if next != nil {
			beforeNode = next.sortedNode
		}
		if pm.SetParentEx(mt.sortedNode, mtm.sortedNodes,
			&propcore.SetParentOpaque{Before: beforeNode}, "") != 0 {
			// C: abort()
			panic("media_track: prop_set_parent_ex failed")
		}
	} else {
		var beforeNode *propcore.Prop
		if next != nil {
			beforeNode = next.sortedNode
		}
		pm.Move(mt.sortedNode, beforeNode)
	}
}

// mtmSuggest — C: mtm_suggest. Picks the best autosel track and fires
// EVENT_SELECT_*_TRACK.
func (mtm *MediaTrackMgr) mtmSuggest() {
	if len(mtm.tracks) == 0 {
		// All tracks deleted, clear the user-has-configured flag
		mtm.userSet = false

		if mtm.mtype == MediaTrackManagerSubtitles {
			// Stop any pending load of subtitles — C: mystrset direct,
			// mp_mutex already held by the caller/dispatcher.
			mtm.mp.SubtitleLoaderURL = ""
			mtm.mtmSendSubUnload()
		}
		return
	}

	if mtm.userSet {
		return
	}

	var mt *mediaTrack
	for _, t := range mtm.sortedTracks {
		if t.url == "" {
			continue
		}
		if t.url == "sub:off" || t.url == "audio:off" {
			continue
		}
		if t.autosel == 0 {
			continue
		}
		mt = t
		break
	}

	if mt == mtm.suggestedTrack {
		return
	}
	mtm.suggestedTrack = mt
	if mt == nil {
		return
	}

	mtm.pm().SelectChildPropEx(mt.root, nil, mtm.nodeSub)
	e := &event.EventSelectTrack{ID: mt.url}
	e.Event.SetConcrete(e)
	e.Type = mtm.mtmEventType()
	MpEnqueueEventLocked(mtm.mp, &MediaEvent{Type: int(e.Type), Data: e})
	if mtm.selector != nil {
		mtm.selector.SetString("score")
	}
}

// mtSetURL — C: mt_set_url
func (mt *mediaTrack) mtSetURL(str string) {
	mtm := mt.mtm
	mt.url = str

	mtm.mtResort(mt)

	if mtm.hasUserPref && mt.url != "" && mtm.userPref == mt.url {
		mtm.userSet = true
		mtm.pm().SelectChildPropEx(mt.root, nil, mtm.nodeSub)
		e := &event.EventSelectTrack{ID: mt.url}
		e.Event.SetConcrete(e)
		e.Type = mtm.mtmEventType()
		MpEnqueueEventLocked(mtm.mp, &MediaEvent{Type: int(e.Type), Data: e})
		if mtm.selector != nil {
			mtm.selector.SetString("user")
		}
		return
	}

	mtm.mtmSuggest()
}

// mtUpdateScore — C: mt_update_score
func (mt *mediaTrack) mtUpdateScore() {
	if mt.baseScore < 0 || mt.isolangScore < 0 {
		return
	}
	score := max(mt.baseScore+mt.isolangScore,
		// C: MAX(base+isolang, 0)
		0)
	if mt.finalScore == score {
		return
	}
	if sp := mt.mtm.pm().CreateEx(mt.root, "score", nil, false, false); sp != nil {
		sp.SetInt(score)
	}
	mt.finalScore = score
	mt.mtm.mtResort(mt)
	mt.mtm.mtmSuggest()
}

// mtSetIsolang — C: mt_set_isolang
func (mt *mediaTrack) mtSetIsolang(str string, hasValue bool) {
	switch mt.mtm.mtype {
	case MediaTrackManagerAudio:
		if hasValue && mt.mtm.mp.Sys.LangScorer != nil {
			mt.isolangScore = mt.mtm.mp.Sys.LangScorer.AudioScore(str)
		} else {
			mt.isolangScore = 0
		}
	case MediaTrackManagerSubtitles:
		if hasValue && mt.mtm.mp.Sys.LangScorer != nil {
			mt.isolangScore = mt.mtm.mp.Sys.LangScorer.SubtitleScore(str)
		} else {
			mt.isolangScore = 0
		}
	default:
		mt.isolangScore = 0
	}
	mt.mtUpdateScore()
}

// mtSetBasescore — C: mt_set_basescore
func (mt *mediaTrack) mtSetBasescore(v int) {
	mt.baseScore = v
	mt.mtUpdateScore()
}

// mtSetAutosel — C: mt_set_autosel
func (mt *mediaTrack) mtSetAutosel(v int) {
	mt.autosel = v
	mt.mtm.mtmSuggest()
}

// mtmAddTrack — C: mtm_add_track
func (mtm *MediaTrackMgr) mtmAddTrack(root *propcore.Prop, before *mediaTrack) {
	pm := mtm.pm()
	mt := &mediaTrack{}
	mt.id = mtm.nextID
	mtm.nextID++

	pm.TagSet(root, mtm.tagKey(), mt)
	mt.mtm = mtm
	mt.root = pm.RefInc(root)

	mt.isolangScore = -1
	mt.baseScore = -1
	mt.finalScore = -1
	mt.autosel = 0

	if before != nil {
		mtm.tracks = insertBeforeMT(mtm.tracks, mt, before)
	} else {
		mtm.tracks = append(mtm.tracks, mt)
	}

	// C: PROP_TAG_NAME("node","url") CALLBACK_STRING mt_set_url
	mt.subURL = mtm.watchNamedChild(root, "url",
		func(str string, has bool) {
			if !has {
				mt.mtSetURL("")
			} else {
				mt.mtSetURL(str)
			}
		})

	// C: PROP_TAG_NAME("node","autosel") CALLBACK_INT mt_set_autosel
	mt.subAutosel = mtm.watchNamedChildInt(root, "autosel",
		func(v int) { mt.mtSetAutosel(v) })

	// C: PROP_TAG_NAME("node","isolang") CALLBACK_STRING mt_set_isolang
	mt.subIsolang = mtm.watchNamedChild(root, "isolang",
		func(str string, has bool) { mt.mtSetIsolang(str, has) })

	// C: PROP_TAG_NAME("node","basescore") CALLBACK_INT mt_set_basescore
	mt.subBasescore = mtm.watchNamedChildInt(root, "basescore",
		func(v int) { mt.mtSetBasescore(v) })
}

// tagKey — C: prop_tag_* keyed by the mtm pointer.
func (mtm *MediaTrackMgr) tagKey() string {
	return "media_track_mgr"
}

// pm — PropManager accessor. Props carry their manager (Go `manager`
// field); mtm_sorted_nodes is always set for started managers.
func (mtm *MediaTrackMgr) pm() *propcore.PropManager {
	if mtm.sortedNodes != nil {
		return mtm.sortedNodes.Manager()
	}
	if mtm.mp != nil && mtm.mp.PropRoot != nil {
		return mtm.mp.PropRoot.Manager()
	}
	return nil
}

// mtDestroy — C: mt_destroy
func (mtm *MediaTrackMgr) mtDestroy(mt *mediaTrack) {
	if mtm.suggestedTrack == mt {
		mtm.suggestedTrack = nil
	}
	if mtm.current == mt {
		mtm.current = nil
	}
	if mt.sortedNode != nil {
		mtm.sortedTracks = removeMT(mtm.sortedTracks, mt)
		mtm.pm().Destroy(mt.sortedNode)
		mt.sortedNode = nil
	}
	mtm.tracks = removeMT(mtm.tracks, mt)

	if mt.subURL != nil {
		mt.subURL.Unsubscribe()
	}
	if mt.subIsolang != nil {
		mt.subIsolang.Unsubscribe()
	}
	if mt.subBasescore != nil {
		mt.subBasescore.Unsubscribe()
	}
	if mt.subAutosel != nil {
		mt.subAutosel.Unsubscribe()
	}
	mt.url = ""
	if mt.root != nil {
		mtm.pm().RefDec(mt.root)
		mt.root = nil
	}
}

// mtmClear — C: mtm_clear
func (mtm *MediaTrackMgr) mtmClear() {
	for len(mtm.tracks) > 0 {
		mt := mtm.tracks[0]
		mtm.pm().TagClear(mt.root, mtm.tagKey())
		mtm.mtDestroy(mt)
	}
}

// mtmUpdateTracks — C: mtm_update_tracks. Runs inside mtm.dispatch.
func (mtm *MediaTrackMgr) mtmUpdateTracks(ev propcore.EventType, args []any) {
	propAt := func(i int) *propcore.Prop {
		if i < len(args) {
			p, _ := args[i].(*propcore.Prop)
			return p
		}
		return nil
	}
	switch ev {
	case propcore.EventAddChild:
		mtm.mtmAddTrack(propAt(0), nil)

	case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		for _, a := range args {
			switch vec := a.(type) {
			case []*propcore.Prop:
				for _, p := range vec {
					mtm.mtmAddTrack(p, nil)
				}
			case *propcore.PropVec:
				for i := range vec.Len() {
					mtm.mtmAddTrack(vec.Get(i), nil)
				}
			}
		}

	case propcore.EventAddChildBefore:
		p1 := propAt(0)
		p2 := propAt(len(args) - 1)
		var before *mediaTrack
		if p2 != nil {
			before, _ = mtm.pm().TagGet(p2, mtm.tagKey()).(*mediaTrack)
		}
		mtm.mtmAddTrack(p1, before)

	case propcore.EventDelChild:
		if p := propAt(0); p != nil {
			if mt, ok := mtm.pm().TagClear(p, mtm.tagKey()).(*mediaTrack); ok && mt != nil {
				mtm.mtDestroy(mt)
			}
		}
		mtm.mtmSuggest()

	case propcore.EventMoveChild:
		// NOP

	case propcore.EventSetDir, propcore.EventWantMoreChilds:
		// NOP

	case propcore.EventSetVoid:
		mtm.mtmClear()
		mtm.mtmSuggest()

	case propcore.EventSelectChild:
		// NOP
	}
}

// mtmSetCurrent — C: mtm_set_current. Runs inside mtm.dispatch.
func (mtm *MediaTrackMgr) mtmSetCurrent(str string, has bool) {
	if has {
		mtm.currentURL = str
	} else {
		mtm.currentURL = ""
	}
	if !has {
		return
	}
	for _, mt := range mtm.tracks {
		if mt.url != "" && mt.url == str {
			mtm.current = mt
			return
		}
	}
}

// mtmSetURL — C: mtm_set_url. Runs inside mtm.dispatch.
func (mtm *MediaTrackMgr) mtmSetURL(str string, has bool) {
	if has {
		mtm.canonicalURL = str
	} else {
		mtm.canonicalURL = ""
	}
	key := "subtitleTrack"
	if mtm.mtype == MediaTrackManagerAudio {
		key = "audioTrack"
	}
	// C: kv_url_opt_get_rstr(str, KVSTORE_DOMAIN_SYS, key)
	// C: kv_url_opt_get_rstr used the global kvstore; here mp.KVStore
	// may still be nil in the window before post-create assignment.
	if mtm.mp.KVStore == nil {
		mtm.userPref = ""
		mtm.hasUserPref = false
		return
	}
	r := mtm.mp.KVStore.UrlOptGetString(str, kvstore.DomainSys, key)
	mtm.userPref = r
	mtm.hasUserPref = r != ""
}

// MpTrackMgrSetup — C: mp_track_mgr_init.
// root is the tracks dir (audiostreams/subtitlestreams), current the
// "current" prop, sorted the "sorted" dir prop.
func MpTrackMgrSetup(mp *MediaPipe, mtm *MediaTrackMgr, root *propcore.Prop,
	mtype MediaTrackManagerType, current, sorted *propcore.Prop) {

	mtm.mp = mp
	mtm.mtype = mtype
	mtm.sortedNodes = sorted

	mtm.dispatch(func() {
		// C: prop_subscribe(PROP_TAG_CALLBACK, mtm_update_tracks,
		//                   PROP_TAG_ROOT, root, PROP_TAG_LOCKMGR,
		//                   mp_lockmgr, PROP_TAG_MUTEX, mp)
		if root != nil {
			mtm.nodeSub = root.Subscribe(
				func(_ any, ev propcore.EventType, a ...any) {
					mtm.dispatch(func() { mtm.mtmUpdateTracks(ev, a) })
				}, nil, mtm.lockArgs()...)
		}

		// C: PROP_TAG_CALLBACK_STRING mtm_set_current on `current`
		//    (+ PROP_TAG_LOCKMGR mp_lockmgr, PROP_TAG_MUTEX mp)
		if current != nil {
			mtm.currentSub = current.Subscribe(
				func(_ any, ev propcore.EventType, a ...any) {
					str, has := propEventString(ev, a)
					mtm.dispatch(func() { mtm.mtmSetCurrent(str, has) })
				}, nil, mtm.lockArgs()...)
		}

		// C: PROP_TAG_CALLBACK_STRING mtm_set_url on mp->mp_prop_url
		//    (+ PROP_TAG_LOCKMGR mp_lockmgr, PROP_TAG_MUTEX mp)
		if mp.PropURL != nil {
			mtm.urlSub = mp.PropURL.Subscribe(
				func(_ any, ev propcore.EventType, a ...any) {
					str, has := propEventString(ev, a)
					mtm.dispatch(func() { mtm.mtmSetURL(str, has) })
				}, nil, mtm.lockArgs()...)
		}
	})
}

// MpTrackMgrDestroy — C: mp_track_mgr_destroy.
func MpTrackMgrDestroy(mtm *MediaTrackMgr) {
	if mtm == nil {
		return
	}
	mtm.dispatch(func() {
		if mtm.nodeSub != nil {
			mtm.nodeSub.Unsubscribe()
			mtm.nodeSub = nil
		}
		if mtm.currentSub != nil {
			mtm.currentSub.Unsubscribe()
			mtm.currentSub = nil
		}
		if mtm.urlSub != nil {
			mtm.urlSub.Unsubscribe()
			mtm.urlSub = nil
		}
		mtm.mtmClear()
		mtm.currentURL = ""
		mtm.canonicalURL = ""
		mtm.userPref = ""
		mtm.hasUserPref = false
	})
}

// MpTrackMgrNextTrack — C: mp_track_mgr_next_track. Cycles to the next
// track (sorted order if the current one is in the sorted set).
func MpTrackMgrNextTrack(mtm *MediaTrackMgr) {
	mtm.dispatch(func() {
		mt := mtm.current

		if mt != nil {
			if mt.sortedNode != nil {
				mt = mtm.sortedNext(mt)
			} else {
				mt = nextMT(mtm.tracks, mt)
			}
		}

		if mt == nil {
			if len(mtm.sortedTracks) > 0 {
				mt = mtm.sortedTracks[0]
			} else if len(mtm.tracks) > 0 {
				mt = mtm.tracks[0]
			}
		}

		if mt != nil && mt != mtm.current {
			mtm.pm().SelectChildPropEx(mt.root, nil, mtm.nodeSub)
			e := &event.EventSelectTrack{ID: mt.url, Manual: true}
			e.Event.SetConcrete(e)
			e.Type = mtm.mtmEventType()
			MpEnqueueEventLocked(mtm.mp, &MediaEvent{Type: int(e.Type), Data: e})
			mtm.current = mt
		}
	})
}

// MpTrackMgrSelectTrack — C: mp_track_mgr_select_track. Returns non-zero
// when a libav stream switch was requested (rval).
func MpTrackMgrSelectTrack(mtm *MediaTrackMgr, id string, manual bool) int {
	var rval int
	mtm.dispatch(func() {
		// C: mp_track_mgr_select_track does not take mp_mutex — the
		// caller holds it (prop-callback context or test).
		rval = mtm.selectTrackLocked(id, manual)
	})
	return rval
}

// selectTrackLocked — C: mp_track_mgr_select_track body.
func (mtm *MediaTrackMgr) selectTrackLocked(id string, manual bool) int {
	isAudio := mtm.mtype == MediaTrackManagerAudio
	rval := 0
	mp := mtm.mp
	man := 0
	if manual {
		man = 1
	}

	if isAudio {
		if id == "audio:off" {
			mp.Audio.Stream = -1
		} else if strings.HasPrefix(id, "libav:") {
			mp.Audio.Stream, _ = strconv.Atoi(id[len("libav:"):])
			rval = 1
		}
		if mp.PropAudioTrackCurrent != nil {
			mp.PropAudioTrackCurrent.SetString(id)
		}
		if mp.PropAudioTrackCurrentManual != nil {
			mp.PropAudioTrackCurrentManual.SetInt(man)
		}
	} else {
		// Sending an empty MB_CTRL_EXT_SUBTITLE will cause unload
		MpSendCmdLocked(mp, mp.Video, int(MBCtrlExtSubtitle))

		// Make the subtitle loader not inject new sub even if running
		// C: mystrset(&mp->mp_subtitle_loader_url, NULL) — under mp_mutex
		mp.SubtitleLoaderURL = ""

		if mp.PropSubtitleTrackCurrent != nil {
			mp.PropSubtitleTrackCurrent.SetString(id)
		}
		if mp.PropSubtitleTrackCurrentManual != nil {
			mp.PropSubtitleTrackCurrentManual.SetInt(man)
		}
		mp.Video.Stream2 = -1

		if strings.HasPrefix(id, "sub:") {
			// nothing
		} else if strings.HasPrefix(id, "libav:") {
			mp.Video.Stream2, _ = strconv.Atoi(id[len("libav:"):])
			rval = 1
		} else {
			mtm.mpLoadExtSub(id)
		}
	}

	if !manual {
		return rval
	}

	mtm.userSet = true
	if mtm.canonicalURL == "" {
		return rval
	}
	key := "subtitleTrack"
	if isAudio {
		key = "audioTrack"
	}
	if mtm.mp.KVStore != nil {
		mtm.mp.KVStore.UrlOptSet(mtm.canonicalURL, kvstore.DomainSys, key,
			kvstore.SetString, id)
	}
	return rval
}

// ---------------------------------------------------------------------------
// External subtitle loader — C: subtitle_loader_thread / mp_load_ext_sub /
// ext_sub_dtor
// ---------------------------------------------------------------------------

// extSubDtor — C: ext_sub_dtor — returns the buffer destructor bound to
// the creating MediaSystem's SubtitlesDestroy (mb_data dtor has no mp
// back-pointer; was the MediaHooks global).
func extSubDtor(destroy func(es any)) func(*MediaBuf) {
	return func(mb *MediaBuf) {
		if mb.DataOpaque != nil {
			// C: subtitles_destroy((void *)mb->mb_data)
			if destroy != nil {
				destroy(mb.DataOpaque)
			}
			mb.DataOpaque = nil
		}
	}
}

// subtitlesLoad — C: subtitles_load(mp, url) → ext_subtitles_t* or NULL.
// C calls subtitles_load (ext_subtitles.c) directly; Go goes through
// MediaHooks.SubtitlesLoad, registered by pkg/subtitles/ext's init() — mediacore
// sits below pkg/text in the import DAG and cannot import ext itself.
func subtitlesLoad(mp *MediaPipe, url string) any {
	if url == "" || mp.Sys.SubtitlesLoad == nil {
		return nil
	}
	return mp.Sys.SubtitlesLoad(mp, url, mp.Sys.LangScorer)
}

// subtitleLoaderThread — C: subtitle_loader_thread
func (mtm *MediaTrackMgr) subtitleLoaderThread(mp *MediaPipe) {
	// Delay a short while to make the subtitle list stabilize
	time.Sleep(100 * time.Millisecond) // C: usleep(100000)

	mp.Mutex.Lock()
	for {
		if mp.SubtitleLoaderURL == "" {
			break
		}
		url := mp.SubtitleLoaderURL

		mp.Mutex.Unlock()
		es := subtitlesLoad(mp, url)
		mp.Mutex.Lock()

		if mp.SubtitleLoaderURL == "" || mp.SubtitleLoaderURL != url {
			// What we loaded is no longer relevant, destroy it
			// C: free(url); if(es != NULL) subtitles_destroy(es)
			if es != nil && mp.Sys.SubtitlesDestroy != nil {
				mp.Sys.SubtitlesDestroy(es)
			}
			continue
		}

		if es != nil {
			// If we failed to load, don't do anything special
			mb := MediaBufAllocLocked(mp, 0)
			if mb != nil {
				mb.DataType = int(MBCtrlExtSubtitle)
				mb.DataOpaque = es
				mb.Dtor = extSubDtor(mp.Sys.SubtitlesDestroy)
				MbEnq(mp, mp.Video, mb)
			}
		}
		mp.SubtitleLoaderURL = ""
	}
	mp.SubtitleLoaderRunning = false
	mp.Mutex.Unlock()
	MpRelease(mp)
}

// mpLoadExtSub — C: mp_load_ext_sub. Spawns the detached loader thread.
// Caller holds mp.Mutex (C: called under mp_mutex from
// mp_track_mgr_select_track).
func (mtm *MediaTrackMgr) mpLoadExtSub(url string) {
	mp := mtm.mp
	if !mp.SubtitleLoaderRunning {
		mp.SubtitleLoaderRunning = true
		MpRetain(mp)
		go mtm.subtitleLoaderThread(mp)
	}
	mp.SubtitleLoaderURL = url
}

// ---------------------------------------------------------------------------
// Named-child watchers (C: PROP_TAG_NAME("node", x) + PROP_TAG_NAMED_ROOT)
// Same pattern as PlayQueue.watchNamedChild: parent child-watch + leaf
// value subscription, all callbacks routed through mtm.dispatch.
//
// namedChildSub wraps the pair. C uses ONE prop_sub_t for the whole
// "node","x" path; the Go split creates a second subscription (the leaf)
// that must die together with the parent — otherwise it stays armed on
// the child prop and keeps the mp lockmgr ref forever (leaks mp_refcount,
// leaves mp_satisfied==0 pipes unreleased, stalls media_buffer_hungry).
// ---------------------------------------------------------------------------

type namedChildSub struct {
	mu     sync.Mutex
	parent *propcore.Subscription
	leaf   *propcore.Subscription
}

func (w *namedChildSub) setLeaf(l *propcore.Subscription) {
	w.mu.Lock()
	old := w.leaf
	w.leaf = l
	w.mu.Unlock()
	if old != nil {
		old.Unsubscribe()
	}
}

// Unsubscribe — C: prop_unsubscribe(mt_sub_*) — kills the whole path sub:
// parent watch + armed leaf.
func (w *namedChildSub) Unsubscribe() {
	if w == nil {
		return
	}
	w.mu.Lock()
	p, l := w.parent, w.leaf
	w.parent, w.leaf = nil, nil
	w.mu.Unlock()
	if p != nil {
		p.Unsubscribe()
	}
	if l != nil {
		l.Unsubscribe()
	}
}

func (mtm *MediaTrackMgr) watchNamedChild(node *propcore.Prop, name string,
	cb func(str string, hasValue bool)) *namedChildSub {

	w := &namedChildSub{}
	var leafChild *propcore.Prop

	arm := func(c *propcore.Prop) {
		leafChild = c
		w.setLeaf(c.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
			str, has := propEventString(ev, a)
			mtm.dispatch(func() { cb(str, has) })
		}, nil, mtm.lockArgs()...))
	}

	sub := node.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
		mtm.dispatch(func() {
			var child *propcore.Prop
			if len(a) > 0 {
				child, _ = a[0].(*propcore.Prop)
			}
			switch ev {
			case propcore.EventAddChild, propcore.EventAddChildBefore:
				if child != nil && child.GetName() == name {
					arm(child)
				}
			case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
				for _, x := range a {
					switch vec := x.(type) {
					case []*propcore.Prop:
						for _, c := range vec {
							if c.GetName() == name {
								arm(c)
							}
						}
					case *propcore.PropVec:
						for i := range vec.Len() {
							if c := vec.Get(i); c != nil && c.GetName() == name {
								arm(c)
							}
						}
					}
				}
			case propcore.EventDelChild, propcore.EventDestroyed, propcore.EventSetVoid:
				w.mu.Lock()
				leaf := w.leaf
				w.mu.Unlock()
				if leaf != nil && (ev != propcore.EventDelChild || child == leafChild) {
					w.setLeaf(nil)
					leafChild = nil
					cb("", false)
				} else if leaf == nil && ev != propcore.EventDelChild {
					// Node went void/destroyed with the leaf unresolved
					cb("", false)
				}
			}
		})
	}, nil, mtm.lockArgs()...)
	w.parent = sub

	// C: a named-path subscription delivers PROP_SET_VOID for a leaf that
	// never resolves (child absent). The initial update has already been
	// delivered (possibly enqueued); if the leaf is still unarmed after it
	// drains, feed the void callback once.
	mtm.dispatch(func() {
		w.mu.Lock()
		leaf := w.leaf
		w.mu.Unlock()
		if leaf == nil {
			cb("", false)
		}
	})
	return w
}
func (mtm *MediaTrackMgr) watchNamedChildInt(node *propcore.Prop, name string,
	cb func(v int)) *namedChildSub {

	w := &namedChildSub{}
	var leafChild *propcore.Prop

	arm := func(c *propcore.Prop) {
		leafChild = c
		w.setLeaf(c.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
			v := propEventToInt(ev, a)
			mtm.dispatch(func() { cb(v) })
		}, nil, mtm.lockArgs()...))
	}

	sub := node.Subscribe(func(_ any, ev propcore.EventType, a ...any) {
		mtm.dispatch(func() {
			var child *propcore.Prop
			if len(a) > 0 {
				child, _ = a[0].(*propcore.Prop)
			}
			switch ev {
			case propcore.EventAddChild, propcore.EventAddChildBefore:
				if child != nil && child.GetName() == name {
					arm(child)
				}
			case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
				for _, x := range a {
					switch vec := x.(type) {
					case []*propcore.Prop:
						for _, c := range vec {
							if c.GetName() == name {
								arm(c)
							}
						}
					case *propcore.PropVec:
						for i := range vec.Len() {
							if c := vec.Get(i); c != nil && c.GetName() == name {
								arm(c)
							}
						}
					}
				}
			case propcore.EventDelChild, propcore.EventDestroyed, propcore.EventSetVoid:
				w.mu.Lock()
				leaf := w.leaf
				w.mu.Unlock()
				if leaf != nil && (ev != propcore.EventDelChild || child == leafChild) {
					w.setLeaf(nil)
					leafChild = nil
					cb(0) // C: int callback receives 0 on void
				} else if leaf == nil && ev != propcore.EventDelChild {
					cb(0)
				}
			}
		})
	}, nil, mtm.lockArgs()...)
	w.parent = sub

	// C: unresolved named leaf delivers void → int cb receives 0.
	mtm.dispatch(func() {
		w.mu.Lock()
		leaf := w.leaf
		w.mu.Unlock()
		if leaf == nil {
			cb(0)
		}
	})
	return w
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// propEventString extracts a string value from a prop value event.
// hasValue=false for EventSetVoid / unknown events.
func propEventString(ev propcore.EventType, args []any) (string, bool) {
	switch ev {
	case propcore.EventSetRString, propcore.EventSetCString,
		propcore.EventSetURI:
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				return s, true
			}
		}
		return "", true
	}
	return "", false
}

// propEventToInt extracts an int from a prop value event
// (C: PROP_TAG_CALLBACK_INT). SetVoid/other events yield 0.
func propEventToInt(ev propcore.EventType, args []any) int {
	switch ev {
	case propcore.EventSetInt:
		if len(args) > 0 {
			switch v := args[0].(type) {
			case int:
				return v
			case int64:
				return int(v)
			case int32:
				return int(v)
			}
		}
	case propcore.EventSetFloat:
		if len(args) > 0 {
			if f, ok := args[0].(float64); ok {
				return int(f)
			}
			if f, ok := args[0].(float32); ok {
				return int(f)
			}
		}
	}
	return 0
}

func removeMT(list []*mediaTrack, mt *mediaTrack) []*mediaTrack {
	for i, x := range list {
		if x == mt {
			return slices.Delete(list, i, i+1)
		}
	}
	return list
}

func insertMT(list []*mediaTrack, mt *mediaTrack, pos int) []*mediaTrack {
	if pos >= len(list) {
		return append(list, mt)
	}
	out := make([]*mediaTrack, 0, len(list)+1)
	out = append(out, list[:pos]...)
	out = append(out, mt)
	out = append(out, list[pos:]...)
	return out
}

func insertBeforeMT(list []*mediaTrack, mt, before *mediaTrack) []*mediaTrack {
	for i, x := range list {
		if x == before {
			out := make([]*mediaTrack, 0, len(list)+1)
			out = append(out, list[:i]...)
			out = append(out, mt)
			out = append(out, list[i:]...)
			return out
		}
	}
	return append(list, mt)
}

func nextMT(list []*mediaTrack, mt *mediaTrack) *mediaTrack {
	for i, x := range list {
		if x == mt {
			if i+1 < len(list) {
				return list[i+1]
			}
			return nil
		}
	}
	return nil
}
