package bittorrent

// C: src/backend/bittorrent/torrent.c — canonical 1:1 port.
// Torrent lifecycle, piece scheduling, request management, hash thread.

import (
	"math"
	"slices"
)

// torrentAttemptMorePeers — C: torrent_attempt_more_peers
func (btg *BtGlobal) torrentAttemptMorePeers(to *Torrent) {
	if to.activePeers >= btg.maxPeersTorrent ||
		btg.activePeers >= btg.maxPeersGlobal {
		return
	}

	p := to.inactivePeers.tqFirst
	if p != nil {
		tailqRemove(&to.inactivePeers.tqFirst, &to.inactivePeers.tqLast,
			p, peerQLink)
		btg.peerConnect(p)
		return
	}

	p = to.disconnectedPeers.tqFirst
	if p != nil {
		tailqRemove(&to.disconnectedPeers.tqFirst, &to.disconnectedPeers.tqLast,
			p, peerQLink)
		btg.peerConnect(p)
		return
	}

	p = to.connectFailedPeers.tqFirst
	if p != nil {
		tailqRemove(&to.connectFailedPeers.tqFirst,
			&to.connectFailedPeers.tqLast, p, peerQLink)
		btg.peerConnect(p)
		return
	}
}

// addRequest — C: add_request
func (btg *BtGlobal) addRequest(tb *TorrentBlock, p *Peer, now int64) {
	for tr := p.downloadRequests.lhFirst; tr != nil; tr = tr.peerLinkNext {
		if tr.block == tb {
			return // Request already sent
		}
	}

	tr := &TorrentRequest{}

	tr.piece = uint32(tb.piece.index)
	tr.begin = tb.begin
	tr.length = tb.length

	tr.block = tb

	tr.sendTime = now
	tr.reqNum = tb.reqTally
	tb.reqTally++

	listInsertHead(&tb.requests.lhFirst, tr, trBlockLink)
	btg.peerSendRequest(p, tb)

	tr.peer = p
	tr.qdepth = uint8(p.activeRequests)

	if p.downloadRequests.lhFirst == nil {
		p.torrent.peersWithOutstandingReq++
	}

	listInsertHead(&p.downloadRequests.lhFirst, tr, trPeerLink)
	p.activeRequests++

	p.lastSend = uint64(now)
}

// checkPeerBad — C: check_peer_bad
func (btg *BtGlobal) checkPeerBad(tp *TorrentPiece, p *Peer) bool {
	for pp := tp.peers.lhFirst; pp != nil; pp = pp.ppPieceLinkNext {
		if pp.bad != 0 && pp.peer == p {
			return true
		}
	}
	return false
}

// findOptimalPeer — C: find_optimal_peer
func (btg *BtGlobal) findOptimalPeer(to *Torrent, tp *TorrentPiece) *Peer {
	bestScore := int(math.MaxInt32)
	var best *Peer

	for p := to.unchokedPeers.lhFirst; p != nil; p = p.unchokedLinkNext {

		if p.pieceFlags == nil ||
			p.pieceFlags[tp.index]&PieceHave == 0 {
			continue
		}

		if btg.checkPeerBad(tp, p) {
			continue
		}

		var score int

		if p.blockDelay == 0 {
			// Delay not known yet

			if p.activeRequests != 0 {
				// We have a request already, skip this peer
				continue
			}

			score = 0 // Assume it's super fast
		} else {
			score = p.blockDelay
		}

		if best == nil || score < bestScore {
			best = p
			bestScore = score
		}
	}
	return best
}

// findAnyPeer — C: find_any_peer
func (btg *BtGlobal) findAnyPeer(to *Torrent, tp *TorrentPiece) *Peer {
	for p := to.unchokedPeers.lhFirst; p != nil; p = p.unchokedLinkNext {

		if p.pieceFlags == nil ||
			p.pieceFlags[tp.index]&PieceHave == 0 {
			continue
		}

		if btg.checkPeerBad(tp, p) {
			continue
		}

		if p.activeRequests < p.maxq/2 {
			return p
		}
	}
	return nil
}

// serveWaitingBlocks — C: serve_waiting_blocks
func (btg *BtGlobal) serveWaitingBlocks(to *Torrent, tp *TorrentPiece, optimal bool,
	now int64) {
	for tb := tp.waitingBlocks.lhFirst; tb != nil; {
		next := tb.pieceLinkNext

		if optimal {
			p := btg.findOptimalPeer(to, tp)

			if p == nil || p.activeRequests >= p.maxq {
				break
			}

			btg.addRequest(tb, p, now)

		} else {
			p := btg.findAnyPeer(to, tp)
			if p == nil {
				break
			}

			btg.addRequest(tb, p, now)

		}
		listRemove(tb, tbPieceLink)
		listInsertHead(&tp.sentBlocks.lhFirst, tb, tbPieceLink)
		tb = next
	}
}

// findFasterPeer — C: find_faster_peer
func (btg *BtGlobal) findFasterPeer(to *Torrent, tb *TorrentBlock,
	etaToBeat int64, now int64) *Peer {
	var best *Peer
	tp := tb.piece

	for p := to.unchokedPeers.lhFirst; p != nil; p = p.unchokedLinkNext {

		if p.pieceFlags == nil ||
			p.pieceFlags[tp.index]&PieceHave == 0 {
			continue
		}

		if p.blockDelay == 0 {
			// Delay not known yet
			continue
		}

		if p.activeRequests >= p.maxq {
			continue // Peer is fully queued
		}

		var found *TorrentRequest
		for tr := tb.requests.lhFirst; tr != nil; tr = tr.blockLinkNext {
			if tr.peer == p {
				found = tr
				break
			}
		}
		if found != nil {
			continue
		}

		t := now + int64(p.blockDelay)*2

		if t < etaToBeat {
			etaToBeat = t
			best = p
		}
	}
	return best
}

// checkActiveRequests — C: check_active_requests
func (btg *BtGlobal) checkActiveRequests(to *Torrent, tp *TorrentPiece,
	deadline int64, now int64) {
	for tb := tp.sentBlocks.lhFirst; tb != nil; {
		next := tb.pieceLinkNext
		/*
		 * The most recent request for the block is always first in
		 * this list. It's the only one we will compare with since
		 * we are only gonna add duplicate requests if we think we can
		 * outrun the currently enqueued requests
		 */
		cur := tb.requests.lhFirst
		if cur == nil {
			panic("assertion failed: tb.requests non-empty")
		}

		curpeer := cur.peer
		if curpeer == nil {
			panic("assertion failed: cur.peer non-nil")
		}

		var delay int64

		eta := cur.sendTime + int64(curpeer.blockDelay)
		if eta < now {
			// Didn't arrive on time, assume the delay will worse
			// the longer it takes
			delay = now - eta
			eta += delay * 2
		}

		if eta < deadline {
			tb = next
			continue // Nothing to worry about
		}

		// Now, let's see if we can find a peer that we think can beat
		// the current (offsetted) ETA for this block

		p := btg.findFasterPeer(to, tb, eta, now)
		if p == nil {
			tb = next
			continue
		}

		btg.addRequest(tb, p, now)

		newDelay := now - cur.sendTime
		if newDelay > int64(curpeer.blockDelay) {
			curpeer.blockDelay = (curpeer.blockDelay*7 + int(newDelay)) / 8
		}
		tb = next
	}
}

// torrentSendHave — C: torrent_send_have
func (btg *BtGlobal) torrentSendHave(to *Torrent) {
	for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		if !tp.hashOk {
			continue
		}

		pid := tp.index
		if pid >= to.numPieces {
			panic("assertion failed: pid < to.num_pieces")
		}
		for p := to.runningPeers.lhFirst; p != nil; p = p.runningLinkNext {

			if p.pieceFlags == nil {
				p.pieceFlags = make([]byte, to.numPieces)
			}

			if p.pieceFlags[pid]&PieceNotified != 0 {
				continue
			}
			btg.peerSendHave(p, uint32(pid))
			p.pieceFlags[pid] |= PieceNotified
		}
	}
}

// candidateForUnchoke — C: candidate_for_unchoke
func (btg *BtGlobal) candidateForUnchoke(to *Torrent, p *Peer) int {
	if btg.maxSendSpeed == 0 {
		return 0
	}

	if !p.peerInterested {
		return 0
	}

	if p.numPiecesHave == to.numPieces {
		return 0
	}
	return 1
}

// peerUnchokeSortCmp — C: peer_unchoke_sort_cmp
func (btg *BtGlobal) peerUnchokeSortCmp(a, b *Peer) int {
	if a.bytesReceived != b.bytesReceived {
		if a.bytesReceived < b.bytesReceived {
			return 1
		}
		return -1
	}
	return 0
}

// torrentUnchokePeers — C: torrent_unchoke_peers
func (btg *BtGlobal) torrentUnchokePeers(to *Torrent) {
	numCandidates := 0

	for p := to.runningPeers.lhFirst; p != nil; p = p.runningLinkNext {
		if btg.candidateForUnchoke(to, p) != 0 {
			numCandidates++
		} else {
			btg.peerChoke(p, 1)
		}
	}

	pv := make([]*Peer, 0, numCandidates)
	for p := to.runningPeers.lhFirst; p != nil; p = p.runningLinkNext {
		if btg.candidateForUnchoke(to, p) != 0 {
			pv = append(pv, p)
		}
	}

	slices.SortStableFunc(pv, btg.peerUnchokeSortCmp)

	maxUnchoked := 5

	for _, p := range pv {
		if maxUnchoked != 0 {
			btg.peerChoke(p, 0)
			maxUnchoked--
		} else {
			btg.peerChoke(p, 1)
		}
	}
}

// torrentIoDoRequests — C: torrent_io_do_requests
func (btg *BtGlobal) torrentIoDoRequests(to *Torrent) {
	now := btg.aio.CurrentTime()

	for tp := to.serveOrder.lhFirst; tp != nil; tp = tp.serveLinkNext {
		if tp.deadline == math.MaxInt64 {
			break
		}
		btg.checkActiveRequests(to, tp, tp.deadline, now)
	}

	for tp := to.serveOrder.lhFirst; tp != nil; tp = tp.serveLinkNext {
		btg.serveWaitingBlocks(to, tp, true, now)
	}

	for tp := to.serveOrder.lhFirst; tp != nil; tp = tp.serveLinkNext {
		btg.serveWaitingBlocks(to, tp, false, now)
	}
}
