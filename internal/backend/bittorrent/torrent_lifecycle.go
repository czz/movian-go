package bittorrent

// C: src/backend/bittorrent/torrent.c — canonical 1:1 port.
// Torrent lifecycle, piece scheduling, request management, hash thread.

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"

	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

// C: #define TORRENT_REQ_SIZE 16384
const torrentReqSize = 16384

// usageEvent — C: usage_event (global function).
func (btg *BtGlobal) usageEvent(key string, count int, seg ...string) {
	btg.backendSystem.Usage().Event(key, count, seg...)
}

// torrentTrace — C: torrent_trace
func (btg *BtGlobal) torrentTrace(t *Torrent, msg string, args ...any) {
	if !btg.cfg().EnableTorrentDebug.Load() {
		return
	}
	btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT", "%s: %s", t.title,
		fmt.Sprintf(msg, args...))
}

// torrentAddTracker — C: torrent_add_tracker
func (btg *BtGlobal) torrentAddTracker(to *Torrent, url string) {
	for tt := to.trackers.lhFirst; tt != nil; tt = tt.torrentLinkNext {
		if tt.tracker.url == url {
			return
		}
	}

	tr := btg.trackerCreate(url)
	if tr != nil {
		btg.trackerAddTorrent(tr, to)
	}
}

// torrentExtractInfoHash — C: torrent_extract_info_hash
func (btg *BtGlobal) torrentExtractInfoHash(opaque any, name string, data []byte) {
	if name != "info" {
		return
	}
	sum := sha1.Sum(data)
	copy(opaque.([]byte), sum[:])
}

// torrentFindByHash — C: torrent_find_by_hash
func (btg *BtGlobal) torrentFindByHash(infoHash []byte) *Torrent {
	for to := btg.torrents.lhFirst; to != nil; to = to.linkNext {
		if bytes.Equal(to.infoHash[:], infoHash) {
			return to
		}
	}
	return nil
}

// torrentCreate — C: torrent_create
func (btg *BtGlobal) torrentCreate(infoHash []byte, initiator string) *Torrent {
	if btg.torrents.lhFirst == nil {
		btg.aio.WakeupWorker(btg.torrentBootPeriodicSignal)
	}

	to := &Torrent{}
	copy(to.infoHash[:], infoHash)
	listInsertHead(&btg.torrents.lhFirst, to, toLink)
	tailqSetup(&to.inactivePeers.tqFirst, &to.inactivePeers.tqLast)
	tailqSetup(&to.disconnectedPeers.tqFirst, &to.disconnectedPeers.tqLast)
	tailqSetup(&to.connectFailedPeers.tqFirst, &to.connectFailedPeers.tqLast)
	tailqSetup(&to.files.tqFirst, &to.files.tqLast)
	tailqSetup(&to.root.tqFirst, &to.root.tqLast)
	tailqSetup(&to.activePieces.tqFirst, &to.activePieces.tqLast)
	tailqSetup(&to.haveSendreqPeers.tqFirst, &to.haveSendreqPeers.tqLast)

	var hs [41]byte
	misc.Bin2hex(hs[:], 41, infoHash, 20)
	to.title = string(hs[:40])

	to.outputRateTimer.Setup(btg.aio, btg.torrentOutputTimer, to)

	btg.usageEvent("btg.Open Torrent", 1, "initiator", initiator)
	return to
}

// torrentCreateFromHash — C: torrent_create_from_hash
func (btg *BtGlobal) torrentCreateFromHash(infoHash []byte, initiator string) *Torrent {
	var to *Torrent

	for to = btg.torrents.lhFirst; to != nil; to = to.linkNext {
		if bytes.Equal(to.infoHash[:], infoHash) {
			break
		}
	}

	if to == nil {
		to = btg.torrentCreate(infoHash, initiator)
	}

	if to.metainfo == nil {
		b := btg.torrentDiskioLoadInfofileFromHash(infoHash)
		if b != nil {

			btg.torrentTrace(to, "Trying to initialize torrent from disk cache")

			doc, _, derr := BencodeDeserialize(b.C8(), nil, nil)

			if doc != nil {

				if perr := btg.torrentParseTorrentfile(to, doc); perr == nil {
					to.metainfo = b // ownership tranfered
					b = nil
					btg.torrentDiskioOpen(to)
					btg.torrentTrace(to, "Torrent initialized from disk cache")
				} else {
					btg.torrentTrace(to, "Failed to decode on-disk metainfo -- %s",
						perr)
				}
				doc.Release()
			} else {
				btg.torrentTrace(to, "Failed to decode on-disk metainfo -- %s",
					derr)
			}
			if b != nil {
				b.Release()
			}
		}
	}
	return to
}

// torrentCreateFromInfofile — C: torrent_create_from_infofile
func (btg *BtGlobal) torrentCreateFromInfofile(b *misc.Buf) (*Torrent, error) {
	var to *Torrent
	infoHash := make([]byte, 20)

	doc, _, derr := BencodeDeserialize(b.C8(),
		btg.torrentExtractInfoHash, infoHash)

	if doc == nil {
		return nil, derr
	}

	for to = btg.torrents.lhFirst; to != nil; to = to.linkNext {
		if bytes.Equal(to.infoHash[:], infoHash) {
			break
		}
	}

	if to == nil {
		to = btg.torrentCreate(infoHash, "infofile")
	}

	if to.metainfo == nil {
		if err := btg.torrentParseTorrentfile(to, doc); err != nil {
			doc.Release()
			return nil, err // Torrent will be garbage collected later
		}

		to.metainfo = b.Retain()
		btg.torrentDiskioOpen(to)
	}

	doc.Release()
	return to, nil
}

// torrentRetain — C: torrent_retain
func (btg *BtGlobal) torrentRetain(to *Torrent) {
	to.refcount++
}

// torrentRelease — C: torrent_release
func (btg *BtGlobal) torrentRelease(to *Torrent) {
	if to.refcount <= 0 {
		panic("assertion failed: to.refcount > 0")
	}
	to.refcount--

	// Actual destruction will take place in torrent_periodic()
	// since it must happen on the async io thread
}

// torrentDestroyFiles — C: torrent_destroy_files
func (btg *BtGlobal) torrentDestroyFiles(to *Torrent) {
	for tf := to.files.tqFirst; tf != nil; {
		next := tf.torrentLinkNext
		if tf.fhs.lhFirst != nil {
			panic("assertion failed: tf.fhs empty")
		}
		tf = next
	}
	to.files.tqFirst = nil
	to.files.tqLast = &to.files.tqFirst
}

// torrentDestroy — C: torrent_destroy
func (btg *BtGlobal) torrentDestroy(to *Torrent) {
	btg.torrentTrace(to, "Torrent destroyed")

	to.outputRateTimer.Disarm()

	// Clean up files

	if to.fhs.lhFirst != nil {
		panic("assertion failed: to.fhs empty")
	}
	btg.torrentDestroyFiles(to)

	// Shutdown peers

	btg.peerShutdownAll(to)
	if to.runningPeers.lhFirst != nil {
		panic("assertion failed: to.running_peers empty")
	}
	if to.unchokedPeers.lhFirst != nil {
		panic("assertion failed: to.unchoked_peers empty")
	}
	if to.inactivePeers.tqFirst != nil {
		panic("assertion failed: to.inactive_peers empty")
	}
	if to.disconnectedPeers.tqFirst != nil {
		panic("assertion failed: to.disconnected_peers empty")
	}
	if to.connectFailedPeers.tqFirst != nil {
		panic("assertion failed: to.connect_failed_peers empty")
	}

	// Flush all active pieces

	for tp := to.activePieces.tqFirst; tp != nil; {
		for tb := tp.waitingBlocks.lhFirst; tb != nil; {
			btg.blockDestroy(tb)
			tb = tp.waitingBlocks.lhFirst
		}
		for tb := tp.sentBlocks.lhFirst; tb != nil; {
			btg.blockDestroy(tb)
			tb = tp.sentBlocks.lhFirst
		}

		next := tp.linkNext
		btg.torrentPieceDestroy(to, tp)
		tp = next
	}

	if to.activePiecesMem != 0 {
		panic("assertion failed: to.active_pieces_mem == 0")
	}
	if to.numActivePieces != 0 {
		panic("assertion failed: to.num_active_pieces == 0")
	}
	if to.serveOrder.lhFirst != nil {
		panic("assertion failed: to.serve_order empty")
	}

	// Unannounce on trackers

	btg.trackerRemoveTorrent(to)

	// Close file

	btg.torrentDiskioClose(to)

	// Final cleanup

	listRemove(to, toLink)

	if to.metainfo != nil {
		to.metainfo.Release()
	}
	to.cachefilePieceMap = nil
	to.cachefilePieceMapInv = nil
	to.pieceHashes = nil
}

// torrentParseInfodict — C: torrent_parse_infodict
func (btg *BtGlobal) torrentParseInfodict(to *Torrent, info *htsmsg.HTSMsg) error {
	if btg.cfg().EnableTorrentDebug.Load() {
		btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT", "%s: Decoded metadata",
			to.title)
		info.Print("BITTORRENT")
	}

	setErr := func(format string, args ...any) error {
		return fmt.Errorf(format, args...)
	}

	if name := info.GetStr("name"); name != "" {
		to.title = name
	}

	files := info.GetList("files")
	var offset uint64

	if files != nil {
		// Multi file torrent

		for _, f := range files.GetFields() {
			file := f.GetMap()
			if file == nil {
				return setErr("File is not a dict")
			}
			length, err := file.GetS64("length")
			if err != nil {
				return setErr("Missing file length")
			}

			if length < 0 {
				return setErr("Invalid file length")
			}

			paths := file.GetList("path")

			var tf *TorrentFile

			filename := ""

			for _, ff := range paths.GetFields() {
				path, ok := ff.FieldGetString()
				if !ok {
					return setErr("Path component is not a string")
				}

				if filename != "" {
					filename += "/"
				}
				filename += path

				tfq := &to.root
				if tf != nil {
					tfq = &tf.files
				}
				for tf = tfq.tqFirst; tf != nil; tf = tf.parentLinkNext {
					if tf.name == path {
						break
					}
				}
				if tf == nil {
					tf = &TorrentFile{}
					tf.torrent = to
					tailqSetup(&tf.files.tqFirst, &tf.files.tqLast)
					tf.name = path
					tailqInsertTail(&to.files.tqFirst, &to.files.tqLast,
						tf, tfTorrentLink)
					tailqInsertTail(&tfq.tqFirst, &tfq.tqLast,
						tf, tfParentLink)
					tf.fullpath = filename
				}
			}

			tf.offset = offset
			tf.size = uint64(length)
			offset += uint64(length)
		}
	} else {
		name := info.GetStr("name")

		if name == "" {
			return setErr("Missing file name")
		}

		length, err := info.GetS64("length")

		if err != nil {
			return setErr("Missing file length")
		}

		if length < 0 {
			return setErr("Invalid file length")
		}

		tf := &TorrentFile{}
		tf.torrent = to
		tailqSetup(&tf.files.tqFirst, &tf.files.tqLast)
		tf.name = name
		tailqInsertTail(&to.files.tqFirst, &to.files.tqLast,
			tf, tfTorrentLink)
		tailqInsertTail(&to.root.tqFirst, &to.root.tqLast,
			tf, tfParentLink)

		tf.offset = 0
		tf.size = uint64(length)
		offset = uint64(length)
		tf.fullpath = "/" + name
	}

	to.totalLength = offset

	to.pieceLength = int(info.GetU32OrDefault("piece length", 0))
	if to.pieceLength < 32768 || to.pieceLength > 16777216 {
		return setErr("Invalid piece length: %d", to.pieceLength)
	}

	piecesData, err := info.GetBin("pieces")
	if err != nil {
		return setErr("No hash list")
	}

	if len(piecesData)%20 != 0 {
		return setErr("Invalid hash list size: %d", len(piecesData))
	}

	to.numPieces = len(piecesData) / 20
	mapsize := to.numPieces
	to.cachefilePieceMap = make([]int32, mapsize)
	to.cachefilePieceMapInv = make([]int32, mapsize)
	for i := range to.cachefilePieceMap {
		to.cachefilePieceMap[i] = -1
		to.cachefilePieceMapInv[i] = -1
	}

	to.pieceHashes = make([]byte, len(piecesData))
	copy(to.pieceHashes, piecesData)

	return nil
}

// torrentParseTorrentfile — C: torrent_parse_torrentfile
func (btg *BtGlobal) torrentParseTorrentfile(to *Torrent, metainfo *htsmsg.HTSMsg) error {
	if al := metainfo.GetList("announce-list"); al != nil {
		for _, f := range al.GetFields() {
			l := f.GetChilds()
			if l == nil {
				continue
			}
			for _, ff := range l.GetFields() {
				if t, ok := ff.FieldGetString(); ok {
					btg.torrentAddTracker(to, t)
				}
			}
		}
	} else {
		if announce := metainfo.GetStr("announce"); announce != "" {
			btg.torrentAddTracker(to, announce)
		}
	}

	info := metainfo.GetMap("info")
	if info == nil {
		return errors.New("Missing info dict")
	}

	return btg.torrentParseInfodict(to, info)
}

// torrentLoad — C: torrent_load
func (btg *BtGlobal) torrentLoad(to *Torrent, buf []byte, offset uint64, size int,
	tfh *TorrentFh) int {
	rval := size
	// First figure out which pieces we need

	piece := int(offset / uint64(to.pieceLength))
	pieceOffset := int(offset % uint64(to.pieceLength))

	// Poor mans read-ahead
	if piece+1 < to.numPieces {
		btg.torrentPieceFind(to, piece+1)
	}
	if piece+2 < to.numPieces {
		btg.torrentPieceFind(to, piece+2)
	}
	boff := 0
	for size > 0 {

		tp := btg.torrentPieceFind(to, piece)

		listInsertHead(&tp.activeFh.lhFirst, tfh, tfhPieceLink)

		btg.pieceUpdateDeadline(to, tp)

		if !tp.hashComputed {
			btg.aio.WakeupWorker(btg.torrentPendingsSignal)

			for !tp.hashOk && !tfh.cancelled {
				btg.pieceVerified.Wait()
			}
		}

		listRemove(tfh, tfhPieceLink)
		btg.pieceUpdateDeadline(to, tp)

		if tfh.cancelled {
			return -1
		}

		copyLen := min(size, to.pieceLength-pieceOffset)

		copy(buf[boff:], tp.data[pieceOffset:pieceOffset+copyLen])

		piece++
		pieceOffset = 0
		size -= copyLen
		boff += copyLen
	}

	return rval
}

// updateInterest — C: update_interest
func (btg *BtGlobal) updateInterest(to *Torrent) {
	for p := to.runningPeers.lhFirst; p != nil; p = p.runningLinkNext {
		btg.peerUpdateInterest(to, p)
	}
}
