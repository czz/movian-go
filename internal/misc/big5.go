package misc

// Port of src/misc/big5.c (+ utf8_put from src/misc/str.c)

const big5TableOffset = 0xa100 // C: BIG5_TABLE_OFFSET

// C: int utf8_put(char *out, int c) — writes c as (legacy 1-6 byte) UTF-8
// at out, returns bytes written. out may be nil (size query).
func utf8Put(out []byte, c int) int {
	if c == 0xfffe || c == 0xffff || (c >= 0xD800 && c < 0xE000) {
		return 0
	}

	if c < 0x80 {
		if out != nil {
			out[0] = byte(c)
		}
		return 1
	}

	if c < 0x800 {
		if out != nil {
			out[0] = 0xc0 | byte(0x1f&(c>>6))
			out[1] = 0x80 | byte(0x3f&c)
		}
		return 2
	}

	if c < 0x10000 {
		if out != nil {
			out[0] = 0xe0 | byte(0x0f&(c>>12))
			out[1] = 0x80 | byte(0x3f&(c>>6))
			out[2] = 0x80 | byte(0x3f&c)
		}
		return 3
	}

	if c < 0x200000 {
		if out != nil {
			out[0] = 0xf0 | byte(0x07&(c>>18))
			out[1] = 0x80 | byte(0x3f&(c>>12))
			out[2] = 0x80 | byte(0x3f&(c>>6))
			out[3] = 0x80 | byte(0x3f&c)
		}
		return 4
	}

	if c < 0x4000000 {
		if out != nil {
			out[0] = 0xf8 | byte(0x03&(c>>24))
			out[1] = 0x80 | byte(0x3f&(c>>18))
			out[2] = 0x80 | byte(0x3f&(c>>12))
			out[3] = 0x80 | byte(0x3f&(c>>6))
			out[4] = 0x80 | byte(0x3f&c)
		}
		return 5
	}

	if out != nil {
		out[0] = 0xfc | byte(0x01&(c>>30))
		out[1] = 0x80 | byte(0x3f&(c>>24))
		out[2] = 0x80 | byte(0x3f&(c>>18))
		out[3] = 0x80 | byte(0x3f&(c>>12))
		out[4] = 0x80 | byte(0x3f&(c>>6))
		out[5] = 0x80 | byte(0x3f&c)
	}
	return 6
}

// C: int big5_convert(const struct charset *cs, char *dst,
//
//	const uint8_t *src, int len, int strict)
//
// dst == nil → size query (C: dst == NULL).
func Big5Convert(cs any, dst []byte, src []byte, len_, strict int) int {
	outlen := 0
	dstOff := 0

	for i := 0; i < len_; i++ {
		if src[0] < 0x80 {
			if dst != nil {
				dst[dstOff] = src[0]
				dstOff++
			}
			outlen++
			src = src[1:]
			continue
		}

		var in int

		if len_ == 1 {
			in = -1
			src = src[1:]
		} else {
			in = (int(src[0]) << 8) | int(src[1])
			in -= big5TableOffset
			src = src[2:]
			i++
		}

		var out uint16

		if in > len(big5table) { // C: in > sizeof(big5table) / 2
			if strict != 0 {
				return -1
			}
			out = 0xfffd
		} else {
			if in >= 0 {
				out = big5table[in]
			}
			if out == 0 {
				if strict != 0 {
					return -1
				}
				out = 0xfffd
			}
		}

		var w []byte
		if dst != nil {
			w = dst[dstOff:]
		}
		ol := utf8Put(w, int(out))
		outlen += ol
		if dst != nil {
			dstOff += ol
		}
	}
	return outlen
}
