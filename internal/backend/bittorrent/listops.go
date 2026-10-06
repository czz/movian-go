package bittorrent

// C: sys/queue.h LIST_* / TAILQ_* primitives.
// Every intrusive link is a (Next *T, Prev **T) field pair; the `link`
// accessor returns pointers to that pair for a given entry field.
// Semantics match the BSD macros verbatim — notably LIST_REMOVE leaves
// the removed element's own le_next/le_prev stale.

// C: LIST_INSERT_HEAD
func listInsertHead[T any](first **T, elm *T, link func(*T) (**T, ***T)) {
	n, p := link(elm)
	if *n = *first; *n != nil {
		_, np := link(*n)
		*np = n
	}
	*first = elm
	*p = first
}

// C: LIST_REMOVE
func listRemove[T any](elm *T, link func(*T) (**T, ***T)) {
	n, p := link(elm)
	if *n != nil {
		_, np := link(*n)
		*np = *p
	}
	**p = *n
}

// C: LIST_INSERT_AFTER
func listInsertAfter[T any](listelm, elm *T, link func(*T) (**T, ***T)) {
	n, p := link(elm)
	ln, _ := link(listelm)
	if *n = *ln; *n != nil {
		_, np := link(*n)
		*np = n
	}
	*ln = elm
	*p = ln
}

// C: LIST_INSERT_BEFORE
func listInsertBefore[T any](listelm, elm *T, link func(*T) (**T, ***T)) {
	n, p := link(elm)
	_, lp := link(listelm)
	*p = *lp
	*n = listelm
	**lp = elm
	*lp = n
}

// C: LIST_INSERT_SORTED — insert before first element where cmp <= 0,
// else after the last.
func listInsertSorted[T any](first **T, elm *T, link func(*T) (**T, ***T),
	cmp func(a, b *T) int) {
	if *first == nil {
		listInsertHead(first, elm, link)
		return
	}
	for tmp := *first; tmp != nil; {
		if cmp(elm, tmp) <= 0 {
			listInsertBefore(tmp, elm, link)
			return
		}
		n, _ := link(tmp)
		if *n == nil {
			listInsertAfter(tmp, elm, link)
			return
		}
		tmp = *n
	}
}

// C: TAILQ_INSERT_TAIL
func tailqInsertTail[T any](first **T, last ***T, elm *T, link func(*T) (**T, ***T)) {
	n, p := link(elm)
	*n = nil
	*p = *last
	**last = elm
	*last = n
}

// C: TAILQ_REMOVE
func tailqRemove[T any](first **T, last ***T, elm *T, link func(*T) (**T, ***T)) {
	n, p := link(elm)
	if *n != nil {
		_, np := link(*n)
		*np = *p
	} else {
		*last = *p
	}
	**p = *n
}

// C: TAILQ_INIT
func tailqSetup[T any](first **T, last ***T) {
	*first = nil
	*last = first
}

// ---------------------------------------------------------------------------
// Link accessors — one per (struct, entry-field) pair

func peerQLink(p *Peer) (**Peer, ***Peer) { return &p.queueLinkNext, &p.queueLinkPrev }
func peerLink(p *Peer) (**Peer, ***Peer)  { return &p.linkNext, &p.linkPrev }
func peerRunningLink(p *Peer) (**Peer, ***Peer) {
	return &p.runningLinkNext, &p.runningLinkPrev
}
func peerUnchokedLink(p *Peer) (**Peer, ***Peer) {
	return &p.unchokedLinkNext, &p.unchokedLinkPrev
}
func peerHaveSendreqLink(p *Peer) (**Peer, ***Peer) {
	return &p.haveSendreqLinkNext, &p.haveSendreqLinkPrev
}

func toLink(to *Torrent) (**Torrent, ***Torrent) { return &to.linkNext, &to.linkPrev }

func tfTorrentLink(tf *TorrentFile) (**TorrentFile, ***TorrentFile) {
	return &tf.torrentLinkNext, &tf.torrentLinkPrev
}
func tfParentLink(tf *TorrentFile) (**TorrentFile, ***TorrentFile) {
	return &tf.parentLinkNext, &tf.parentLinkPrev
}

func tpLink(tp *TorrentPiece) (**TorrentPiece, ***TorrentPiece) {
	return &tp.linkNext, &tp.linkPrev
}
func tpServeLink(tp *TorrentPiece) (**TorrentPiece, ***TorrentPiece) {
	return &tp.serveLinkNext, &tp.serveLinkPrev
}

func tbPieceLink(tb *TorrentBlock) (**TorrentBlock, ***TorrentBlock) {
	return &tb.pieceLinkNext, &tb.pieceLinkPrev
}

func trPeerLink(tr *TorrentRequest) (**TorrentRequest, ***TorrentRequest) {
	return &tr.peerLinkNext, &tr.peerLinkPrev
}
func trBlockLink(tr *TorrentRequest) (**TorrentRequest, ***TorrentRequest) {
	return &tr.blockLinkNext, &tr.blockLinkPrev
}

func tsPeerLink(ts *TorrentSendreq) (**TorrentSendreq, ***TorrentSendreq) {
	return &ts.peerLinkNext, &ts.peerLinkPrev
}
func tsPieceLink(ts *TorrentSendreq) (**TorrentSendreq, ***TorrentSendreq) {
	return &ts.pieceLinkNext, &ts.pieceLinkPrev
}

func tfhTorrentFileLink(tfh *TorrentFh) (**TorrentFh, ***TorrentFh) {
	return &tfh.torrentFileLinkNext, &tfh.torrentFileLinkPrev
}
func tfhTorrentLink(tfh *TorrentFh) (**TorrentFh, ***TorrentFh) {
	return &tfh.torrentLinkNext, &tfh.torrentLinkPrev
}
func tfhPieceLink(tfh *TorrentFh) (**TorrentFh, ***TorrentFh) {
	return &tfh.pieceLinkNext, &tfh.pieceLinkPrev
}

func ttTrackerLink(tt *TrackerTorrent) (**TrackerTorrent, ***TrackerTorrent) {
	return &tt.trackerLinkNext, &tt.trackerLinkPrev
}
func ttTorrentLink(tt *TrackerTorrent) (**TrackerTorrent, ***TrackerTorrent) {
	return &tt.torrentLinkNext, &tt.torrentLinkPrev
}

func ppPieceLink(pp *PiecePeer) (**PiecePeer, ***PiecePeer) {
	return &pp.ppPieceLinkNext, &pp.ppPieceLinkPrev
}
func ppPeerLink(pp *PiecePeer) (**PiecePeer, ***PiecePeer) {
	return &pp.ppPeerLinkNext, &pp.ppPeerLinkPrev
}

func mrPeerLink(mr *MetainfoRequest) (**MetainfoRequest, ***MetainfoRequest) {
	return &mr.mrPeerLinkNext, &mr.mrPeerLinkPrev
}
func mrQueryLink(mr *MetainfoRequest) (**MetainfoRequest, ***MetainfoRequest) {
	return &mr.mrQueryLinkNext, &mr.mrQueryLinkPrev
}

func tLink(t *Tracker) (**Tracker, ***Tracker) { return &t.tLinkNext, &t.tLinkPrev }
