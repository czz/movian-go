package metadata

import (
	"slices"
	"time"

	"github.com/czz/movian-go/internal/db/kvstore"
	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

const (
	// PlayInfoAudioPlayThreshold is the audio play threshold in microseconds
	PlayInfoAudioPlayThreshold = 10 * 1000000
)

const (
	mipHashWidth = 311
)

// metadbItemProp represents property bindings for a URL
type metadbItemProp struct {
	playcount    *propcore.Prop
	lastplayed   *propcore.Prop
	restartpos   *propcore.Prop
	url          string
	destroySub   *propcore.Subscription
	playcountSub *propcore.Subscription
	refcount     int
}

// metadbItemInfo represents play info for a URL
type metadbItemInfo struct {
	playcount  int
	lastplayed int
	restartpos int64
}

// hashString is a simple string hash function
func hashString(s string) uint32 {
	var h uint32
	for _, c := range s {
		h = h*31 + uint32(c)
	}
	return h
}

// mipGet retrieves play info from kvstore
func (mm *MetadataManager) mipGet(url string) *metadbItemInfo {
	mii := &metadbItemInfo{}

	// Get playcount
	mii.playcount = mm.kvstore.UrlOptGetInt(url, kvstore.DomainSys, "playcount", 0)

	// Get lastplayed
	mii.lastplayed = mm.kvstore.UrlOptGetInt(url, kvstore.DomainSys, "lastplayed", 0)

	// Get restartpos
	mii.restartpos = mm.kvstore.UrlOptGetInt64(url, kvstore.DomainSys, "restartposition", 0)

	return mii
}

// mipSet sets play info to properties
func (mm *MetadataManager) mipSet(mip *metadbItemProp, mii *metadbItemInfo) {
	if mip.playcount != nil {
		mm.pm.SetIntEx(mip.playcount, mip.playcountSub, mii.playcount)
	}
	if mip.lastplayed != nil {
		mip.lastplayed.SetInt(mii.lastplayed)
	}
	if mip.restartpos != nil {
		mip.restartpos.SetFloat(float32(float64(mii.restartpos) / 1000.0))
	}
}

// updateByUrl updates all property bindings for a URL
func (mm *MetadataManager) updateByUrl(url string, dolock bool) {
	mii := mm.mipGet(url)

	hash := hashString(url) % mipHashWidth

	if dolock {
		mm.mipMutex.Lock()
		defer mm.mipMutex.Unlock()
	}

	for _, mip := range mm.mipHash[hash] {
		if mip.url == url {
			mm.mipSet(mip, mii)
		}
	}
}

// mipRelease releases a metadbItemProp
func (mm *MetadataManager) mipRelease(mip *metadbItemProp) {
	mip.refcount--
	if mip.refcount > 0 {
		return
	}

	hash := hashString(mip.url) % mipHashWidth
	// Remove from hash table
	for i, m := range mm.mipHash[hash] {
		if m == mip {
			mm.mipHash[hash] = slices.Delete(mm.mipHash[hash], i, i+1)
			break
		}
	}

	if mip.destroySub != nil {
		mm.pm.Unsubscribe(mip.destroySub)
	}
	if mip.playcountSub != nil {
		mm.pm.Unsubscribe(mip.playcountSub)
	}
	if mip.playcount != nil {
		mm.pm.RefDec(mip.playcount)
	}
	if mip.lastplayed != nil {
		mm.pm.RefDec(mip.lastplayed)
	}
	if mip.restartpos != nil {
		mm.pm.RefDec(mip.restartpos)
	}
}

// PlayInfoRegisterPlay registers a play event for a URL
func (mm *MetadataManager) PlayInfoRegisterPlay(url string, inc int) {
	cur := mm.kvstore.UrlOptGetInt(url, kvstore.DomainSys, "playcount", 0)
	mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "playcount", kvstore.SetInt, cur+inc)

	mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "lastplayed", kvstore.SetInt, int(time.Now().Unix()))

	mm.updateByUrl(url, true)
}

// PlayInfoSetRestartPos sets the restart position for a URL
func (mm *MetadataManager) PlayInfoSetRestartPos(url string, posMs int64, unimportant bool) {
	flags := 0
	if unimportant {
		flags = kvstore.Unimportant
	}

	if posMs <= 0 {
		mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "restartposition", flags|kvstore.SetVoid, 0)
	} else {
		mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "restartposition", flags|kvstore.SetInt64, posMs)
	}

	mm.updateByUrl(url, true)
}

// PlayInfoGetRestartPos — C: playinfo_get_restartpos (playinfo.c:79-103)
func (mm *MetadataManager) PlayInfoGetRestartPos(url string, title string, resumeMode int) int64 {
	if resumeMode == 0 { // VIDEO_RESUME_NO
		return 0
	}

	pos := mm.kvstore.UrlOptGetInt64(url, kvstore.DomainSys, "restartposition", 0)
	if pos == 0 || resumeMode == 1 { // VIDEO_RESUME_YES
		return pos
	}

	// VIDEO_RESUME_ASK: show the resume popup
	p := mm.pm.CreateRoot("") // C: prop_ref_inc(prop_create_root(NULL))
	p.Retain()

	mm.pm.SetVEx(nil, p, "type", "resume")
	mm.pm.SetVEx(nil, p, "title", title)
	mm.pm.SetVEx(nil, p, "position", int(pos/1000))

	var e *event.Event
	if nm := mm.nm; nm != nil {
		e = nm.PopupDisplay(p)
	}

	if e == nil || !e.IsAction(event.ACTION_OK) {
		pos = 0
	}
	if e != nil {
		e.Release()
	}
	mm.pm.Destroy(p)
	p.Release() // C: prop_ref_dec
	return pos
}

// metadbItemPropDestroyed is called when the parent property is destroyed
func (mm *MetadataManager) metadbItemPropDestroyed(opaque any, event propcore.EventType, _ ...any) {
	mip, ok := opaque.(*metadbItemProp)
	if !ok {
		return
	}
	if event == propcore.EventDestroyed {
		mm.mipRelease(mip)
	}
}

// metadbSetPlaycount is called when playcount property is set
func (mm *MetadataManager) metadbSetPlaycount(opaque any, event propcore.EventType, args ...any) {
	mip, ok := opaque.(*metadbItemProp)
	if !ok {
		return
	}
	if event == propcore.EventDestroyed {
		mm.mipRelease(mip)
		return
	}
	if event != propcore.EventSetInt {
		return
	}

	if len(args) > 0 {
		if v, ok := args[0].(int); ok {
			mm.kvstore.UrlOptSet(mip.url, kvstore.DomainSys, "playcount", kvstore.SetInt, v)
			mm.updateByUrl(mip.url, false)
		}
	}
}

// PlayInfoBindURLToProp binds play info properties to a parent property
func (mm *MetadataManager) PlayInfoBindURLToProp(url string, parent *propcore.Prop) {
	mii := mm.mipGet(url)

	mip := &metadbItemProp{
		url:      url,
		refcount: 2, // One per subscription created below
	}

	mm.mipMutex.Lock()

	// Subscribe to parent destruction
	// C: PROP_SUB_TRACK_DESTROY + PROP_TAG_MUTEX(&mip_mutex) — the callback
	// (metadb_item_prop_destroyed → mip_release) is dispatched while holding
	// mip_mutex; mip_release mutates mip_hash without taking it again.
	mip.destroySub = parent.Subscribe(
		func(opaque any, event propcore.EventType, args ...any) {
			mm.metadbItemPropDestroyed(opaque, event, args...)
		},
		mip,
		propcore.SubFlagTrackDestroy,
		propcore.SubMutex{Ptr: &mm.mipMutex},
	)

	// Create child properties
	// C: prop_create_r — prop_create + prop_ref_inc; the caller holds a
	// ref released by mip_release's prop_ref_dec (playinfo.c:205-207).
	// C: prop_create_r — prop_create + prop_ref_inc; the caller holds a
	// ref released by mip_release's prop_ref_dec (playinfo.c:205-207).
	mip.playcount = mm.pm.RefInc(mm.pm.CreateEx(parent, "playcount", nil, false, true))
	mip.lastplayed = mm.pm.RefInc(mm.pm.CreateEx(parent, "lastplayed", nil, false, true))
	mip.restartpos = mm.pm.RefInc(mm.pm.CreateEx(parent, "restartpos", nil, false, true))

	// Subscribe to playcount changes
	// C: PROP_SUB_NO_INITIAL_UPDATE | PROP_SUB_TRACK_DESTROY +
	//   PROP_TAG_MUTEX(&mip_mutex) — metadb_set_playcount calls
	//   update_by_url(url, 0), which iterates mip_hash WITHOUT locking
	//   because the subscription dispatch already holds mip_mutex.
	mip.playcountSub = mip.playcount.Subscribe(
		func(opaque any, event propcore.EventType, args ...any) {
			mm.metadbSetPlaycount(opaque, event, args...)
		},
		mip,
		propcore.SubNoInitialUpdate, propcore.SubFlagTrackDestroy,
		propcore.SubMutex{Ptr: &mm.mipMutex},
	)

	hash := hashString(url) % mipHashWidth
	mm.mipHash[hash] = append(mm.mipHash[hash], mip)
	mm.mipSet(mip, mii)

	mm.mipMutex.Unlock()
}

// PlayInfoMarkURLsAs marks URLs as seen with a specific playcount
func (mm *MetadataManager) PlayInfoMarkURLsAs(urls []string, seen int) {
	for _, url := range urls {
		mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "playcount", kvstore.SetInt, seen)
		mm.updateByUrl(url, true)
	}
}

// PlayInfoEraseURLs erases play info for URLs
func (mm *MetadataManager) PlayInfoEraseURLs(urls []string) {
	for _, url := range urls {
		mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "playcount", kvstore.SetVoid, 0)
		mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "lastplayed", kvstore.SetVoid, 0)
		mm.kvstore.UrlOptSet(url, kvstore.DomainSys, "restartposition", kvstore.SetVoid, 0)
		mm.updateByUrl(url, true)
	}
}
