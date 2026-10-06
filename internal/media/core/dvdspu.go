package core

import (
	"slices"
	"strings"
)

// Canonical port of src/subtitles/dvdspu.c.
//
// dvdspu_t entries live on mp->mp_spu_queue (guarded by
// mp->mp_overlay_mutex). The decoder thread feeds raw DVD subpicture
// packets via dvdspu_enqueue; the video overlay renderer calls
// dvdspu_decode against the queue head.

// DVDSPU — C: dvdspu_t (subtitles/dvdspu.h)
type DVDSPU struct {
	Data         []byte // C: d_data — appended packet payload
	Size         int    // C: d_size
	CmdPos       int    // C: d_cmdpos (-1 = exhausted)
	PTS          int64  // C: d_pts
	CLUT         [16]uint32
	Palette      [4]int // C: d_palette — 2-bit palette indices
	Alpha        [4]int // C: d_alpha
	X1, X2       int    // C: d_x1, d_x2
	Y1, Y2       int    // C: d_y1, d_y2
	Bitmap       []byte // C: d_bitmap — 2-bit indexed bitmap
	CanvasWidth  int    // C: d_canvas_width
	CanvasHeight int    // C: d_canvas_height
	DestroyMe    bool   // C: d_destroyme
}

// getbe16 — C: getbe16
func getbe16(p []byte) int {
	return int(p[0])<<8 | int(p[1])
}

// getNibble — C: get_nibble
func getNibble(buf []byte, nibbleOffset int) int {
	return int(buf[nibbleOffset>>1]>>uint((1-(nibbleOffset&1))<<2)) & 0xf
}

// decodeRLE — C: decode_rle. Decodes the 2-bit RLE field into bitmap
// (index colors 0..3), interlaced by the caller's linesize stride.
func decodeRLE(bitmap []byte, linesize, w, h int, buf []byte,
	nibbleOffset, bufSize int) int {

	nibbleEnd := bufSize * 2
	x := 0
	y := 0
	d := 0 // offset into bitmap (C: uint8_t *d walking linesize)
	for {
		if nibbleOffset >= nibbleEnd {
			return -1
		}
		v := getNibble(buf, nibbleOffset)
		nibbleOffset++
		if v < 0x4 {
			v = (v << 4) | getNibble(buf, nibbleOffset)
			nibbleOffset++
			if v < 0x10 {
				v = (v << 4) | getNibble(buf, nibbleOffset)
				nibbleOffset++
				if v < 0x040 {
					v = (v << 4) | getNibble(buf, nibbleOffset)
					nibbleOffset++
					if v < 4 {
						v |= (w - x) << 2
					}
				}
			}
		}
		length := min(v>>2, w-x)
		color := byte(v & 0x03)

		for i := range length {
			bitmap[d+x+i] = color
		}
		x += length
		if x >= w {
			y++
			if y >= h {
				break
			}
			d += linesize
			x = 0
			// byte align
			nibbleOffset += nibbleOffset & 1
		}
	}
	return 0
}

// DvdspuDecode — C: dvdspu_decode.
//
// C takes (dvdspu_t *d, int64_t pts) and uses TAILQ_NEXT to peek at the
// next queue element when the current one is exhausted; the queue head
// is mp->mp_spu_queue, so the Go port takes mp for the next lookup.
// Returns 1 when palette/alpha were updated (repaint needed), -1 when
// the entry is exhausted/late, 0 otherwise.
func DvdspuDecode(mp *MediaPipe, d *DVDSPU, pts int64) int {
	retval := 0
	buf := d.Data

	if d.CmdPos == -1 {
		// C: d = TAILQ_NEXT(d, d_link)
		var nxt *DVDSPU
		for i, e := range mp.SpuQueue {
			if e == d && i+1 < len(mp.SpuQueue) {
				nxt = mp.SpuQueue[i+1]
				break
			}
		}
		d = nxt
		if d == nil {
			return 0
		}
		if d.PTS <= pts {
			return -1
		}
		return 0
	}

	for d.CmdPos+4 < d.Size {
		date := getbe16(buf[d.CmdPos:])
		picts := d.PTS + int64(date<<10)/90*1000

		if pts != PTSUnset && pts < picts {
			return retval
		}

		nextCmdPos := getbe16(buf[d.CmdPos+2:])

		pos := d.CmdPos + 4
		offset1 := -1
		offset2 := -1
		x1, y1, x2, y2 := 0, 0, 0, 0
		stop := false

		for !stop && pos < d.Size {
			cmd := buf[pos]
			pos++
			switch cmd {
			case 0x00:
				// C: forced display / nop
			case 0x01:
				// Start of picture
			case 0x02:
				// End of picture
				return -1

			case 0x03:
				// set palette
				if d.Size-pos < 2 {
					return -1
				}
				d.Palette[3] = int(buf[pos]) >> 4
				d.Palette[2] = int(buf[pos]) & 0x0f
				d.Palette[1] = int(buf[pos+1]) >> 4
				d.Palette[0] = int(buf[pos+1]) & 0x0f
				retval = 1
				pos += 2

			case 0x04:
				// set alpha
				if d.Size-pos < 2 {
					return -1
				}
				d.Alpha[3] = int(buf[pos]) >> 4
				d.Alpha[2] = int(buf[pos]) & 0x0f
				d.Alpha[1] = int(buf[pos+1]) >> 4
				d.Alpha[0] = int(buf[pos+1]) & 0x0f
				retval = 1
				pos += 2

			case 0x05:
				if d.Size-pos < 6 {
					return -1
				}
				x1 = int(buf[pos])<<4 | int(buf[pos+1])>>4
				x2 = int(buf[pos+1]&0x0f)<<8 | int(buf[pos+2])
				y1 = int(buf[pos+3])<<4 | int(buf[pos+4])>>4
				y2 = int(buf[pos+4]&0x0f)<<8 | int(buf[pos+5])
				pos += 6

			case 0x06:
				if d.Size-pos < 4 {
					return -1
				}
				offset1 = getbe16(buf[pos:])
				offset2 = getbe16(buf[pos+2:])
				pos += 4

			case 0x07:
				// C: blindly reverse-engineered
				d.Alpha[3] = int(buf[pos+10]) >> 4
				d.Alpha[2] = int(buf[pos+10]) & 0x0f
				d.Alpha[1] = int(buf[pos+11]) >> 4
				d.Alpha[0] = int(buf[pos+11]) & 0x0f
				retval = 1
				stop = true

			default:
				stop = true
			}
		}

		if offset1 >= 0 && x2-x1+1 > 0 && y2-y1 > 0 {
			width := x2 - x1 + 1
			height := y2 - y1

			d.X1 = x1
			d.X2 = x2 + 1
			d.Y1 = y1
			d.Y2 = y2

			d.Bitmap = make([]byte, width*height)

			decodeRLE(d.Bitmap, width*2, width, height/2+height&1,
				buf, offset1*2, d.Size)
			decodeRLE(d.Bitmap[width:], width*2, width, height/2,
				buf, offset2*2, d.Size)
		}

		if nextCmdPos == d.CmdPos {
			d.CmdPos = -1
			break
		}
		d.CmdPos = nextCmdPos
	}

	return retval
}

// yuvToRgb — C: yuv_to_rgb (bt601 limited range)
func yuvToRgb(u32 uint32) uint32 {
	Y := int(u32>>16) & 0xff
	V := int(u32>>8) & 0xff
	U := int(u32) & 0xff

	C := Y - 16
	D := U - 128
	E := V - 128

	R := (298*C + 409*E + 128) >> 8
	G := (298*C - 100*D - 208*E + 128) >> 8
	B := (298*C + 516*D + 128) >> 8

	clip256 := func(x int) int {
		if x < 0 {
			return 0
		}
		if x > 255 {
			return 255
		}
		return x
	}

	return uint32(clip256(R)) | uint32(clip256(G))<<8 | uint32(clip256(B))<<16
}

// DvdspuDecodeClut — C: dvdspu_decode_clut — converts the 16-entry YUV
// CLUT to RGB.
func DvdspuDecodeClut(dst, src []uint32) {
	for i := range 16 {
		dst[i] = yuvToRgb(src[i])
	}
}

// DvdspuEnqueue — C: dvdspu_enqueue — appends a raw SPU packet to
// mp->mp_spu_queue (under mp->mp_overlay_mutex).
func DvdspuEnqueue(mp *MediaPipe, data []byte, size int,
	clut []uint32, width, height int, pts int64) {

	if size < 4 {
		return
	}

	d := &DVDSPU{
		Data:         make([]byte, size),
		Size:         size,
		PTS:          pts,
		CanvasWidth:  width,
		CanvasHeight: height,
	}
	copy(d.CLUT[:], clut[:16])
	copy(d.Data, data[:size])

	d.CmdPos = getbe16(d.Data[2:])

	mp.OverlayMutex.Lock()
	mp.SpuQueue = append(mp.SpuQueue, d)
	mp.OverlayMutex.Unlock()
}

// DvdspuDestroyOne — C: dvdspu_destroy_one — removes d from the queue
// and frees it. Caller holds mp->mp_overlay_mutex.
func DvdspuDestroyOne(mp *MediaPipe, d *DVDSPU) {
	for i, e := range mp.SpuQueue {
		if e == d {
			mp.SpuQueue = slices.Delete(mp.SpuQueue, i, i+1)
			break
		}
	}
	d.Bitmap = nil
}

// DvdspuDestroyAll — C: dvdspu_destroy_all. Caller holds
// mp->mp_overlay_mutex.
func DvdspuDestroyAll(mp *MediaPipe) {
	for len(mp.SpuQueue) > 0 {
		DvdspuDestroyOne(mp, mp.SpuQueue[0])
	}
}

// DvdspuFlushLocked — C: dvdspu_flush_locked — marks every queued SPU
// for destruction. Caller holds mp->mp_overlay_mutex.
func DvdspuFlushLocked(mp *MediaPipe) {
	for _, d := range mp.SpuQueue {
		d.DestroyMe = true
	}
}

// dvdspuCodec — C: dvdspu_codec_t
type dvdspuCodec struct {
	clut [16]uint32
	w, h int
}

// dvdspuCodecDecode — C: dvdspu_codec_decode
func dvdspuCodecDecode(mc *MediaCodec, vd any, mq *MediaQueue,
	mb *MediaBuf, reqsize int) {
	dc := mc.Opaque.(*dvdspuCodec)
	DvdspuEnqueue(mc.MP, mb.Data, mb.Size, dc.clut[:], dc.w, dc.h, mb.PTS)
}

// dvdspuCodecClose — C: dvdspu_codec_close
func dvdspuCodecClose(mc *MediaCodec) {
	mc.Opaque = nil
}

// dvdspuCodecCreate — C: dvdspu_codec_create (REGISTER_CODEC prio 50).
// Claims AV_CODEC_ID_DVD_SUBTITLE streams that carry vobsub-style
// extradata (palette:/size: lines).
func dvdspuCodecCreate(mc *MediaCodec, mcp *MediaCodecParams,
	mp *MediaPipe) int {
	if mc.CodecID != CodecIDDVDSubtitle {
		return 1
	}

	extradata := mcpExtradata(mcp)
	if len(extradata) == 0 {
		return 1
	}

	dc := &dvdspuCodec{}

	// C: line loop over the extradata string
	forEachCRLFLine(string(extradata), func(line string) {
		if p, ok := myStrBegins(line, "palette:"); ok {
			VobsubDecodePalette(dc.clut[:], p)
		}
		if p, ok := myStrBegins(line, "size:"); ok {
			VobsubDecodeSize(&dc.w, &dc.h, p)
		}
	})

	mc.Opaque = dc
	mc.Decode = dvdspuCodecDecode
	mc.Close = dvdspuCodecClose
	return 0
}

// VobsubDecodePalette — C: vobsub_decode_palette (subtitles/vobsub.c:37).
// Lives in this package because dvdspu.c's codec create uses it and the
// ext-package vobsub port sits above mediacore in the import DAG.
// Hex values separated by spaces/commas; each entry is RRGGBB byte-
// swapped into C's BGR-in-word layout.
func VobsubDecodePalette(clut []uint32, str string) {
	i := 0
	for len(str) > 0 && i < 16 {
		// C: strtol(str, &end, 16)
		end := 0
		for end < len(str) && str[end] == ' ' {
			end++
		}
		start := end
		for end < len(str) && isHexDigit(str[end]) {
			end++
		}
		if end == start {
			break
		}
		var v uint32
		for _, c := range str[start:end] {
			v = v<<4 | uint32(hexNibbleVal(byte(c)))
		}
		clut[i] = v&0xff0000>>16 | v&0xff00 | v&0xff<<16
		i++
		str = str[end:]
		for len(str) > 0 && str[0] == ' ' {
			str = str[1:]
		}
		if len(str) > 0 && str[0] == ',' {
			str = str[1:]
		}
	}
}

// VobsubDecodeSize — C: vobsub_decode_size (subtitles/vobsub.c:57).
// Parses "WxH"; only writes when both are > 0.
func VobsubDecodeSize(width, height *int, str string) {
	w := atoiPrefix(str)
	_, after, ok := strings.Cut(str, "x")
	if !ok {
		return
	}
	h := atoiPrefix(after)
	if w > 0 && h > 0 {
		*width = w
		*height = h
	}
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexNibbleVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}

// atoiPrefix — C: atoi
func atoiPrefix(s string) int {
	i := 0
	for i < len(s) && s[i] == ' ' {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	v := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		v = v*10 + int(s[i]-'0')
		i++
	}
	if neg {
		return -v
	}
	return v
}

// mcpExtradata — C: mcp->extradata (+ extradata_size)
func mcpExtradata(mcp *MediaCodecParams) []byte {
	if mcp == nil {
		return nil
	}
	if mcp.ExtraData != nil {
		return mcp.ExtraData
	}
	return nil
}

// myStrBegins — C: mystrbegins (str.h) — returns s+len(prefix) if s
// starts with prefix.
func myStrBegins(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

// forEachCRLFLine iterates lines split on CR/LF runs.
// C: for(; l = strcspn(s, "\r\n"), *s; s += l+1+strspn(s+l+1, "\r\n"))
func forEachCRLFLine(s string, fn func(line string)) {
	for len(s) > 0 {
		l := len(s)
		for i := range len(s) {
			if s[i] == '\r' || s[i] == '\n' {
				l = i
				break
			}
		}
		line := s[:l]
		next := l + 1
		for next < len(s) && (s[next] == '\r' || s[next] == '\n') {
			next++
		}
		if next > len(s) {
			next = len(s)
		}
		s = s[next:]
		fn(line)
	}
}

// REGISTER_CODEC(NULL, dvdspu_codec_create, 50)
func registerDvdspuCodec() {
	MediaRegisterCodec(&CodecDef{Open: dvdspuCodecCreate, Prio: 50})
}
