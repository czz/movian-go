package decoder

// Canonical port of src/video/h264_annexb.c + h264_annexb.h.
//
// Two halves, exactly as the C file:
//  1. h264_to_annexb — length-prefixed NAL → Annex-B start-code
//     conversion plus SPS/PPS extradata collection (avcC → Annex-B).
//  2. h264_annexb_to_avc — a media_codec wrapper that converts an
//     Annex-B stream back to AVC on the fly, tracking parameter sets
//     and (re)creating the inner decoder when they change.

import (
	"bytes"
	"slices"

	"github.com/czz/movian-go/internal/libav"

	mediacore "github.com/czz/movian-go/internal/media/core"
	miscpkg "github.com/czz/movian-go/internal/misc"
)

// H264AnnexBCtx — C: h264_annexb_ctx_t (h264_annexb.h:25-32).
type H264AnnexBCtx struct {
	Lsize             int
	Tmpbuf            []byte // C: uint8_t *tmpbuf (realloc'd scratch)
	Tmpbufsize        int
	Extradata         []byte // C: uint8_t *extradata + extradata_size
	ExtradataInjected bool
}

// h264ToAnnexBInplace — C: h264_to_annexb_inplace (h264_annexb.c:31-45).
// Rewrites each 4-byte length prefix as 00 00 00 01 in place; stops at
// the first nonzero prefix byte (the C overflow guard).
func h264ToAnnexBInplace(b []byte) {
	p := 0
	for p < len(b) {
		if b[p] != 0 {
			break // Avoid overflows with this simple check
		}
		l := int(b[p+1])<<16 | int(b[p+2])<<8 | int(b[p+3])
		b[p] = 0
		b[p+1] = 0
		b[p+2] = 0
		b[p+3] = 1
		p += l + 4
	}
}

// h264ToAnnexBBuffered — C: h264_to_annexb_buffered (h264_annexb.c:51-75).
// When dst is nil only the output length is computed.
func h264ToAnnexBBuffered(dst []byte, b []byte, lsize int) int {
	p := 0
	ol := 0
	for p < len(b) {
		l := 0
		for range lsize {
			l = l<<8 | int(b[p])
			p++
		}

		if dst != nil {
			dst[0] = 0
			dst[1] = 0
			dst[2] = 0
			dst[3] = 1
			copy(dst[4:], b[p:p+l])
			dst = dst[4+l:]
		}
		ol += 4 + l
		p += l
	}
	return ol
}

// ToAnnexB — C: h264_to_annexb (h264_annexb.c:81-113).
// Rewrites *datap/*sizep; returns 0 or -1 (tmpbuf alloc failure — in Go
// the scratch slice always materializes, so the -1 path is unreachable,
// same as C once realloc succeeds).
func (ctx *H264AnnexBCtx) ToAnnexB(datap *[]byte, sizep *int) int {
	switch ctx.Lsize {
	case 4:
		h264ToAnnexBInplace((*datap)[:*sizep])
		// C falls through to case 0 — but that case is empty (dead
		// commented-out submit_au + break), so the fall-through is a
		// no-op and Go's implicit break is equivalent.
	case 0:
	case 3, 2, 1:
		l := h264ToAnnexBBuffered(nil, (*datap)[:*sizep], ctx.Lsize)
		if l > ctx.Tmpbufsize {
			ctx.Tmpbuf = make([]byte, l)
			ctx.Tmpbufsize = l
		}
		if ctx.Tmpbuf == nil {
			return -1
		}
		h264ToAnnexBBuffered(ctx.Tmpbuf, (*datap)[:*sizep], ctx.Lsize)
		*datap = ctx.Tmpbuf
		*sizep = l
	}
	return 0
}

// Cleanup — C: h264_to_annexb_cleanup (h264_annexb.h:39-44).
func (ctx *H264AnnexBCtx) Cleanup() {
	ctx.Tmpbuf = nil
	ctx.Extradata = nil
}

// appendExtradata — C: append_extradata (h264_annexb.c:119-125).
func appendExtradata(ctx *H264AnnexBCtx, data []byte) {
	ctx.Extradata = append(ctx.Extradata, data...)
}

// Start — C: h264_to_annexb_init (h264_annexb.c:130-174).
// Parses an avcC extradata blob; collects SPS/PPS (each prefixed with a
// 00 00 00 01 start code) into ctx->extradata and records lsize.
func (ctx *H264AnnexBCtx) Setup(data []byte) {
	buf := []byte{0, 0, 0, 1}

	if len(data) < 7 || data[0] != 1 {
		return
	}

	lsize := int(data[4]&0x3) + 1

	n := int(data[5] & 0x1f)
	data = data[6:]

	for i := 0; i < n && len(data) >= 2; i++ {
		s := int(data[0])<<8 | int(data[1])
		s += 2
		if len(data) < s {
			break
		}

		appendExtradata(ctx, buf)
		appendExtradata(ctx, data[2:s])
		data = data[s:]
	}

	if len(data) < 1 {
		return
	}
	n = int(data[0])
	data = data[1:]

	for i := 0; i < n && len(data) >= 2; i++ {
		s := int(data[0])<<8 | int(data[1])
		s += 2
		if len(data) < s {
			break
		}

		appendExtradata(ctx, buf)
		appendExtradata(ctx, data[2:s])
		data = data[s:]
	}

	ctx.Lsize = lsize
}

// h264AnnexBToAvc — C: h264_annexb_to_avc_t (h264_annexb.c:181-215).
type h264AnnexBToAvc struct {
	Decoder *mediacore.MediaCodec // C: hata_decoder
	MP      *mediacore.MediaPipe  // C: hata_mp

	CreateDecoder func(mc *mediacore.MediaCodec, mcp *mediacore.MediaCodecParams,
		mp *mediacore.MediaPipe) int // C: hata_create_decoder

	Buffer         []byte // C: hata_buffer
	BufferCapacity int    // C: hata_buffer_capacity
	BufferLen      int    // C: hata_buffer_len

	Width  uint16 // C: hata_width
	Height uint16 // C: hata_height

	Avcc    []byte // C: hata_avcc
	AvccLen int    // C: hata_avcc_len

	SPS [8]struct {
		Data   []byte
		Len    int
		Width  uint16
		Height uint16
	}

	PPS [32]struct {
		Data []byte
		Len  int
	}

	PSUpdated bool // C: hata_ps_updated
}

// hataSPS — C: hata_sps (h264_annexb.c:227-254).
func hataSPS(hata *h264AnnexBToAvc, data []byte) {
	if len(data) < 5 {
		return
	}

	var bs miscpkg.BitstreamT
	var sps H264SPS
	miscpkg.SetupRbits(&bs, data[1:], len(data)-1, 0)

	var nilHp *H264Parser
	spsID := nilHp.DecodeSPS(&bs, &sps)
	if spsID < 0 || spsID >= 8 {
		return
	}

	if hata.SPS[spsID].Len == len(data) && bytes.Equal(hata.SPS[spsID].Data, data) {
		return
	}

	hata.SPS[spsID].Width = sps.MBWidth * 16
	hata.SPS[spsID].Height = sps.MBHeight * 16 * uint16(2-sps.MBSOnlyFlag)
	hata.SPS[spsID].Data = slices.Clone(data)
	hata.SPS[spsID].Len = len(data)
	hata.PSUpdated = true
}

// hataPPS — C: hata_pps (h264_annexb.c:260-283).
func hataPPS(hata *h264AnnexBToAvc, data []byte) {
	if len(data) < 2 {
		return
	}

	var bs miscpkg.BitstreamT

	// C: assert(len < 4000)
	if len(data) >= 4000 {
		panic("hata_pps: len >= 4000")
	}

	miscpkg.SetupRbits(&bs, data[1:], len(data)-1, 0)
	ppsID := bs.ReadGolombUe(&bs)
	if ppsID >= 32 {
		return
	}

	if hata.PPS[ppsID].Len == len(data) && bytes.Equal(hata.PPS[ppsID].Data, data) {
		return
	}

	hata.PPS[ppsID].Data = slices.Clone(data)
	hata.PPS[ppsID].Len = len(data)
	hata.PSUpdated = true
}

// hataSlice — C: hata_slice (h264_annexb.c:288-305).
func hataSlice(hata *h264AnnexBToAvc, data []byte) {
	newlen := hata.BufferLen + len(data) + 4

	if newlen > hata.BufferCapacity {
		hata.BufferCapacity = newlen
		nb := make([]byte, hata.BufferCapacity)
		copy(nb, hata.Buffer[:hata.BufferLen])
		hata.Buffer = nb
	}

	hata.Buffer[hata.BufferLen+0] = byte(len(data) >> 24)
	hata.Buffer[hata.BufferLen+1] = byte(len(data) >> 16)
	hata.Buffer[hata.BufferLen+2] = byte(len(data) >> 8)
	hata.Buffer[hata.BufferLen+3] = byte(len(data))

	copy(hata.Buffer[hata.BufferLen+4:], data)
	hata.BufferLen = newlen
}

// hataHandleNAL — C: hata_handle_nal (h264_annexb.c:311-329).
func hataHandleNAL(hata *h264AnnexBToAvc, data []byte) {
	nalUnitType := data[0] & 0x1f
	switch nalUnitType {
	case 7: // SPS
		hataSPS(hata, data)
	case 8: // PPS
		hataPPS(hata, data)
	case 1, 5: // slice
		hataSlice(hata, data)
	}
}

// buildExtradata — C: build_extradata (h264_annexb.c:335-402).
// Writes an avcC blob into out; returns bytes written or 0.
func buildExtradata(hata *h264AnnexBToAvc, out []byte) int {
	start := out
	if len(out) < 6 {
		return 0
	}

	if hata.SPS[0].Len < 4 {
		return 0
	}

	numSPS := 0
	for i := range 8 {
		if hata.SPS[i].Len != 0 {
			numSPS++
		}
	}

	out[0] = 0x1
	out[1] = hata.SPS[0].Data[1]
	out[2] = hata.SPS[0].Data[2]
	out[3] = hata.SPS[0].Data[3]
	out[4] = 0xff
	out[5] = 0xe0 | byte(numSPS)
	outlen := len(out) - 6
	out = out[6:]

	for i := range 8 {
		l := hata.SPS[i].Len
		if l != 0 {
			if outlen < 2+l {
				return 0
			}
			out[0] = byte(l >> 8)
			out[1] = byte(l)
			copy(out[2:], hata.SPS[i].Data[:l])
			out = out[2+l:]
			outlen -= 2 + l

			hata.Width = hata.SPS[i].Width
			hata.Height = hata.SPS[i].Height
		}
	}

	if outlen < 1 {
		return 0
	}

	numPPS := 0
	for i := range 32 {
		if hata.PPS[i].Len != 0 {
			numPPS++
		}
	}

	out[0] = byte(numPPS)
	out = out[1:]
	outlen--

	for i := range 32 {
		l := hata.PPS[i].Len
		if l != 0 {
			if outlen < 2+l {
				return 0
			}
			out[0] = byte(l >> 8)
			out[1] = byte(l)
			copy(out[2:], hata.PPS[i].Data[:l])
			out = out[2+l:]
			outlen -= 2 + l
		}
	}
	return len(start) - len(out)
}

// hataDecode — C: hata_decode (h264_annexb.c:408-488).
// vd is any — mirrors C's `struct video_decoder *` pass-through.
func hataDecode(mc *mediacore.MediaCodec, vd any,
	mq *mediacore.MediaQueue, mb *mediacore.MediaBuf, reqsize int) {
	hata := mc.Opaque.(*h264AnnexBToAvc)

	d := mb.Data
	l := mb.Size
	p := -1 // C: const uint8_t *p = NULL — index into d, -1 = NULL

	i := 0
	for l-i > 3 {
		if !(d[i] == 0 && d[i+1] == 0 && d[i+2] == 1) {
			i++
			continue
		}

		if p >= 0 {
			hataHandleNAL(hata, d[p:i])
		}

		i += 3
		p = i
	}
	i = l

	if p >= 0 {
		hataHandleNAL(hata, d[p:i])
	}

	if hata.BufferLen == 0 {
		return
	}

	reconfig := false

	if hata.PSUpdated {
		hata.PSUpdated = false
		buf := make([]byte, 4096)

		blen := buildExtradata(hata, buf)
		if blen == 0 {
			return
		}
		buf = buf[:blen]

		if hata.AvccLen != blen || !bytes.Equal(hata.Avcc[:hata.AvccLen], buf) {
			hata.AvccLen = blen
			hata.Avcc = slices.Clone(buf)
			reconfig = true
		}
	}

	if reconfig && hata.Decoder != nil {
		mediacore.MediaCodecDeref(hata.Decoder)
		hata.Decoder = nil
	}

	if hata.Decoder == nil {
		// C: media_codec_params_t mcp = {0}; extradata/size/width/height
		mcp := &mediacore.MediaCodecParams{
			ExtraData:     hata.Avcc[:hata.AvccLen],
			ExtraDataSize: hata.AvccLen,
			Width:         int(hata.Width),
			Height:        int(hata.Height),
		}

		// C: media_codec_create(mc->codec_id, 0, NULL, NULL, &mcp, mc->mp)
		hata.Decoder = mediacore.MediaCodecCreate(mc.CodecID, 0, nil, nil, mcp, mc.MP)
	}

	// C: media_buf_t mb2 = *mb; mb2.mb_data = hata->hata_buffer; ...
	mb2 := *mb
	mb2.Data = hata.Buffer[:hata.BufferLen]
	mb2.Size = hata.BufferLen
	mb2.Codec = nil
	mb2.Dtor = nil
	hata.Decoder.Decode(hata.Decoder, vd, mq, &mb2, reqsize)
	hata.BufferLen = 0
}

// hataFlush — C: hata_flush (h264_annexb.c:494-503).
func hataFlush(mc *mediacore.MediaCodec, vd any) {
	hata := mc.Opaque.(*h264AnnexBToAvc)
	mc = hata.Decoder

	if mc == nil || mc.Flush == nil {
		return
	}
	mc.Flush(mc, vd)
}

// hataClose — C: hata_close (h264_annexb.c:509-519).
func hataClose(mc *mediacore.MediaCodec) {
	hata := mc.Opaque.(*h264AnnexBToAvc)

	mc = hata.Decoder

	if mc != nil {
		mediacore.MediaCodecDeref(mc)
	}
	// C: free(hata) — GC
}

// H264AnnexBToAvc — C: h264_annexb_to_avc (h264_annexb.c:527-545).
// Converts a h264 annexb into an AVC stream, creating decoders as
// needed. C: assert(mc->codec_id == AV_CODEC_ID_H264).
func H264AnnexBToAvc(mc *mediacore.MediaCodec, mp *mediacore.MediaPipe,
	create func(mc *mediacore.MediaCodec, mcp *mediacore.MediaCodecParams,
		mp *mediacore.MediaPipe) int) int {
	hata := &h264AnnexBToAvc{}

	// C: assert(mc->codec_id == AV_CODEC_ID_H264) — callers carry raw
	// FFmpeg codec ids, not the mediacore enum.
	if mc.CodecID != mediacore.CodecID(libav.AVCodecIDH264) {
		panic("h264_annexb_to_avc: codec_id != H264")
	}

	// C: hata->hata_create_decoder = create — stored, never invoked by
	// hata_decode (which uses the media_codec_create registry instead).
	hata.CreateDecoder = create
	hata.MP = mp

	mc.Decode = hataDecode
	mc.Close = hataClose
	mc.Flush = hataFlush
	mc.Opaque = hata
	return 0
}
