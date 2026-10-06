package misc

import (
	"crypto/rand"
	"fmt"
	"strings"
)

func StrCleanup(s []byte, forbidden string) {
	i := 0
	for i < len(s) && s[i] != 0 {
		if strings.IndexByte(forbidden, s[i]) >= 0 {
			s[i] = '_'
		}
		i++
	}
}

// C: #define URL_ESCAPE_PATH 1 / URL_ESCAPE_PARAM 2 / URL_ESCAPE_SPACE_ONLY 3
const (
	URLEscapePath      = 1
	URLEscapeParam     = 2
	URLEscapeSpaceOnly = 3
)

func noThe(a string, ignoreThe int) string {
	if ignoreThe == 0 {
		return a
	}
	if len(a) < 3 {
		return a
	}
	if a[0] != 't' && a[0] != 'T' {
		return a
	}
	if a[1] != 'h' && a[1] != 'H' {
		return a
	}
	if a[2] != 'e' && a[2] != 'E' {
		return a
	}

	i := 3
	for i < len(a) && (a[i] == ' ' || a[i] == '.') {
		i++
	}
	return a[i:]
}

func Dictcmp(a, b string, ignoreThe int) int {
	a = noThe(a, ignoreThe)
	b = noThe(b, ignoreThe)

	ab := []byte(a)
	bb := []byte(b)

	for {
		ua := Utf8Get(&ab)
		ub := Utf8Get(&bb)

		adig := 0
		if ua >= '0' && ua <= '9' {
			adig = 1
		}
		bdig := 0
		if ub >= '0' && ub <= '9' {
			bdig = 1
		}
		switch adig | bdig<<1 {
		case 0: /* 0: a is not a digit, nor is b */
			if ua != ub {
				ua = unicodeCasefold(uint(ua))
				ub = unicodeCasefold(uint(ub))
				if ua != ub {
					return ua - ub
				}
			}
			if ua == 0 {
				return 0
			}
		case 1: /* 1: a is a digit,  b is not */
			return ua - ub
		case 2: /* 2: a is not a digit,  b is */
			return ua - ub
		case 3: /* both are digits, switch to integer compare */
			da := int64(ua - '0')
			db := int64(ub - '0')

			for len(ab) > 0 && ab[0] >= '0' && ab[0] <= '9' {
				da = da*10 + int64(ab[0]-'0')
				ab = ab[1:]
			}
			for len(bb) > 0 && bb[0] >= '0' && bb[0] <= '9' {
				db = db*10 + int64(bb[0]-'0')
				bb = bb[1:]
			}
			if da != db {
				return int(da - db)
			}
		}
	}
}

func Mystrstr(haystack, needle string) string {
	h := []byte(haystack)
	n := []byte(needle)

	nv := unicodeCasefold(uint(Utf8Get(&n)))

	for {
		r := h
		hv := unicodeCasefold(uint(Utf8Get(&h)))
		if hv == 0 {
			return ""
		}

		if nv == hv {
			h1 := h
			n1 := n

			for {
				nv = unicodeCasefold(uint(Utf8Get(&n1)))
				if nv == 0 {
					return string(r)
				}
				hv = unicodeCasefold(uint(Utf8Get(&h1)))
				if nv != hv {
					break
				}
			}
		}
	}
}

func UnicodeStart() {
	n := len(unicodeCasefolding) / 2
	x := int(unicodeCasefolding[n*2-1])
	casefoldtable = make([]uint16, x)

	for i := range n {
		from := unicodeCasefolding[i*2+0]
		to := unicodeCasefolding[i*2+1]
		casefoldtable[from] = to
	}
}

func StrvecSplit(str string, ch byte) []string {
	c := 1
	for i := range len(str) {
		if str[i] == ch {
			c++
		}
	}

	r := make([]string, 0, c)
	s := str
	for range c {
		idx := strings.IndexByte(s, ch)
		if idx < 0 {
			r = append(r, s)
		} else {
			r = append(r, s[:idx])
			s = s[idx+1:]
		}
	}
	return r
}

func StrvecFree(s []string) {
}

func StrvecAddpn(strvp *[]string, v []byte, len_ int) {
	strv := *strvp
	nv := make([]byte, len_)
	copy(nv, v[:len_])
	strv = append(strv, string(nv))
	*strvp = strv
}

func StrvecAddp(strvp *[]string, v string) {
	StrvecAddpn(strvp, []byte(v), len(v))
}

func StrvecLen(s []string) int {
	return len(s)
}

func Strappend(strp *string, src string) {
	if *strp == "" {
		*strp = src
	} else {
		*strp = *strp + src
	}
}

func Hex2binl(buf []byte, buflen int, str string, maxlen int) int {
	bl := buflen
	i := 0
	o := 0
	for i < len(str) && str[i] != 0 {
		if maxlen < 2 {
			break
		}
		if buflen == 0 {
			return -1
		}
		hi := Hexnibble(str[i])
		i++
		if hi == -1 {
			return -1
		}
		lo := Hexnibble(str[i])
		i++
		if lo == -1 {
			return -1
		}
		maxlen -= 2
		buf[o] = byte(hi<<4 | lo)
		o++
		buflen--
	}
	return bl - buflen
}

// C: #define hex2bin(a,b,c) hex2binl(a,b,c,INT32_MAX)
func Hex2bin(buf []byte, buflen int, str string) int {
	return Hex2binl(buf, buflen, str, 1<<31-1)
}

func Bin2hex(dst []byte, dstlen int, src []byte, srclen int) {
	o := 0
	i := 0
	const hexl = "0123456789abcdef"
	for dstlen > 2 && srclen > 0 {
		dst[o] = hexl[src[i]>>4]
		o++
		dst[o] = hexl[src[i]&0xf]
		o++
		i++
		srclen--
		dstlen -= 2
	}
	dst[o] = 0
}

func Fmtstr(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// Atoi — C: atoi(s) = strtol(s, NULL, 10): skips isspace() whitespace
// (" \t\n\v\f\r"), optional +/- sign, then digits. Garbage-tolerant,
// returns 0 when no digits follow.
func Atoi(s string) int {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' ||
		s[i] == '\v' || s[i] == '\f' || s[i] == '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	n := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
	}
	if neg {
		n = -n
	}
	return n
}

// Atoi64 — C: atol(s) for int64 targets (same rules as Atoi).
func Atoi64(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' ||
		s[i] == '\v' || s[i] == '\f' || s[i] == '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	var n int64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int64(s[i]-'0')
		i++
	}
	if neg {
		n = -n
	}
	return n
}

// CStr reads a NUL-terminated byte buffer as a Go string.
func CStr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func Hexnibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}

func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func GetRandomString() *Rstr {
	var d [20]byte
	var buf [40]byte
	rand.Read(d[:]) // C: arch_get_random_bytes
	const hexl = "0123456789abcdef"
	for i := range 20 {
		buf[i*2+0] = hexl[d[i]&0xf]
		buf[i*2+1] = hexl[d[i]>>4]
	}
	s := string(buf[:])
	return RstrAllocl(&s, 40)
}

func LpGet(lp *[]byte) []byte {
	for {
		if *lp == nil {
			return nil
		}
		r := *lp
		// C: int l = strcspn(r, "\r\n")
		l := 0
		for l < len(r) && r[l] != 0 && r[l] != '\r' && r[l] != '\n' {
			l++
		}
		if l >= len(r) || r[l] == 0 {
			*lp = nil
		} else {
			r[l] = 0
			s := l + 1
			for s < len(r) && (r[s] == '\r' || r[s] == '\n') {
				s++
			}
			*lp = r[s:]
		}
		if l > 0 && r[0] != 0 {
			return r
		}
		if len(r) == 0 || r[0] == 0 {
			continue
		}
		return r
	}
}

func FindStr(s []byte, len_ int, needle string) int {
	nlen := len(needle)
	if len_ < nlen {
		return -1
	}

	len_ -= nlen
	for i := range len_ + 1 {
		j := 0
		for j = 0; j < nlen; j++ {
			if s[i+j] != needle[j] {
				break
			}
		}
		if j == nlen {
			return i
		}
	}
	return -1
}

func Mystrlower(s []byte) {
	for i := 0; i < len(s) && s[i] != 0; i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			s[i] = s[i] + 32
		}
	}
}

func DeescapeCstyle(src []byte) {
	dst := 0
	i := 0
	for i < len(src) && src[i] != 0 {
		if src[i] == '\\' {
			i++
			if i >= len(src) || src[i] == 0 {
				break
			}
			if src[i] == 'n' {
				src[dst] = '\n'
				dst++
			}
			if src[i] == '\\' {
				src[dst] = '\\'
				dst++
			}
			i++
		} else {
			src[dst] = src[i]
			dst++
			i++
		}
	}
	src[dst] = 0
}

func RgbstrToFloatvec(s string, out *[3]float32) {
	var nibbles [6]int
	n := 0

	for n < len(s) && s[n] != 0 && n < 6 {
		nibbles[n] = Hexnibble(s[n])
		n++
	}

	if n >= 6 {
		out[0] = float32(nibbles[0]<<4|nibbles[1]) / 255.0
		out[1] = float32(nibbles[2]<<4|nibbles[3]) / 255.0
		out[2] = float32(nibbles[4]<<4|nibbles[5]) / 255.0
	} else if n >= 3 {
		out[0] = float32(nibbles[0]) / 15.0
		out[1] = float32(nibbles[1]) / 15.0
		out[2] = float32(nibbles[2]) / 15.0
	} else {
		out[0] = 0
		out[1] = 0
		out[2] = 0
	}
}

// C: static char mkup(char x)
func mkup(x byte) byte {
	if x >= 'a' && x <= 'z' {
		x -= 32
	}
	return x
}

func PatternMatch(str, pat string) int {
	si, pi := 0, 0
	for {
		// C reads *pat until '*'; the NUL terminator (modeled as
		// end-of-string) enters the loop body, it does not exit it.
		var p byte
		if pi < len(pat) {
			p = pat[pi]
		}
		if p == '*' {
			break
		}
		if si >= len(str) || str[si] == 0 {
			if p != 0 {
				return 0
			}
			return 1
		}
		if mkup(str[si]) != mkup(p) && p != '?' {
			return 0
		}
		si++
		pi++
	}
	for pi+1 < len(pat) && pat[pi+1] == '*' {
		pi++
	}

	for {
		if patternMatchAt(str[si:], pat[pi+1:]) != 0 {
			return 1
		}
		if si >= len(str) || str[si] == 0 {
			break
		}
		si++
	}
	return 0
}

// patternMatchAt is pattern_match on substrings (C recursion on pointers).
func patternMatchAt(str, pat string) int {
	return PatternMatch(str, pat)
}

// Mystrhash — C: mystrhash (main.h:135, static inline)
func Mystrhash(s string) uint32 {
	var v uint32 = 5381
	for i := range len(s) {
		v += (v << 5) + v + uint32(s[i])
	}
	return v
}
