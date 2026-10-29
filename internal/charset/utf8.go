package charset

// Utf8Get/Uft8Put — raw UTF-8 decode/encode primitives used by the
// charset convert path and by misc string helpers.
// C: utf8_get/utf8_put (str.c).

func Utf8Get(s *[]byte) int {
	if len(*s) == 0 {
		return 0
	}
	c := (*s)[0]
	*s = (*s)[1:]

	var r, l, m int

	switch {
	case c <= 127:
		return int(c)

	case c >= 194 && c <= 223:
		r = int(c & 0x1f)
		l = 1
		m = 0x80

	case c >= 224 && c <= 239:
		r = int(c & 0xf)
		l = 2
		m = 0x800

	case c >= 240 && c <= 247:
		r = int(c & 0x7)
		l = 3
		m = 0x10000

	case c >= 248 && c <= 251:
		r = int(c & 0x3)
		l = 4
		m = 0x200000

	case c >= 252 && c <= 253:
		r = int(c & 0x1)
		l = 5
		m = 0x4000000

	default:
		return 0xfffd
	}

	for l > 0 {
		l--
		if len(*s) == 0 {
			return 0xfffd
		}
		c = (*s)[0]
		if (c & 0xc0) != 0x80 {
			return 0xfffd
		}
		*s = (*s)[1:]
		r = r<<6 | int(c&0x3f)
	}
	if r < m {
		return 0xfffd // overlong sequence
	}

	return r
}

func Utf8Put(out []byte, c int) int {
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

// Utf8Verify — C: utf8_verify (str.c): 0 when the string contains the
// replacement char (decode failure marker).
func Utf8Verify(str string) int {
	b := []byte(str)
	for {
		c := Utf8Get(&b)
		if c == 0 {
			break
		}
		if c == 0xfffd {
			return 0
		}
	}
	return 1
}
