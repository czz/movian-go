package bittorrent

// C: src/backend/bittorrent/torrent.c — canonical 1:1 port.
// Torrent lifecycle, piece scheduling, request management, hash thread.

import (
	"bytes"
	"crypto/sha1"
	"math"
	"strings"

	archpkg "github.com/czz/movian-go/internal/arch"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/trace"
)

// blockDestroy — C: block_destroy
func (btg *BtGlobal) blockDestroy(tb *TorrentBlock) {
	listRemove(tb, tbPieceLink)
	if tb.requests.lhFirst != nil {
		panic("assertion failed: tb.requests empty")
	}
}

// blockCancelRequests — C: block_cancel_requests
func (btg *BtGlobal) blockCancelRequests(tb *TorrentBlock) {
	for tr := tb.requests.lhFirst; tr != nil; {
		next := tr.blockLinkNext
		if tr.block != tb {
			panic("assertion failed: tr.block == tb")
		}
		listRemove(tr, trBlockLink)
		tr.block = nil

		// If we don't know the response delay yet, then keep the request
		// around just for measurement purposes
		if tr.peer.blockDelay != 0 {
			btg.peerCancelRequest(tr)
		}
		tr = next
	}
}

// addContributor — C: add_contributor
func (btg *BtGlobal) addContributor(tp *TorrentPiece, p *Peer) {
	for pp := tp.peers.lhFirst; pp != nil; pp = pp.ppPieceLinkNext {
		if pp.peer == p {
			return
		}
	}

	pp := &PiecePeer{}
	pp.tp = tp
	pp.peer = p
	listInsertHead(&tp.peers.lhFirst, pp, ppPieceLink)
	listInsertHead(&p.pieces.lhFirst, pp, ppPeerLink)
}

// torrentPiecePeerDestroy — C: torrent_piece_peer_destroy
func (btg *BtGlobal) torrentPiecePeerDestroy(pp *PiecePeer) {
	listRemove(pp, ppPieceLink)
	listRemove(pp, ppPeerLink)
}

// torrentPieceRemoveContributors — C: torrent_piece_remove_contributors
func (btg *BtGlobal) torrentPieceRemoveContributors(tp *TorrentPiece, hashOk int) {
	for pp := tp.peers.lhFirst; pp != nil; {
		next := pp.ppPieceLinkNext
		btg.torrentPiecePeerDestroy(pp)
		pp = next
	}
}

// torrentPieceMarkContributors — C: torrent_piece_mark_contributors
func (btg *BtGlobal) torrentPieceMarkContributors(tp *TorrentPiece) {
	for pp := tp.peers.lhFirst; pp != nil; pp = pp.ppPieceLinkNext {
		pp.bad = 1
	}
}

// torrentReceiveBlock — C: torrent_receive_block
func (btg *BtGlobal) torrentReceiveBlock(tb *TorrentBlock, buf []byte,
	begin int, length int, to *Torrent, p *Peer) {
	second := int(btg.aio.CurrentTime() / 1000000)

	tp := tb.piece

	tp.downloadedBytes += length
	tp.downloadRate.AverageFill(second, int64(tp.downloadedBytes))

	copy(tp.data[begin:], buf[:length])

	btg.addContributor(tp, p)

	// If there are any other requests for this block, cancel them
	btg.blockCancelRequests(tb)
	btg.blockDestroy(tb)

	if tp.waitingBlocks.lhFirst == nil &&
		tp.sentBlocks.lhFirst == nil {

		// Piece complete

		tp.complete = true
		btg.torrentHashWakeup()
	}
	btg.torrentIoDoRequests(to)
}

// tpDeadlineCmp — C: tp_deadline_cmp (returns -1 or 1, never 0)
func (btg *BtGlobal) tpDeadlineCmp(a, b *TorrentPiece) int {
	if a.deadline < b.deadline {
		return -1
	}
	if a.deadline >= b.deadline {
		return 1
	}
	return 0
}

// torrentPieceEnqueueRequests — C: torrent_piece_enqueue_requests
func (btg *BtGlobal) torrentPieceEnqueueRequests(to *Torrent, tp *TorrentPiece) {
	if tp.waitingBlocks.lhFirst != nil {
		panic("assertion failed: tp.waiting_blocks empty")
	}
	if tp.sentBlocks.lhFirst != nil {
		panic("assertion failed: tp.sent_blocks empty")
	}

	for i := 0; i < tp.pieceLength; i += torrentReqSize {
		tb := &TorrentBlock{}
		tb.piece = tp
		listInsertHead(&tp.waitingBlocks.lhFirst, tb, tbPieceLink)
		tb.begin = uint32(i)
		tb.length = uint32(min(torrentReqSize, tp.pieceLength-i))
	}

	to.needUpdatedInterest = true
	btg.aio.WakeupWorker(btg.torrentPendingsSignal)
}

// torrentPieceCreate — C: torrent_piece_create
func (btg *BtGlobal) torrentPieceCreate(to *Torrent, pieceIndex int) *TorrentPiece {
	to.numActivePieces++
	tp := &TorrentPiece{}
	tp.refcount = 1
	tp.index = pieceIndex
	tailqInsertTail(&to.activePieces.tqFirst, &to.activePieces.tqLast,
		tp, tpLink)
	tp.deadline = math.MaxInt64
	listInsertSorted(&to.serveOrder.lhFirst, tp, tpServeLink, btg.tpDeadlineCmp)

	tp.data = make([]byte, to.pieceLength)
	tp.pieceLength = to.pieceLength

	if pieceIndex == to.numPieces-1 {
		// Last piece, truncate piece length
		tp.pieceLength = int(to.totalLength % uint64(to.pieceLength))
	}
	to.activePiecesMem += uint(tp.pieceLength)

	return tp
}

// torrentPieceFind — C: torrent_piece_find
func (btg *BtGlobal) torrentPieceFind(to *Torrent, pieceIndex int) *TorrentPiece {
	for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		if tp.index == pieceIndex {
			tailqRemove(&to.activePieces.tqFirst, &to.activePieces.tqLast,
				tp, tpLink)
			tailqInsertTail(&to.activePieces.tqFirst,
				&to.activePieces.tqLast, tp, tpLink)
			return tp
		}
	}

	tp := btg.torrentPieceCreate(to, pieceIndex)

	if to.cachefilePieceMap[pieceIndex] != -1 {
		// We have this piece on disk, signal that we want to load it
		tp.loadReq = true
		// and wakeup diskio thread
		btg.torrentDiskioWakeup()
		return tp
	}

	btg.torrentPieceEnqueueRequests(to, tp)

	return tp
}

// pieceUpdateDeadline — C: piece_update_deadline
func (btg *BtGlobal) pieceUpdateDeadline(to *Torrent, tp *TorrentPiece) {
	deadline := int64(math.MaxInt64)
	for tfh := tp.activeFh.lhFirst; tfh != nil; tfh = tfh.pieceLinkNext {
		if tfh.deadline < deadline {
			deadline = tfh.deadline
		}
	}

	if tp.deadline == deadline {
		return
	}

	tp.deadline = deadline

	listRemove(tp, tpServeLink)
	listInsertSorted(&to.serveOrder.lhFirst, tp, tpServeLink, btg.tpDeadlineCmp)
}

// torrentPieceRelease — C: torrent_piece_release
func (btg *BtGlobal) torrentPieceRelease(tp *TorrentPiece) {
	tp.refcount--
	if tp.refcount > 0 {
		return
	}

	btg.torrentPieceRemoveContributors(tp, 0)

	tp.data = nil
}

// torrentPieceDestroy — C: torrent_piece_destroy
func (btg *BtGlobal) torrentPieceDestroy(to *Torrent, tp *TorrentPiece) {
	if tp.activeFh.lhFirst != nil {
		panic("assertion failed: tp.active_fh empty")
	}
	if tp.waitingBlocks.lhFirst != nil {
		panic("assertion failed: tp.waiting_blocks empty")
	}
	if tp.sentBlocks.lhFirst != nil {
		panic("assertion failed: tp.sent_blocks empty")
	}
	if tp.sendreqs.lhFirst != nil {
		panic("assertion failed: tp.sendreqs empty")
	}
	to.activePiecesMem -= uint(tp.pieceLength)
	to.numActivePieces--

	tailqRemove(&to.activePieces.tqFirst, &to.activePieces.tqLast,
		tp, tpLink)
	listRemove(tp, tpServeLink)

	btg.torrentPieceRelease(tp)
}

// flushActivePieces — C: flush_active_pieces
func (btg *BtGlobal) flushActivePieces(to *Torrent) {
	for tp := to.activePieces.tqFirst; tp != nil; {
		next := tp.linkNext

		if to.activePiecesMem <= 32*1024*1024 {
			break
		}

		if tp.loadReq {
			tp = next
			continue
		}

		if tp.activeFh.lhFirst != nil {
			tp = next
			continue
		}

		if tp.waitingBlocks.lhFirst != nil {
			tp = next
			continue
		}

		if tp.sentBlocks.lhFirst != nil {
			tp = next
			continue
		}

		if tp.sendreqs.lhFirst != nil &&
			to.activePiecesMem <= 64*1024*1024 {
			tp = next
			continue
		}

		btg.torrentPieceDestroy(to, tp)
		tp = next
	}
}

// torrentReloadCorruptPieces — C: torrent_reload_corrupt_pieces
func (btg *BtGlobal) torrentReloadCorruptPieces(to *Torrent) {
	for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		if tp.hashComputed && !tp.hashOk {

			/**
			 * Setting hash_computed to 0 again basically means that
			 * the piece is not verified so noone will annouce it, etc
			 */
			tp.hashComputed = false

			if tp.onDisk {
				tp.onDisk = false
				/**
				 * This is pretty easy, we just create and enqueue the requests
				 * (we never did this in the first place if we figured we
				 * had the piece on disk
				 */

				btg.torrentTrace(to, "Got corrupt piece %d from disk", tp.index)
			} else {
				btg.torrentTrace(to, "Got corrupt piece %d from network", tp.index)
			}
			btg.torrentPieceEnqueueRequests(to, tp)
		}
	}
}

// torrentSendreqDestroy — C: torrent_sendreq_destroy
func (btg *BtGlobal) torrentSendreqDestroy(ts *TorrentSendreq) {
	p := ts.peer
	tailqRemove(&p.sendreqs.tqFirst, &p.sendreqs.tqLast, ts, tsPeerLink)
	if p.sendreqs.tqFirst == nil {
		to := p.torrent
		tailqRemove(&to.haveSendreqPeers.tqFirst,
			&to.haveSendreqPeers.tqLast, p, peerHaveSendreqLink)
	}

	listRemove(ts, tsPieceLink)
}

// torrentReloadLoadfailPieces — C: torrent_reload_loadfail_pieces
func (btg *BtGlobal) torrentReloadLoadfailPieces(to *Torrent) {
	for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		if tp.loadfail {
			tp.loadfail = false

			for ts := tp.sendreqs.lhFirst; ts != nil; {
				next := ts.pieceLinkNext
				btg.peerSendReject(ts.peer, uint32(tp.index),
					ts.offset, ts.length)
				btg.torrentSendreqDestroy(ts)
				ts = next
			}

			btg.torrentPieceEnqueueRequests(to, tp)
		}
	}
}

// torrentServeSendreqs — C: torrent_serve_sendreqs
func (btg *BtGlobal) torrentServeSendreqs(to *Torrent) {
	cnt := 4
	for {
		p := to.haveSendreqPeers.tqFirst
		if p == nil {
			break
		}
		tailqRemove(&to.haveSendreqPeers.tqFirst,
			&to.haveSendreqPeers.tqLast, p, peerHaveSendreqLink)
		tailqInsertTail(&to.haveSendreqPeers.tqFirst,
			&to.haveSendreqPeers.tqLast, p, peerHaveSendreqLink)

		ts := p.sendreqs.tqFirst
		if ts == nil {
			panic("assertion failed: p.sendreqs non-empty")
		}
		tp := ts.piece

		if !tp.hashComputed {
			break
		}
		if tp.hashOk {
			if to.outputRateTokens < int(ts.length) {
				if !to.outputRateTimer.IsArmed() {
					to.outputRateTimer.Arm(
						to.outputRateRefillTime + 100000)
				}
				break
			}

			to.outputRateTokens -= int(ts.length)
			btg.peerSendPiece(ts.peer, tp, ts.offset, ts.length)
		} else {
			btg.peerSendReject(ts.peer, uint32(tp.index), ts.offset, ts.length)
		}
		btg.torrentSendreqDestroy(ts)
		cnt--
		if cnt == 0 {
			break
		}
	}
}

// torrentOutputTimer — C: torrent_output_timer
func (btg *BtGlobal) torrentOutputTimer(aux any) {
	to := aux.(*Torrent)
	to.outputRateRefillTime = btg.aio.CurrentTime()
	to.outputRateTokens =
		min(to.outputRateTokens+btg.maxSendSpeed,
			btg.maxSendSpeed*2)
	btg.torrentServeSendreqs(to)
}

// torrentCheckPendings — C: torrent_check_pendings
func (btg *BtGlobal) torrentCheckPendings() {
	btg.mu.Lock()

	for to := btg.torrents.lhFirst; to != nil; to = to.linkNext {

		if to.newValidPiece {
			to.newValidPiece = false
			btg.torrentSendHave(to)
			btg.torrentServeSendreqs(to)
		}

		if to.needUpdatedInterest {
			to.needUpdatedInterest = false
			btg.updateInterest(to)
		}

		if to.corruptPiece {
			to.corruptPiece = false
			btg.torrentReloadCorruptPieces(to)
		}

		if to.loadfail {
			to.loadfail = false
			btg.torrentReloadLoadfailPieces(to)
		}

		btg.torrentIoDoRequests(to)
	}
	btg.mu.Unlock()
}

// torrentPeriodicOne — C: torrent_periodic_one
func (btg *BtGlobal) torrentPeriodicOne(to *Torrent, second int) {
	rate := to.downloadRate.AverageRead(second) / 125

	seeders := 0
	leechers := 0

	for tt := to.trackers.lhFirst; tt != nil; tt = tt.torrentLinkNext {
		if int(tt.seeders) > seeders {
			seeders = int(tt.seeders)
		}
		if int(tt.leechers) > leechers {
			leechers = int(tt.leechers)
		}
	}

	for tfh := to.fhs.lhFirst; tfh != nil; tfh = tfh.torrentLinkNext {
		if tfh.faStats != nil {
			tfh.faStats.CreateInt("bitrate", rate)
			tfh.knownPeers.SetInt(to.numPeers)
			tfh.connectedPeers.SetInt(to.activePeers)
			tfh.torrentSeeders.SetInt(seeders)
			tfh.torrentLeechers.SetInt(leechers)
			tfh.recvPeers.SetInt(to.peersWithOutstandingReq)
		}
	}

	btg.flushActivePieces(to)

	if to.lastUnchokeCheck+5 < second {
		to.lastUnchokeCheck = second
		btg.torrentUnchokePeers(to)
	}
	btg.torrentServeSendreqs(to)
}

// torrentPeriodic — C: torrent_periodic
func (btg *BtGlobal) torrentPeriodic(aux any) {
	second := int(btg.aio.CurrentTime() / 1000000)

	btg.mu.Lock()

	for to := btg.torrents.lhFirst; to != nil; {
		next := to.linkNext
		if to.refcount == 0 {
			btg.torrentDestroy(to)
			to = next
			continue
		}

		btg.torrentIoDoRequests(to)
		btg.torrentPeriodicOne(to, second)

		// EXTENSION (no C counterpart): trackerless torrents (magnets
		// without tr= / torrents sans announce) get peer discovery via
		// Mainline DHT get_peers — see dht.go.
		if to.trackers.lhFirst == nil {
			btg.dhtEnsureSearch(to.infoHash[:])
		}
		to = next
	}

	if btg.torrents.lhFirst != nil {
		btg.torrentPeriodicTimer.ArmDeltaSec(1)
	}
	btg.mu.Unlock()
}

// torrentBootPeriodic — C: torrent_boot_periodic
func (btg *BtGlobal) torrentBootPeriodic() {
	btg.mu.Lock()
	defer btg.mu.Unlock()
	if btg.torrents.lhFirst != nil &&
		!btg.torrentPeriodicTimer.IsArmed() {
		btg.torrentPeriodicTimer.ArmDeltaSec(1)
	}
}

// torrentPieceVerifyHash — C: torrent_piece_verify_hash
func (btg *BtGlobal) torrentPieceVerifyHash(to *Torrent, tp *TorrentPiece) {
	btg.torrentRetain(to)
	tp.refcount++

	btg.mu.Unlock()
	ts := archpkg.GetTS()
	digest := sha1.Sum(tp.data[:tp.pieceLength])
	ts = archpkg.GetTS() - ts
	btg.mu.Lock()

	tp.hashComputed = true

	piecehash := to.pieceHashes[tp.index*20:]
	tp.hashOk = bytes.Equal(piecehash[:20], digest[:])

	hashRes := "FAIL"
	if tp.hashOk {
		hashRes = "OK"
	}
	btg.torrentTrace(to, "Hash check on piece %d %s", tp.index, hashRes)

	if tp.hashOk {
		to.newValidPiece = true
		btg.torrentPieceRemoveContributors(tp, 1)
	} else {
		btg.torrentPieceMarkContributors(tp)
		to.corruptPiece = true
		tp.complete = false
	}

	btg.aio.WakeupWorker(btg.torrentPendingsSignal)

	if tp.hashOk && to.cachefile != nil {
		btg.torrentDiskioWakeup()
	}

	btg.torrentPieceRelease(tp)
	btg.torrentRelease(to)

	btg.pieceVerified.Broadcast()
}

// btHashThread — C: bt_hash_thread
func (btg *BtGlobal) btHashThread(aux any) any {
	btg.mu.Lock()

	for {

	restart:
		for to := btg.torrents.lhFirst; to != nil; to = to.linkNext {
			for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
				if tp.complete && !tp.hashComputed {
					btg.torrentPieceVerifyHash(to, tp)
					/**
					 * 'to' may be invalid here because we have unlocked so restart
					 * from begining
					 */
					goto restart
				}
			}
		}

		if btg.condWaitTimeout(btg.pieceHashNeeded, 60000) {
			break
		}
	}

	btg.torrentHashThreadRunning = false
	btg.mu.Unlock()
	return nil
}

// torrentHashWakeup — C: torrent_hash_wakeup
func (btg *BtGlobal) torrentHashWakeup() {
	if !btg.torrentHashThreadRunning {
		btg.torrentHashThreadRunning = true
		archpkg.ThreadCreateDetached("bthasher", btg.btHashThread, nil,
			archpkg.ThreadPrioBgtask)
	}
	btg.pieceHashNeeded.Signal()
}

// torrentCheckMetainfo — C: torrent_check_metainfo
func (btg *BtGlobal) torrentCheckMetainfo() {
	btg.mu.Lock()
	defer btg.mu.Unlock()

	for to := btg.torrents.lhFirst; to != nil; to = to.linkNext {
		for p := to.runningPeers.lhFirst; p != nil; p = p.runningLinkNext {
			for mr := p.metainfoRequests.lhFirst; mr != nil; mr = mr.mrPeerLinkNext {
				if mr.state == MRPendingSend {
					btg.peerSendMetainfoRequest(p, mr)
				}
			}
		}
	}

}

// torrentWakeupForMetadataRequests — C: torrent_wakeup_for_metadata_requests
func (btg *BtGlobal) torrentWakeupForMetadataRequests() {
	btg.aio.WakeupWorker(btg.torrentMetainfoSignal)
}

// torrentEarlyStart — C: torrent_early_init
// INITME(INIT_GROUP_NET, torrent_early_init, NULL, 0)
func (btg *BtGlobal) torrentEarlyStart() {
	btg.maxPeersGlobal = 60
	btg.maxPeersTorrent = 50

	btg.torrentPeriodicTimer.Setup(btg.aio, btg.torrentPeriodic, nil)

	btg.torrentPendingsSignal = btg.aio.AddWorker(btg.torrentCheckPendings)
	btg.torrentBootPeriodicSignal = btg.aio.AddWorker(btg.torrentBootPeriodic)
	btg.torrentMetainfoSignal = btg.aio.AddWorker(btg.torrentCheckMetainfo)
}

// torrentAsyncioStart — C: torrent_asyncio_init
// INITME(INIT_GROUP_ASYNCIO, torrent_asyncio_init, NULL, 0)
func (btg *BtGlobal) torrentAsyncioStart() {
	btg.mu.Lock()
	defer btg.mu.Unlock()
	btg.torrentSettingsStart()
}

// BtSetFAM injects the file access manager (C: implicit global fa
// context used by diskio + torrent_create_from_uri).
func (btg *BtGlobal) BtSetFAM(fam *facore.FileAccessManager) { btg.fam = fam }

// BtSetTraceSystem injects the trace system (C: trace() global).
func (btg *BtGlobal) BtSetTraceSystem(ts *trace.TraceSystem) { btg.ts = ts }

// RegisterBittorrentStart registers the canonical INITME helpers.
// C: INITME(INIT_GROUP_NET, torrent_early_init, NULL, 0)
// C: INITME(INIT_GROUP_ASYNCIO, torrent_asyncio_init, NULL, 0)
// C: INITME(INIT_GROUP_ASYNCIO, tracker_init, NULL, 0)
// C: INITME(INIT_GROUP_ASYNCIO, tracker_udp_init, NULL, 0)
func (btg *BtGlobal) RegisterBittorrentStart(igs *archpkg.InitGroupSystem) {
	igs.RegisterInitHelper(&archpkg.InitHelper{
		Group: archpkg.InitGroupNet,
		Prio:  0,
		Start: btg.torrentEarlyStart,
	})
	igs.RegisterInitHelper(&archpkg.InitHelper{
		Group: archpkg.InitGroupAsyncIO,
		Prio:  0,
		Start: btg.torrentAsyncioStart,
	})
	igs.RegisterInitHelper(&archpkg.InitHelper{
		Group: archpkg.InitGroupAsyncIO,
		Prio:  0,
		Start: btg.trackerStart,
	})
	igs.RegisterInitHelper(&archpkg.InitHelper{
		Group: archpkg.InitGroupAsyncIO,
		Prio:  0,
		Start: btg.trackerUDPStart,
	})
	// EXTENSION (no C counterpart): Mainline DHT init for trackerless
	// torrents — dht.go binds its own UDP socket on the asyncio thread.
	igs.RegisterInitHelper(&archpkg.InitHelper{
		Group: archpkg.InitGroupAsyncIO,
		Prio:  0,
		Start: btg.dhtStart,
	})
	// C: INITME(INIT_GROUP_API, torrent_stats_init, NULL, 0)
	igs.RegisterInitHelper(&archpkg.InitHelper{
		Group: archpkg.InitGroupAPI,
		Prio:  0,
		Start: btg.torrentStatsStart,
	})
}

// mystrbegins — C: mystrbegins(url, prefix).
// Returns (rest, true) when s starts with prefix. C returns a non-NULL
// pointer even when rest is empty — callers must check the bool, not "".
func mystrbegins(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}
