package text

import (
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/misc"
)

// C: src/text/parser.c — text_parse and helpers

// C: typedef struct parse_ctx { char eol_reset_bold; char eol_reset_italic;
//
//	char eol_reset_color; } parse_ctx_t;
type parseCtxT struct {
	eolResetBold   bool
	eolResetItalic bool
	eolResetColor  bool
}

// C: static int add_one_code(int c, uint32_t *output, int olen)
func addOneCode(c int, output []uint32, olen int) int {
	if output != nil {
		output[olen] = uint32(c)
	}
	return olen + 1
}

// C: static int is_ws(char c)
func isWs(c byte) bool {
	return c == ' ' || c == '\t'
}

// attribFn matches C's attrib callback signature.
// C: int (*fn)(uint32_t *output, int olen, const char *attrib,
//
//	const char *value, int context)
type attribFn func(output []uint32, olen int, attrib, value string, context int) int

// C: static int attrib_parser(char *v, uint32_t *output, int olen,
//
//	int (*fn)(...), int context)
//
// The C version writes NUL bytes into v to NUL-terminate attrib/value.
// Go: v is the mutable tmp buffer; we write the same NULs and slice.
func attribParser(v []byte, output []uint32, olen int, fn attribFn, context int) int {
	var attrib, value []byte
	var quote byte
	i := 0

	for i < len(v) && v[i] != 0 {
		for i < len(v) && isWs(v[i]) {
			i++
		}
		if i >= len(v) || v[i] == 0 {
			return olen
		}
		attribStart := i
		for {
			if i >= len(v) || v[i] == 0 {
				return olen
			}
			if isWs(v[i]) || v[i] == '=' {
				break
			}
			i++
		}
		attribStop := i
		for i < len(v) && isWs(v[i]) {
			i++
		}
		if i >= len(v) || v[i] != '=' {
			return olen
		}
		i++
		v[attribStop] = 0
		attrib = v[attribStart:attribStop]
		for i < len(v) && isWs(v[i]) {
			i++
		}
		if i >= len(v) {
			return olen
		}
		quote = v[i]
		i++
		if quote != '"' && quote != '\'' {
			return olen
		}
		valueStart := i
		for {
			if i >= len(v) || v[i] == 0 {
				return olen
			}
			if v[i] == quote {
				break
			}
			i++
		}
		v[i] = 0
		i++
		value = v[valueStart : i-1]
		olen = fn(output, olen, string(attrib), string(value), context)
	}
	return olen
}

// C: static int font_tag(uint32_t *output, int olen, const char *attrib,
//
//	const char *value, int context)
func fontTag(sys *System, output []uint32, olen int, attrib, value string, context int) int {
	if strings.EqualFold(attrib, "size") {
		sz, _ := strconv.Atoi(value)
		if sz > 7 {
			sz = 7
		}
		if sz < 1 {
			sz = 1
		}
		return addOneCode(TR_CODE_FONT_SIZE|sz, output, olen)
	}
	if strings.EqualFold(attrib, "face") {
		return addOneCode(TR_CODE_FONT_FAMILY|
			sys.FreetypeFamilyId(value, context), output, olen)
	}
	if strings.EqualFold(attrib, "color") {
		return addOneCode(TR_CODE_COLOR|
			int(misc.HtmlMakecolor(value)), output, olen)
	}
	return olen
}

// C: static int outline_tag(...)
func outlineTag(output []uint32, olen int, attrib, value string, context int) int {
	if strings.EqualFold(attrib, "size") {
		sz, _ := strconv.Atoi(value)
		if sz > 10 {
			sz = 10
		}
		return addOneCode(TR_CODE_OUTLINE|sz, output, olen)
	}
	if strings.EqualFold(attrib, "color") {
		return addOneCode(TR_CODE_OUTLINE_COLOR|
			int(misc.HtmlMakecolor(value)), output, olen)
	}
	return olen
}

// C: static int shadow_tag(...)
func shadowTag(output []uint32, olen int, attrib, value string, context int) int {
	if strings.EqualFold(attrib, "displacement") {
		sz, _ := strconv.Atoi(value)
		if sz > 10 {
			sz = 10
		}
		return addOneCode(TR_CODE_SHADOW|sz, output, olen)
	}
	if strings.EqualFold(attrib, "color") {
		return addOneCode(TR_CODE_SHADOW_COLOR|
			int(misc.HtmlMakecolor(value)), output, olen)
	}
	return olen
}

// C: static int html_tag_to_code(char *s, uint32_t *output, int olen,
//
//	int context, int flags)
//
// The C version writes NULs into s to strip trailing spaces. Go: s is a
// mutable []byte; we apply the same edits and slice.
func htmlTagToCode(sys *System, s []byte, output []uint32, olen int, context int, flags int) int {
	endtag := 0

	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	if len(s) == 0 || s[0] == 0 {
		return olen
	}

	tag := s

	if tag[0] == '/' {
		endtag = 1
		tag = tag[1:]
	}

	for len(tag) > 0 && tag[0] == ' ' {
		tag = tag[1:]
	}

	// Trim trailing spaces by writing NULs, matching C's s[len--] = 0.
	// tag is a subslice of s, so the NULs apply to tag too.
	l := len(s)
	for l > 0 && s[l-1] == ' ' {
		s[l-1] = 0
		l--
	}
	tagStr := misc.CStr(tag)

	var c int
	if strings.EqualFold(tagStr, "ruby") {
		if endtag != 0 {
			c = TR_CODE_FONT_RESET
		} else {
			c = TR_CODE_ITALIC_ON
		}
	} else if strings.EqualFold(tagStr, "rt") {
		olen = addOneCode(TR_CODE_ITALIC_OFF, output, olen)
		c = TR_CODE_START
	} else if endtag == 0 && strings.EqualFold(tagStr, "p") {
		c = TR_CODE_START
	} else if endtag == 0 && strings.EqualFold(tagStr, "br") {
		c = TR_CODE_NEWLINE
	} else if endtag == 0 && strings.EqualFold(tagStr, "hr") {
		c = TR_CODE_HR
	} else if endtag == 0 && strings.EqualFold(tagStr, "margin") {
		c = TR_CODE_SET_MARGIN
	} else if strings.EqualFold(tagStr, "center") {
		if endtag != 0 {
			c = TR_CODE_CENTER_OFF
		} else {
			c = TR_CODE_CENTER_ON
		}
	} else if strings.EqualFold(tagStr, "i") {
		if endtag != 0 {
			c = TR_CODE_ITALIC_OFF
		} else {
			c = TR_CODE_ITALIC_ON
		}
	} else if strings.EqualFold(tagStr, "b") {
		if endtag != 0 {
			c = TR_CODE_BOLD_OFF
		} else {
			c = TR_CODE_BOLD_ON
		}
	} else if len(tagStr) >= 4 && strings.EqualFold(tagStr[:4], "font") {
		if endtag != 0 {
			c = TR_CODE_FONT_RESET
		} else {
			return attribParser(tag[4:], output, olen,
				func(output []uint32, olen int, attrib, value string, context int) int {
					return fontTag(sys, output, olen, attrib, value, context)
				}, context)
		}
	} else if len(tagStr) >= 7 && strings.EqualFold(tagStr[:7], "outline") {
		if endtag != 0 {
			c = TR_CODE_OUTLINE
		} else {
			return attribParser(tag[7:], output, olen, outlineTag, context)
		}
	} else if len(tagStr) >= 6 && strings.EqualFold(tagStr[:6], "shadow") {
		if endtag != 0 {
			c = TR_CODE_SHADOW
		} else {
			return attribParser(tag[6:], output, olen, shadowTag, context)
		}
	} else if flags&TEXT_PARSE_SLOPPY_TAGS != 0 {
		return -1
	} else {
		return olen
	}

	return addOneCode(c, output, olen)
}

// C: static int sub_tag_to_code(char *s, uint32_t *output, int olen,
//
//	int context, int flags, parse_ctx_t *pc)
func subTagToCode(s []byte, output []uint32, olen int, context int, flags int,
	pc *parseCtxT) int {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	if len(s) == 0 || s[0] == 0 {
		return olen
	}

	doreset := false

	switch s[0] {
	default:
		if flags&TEXT_PARSE_SLOPPY_TAGS != 0 {
			return -1
		}
		return olen

	case 'y':
		doreset = true
		fallthrough
	case 'Y':
		if len(s) < 2 || s[1] != ':' {
			if flags&TEXT_PARSE_SLOPPY_TAGS != 0 {
				return -1
			}
			return olen
		}

		i := 0
		for i < len(s) && s[i] != 0 {
			if s[i] == 'b' {
				olen = addOneCode(TR_CODE_BOLD_ON, output, olen)
				pc.eolResetBold = doreset
			} else if s[i] == 'i' {
				olen = addOneCode(TR_CODE_ITALIC_ON, output, olen)
				pc.eolResetItalic = doreset
			}
			i++
		}

	case 'c':
		pc.eolResetColor = true
		fallthrough
	case 'C':
		if len(s) < 2 || s[1] != ':' {
			if flags&TEXT_PARSE_SLOPPY_TAGS != 0 {
				return -1
			}
			return olen
		}
		if len(s) < 3 || s[2] != '$' {
			if flags&TEXT_PARSE_SLOPPY_TAGS != 0 {
				return -1
			}
			return olen
		}
		if cstrlen(s) < 9 {
			break
		}
		olen = addOneCode(TR_CODE_COLOR|
			(misc.Hexnibble(s[3])<<20)|
			(misc.Hexnibble(s[4])<<16)|
			(misc.Hexnibble(s[5])<<12)|
			(misc.Hexnibble(s[6])<<8)|
			(misc.Hexnibble(s[7])<<4)|
			(misc.Hexnibble(s[8])),
			output, olen)
	}
	return olen
}

// cstrlen mimics C strlen on a byte slice (stops at first NUL).
func cstrlen(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return len(b)
}

// C: static int parse_str(uint32_t *output, const char *str, int flags,
//
//	int context, int default_color)
//
// C uses utf8_get(&str) which advances a const char*. Go: str is a []byte
// cursor; misc.Utf8Get(&str) advances it identically.
func parseStr(sys *System, output []uint32, str []byte, flags int, context int,
	defaultColor int) int {
	pc := parseCtxT{}
	olen := 0
	p := -1
	l := cstrlen(str)
	var tmp []byte
	sol := 1 // Start of line

	for {
		c := misc.Utf8Get(&str)
		if c == 0 {
			break
		}
		if c == '\r' {
			continue
		}

		if c == '\n' {
			sol = 1
			if pc.eolResetColor {
				olen = addOneCode(TR_CODE_COLOR|defaultColor,
					output, olen)
				pc.eolResetColor = false
			}

			if pc.eolResetBold {
				olen = addOneCode(TR_CODE_BOLD_OFF, output, olen)
				pc.eolResetBold = false
			}

			if pc.eolResetItalic {
				olen = addOneCode(TR_CODE_ITALIC_OFF, output, olen)
				pc.eolResetItalic = false
			}
		}

		if flags&TEXT_PARSE_SLASH_PREFIX != 0 && sol != 0 && c == '/' {
			olen = addOneCode(TR_CODE_ITALIC_ON, output, olen)
			sol = 0
			continue
		}

		var d int
		if flags&TEXT_PARSE_HTML_TAGS != 0 && c == '<' {
			s2 := str
			lp := 0
			if tmp == nil {
				tmp = make([]byte, l)
			}

			for {
				d = misc.Utf8Get(&str)
				if d == 0 {
					break
				}
				if d == '>' {
					break
				}
				tmp[lp] = byte(d)
				lp++
			}
			if d == 0 {
				if output != nil {
					output[olen] = '<'
				}
				olen++
				str = s2
				continue
			}
			tmp[lp] = 0

			r := htmlTagToCode(sys, tmp[:lp+1], output, olen, context, flags)
			if r != -1 {
				olen = r
				p = -1
				continue
			}
			// Failed to parse tag
			str = s2
		}

		if flags&TEXT_PARSE_SUB_TAGS != 0 && c == '{' {
			s2 := str
			lp := 0
			if tmp == nil {
				tmp = make([]byte, l)
			}

			for {
				d = misc.Utf8Get(&str)
				if d == 0 {
					break
				}
				if d == '}' {
					break
				}
				tmp[lp] = byte(d)
				lp++
			}
			if d == 0 {
				if output != nil {
					output[olen] = '{'
				}
				olen++
				str = s2
				continue
			}
			tmp[lp] = 0
			r := subTagToCode(tmp[:lp+1], output, olen, context, flags, &pc)
			if r != -1 {
				olen = r
				p = -1
				continue
			}
			// Failed to parse tag
			str = s2
		}

		if flags&TEXT_PARSE_HTML_ENTITIES != 0 && c == '&' {
			s2 := str
			lp := 0
			if tmp == nil {
				tmp = make([]byte, l)
			}

			for {
				d = misc.Utf8Get(&str)
				if d == 0 {
					break
				}
				if d == ';' {
					break
				}
				tmp[lp] = byte(d)
				lp++
			}
			if d != 0 {
				tmp[lp] = 0

				c = misc.HtmlEntityLookup(misc.CStr(tmp[:lp]))

				if c != -1 {
					if output != nil {
						output[olen] = uint32(c)
					}
					olen++
				}
				continue
			}
			if output != nil {
				output[olen] = '&'
			}
			olen++
			str = s2
			continue
		}

		if p != -1 {
			d = misc.UnicodeCompose(p, c)
		} else {
			d = -1
		}
		if d != -1 {
			if output != nil {
				output[olen-1] = uint32(d)
			}
			p = -1
		} else {
			p = c
			olen = addOneCode(c, output, olen)
			sol = 0
		}
	}
	return olen
}

// C: uint32_t *text_parse(const char *str, int *lenp, int flags,
//
//	const uint32_t *prefix, int prefixlen, int context)
//
// Go: returns the buffer and length (replaces lenp out-param semantics with
// a return value; nil buffer + 0 length when parse produces nothing).
func (sys *System) TextParse(str string, flags int, prefix []uint32, context int) ([]uint32, int) {
	defaultColor := 0xffffff
	for i := range prefix {
		if prefix[i]&0xff000000 == TR_CODE_COLOR {
			defaultColor = int(prefix[i] & 0xffffff)
		}
	}

	olen := parseStr(sys, nil, []byte(str), flags, context, defaultColor)
	if olen == 0 {
		return nil, 0
	}
	olen += len(prefix)
	buf := make([]uint32, olen)
	copy(buf, prefix)
	parseStr(sys, buf[len(prefix):], []byte(str), flags, context, defaultColor)
	return buf, olen
}
