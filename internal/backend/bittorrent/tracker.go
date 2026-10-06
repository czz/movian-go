package bittorrent

// Canonical port of src/backend/bittorrent/tracker.c.

import (
	"fmt"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/asyncio"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

// C: static int tracker_new_torrent_signal — now a BtGlobal field.

// C: struct tracker_list trackers; — defined in bittorrent_h.go

// trackerTrace — C: tracker_trace (tracker.c:37-49)
func (btg *BtGlobal) trackerTrace(t *Tracker, format string, args ...any) {
	if !btg.cfg().EnableTorrentTrackerDebug.Load() {
		return
	}
	btg.ts.Trace(trace.TRACE_DEBUG, "TRACKER", "%s: %s", t.url,
		fmt.Sprintf(format, args...))
}

// trackerDestroy — C: tracker_destroy (tracker.c:56-66)
func (btg *BtGlobal) trackerDestroy(t *Tracker) {
	btg.trackerTrace(t, "Destroyed")
	if t.adr != nil {
		asyncio.DNSCancel(t.adr)
	}
	t.timer.Disarm()
	listRemove(t, tLink)
	// C: free(t->t_url); free(t) — GC
}

// trackerTorrentDestroy — C: tracker_torrent_destroy (tracker.c:73-89)
func (btg *BtGlobal) trackerTorrentDestroy(tt *TrackerTorrent) {
	if tt.tracker.destroy != nil {
		tt.tracker.destroy(tt)
	}

	tt.timer.Disarm()

	if tt.torrent != nil {
		listRemove(tt, ttTorrentLink)
	}

	listRemove(tt, ttTrackerLink)
	t := tt.tracker
	if t.torrents.lhFirst == nil {
		btg.trackerDestroy(t)
	}
	// C: free(tt) — GC
}

// trackerRemoveTorrent — C: tracker_remove_torrent (tracker.c:98-112)
func (btg *BtGlobal) trackerRemoveTorrent(to *Torrent) {
	for {
		tt := to.trackers.lhFirst
		if tt == nil {
			break
		}

		if tt.tracker.state == TrackerStateConnected {
			tt.tracker.announce(tt, 3)
			listRemove(tt, ttTorrentLink)
			tt.torrent = nil
		} else {
			btg.trackerTorrentDestroy(tt)
		}
	}
}

// trackerCreate — C: tracker_create (tracker.c:119-153)
func (btg *BtGlobal) trackerCreate(url string) *Tracker {
	// C: hts_mutex_assert(&bittorrent_mutex)

	for t := btg.trackers.lhFirst; t != nil; t = t.tLinkNext {
		if t.url == url {
			return t
		}
	}

	var protostr [16]byte
	var hostname [128]byte
	port := -1

	misc.UrlSplit(protostr[:], len(protostr), nil, 0,
		hostname[:], len(hostname),
		&port, nil, 0, url)

	proto := misc.CStr(protostr[:])
	host := misc.CStr(hostname[:])

	var t *Tracker
	switch proto {
	case "udp":
		if port == -1 {
			port = 6969
		}
		t = btg.trackerUDPCreate(host, port)
	case "http", "https":
		t = btg.trackerHTTPCreate()
	default:
		return nil
	}

	t.url = url // C: strdup
	listInsertHead(&btg.trackers.lhFirst, t, tLink)
	btg.trackerTrace(t, "New tracker added")
	return t
}

// trackerTorrentPeriodic — C: tracker_torrent_periodic (tracker.c:163-179).
// This timer callback is used either to reannounce periodically
// or to un-announce (remove us) from tracker.
// The latter is indicated when tt->tt_torrent no longer is pointed to
func (btg *BtGlobal) trackerTorrentPeriodic(aux any) {
	tt := aux.(*TrackerTorrent)

	if tt.torrent == nil {
		tt.attempt++
		if tt.attempt == 5 {
			btg.trackerTorrentDestroy(tt)
		} else {
			// Resend stop request
			tt.timer.ArmDeltaSec(5)
		}
		return
	}

	tt.tracker.announce(tt, 2)
}

// trackerAddTorrent — C: tracker_add_torrent (tracker.c:186-197)
func (btg *BtGlobal) trackerAddTorrent(tr *Tracker, to *Torrent) {
	tt := &TrackerTorrent{}
	tt.interval = 15
	tt.tracker = tr
	tt.torrent = to
	tt.tentative = 1
	listInsertHead(&to.trackers.lhFirst, tt, ttTorrentLink)
	listInsertHead(&tr.torrents.lhFirst, tt, ttTrackerLink)
	tt.timer.Setup(btg.aio, btg.trackerTorrentPeriodic, tt)
	btg.aio.WakeupWorker(btg.trackerNewTorrentSignal)
}

// trackerNewTorrent — C: tracker_new_torrent (tracker.c:205-214).
// Runs on the asyncio thread.
func (btg *BtGlobal) trackerNewTorrent() {
	for t := btg.trackers.lhFirst; t != nil; t = t.tLinkNext {
		for tt := t.torrents.lhFirst; tt != nil; tt = tt.trackerLinkNext {
			if tt.tentative != 0 {
				t.announce(tt, 2)
			}
		}
	}
}

// trackerStart — C: tracker_init (tracker.c:235-244),
// INITME(INIT_GROUP_ASYNCIO, tracker_init, NULL, 0).
// Runs on the asyncio thread (registered via torrentAsyncioStart).
func (btg *BtGlobal) trackerStart() {
	x := uint32(arch.GetTS())
	const chars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_."
	for i := range 20 {
		x = x*1664525 + 1013904223
		btg.peerID[i] = chars[x&0x3f]
	}

	btg.trackerNewTorrentSignal = btg.aio.AddWorker(btg.trackerNewTorrent)
}
