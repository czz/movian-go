package misc

import (
	"fmt"
	charset "github.com/czz/movian-go/internal/charset"
	"unsafe"
)

// charset.CharsetDefaultSrc — C: i18n_get_default_charset() (i18n.c). The
// caller passes the i18n instance (or any resolver) — keeps misc a
// leaf package below i18n (C has no package boundary between str.c
// and i18n.c).

func Utf8Cleanup(str string) string {
	s := []byte(str)
	outlen := 1
	bad := false
	for {
		c := charset.Utf8Get(&s)
		if c == 0 {
			break
		}
		if c == 0xfffd {
			bad = true
		}
		outlen += charset.Utf8Put(nil, c)
	}

	if !bad {
		return ""
	}

	out := make([]byte, outlen)
	o := 0
	s = []byte(str)
	for {
		c := charset.Utf8Get(&s)
		if c == 0 {
			break
		}
		o += charset.Utf8Put(out[o:], c)
	}
	out[o] = 0
	return string(out[:o])
}

func Utf8FromBytes(str []byte, len_ int, cs *charset.Charset, dcs charset.CharsetDefaultSrc) (*Buf, string) {
	if len_ == 0 {
		len_ = len(str)
		for i := range str {
			if str[i] == 0 {
				len_ = i
				break
			}
		}
	}

	var how string
	if cs == nil {
		if dcs != nil {
			cs = dcs.GetDefaultCharset()
		}
		if cs == nil {
			var lang string
			name := charset.CharsetDetector(str[:len_], &lang)
			if name != "" {
				cs = charset.CharsetGet(name)
				if cs != nil {
					if lang != "" {
						how = fmt.Sprintf("Decoded as %s (detected language: %s)", name, lang)
					} else {
						how = fmt.Sprintf("Decoded as %s (detected language: <Unknown>)", name)
					}
				}
				// else: C TRACE(ERROR, "Language %s not found internally")
			} else {
				cs = charset.DefaultCharset()
				how = fmt.Sprintf("Unable to determine character encoding, decoding as %s", cs.Title)
			}
		} else {
			how = fmt.Sprintf("Decoded as %s (specified by user)", cs.Title)
		}
	} else {
		how = fmt.Sprintf("Decoded as %s (specified by request)", cs.Title)
	}

	olen := cs.Convert(cs, nil, str, len_, 0)
	r := BufCreate(olen)
	cs.Convert(cs, bufStrBytes(r), str, len_, 0)
	return r, how
}

// bufStrBytes returns a writable view of b's content (C: buf_str).
func bufStrBytes(b *Buf) []byte {
	return unsafe.Slice((*byte)(b.bPtr), b.bSize+1)
}

func RstrFromBytes(str string, dcs charset.CharsetDefaultSrc) (*Rstr, string) {
	if charset.Utf8Verify(str) != 0 {
		return RstrAllocStr(str), "Decoding as UTF-8"
	}
	b, how := Utf8FromBytes([]byte(str), len(str), nil, dcs)
	r := RstrAllocStr(string(b.C8()))
	b.Release()
	return r, how
}

func RstrFromBytesLen(str []byte, len_ int, dcs charset.CharsetDefaultSrc) (*Rstr, string) {
	zstr := make([]byte, len_+1)
	copy(zstr, str[:len_])
	return RstrFromBytes(string(zstr[:len_]), dcs)
}

// C: static int convert_table(const struct charset *cs, char *dst,
//
//	const uint8_t *src, int len, int strict)
func Ucs2ToUtf8(dst []byte, dstlen int, src []byte, srclen int, le int) {
	o := 0
	i := 0
	for dstlen > 3 && srclen >= 2 {
		var c int
		if le != 0 {
			c = int(src[i]) | int(src[i+1])<<8
		} else {
			c = int(src[i+1]) | int(src[i])<<8
		}
		if c == 0 {
			break
		}
		i += 2
		srclen -= 2
		r := charset.Utf8Put(dst[o:], c)
		o += r
		dstlen -= r
	}
	dst[o] = 0
}

func Utf8ToUcs2(dst []byte, src string, le int) int {
	s := []byte(src)
	o := 0
	for {
		c := charset.Utf8Get(&s)
		if c == 0 {
			break
		}
		if c > 0xffff {
			return -1
		}

		if dst != nil {
			if le != 0 {
				dst[o] = byte(c)
				dst[o+1] = byte(c >> 8)
			} else {
				dst[o] = byte(c >> 8)
				dst[o+1] = byte(c)
			}
		}
		o += 2
	}
	if dst != nil {
		dst[o] = 0
		dst[o+1] = 0
	}
	o += 2
	return o
}

func Utf8ToAscii(dst []byte, src string) int {
	s := []byte(src)
	o := 0
	for {
		c := charset.Utf8Get(&s)
		if c == 0 {
			break
		}
		if c > 0xff {
			return -1
		}

		if dst != nil {
			dst[o] = byte(c)
		}
		o += 1
	}
	if dst != nil {
		dst[o] = 0
	}
	o += 1
	return o
}

func Utf16ToUtf8(b *Buf) *Buf {
	src := b.C8()
	len_ := b.bSize
	le := 0
	if len_ < 2 {
		return nil
	}

	if src[0] == 0xff && src[1] == 0xfe {
		le = 1
		src = src[2:]
		len_ -= 2
	} else if src[0] == 0xfe && src[1] == 0xff {
		src = src[2:]
		len_ -= 2
	}

	src2 := src
	len2 := len_

	olen := 0
	for len_ >= 2 {
		c := int(src[BoolToInt(le == 0)]) | int(src[le])<<8
		olen += charset.Utf8Put(nil, c)
		src = src[2:]
		len_ -= 2
	}

	out := BufCreate(olen)

	o2 := bufStrBytes(out)
	off := 0
	for len2 >= 2 {
		c := int(src2[BoolToInt(le == 0)]) | int(src2[le])<<8
		off += charset.Utf8Put(o2[off:], c)
		src2 = src2[2:]
		len2 -= 2
	}
	o2[off] = 0
	off++
	// C: assert(o2 == buf_str(out) + olen + 1)
	if off != olen+1 {
		panic("utf16_to_utf8: length mismatch")
	}
	b.Release()
	return out
}
