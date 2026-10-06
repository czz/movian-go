// Canonical port of src/fileaccess/fa_buffer.c — buffered read wrapper.
// FA_BUFFERED_SMALL/FA_BUFFERED_BIG opens wrap the real handle in a
// readahead cache: 1 MiB ring buffer, 8 zone map, single-slot parking
// (5 s callout) for same-URL reopen.
package fileaccess

import (
	"errors"
	"io"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/misc"
)

// C: BF_ZONES / BF_MASK (fa_buffer.c)
const (
	bfZones = 8
	bfMask  = bfZones - 1
)

// C: FAP_ALLOW_CACHE / FAP_INCLUDE_PROTO_IN_URL (fa_proto.h:32-33)
const (
	FAPIncludeProtoInURL = 0x1
	FAPAllowCache        = 0x2
)

// C: static HTS_MUTEX_DECL(buffered_global_mutex)
// C: buffered_global_mutex + bf_parked + parked callout — grouped state
// C: buffered_global_mutex + bf_parked + parked callout → fam.bf

// C: buffered_zone_t
type bufferedZone struct {
	fpos int64 // C: bz_fpos — file position of zone start
	mpos int   // C: bz_mpos — offset into bf_mem
	size int   // C: bz_size
}

// C: buffered_file_t (fa_buffer.c)
type bufferedFile struct {
	fam *FileAccessManager // C: implicit global fa context
	src *Handle            // C: bf_src

	outboundCancellable *misc.Cancellable // C: bf_outbound_cancellable
	inboundCancellable  *misc.Cancellable // C: bf_inbound_cancellable

	mem        []byte // C: bf_mem (halloc'd 1 MiB ring)
	memSize    int    // C: bf_mem_size
	minRequest int    // C: bf_min_request

	memPtr int // C: bf_mem_ptr — ring write cursor

	fpos       int64 // C: bf_fpos
	replacePtr int   // C: bf_replace_ptr

	size  int64 // C: bf_size (-1 until EOF known)
	flags int   // C: bf_flags

	url   string // C: bf_url
	zones [bfZones]bufferedZone
}

// eraseZone — C: erase_zone (fa_buffer.c:97). Removes [mpos,mpos+size)
// from the zone map (trim head-aligned overlap, drop others).
func eraseZone(bf *bufferedFile, mpos, size int) {
	for i := range bfZones {
		bz := &bf.zones[i]
		if bz.size == 0 {
			continue
		}
		if mpos < bz.mpos+bz.size && bz.mpos < mpos+size {
			if mpos == bz.mpos {
				s0 := min(bz.size, size)
				bz.fpos += int64(s0)
				bz.mpos += s0
				bz.size -= s0
				mpos += s0
				size -= s0
			} else {
				bz.size = 0
			}
		}
	}
}

// mapZone — C: map_zone (fa_buffer.c:138). Extend a zone if contiguous,
// else take a free slot or round-robin evict.
func mapZone(bf *bufferedFile, mpos, size int, fpos int64) {
	j := bfZones
	for i := range bfZones {
		bz := &bf.zones[i]
		if bz.size == 0 {
			if i < j {
				j = i
			}
			continue
		}
		if bz.fpos+int64(bz.size) == fpos && bz.mpos+bz.size == mpos {
			// extend up
			bz.size += size
			return
		}
	}
	if j == bfZones {
		bf.replacePtr = (bf.replacePtr + 1) & bfMask
		j = bf.replacePtr
	}
	bz := &bf.zones[j]
	bz.fpos = fpos
	bz.mpos = mpos
	bz.size = size
}

// resolveZone — C: resolve_zone (fa_buffer.c:191). Returns bytes served
// from cache at fpos (≤size) and the memory offset, or -1 on miss.
func resolveZone(bf *bufferedFile, fpos int64, size int, mpos *int) int {
	for i := range bfZones {
		bz := &bf.zones[i]
		if bz.size == 0 {
			continue
		}
		if fpos >= bz.fpos && fpos < bz.fpos+int64(bz.size) {
			d := int(fpos - bz.fpos)
			*mpos = bz.mpos + d
			r := bz.size - d
			if size < r {
				return size
			}
			return r
		}
	}
	return -1
}

// needToFill — C: need_to_fill (fa_buffer.c:212). Bytes until the next
// mapped zone (don't read past a cached region).
func needToFill(bf *bufferedFile, fpos int64, rd int) int {
	var d int64 = 1<<62 - 1
	for i := range bfZones {
		bz := &bf.zones[i]
		if bz.size == 0 {
			continue
		}
		if bz.fpos >= fpos && bz.fpos-fpos < d {
			d = bz.fpos - fpos
		}
	}
	if int64(rd) < d {
		return rd
	}
	return int(d)
}

// fabDestroy — C: fab_destroy (fa_buffer.c:233)
func fabDestroy(bf *bufferedFile) {
	bf.src.Close()
	bf.mem = nil
	misc.CancellableRelease(bf.outboundCancellable)
}

// closeParkedFile — C: close_parked_file (fa_buffer.c:250)
func (fam *FileAccessManager) closeParkedFile(c *callout.Callout, aux any) {
	fam.bf.mu.Lock()
	closeme := fam.bf.parked
	fam.bf.parked = nil
	fam.bf.mu.Unlock()
	if closeme != nil {
		fabDestroy(closeme)
	}
}

// fabPark — C: fab_park (fa_buffer.c:270). Single global slot; displaced
// park is destroyed after the swap; 5 s callout reaps the parked file.
func fabPark(fh *Handle) {
	bf := fhOpaque(fh).(*bufferedFile)
	misc.CancellableUnbind(bf.inboundCancellable, bf)
	bf.inboundCancellable = nil

	var closeme *bufferedFile
	src := bf.src

	srcNoParking := src.fap != nil && src.fap.NoParking != nil &&
		src.fap.NoParking(src)
	if srcNoParking || bf.flags&FaNoParking != 0 ||
		misc.CancellableIsCancelled(bf.outboundCancellable) != 0 {
		fabDestroy(bf)
		return
	}

	fam := bf.fam   // owning fa context (C: global bf_parked)
	if fam == nil { // unwired handle — C: parked slot was global
		return
	}
	fam.bf.mu.Lock()
	if fam.bf.parked != nil {
		closeme = fam.bf.parked
	}
	fam.bf.parked = bf
	fam.bf.mu.Unlock()
	if cs := fam.calloutSystem; cs != nil {
		cs.Arm(&fam.bf.parkedCallout, fam.closeParkedFile, nil, 5)
	}
	if closeme != nil {
		fabDestroy(closeme)
	}
}

// Close — C: fab_close (fa_buffer.c:299), reached via Handle.reader.Close
// (fap_close on fa_protocol_buffered).
func (bf *bufferedFile) Close() error {
	misc.CancellableUnbind(bf.inboundCancellable, bf)
	bf.inboundCancellable = nil
	fabDestroy(bf)
	return nil
}

// Seek — io.Seeker form (lazy=false); the canonical lazy variant is Seek4.
func (bf *bufferedFile) Seek(pos int64, whence int) (int64, error) {
	return bf.Seek4(pos, whence, false)
}

// fabSeek — C: fab_seek (fa_buffer.c:310). Mapped seeks are free; unmapped
// seeks verify reachability by seeking the source. C passes `lazy`
// through to fap_seek(src, pos, whence, lazy).
func (bf *bufferedFile) Seek4(pos int64, whence int, lazy bool) (int64, error) {
	src := bf.src
	var np int64
	switch whence {
	case io.SeekStart:
		np = pos
	case io.SeekCurrent:
		np = bf.fpos + pos
	case io.SeekEnd:
		// C: np = fap_seek(src, pos, SEEK_END, lazy) — passthrough
		v, err := Seek4(src, pos, io.SeekEnd, lazy)
		if err != nil {
			return -1, err
		}
		np = v
	default:
		return -1, errors.New("buffered: invalid whence")
	}
	if np < 0 {
		return -1, errors.New("buffered: negative position")
	}

	var mpos int
	if resolveZone(bf, np, 1, &mpos) == -1 {
		// Not mapped — verify the source can reach it.
		if v, err := Seek4(src, np, io.SeekStart, lazy); err != nil || v != np {
			return -1, errors.New("buffered: source seek failed")
		}
	}
	bf.fpos = np
	return np, nil
}

// fabFsize — C: fab_fsize (fa_buffer.c:344)
func (bf *bufferedFile) fabFsize() int64 {
	if bf.size != -1 {
		return bf.size
	}
	bf.size = bf.src.Size()
	return bf.size
}

// storeInCache — C: store_in_cache (fa_buffer.c:357). Ring-buffer write
// with wraparound; erases overwritten zones then maps the new one.
func storeInCache(bf *bufferedFile, buf []byte) {
	size := len(buf)
	if size > bf.memSize {
		return
	}
	s1 := size
	s2 := 0
	if bf.memPtr+s1 > bf.memSize {
		s1 = bf.memSize - bf.memPtr
		s2 = size - s1
	}

	eraseZone(bf, bf.memPtr, s1)
	mapZone(bf, bf.memPtr, s1, bf.fpos)
	copy(bf.mem[bf.memPtr:bf.memPtr+s1], buf[:s1])

	bf.memPtr += s1
	if bf.memPtr == bf.memSize {
		bf.memPtr = 0
	}
	if s2 > 0 {
		eraseZone(bf, bf.memPtr, s2)
		mapZone(bf, bf.memPtr, s2, bf.fpos+int64(s1))
		copy(bf.mem[bf.memPtr:bf.memPtr+s2], buf[s1:s1+s2])
		bf.memPtr += s2
	}
}

// Read — C: fab_read (fa_buffer.c:425). Zone-cached read: serve hits,
// direct-read big requests, prefetch min_request for small ones.
func (bf *bufferedFile) Read(buf []byte) (int, error) {
	src := bf.src

	if bf.mem == nil {
		bf.mem = make([]byte, bf.memSize)
	}

	size := len(buf)
	if bf.size != -1 && bf.fpos+int64(size) > bf.size {
		size = int(bf.size - bf.fpos)
	}
	if size <= 0 {
		return 0, io.EOF
	}

	rval := 0
	for size > 0 {
		mpos := -1
		if cs := resolveZone(bf, bf.fpos, size, &mpos); cs > 0 {
			copy(buf[rval:], bf.mem[mpos:mpos+cs])
			rval += cs
			bf.fpos += int64(cs)
			size -= cs
			continue
		}

		rreq := needToFill(bf, bf.fpos, size)
		if rreq >= bf.minRequest {
			if v, err := src.Seek(bf.fpos, io.SeekStart); err != nil || v != bf.fpos {
				// C: return -1 — error discards accumulated rval
				return 0, errors.New("buffered: source seek failed")
			}
			r, err := readFull(src, buf[rval:rval+rreq])
			if r > 0 {
				storeInCache(bf, buf[rval:rval+r])
				rval += r
				bf.fpos += int64(r)
				size -= r
			}
			if r != rreq {
				// C: bf->bf_size = bf->bf_fpos; return r < 0 ? r : rval;
				bf.size = bf.fpos
				if err != nil {
					return 0, err
				}
				return rval, nil
			}
			continue
		}

		if bf.memPtr+bf.minRequest > bf.memSize {
			bf.memPtr = 0
		}
		eraseZone(bf, bf.memPtr, bf.minRequest)

		if v, err := src.Seek(bf.fpos, io.SeekStart); err != nil || v != bf.fpos {
			// C: return -1 — error discards accumulated rval
			return 0, errors.New("buffered: source seek failed")
		}
		r, err := readFull(src, bf.mem[bf.memPtr:bf.memPtr+bf.minRequest])
		if r < 1 {
			bf.size = bf.fpos
			if err != nil {
				return 0, err
			}
			return rval, nil
		}

		mapZone(bf, bf.memPtr, r, bf.fpos)

		if r != bf.minRequest {
			// EOF
			bf.size = bf.fpos + int64(r)
			r2 := min(size, r)
			copy(buf[rval:], bf.mem[bf.memPtr:bf.memPtr+r2])
			bf.memPtr += r
			rval += r2
			bf.fpos += int64(r2)
			return rval, nil
		}
		bf.memPtr += r
	}
	return rval, nil
}

// readFull — C's fap_read fills as much as available; emulate the exact
// count semantics with io.ReadFull (ErrUnexpectedEOF → short count).
func readFull(h *Handle, buf []byte) (int, error) {
	n, err := io.ReadFull(h, buf)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return n, nil
	}
	if errors.Is(err, io.EOF) && n == 0 {
		return 0, nil
	}
	if err != nil {
		return n, err
	}
	return n, nil
}

// fabDeadline — C: fab_set_read_timeout (fa_buffer.c:556). Propagates the
// read timeout to the inner handle's protocol.
func fabDeadline(fh *Handle, ms int) {
	bf := fhOpaque(fh).(*bufferedFile)
	src := bf.src
	if src.fap != nil && src.fap.Deadline != nil {
		src.fap.Deadline(src, ms)
	}
}

// fabCancel — C: fab_cancel (fa_buffer.c:617). Inbound cancel → cancel
// the outbound cancellable handed to the inner open.
func fabCancel(aux any) {
	bf := aux.(*bufferedFile)
	misc.CancellableCancelLocked(bf.outboundCancellable)
}

// faProtocolBuffered — C: fa_protocol_buffered (fa_buffer.c:565)
var faProtocolBuffered = &FAProtocol{
	Name:     "buffer",
	Park:     fabPark,
	Deadline: fabDeadline,
}

// fhOpaque extracts the opaque impl stored on a buffered Handle's reader.
func fhOpaque(fh *Handle) any { return fh.reader }

// FABufferedOpen — C: fa_buffered_open (fa_buffer.c:621).
// Resolves the protocol; if it lacks FAP_ALLOW_CACHE the file opens
// directly. Otherwise reuses the single parked file for the same URL or
// wraps a fresh handle in the readahead cache.
func FABufferedOpen(fam *FileAccessManager, url string, flags int,
	extra *OpenExtra) (*Handle, error) {

	proto, _, err := fam.FAResolveProto(url)
	if err != nil {
		return nil, err
	}

	if proto.Flags&FAPAllowCache == 0 {
		fapRelease(proto)
		// C: fh = fap->fap_open(...) — direct open, no wrapper.
		return OpenEx(fam, url, extra, flags)
	}
	fapRelease(proto)

	fam.bf.mu.Lock()
	if fam.bf.parked != nil && fam.bf.parked.url == url {
		fam.bf.parked.fpos = 0
		bf := fam.bf.parked
		fam.bf.parked = nil
		fam.bf.mu.Unlock()

		fh := bfHandle(bf)
		if extra != nil && extra.Cancellable != nil {
			if c, ok := extra.Cancellable.(*misc.Cancellable); ok {
				bf.inboundCancellable =
					misc.CancellableBind(c, fabCancel, bf)
			}
		}
		return fh, nil
	}
	fam.bf.mu.Unlock()

	mflags := flags
	flags &^= FaBufferedSmall | FaBufferedBig | FaBufferedNoPrefetch

	bf := &bufferedFile{size: -1, fam: fam}
	bf.outboundCancellable = misc.CancellableCreate()

	// C: foe->foe_cancellable = bf->bf_outbound_cancellable — the inner
	// open observes the outbound cancellable.
	innerExtra := &OpenExtra{}
	if extra != nil {
		*innerExtra = *extra
		if c, ok := extra.Cancellable.(*misc.Cancellable); ok && c != nil {
			bf.inboundCancellable = misc.CancellableBind(c, fabCancel, bf)
		}
	}
	innerExtra.Cancellable = bf.outboundCancellable

	fh, oerr := OpenEx(fam, url, innerExtra, flags)
	if fh == nil || oerr != nil {
		misc.CancellableUnbind(bf.inboundCancellable, bf)
		misc.CancellableRelease(bf.outboundCancellable)
		return nil, oerr
	}

	bf.url = url
	if mflags&FaBufferedNoPrefetch == 0 {
		if mflags&FaBufferedBig != 0 {
			bf.minRequest = 256 * 1024
		} else {
			bf.minRequest = 64 * 1024
		}
	}
	bf.memSize = 1024 * 1024
	bf.flags = flags
	bf.src = fh

	return bfHandle(bf), nil
}

// bfHandle wraps a bufferedFile in a Handle (C: &bf->h with fh_proto =
// fa_protocol_buffered).
func bfHandle(bf *bufferedFile) *Handle {
	h := &Handle{
		fam:    bf.fam,
		fap:    faProtocolBuffered,
		reader: bf,
		seeker: bf,
		url:    bf.url,
		size:   -1,
	}
	h.sizer = bf.fabFsize
	return h
}
