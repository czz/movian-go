package misc

// Canonical port of src/misc/json.c — the callback-driven JSON
// deserializer used by htsmsg_json.c (and ecmascript helpers).
//
// Unlike encoding/json this parser is deliberately lenient in the
// same ways the C version is: bytes 1-32 count as whitespace,
// unknown escapes return the escaped byte itself, integers are
// parsed with strtol semantics (so "+5" and "05" are valid), and
// \uXXXX is emitted per-unit without surrogate-pair combining.

import (
	"fmt"
	"math"
	"strings"
)

// JSONDeserializer mirrors C's json_deserializer_t (misc/json.h).
// Callbacks receive the opaque value plus the parent object and the
// field name ("" for list elements, where C passes NULL).
type JSONDeserializer struct {
	CreateMap  func(opaque any) any
	CreateList func(opaque any) any
	DestroyObj func(opaque, obj any)
	AddObj     func(opaque, parent any, name string, child any)
	AddString  func(opaque, parent any, name string, str string)
	AddLong    func(opaque, parent any, name string, v int64)
	AddDouble  func(opaque, parent any, name string, v float64)
	AddBool    func(opaque, parent any, name string, v int)
	AddNull    func(opaque, parent any, name string)
}

const (
	jsonOK = iota
	jsonNotThis
	jsonFail
)

// jsonNotThisType is the Go spelling of C's NOT_THIS_TYPE ((void *)-1).
var jsonNotThisType = &struct{}{}

type jsonParser struct {
	s       string
	jd      *JSONDeserializer
	opaque  any
	failpos int
	failmsg string
}

// jsonSkipSpace — C: while(*s > 0 && *s < 33) s++
func (p *jsonParser) skipSpace(pos int) int {
	for pos < len(p.s) && p.s[pos] > 0 && p.s[pos] < 33 {
		pos++
	}
	return pos
}

// jsonStrReadChar — C: json_str_read_char (misc/json.c:40-80).
// Returns (value, newpos): -1 end-of-input, -2 bad escape.
func (p *jsonParser) strReadChar(pos int) (int, int) {
	if pos >= len(p.s) || p.s[pos] == 0 {
		return -1, pos
	}
	if p.s[pos] != '\\' {
		b := []byte(p.s[pos:])
		v := Utf8Get(&b)
		return v, len(p.s) - len(b)
	}
	pos++
	npos := pos + 1
	if pos >= len(p.s) {
		return -1, pos // C: case 0
	}
	switch p.s[pos] {
	case 'b':
		return '\b', npos
	case 'f':
		return '\f', npos
	case 'n':
		return '\n', npos
	case 'r':
		return '\r', npos
	case 't':
		return '\t', npos
	case 'u':
		pos++
		v := 0
		for range 4 {
			var c byte
			if pos < len(p.s) {
				c = p.s[pos]
			}
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= int(c - '0')
			case c >= 'a' && c <= 'f':
				v |= int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v |= int(c-'F') + 10
			default:
				return -2, pos
			}
			pos++
		}
		return v, pos
	default:
		// C: default: return *s — signed char; high bytes go
		// negative and utf8_put() emits them raw.
		return int(int8(p.s[pos])), npos
	}
}

// jsonParseString — C: json_parse_string (misc/json.c:90-131).
func (p *jsonParser) parseString(pos int) (string, int, int) {
	pos = p.skipSpace(pos)
	if pos >= len(p.s) || p.s[pos] != '"' {
		return "", pos, jsonNotThis
	}
	pos++

	var sb []byte
	for {
		if pos >= len(p.s) || p.s[pos] == 0 {
			p.failmsg = "Unexpected end of JSON message"
			p.failpos = pos
			return "", pos, jsonFail
		}
		if p.s[pos] == '"' {
			break
		}
		v, np := p.strReadChar(pos)
		if v == -1 {
			p.failmsg = "Unexpected end of JSON message"
			p.failpos = np
			return "", np, jsonFail
		}
		if v == -2 {
			p.failmsg = "Incorrect escape sequence"
			p.failpos = np
			return "", np, jsonFail
		}
		pos = np
		var tmp [8]byte
		n := Utf8Put(tmp[:], v)
		sb = append(sb, tmp[:n]...)
	}
	return string(sb), pos + 1, jsonOK
}

// jsonParseMap — C: json_parse_map (misc/json.c:137-215).
func (p *jsonParser) parseMap(pos int) (any, int, int) {
	pos = p.skipSpace(pos)
	if pos >= len(p.s) || p.s[pos] != '{' {
		return jsonNotThisType, pos, jsonNotThis
	}
	pos++
	r := p.jd.CreateMap(p.opaque)

	pos = p.skipSpace(pos)
	if pos >= len(p.s) || p.s[pos] != '}' {
		for {
			name, npos, st := p.parseString(pos)
			if st == jsonNotThis {
				p.failmsg = "Expected string"
				p.failpos = pos
				return nil, pos, jsonFail
			}
			if st == jsonFail {
				return nil, pos, jsonFail
			}
			pos = npos

			pos = p.skipSpace(pos)
			if pos >= len(p.s) || p.s[pos] != ':' {
				p.jd.DestroyObj(p.opaque, r)
				p.failmsg = "Expected ':'"
				p.failpos = pos
				return nil, pos, jsonFail
			}
			pos++

			end, ok := p.parseValue(pos, r, name)
			if !ok {
				p.jd.DestroyObj(p.opaque, r)
				return nil, pos, jsonFail
			}
			pos = end

			pos = p.skipSpace(pos)
			if pos < len(p.s) && p.s[pos] == '}' {
				break
			}
			if pos >= len(p.s) || p.s[pos] != ',' {
				p.jd.DestroyObj(p.opaque, r)
				p.failmsg = "Expected ','"
				p.failpos = pos
				return nil, pos, jsonFail
			}
			pos++
		}
	}
	pos++
	return r, pos, jsonOK
}

// jsonParseList — C: json_parse_list (misc/json.c:221-272).
func (p *jsonParser) parseList(pos int) (any, int, int) {
	pos = p.skipSpace(pos)
	if pos >= len(p.s) || p.s[pos] != '[' {
		return jsonNotThisType, pos, jsonNotThis
	}
	pos++
	r := p.jd.CreateList(p.opaque)

	pos = p.skipSpace(pos)
	if pos >= len(p.s) || p.s[pos] != ']' {
		for {
			end, ok := p.parseValue(pos, r, "")
			if !ok {
				p.jd.DestroyObj(p.opaque, r)
				return nil, pos, jsonFail
			}
			pos = end

			pos = p.skipSpace(pos)
			if pos < len(p.s) && p.s[pos] == ']' {
				break
			}
			if pos >= len(p.s) || p.s[pos] != ',' {
				p.jd.DestroyObj(p.opaque, r)
				p.failmsg = "Expected ','"
				p.failpos = pos
				return nil, pos, jsonFail
			}
			pos++
		}
	}
	pos++
	return r, pos, jsonOK
}

// jsonStrtol — C: strtol(s, &ep, 10) — base-10 parse with whitespace
// skip, +/- sign, partial consumption and LONG_MAX/MIN clamping.
// Returns (value, endpos); endpos == pos when no digits (ep == s).
func jsonStrtol(s string, pos int) (int64, int) {
	i := pos
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' ||
		s[i] == '\v' || s[i] == '\f' || s[i] == '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	var v int64
	digits := 0
	overflow := false
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			break
		}
		digits++
		if !overflow {
			d := int64(c - '0')
			if v > (math.MaxInt64-d)/10 {
				overflow = true
			} else {
				v = v*10 + d
			}
		}
	}
	if digits == 0 {
		return 0, pos
	}
	if overflow {
		if neg {
			return math.MinInt64, i
		}
		return math.MaxInt64, i
	}
	if neg {
		v = -v
	}
	return v, i
}

// jsonParseInteger — C: json_parse_integer (misc/json.c:297-323).
// Rejects numbers ending at NUL, followed by '.', 'e' or 'E'
// (→ floating point), and strtol LONG_MIN/LONG_MAX results.
func (p *jsonParser) parseInteger(pos int) (int64, int, bool) {
	pos = p.skipSpace(pos)
	s2 := pos
	if s2 < len(p.s) && p.s[s2] == '-' {
		s2++
	}
	for s2 < len(p.s) && p.s[s2] >= '0' && p.s[s2] <= '9' {
		s2++
	}
	if s2 >= len(p.s) || p.s[s2] == 0 {
		return 0, pos, false // *s2 == 0
	}
	if p.s[s2] == '.' || p.s[s2] == 'e' || p.s[s2] == 'E' {
		return 0, pos, false // Is floating point
	}
	v, ep := jsonStrtol(p.s, pos)
	if v == math.MinInt64 || v == math.MaxInt64 {
		return 0, pos, false
	}
	if ep == pos {
		return 0, pos, false
	}
	return v, ep, true
}

// jsonParseDouble — C: json_parse_double (misc/json.c:277-291).
func (p *jsonParser) parseDouble(pos int) (float64, int, bool) {
	pos = p.skipSpace(pos)
	d, ep := MyStr2double(p.s[pos:])
	if ep == 0 {
		return 0, pos, false // ep == s
	}
	return d, pos + ep, true
}

// jsonParseValue — C: json_parse_value (misc/json.c:328-392).
func (p *jsonParser) parseValue(pos int, parent any, name string) (int, bool) {
	c, end, st := p.parseMap(pos)
	if st == jsonFail {
		return pos, false
	}
	if st == jsonOK {
		p.jd.AddObj(p.opaque, parent, name, c)
		return end, true
	}

	c, end, st = p.parseList(pos)
	if st == jsonFail {
		return pos, false
	}
	if st == jsonOK {
		p.jd.AddObj(p.opaque, parent, name, c)
		return end, true
	}

	str, end2, st2 := p.parseString(pos)
	if st2 == jsonFail {
		return pos, false
	}
	if st2 == jsonOK {
		p.jd.AddString(p.opaque, parent, name, str)
		return end2, true
	}

	if l, iep, ok := p.parseInteger(pos); ok {
		p.jd.AddLong(p.opaque, parent, name, l)
		return iep, true
	} else if d, dep, ok2 := p.parseDouble(pos); ok2 {
		p.jd.AddDouble(p.opaque, parent, name, d)
		return dep, true
	}

	pos = p.skipSpace(pos)
	rest := ""
	if pos < len(p.s) {
		rest = p.s[pos:]
	}
	if strings.HasPrefix(rest, "true") {
		p.jd.AddBool(p.opaque, parent, name, 1)
		return pos + 4, true
	}
	if strings.HasPrefix(rest, "false") {
		p.jd.AddBool(p.opaque, parent, name, 0)
		return pos + 5, true
	}
	if strings.HasPrefix(rest, "null") {
		p.jd.AddNull(p.opaque, parent, name)
		return pos + 4, true
	}

	p.failmsg = "Unknown token"
	p.failpos = pos
	return pos, false
}

// JSONDeserialize — C: json_deserialize (misc/json.c:398-430).
// Returns (root, errmsg): root is nil on failure. Trailing bytes
// after the root object/array are ignored, as in C.
func JSONDeserialize(src string, jd *JSONDeserializer, opaque any) (any, string) {
	p := &jsonParser{s: src, jd: jd, opaque: opaque}

	c, _, st := p.parseMap(0)
	if st == jsonNotThis {
		c, _, st = p.parseList(0)
	}
	if st == jsonNotThis {
		return nil, "Invalid JSON, expected '{' or '['"
	}
	if st == jsonFail {
		offset := p.failpos
		if offset > len(src) || offset < 0 {
			return nil, fmt.Sprintf("%s at (bad) offset %d", p.failmsg, offset)
		}
		offset -= 10
		if offset < 0 {
			offset = 0
		}
		end := min(offset+20, len(src))
		return nil, fmt.Sprintf("%s at offset %d : '%s'", p.failmsg, offset,
			src[offset:end])
	}
	return c, ""
}
