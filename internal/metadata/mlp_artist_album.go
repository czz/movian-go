package metadata

// Canonical 1:1 port of src/metadata/mlp.c
//
// Lazy-loaded metadata: artist pictures, album art and video info are
// bound to props; a subscription monitor on each bound prop enqueues the
// item on the mlp queue only when an interested (non-monitor) subscriber
// appears. Worker threads (max 4) drain the queue and run the per-class
// load callback.
//
// C file-statics mapped onto the MetadataManager:
//   metadata_mutex          -> mm.mlpMutex
//   metadata_loading_cond   -> mm.mlpCond
//   metadata_num_threads    -> mm.metadataNumThreads
//   mlpqueue                -> mm.mlpQueue

import (
	"unsafe"

	dbpkg "github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// MetadataLazyArtist — C: metadata_lazy_artist_t (mlp.c:230-235)
type MetadataLazyArtist struct {
	MLP    MetadataLazyProp       // mla_mlp
	Artist *misc.Rstr             // mla_artist
	Prop   *propcore.Prop         // mla_prop
	Sub    *propcore.Subscription // mla_sub
}

// mlpArtistLoad — C: mlp_artist_load (mlp.c:242-263). The C body is
// entirely inside #if 0 (lastfm artistinfo no longer gives images), so
// this is intentionally empty.
func mlpArtistLoad(db *dbpkg.DB, mlp *MetadataLazyProp) {
}

// mlpArtistDtor — C: mlp_artist_dtor (mlp.c:269-275)
func mlpArtistDtor(mlp *MetadataLazyProp) {
	mla := (*MetadataLazyArtist)(unsafe.Pointer(mlp))
	misc.RstrRelease(mla.Artist)
	pm := mlp.mm.pm
	if mla.Prop != nil {
		pm.RefDec(mla.Prop)
	}
	if mla.Sub != nil {
		pm.Unsubscribe(mla.Sub)
	}
}

// mlcArtist — C: mlc_artist (mlp.c:281-285)
var mlcArtist = MetadataLazyClass{
	Load: mlpArtistLoad,
	Dtor: mlpArtistDtor,
}

// MetadataBindArtistpics — C: metadata_bind_artistpics (mlp.c:291-306).
// Go boundary takes a plain string; the C signature is (prop, rstr_t*).
func (mm *MetadataManager) MetadataBindArtistpics(prop *propcore.Prop, artist string) {
	mla := &MetadataLazyArtist{}
	mla.MLP = *mlpAlloc(&mlcArtist)
	mla.MLP.mm = mm

	mla.Prop = mm.pm.RefInc(prop)
	mla.Artist = misc.RstrSpn(misc.RstrAllocStr(artist), ";:,-[", 1)

	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY_EXP |
	//       PROP_SUB_SUBSCRIPTION_MONITOR,
	//     PROP_TAG_CALLBACK_USER_INT, mlp_sub_cb, mla,
	//     METADATA_PROP_ARTIST_PICTURES,
	//     PROP_TAG_MUTEX, &metadata_mutex,
	//     PROP_TAG_ROOT, prop, NULL)
	mla.Sub = prop.Subscribe(
		func(opaque any, ev propcore.EventType, args ...any) {
			mm.mlpSubCb(&mla.MLP, ev, args...)
		},
		mla,
		propcore.SubFlagTrackDestroyExp|propcore.SubFlagSubscriptionMonitor,
		propcore.SubUserInt(MetadataPropArtistPictures),
		propcore.SubMutex{Ptr: &mm.mlpMutex},
	)
}

// MetadataLazyAlbum — C: metadata_lazy_album_t (mlp.c:312-318)
type MetadataLazyAlbum struct {
	MLP    MetadataLazyProp       // mla_mlp
	Album  *misc.Rstr             // mla_album
	Artist *misc.Rstr             // mla_artist
	Prop   *propcore.Prop         // mla_prop
	Sub    *propcore.Subscription // mla_sub
}

// mlpAlbumLoad — C: mlp_album_load (mlp.c:325-347).
// Called with metadata_mutex held; releases and re-acquires it.
func mlpAlbumLoad(dbc *dbpkg.DB, mlp *MetadataLazyProp) {
	mla := (*MetadataLazyAlbum)(unsafe.Pointer(mlp))
	mm := mlp.mm

	mm.mlpRetain(mlp)
	mm.mlpMutex.Unlock()

	r := mm.MetadbGetAlbumArt(dbc, misc.RstrGet(mla.Album),
		misc.RstrGet(mla.Artist))

	if r == "" {
		// No album art available in our db, try to get some
		if mm.lastfmLoadAlbumInfo != nil {
			mm.lastfmLoadAlbumInfo(dbc, misc.RstrGet(mla.Album),
				misc.RstrGet(mla.Artist))
		}
		r = mm.MetadbGetAlbumArt(dbc, misc.RstrGet(mla.Album),
			misc.RstrGet(mla.Artist))
	}
	// C: prop_set_rstring(mla->mla_prop, r)
	mm.pm.SetStringEx(mla.Prop, nil, r, propcore.StringUTF8)

	mm.mlpMutex.Lock()
	mm.mlpRelease(mlp)
}

// mlpAlbumDtor — C: mlp_album_dtor (mlp.c:353-358)
func mlpAlbumDtor(mlp *MetadataLazyProp) {
	mla := (*MetadataLazyAlbum)(unsafe.Pointer(mlp))
	misc.RstrRelease(mla.Album)
	misc.RstrRelease(mla.Artist)
	pm := mlp.mm.pm
	if mla.Prop != nil {
		pm.RefDec(mla.Prop)
	}
	if mla.Sub != nil {
		pm.Unsubscribe(mla.Sub)
	}
}

// mlcAlbum — C: mlc_album (mlp.c:364-368)
var mlcAlbum = MetadataLazyClass{
	Load: mlpAlbumLoad,
	Dtor: mlpAlbumDtor,
}

// MetadataBindAlbumart — C: metadata_bind_albumart (mlp.c:374-389).
// C signature is (prop, artist, album); Go boundary takes strings.
func (mm *MetadataManager) MetadataBindAlbumart(prop *propcore.Prop, artist string, album string) {
	mla := &MetadataLazyAlbum{}
	mla.MLP = *mlpAlloc(&mlcAlbum)
	mla.MLP.mm = mm

	mla.Prop = mm.pm.RefInc(prop)
	mla.Artist = misc.RstrSpn(misc.RstrAllocStr(artist), ";:,-[", 1)
	mla.Album = misc.RstrSpn(misc.RstrAllocStr(album), "[(", 1)

	mla.Sub = prop.Subscribe(
		func(opaque any, ev propcore.EventType, args ...any) {
			mm.mlpSubCb(&mla.MLP, ev, args...)
		},
		mla,
		propcore.SubFlagTrackDestroyExp|propcore.SubFlagSubscriptionMonitor,
		propcore.SubUserInt(MetadataPropAlbumArt),
		propcore.SubMutex{Ptr: &mm.mlpMutex},
	)
}
