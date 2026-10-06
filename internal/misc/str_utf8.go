package misc

import (
	"fmt"
	"strings"
	"unsafe"
)

// CharsetDefaultSrc — C: i18n_get_default_charset() (i18n.c). The
// caller passes the i18n instance (or any resolver) — keeps misc a
// leaf package below i18n (C has no package boundary between str.c
// and i18n.c).
type CharsetDefaultSrc interface {
	GetDefaultCharset() *Charset
}

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

func Utf8Cleanup(str string) string {
	s := []byte(str)
	outlen := 1
	bad := false
	for {
		c := Utf8Get(&s)
		if c == 0 {
			break
		}
		if c == 0xfffd {
			bad = true
		}
		outlen += Utf8Put(nil, c)
	}

	if !bad {
		return ""
	}

	out := make([]byte, outlen)
	o := 0
	s = []byte(str)
	for {
		c := Utf8Get(&s)
		if c == 0 {
			break
		}
		o += Utf8Put(out[o:], c)
	}
	out[o] = 0
	return string(out[:o])
}

// Charset — C: typedef struct charset { id, title, table, convert, aliases }
type Charset struct {
	ID      string
	Title   string
	Table   *[256]uint16
	Convert func(cs *Charset, dst []byte, src []byte, len_ int, strict int) int
	Aliases []string
}

func Utf8FromBytes(str []byte, len_ int, cs *Charset, dcs CharsetDefaultSrc) (*Buf, string) {
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
			name := CharsetDetector(str[:len_], &lang)
			if name != "" {
				cs = CharsetGet(name)
				if cs != nil {
					if lang != "" {
						how = fmt.Sprintf("Decoded as %s (detected language: %s)", name, lang)
					} else {
						how = fmt.Sprintf("Decoded as %s (detected language: <Unknown>)", name)
					}
				}
				// else: C TRACE(ERROR, "Language %s not found internally")
			} else {
				cs = &charsets[0]
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

func RstrFromBytes(str string, dcs CharsetDefaultSrc) (*Rstr, string) {
	if Utf8Verify(str) != 0 {
		return RstrAllocStr(str), "Decoding as UTF-8"
	}
	b, how := Utf8FromBytes([]byte(str), len(str), nil, dcs)
	r := RstrAllocStr(string(b.C8()))
	b.Release()
	return r, how
}

func RstrFromBytesLen(str []byte, len_ int, dcs CharsetDefaultSrc) (*Rstr, string) {
	zstr := make([]byte, len_+1)
	copy(zstr, str[:len_])
	return RstrFromBytes(string(zstr[:len_]), dcs)
}

var casefoldtable []uint16

func unicodeCasefold(i uint) int {
	if int(i) < len(casefoldtable) {
		if r := int(casefoldtable[i]); r != 0 {
			return r
		}
	}
	return int(i)
}

// C: static int convert_table(const struct charset *cs, char *dst,
//
//	const uint8_t *src, int len, int strict)
func convertTable(cs *Charset, dst []byte, src []byte, len_ int, strict int) int {
	olen := 0
	cp := cs.Table[:]
	dstOff := 0

	for i := range len_ {
		if src[i] == 0 {
			break
		}
		c := int(cp[src[i]])
		if c == 0 {
			c = 0xfffd
		}
		l := Utf8Put(nil, c)
		if dst != nil {
			Utf8Put(dst[dstOff:], c)
			dstOff += l
		}
		olen += l
	}
	return olen
}

// C: static int convert_iso_8859_1(const struct charset *cs, char *dst,
//
//	const uint8_t *src, int len, int strict)
func convertIso88591(cs *Charset, dst []byte, src []byte, len_ int, strict int) int {
	olen := 0
	dstOff := 0

	for i := range len_ {
		if src[i] == 0 {
			break
		}
		l := Utf8Put(nil, int(src[i]))
		if dst != nil {
			Utf8Put(dst[dstOff:], int(src[i]))
			dstOff += l
		}
		olen += l
	}
	return olen
}

// C: const static charset_t charsets[]
var charsets = []Charset{
	// ISO-8859-1 must be first
	{"ISO-8859-1", "ISO-8859-1 (Latin-1)", nil, convertIso88591,
		[]string{"iso-ir-100", "ISO_8859-1", "latin1", "latin-1", "l1",
			"IBM819", "CP819", "csISOLatin1"}},
	{"ISO-8859-2", "ISO-8859-2 (Latin-2)", &ISO_8859_2, convertTable,
		[]string{"iso-ir-101", "ISO_8859-2", "latin2", "l2", "csISOLatin2"}},
	{"ISO-8859-3", "ISO-8859-3 (Latin-3)", &ISO_8859_3, convertTable,
		[]string{"iso-ir-109", "ISO_8859-3", "latin3", "l3", "csISOLatin3"}},
	{"ISO-8859-4", "ISO-8859-4 (Latin-4)", &ISO_8859_4, convertTable,
		[]string{"iso-ir-110", "ISO_8859-4", "latin4", "l4", "csISOLatin4"}},
	{"ISO-8859-5", "ISO-8859-5 (Latin/Cyrillic)", &ISO_8859_5, convertTable,
		[]string{"iso-ir-144", "ISO_8859-5", "cyrillic", "csISOLatinCyrillic"}},
	{"ISO-8859-6", "ISO-8859-6 (Latin/Arabic)", &ISO_8859_6, convertTable,
		[]string{"iso-ir-127", "ISO_8859-6", "ECMA-114", "ASMO-708",
			"arabic", "csISOLatinArabic"}},
	{"ISO-8859-7", "ISO-8859-7 (Latin/Greek)", &ISO_8859_7, convertTable,
		[]string{"iso-ir-126", "ISO_8859-7", "ELOT_928", "ECMA-118",
			"greek", "greek8", "csISOLatinGreek"}},
	{"ISO-8859-8", "ISO-8859-8 (Latin/Hebrew)", &ISO_8859_8, convertTable,
		[]string{"iso-ir-138", "ISO_8859-8", "hebrew", "csISOLatinHebrew"}},
	{"ISO-8859-9", "ISO-8859-9 (Latin-5/Turkish)", &ISO_8859_9, convertTable,
		[]string{"iso-ir-148", "ISO_8859-9", "latin5", "l5", "csISOLatin5"}},
	{"ISO-8859-10", "ISO-8859-10 (Latin-6)", &ISO_8859_10, convertTable,
		[]string{"iso-ir-157", "l6", "ISO_8859-10:1992", "csISOLatin6", "latin6"}},
	{"ISO-8859-11", "ISO-8859-11 (Latin/Thai)", &ISO_8859_11, convertTable, nil},
	{"ISO-8859-13", "ISO-8859-13 (Baltic Rim)", &ISO_8859_13, convertTable, nil},
	{"ISO-8859-14", "ISO-8859-14 (Celtic)", &ISO_8859_14, convertTable, nil},
	{"ISO-8859-15", "ISO-8859-15 (Latin-9)", &ISO_8859_15, convertTable, nil},
	{"ISO-8859-16", "ISO-8859-16 (Latin-10)", &ISO_8859_16, convertTable, nil},
	{"CP1250", "Windows 1250", &CP1250, convertTable,
		[]string{"windows-1250", "cswindows1250"}},
	{"CP1251", "Windows 1251", &CP1251, convertTable,
		[]string{"windows-1251", "cswindows1251"}},
	{"CP1252", "Windows 1252", &CP1252, convertTable,
		[]string{"windows-1252", "cswindows1252"}},
	{"CP1253", "Windows 1253", &CP1253, convertTable,
		[]string{"windows-1253", "cswindows1253"}},
	{"CP1254", "Windows 1254", &CP1254, convertTable,
		[]string{"windows-1254", "cswindows1254"}},
	{"CP1255", "Windows 1255", &CP1255, convertTable,
		[]string{"windows-1255", "cswindows1255"}},
	{"CP1256", "Windows 1256", &CP1256, convertTable,
		[]string{"windows-1256", "cswindows1256"}},
	{"CP1257", "Windows 1257", &CP1257, convertTable,
		[]string{"windows-1257", "cswindows1257"}},
	{"CP1258", "Windows 1258", &CP1258, convertTable,
		[]string{"windows-1258", "cswindows1258"}},
	{"BIG5", "BIG5", nil, big5ConvertCharset, nil},
}

// big5ConvertCharset adapts Big5Convert (which takes any) to the
// charset convert signature. C: big5_convert matches charset_t.convert.
func big5ConvertCharset(cs *Charset, dst []byte, src []byte, len_ int, strict int) int {
	return Big5Convert(cs, dst, src, len_, strict)
}

// C: const charset_t *charset_get_idx(unsigned int i)
func CharsetGetIdx(i uint) *Charset {
	if int(i) < len(charsets) {
		return &charsets[i]
	}
	return nil
}

// C: const charset_t *charset_get(const char *id)
func CharsetGet(id string) *Charset {
	if id == "" {
		return &charsets[0]
	}

	for i := range charsets {
		if strings.EqualFold(id, charsets[i].ID) {
			return &charsets[i]
		}
		for _, a := range charsets[i].Aliases {
			if strings.EqualFold(id, a) {
				return &charsets[i]
			}
		}
	}

	return nil
}

func CharsetGetName(p []uint16) string {
	for i := range charsets {
		if len(p) > 0 && charsets[i].Table != nil &&
			&p[0] == &charsets[i].Table[0] {
			return charsets[i].Title
		}
	}
	return "???"
}

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
		r := Utf8Put(dst[o:], c)
		o += r
		dstlen -= r
	}
	dst[o] = 0
}

func Utf8ToUcs2(dst []byte, src string, le int) int {
	s := []byte(src)
	o := 0
	for {
		c := Utf8Get(&s)
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
		c := Utf8Get(&s)
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
		olen += Utf8Put(nil, c)
		src = src[2:]
		len_ -= 2
	}

	out := BufCreate(olen)

	o2 := bufStrBytes(out)
	off := 0
	for len2 >= 2 {
		c := int(src2[BoolToInt(le == 0)]) | int(src2[le])<<8
		off += Utf8Put(o2[off:], c)
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
