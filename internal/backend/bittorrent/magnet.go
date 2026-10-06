package bittorrent

// Canonical port of src/backend/bittorrent/magnet.c.
// http://www.bittorrent.org/beps/bep_0009.html

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/trace"
)

// magnetTrace — C: magnet_trace (magnet.c:42-54)
func (btg *BtGlobal) magnetTrace(t *Torrent, format string, args ...any) {
	if !btg.cfg().EnableTorrentDebug.Load() {
		return
	}
	btg.ts.Trace(trace.TRACE_DEBUG, "MAGNET", "%s: %s", t.title,
		fmt.Sprintf(format, args...))
}

// base32decode — C: base32decode (magnet.c:58-87)
func (btg *BtGlobal) base32decode(dst []byte, in string) int {
	bits := 11
	var x uint16
	written := 0
	di := 0
	for i := range len(in) {
		c := in[i]
		var v uint16
		if c >= 'A' && c <= 'Z' {
			v = uint16(c - 'A')
		} else if c >= 'a' && c <= 'z' {
			v = uint16(c - 'a')
		} else if c >= '2' && c <= '7' {
			v = 26 + uint16(c-'2')
		} else {
			return -1
		}

		x |= v << uint(bits)
		if bits <= 8 {
			dst[di] = byte(x >> 8)
			di++
			written++
			if written == 20 {
				return 0
			}
			x <<= 8
			bits += 3
		} else {
			bits -= 5
		}
	}
	return -1
}

// magnetParse — C: magnet_parse (magnet.c:93-145)
func (btg *BtGlobal) magnetParse(list *httpnet.HTTPHeaderList) (*Torrent, error) {
	dn, _ := list.Get("dn")

	xt, ok := list.Get("xt")
	if !ok {
		return nil, errors.New("No 'xt' in magnet link")
	}

	hash, ok := mystrbegins(xt, "urn:btih:")
	if !ok {
		return nil, fmt.Errorf("Unknown hash scheme: %s", xt)
	}

	var infohash [20]byte

	if misc.Hex2bin(infohash[:], len(infohash), hash) != len(infohash) {
		// Try base32
		if btg.base32decode(infohash[:], hash) != 0 {
			return nil, fmt.Errorf("Invalid hash: %s", hash)
		}
	}

	numTrackers := 0
	for _, hh := range list.List {
		if hh.Key == "tr" {
			numTrackers++
		}
	}

	// EXTENSION (no C counterpart): upstream C rejects trackerless
	// magnets here ("Trackerless torrents is not supported") because
	// trackers are its only peer source. With the Mainline DHT this
	// torrent still gets peers — torrentPeriodic runs
	// dhtEnsureSearch() while to_trackers stays empty (dht.go).
	if numTrackers == 0 {
		btg.ts.Trace(trace.TRACE_DEBUG, "MAGNET",
			"Trackerless magnet %s -- peer discovery via DHT", hash)
	}

	dnStr := dn
	if dnStr == "" {
		dnStr = "<unknown name>"
	}
	btg.ts.Trace(trace.TRACE_DEBUG, "MAGNET",
		"Opening magnet for hash %s -- %s", hash, dnStr)

	to := btg.torrentCreateFromHash(infohash[:], "magnet")

	for _, hh := range list.List {
		if hh.Key == "tr" {
			tr := btg.trackerCreate(hh.Value)
			if tr != nil {
				btg.trackerAddTorrent(tr, to)
			}
		}
	}
	return to, nil
}

// magnetDestroyMetainfoRequests — C: magnet_destroy_metainfo_requests
// (magnet.c:150-159)
func (btg *BtGlobal) magnetDestroyMetainfoRequests(list *metainfoRequestList) {
	for {
		mr := list.lhFirst
		if mr == nil {
			break
		}
		listRemove(mr, mrPeerLink)
		listRemove(mr, mrQueryLink)
		// C: free(mr->mr_data); free(mr) — GC
	}
}

// metainfoLoad — C: metainfo_load (magnet.c:166-335)
func (btg *BtGlobal) metainfoLoad(to *Torrent) (*misc.Buf, error) {
	// C: hts_mutex_assert(&bittorrent_mutex)

	// Compute final deadline for getting metadata
	deadline := arch.GetTS() + 120*1000000

	for {
		var r *misc.Buf
		numPieces := 1 // Until we know better
		metainfoSize := 0

		for piece := range numPieces {

			maxActiveRequests := 1
			pieceStart := arch.GetTS()
			var mr *MetainfoRequest
			var p *Peer
			var requests metainfoRequestList

			for {
				// First check if we have a satisfied request

				mr = nil
				for p = to.runningPeers.lhFirst; p != nil; p = p.runningLinkNext {
					for mr = p.metainfoRequests.lhFirst; mr != nil; mr = mr.mrPeerLinkNext {
						if mr.piece == piece && mr.state >= MRReceived {
							break
						}
					}
					if mr != nil {
						break
					}
				}

				if mr != nil {

					p = mr.peer

					if mr.state == MRReceived {

						// Move peer that responded first to front
						listRemove(p, peerRunningLink)
						listInsertHead(&to.runningPeers.lhFirst,
							p, peerRunningLink)

						listRemove(mr, mrPeerLink)
						listRemove(mr, mrQueryLink)
						btg.magnetDestroyMetainfoRequests(&requests)
						break

					} else {

						// C: assert(mr->mr_state == MR_REJECTED)
						// Peer rejected our request, mark it as
						// unable to send metadata
						p.extUtMetadata = 0

						listRemove(mr, mrPeerLink)
						listRemove(mr, mrQueryLink)
						// C: free(mr) — GC
					}
				}

				activeRequests := 0
				for e := requests.lhFirst; e != nil; e = e.mrQueryLinkNext {
					activeRequests++
				}

				for p = to.runningPeers.lhFirst; p != nil; p = p.runningLinkNext {
					if activeRequests >= maxActiveRequests {
						break
					}
					if p.extUtMetadata == 0 {
						continue
					}
					for mr = p.metainfoRequests.lhFirst; mr != nil; mr = mr.mrPeerLinkNext {
						if mr.piece == piece {
							break
						}
					}
					if mr != nil {
						continue
					}

					mr = &MetainfoRequest{}
					mr.state = MRPendingSend
					mr.peer = p
					mr.piece = piece
					listInsertHead(&p.metainfoRequests.lhFirst, mr, mrPeerLink)
					listInsertHead(&requests.lhFirst, mr, mrQueryLink)
					activeRequests++
				}
				btg.torrentWakeupForMetadataRequests()

				btg.condWaitTimeout(btg.metainfoAvailable, 1000)

				now := arch.GetTS()

				if now > deadline {
					if r != nil {
						r.Release()
					}
					btg.magnetDestroyMetainfoRequests(&requests)
					return nil, fmt.Errorf(
						"Timeout waiting for metadata piece %d/%d",
						piece, numPieces)
				}

				elapsed := now - pieceStart

				maxActiveRequests = min(int(elapsed/1000000), 5)
			}

			btg.magnetTrace(to, "Got metadata piece %d/%d (%d bytes) from peer %s",
				piece, numPieces, mr.size, mr.peer.name)

			if piece == 0 {
				metainfoSize = mr.totalSize

				btg.magnetTrace(to, "Got first piece claiming total size %d",
					metainfoSize)

				if r != nil {
					r.Release()
				}

				r = misc.BufCreate(metainfoSize)
				numPieces = (metainfoSize + 16383) / 16384
			}

			var badSize bool

			if piece == numPieces-1 {
				badSize = mr.size != (metainfoSize & 16383)
			} else {
				badSize = mr.size != 16384
			}

			if badSize {
				btg.magnetTrace(to, "Got bad metadata piece size (%d/%d) is %d bytes",
					piece, numPieces, mr.size)
				// C: free(mr->mr_data); free(mr)
				r.Release()
				r = nil
				break
			}

			copy(r.C8()[piece*16384:], mr.data[:mr.size])
			// C: free(mr->mr_data); free(mr) — GC
		}

		if r == nil {
			continue
		}

		// C: sha1_init/update/final over buf_data(r)
		digest := sha1.Sum(r.C8()[:r.Len()])

		if bytes.Equal(digest[:], to.infoHash[:]) {
			btg.magnetTrace(to, "Downloaded metainfo hash verified OK")
			return r, nil
		}

		btg.magnetTrace(to, "Downloaded metainfo hash failed, retrying")
		r.Release()
	}
}

// createTorrentDoc — C: create_torrent_doc (magnet.c:344-361).
// This function creates a so called 'torrentdoc' (basically what's
// bencoded into a .torrent file). That's currently just the list
// of trackers + metadata info dict
func (btg *BtGlobal) createTorrentDoc(to *Torrent, info *htsmsg.HTSMsg) *htsmsg.HTSMsg {
	torrentdoc := htsmsg.NewMap()

	al := htsmsg.NewList()
	al2 := htsmsg.NewList()

	for tt := to.trackers.lhFirst; tt != nil; tt = tt.torrentLinkNext {
		al2.AddStr("", tt.tracker.url)
	}

	al.AddMsg("", al2)
	torrentdoc.AddMsg("announce-list", al)
	torrentdoc.AddMsg("info", info)

	return torrentdoc
}

// magnetOpen — C: magnet_open (magnet.c:368-426)
func (btg *BtGlobal) magnetOpen(url0 string) (*Torrent, error) {
	if len(url0) > 0 && url0[0] == '?' {
		url0 = url0[1:]
	}

	url := url0 // C: mystrdupa — Go strings are immutable

	var list httpnet.HTTPHeaderList

	list.ParseURIArgs(url, true)

	to, err := btg.magnetParse(&list)
	list.Free()
	if to == nil {
		return nil, err
	}

	if to.metainfo != nil {
		return to, nil
	}

	btg.torrentRetain(to)

	for to.loadingMetadata {
		btg.metainfoAvailable.Wait()
	}

	if to.metainfo != nil {
		btg.torrentRelease(to)
		return to, nil
	}

	to.loadingMetadata = true

	b, lerr := btg.metainfoLoad(to)

	btg.torrentRelease(to)

	to.loadingMetadata = false
	btg.metainfoAvailable.Broadcast()

	if b == nil {
		return nil, lerr
	}

	doc, _, derr := BencodeDeserialize(b.C8(), nil, nil)
	b.Release()
	if doc == nil {
		return nil, derr
	}

	if perr := btg.torrentParseInfodict(to, doc); perr != nil {
		doc.Release()
		return nil, perr
	}

	torrentdoc := btg.createTorrentDoc(to, doc) // Gets ownership of 'doc'

	to.metainfo = BencodeSerialize(torrentdoc)
	torrentdoc.Release()

	btg.torrentDiskioOpen(to)
	btg.peerActivatePendingData(to)
	return to, nil
}
