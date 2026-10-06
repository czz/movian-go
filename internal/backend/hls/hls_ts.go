// Canonical port of src/backend/hls/hls_ts.c — MPEG-TS demuxer for HLS
// segments: PAT/PMT section reassembly, PES assembly, av_parser-based
// elementary stream parsing, discontinuity timestamp offsets, and
// non-muxed (raw AAC + ID3) probing.
package hls

import (
	"slices"
	"strings"

	"encoding/binary"
	"errors"
	"fmt"
	"io"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/libav"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

const ptsMask = 0x1ffffffff // C: PTS_MASK (hls_ts.c:35)

// mpegTC — C: mpeg_tc (hls_ts.c:48) = {1, 90000}.
var mpegTC = libav.AVRational{Num: 1, Den: 90000}

// avTimeBaseQ — C: AV_TIME_BASE_Q = {1, AV_TIME_BASE=1000000}.
var avTimeBaseQ = libav.AVRational{Num: 1, Den: 1000000}

// C: FF_INPUT_BUFFER_PADDING_SIZE (libavcodec) = 64.
const ffInputBufferPaddingSize = 64

// tsTable — C: ts_table_t (hls_ts.c:40-44).
type tsTable struct {
	Offset int    // C: tt_offset
	Lock   int    // C: tt_lock
	Data   []byte // C: tt_data
}

// tsDemuxer — C: ts_demuxer_t (hls_ts.c:50-68).
type tsDemuxer struct {
	Services          []*tsService // C: td_services (LIST)
	ElementaryStreams []*tsES      // C: td_elemtary_streams (LIST)
	Pat               tsTable      // C: td_pat
	MP                *mediacore.MediaPipe
	HD                *hlsDemuxer
	Packets           []*mediacore.MediaBuf // C: td_packets (TAILQ)

	MuxMode int // C: td_mux_mode

	// C: td_buf[2048] — a plain byte array inside malloc'd td. Go keeps
	// it a separately-allocated slice: slices of a struct that also
	// holds pointers (MP/HD/…) hit the cgo "unpinned Go pointer" check
	// when &Buf[0] reaches av_parser_parse2.
	Buf      []byte // C: td_buf
	BufBytes int    // C: td_buf_bytes
}

// C: td_mux_mode enum.
const (
	tdMuxModeUnset = iota // C: TD_MUX_MODE_UNSET
	tdMuxModeTS           // C: TD_MUX_MODE_TS
	tdMuxModeRaw          // C: TD_MUX_MODE_RAW
)

// tsService — C: ts_service_t (hls_ts.c:71-76).
type tsService struct {
	PMTPid  uint16 // C: tss_pmtpid
	PMT     tsTable
	Demuxer *tsDemuxer // C: tss_demuxer
}

// tsES — C: ts_es_t (hls_ts.c:79-113) — elementary stream state.
type tsES struct {
	Pid uint16 // C: te_pid

	Buf        []byte // C: te_buf
	BufSize    int    // C: te_buf_size
	PacketSize int    // C: te_packet_size

	DataType   int // C: te_data_type
	ProbeFrame int // C: te_probe_frame
	Stream     int // C: te_stream

	LoggedTS       bool // C: te_logged_ts
	LoggedKeyframe bool // C: te_logged_keyframe
	LoggedInfo     bool // C: te_logged_info

	PTS int64 // C: te_pts
	DTS int64 // C: te_dts
	BTS int64 // C: te_bts — base timestamp

	Codec *mediacore.MediaCodec // C: te_codec

	// Used to derive timestamps for files without timestamps
	SamplesPerFrame int   // C: te_samples_per_frame
	SampleRate      int   // C: te_sample_rate
	Samples         int64 // C: te_samples

	CurrentSeq int // C: te_current_seq

	TSOffset int64 // C: te_ts_offset

	LastSeq int // C: te_last_seq
}

// ---------------------------------------------------------------------------
// td_flush_packets (hls_ts.c:119-128)
// ---------------------------------------------------------------------------

func tdFlushPackets(td *tsDemuxer) {
	for len(td.Packets) > 0 {
		mb := td.Packets[0]
		td.Packets = td.Packets[1:]
		mediacore.MediaBufFreeUnlocked(td.MP, mb)
	}
}

// ---------------------------------------------------------------------------
// te_destroy / tss_destroy / ts_demuxer_destroy (hls_ts.c:134-176)
// ---------------------------------------------------------------------------

func (td *tsDemuxer) teDestroy(te *tsES) {
	for i, x := range td.ElementaryStreams {
		if x == te {
			td.ElementaryStreams = append(
				td.ElementaryStreams[:i], td.ElementaryStreams[i+1:]...)
			break
		}
	}
	if te.Codec != nil {
		mediacore.MediaCodecDeref(te.Codec)
	}
}

func (td *tsDemuxer) tssDestroy(tss *tsService) {
	for i, x := range td.Services {
		if x == tss {
			td.Services = slices.Delete(td.Services, i, i+1)
			break
		}
	}
}

func (td *tsDemuxer) destroy() {
	tdFlushPackets(td)
	for len(td.ElementaryStreams) > 0 {
		td.teDestroy(td.ElementaryStreams[0])
	}
	for len(td.Services) > 0 {
		td.tssDestroy(td.Services[0])
	}
}

// ---------------------------------------------------------------------------
// table_reassemble / parse_table (hls_ts.c:182-259)
// ---------------------------------------------------------------------------

func tableReassemble(tt *tsTable, data []byte, pusi int,
	cb func(opaque any, data []byte),
	opaque any) int {
	if pusi != 0 {
		tt.Offset = 0
		tt.Lock = 1
	}
	if tt.Lock == 0 {
		return -1
	}

	tt.Data = append(tt.Data, data...)
	tt.Offset += len(data)

	if tt.Offset < 3 {
		return len(data)
	}

	tsize := 3 + (int(tt.Data[1]&0xf)<<8 | int(tt.Data[2]))
	if tt.Offset < tsize {
		return len(data)
	}

	excess := tt.Offset - tsize
	tt.Offset = 0
	if tsize >= 7 {
		cb(opaque, tt.Data[3:3+tsize-7])
	}
	tt.Data = nil

	return len(data) - excess
}

func parseTable(tt *tsTable, tsb []byte,
	cb func(opaque any, data []byte), opaque any) {
	off := 4
	if tsb[3]&0x20 != 0 {
		off = int(tsb[4]) + 5
	}
	pusi := int(tsb[1] & 0x40)

	if off >= 188 {
		tt.Lock = 0
		return
	}

	if pusi != 0 {
		length := int(tsb[off])
		off++
		if length > 0 {
			if length > 188-off {
				tt.Lock = 0
				return
			}
			tableReassemble(tt, tsb[off:off+length], 0, cb, opaque)
			off += length
		}
	}

	for off < 188 {
		r := tableReassemble(tt, tsb[off:188], pusi, cb, opaque)
		if r < 0 {
			tt.Lock = 0
			break
		}
		off += r
		pusi = 0
	}
}

// ---------------------------------------------------------------------------
// find_service / find_es (hls_ts.c:265-302)
// ---------------------------------------------------------------------------

func (td *tsDemuxer) findService(pmtpid uint16, create int) *tsService {
	for _, tss := range td.Services {
		if tss.PMTPid == pmtpid {
			return tss
		}
	}
	if create != 0 {
		tss := &tsService{Demuxer: td, PMTPid: pmtpid}
		// C: LIST_INSERT_HEAD
		td.Services = slices.Insert(td.Services, 0, tss)
		return tss
	}
	return nil
}

func (td *tsDemuxer) findES(pid uint16, create int) *tsES {
	for _, te := range td.ElementaryStreams {
		if te.Pid == pid {
			return te
		}
	}
	if create != 0 {
		te := &tsES{
			Pid: pid,
			PTS: mediacore.PTSUnset,
			DTS: mediacore.PTSUnset,
		}
		td.ElementaryStreams = slices.Insert(td.ElementaryStreams, 0, te)
		return te
	}
	return nil
}

// ---------------------------------------------------------------------------
// handle_pat (hls_ts.c:308-321)
// ---------------------------------------------------------------------------

func handlePAT(opaque any, ptr []byte) {
	td := opaque.(*tsDemuxer)
	if len(ptr) < 5 {
		return // C: ptr += 5; len -= 5 → negative → while(len >= 4) skipped
	}
	ptr = ptr[5:]
	length := len(ptr)
	for length >= 4 {
		pid := uint16(ptr[2]&0x1f)<<8 | uint16(ptr[3])
		td.findService(pid, 1)
		length -= 4
		ptr = ptr[4:]
	}
}

// ---------------------------------------------------------------------------
// handle_pmt (hls_ts.c:328-460)
// ---------------------------------------------------------------------------

func handlePMT(opaque any, ptr []byte) {
	tss := opaque.(*tsService)
	td := tss.Demuxer
	hd := td.HD
	h := hd.HLS

	length := len(ptr)
	if length < 9 {
		return
	}

	dllen := int(ptr[7]&0xf)<<8 | int(ptr[8])
	ptr = ptr[9:]
	length -= 9

	for dllen > 1 {
		dlen := int(ptr[1])
		length -= 2
		ptr = ptr[2:]
		dllen -= 2
		if dlen > length {
			return
		}
		length -= dlen
		ptr = ptr[dlen:]
		dllen -= dlen
	}

	for length >= 5 {
		estype := ptr[0]
		pid := uint16(ptr[1]&0x1f)<<8 | uint16(ptr[2])
		dllen = int(ptr[3]&0xf)<<8 | int(ptr[4])

		ptr = ptr[5:]
		length -= 5

		var langbuf [4]byte
		lang := ""

		for dllen > 1 {
			dtag := ptr[0]
			dlen := int(ptr[1])
			length -= 2
			ptr = ptr[2:]
			dllen -= 2

			if dlen > length {
				break
			}
			if dtag == 0xa && dlen >= 3 {
				copy(langbuf[:], ptr[:3])
				lang = string(langbuf[:])
			}
			length -= dlen
			ptr = ptr[dlen:]
			dllen -= dlen
		}

		te := td.findES(pid, 1)
		name := ""
		if te.Codec == nil {
			var muxid string
			var hatPid int
			if hd == &h.Primary {
				muxid = ""
				hatPid = int(pid)
			} else {
				muxid = hd.Current.URL
				hatPid = 0
			}
			switch estype {
			case 0x15:
				name = "data"
			case 0x1b:
				te.Codec = mediacore.MediaCodecRef(h.CodecH264)
				te.DataType = int(mediacore.MBVideo)
				te.Stream = 0
				name = "h264"
			case 0x0f:
				te.Codec = mediacore.MediaCodecCreate(
					mediacore.CodecID(libav.AVCodecIDAAC),
					1, nil, nil, nil, td.MP)
				te.DataType = int(mediacore.MBAudio)
				te.Stream = hlsGetAudioTrack(h, hatPid, muxid,
					lang, "AAC", 1)
				name = "AAC"
			case 0x81:
				te.Codec = mediacore.MediaCodecCreate(
					mediacore.CodecID(libav.AVCodecIDAC3),
					1, nil, nil, nil, td.MP)
				te.DataType = int(mediacore.MBAudio)
				te.Stream = hlsGetAudioTrack(h, hatPid, muxid,
					lang, "AC3", 1)
				name = "AC3"
			case 0x03, 0x04:
				te.Codec = mediacore.MediaCodecCreate(
					mediacore.CodecID(libav.AVCodecIDMP3),
					1, nil, nil, nil, td.MP)
				te.DataType = int(mediacore.MBAudio)
				te.Stream = hlsGetAudioTrack(h, hatPid, muxid,
					lang, "MP3", 1)
				name = "MP3"
			}
		}

		if !te.LoggedInfo {
			te.LoggedInfo = true
			if name == "" {
				h.ts.Trace(trace.TRACE_ERROR, "HLS",
					"Unsupported estype 0x%x on pid %d in %s",
					estype, pid, td.HD.Type)
			} else {
				hlsTrace(h, "New %s TS PID %d type %s (0x%x) stream=%d",
					td.HD.Type, pid, name, estype, te.Stream)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// getpts / parse_pes_header / rescale (hls_ts.c:470-547)
// ---------------------------------------------------------------------------

func getpts(p []byte) int64 {
	a := int(p[0])
	b := int(p[1])<<8 | int(p[2])
	c := int(p[3])<<8 | int(p[4])

	if a&1 != 0 && b&1 != 0 && c&1 != 0 {
		return int64((a>>1)&0x07)<<30 |
			int64(b>>1)<<15 |
			int64(c>>1)
	}
	// Marker bits not present
	return mediacore.PTSUnset
}

// parsePesHeader — C: parse_pes_header (hls_ts.c:496-532).
// Returns header length to skip, or -1.
func parsePesHeader(te *tsES, buf []byte) int {
	length := len(buf)
	if length < 3 {
		return -1
	}
	hdr := int(buf[0])
	flags := int(buf[1])
	hlen := int(buf[2])
	buf = buf[3:]
	length -= 3

	te.PTS = mediacore.PTSUnset
	te.DTS = mediacore.PTSUnset

	if length < hlen || (hdr&0xc0) != 0x80 {
		return -1
	}

	if flags&0xc0 == 0xc0 {
		if hlen < 10 {
			return -1
		}
		te.PTS = getpts(buf)
		te.DTS = getpts(buf[5:])

		d := (te.PTS - te.DTS) & ptsMask
		if d > 180000 {
			// More than two seconds of PTS/DTS delta, PTS probably corrupt
			te.PTS = mediacore.PTSUnset
		}
	} else if flags&0xc0 == 0x80 {
		if hlen < 5 {
			return -1
		}
		te.DTS = getpts(buf)
		te.PTS = te.DTS
	}
	return hlen + 3
}

// rescale — C: rescale (hls_ts.c:539-547).
func rescale(ts int64) int64 {
	if ts == mediacore.PTSUnset {
		return mediacore.PTSUnset
	}
	return libav.AvRescaleQ(ts, mpegTC, avTimeBaseQ)
}

// ---------------------------------------------------------------------------
// probe_duration (hls_ts.c:553-594) — decode one audio frame to learn
// samples_per_frame / sample_rate for timestamp-less raw streams.
// ---------------------------------------------------------------------------

func probeDuration(te *tsES, data []byte) {
	mc := te.Codec

	codec := libav.AvcodecFindDecoder(int(mc.CodecID))
	if codec == nil {
		te.ProbeFrame = 0
		return
	}
	ctx := libav.AvcodecAllocContext3(codec)
	if ctx == nil {
		te.ProbeFrame = 0
		return
	}
	if err := libav.AvcodecOpen2(ctx, codec, nil); err != nil {
		libav.AvcodecFreeContext(ctx)
		te.ProbeFrame = 0
		return
	}

	frame := libav.AvFrameAlloc()
	pkt := libav.AvPacketAlloc()
	libav.AvPacketSetData(pkt, data)

	// C: avcodec_decode_audio4 — removed in FFmpeg 7; equivalent
	// send_packet + receive_frame sequence.
	gotFrame := false
	if libav.AvcodecSendPacket(ctx, pkt) == nil {
		if libav.AvcodecReceiveFrame(ctx, frame) == nil {
			gotFrame = true
		}
	}

	if gotFrame {
		te.ProbeFrame = 0
		te.SamplesPerFrame = libav.AvFrameNbSamples(frame.CPtr())
		te.SampleRate = libav.AvFrameSampleRate(frame.CPtr())
		te.Samples = 0
	}

	libav.AvPacketFree(pkt)
	libav.AvcodecClose(ctx)
	libav.AvcodecFreeContext(ctx)
	libav.AvFrameFree(frame)
}

// ---------------------------------------------------------------------------
// enqueue_packet (hls_ts.c:600-716)
// ---------------------------------------------------------------------------

func enqueuePacket(td *tsDemuxer, data []byte, dts, pts int64,
	keyframe int, hs *hlsSegment, seq int, te *tsES,
	hd *hlsDemuxer) {
	h := td.HD.HLS

	dts = rescale(dts)
	pts = rescale(pts)

	userTime := mediacore.PTSUnset
	driveClock := 0

	if hs != nil {
		hds := hs.DiscontinuitySegment

		// Compute user time
		if pts != mediacore.PTSUnset {
			if hs.TSOffset == mediacore.PTSUnset {
				hs.TSOffset = pts
			}
			userTime = hs.TimeOffset + max64(pts-hs.TSOffset, 0)
			if te.DataType == int(mediacore.MBVideo) {
				driveClock = 1
			}
		}

		if hds.Offset == mediacore.PTSUnset {
			if dts != mediacore.PTSUnset {
				if hd.LastDTS != mediacore.PTSUnset {
					hds.Offset = hd.LastDTS - dts
				} else {
					hds.Offset = 0
				}
				hlsTrace(h, "Discontinuity segment %d gets "+
					"timestamp offset: %d", hds.Seq, hds.Offset)
			}
		}

		if hds.Offset != mediacore.PTSUnset {
			if dts != mediacore.PTSUnset {
				dts += hds.Offset
				hd.LastDTS = dts
			}
			if pts != mediacore.PTSUnset {
				pts += hds.Offset
			}
		} else {
			pts = mediacore.PTSUnset
			dts = mediacore.PTSUnset
		}
	} else {
		pts = mediacore.PTSUnset
		dts = mediacore.PTSUnset
	}

	if te.SampleRate == 0 {
		mp := td.MP
		var mq *mediacore.MediaQueue
		if te.DataType == int(mediacore.MBVideo) {
			mq = mp.Video
		} else {
			mq = mp.Audio
		}
		if mq.DemuxerFlags&HLSQueueKeyframeSeen == 0 {
			if dts == mediacore.PTSUnset {
				return
			}
			if keyframe == 0 {
				return
			}
		}
	}

	mb := mediacore.MediaBufAllocUnlocked(td.MP, len(data))
	copy(mb.Data, data)
	mb.UserTime = userTime
	mb.DTS = dts
	mb.PTS = pts
	mb.Flags.DriveClock = driveClock

	mb.Codec = mediacore.MediaCodecRef(te.Codec)
	mb.DataType = te.DataType
	mb.Flags.Keyframe = keyframe != 0
	mb.Sequence = seq

	if mb.Flags.Keyframe && !te.LoggedKeyframe {
		te.LoggedKeyframe = true
		hlsTrace(h,
			"%s        keyframe %20d:%20d demuxer:%s stream:%d",
			map[bool]string{true: "VIDEO",
				false: "AUDIO"}[te.DataType == int(mediacore.MBVideo)],
			te.Codec.ParserCtx.Dts(),
			te.Codec.ParserCtx.Pts(),
			hd.Type, te.Stream)
	}

	mb.Stream = te.Stream
	td.Packets = append(td.Packets, mb)
}

// ---------------------------------------------------------------------------
// parse_data (hls_ts.c:722-784)
// ---------------------------------------------------------------------------

func parseData(td *tsDemuxer, hv *hlsVariant, te *tsES,
	data []byte, size int) {
	mc := te.Codec
	if mc == nil {
		return
	}

	for size > 0 || data == nil {
		out, rlen := mc.ParserCtx.Parse2Pos(mc.FmtCtx,
			data[:size], te.PTS, te.DTS, int64(te.CurrentSeq))

		if len(out) > 0 {
			dts := mc.ParserCtx.Dts()
			pts := mc.ParserCtx.Pts()
			seq := int(mc.ParserCtx.Pos())
			keyframe := mc.ParserCtx.KeyFrame()

			if seq == -1 {
				seq = te.LastSeq
			} else {
				te.LastSeq = seq
			}

			var hs *hlsSegment
			if hv != nil {
				hs = hvFindSegmentBySeq(hv, seq)
			}

			if te.ProbeFrame != 0 {
				// Need to find actual duration by decoding a frame
				probeDuration(te, out)
			}

			if te.SampleRate != 0 {
				te.Samples += int64(te.SamplesPerFrame)
				tb := libav.AVRational{Num: 1, Den: te.SampleRate}
				ts := libav.AvRescaleQ(te.Samples, tb, mpegTC)
				dts = te.BTS + ts
				pts = dts
			}

			enqueuePacket(td, out, dts, pts, keyframe, hs, seq, te,
				td.HD)
		}

		te.PTS = mediacore.PTSUnset
		te.DTS = mediacore.PTSUnset

		if data == nil {
			if len(out) == 0 {
				return
			}
			continue
		}

		data = data[rlen:]
		size -= rlen
	}
}

// ---------------------------------------------------------------------------
// drain_parsers / emit_packet / process_es / process_tsb / get_pkt
// (hls_ts.c:790-927)
// ---------------------------------------------------------------------------

func drainParsers(td *tsDemuxer) {
	for len(td.ElementaryStreams) > 0 {
		te := td.ElementaryStreams[0]
		if te.Codec != nil {
			parseData(td, nil, te, nil, 0)
		}
		td.teDestroy(te)
	}
}

func emitPacket(te *tsES, td *tsDemuxer, hs *hlsSegment) {
	data := te.Buf[:te.PacketSize]

	if len(data) < 9 {
		return
	}

	data = data[6:]

	hlen := parsePesHeader(te, data)
	if hlen < 0 {
		return
	}

	if !te.LoggedTS && te.PTS != mediacore.PTSUnset {
		te.LoggedTS = true
		hlsTrace(hs.Variant.Demuxer.HLS,
			"%s First timestamp %20d:%20d",
			map[bool]string{true: "VIDEO",
				false: "AUDIO"}[te.DataType == int(mediacore.MBVideo)],
			te.DTS, te.PTS)
	}

	data = data[hlen:]

	parseData(td, hs.Variant, te, data, len(data))
}

func processES(te *tsES, tsb []byte, td *tsDemuxer, hs *hlsSegment) {
	off := 4
	if tsb[3]&0x20 != 0 {
		off = int(tsb[4]) + 5
	}
	pusi := int(tsb[1] & 0x40)
	data := tsb[off:]
	size := 188 - off

	if pusi != 0 {
		if te.Buf != nil {
			// C: memset(te->te_buf + te->te_packet_size, 0,
			//   FF_INPUT_BUFFER_PADDING_SIZE)
			for i := te.PacketSize; i < len(te.Buf); i++ {
				te.Buf[i] = 0
			}
		}
		emitPacket(te, td, hs)
		te.PacketSize = 0
		te.CurrentSeq = hs.Seq
	}

	if te.PacketSize+size > te.BufSize {
		te.BufSize = te.BufSize*2 + size
		nb := make([]byte, te.BufSize+ffInputBufferPaddingSize)
		copy(nb, te.Buf)
		te.Buf = nb
	}
	copy(te.Buf[te.PacketSize:], data[:size])
	te.PacketSize += size
}

func processTSB(td *tsDemuxer, tsb []byte, hs *hlsSegment) {
	if tsb[0] != 0x47 {
		return
	}
	pid := uint16(tsb[1]&0x1f)<<8 | uint16(tsb[2])

	for _, te := range td.ElementaryStreams {
		if pid == te.Pid {
			processES(te, tsb, td, hs)
			return
		}
	}
	for _, tss := range td.Services {
		if pid == tss.PMTPid {
			parseTable(&tss.PMT, tsb, handlePMT, tss)
			return
		}
	}
	if pid == 0 {
		parseTable(&td.Pat, tsb, handlePAT, td)
	}
}

func getPkt(td *tsDemuxer) *mediacore.MediaBuf {
	if len(td.Packets) == 0 {
		return nil
	}
	mb := td.Packets[0]
	td.Packets = td.Packets[1:]
	return mb
}

// ---------------------------------------------------------------------------
// hls_ts_demuxer_close / hls_ts_demuxer_flush (hls_ts.c:933-969)
// ---------------------------------------------------------------------------

func hlsTsDemuxerClose(hv *hlsVariant) {
	td := hv.DemuxerPrivate
	if td == nil {
		panic("td == NULL")
	}
	td.destroy()
	hv.DemuxerPrivate = nil
	hv.DemuxerClose = nil
	hv.DemuxerFlush = nil
}

func hlsTsDemuxerFlush(hv *hlsVariant) {
	td := hv.DemuxerPrivate
	for _, te := range td.ElementaryStreams {
		mc := te.Codec
		if mc == nil {
			continue
		}
		mc.ParserCtx.Close()
		mc.ParserCtx = libav.NewAvParser(int(mc.CodecID))

		te.PacketSize = 0
		te.PTS = mediacore.PTSUnset
		te.DTS = mediacore.PTSUnset
		te.LastSeq = 0
	}
	tdFlushPackets(td)
}

// ---------------------------------------------------------------------------
// unmuxed_input / probe_non_muxed (hls_ts.c:976-1045)
// ---------------------------------------------------------------------------

func unmuxedInput(td *tsDemuxer, buf []byte, hs *hlsSegment) {
	if len(td.ElementaryStreams) == 0 {
		panic("no elementary stream") // C: assert(te != NULL)
	}
	te := td.ElementaryStreams[0]
	te.CurrentSeq = hs.Seq
	parseData(td, hs.Variant, te, buf, len(buf))
}

func probeNonMuxed(td *tsDemuxer, data []byte, hs *hlsSegment) int {
	var ptsoffset int64
	o := 0
	hv := hs.Variant

	// Not really an ID3 parser, we just search more or less blindly
	if len(data) >= 3 && string(data[:3]) == "ID3" {
		const s = "com.apple.streaming.transportStreamTimestamp"
		if t := misc.FindStr(data, len(data), s); t >= 0 {
			off := t + 1
			// C checks off <= size - 8 but reads at t + len(s) + 1;
			// keep the canonical check and guard the slice to avoid a Go panic.
			if off <= len(data)-8 && t+len(s)+9 <= len(data) {
				ptsoffset = int64(binary.BigEndian.Uint64(
					data[t+len(s)+1:]))
				o = off + len(s) + 8
			}
		}
	}

	if o+7 >= 188 {
		goto bad
	}

	{
		h1 := binary.BigEndian.Uint32(data[o:])
		if h1&0xfff60000 == 0xfff00000 {
			// AAC
			te := td.findES(0, 1)
			if te.Codec != nil &&
				te.Codec.CodecID != mediacore.CodecID(libav.AVCodecIDAAC) {
				mediacore.MediaCodecDeref(te.Codec)
				te.Codec = nil
			}
			if te.Codec == nil {
				te.Codec = mediacore.MediaCodecCreate(
					mediacore.CodecID(libav.AVCodecIDAAC),
					1, nil, nil, nil, td.MP)
				if hv.AudioStream == 0 {
					panic("hv_audio_stream == 0")
				}
				te.Stream = hv.AudioStream
			}
			te.DataType = int(mediacore.MBAudio)
			te.BTS = ptsoffset
			te.DTS = ptsoffset
			te.PTS = ptsoffset
			te.ProbeFrame = 1
			unmuxedInput(td, data[o:], hs)
			return 0
		}
	}

bad:
	hs.Variant.Demuxer.HLS.ts.Trace(trace.TRACE_ERROR, "HLS",
		"Unable to probe contents, dumping buffer")
	hexdumpHLS(hs.Variant.Demuxer.HLS.ts, "BUFFER", data, min(len(data), 256))
	return -1
}

// ---------------------------------------------------------------------------
// hls_ts_demuxer_read (hls_ts.c:1051-1316)
// ---------------------------------------------------------------------------

func hlsTsDemuxerRead(hd *hlsDemuxer) mbRef {
	h := hd.HLS
	mp := h.MP

	if hd.Current == nil {
		return mrefEOF
	}

	for {
		if hd.Current == nil {
			panic("hd_current == NULL")
		}

		hlsCheckBWSwitch(hd)

		if hd.Req != nil {
			hlsTrace(h, "Switching from %s to %s",
				hd.Current.Name, hd.Req.Name)
			hlsVariantClose(hd.Current)

			hd.Current = hd.Req
			hd.Req = nil

			if hd == &h.Primary {
				// Primary demuxer always controls the video queue
				mp.Video.DemuxerFlags |= HLSQueueMerge
				mp.Video.DemuxerFlags &^= HLSQueueKeyframeSeen

				if h.Audio.Current == nil {
					// No audio variant running — we also control
					// the audio queue
					mp.Audio.DemuxerFlags |= HLSQueueMerge
					mp.Audio.DemuxerFlags &^= HLSQueueKeyframeSeen
				}
			}
			if hd == &h.Audio {
				// Audio demuxer switching controls the audio queue
				mp.Audio.DemuxerFlags |= HLSQueueMerge
				mp.Audio.DemuxerFlags &^= HLSQueueKeyframeSeen
			}
		}

		hv := hd.Current
		td := hv.DemuxerPrivate

		if td == nil {
			td = &tsDemuxer{MP: mp, HD: hd, Buf: make([]byte, 2048)}
			hv.DemuxerPrivate = td
			hv.DemuxerClose = hlsTsDemuxerClose
			hv.DemuxerFlush = hlsTsDemuxerFlush
		}

		if mb := getPkt(td); mb != nil {
			return mbRef{mb, 0}
		}

		if hd.SeekToSegment != mediacore.PTSUnset &&
			hv.CurrentSeg != nil {
			hlsSegmentClose(hv.CurrentSeg)
			hv.CurrentSeg = nil
		}

		if hv.CurrentSeg == nil || hv.CurrentSeg.FH == nil {
			var hs *hlsSegment
			for {
				var code int
				hs, code = hlsVariantSelectNextSegment(hv)

				if code == codeNYA {
					continue
				}
				if code == codeEOF {
					return mrefEOF
				}
				if hs == nil {
					return mbRef{}
				}

				hlsVariantOpen(hv)

				hlsTrace(h,
					"%s: Opening variant %s sequence %d discontinuity-seq:%d",
					hd.Type, hv.Name, hs.Seq,
					hs.DiscontinuitySegment.Seq)
				break
			}

			attempts := 0
			for {
				err := hlsSegmentOpen(hs)

				if misc.CancellableIsCancelled(hd.Cancellable) != 0 {
					hlsSegmentClose(hs)
					hv.CurrentSeg = nil
					return mbRef{}
				}

				if !hv.Frozen && err == hlsErrorSegmentNotFound {
					// In live mode segments may disappear between
					// playlist load and open — retry a few times
					// C: hls_ts.c:1164 — attempts < 10
					if attempts < 10 {
						attempts++
						idx := -1
						for i, x := range hv.Segments {
							if x == hs {
								idx = i
								break
							}
						}
						if idx >= 0 && idx+1 < len(hv.Segments) {
							hs = hv.Segments[idx+1]
							hlsTrace(h,
								"Segment %d not found in live mode, trying next",
								hs.Seq-1)
							continue
						}
					}
				}

				if err != hlsErrorOK {
					hlsBadVariant(hv, err)
					return mbRef{}
				}
				hv.CurrentSeg = hs
				td.MuxMode = tdMuxModeUnset
				break
			}
		}

		// Try to read some bytes
		hs := hv.CurrentSeg
		hds := hs.DiscontinuitySegment.Seq

		if hds != hd.DiscontinuitySeq {
			drainParsers(td)
			hd.DiscontinuitySeq = hds
		}

		switch td.MuxMode {
		case tdMuxModeUnset:
			hlsTrace(h, "Probing variant %s, sequence %d",
				hv.Name, hs.Seq)

			for td.BufBytes < len(td.Buf) {
				r := faReadC(hs.FH, td.Buf[td.BufBytes:])

				if misc.CancellableIsCancelled(hd.Cancellable) != 0 {
					return mbRef{}
				}
				if r <= 0 {
					hlsBadVariant(hv, hlsErrorVariantProbeError)
					return mbRef{}
				}
				td.BufBytes += r
				hd.DownloadCounter += int64(r)
			}

			// Search stream for TS mux lock — two continuous packets
			i := 0
			for ; i < len(td.Buf)-188*2; i++ {
				if td.Buf[i] == 0x47 &&
					td.Buf[i+188] == 0x47 &&
					td.Buf[i+188*2] == 0x47 {
					break
				}
			}

			if i != len(td.Buf)-188*2 {
				td.MuxMode = tdMuxModeTS
				hlsTrace(h, "Variant %s is a transport stream",
					hv.Name)

				for i <= len(td.Buf)-188 {
					processTSB(td, td.Buf[i:i+188], hs)
					i += 188
				}

				spill := len(td.Buf) - i
				if spill >= 188 {
					panic("spill >= 188")
				}
				td.BufBytes = spill
				copy(td.Buf[:], td.Buf[i:])
				break
			}

			if hd == &h.Primary {
				hlsBadVariant(hv, hlsErrorVariantNoVideo)
				return mbRef{}
			}

			if probeNonMuxed(td, td.Buf[:], hs) != 0 {
				hlsBadVariant(hv, hlsErrorVariantUnknownAudio)
				return mbRef{}
			}

			td.MuxMode = tdMuxModeRaw
			td.BufBytes = 0

		case tdMuxModeRaw:
			r := faReadC(hs.FH, td.Buf[:])

			if misc.CancellableIsCancelled(hd.Cancellable) != 0 {
				return mbRef{}
			}
			if r < 0 {
				return mrefEOF
			}
			if r == 0 {
				hlsSegmentClose(hs)
				continue
			}
			hd.DownloadCounter += int64(r)
			unmuxedInput(td, td.Buf[:r], hs)

		case tdMuxModeTS:
			if td.BufBytes >= 188 {
				panic("td_buf_bytes >= 188")
			}
			r := faReadC(hs.FH, td.Buf[td.BufBytes:188])

			if misc.CancellableIsCancelled(hd.Cancellable) != 0 {
				return mbRef{}
			}
			if r < 0 {
				return mrefEOF
			}
			if r == 0 {
				hlsSegmentClose(hs)
				continue
			}
			td.BufBytes += r
			hd.DownloadCounter += int64(r)

			if td.BufBytes == 188 {
				td.BufBytes = 0
				processTSB(td, td.Buf[:188], hs)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// faReadC — C: fa_read semantics — returns n>=0 bytes read, 0 at EOF,
// -1 on error (fileaccess.c faRead convention).
func faReadC(fh *fileaccesscore.Handle, buf []byte) int {
	n, err := fileaccesscore.FARead(fh, buf)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return n
		}
		return -1
	}
	return n
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// hexdumpHLS — C: hexdump (misc/str.h) — diagnostic buffer dump.
func hexdumpHLS(ts *trace.TraceSystem, prefix string, data []byte, n int) {
	if n > len(data) {
		n = len(data)
	}
	for i := 0; i < n; i += 16 {
		var line strings.Builder
		line.WriteString(prefix + ": ")
		for j := i; j < i+16 && j < n; j++ {
			line.WriteString(fmt.Sprintf("%02x ", data[j]))
		}
		ts.Trace(trace.TRACE_DEBUG, "HLS", "%s", line.String())
	}
}
