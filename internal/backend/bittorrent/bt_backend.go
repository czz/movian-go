package bittorrent

// Canonical port of src/backend/bittorrent/bt_backend.c.
// http://www.bittorrent.org/beps/bep_0009.htm

import (
	"errors"
	"fmt"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	navigator "github.com/czz/movian-go/internal/navigator"
	propcore "github.com/czz/movian-go/internal/prop"
)

// injected at backend registration.

// torrentCreateFromURI — C: torrent_create_from_uri (bt_backend.c:43-60)
func (btg *BtGlobal) torrentCreateFromURI(url string) (*Torrent, error) {
	if magnet, ok := mystrbegins(url, "magnet:"); ok {
		return btg.magnetOpen(magnet)
	}

	btg.mu.Unlock()
	b, lerr := facore.FALoad(btg.fam, url,
		nil, nil, 0)
	btg.mu.Lock()

	if b == nil {
		return nil, lerr
	}

	// C: buf_t *b — FALoad returns *facore.Buffer; wrap into *misc.Buf
	// for torrent_create_from_infofile.
	buf := misc.BufCreateAndCopy(len(b.Data), b.Data)
	to, err := btg.torrentCreateFromInfofile(buf)
	buf.Release()
	return to, err
}

// torrentOpenURL — C: torrent_open_url (bt_backend.c:67-101).
// urlp is updated to the unconsumed remainder ("" = fully consumed,
// matching C's NULL store).
func (btg *BtGlobal) torrentOpenURL(urlp *string) (*Torrent, error) {
	var infohash [20]byte
	var to *Torrent
	var err error
	url := *urlp

	btg.mu.Lock()

	if misc.Hex2binl(infohash[:], 20, url, 40) == 20 &&
		(len(url) == 40 || (len(url) > 40 && url[40] == '/')) {
		to = btg.torrentCreateFromHash(infohash[:], "URL")

		if len(url) == 40 {
			*urlp = ""
		} else {
			url = url[40:]
			if url == "" || url == "/" {
				*urlp = ""
			} else {
				*urlp = url[1:]
			}
		}
	} else {

		*urlp = ""

		u := url

		n := len(u)
		for n > 0 && u[n-1] == '/' {
			u = u[:n-1]
			n--
		}

		to, err = btg.torrentCreateFromURI(u)
	}
	return to, err
}

// doTorrentRelease — C: do_torrent_release (bt_backend.c:108-112)
func (btg *BtGlobal) doTorrentRelease(opaque any, et propcore.EventType,
	args ...any) {
	if et == propcore.EventDestroyed {
		btg.torrentRelease(opaque.(*Torrent))
	}
}

// torrentReleaseOnPropDestroy — C: torrent_release_on_prop_destroy
// (bt_backend.c:119-127). C: PROP_SUB_TRACK_DESTROY +
// PROP_TAG_CALLBACK_DESTROYED → Go SubFlagTrackDestroy (the subscription
// is auto-removed before EventDestroyed fires, covering C's
// prop_unsubscribe(s)); PROP_TAG_MUTEX is managed by the prop layer.
func (btg *BtGlobal) torrentReleaseOnPropDestroy(p *propcore.Prop, to *Torrent) {
	btg.torrentRetain(to)
	p.Subscribe(btg.doTorrentRelease, to, propcore.SubFlagTrackDestroy)
}

// torrentBrowseOpen — C: torrent_browse_open (bt_backend.c:134-168)
func (btg *BtGlobal) torrentBrowseOpen(page *propcore.Prop, url string, sync bool) int {
	model := btg.propManager.RefInc(btg.propManager.CreateEx(page, "model", nil, false, false))
	model.CreateInt("loading", 1)

	// C: usage_page_open(sync, "Torrent browse")
	btg.backendSystem.Usage().PageOpen(sync, "Torrent browse")

	to, terr := btg.torrentOpenURL(&url)
	if to == nil {
		navigator.OpenErrorf(btg.propManager, page,
			"Unable to open torrent: %s", terr)
	} else {

		var hashstr [41]byte

		btg.torrentReleaseOnPropDestroy(page, to)
		model.CreateInt("loading", 0)

		misc.Bin2hex(hashstr[:], len(hashstr), to.infoHash[:], 20)
		hashstr[40] = 0

		redir := fmt.Sprintf("torrentfile://%s/", string(hashstr[:40]))
		var e *event.EventPayload
		if btg.eventManager != nil {
			e = btg.eventManager.CreateStr(event.EVENT_REDIRECT, redir)
		}

		sink := btg.propManager.RefInc(btg.propManager.CreateEx(page, "eventSink", nil, false, false))
		if e != nil {
			btg.propManager.SendExtEvent(sink, e)
		}
		btg.propManager.RefDec(sink)
		if e != nil {
			e.Release()
		}
	}
	btg.mu.Unlock()
	btg.propManager.RefDec(model)
	return 0
}

// findMovieTorrent — C: find_movie_torrent (bt_backend.c:175-185).
// Find biggest file and use that as movie source
func (btg *BtGlobal) findMovieTorrent(to *Torrent) *TorrentFile {
	var best *TorrentFile
	for tf := to.files.tqFirst; tf != nil; tf = tf.torrentLinkNext {
		if best == nil || best.size < tf.size {
			best = tf
		}
	}
	return best
}

// torrentMovieOpen — C: torrent_movie_open (bt_backend.c:191-255)
func (btg *BtGlobal) torrentMovieOpen(page *propcore.Prop, url0 string, sync bool) int {
	// C: usage_page_open(sync, "Torrent movie")
	btg.backendSystem.Usage().PageOpen(sync, "Torrent movie")

	m := btg.propManager.RefInc(btg.propManager.CreateEx(page, "model", nil, false, false))

	m.CreateInt("loading", 1)

	btg.mu.Lock()

	to, terr := btg.torrentCreateFromURI(url0)

	if to == nil {
		btg.mu.Unlock()
		btg.propManager.RefDec(m)
		// C: return nav_open_errorf(page, ...)
		if navigator.OpenErrorf(btg.propManager, page,
			"Unable to open torrent: %s", terr) != nil {
			return -1
		}
		return 0
	}

	best := btg.findMovieTorrent(to)

	if best == nil {
		btg.mu.Unlock()
		btg.propManager.RefDec(m)
		if navigator.OpenErrorf(btg.propManager, page, "No files in torrent") != nil {
			return -1
		}
		return 0
	}

	var hashstr [41]byte
	misc.Bin2hex(hashstr[:], len(hashstr), to.infoHash[:], 20)
	hashstr[40] = 0

	// Create videoparams message

	vp := htsmsg.NewMap()

	vp.AddStr("title", to.title)

	url := fmt.Sprintf("torrentfile://%s/%s",
		string(hashstr[:40]), best.fullpath)
	vp.AddStr("canonicalUrl", url)

	src := htsmsg.NewMap()
	src.AddStr("url", url)

	sources := htsmsg.NewList()
	sources.AddMsg("", src)
	vp.AddMsg("sources", sources)

	page.CreateInt("directClose", 1)

	rstr, _ := htsmsg.SerializeJSONToRstr(vp, "videoparams:")
	// C: prop_set(page, "source", PROP_ADOPT_RSTRING, rstr)
	source := btg.propManager.CreateEx(page, "source", nil, false, false)
	btg.propManager.SetStringEx(source, nil, rstr, propcore.StringUTF8)

	m.CreateString("type", "video")
	btg.propManager.RefDec(m)

	btg.torrentReleaseOnPropDestroy(page, to)
	btg.mu.Unlock()

	return 0
}

// btOpen — C: bt_open (bt_backend.c:262-275)
func (btg *BtGlobal) btOpen(page *propcore.Prop, url string, sync bool) int {
	if u, ok := mystrbegins(url, "torrent:video:"); ok {
		return btg.torrentMovieOpen(page, u, sync)
	} else if u, ok := mystrbegins(url, "torrent:browse:"); ok {
		return btg.torrentBrowseOpen(page, u, sync)
	} else if _, ok := mystrbegins(url, "magnet:"); ok {
		return btg.torrentBrowseOpen(page, url, sync)
	}

	return 0
}

// btCanHandle — C: bt_canhandle (bt_backend.c:282-285)
func (btg *BtGlobal) btCanHandle(url string) int {
	_, ok1 := mystrbegins(url, "torrent:")
	_, ok2 := mystrbegins(url, "magnet:")
	if ok1 || ok2 {
		return 1
	}
	return 0
}

// btPlayvideo — C: bt_playvideo (bt_backend.c:289-338)
func (btg *BtGlobal) btPlayvideo(url string, mp any,
	vq, vsl any, va0 *backendcore.VideoArgs) (any, error) {
	u, ok := mystrbegins(url, "torrent:video:")
	if !ok {
		return nil, errors.New("Invalid torrent link")
	}

	btg.mu.Lock()
	to, terr := btg.torrentCreateFromURI(u)
	if to == nil {
		btg.mu.Unlock()
		return nil, terr
	}

	best := btg.findMovieTorrent(to)

	if best == nil {
		btg.mu.Unlock()
		return nil, errors.New("No files in torrent")
	}

	var hashstr [41]byte
	misc.Bin2hex(hashstr[:], len(hashstr), to.infoHash[:], 20)
	hashstr[40] = 0

	newurl := fmt.Sprintf("torrentfile://%s/%s",
		string(hashstr[:40]), best.fullpath)

	btg.torrentRetain(to)
	btg.mu.Unlock()

	va := *va0
	va.CanonicalURL = newurl

	var e any
	var perr error
	if btg.backendSystem != nil {
		e, perr = btg.backendSystem.PlayVideo(newurl, mp, vq, vsl, &va)
	}

	btg.mu.Lock()
	btg.torrentRelease(to)
	btg.mu.Unlock()
	return e, perr
}

// RegisterBittorrentBackend — C: backend_t be_bittorrent (bt_backend.c:344-348)
// + BE_REGISTER(bittorrent). Also registers the "torrentfile" fa protocol
// (FAP_REGISTER in fa_torrent.c) and stores the injected global seams.
func (btg *BtGlobal) RegisterBittorrentBackend(bs *backendcore.BackendSystem,
	pm *propcore.PropManager, em *event.EventManager) {
	btg.propManager = pm
	btg.eventManager = em
	btg.backendSystem = bs

	be := &backendcore.Backend{}
	be.CanHandle = btg.btCanHandle
	be.Open = func(page any, url string, sync bool) error {
		p, _ := page.(*propcore.Prop)
		btg.btOpen(p, url, sync)
		return nil
	}
	be.PlayVideo = btg.btPlayvideo
	bs.Register(be)

	btg.faTorrentRegister()
}
