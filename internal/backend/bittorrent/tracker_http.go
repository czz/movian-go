package bittorrent

// Canonical port of src/backend/bittorrent/tracker_http.c.

import (
	"encoding/binary"

	facore "github.com/czz/movian-go/internal/fileaccess"
	netcore "github.com/czz/movian-go/internal/networking/core"
	"github.com/czz/movian-go/internal/trace"
)

// trackerHTTPTorrentDestroy — C: tracker_http_torrent_destroy
// (tracker_http.c:37-42)
func (btg *BtGlobal) trackerHTTPTorrentDestroy(tt *TrackerTorrent) {
	if tt.httpReq != nil {
		tt.httpReq.AsyncioHTTPCancel()
	}
	// C: free(tt->tt_trackerid) — GC
}

// httpCallback — C: http_callback (tracker_http.c:49-136).
// Runs on the asyncio thread.
func (btg *BtGlobal) httpCallback(req *facore.HTTPReqAux, opaque any) {

	tt := opaque.(*TrackerTorrent)
	to := tt.torrent

	// C: assert(tt->tt_http_req != NULL)
	tt.httpReq = nil

	b := facore.HTTPReqGetResult(req)

	tt.interval = min(tt.interval*2, 3600)

	if b != nil {
		msg, _, _ := BencodeDeserialize(b.C8(), nil, nil)
		if msg != nil {

			if btg.cfg().EnableTorrentTrackerDebug.Load() {
				btg.ts.Trace(trace.TRACE_DEBUG, "TRACKER",
					"%s: Decoded response:", tt.tracker.url)
				msg.Print("TRACKER")
			}

			errStr, hasErr := "", false
			if f := msg.FieldFind("failure reason"); f != nil {
				errStr, hasErr = f.FieldGetString()
			}
			if hasErr {
				btg.trackerTrace(tt.tracker, "%s for %s", errStr, to.title)
				goto done
			}

			if f := msg.FieldFind("trackerid"); f != nil {
				if trackerid, ok := f.FieldGetString(); ok {
					tt.trackerid = trackerid // C: mystrset
				}
			}

			tt.seeders = msg.GetU32OrDefault("complete", 0)
			tt.leechers = msg.GetU32OrDefault("incomplete", 0)

			tt.interval = msg.GetU32OrDefault("min interval",
				msg.GetU32OrDefault("interval", 1800))

			peers := msg.GetList("peers")
			if peers != nil {
				for _, f := range peers.GetFields() {
					sub := f.GetMap()
					if sub == nil {
						continue
					}
					ip := sub.GetStr("ip")
					if ip == "" {
						continue
					}

					nap, rerr := netcore.NetResolveNumeric(ip)
					if rerr != nil {
						continue
					}
					na := *nap

					na.Port = uint16(sub.GetU32OrDefault("port", 0))
					if na.Port == 0 {
						continue
					}
					btg.peerAdd(to, &na)
				}
			}

			if data, gerr := msg.GetBin("peers"); gerr == nil {
				var na netcore.NetAddr
				na.Family = 4
				for len(data) >= 6 {
					copy(na.Addr[:], data[:4])
					na.Port = binary.BigEndian.Uint16(data[4:])
					if na.Port > 0 {
						btg.peerAdd(to, &na)
					}
					data = data[6:]
				}
			}
			msg.Release()
		}
	}
done:
	tt.timer.ArmDeltaSec(int(tt.interval))
}

// trackerHTTPTorrentAnnounce — C: tracker_http_torrent_announce
// (tracker_http.c:143-180)
func (btg *BtGlobal) trackerHTTPTorrentAnnounce(tt *TrackerTorrent, event int) {
	t := tt.tracker
	to := tt.torrent
	var eventstr string
	flags := 0

	if tt.tentative != 0 {
		eventstr = "started"
	} else if event == 3 {
		eventstr = "stopped"
	}

	tt.tentative = 0

	if tt.httpReq != nil {
		tt.httpReq.AsyncioHTTPCancel()
	}

	if btg.cfg().EnableTorrentTrackerDebug.Load() {
		flags = facore.FaDebug
	}

	var left uint64 = 16384
	if to.totalLength != 0 {
		left = to.totalLength
	}

	tt.httpReq = btg.fam.NewAsyncioHTTPReq(t.url, btg.httpCallback, tt,
		facore.HTTPTagArgBin, "info_hash", to.infoHash[:],
		facore.HTTPTagArgBin, "peer_id", btg.peerID[:20],
		facore.HTTPTagArgInt, "port", 7898,
		facore.HTTPTagArgInt, "compact", 1,
		facore.HTTPTagArgInt64, "uploaded", int64(to.uploadedBytes),
		facore.HTTPTagArgInt64, "downloaded", int64(to.downloadedBytes),
		facore.HTTPTagArgInt64, "left", int64(left),
		facore.HTTPTagArg, "event", eventstr,
		facore.HTTPTagArg, "trackerid", tt.trackerid,
		facore.HTTPTagResultPtr, facore.HTTPBufferInternally,
		facore.HTTPTagFlags, flags,
	)
}

// trackerHTTPCreate — C: tracker_http_create (tracker_http.c:188-194)
func (btg *BtGlobal) trackerHTTPCreate() *Tracker {
	t := &Tracker{}
	t.announce = btg.trackerHTTPTorrentAnnounce
	t.destroy = btg.trackerHTTPTorrentDestroy
	return t
}
