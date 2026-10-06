package metadata

import (
	"fmt"
	"sync"

	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
)

// MetadataStart initializes the metadata system
func (mm *MetadataManager) MetadataStart() {
	mm.mlpCond = sync.NewCond(&mm.mlpMutex)
	mm.mlpQueue = make([]*MetadataLazyProp, 0)
}

// MetadataFini finalizes the metadata system
func (mm *MetadataManager) MetadataFini() {
	mm.mlpMutex.Lock()
	defer mm.mlpMutex.Unlock()

	// Cleanup all queued items
	mm.mlpQueue = nil
}

// MetadataDestroy destroys a metadata structure
func MetadataDestroy(md *Metadata) {
	if md == nil {
		return
	}

	if md.Parent != nil {
		MetadataDestroy(md.Parent)
	}

	md.Title = ""
	md.Album = ""
	md.Artist = ""
	md.Format = ""
	md.Genre = ""
	md.Description = ""
	md.Tagline = ""
	md.IMDBID = ""
	md.Manufacturer = ""
	md.Equipment = ""
	md.ExtID = ""
	md.Redirect = ""

	md.Icons = nil
	md.Backdrops = nil
	md.WideBanners = nil
	md.Thumbs = nil

	md.Streams = nil
	md.Cast = nil
	md.Crew = nil
}

// MetadataAddStream adds a stream to metadata
func MetadataAddStream(md *Metadata, codec string, streamType int, streamIndex int, title string, info string, isolang string, disposition int, trackNum int, channels int) {
	ms := MetadataStream{
		Title:       title,
		Info:        info,
		ISOLang:     isolang,
		Codec:       codec,
		Type:        streamType,
		Disposition: disposition,
		StreamIndex: streamIndex,
		TrackNum:    trackNum,
		Channels:    channels,
	}
	md.Streams = append(md.Streams, ms)
}

// metadataStreamMakeProp — C: metadata_stream_make_prop
// (metadata.c:202-237). Creates a track prop under parent for the
// embedded stream ms.
func metadataStreamMakeProp(pm *propcore.PropManager, ms *MetadataStream,
	parent *propcore.Prop, score, autosel int) {

	// C: snprintf(url, sizeof(url), "libav:%d", ms->ms_streamindex)
	url := fmt.Sprintf("libav:%d", ms.StreamIndex)

	if ms.Disposition&1 != 0 {
		score += 10
	} else {
		score += 5
	}
	if ms.Channels > 2 {
		score++
	}
	if ms.Channels > 0 {
		score++
	}

	// C: ms->ms_title ?: _("Track %d", ms->ms_tracknum)
	title := ms.Title
	if title == "" {
		title = fmt.Sprintf("Track %d", ms.TrackNum)
	}

	// C: mp_add_trackr(parent, title, url, ms->ms_codec, ms->ms_info,
	//   ms->ms_isolang, NULL, _p("Embedded in file"), score, autosel)
	p := mediacore.MpAddTrackR(pm, parent, title, url, ms.Codec, ms.Info,
		ms.ISOLang, "", nls.GetProp("Embedded in file"), score, autosel)
	// C: prop_ref_dec(p)
	if p != nil {
		pm.RefDec(p)
	}
}

// MetadataToProptree — C: metadata_to_proptree (metadata.c:250-336).
func (mm *MetadataManager) MetadataToProptree(md *Metadata, proproot *propcore.Prop, cleanupStreams bool) {
	if md == nil || proproot == nil {
		return
	}
	pm := proproot.Manager()
	if pm == nil {
		return
	}

	setStr := func(name, v string) {
		if c := pm.CreateEx(proproot, name, nil, false, false); c != nil {
			c.SetString(v)
		}
	}
	setInt := func(name string, v int64) {
		if c := pm.CreateEx(proproot, name, nil, false, false); c != nil {
			c.SetInt(int(v))
		}
	}
	setFloat := func(name string, v float32) {
		if c := pm.CreateEx(proproot, name, nil, false, false); c != nil {
			c.SetFloat(v)
		}
	}

	if md.Title != "" {
		setStr("title", md.Title)
	}

	if md.Artist != "" {
		setStr("artist", md.Artist)
		// C: metadata_bind_artistpics(prop_create(proproot, "artist_images"),
		//   md->md_artist)
		mm.MetadataBindArtistpics(
			pm.CreateEx(proproot, "artist_images", nil, false, false),
			md.Artist)
	}

	if len(md.Icons) > 0 {
		setStr("icon", md.Icons[0])
	}

	if md.Album != "" {
		setStr("album", md.Album)
		if md.Artist != "" {
			// C: metadata_bind_albumart(prop_create(proproot, "album_art"),
			//   md->md_artist, md->md_album)
			mm.MetadataBindAlbumart(
				pm.CreateEx(proproot, "album_art", nil, false, false),
				md.Artist, md.Album)
		}
	}

	ac, vc, sc := 0, 0, 0
	for i := range md.Streams {
		ms := &md.Streams[i]
		var p *propcore.Prop
		var pc *int
		score, autosel := 0, 1

		switch mediacore.MediaType(ms.Type) {
		case mediacore.MediaTypeAudio:
			p = pm.CreateEx(proproot, "audiostreams", nil, false, false)
			pc = &ac
		case mediacore.MediaTypeVideo:
			p = pm.CreateEx(proproot, "videostreams", nil, false, false)
			pc = &vc
		case mediacore.MediaTypeSubtitle:
			if mm.subSys != nil {
				score = mm.subSys.SubtitlesEmbeddedScore()
				autosel = mm.subSys.SubtitlesEmbeddedAutosel()
			}
			p = pm.CreateEx(proproot, "subtitlestreams", nil, false, false)
			pc = &sc
		default:
			continue
		}
		if p == nil {
			continue
		}
		// C: if(cleanup_streams && *pc == 0) { prop_destroy_childs(p); *pc = 1; }
		if cleanupStreams && *pc == 0 {
			pm.DestroyChilds(p)
			*pc = 1
		}
		if score == -1 {
			continue
		}
		metadataStreamMakeProp(pm, ms, p, score, autosel)
	}

	if md.Format != "" {
		setStr("format", md.Format)
	}

	if md.Duration != 0 {
		setFloat("duration", md.Duration)
	}

	if md.Tracks != 0 {
		setInt("tracks", int64(md.Tracks))
	}

	if md.Track != 0 {
		setInt("track", int64(md.Track))
	}

	if !md.Time.IsZero() {
		setInt("timestamp", md.Time.Unix())
	}

	if md.Manufacturer != "" {
		setStr("manufacturer", md.Manufacturer)
	}

	if md.Equipment != "" {
		setStr("equipment", md.Equipment)
	}

	if md.Tagline != "" {
		setStr("tagline", md.Tagline)
	}

	if md.Description != "" {
		setStr("description", md.Description)
	}
}
