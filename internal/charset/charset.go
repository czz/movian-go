package charset

import "strings"

// Charset — C: typedef struct charset { id, title, table, convert, aliases }
type Charset struct {
	ID      string
	Title   string
	Table   *[256]uint16
	Convert func(cs *Charset, dst []byte, src []byte, len_ int, strict int) int
	Aliases []string
}

// CharsetDefaultSrc — C: charset_dcs_t interface (str.h).
type CharsetDefaultSrc interface {
	GetDefaultCharset() *Charset
}

// DefaultCharset — the charsets[0] fallback used when detection fails.
// C: charsets[0] (str.c).
func DefaultCharset() *Charset { return &charsets[0] }

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
