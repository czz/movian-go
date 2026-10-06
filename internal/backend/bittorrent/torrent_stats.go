package bittorrent

// Canonical port of src/backend/bittorrent/torrent_stats.c.

import (
	"fmt"
	"math"
	"strconv"

	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// btDeps.httpServer — C: http_path_add uses the global http server.
// Wired via SetHTTPServer (mirrors ecmascript.EsSetHTTPServer).

// SetHTTPServer wires the http server for the /api/torrents endpoint.
// Runs torrent_stats_init immediately when the server is available —
// covers the case where INIT_GROUP_API fired before the http server
// existed (HTTPPathAdd is idempotent by path key).
func (btg *BtGlobal) SetHTTPServer(s *httpnet.HTTPServer) {
	btg.httpServer = s
	btg.torrentStatsStart()
}

// torrentDumpFiles — C: torrent_dump_files (torrent_stats.c:36-50)
func (btg *BtGlobal) torrentDumpFiles(q *torrentFileQueue, out *misc.HtsbufQueue, indent int) {
	for tf := q.tqFirst; tf != nil; tf = tf.parentLinkNext {
		if tf.size != 0 {
			out.QPrintf("%*.s%s  [%d bytes @ %d]\n", indent, "",
				tf.name, tf.size, tf.offset)
		} else {
			out.QPrintf("%*.s%s/\n", indent, "", tf.name)
			btg.torrentDumpFiles(&tf.files, out, indent+2)
		}
	}
}

// torrentDump — C: torrent_dump (torrent_stats.c:57-192)
func (btg *BtGlobal) torrentDump(to *Torrent, q *misc.HtsbufQueue, showRequests int) {
	var str [41]byte

	now := btg.aio.CurrentTime()

	misc.Bin2hex(str[:], len(str), to.infoHash[:], 20)

	q.QPrintf("Infohash: %s  %d pieces (%d in RAM using %d bytes) "+
		"refcount:%d\n", string(str[:40]),
		to.numPieces, to.numActivePieces,
		to.activePiecesMem, to.refcount)
	q.QPrintf("\nOpen files\n")

	for tfh := to.fhs.lhFirst; tfh != nil; tfh = tfh.torrentLinkNext {
		q.QPrintf("%s\n", tfh.file.fullpath)
	}

	q.QPrintf("\nFiles in torrent\n")
	btg.torrentDumpFiles(&to.root, q, 4)

	q.QPrintf("\n%d active peers out of %d known peers\n",
		to.activePeers, to.numPeers)

	q.QPrintf("%-30s %-15s %7s  %6s Snd Rcv Queue  %10s %10s Delay Requests Cancels Waste  ConFail Discon ChokTim\n",
		"Remote", "Status", "StTime", "Pieces", "Recv (kB)", "Sent (kB)")

	for p := to.peers.lhFirst; p != nil; p = p.linkNext {

		if !(p.state == PeerStateRunning ||
			p.state == PeerStateWaitHandshake ||
			p.state == PeerStateConnecting) {
			continue
		}

		if p.peerChoking && false { // C: if(p->p_peer_choking && 0)
			continue
		}

		numHave := p.numPiecesHave

		snd := ""
		if p.peerInterested && !p.amChoking {
			snd = "Yes"
		} else if p.peerInterested {
			snd = "Req"
		}
		rcv := ""
		if p.amInterested && !p.peerChoking {
			rcv = "Yes"
		} else if p.amInterested {
			rcv = "Req"
		}

		q.QPrintf("%-30s %-15s %7ds %6d %3s %3s  %2d/%-2d %10d %10d %5d %8d %7d %6d %7d %6d %7d\n",
			p.name,
			btg.peerStateTxt(uint(p.state)),
			int(now-int64(p.stateChangeT))/1000000,
			numHave,
			snd,
			rcv,
			p.activeRequests,
			p.maxq,
			p.bytesReceived/1000,
			p.bytesSent/1000,
			p.blockDelay/1000,
			p.numRequests,
			p.numCancels,
			p.numWaste,
			p.connectFail,
			p.disconnected,
			int(now-int64(p.chokedTime))/1000000)

		// C: #if 0 per-peer block-delay dump omitted (dead code)

		if showRequests != 0 {
			for tr := p.downloadRequests.lhFirst; tr != nil; tr = tr.peerLinkNext {

				var deadline string

				if tr.block == nil {
					deadline = "---"
				} else if tr.block.piece.deadline == math.MaxInt64 { // C: INT64_MAX
					deadline = "Unset"
				} else {
					delta := tr.block.piece.deadline - now
					deadline = fmt.Sprintf("%1.1fs", float64(delta)/1000000.0)
				}

				orphaned := ""
				if tr.block == nil {
					orphaned = "ORPHANED "
				}
				q.QPrintf("  piece:%-4d offset:0x%-8x length:0x%-5x age:%2.2fs ETA:%-6d req:%d %s  piece deadline:%s\n",
					tr.piece, tr.begin, tr.length,
					float64(now-tr.sendTime)/1000000.0,
					(tr.sendTime+int64(p.blockDelay)-now)/1000,
					tr.reqNum,
					orphaned,
					deadline)
			}
		}
	}
	waitingBlocks := 0
	sentBlocks := 0

	for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
		for tb := tp.waitingBlocks.lhFirst; tb != nil; tb = tb.pieceLinkNext {
			waitingBlocks++
		}

		for tb := tp.sentBlocks.lhFirst; tb != nil; tb = tb.pieceLinkNext {
			sentBlocks++
			// C: #if 0 per-request dump omitted (dead code)
		}
	}

	q.QPrintf("%d request queued, %d in-flight\n",
		waitingBlocks, sentBlocks)

	q.QPrintf("%d bytes downloaded, %d bytes wasted\n",
		to.downloadedBytes, to.wastedBytes)
}

// torrentDumpHTTP — C: torrent_dump_http (torrent_stats.c:195-214)
func (btg *BtGlobal) torrentDumpHTTP(hc *httpnet.HTTPConnection, remain string,
	opaque any, method httpnet.HTTPCmd) int {
	var out misc.HtsbufQueue
	out.HtsbufQueueSetup(0)

	arg := hc.HTTPArgGetReq("peerrequests")
	if arg == "" {
		arg = "0"
	}
	showRequests, _ := strconv.Atoi(arg)

	btg.mu.Lock()

	for to := btg.torrents.lhFirst; to != nil; to = to.linkNext {
		btg.torrentDump(to, &out, showRequests)
	}

	btg.mu.Unlock()

	return hc.HTTPSendReply(0,
		"text/plain; charset=utf-8", "", "", 0, out.Bytes())
}

// torrentStatsStart — C: torrent_stats_init (torrent_stats.c:220-223),
// INITME(INIT_GROUP_API, torrent_stats_init, NULL, 0).
func (btg *BtGlobal) torrentStatsStart() {
	if btg.httpServer == nil {
		return
	}
	btg.httpServer.HTTPPathAdd("/api/torrents", nil, btg.torrentDumpHTTP, true)
}
