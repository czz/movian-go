package bittorrent

// C: src/backend/bittorrent/peer.c — canonical 1:1 port.
// Peer wire protocol, handshake, message dispatch, request management.

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/asyncio"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/version"
)

// C: BT_MSGID_* (peer.c:36-49)
const (
	btMsgidChoke         = 0x0
	btMsgidUnchoke       = 0x1
	btMsgidInterested    = 0x2
	btMsgidNotInterested = 0x3
	btMsgidHave          = 0x4
	btMsgidBitfield      = 0x5
	btMsgidRequest       = 0x6
	btMsgidPiece         = 0x7
	btMsgidCancel        = 0x8
	btMsgidPort          = 0x9
	btMsgidHaveAll       = 0xe
	btMsgidHaveNone      = 0xf
	btMsgidReject        = 0x10
	btMsgidAllowedFast   = 0x11
	btMsgidExtension     = 0x14
)

// C: EXTENSION_MSGID_* (peer.c:51-52)
const (
	extensionMsgidHandshake = 0
	extensionMsgidMetadata  = 2
)

// C: PEER_DBG_* (peer.c:68-70)
const (
	peerDbgConn     = 0x1
	peerDbgDownload = 0x2
	peerDbgUpload   = 0x4
)

// peerTrace — C: peer_trace
func (btg *BtGlobal) peerTrace(p *Peer, typ int, msg string, args ...any) {
	switch typ {
	case peerDbgConn:
		if !btg.cfg().EnableTorrentPeerConnectionDebug.Load() {
			return
		}
	case peerDbgDownload:
		if !btg.cfg().EnableTorrentPeerDownloadDebug.Load() {
			return
		}
	case peerDbgUpload:
		if !btg.cfg().EnableTorrentPeerUploadDebug.Load() {
			return
		}
	}

	btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT", "%s: %s", p.name,
		fmt.Sprintf(msg, args...))
}

// peerSetState — C: peer_set_state
func (btg *BtGlobal) peerSetState(p *Peer, state int) {
	p.state = state
	p.stateChangeT = uint64(btg.aio.CurrentTime())
}

// C: static const char *peer_state_tab[] (peer.c:112-118)
var peerStateTab = [...]string{
	PeerStateInactive:      "Inactive",
	PeerStateConnecting:    "Connecting",
	PeerStateConnectFail:   "Connect fail",
	PeerStateWaitHandshake: "Wait Handshake",
	PeerStateRunning:       "Running",
}

// peerStateTxt — C: peer_state_txt
func (btg *BtGlobal) peerStateTxt(state uint) string {
	if state >= PeerStateNum {
		return "???"
	}
	if int(state) >= len(peerStateTab) {
		return "???"
	}
	return peerStateTab[state]
}

// peerArmKaTimer — C: peer_arm_ka_timer
func (btg *BtGlobal) peerArmKaTimer(p *Peer) {
	p.kaSendTimer.ArmDeltaSec(60)
}

// sendHandshake — C: send_handshake
func (btg *BtGlobal) sendHandshake(p *Peer) {
	to := p.torrent
	var handshake [1 + 19 + 8 + 20 + 20]byte

	handshake[0] = 19
	copy(handshake[1:], "BitTorrent protocol")

	reserved := handshake[1+19:]
	reserved[7] = 0x04
	reserved[5] = 0x10

	copy(handshake[1+19+8:], to.infoHash[:])
	copy(handshake[1+19+8+20:], btg.peerID[:20])
	asyncio.Send(p.connection, handshake[:], 0)
	btg.peerArmKaTimer(p)
}

// peerSendKeepalive — C: peer_send_keepalive
func (btg *BtGlobal) peerSendKeepalive(aux any) {
	p := aux.(*Peer)
	buf := [4]byte{0}
	asyncio.Send(p.connection, buf[:], 0)
	btg.peerArmKaTimer(p)
}

// peerAbortRequests — C: peer_abort_requests
func (btg *BtGlobal) peerAbortRequests(p *Peer) {
	if p.state != PeerStateRunning {
		return
	}

	p.dataRecvTimer.Disarm()

	if p.downloadRequests.lhFirst != nil {
		p.torrent.peersWithOutstandingReq--
	}

	for ts := p.sendreqs.tqFirst; ts != nil; {
		next := ts.peerLinkNext
		btg.torrentSendreqDestroy(ts)
		ts = next
	}

	for tr := p.downloadRequests.lhFirst; tr != nil; {
		next := tr.peerLinkNext
		tb := tr.block

		if tr.peer != p {
			panic("assertion failed: tr.peer == p")
		}

		listRemove(tr, trPeerLink)

		if tb != nil {

			listRemove(tr, trBlockLink)
			if tb.requests.lhFirst == nil {
				tp := tb.piece
				// Put block back on waiting list
				listRemove(tb, tbPieceLink)
				listInsertHead(&tp.waitingBlocks.lhFirst, tb, tbPieceLink)
			}
		}
		tr = next
	}
	p.activeRequests = 0
}

// peerConnectCb — C: peer_connect_cb
func (btg *BtGlobal) peerConnectCb(opaque any, err string) {
	p := opaque.(*Peer)

	btg.mu.Lock()

	if err == "" {
		p.amChoking = true
		p.amInterested = false
		p.peerChoking = true
		p.peerInterested = false

		btg.sendHandshake(p)
		asyncio.SetTimeoutDeltaSec(p.connection, 15)
		btg.peerSetState(p, PeerStateWaitHandshake)
		p.chokedTime = p.stateChangeT
		btg.peerTrace(p, peerDbgConn, "Connected")
	} else {

		btg.peerTrace(p, peerDbgConn, "%s in state %s",
			err, btg.peerStateTxt(uint(p.state)))

		nextState := PeerStateConnectFail
		if p.state == PeerStateRunning {
			nextState = PeerStateDisconnected
		}
		btg.peerShutdown(p, nextState, 1)
	}
	btg.mu.Unlock()
}

// peerDestroyAllMetainfoRequests — C: peer_destroy_all_metainfo_requests
func (btg *BtGlobal) peerDestroyAllMetainfoRequests(p *Peer) {
	if p.metainfoRequests.lhFirst == nil {
		return
	}

	for mr := p.metainfoRequests.lhFirst; mr != nil; {
		next := mr.mrPeerLinkNext
		listRemove(mr, mrPeerLink)
		listRemove(mr, mrQueryLink)
		mr.data = nil
		mr = next
	}
	btg.metainfoAvailable.Broadcast()
}

// peerFreePieces — C: peer_free_pieces
func (btg *BtGlobal) peerFreePieces(p *Peer) {
	for pp := p.pieces.lhFirst; pp != nil; {
		next := pp.ppPeerLinkNext
		btg.torrentPiecePeerDestroy(pp)
		pp = next
	}
}

// peerDestroyRequest — C: peer_destroy_request
func (btg *BtGlobal) peerDestroyRequest(tr *TorrentRequest) {
	p := tr.peer
	if p.activeRequests <= 0 {
		panic("assertion failed: p.active_requests > 0")
	}
	p.activeRequests--
	listRemove(tr, trPeerLink)

	if p.downloadRequests.lhFirst == nil {
		p.torrent.peersWithOutstandingReq--
	}
}

// peerShutdown — C: peer_shutdown
func (btg *BtGlobal) peerShutdown(p *Peer, nextState int, resched int) {
	to := p.torrent

	if p.state != PeerStateInactive {
		to.activePeers--
		btg.activePeers--
		if resched != 0 {
			btg.torrentAttemptMorePeers(to)
		}
	}

	if p.connection != nil {
		asyncio.DelFD(p.connection)
		p.connection = nil
	}

	if p.pendingBitfield != nil {
		p.pendingBitfield = nil
	}

	p.kaSendTimer.Disarm()
	p.dataRecvTimer.Disarm()

	p.pieceFlags = nil

	// Do stuff depending on current (old) state

	switch p.state {

	case PeerStateDisconnected:
		tailqRemove(&to.disconnectedPeers.tqFirst,
			&to.disconnectedPeers.tqLast, p, peerQLink)

	case PeerStateConnectFail:
		tailqRemove(&to.connectFailedPeers.tqFirst,
			&to.connectFailedPeers.tqLast, p, peerQLink)

	case PeerStateInactive:
		tailqRemove(&to.inactivePeers.tqFirst,
			&to.inactivePeers.tqLast, p, peerQLink)

	case PeerStateRunning:
		listRemove(p, peerRunningLink)
		btg.peerDestroyAllMetainfoRequests(p)
		if !p.peerChoking {
			listRemove(p, peerUnchokedLink)
			p.peerChoking = true
		}

	case PeerStateWaitHandshake, PeerStateConnecting:

	default:
		fmt.Printf("Cant shutdown peer in state %d\n", p.state)
		panic("abort")
	}

	btg.peerAbortRequests(p)

	btg.peerSetState(p, nextState)

	// Do stuff depending on next (new) state
	destroy := false
	switch p.state {
	case PeerStateConnectFail:
		p.failTime = uint64(btg.aio.CurrentTime())
		p.connectFail++
		if p.connectFail == 5 {
			destroy = true
			break
		}
		tailqInsertTail(&to.connectFailedPeers.tqFirst,
			&to.connectFailedPeers.tqLast, p, peerQLink)

	case PeerStateDisconnected:
		p.failTime = uint64(btg.aio.CurrentTime())
		p.disconnected++
		if p.disconnected == 5 {
			destroy = true
			break
		}
		tailqInsertTail(&to.disconnectedPeers.tqFirst,
			&to.disconnectedPeers.tqLast, p, peerQLink)

	case PeerStateDestroyed:
		destroy = true

	default:
		fmt.Printf("Can't shutdown peer to state %d\n", p.state)
		panic("abort")
	}

	if destroy {
		listRemove(p, peerLink)
		to.numPeers--
		btg.peerFreePieces(p)
	}
	if resched != 0 {
		btg.torrentIoDoRequests(to)
	}
}

// peerDisconnect — C: peer_disconnect
func (btg *BtGlobal) peerDisconnect(p *Peer, format string, args ...any) {
	buf := fmt.Sprintf(format, args...)

	btg.peerTrace(p, peerDbgConn, "Disconnected by us: %s", buf)
	btg.peerShutdown(p, PeerStateDestroyed, 1)
}

// sendInitialSet — C: send_initial_set
func (btg *BtGlobal) sendInitialSet(p *Peer) {
	to := p.torrent

	bitfieldLen := (to.numPieces + 7) / 8
	something := false
	bitfield := make([]byte, bitfieldLen)

	for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		if !tp.hashOk {
			continue
		}

		i := tp.index

		bitfield[i/8] |= 0x80 >> uint(i&0x7)
		something = true
	}

	for i := range to.numPieces {
		if to.cachefilePieceMap[i] != -1 {
			bitfield[i/8] |= 0x80 >> uint(i&0x7)
			something = true
		}
	}

	if something {
		buf := [5]byte{0, 0, 0, 0, btMsgidBitfield}
		binary.BigEndian.PutUint32(buf[:], uint32(bitfieldLen+1))
		asyncio.Send(p.connection, buf[:], 1)
		asyncio.Send(p.connection, bitfield, 0)
	}
}

// recvHandshake — C: recv_handshake
func (btg *BtGlobal) recvHandshake(p *Peer, q *misc.HtsbufQueue) int {
	var msg [1 + 19 + 8 + 20 + 20]byte

	if q.Peek(msg[:], len(msg)) != len(msg) {
		return 1
	}

	q.Drop(len(msg))

	if msg[0] != 19 || !bytes.Equal(msg[1:20], []byte("BitTorrent protocol")) {
		btg.peerDisconnect(p, "Wrong protocol")
		return 1
	}

	reserved := msg[1+19:]

	if reserved[7]&0x4 != 0 {
		p.fastExt = true
	}
	if reserved[5]&0x10 != 0 {
		p.extProt = true
	}

	if !bytes.Equal(msg[1+19+8:1+19+8+20], p.torrent.infoHash[:]) {
		btg.peerDisconnect(p, "Invalid info hash")
		return 1
	}

	copy(p.id[:20], msg[1+19+8+20:1+19+8+20+20])
	p.id[20] = 0

	sfx := ""
	if p.fastExt {
		sfx += ", Fast extensions"
	}
	if p.extProt {
		sfx += ", Extension Protocol"
	}
	btg.peerTrace(p, peerDbgConn, "Handshake received%s", sfx)

	btg.sendInitialSet(p)
	if p.extProt {
		btg.peerSendExtensionHandshake(p)
	}
	return 0
}

// peerHavePiece — C: peer_have_piece
func (btg *BtGlobal) peerHavePiece(p *Peer, pid uint32) {
	if p.pieceFlags[pid]&PieceHave != 0 {
		return
	}
	p.pieceFlags[pid] |= PieceHave
	p.numPiecesHave++
	if p.numPiecesHave > p.torrent.numPieces {
		panic("assertion failed: num_pieces_have <= num_pieces")
	}
}

// parseBitfield — C: parse_bitfield
func (btg *BtGlobal) parseBitfield(p *Peer, data []byte, length int) int {
	to := p.torrent

	if (to.numPieces+7)/8 != length {
		return 1
	}

	if p.pieceFlags == nil {
		p.pieceFlags = make([]byte, to.numPieces)
	}

	p.numPiecesHave = 0
	for i := range to.numPieces {
		if data[i/8]&(0x80>>uint(i&0x7)) != 0 {
			btg.peerHavePiece(p, uint32(i))
		}
	}
	btg.peerUpdateInterest(to, p)
	if !p.peerChoking {
		btg.torrentIoDoRequests(to)
	}
	return 0
}

// recvBitfield — C: recv_bitfield
func (btg *BtGlobal) recvBitfield(p *Peer, data []byte, length int) int {
	if length == 0 || length > 8192 {
		return 0
	}

	to := p.torrent

	if to.metainfo == nil {

		p.pendingBitfield = make([]byte, length)
		copy(p.pendingBitfield, data[:length])
		p.pendingBitfieldSize = length
		btg.peerTrace(p, peerDbgConn, "Initial bitfield message stashed")
		return 0
	}

	if btg.parseBitfield(p, data, length) != 0 {
		btg.peerDisconnect(p, "Invalid 'bitfield' length got:%d, pieces:%d",
			length, to.numPieces)
		return 1
	}
	return 0
}

// recvHave — C: recv_have
func (btg *BtGlobal) recvHave(p *Peer, data []byte, length int) int {
	to := p.torrent

	if length != 4 {
		btg.peerDisconnect(p, "Invalid 'have' length: %d", length)
		return 1
	}

	pid := uint32(data[0])<<24 | uint32(data[1])<<16 |
		uint32(data[2])<<8 | uint32(data[3])

	if to.metainfo == nil {

		if p.pendingBitfield != nil {
			if pid/8 < uint32(p.pendingBitfieldSize) {
				p.pendingBitfield[pid/8] |= 0x80 >> (pid & 0x7)
			}
		}

		return 0
	}

	if pid >= uint32(to.numPieces) {
		btg.peerDisconnect(p, "Excessive piece index %d / %d", pid, to.numPieces)
		return 1
	}

	if p.pieceFlags == nil {
		p.pieceFlags = make([]byte, to.numPieces)
	}

	btg.peerHavePiece(p, pid)

	btg.peerUpdateInterest(to, p)
	if !p.peerChoking {
		btg.torrentIoDoRequests(to)
	}
	return 0
}

// recvHaveAll — C: recv_have_all
func (btg *BtGlobal) recvHaveAll(p *Peer) {
	to := p.torrent

	if to.metainfo == nil {
		p.pendingHaveAll = true
		return
	}

	if p.pieceFlags == nil {
		p.pieceFlags = make([]byte, to.numPieces)
	}

	for i := range to.numPieces {
		p.pieceFlags[i] |= PieceHave
	}
	p.numPiecesHave = to.numPieces

	btg.peerUpdateInterest(to, p)
	if !p.peerChoking {
		btg.torrentIoDoRequests(to)
	}
}

// recvPiece — C: recv_piece
func (btg *BtGlobal) recvPiece(p *Peer, buf []byte, length int) int {
	to := p.torrent
	if length < 8 {
		btg.peerDisconnect(p, "Bad piece header length")
		return 1
	}

	index := uint32(buf[0])<<24 | uint32(buf[1])<<16 |
		uint32(buf[2])<<8 | uint32(buf[3])
	begin := uint32(buf[4])<<24 | uint32(buf[5])<<16 |
		uint32(buf[6])<<8 | uint32(buf[7])

	length -= 8
	buf = buf[8:]

	var tr *TorrentRequest

	for tr = p.downloadRequests.lhFirst; tr != nil; tr = tr.peerLinkNext {
		if tr.piece == index && tr.begin == begin && tr.length == uint32(length) {
			break
		}
	}

	if tr == nil {
		to.wastedBytes += uint64(length)
		p.numWaste++
		btg.peerTrace(p, peerDbgDownload,
			"Got data not asked for: %d:0x%x+0x%x",
			index, begin, length)
		return 0
	}

	to.downloadedBytes += uint64(length)
	p.bytesReceived += uint64(length)

	now := btg.aio.CurrentTime()
	second := int(now / 1000000)

	to.downloadRate.AverageFill(second, int64(to.downloadedBytes))
	p.downloadRate.AverageFill(second, int64(p.bytesReceived))

	delay := int(min(int64(60000000), now-tr.sendTime))

	if p.blockDelay != 0 {
		p.blockDelay = (p.blockDelay*7 + delay) / 8
	} else {
		p.blockDelay = delay
		btg.peerCancelOrphanedRequests(p, tr)
	}
	p.maxq = 10

	if tr.qdepth >= 10 {
		panic("assertion failed: tr.qdepth < 10")
	}
	if p.bd[tr.qdepth] != 0 {
		p.bd[tr.qdepth] = (p.bd[tr.qdepth]*7 + delay) / 8
	} else {
		p.bd[tr.qdepth] = delay
	}

	if tr.block != nil {
		listRemove(tr, trBlockLink)
		btg.torrentReceiveBlock(tr.block, buf, int(begin), length, to, p)
	}

	btg.peerDestroyRequest(tr)
	return 0
}

// recvReject — C: recv_reject
func (btg *BtGlobal) recvReject(p *Peer, buf []byte, length int) int {
	if length != 12 {
		btg.peerDisconnect(p, "Bad reject header length")
		return 1
	}

	piece := binary.BigEndian.Uint32(buf)
	offset := binary.BigEndian.Uint32(buf[4:])
	reqlen := binary.BigEndian.Uint32(buf[8:])

	var tr *TorrentRequest

	for tr = p.downloadRequests.lhFirst; tr != nil; tr = tr.peerLinkNext {
		if tr.piece == piece &&
			tr.begin == offset &&
			tr.length == reqlen {
			break
		}
	}

	if tr == nil {
		/**
		 * Request not sent (or have already been cancelled)
		 * Some peers send reject when as a response to a cancel
		 * so this can be quite common
		 */
		return 0
	}

	linked := "NO"
	if tr.block != nil {
		linked = "YES"
	}
	btg.peerTrace(p, peerDbgDownload,
		"Rejected request for %d:0x%x+0x%x linked:%s",
		piece, offset, reqlen, linked)

	tb := tr.block
	if tb != nil {
		tp := tb.piece
		listRemove(tr, trBlockLink)

		/**
		 * If no more requests are pending, we need to put back block on
		 * wait list.  Next torrent_io_do_requests() will take care of
		 * sending out new requests again.
		 */

		if tb.requests.lhFirst == nil {
			listRemove(tb, tbPieceLink)
			listInsertHead(&tp.waitingBlocks.lhFirst, tb, tbPieceLink)
		}
	}

	to := p.torrent

	if p.pieceFlags == nil {
		p.pieceFlags = make([]byte, to.numPieces)
	}

	p.pieceFlags[tr.piece] |= PieceRejected

	btg.peerDestroyRequest(tr)
	return 0
}

// peerSendPiece — C: peer_send_piece
func (btg *BtGlobal) peerSendPiece(p *Peer, tp *TorrentPiece, offset uint32, length uint32) {
	var out [13]byte

	binary.BigEndian.PutUint32(out[:], 9+length)
	out[4] = btMsgidPiece
	binary.BigEndian.PutUint32(out[5:], uint32(tp.index))
	binary.BigEndian.PutUint32(out[9:], offset)

	p.torrent.uploadedBytes += uint64(length)
	p.bytesSent += uint64(length)

	asyncio.Send(p.connection, out[:], 1)
	asyncio.Send(p.connection, tp.data[offset:offset+length], 0)
}

// peerSendReject — C: peer_send_reject
func (btg *BtGlobal) peerSendReject(p *Peer, piece uint32, offset uint32, length uint32) {
	buf := [17]byte{0, 0, 0, 13, btMsgidReject,
		byte(piece >> 24),
		byte(piece >> 16),
		byte(piece >> 8),
		byte(piece),
		byte(offset >> 24),
		byte(offset >> 16),
		byte(offset >> 8),
		byte(offset),
		byte(length >> 24),
		byte(length >> 16),
		byte(length >> 8),
		byte(length)}
	asyncio.Send(p.connection, buf[:], 0)
}

// recvRequest — C: recv_request
func (btg *BtGlobal) recvRequest(p *Peer, buf []byte, length int) int {
	if length != 12 {
		btg.peerDisconnect(p, "Bad request packet length")
		return 1
	}

	piece := binary.BigEndian.Uint32(buf)
	offset := binary.BigEndian.Uint32(buf[4:])
	reqlen := binary.BigEndian.Uint32(buf[8:])
	var tp *TorrentPiece
	to := p.torrent

	btg.peerTrace(p, peerDbgUpload,
		"Got request for piece %d:0x%x+0x%x",
		piece, offset, reqlen)

	for tp = to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		if tp.index == int(piece) {
			break
		}
	}

	if p.amChoking {
		return 0 // Don't send to peer which we have choked
	}

	if tp == nil {

		// C indexes to_cachefile_piece_map[piece] unchecked; the
		// canonical read is UB for out-of-range piece indexes.
		// Go: treat as "not in cache" (equivalent for all defined
		// behavior — the C UB case has no defined result).
		if int(piece) < to.numPieces &&
			to.cachefilePieceMap[piece] != -1 {

			tp = btg.torrentPieceCreate(to, int(piece))
			tp.loadReq = true
			btg.torrentDiskioWakeup()

		} else {
			btg.peerTrace(p, peerDbgUpload,
				"Got request for piece %d:0x%x+0x%x -- Not in cache",
				piece, offset, reqlen)
			return 0
		}
	}

	if offset+reqlen > uint32(tp.pieceLength) {
		btg.peerDisconnect(p, "Request piece %d:0x%x+0x%x out of range",
			piece, offset, reqlen)
		return 1
	}

	delayXmit := false
	if to.outputRateTokens < int(reqlen) {
		if !to.outputRateTimer.IsArmed() {
			to.outputRateTimer.Arm(to.outputRateRefillTime + 100000)
		}
		delayXmit = true
	} else {
		delayXmit = !tp.hashOk
	}

	if delayXmit {

		ts := &TorrentSendreq{}

		if p.sendreqs.tqFirst == nil {
			tailqInsertTail(&to.haveSendreqPeers.tqFirst,
				&to.haveSendreqPeers.tqLast, p, peerHaveSendreqLink)
		}

		tailqInsertTail(&p.sendreqs.tqFirst, &p.sendreqs.tqLast,
			ts, tsPeerLink)
		ts.peer = p
		listInsertHead(&tp.sendreqs.lhFirst, ts, tsPieceLink)
		ts.piece = tp

		ts.offset = offset
		ts.length = reqlen
		return 0
	}
	to.outputRateTokens -= int(reqlen)
	btg.peerSendPiece(p, tp, offset, reqlen)
	return 0
}

// recvAllowedFast — C: recv_allowed_fast
func (btg *BtGlobal) recvAllowedFast(p *Peer, buf []byte, length int) int {
	if length != 4 {
		btg.peerDisconnect(p, "Bad request packet length")
		return 1
	}

	return 0
}

// recvExtension — C: recv_extension
func (btg *BtGlobal) recvExtension(p *Peer, buf []byte, length int) int {
	if length < 1 {
		btg.peerDisconnect(p, "Bad request packet length")
		return 1
	}
	msgid := buf[0]
	buf = buf[1:]
	length--

	msg, consumed, derr := BencodeDeserialize(buf[:length], nil, nil)
	if msg == nil {
		btg.peerDisconnect(p, "Bad extension message -- %s", derr)
		return 1
	}
	btg.peerParseExtension(p, msg, msgid,
		buf[consumed:length], length-consumed)

	msg.Release()
	return 0
}

// peerConnect — C: peer_connect
func (btg *BtGlobal) peerConnect(p *Peer) {
	to := p.torrent

	if p.connection != nil {
		panic("assertion failed: p.connection == nil")
	}

	btg.peerSetState(p, PeerStateConnecting)

	to.activePeers++
	btg.activePeers++

	name := fmt.Sprintf("BT Peer %s", netcore.NetAddrStr(&p.addr))

	p.connection = btg.aio.Connect(name, &p.addr,
		btg.peerConnectCb, btg.peerReadCb, p, 5000, nil, "")
}

// recvMessage — C: recv_message
func (btg *BtGlobal) recvMessage(p *Peer, q *misc.HtsbufQueue) int {
	var d4 [4]byte
	var msgid [1]byte

	if q.Peek(d4[:], 4) != 4 {
		return 1
	}
	length := int(binary.BigEndian.Uint32(d4[:]))

	if length > 0x100000 { // Arbitrary
		btg.peerDisconnect(p, "Bad message length")
		return 1
	}

	if q.Len() < length+4 {
		return 1 // Not enoguh bytes in buffer yet
	}

	q.Drop(4)

	if length == 0 {
		return 0 // Keep alive
	}
	q.Read(msgid[:], 1)
	length--

	var data []byte

	if length != 0 {
		data = make([]byte, length)
		q.Read(data, length)
	}

	r := 0

	switch msgid[0] {
	case btMsgidBitfield:
		r = btg.recvBitfield(p, data, length)

	case btMsgidHave:
		r = btg.recvHave(p, data, length)

	case btMsgidUnchoke:
		if !p.peerChoking {
			break
		}

		interested := "not "
		if p.amInterested {
			interested = ""
		}
		btg.peerTrace(p, peerDbgDownload,
			"Unchoked us, we are %sinterested", interested)
		p.peerChoking = false
		listInsertHead(&p.torrent.unchokedPeers.lhFirst, p, peerUnchokedLink)
		p.maxq = 1
		btg.torrentIoDoRequests(p.torrent)

	case btMsgidChoke:
		if p.peerChoking {
			break
		}

		interested := "not "
		if p.amInterested {
			interested = ""
		}
		btg.peerTrace(p, peerDbgDownload,
			"Choked us, we are %sinterested", interested)
		p.peerChoking = true
		p.chokedTime = uint64(btg.aio.CurrentTime())
		listRemove(p, peerUnchokedLink)
		btg.peerAbortRequests(p)
		btg.torrentIoDoRequests(p.torrent)

	case btMsgidPiece:
		btg.recvPiece(p, data, length)

	case btMsgidInterested:
		btg.peerTrace(p, peerDbgUpload, "Is interested")
		p.peerInterested = true

	case btMsgidNotInterested:
		btg.peerTrace(p, peerDbgUpload, "Is not interested")
		p.peerInterested = false

	case btMsgidRequest:
		r = btg.recvRequest(p, data, length)

	case btMsgidHaveAll:
		btg.recvHaveAll(p)

	case btMsgidHaveNone:

	case btMsgidAllowedFast:
		r = btg.recvAllowedFast(p, data, length)

	case btMsgidReject:
		r = btg.recvReject(p, data, length)
	case btMsgidExtension:
		r = btg.recvExtension(p, data, length)

	default:
	}

	return r
}

// peerReadCb — C: peer_read_cb
func (btg *BtGlobal) peerReadCb(opaque any, q *misc.HtsbufQueue) {
	p := opaque.(*Peer)

	btg.mu.Lock()

	switch p.state {

	case PeerStateWaitHandshake:
		if btg.recvHandshake(p, q) != 0 {
			break
		}
		listInsertHead(&p.torrent.runningPeers.lhFirst, p, peerRunningLink)
		btg.peerSetState(p, PeerStateRunning)
		// FALLTHRU
		p.connectFail = 0
		p.disconnected = 0
		fallthrough

	case PeerStateRunning:
		for {
			if btg.recvMessage(p, q) != 0 {
				break
			}
		}

		if p.connection != nil {
			timeout := 300
			asyncio.SetTimeoutDeltaSec(p.connection, timeout)
		}

	default:
		panic("abort")
	}
	btg.mu.Unlock()
}

// peerUpdateInterest — C: peer_update_interest
func (btg *BtGlobal) peerUpdateInterest(to *Torrent, p *Peer) {
	interested := 0

	for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		if tp.waitingBlocks.lhFirst == nil &&
			tp.sentBlocks.lhFirst == nil {
			continue
		}

		if p.pieceFlags == nil {
			continue
		}

		if tp.index >= to.numPieces {
			panic("assertion failed: tp.index < to.num_pieces")
		}
		if p.pieceFlags[tp.index]&PieceHave == 0 {
			continue
		}

		interested = 1
		break
	}

	if p.state != PeerStateRunning {
		return
	}

	if p.amInterested != (interested != 0) {
		p.amInterested = interested != 0
		s := "not "
		if p.amInterested {
			s = ""
		}
		btg.peerTrace(p, peerDbgDownload, "Sending %sinterested", s)
		msgid := btMsgidNotInterested
		if p.amInterested {
			msgid = btMsgidInterested
		}
		btg.peerSendMsgid(p, msgid)
	}
}

// peerCancelRequest — C: peer_cancel_request
func (btg *BtGlobal) peerCancelRequest(tr *TorrentRequest) {
	btg.peerSendCancel(tr.peer, tr)
	btg.peerDestroyRequest(tr)
}

// peerCancelOrphanedRequests — C: peer_cancel_orphaned_requests
func (btg *BtGlobal) peerCancelOrphanedRequests(p *Peer, skip *TorrentRequest) {
	for tr := p.downloadRequests.lhFirst; tr != nil; {
		next := tr.peerLinkNext

		if tr.block != nil || tr == skip {
			tr = next
			continue
		}

		btg.peerCancelRequest(tr)
		tr = next
	}
}

// peerSendMsgid — C: peer_send_msgid
func (btg *BtGlobal) peerSendMsgid(p *Peer, msgid int) {
	buf := [5]byte{0, 0, 0, 1, byte(msgid)}
	asyncio.Send(p.connection, buf[:], 0)
	btg.peerArmKaTimer(p)
}

// peerSendRequest — C: peer_send_request
func (btg *BtGlobal) peerSendRequest(p *Peer, tb *TorrentBlock) {
	piece := uint32(tb.piece.index)
	offset := tb.begin
	length := tb.length

	btg.peerTrace(p, peerDbgDownload,
		"Requesting %d:0x%x+0x%x",
		piece, offset, length)

	p.numRequests++

	buf := [17]byte{0, 0, 0, 13, btMsgidRequest,
		byte(piece >> 24),
		byte(piece >> 16),
		byte(piece >> 8),
		byte(piece),
		byte(offset >> 24),
		byte(offset >> 16),
		byte(offset >> 8),
		byte(offset),
		byte(length >> 24),
		byte(length >> 16),
		byte(length >> 8),
		byte(length)}

	asyncio.Send(p.connection, buf[:], 0)
	btg.peerArmKaTimer(p)
}

// peerSendCancel — C: peer_send_cancel
func (btg *BtGlobal) peerSendCancel(p *Peer, tr *TorrentRequest) {
	piece := tr.piece
	offset := tr.begin
	length := tr.length

	btg.peerTrace(p, peerDbgDownload,
		"Canceling %d:0x%x+0x%x",
		piece, offset, length)

	p.numCancels++

	buf := [17]byte{0, 0, 0, 13, btMsgidCancel,
		byte(piece >> 24),
		byte(piece >> 16),
		byte(piece >> 8),
		byte(piece),
		byte(offset >> 24),
		byte(offset >> 16),
		byte(offset >> 8),
		byte(offset),
		byte(length >> 24),
		byte(length >> 16),
		byte(length >> 8),
		byte(length)}

	asyncio.Send(p.connection, buf[:], 0)
	btg.peerArmKaTimer(p)
}

// peerSendHave — C: peer_send_have
func (btg *BtGlobal) peerSendHave(p *Peer, piece uint32) {
	buf := [9]byte{0, 0, 0, 5, btMsgidHave,
		byte(piece >> 24),
		byte(piece >> 16),
		byte(piece >> 8),
		byte(piece)}

	asyncio.Send(p.connection, buf[:], 0)
	btg.peerArmKaTimer(p)
}

// peerChoke — C: peer_choke
func (btg *BtGlobal) peerChoke(p *Peer, choke int) {
	if p.amChoking != (choke != 0) {
		p.amChoking = choke != 0

		s := "unchoke"
		msgid := btMsgidUnchoke
		if choke != 0 {
			s = "choke"
			msgid = btMsgidChoke
		}
		btg.peerTrace(p, peerDbgUpload, "Sending %s", s)
		btg.peerSendMsgid(p, msgid)
	}
}

// peerAdd — C: peer_add
func (btg *BtGlobal) peerAdd(to *Torrent, na *netcore.NetAddr) {
	for p := to.peers.lhFirst; p != nil; p = p.linkNext {
		if netcore.NetAddrCmp(&p.addr, na) {
			return // Already know about peer
		}
	}

	to.numPeers++
	p := &Peer{}

	p.addr = *na
	p.torrent = to
	p.name = netcore.NetAddrStr(na)
	p.kaSendTimer.Setup(btg.aio, btg.peerSendKeepalive, p)

	tailqSetup(&p.sendreqs.tqFirst, &p.sendreqs.tqLast)

	listInsertHead(&to.peers.lhFirst, p, peerLink)
	if to.activePeers >= btg.maxPeersTorrent ||
		btg.activePeers >= btg.maxPeersGlobal {

		btg.peerSetState(p, PeerStateInactive)
		tailqInsertTail(&to.inactivePeers.tqFirst,
			&to.inactivePeers.tqLast, p, peerQLink)
		return
	}

	btg.peerConnect(p)
}

// peerShutdownAll — C: peer_shutdown_all
func (btg *BtGlobal) peerShutdownAll(to *Torrent) {
	for p := to.peers.lhFirst; p != nil; {
		next := p.linkNext
		btg.peerShutdown(p, PeerStateDestroyed, 0)
		p = next
	}
}

// peerActivatePendingData — C: peer_activate_pending_data
func (btg *BtGlobal) peerActivatePendingData(to *Torrent) {
	for p := to.runningPeers.lhFirst; p != nil; {
		next := p.runningLinkNext

		if p.pendingHaveAll {

			if p.pieceFlags == nil {
				p.pieceFlags = make([]byte, to.numPieces)
			}

			for i := range to.numPieces {
				p.pieceFlags[i] |= PieceHave
			}
			p.numPiecesHave = to.numPieces
		}

		if p.pendingBitfield != nil {
			d := p.pendingBitfield
			p.pendingBitfield = nil

			if btg.parseBitfield(p, d, p.pendingBitfieldSize) != 0 {
				btg.peerDisconnect(p, "Invalid 'bitfield' length got:%d, pieces:%d",
					p.pendingBitfieldSize, to.numPieces)
			}
		}
		p = next
	}
}

// peerParseExtensionHandshake — C: peer_parse_extension_handshake
func (btg *BtGlobal) peerParseExtensionHandshake(p *Peer, handshake *htsmsg.HTSMsg) {
	m := handshake.GetMap("m")

	if m != nil {
		p.extUtMetadata = uint8(m.GetU32OrDefault("ut_metadata", 0))
		btg.metainfoAvailable.Broadcast()
	}
}

// peerParseExtensionMetadata — C: peer_parse_extension_metadata
func (btg *BtGlobal) peerParseExtensionMetadata(p *Peer, msg *htsmsg.HTSMsg,
	data []byte, length int) {
	msgType := int32(msg.GetU32OrDefault("msg_type", 0xFFFFFFFF))
	piece := int32(msg.GetU32OrDefault("piece", 0xFFFFFFFF))
	totalSize := int32(msg.GetU32OrDefault("total_size", 0xFFFFFFFF))
	if msgType != 1 && msgType != 2 {
		return
	}
	if totalSize < 1 {
		return
	}

	for mr := p.metainfoRequests.lhFirst; mr != nil; mr = mr.mrPeerLinkNext {

		if mr.piece == int(piece) && mr.state == MRSent {
			if msgType == 1 {
				mr.size = length
				mr.data = make([]byte, length)
				mr.totalSize = int(totalSize)
				copy(mr.data, data[:length])
				mr.state = MRReceived
			} else {
				mr.state = MRRejected
			}
			btg.metainfoAvailable.Broadcast()
		}
	}
}

// peerParseExtension — C: peer_parse_extension
func (btg *BtGlobal) peerParseExtension(p *Peer, msg *htsmsg.HTSMsg, msgid uint8,
	data []byte, length int) {
	switch msgid {
	case extensionMsgidHandshake:
		btg.peerParseExtensionHandshake(p, msg)
	case extensionMsgidMetadata:
		btg.peerParseExtensionMetadata(p, msg, data, length)
	}
}

// peerSendExtensionMsg — C: peer_send_extension_msg
func (btg *BtGlobal) peerSendExtensionMsg(p *Peer, msg *htsmsg.HTSMsg, msgid uint8) {
	b := BencodeSerialize(msg)
	msg.Release()

	buf := [6]byte{0, 0, 0, 0, btMsgidExtension, msgid}
	binary.BigEndian.PutUint32(buf[:], uint32(b.Len()+2))
	asyncio.Send(p.connection, buf[:], 1)
	asyncio.Send(p.connection, b.C8(), 0)
	b.Release()
}

// peerSendExtensionHandshake — C: peer_send_extension_handshake
func (btg *BtGlobal) peerSendExtensionHandshake(p *Peer) {
	handshake := htsmsg.NewMap()

	m := htsmsg.NewMap()
	m.AddU32("ut_metadata", extensionMsgidMetadata)
	handshake.AddMsg("m", m)

	ver := fmt.Sprintf("%s %s", app.AppNameUser, version.AppVersion())
	handshake.AddStr("v", ver)
	btg.peerSendExtensionMsg(p, handshake, extensionMsgidHandshake)
}

// peerSendMetainfoRequest — C: peer_send_metainfo_request
func (btg *BtGlobal) peerSendMetainfoRequest(p *Peer, mr *MetainfoRequest) {
	mr.state = MRSent

	req := htsmsg.NewMap()
	req.AddU32("msg_type", 0) // 0 == request
	req.AddU32("piece", uint32(mr.piece))
	btg.peerSendExtensionMsg(p, req, p.extUtMetadata)
	btg.peerTrace(p, peerDbgDownload,
		"Requesting metadata piece %d", mr.piece)
}
