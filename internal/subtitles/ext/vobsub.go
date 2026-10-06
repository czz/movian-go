package ext

// Canonical port of src/subtitles/vobsub.c — the VobSub (.idx/.sub)
// external-subtitle loader. The palette/size decoders and the probe live
// elsewhere for layering reasons: vobsub_probe sits in pkg/subtitles
// (called by the subtitles.c scanner) and VobsubDecodePalette /
// VobsubDecodeSize in pkg/media/core (used by dvdspu.c's codec create).

import (
	"encoding/binary"
	"math"
	"sync"

	"github.com/czz/movian-go/internal/misc"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/subtitles"
)

// vobsubEntry — C: vobsub_entry_t (vs_entries TAILQ)
type vobsubEntry struct {
	pts  int64 // C: ve_pts
	fpos int   // C: ve_fpos
}

// vobsubCmd — C: vobsub_cmd_t (vs_cmds TAILQ)
type vobsubCmd struct {
	start int   // C: vc_start
	size  int   // C: vc_size
	pts   int64 // C: vc_pts
}

// Vobsub — C: vobsub_t
type Vobsub struct {
	subtitles.ExtSubtitles                        // C: vs_es (embedded first member)
	sub                    *fileaccesscore.Handle // C: vs_sub
	entries                []*vobsubEntry         // C: vs_entries
	cur                    *vobsubEntry           // C: vs_cur
	stop                   int                    // C: vs_stop
	parser                 *libav.AvParser        // C: vs_parser — AVCodecParserContext
	ctx                    *libav.AVCodecContext  // C: vs_ctx
	clut                   [16]uint32             // C: vs_clut
	width                  int                    // C: vs_width
	height                 int                    // C: vs_height

	mu   sync.Mutex   // C: vs_mutex
	cond *sync.Cond   // C: vs_cond
	cmds []*vobsubCmd // C: vs_cmds

	mp   *mediacore.MediaPipe // C: vs_mp
	done chan struct{}        // thread join (C: vs_tid / hts_thread_join)
}

// veStop — C: ve_stop — pts of the next entry or INT64_MAX.
func veStop(entries []*vobsubEntry, ve *vobsubEntry) int64 {
	for i, e := range entries {
		if e == ve {
			if i+1 < len(entries) {
				return entries[i+1].pts
			}
			break
		}
	}
	return math.MaxInt64
}

// pesReader — Go cursor emulating C's getu8/getu16/getu32/getpts
// macros which advance the buffer pointer and shrink len.
type pesReader struct {
	b []byte
	l int
}

func (r *pesReader) getu8() int {
	v := int(r.b[0])
	r.b = r.b[1:]
	r.l--
	return v
}

func (r *pesReader) getu16() int {
	v := int(r.b[0])<<8 | int(r.b[1])
	r.b = r.b[2:]
	r.l -= 2
	return v
}

func (r *pesReader) getu32() int {
	v := int(r.b[0])<<24 | int(r.b[1])<<16 | int(r.b[2])<<8 | int(r.b[3])
	r.b = r.b[4:]
	r.l -= 4
	return v
}

// getpts — C: getpts macro (5-byte MPEG PTS)
func (r *pesReader) getpts() int64 {
	pts := int64((r.getu8()>>1)&0x07) << 30
	pts |= int64(r.getu16()>>1) << 15
	pts |= int64(r.getu16() >> 1)
	return pts
}

// demuxPES — C: demux_pes — parses one PES packet and feeds the
// payload through the DVD subtitle parser; complete packets are
// enqueued as MB_CTRL_DVD_SPU2 buffers (18×u32 header + payload).
func (vs *Vobsub) demuxPES(mp *mediacore.MediaPipe, sc uint32,
	buf []byte, pts int64) {

	dts := int64(mediacore.PTSUnset)
	r := &pesReader{b: buf, l: len(buf)}

	x := r.getu8()
	flags := r.getu8()
	hlen := r.getu8()

	if r.l < hlen {
		return
	}

	if x&0xc0 != 0x80 {
		// no MPEG 2 PES
		return
	}

	if flags&0xc0 == 0xc0 {
		if hlen < 10 {
			return
		}
		pts = r.getpts()
		dts = r.getpts()
		hlen -= 10
	} else if flags&0xc0 == 0x80 {
		if hlen < 5 {
			return
		}
		pts = r.getpts()
		dts = pts
		hlen -= 5
	}

	r.b = r.b[hlen:]
	r.l -= hlen

	if sc == 0x1bd {
		if r.l < 1 {
			return
		}
		sc = uint32(r.getu8())
	}

	if sc < 0x20 || sc > 0x3f {
		return
	}

	for r.l > 0 {
		outbuf, rlen := vs.parser.Parse2(vs.ctx,
			r.b, pts, dts)
		if len(outbuf) > 0 {
			mb := mediacore.MediaBufAllocUnlocked(mp, len(outbuf)+18*4)
			mb.DataType = int(mediacore.MBCtrlDVDSPU2)
			mb.DTS = dts
			mb.PTS = pts
			// C: d[16]=width; d[17]=height; memcpy(data, clut, 64)
			// — native-endian uint32 words, as the C cast does.
			d := mb.Data
			for i := range 16 {
				binary.NativeEndian.PutUint32(d[i*4:], vs.clut[i])
			}
			binary.NativeEndian.PutUint32(d[16*4:], uint32(vs.width))
			binary.NativeEndian.PutUint32(d[17*4:], uint32(vs.height))
			copy(d[18*4:], outbuf)
			mediacore.MbEnqueueAlways(mp, mp.Video, mb)
		}
		pts = int64(mediacore.PTSUnset)
		dts = int64(mediacore.PTSUnset)
		r.b = r.b[rlen:]
		r.l -= rlen
	}
}

// demuxBlock — C: demux_block — one 2048-byte PS pack; walks PES
// startcodes, resyncs past unknown codes.
func (vs *Vobsub) demuxBlock(buf []byte, mp *mediacore.MediaPipe,
	pts int64) {

	if buf[13]&7 != 0 {
		return // Stuffing is not supported
	}

	buf = buf[14:]
	length := len(buf)

	for length > 0 {
		if length < 6 {
			break
		}
		startcode := uint32(buf[0])<<24 | uint32(buf[1])<<16 |
			uint32(buf[2])<<8 | uint32(buf[3])
		buf = buf[4:]
		length -= 4
		pesLen := int(buf[0])<<8 | int(buf[1])
		buf = buf[2:]
		length -= 2

		if pesLen < 3 {
			break
		}

		switch {
		case startcode == 0x1bd || startcode == 0x1bf ||
			(startcode >= 0x1c0 && startcode <= 0x1df) ||
			(startcode >= 0x1e0 && startcode <= 0x1ef):
			if pesLen > length {
				pesLen = length
			}
			vs.demuxPES(mp, startcode, buf[:pesLen], pts)
			length -= pesLen
			buf = buf[pesLen:]

		default:
			// C: default: break — resync scan continues
		}
	}
}

// demuxBlocks — C: demux_blocks — iterates 2048-byte blocks.
func (vs *Vobsub) demuxBlocks(buf []byte, mp *mediacore.MediaPipe,
	pts int64) {
	for len(buf) >= 2048 {
		vs.demuxBlock(buf[:2048], mp, pts)
		buf = buf[2048:]
	}
}

// veLoad — C: ve_load — seeks the .sub file to the entry and demuxes.
func (vs *Vobsub) veLoad(start, size int, pts int64) {
	pos, err := fileaccesscore.FASeek(vs.sub, int64(start), 0)
	if err != nil || pos != int64(start) {
		return
	}

	buf := make([]byte, size)
	n, err := fileaccesscore.FARead(vs.sub, buf)
	if n == size {
		vs.demuxBlocks(buf, vs.mp, pts)
	}
	_ = err
}

// vobsubThread — C: vobsub_thread — consumes vc_cmds; a zero-size
// command terminates the loop.
func (vs *Vobsub) vobsubThread() {
	run := true
	vs.mu.Lock()
	for run {
		for len(vs.cmds) == 0 {
			vs.cond.Wait()
		}
		vc := vs.cmds[0]
		vs.cmds = vs.cmds[1:]
		vs.mu.Unlock()

		if vc.size == 0 {
			run = false
		} else {
			vs.veLoad(vc.start, vc.size, vc.pts)
		}

		vs.mu.Lock()
	}
	vs.mu.Unlock()
	close(vs.done)
}

// vsSendCmd — C: vs_send_cmd — queues a load command and wakes the
// worker thread.
func (vs *Vobsub) vsSendCmd(start, size int, pts int64) {
	vc := &vobsubCmd{start: start, size: size, pts: pts}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.cmds = append(vs.cmds, vc)
	vs.cond.Signal()
}

// veDeliver — C: ve_deliver — loads the byte range of the entry into
// the demuxer.
func (vs *Vobsub) veDeliver(ve *vobsubEntry) {
	vs.cur = ve

	var nxt *vobsubEntry
	for i, e := range vs.entries {
		if e == ve && i+1 < len(vs.entries) {
			nxt = vs.entries[i+1]
			break
		}
	}
	fend := vs.stop
	if nxt != nil {
		fend = nxt.fpos
	}
	size := fend - ve.fpos
	if size > 0 {
		vs.vsSendCmd(ve.fpos, size, ve.pts)
	}
}

// vobsubPicker — C: vobsub_picker — es_picker implementation: keeps a
// cursor into vs_entries and delivers the entry whose [pts,stop) window
// contains the position.
func (vs *Vobsub) vobsubPicker(pts int64) {
	ve := vs.cur

	if ve != nil && ve.pts <= pts && veStop(vs.entries, ve) > pts {
		return // Already sent
	}

	if ve != nil {
		var nxt *vobsubEntry
		for i, e := range vs.entries {
			if e == ve && i+1 < len(vs.entries) {
				nxt = vs.entries[i+1]
				break
			}
		}
		ve = nxt
		if ve != nil && ve.pts <= pts && veStop(vs.entries, ve) > pts {
			vs.veDeliver(ve)
			return
		}
	}

	for _, ve = range vs.entries {
		if ve.pts <= pts && veStop(vs.entries, ve) > pts {
			vs.veDeliver(ve)
			return
		}
	}
	vs.cur = nil
}

// VobsubGetTS — C: vobsub_get_ts — parses "HH:MM:SS.mmm" into
// microseconds.
func VobsubGetTS(buf string) int64 {
	if len(buf) < 12 {
		return int64(mediacore.PTSUnset)
	}
	if buf[2] != ':' || buf[5] != ':' || buf[8] != ':' {
		return int64(mediacore.PTSUnset)
	}
	return 1000 * (int64(buf[0]-'0')*36000000 +
		int64(buf[1]-'0')*3600000 +
		int64(buf[3]-'0')*600000 +
		int64(buf[4]-'0')*60000 +
		int64(buf[6]-'0')*10000 +
		int64(buf[7]-'0')*1000 +
		int64(buf[9]-'0')*100 +
		int64(buf[10]-'0')*10 +
		int64(buf[11]-'0'))
}

// vobsubDtor — C: vobsub_dtor — terminates the worker, joins it, and
// releases mp/parser/ctx/handle.
func (vs *Vobsub) vobsubDtor() {
	vs.vsSendCmd(0, 0, 0) // Tell worker to terminate
	<-vs.done             // hts_thread_join

	mediacore.MpRelease(vs.mp)

	vs.parser.Close()
	libav.AvcodecFreeContext(vs.ctx)
	fileaccesscore.FAClose(vs.sub)
}

// VobsubLoad — C: vobsub_load — parses the JSON parameter block
// ("idx", "sub", "index"), loads the .idx, indexes timestamp/filepos
// pairs for the chosen language index, then spawns the loader thread.
func VobsubLoad(json string, mp *mediacore.MediaPipe) *subtitles.ExtSubtitles {
	m, err := htsmsg.DeserializeJSON(json)
	if m == nil || err != nil {
		// C: "Unable to decode JSON"
		return nil
	}

	idxfile := m.GetStr("idx")
	subfile := m.GetStr("sub")
	idx := int(m.GetU32OrDefault("index", 0))

	if idxfile == "" || subfile == "" {
		// C: "Missing message fields"
		return nil
	}

	b := faLoad(mp.FAM, idxfile) // C: fa_load(idxfile, FA_LOAD_ERRBUF, DISABLE_CACHE)
	if b == nil {
		return nil
	}

	vs := &Vobsub{}
	vs.cond = sync.NewCond(&vs.mu)
	vs.done = make(chan struct{})
	vs.parser = libav.NewAvParser(avCodecIDDVDSubtitle)
	vs.ctx = libav.AvcodecAllocContext3(nil)

	fh, ferr := fileaccesscore.FAOpenEx(mp.FAM, subfile, 0, nil)
	if ferr != nil || fh == nil {
		vs.parser.Close()
		libav.AvcodecFreeContext(vs.ctx)
		return nil
	}
	vs.sub = fh

	parseTS := false
	writeStop := false // C: dead flag — never set in vobsub.c; kept verbatim

	forEachLine(b.Data[:b.Size], func(s string) {
		if p, ok := myStrBegins2(s, "palette:"); ok {
			mediacore.VobsubDecodePalette(vs.clut[:], p)
		}
		if p, ok := myStrBegins2(s, "size:"); ok {
			mediacore.VobsubDecodeSize(&vs.width, &vs.height, p)
		}
		if p, ok := myStrBegins2(s, "id:"); ok {
			x := indexOf(p, "index:")
			if x < 0 && idx == -1 {
				parseTS = true
			} else if x >= 0 && misc.Atoi(p[x+len("index:"):]) == idx {
				parseTS = true
			} else {
				parseTS = false
			}
			return
		}
		if p, ok := myStrBegins2(s, "timestamp:"); ok {
			for len(p) > 0 && p[0] == ' ' {
				p = p[1:]
			}
			ts := VobsubGetTS(p)
			if ts == int64(mediacore.PTSUnset) {
				return
			}
			x := indexOf(p, "filepos:")
			if x < 0 {
				return
			}
			fpos := int(strtolHex(p[x+len("filepos:"):]))

			if parseTS {
				vs.entries = append(vs.entries,
					&vobsubEntry{pts: ts, fpos: fpos})
			} else {
				if writeStop {
					vs.stop = fpos
				}
				writeStop = false
			}
		}
	})

	if writeStop {
		vs.stop = int(vs.sub.Size()) // C: fa_fsize
	}

	vs.Dtor = func(*subtitles.ExtSubtitles) { vs.vobsubDtor() }
	vs.Picker = func(_ *subtitles.ExtSubtitles, pts int64) { vs.vobsubPicker(pts) }

	vs.mp = mediacore.MpRetain(mp)

	// C: hts_thread_create_joinable("vobsub loader", vobsub_thread, vs)
	go vs.vobsubThread()

	return &vs.ExtSubtitles
}

// avCodecIDDVDSubtitle — C: AV_CODEC_ID_DVD_SUBTITLE (0x17000)
const avCodecIDDVDSubtitle = 0x17000

// forEachLine — C: the writable-buf line loop
// for(; l = strcspn(s, "\r\n"), *s; s += l+1+strspn(s+l+1, "\r\n"))
func forEachLine(b []byte, fn func(string)) {
	for len(b) > 0 {
		l := len(b)
		for i := range len(b) {
			if b[i] == '\r' || b[i] == '\n' {
				l = i
				break
			}
		}
		line := b[:l]
		if l < len(b) {
			next := l + 1
			for next < len(b) && (b[next] == '\r' || b[next] == '\n') {
				next++
			}
			b = b[next:]
		} else {
			b = b[len(b):]
		}
		fn(string(line))
	}
}

// myStrBegins2 — C: mystrbegins (shared with dvdspu.go's copy in
// mediacore; ext has its own to stay in-package)
func myStrBegins2(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// strtolHex — C: strtol(str, NULL, 16)
func strtolHex(s string) int64 {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	var v int64
	for i < len(s) {
		c := s[i]
		var d int64
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'f':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int64(c-'A') + 10
		default:
			return v
		}
		v = v<<4 | d
		i++
	}
	return v
}
