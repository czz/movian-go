package bittorrent

// C: src/backend/bittorrent/diskio.c — canonical 1:1 port.
// Disk cache management for torrent pieces.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"sync"
	"time"

	archpkg "github.com/czz/movian-go/internal/arch"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
	"github.com/czz/movian-go/internal/trace"
)

// btCacheMagic — C: 'bt02' multi-char constant (0x62743032 big-endian)
const btCacheMagic = 0x62743032

// diskioTrace — C: diskio_trace
func (btg *BtGlobal) diskioTrace(t *Torrent, msg string, args ...any) {
	if !btg.cfg().EnableTorrentDiskIODebug.Load() {
		return
	}
	btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT", "%s: %s", t.title,
		fmt.Sprintf(msg, args...))
}

// cacheFileSize — C: cache_file_size
func (btg *BtGlobal) cacheFileSize(to *Torrent, blocks int) uint64 {
	return uint64(blocks)*uint64(to.pieceLength) +
		uint64(to.cachefileStoreOffset)
}

// updateDiskAvail — C: update_disk_avail
func (btg *BtGlobal) updateDiskAvail() {
	path := misc.RstrDup(btg.cachePath)

	btg.mu.Unlock()
	ffi, err := facore.FAFSInfo(btg.fam,
		misc.RstrGet(path))
	btg.mu.Lock()
	if err == nil {
		btg.diskAvail = ffi.Avail
	}
	misc.RstrRelease(path)
}

// updateDiskUsage — C: update_disk_usage
func (btg *BtGlobal) updateDiskUsage() {
	var activeTotal uint64
	for to := btg.torrents.lhFirst; to != nil; to = to.linkNext {
		activeTotal += btg.cacheFileSize(to, to.totalDiskBlocks)
	}

	btg.totalBytesActive = activeTotal

	sum := int64(btg.totalBytesInactive + btg.totalBytesActive)
	limit := int64(btg.diskAvail+uint64(sum)) *
		int64(btg.freeSpacePercent) / 100

	btg.cacheLimit = uint64(limit)
	if btg.cacheLimit == 0 {
		btg.cacheLimit = 1 // C: limit ?: 1
	}
	if btg.cfg().EnableTorrentDiskIODebug.Load() {
		btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT",
			"Disk usage %d MB / %d MB (%d%%)",
			sum, btg.cacheLimit, int(sum*100/int64(btg.cacheLimit)))
	}

	r := nls.GetRString("Cached torrents use %d MB out of allowed %d MB. Total free space on volume: %d MB")

	tmp := fmt.Sprintf(r,
		int(sum/1000000),
		int(limit/1000000),
		int(btg.diskAvail/1000000))

	btg.diskStatus.SetString(tmp)
}

// torrentWriteToDisk — C: torrent_write_to_disk
func (btg *BtGlobal) torrentWriteToDisk(to *Torrent, tp *TorrentPiece) {
	btg.torrentRetain(to)
	tp.refcount++

	var mapdata [4]byte
	ok := false

	for range 2 {

		btg.updateDiskUsage()

		growth := max(to.nextDiskBlock+1-to.totalDiskBlocks, 0)

		if btg.totalBytesActive+btg.totalBytesInactive+
			uint64(growth)*uint64(to.pieceLength) >= btg.cacheLimit {

			btg.diskioTrace(to, "Write would exceed cache size, need to cleanup")
			if btg.torrentDiskioScan(false) != 0 {
				// Managed to clean up something
				continue
			}
			// Otherwise, just restart in our file 50% back
			to.nextDiskBlock /= 2
			growth = max(to.nextDiskBlock+1-to.totalDiskBlocks, 0)
		}

		location := to.nextDiskBlock
		binary.BigEndian.PutUint32(mapdata[:], uint32(to.nextDiskBlock))
		to.nextDiskBlock++

		if growth > 0 {
			btg.diskAvail -= uint64(growth) * uint64(to.pieceLength)
			to.totalDiskBlocks = to.nextDiskBlock
		}

		oldPiece := to.cachefilePieceMapInv[location]
		var oldMapOffset uint64
		if oldPiece != -1 {
			// Some other block already occupied this slot in the file
			// We need to clear that out

			oldMapOffset =
				uint64(4*oldPiece) + uint64(to.cachefileMapOffset)

			to.cachefilePieceMap[oldPiece] = -1
		}

		oldPos := to.cachefilePieceMap[tp.index]
		if oldPos != -1 {
			// Piece was already written to another location.
			// We are writing again, probably due to hash corruption.
			// Clear out inverse table info
			to.cachefilePieceMapInv[oldPos] = -1
		}

		to.cachefilePieceMap[tp.index] = int32(location)
		to.cachefilePieceMapInv[location] = int32(tp.index)

		dataOffset := uint64(location)*uint64(to.pieceLength) +
			uint64(to.cachefileStoreOffset)

		mapOffset := uint64(4*tp.index) + uint64(to.cachefileMapOffset)

		btg.mu.Unlock()

		if n, err := facore.FASeek(to.cachefile, int64(dataOffset), 0); err == nil && n == int64(dataOffset) {
			if w, err := facore.FAWrite(to.cachefile, tp.data[:tp.pieceLength]); err == nil && w == tp.pieceLength {

				if n, err := facore.FASeek(to.cachefile, int64(mapOffset), 0); err == nil && n == int64(mapOffset) {

					if w, err := facore.FAWrite(to.cachefile, mapdata[:]); err == nil && w == 4 {
						ok = true
					}
				}
			}
		}

		if oldMapOffset != 0 {
			if n, err := facore.FASeek(to.cachefile, int64(oldMapOffset), 0); err == nil && n == int64(oldMapOffset) {
				mapdata[0] = 0xff
				mapdata[1] = 0xff
				mapdata[2] = 0xff
				mapdata[3] = 0xff
				facore.FAWrite(to.cachefile, mapdata[:])
			}
		}

		btg.mu.Lock()

		res := "FAIL"
		if ok {
			res = "OK"
		}
		btg.diskioTrace(to, "Wrote piece %d to disk at %d (%d). Result: %s",
			tp.index, location, dataOffset, res)
		break
	}

	if ok {
		tp.onDisk = true
	} else {
		tp.diskFail = true
	}

	btg.torrentPieceRelease(tp)
	btg.torrentRelease(to)
}

// torrentReadFromDisk — C: torrent_read_from_disk
func (btg *BtGlobal) torrentReadFromDisk(to *Torrent, tp *TorrentPiece) {
	btg.torrentRetain(to)
	tp.refcount++

	idx := to.cachefilePieceMap[tp.index]
	var ok bool

	if idx >= 0 {
		dataOffset := uint64(idx)*uint64(to.pieceLength) +
			uint64(to.cachefileStoreOffset)

		btg.mu.Unlock()
		facore.FASeek(to.cachefile, int64(dataOffset), 0)
		length, _ := facore.FARead(to.cachefile, tp.data[:tp.pieceLength])
		btg.mu.Lock()
		ok = length == tp.pieceLength

		res := "FAIL"
		if ok {
			res = "OK"
		}
		btg.diskioTrace(to, "Load piece %d from disk: %s", tp.index, res)
	} else {
		// Piece no longer exist on disk. We fail silently here and just
		// let the torrent streamer reload it
		ok = false
	}

	tp.loadReq = false

	if ok {
		tp.complete = true
		tp.onDisk = true
		btg.torrentHashWakeup()
	} else {
		tp.loadfail = true
		to.loadfail = true
	}
	btg.torrentPieceRelease(tp)
	btg.torrentRelease(to)
}

// btDiskioThread — C: bt_diskio_thread
func (btg *BtGlobal) btDiskioThread(aux any) any {
	btg.mu.Lock()

	for {
	restart:
		btg.updateDiskAvail()

		for to := btg.torrents.lhFirst; to != nil; to = to.linkNext {
			if to.cachefile == nil {
				continue
			}

			for tp := to.activePieces.tqFirst; tp != nil; tp = tp.linkNext {
				if tp.loadReq {
					btg.torrentReadFromDisk(to, tp)
					goto restart
				}

				if tp.hashOk && !tp.onDisk && !tp.diskFail {
					btg.torrentWriteToDisk(to, tp)
					goto restart
				}
			}
		}

		if btg.condWaitTimeout(btg.pieceIONeeded, 60000) {
			break
		}
	}

	btg.torrentWriteThreadRunning = false
	btg.mu.Unlock()
	return nil
}

// condWaitTimeout — C: hts_cond_wait_timeout.
// Caller must hold the cond's mutex. Returns true on timeout.
func (btg *BtGlobal) condWaitTimeout(c *sync.Cond, timeoutMs int) bool {
	timedOut := false
	timer := time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
		c.L.Lock()
		timedOut = true
		c.Broadcast()
		c.L.Unlock()
	})
	c.Wait()
	timer.Stop()
	return timedOut
}

// torrentDiskioWakeup — C: torrent_diskio_wakeup
func (btg *BtGlobal) torrentDiskioWakeup() {
	if !btg.torrentWriteThreadRunning {
		btg.torrentWriteThreadRunning = true
		archpkg.ThreadCreateDetached("btdiskio", btg.btDiskioThread, nil,
			archpkg.ThreadPrioBgtask)
	}
	btg.pieceIONeeded.Signal()
}

// torrentDiskioVerify — C: torrent_diskio_verify
func (btg *BtGlobal) torrentDiskioVerify(to *Torrent) int {
	fh := to.cachefile

	facore.FASeek(fh, 0, 0)
	size, _ := facore.FSize(fh)

	var tmp [8]byte

	if size < int64(len(tmp)) {
		return -1
	}

	if n, _ := facore.FARead(fh, tmp[:]); n != len(tmp) {
		btg.diskioTrace(to, "Unable to read bencode size")
		return -1
	}

	magic := binary.BigEndian.Uint32(tmp[:])
	if magic != btCacheMagic {
		btg.diskioTrace(to, "Bad magic 0x%08x", magic)
		return -1
	}

	bencodesize := int(binary.BigEndian.Uint32(tmp[4:]))
	btg.diskioTrace(to, "Size of metainfo: %d", bencodesize)

	if size < int64(bencodesize+len(tmp)) || bencodesize > 1024*1024 {
		btg.diskioTrace(to, "Bad bencode size %d for filesize %d",
			bencodesize, size)
		return -1
	}

	b := misc.BufCreate(bencodesize)
	if n, _ := facore.FARead(fh, b.C8()[:bencodesize]); n != bencodesize {
		btg.diskioTrace(to, "Unable to read metainto")
		b.Release()
		return -1
	}

	var infoHash [20]byte

	doc, _, derr := BencodeDeserialize(b.C8(),
		btg.torrentExtractInfoHash, infoHash[:])

	b.Release()
	if doc == nil {
		btg.diskioTrace(to, "Unable to parse metainto: %s", derr)
		return -1
	}

	doc.Release()

	if !bytes.Equal(infoHash[:], to.infoHash[:]) {
		btg.diskioTrace(to, "Metainfo hash mismatch for file on disk")
		return -1
	}

	mapsize := to.numPieces * 4

	mapBuf := make([]byte, mapsize)
	if n, _ := facore.FARead(fh, mapBuf); n != mapsize {
		btg.diskioTrace(to, "Unable to read piece flags, clearing all on-disk pieces")
		for i := range to.cachefilePieceMap {
			to.cachefilePieceMap[i] = -1
		}
	} else {
		for i := range to.numPieces {
			to.cachefilePieceMap[i] =
				int32(binary.BigEndian.Uint32(mapBuf[i*4:]))
		}
	}

	for i := range to.cachefilePieceMapInv {
		to.cachefilePieceMapInv[i] = -1
	}

	cnt := 0
	maxBlock := -1
	for i := range to.numPieces {
		location := to.cachefilePieceMap[i]

		if uint32(location) >= uint32(to.numPieces) {
			location = -1
		}

		to.cachefilePieceMap[i] = location
		if to.cachefilePieceMap[i] != -1 {
			cnt++
			if int(location) > maxBlock {
				maxBlock = int(location)
			}
			to.cachefilePieceMapInv[location] = int32(i)
		}
	}

	to.nextDiskBlock = maxBlock + 1
	to.totalDiskBlocks = to.nextDiskBlock

	btg.diskioTrace(to, "%d pieces valid on disk", cnt)

	to.cachefileMapOffset = 8 + bencodesize
	to.cachefileStoreOffset =
		to.cachefileMapOffset + to.numPieces*4

	return 0
}

// torrentDiskioOpen — C: torrent_diskio_open
func (btg *BtGlobal) torrentDiskioOpen(to *Torrent) {
	var str [41]byte
	misc.Bin2hex(str[:], len(str), to.infoHash[:], 20)

	path := fmt.Sprintf("%s/%s.tc",
		misc.RstrGet(btg.cachePath), string(str[:40]))

	facore.FAMakedirs(btg.fam,
		misc.RstrGet(btg.cachePath))

	if to.cachefile != nil {
		panic("assertion failed: to.cachefile == nil")
	}

	h, oerr := facore.FAOpenEx(btg.fam, path,
		facore.FaAppend|facore.FaWrite, nil)
	to.cachefile = h
	if to.cachefile == nil || oerr != nil {
		btg.ts.Trace(trace.TRACE_ERROR, "BITTORRENT",
			"Unable to open cache file %s -- %v", path, oerr)
		return
	}

	if btg.torrentDiskioVerify(to) == 0 {
		btg.diskioTrace(to, "File %s seems valid", path)
	} else {

		facore.FASeek(to.cachefile, 0, 0)
		var tmp [8]byte
		binary.BigEndian.PutUint32(tmp[:], btCacheMagic)
		binary.BigEndian.PutUint32(tmp[4:], uint32(to.metainfo.Size()))

		if n, werr := facore.FAWrite(to.cachefile, tmp[:]); werr != nil || n != 8 {
			goto fail
		}

		{
			bencodesize := to.metainfo.Size()

			if n, werr := facore.FAWrite(to.cachefile,
				to.metainfo.C8()[:bencodesize]); werr != nil || n != bencodesize {
				goto fail
			}

			mapsize := to.numPieces * 4

			ff := make([]byte, mapsize)
			for i := range ff {
				ff[i] = 0xff
			}

			n, werr := facore.FAWrite(to.cachefile, ff)
			if werr != nil || n != mapsize {
				goto fail
			}

			to.cachefileMapOffset = 8 + bencodesize
			to.cachefileStoreOffset =
				to.cachefileMapOffset + to.numPieces*4

			to.nextDiskBlock = 0
			btg.diskioTrace(to, "New disk cache initialized at %s", path)
		}
	}
	btg.diskioTrace(to, "Disk offsets: map:0x%x store:0x%x next block stored at %d",
		to.cachefileMapOffset,
		to.cachefileStoreOffset,
		to.nextDiskBlock)
	return

fail:
	btg.ts.Trace(trace.TRACE_ERROR, "BITTORRENT",
		"%s: Unable to create cachefile, running without diskcache",
		to.title)
	facore.FAClose(to.cachefile)
	to.cachefile = nil
}

// torrentDiskioClose — C: torrent_diskio_close
func (btg *BtGlobal) torrentDiskioClose(to *Torrent) {
	if to.cachefile == nil {
		return
	}

	facore.FAClose(to.cachefile)
	to.cachefile = nil
}

// C: typedef struct scanned_file
type scannedFile struct {
	url      *misc.Rstr // C: sf_url
	infoHash [20]byte   // C: sf_info_hash

	size  int64     // C: sf_size
	mtime time.Time // C: sf_mtime

	active bool // C: sf_active
}

// torrentDiskioScan — C: torrent_diskio_scan
func (btg *BtGlobal) torrentDiskioScan(forceFlush bool) int {
	var sfl []*scannedFile
	var tmp [41]byte

	if btg.cachePath == nil {
		return 0
	}

	btg.updateDiskAvail()

	path := misc.RstrDup(btg.cachePath)

	btg.mu.Unlock()

	fam := btg.fam
	fd, _ := facore.FAScandir(fam, misc.RstrGet(path), 0)
	tmp[40] = 0
	if fd != nil {
		for _, fde := range fd.Entries {
			fname := fde.Filename
			if len(fname) != 43 { // [40char sha].tc
				continue
			}

			if fname[40:] != ".tc" {
				continue
			}
			copy(tmp[:40], fname)
			sf := &scannedFile{}
			if misc.Hex2bin(sf.infoHash[:], 20, string(tmp[:40])) != 20 {
				continue
			}

			if !fde.StatDone {
				if st, serr := facore.Stat(fam, fde.URL); serr == nil {
					fde.Stat = *st
					fde.StatDone = true
				}
			}

			sf.size = fde.Stat.Size
			sf.mtime = fde.Stat.MTime
			sf.url = misc.RstrAllocStr(fde.URL)
			sfl = append(sfl, sf)
		}
		slices.SortStableFunc(sfl, func(a, b *scannedFile) int { return a.mtime.Compare(b.mtime) })

		fd.Free()
	}

	btg.mu.Lock()

	if fd == nil {
		misc.RstrRelease(path)
		return 0
	}

	btg.totalBytesInactive = 0

	for _, sf := range sfl {
		to := btg.torrentFindByHash(sf.infoHash[:])
		if to != nil {
			sf.active = true
		} else {
			btg.totalBytesInactive += uint64(sf.size)
		}

		if btg.cfg().EnableTorrentDiskIODebug.Load() {
			btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT",
				"%s: %d bytes %d seconds old",
				misc.RstrGet(sf.url),
				sf.size, int(time.Since(sf.mtime).Seconds()))
		}
	}

	if btg.cfg().EnableTorrentDiskIODebug.Load() {
		btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT",
			"Disk usage: Active: %d MB, Inactive: %d MB",
			btg.totalBytesActive/1000000,
			btg.totalBytesInactive/1000000)
	}

	btg.updateDiskUsage()
	rval := 0
	for _, sf := range sfl {
		// Delete inactive torrents that exceed max limit

		if !sf.active {

			if forceFlush ||
				btg.totalBytesActive+btg.totalBytesInactive >=
					btg.cacheLimit {
				if uerr := facore.FAUnlink(fam, misc.RstrGet(sf.url)); uerr != nil {
					btg.ts.Trace(trace.TRACE_ERROR, "BITTORRENT",
						"Unable to delete %s from cache -- %v",
						misc.RstrGet(sf.url), uerr)
					rval = 1
				} else {
					btg.totalBytesInactive -= uint64(sf.size)

					if btg.cfg().EnableTorrentDiskIODebug.Load() {
						btg.ts.Trace(trace.TRACE_DEBUG, "BITTORRENT",
							"Removed %s (%d bytes) from cache",
							misc.RstrGet(sf.url), sf.size)
					}
				}
			}
		}

		misc.RstrRelease(sf.url)
	}
	misc.RstrRelease(path)
	btg.updateDiskUsage()
	return rval
}

// torrentDiskioCacheClear — C: torrent_diskio_cache_clear
func (btg *BtGlobal) torrentDiskioCacheClear() {
	btg.torrentDiskioScan(true)
}

// torrentDiskioLoadInfofileFromHash — C: torrent_diskio_load_infofile_from_hash
func (btg *BtGlobal) torrentDiskioLoadInfofileFromHash(reqHash []byte) *misc.Buf {
	var str [41]byte
	misc.Bin2hex(str[:], len(str), reqHash, 20)

	path := fmt.Sprintf("%s/%s.tc",
		misc.RstrGet(btg.cachePath), string(str[:40]))

	fh, err := facore.FAOpenEx(btg.fam, path,
		0, nil)
	if fh == nil || err != nil {
		return nil
	}

	var tmp [8]byte

	if n, _ := facore.FARead(fh, tmp[:]); n != len(tmp) {
		goto bad
	}

	{
		magic := binary.BigEndian.Uint32(tmp[:])
		if magic != btCacheMagic {
			goto bad
		}

		bencodesize := int(binary.BigEndian.Uint32(tmp[4:]))

		if bencodesize > 1024*1024 {
			goto bad
		}

		b := misc.BufCreate(bencodesize)
		var doc *htsmsg.HTSMsg
		var diskHash [20]byte
		if n, _ := facore.FARead(fh, b.C8()[:bencodesize]); n != bencodesize {
			goto bad2
		}

		doc, _, _ = BencodeDeserialize(b.C8(),
			btg.torrentExtractInfoHash, diskHash[:])

		if doc == nil {
			goto bad2
		}

		doc.Release()

		if !bytes.Equal(reqHash, diskHash[:]) {
			goto bad2
		}

		facore.FAClose(fh)
		return b

	bad2:
		b.Release()
	}
bad:
	facore.FAClose(fh)
	return nil
}
