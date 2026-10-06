package bittorrent

// C: src/backend/bittorrent/bencode.c — canonical 1:1 port.
// Bencode parser + serializer over htsmsg. Pointer arithmetic on the
// input is expressed as indices into a []byte.

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"

	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
)

// BencodeParseCb — C: bencode_pase_cb_t (bencode.h:22-23)
type BencodeParseCb func(opaque any, name string, data []byte)

// C: static void *bencode_parse_binary (bencode.c:40-75)
// Returns (value, endpos, failpos, failmsg, notType) — notType is the
// C NOT_THIS_TYPE sentinel (non-match), nil+failmsg on hard failure.
func bencodeParseBinary(src []byte, start, stop int) ([]byte, int,
	int, string, bool) {
	length := 0
	for {
		if start == stop {
			return nil, 0, start, "Unexpected end of BENCODE message",
				false
		}
		c := src[start]
		if c == ':' {
			break
		}
		if c < '0' || c > '9' {
			return nil, 0, 0, "", true // NOT_THIS_TYPE
		}
		length = length*10 + int(c-'0')
		start++
	}

	start++

	if start+length >= stop {
		return nil, 0, start, "Excessive binary length", false
	}

	// C: malloc(len+1), x[len]=0 — Go slice is exact length
	x := make([]byte, length)
	copy(x, src[start:start+length])
	return x, start + length, 0, "", false
}

// C: static char *bencode_parse_string (bencode.c:81-99)
func bencodeParseString(src []byte, start, stop int) (string, int,
	int, string, bool) {
	x, endp, failp, failmsg, notType :=
		bencodeParseBinary(src, start, stop)
	if x == nil && !notType {
		return "", 0, failp, failmsg, false
	}
	if notType {
		return "", 0, 0, "", true // NOT_THIS_TYPE
	}
	if idx := bytes.IndexByte(x, 0); idx >= 0 {
		return "", 0, start + idx, "Unexpected NUL byte", false
	}
	return string(x), endp, 0, "", false
}

// C: static void *bencode_parse_map (bencode.c:105-166)
func bencodeParseMap(src []byte, s, stop int, cb BencodeParseCb,
	opaque any) (*htsmsg.HTSMsg, int, int, string, bool) {
	if src[s] != 'd' {
		return nil, 0, 0, "", true // NOT_THIS_TYPE
	}
	s++

	r := htsmsg.NewMap()

	if s != stop && src[s] != 'e' {
		for {
			name, s2, failp, failmsg, notType :=
				bencodeParseString(src, s, stop)
			if notType {
				r.Release()
				return nil, 0, s, "Expected string", false
			}
			if failmsg != "" {
				r.Release()
				return nil, 0, failp, failmsg, false
			}

			s = s2

			s2, failp, failmsg = bencodeParseValue(src, s, stop, r,
				name)
			if failmsg != "" || s2 == -1 {
				r.Release()
				return nil, 0, failp, failmsg, false
			}
			if s2 == 0 {
				r.Release()
				return nil, 0, failp, failmsg, false
			}

			if cb != nil {
				cb(opaque, name, src[s:s2])
			}

			s = s2

			if src[s] == 'e' {
				break
			}
		}
	}

	s++
	return r, s, 0, "", false
}

// C: static void *bencode_parse_list (bencode.c:172-201)
func bencodeParseList(src []byte, s, stop int) (*htsmsg.HTSMsg, int,
	int, string, bool) {
	if src[s] != 'l' {
		return nil, 0, 0, "", true // NOT_THIS_TYPE
	}
	s++

	r := htsmsg.NewList()

	if s != stop && src[s] != 'e' {
		for {
			s2, failp, failmsg := bencodeParseValue(src, s, stop, r, "")
			if s2 <= 0 {
				r.Release()
				return nil, 0, failp, failmsg, false
			}

			s = s2

			if src[s] == 'e' {
				break
			}
		}
	}
	s++
	return r, s, 0, "", false
}

// C: static const char *bencode_parse_integer (bencode.c:207-234)
// Returns end position or -1 on non-match.
func bencodeParseInteger(src []byte, s, stop int, lp *int64) int {
	if src[s] != 'i' {
		return -1
	}

	neg := int64(1)
	var val int64

	s++
	if s != stop && src[s] == '-' {
		neg = -1
		s++
	}

	for {
		if s == stop {
			break
		}
		c := src[s]
		if c == 'e' {
			s++
			break
		}
		val = val*10 + int64(c-'0')
		s++
	}

	*lp = val * neg
	return s
}

// C: static const char *bencode_parse_value (bencode.c:240-298)
// Returns (endpos, failpos, failmsg); endpos==0 means hard fail,
// endpos==-1 reserved.
func bencodeParseValue(src []byte, s, stop int,
	parent *htsmsg.HTSMsg, name string) (int, int, string) {
	var l int64

	if s == stop {
		return 0, 0, ""
	}

	c, endp, failp, failmsg, notType :=
		bencodeParseMap(src, s, stop, nil, nil)
	if failmsg != "" {
		return 0, failp, failmsg
	}
	if !notType {
		parent.AddMsg(name, c)
		return endp, 0, ""
	}

	c, endp, failp, failmsg, notType = bencodeParseList(src, s, stop)
	if failmsg != "" {
		return 0, failp, failmsg
	}
	if !notType {
		parent.AddMsg(name, c)
		return endp, 0, ""
	}

	if s2 := bencodeParseInteger(src, s, stop, &l); s2 != -1 {
		parent.AddS64(name, l)
		return s2, 0, ""
	}

	str, endp2, failp2, failmsg2, binNotType :=
		bencodeParseBinary(src, s, stop)
	if failmsg2 != "" {
		return 0, failp2, failmsg2
	}

	if !binNotType {
		cleanstr := true
		for i := range str {
			if str[i] < 0x20 {
				cleanstr = false
				break
			}
		}
		if cleanstr {
			parent.AddStr(name, string(str))
		} else {
			parent.AddBin(name, str)
		}
		return endp2, 0, ""
	}

	return 0, s, "Unknown token"
}

// BencodeDeserialize — C: bencode_deserialize (bencode.c:304-349).
// Returns (msg, consumedBytes, err).
func BencodeDeserialize(src []byte,
	cb BencodeParseCb, opaque any) (*htsmsg.HTSMsg, int, error) {

	if len(src) == 0 {
		return nil, 0, errors.New("zero size bencode")
	}

	c, end, errp, errmsg, notType := bencodeParseMap(src, 0, len(src),
		cb, opaque)
	if notType {
		c, end, errp, errmsg, notType = bencodeParseList(src, 0, len(src))
	}

	if notType {
		return nil, 0, errors.New("Invalid BENCODE, expected 'd' or 'l'")
	}

	if c == nil {
		offset := errp
		if offset > len(src) || offset < 0 {
			return nil, 0, fmt.Errorf("%s at (bad) offset %d",
				errmsg, offset)
		}
		offset -= 10
		if offset < 0 {
			offset = 0
		}
		n := min(offset+20, len(src))
		return nil, 0, fmt.Errorf("%s at offset %d : '%s'",
			errmsg, offset, src[offset:n])
	}
	return c, end, nil
}

// ---------------------------------------------------------------------------
// Serializer

// C: static int serialize_bytes (bencode.c:355-367)
func serializeBytes(data []byte, ptr []byte) int {
	h := strconv.Itoa(len(data)) + ":"
	l := len(h)
	if ptr != nil {
		copy(ptr, h)
		copy(ptr[l:], data)
	}
	return l + len(data)
}

// C: static int bencode_serialize_r (bencode.c:373-436)
func bencodeSerializeR(msg *htsmsg.HTSMsg, ptr []byte, ppos int) int {
	var buf [32]byte
	length := 0
	isarray := msg.IsList()

	if ptr != nil {
		if isarray {
			ptr[ppos] = 'l'
		} else {
			ptr[ppos] = 'd'
		}
		ppos++
	}
	length++

	for _, f := range msg.GetFields() {
		var sublen int

		if !isarray {
			sublen = serializeBytes([]byte(f.GetName()), sliceAt(ptr, ppos))
			if ptr != nil {
				ppos += sublen
			}
			length += sublen
		}

		switch f.GetType() {
		case htsmsg.HmfMap, htsmsg.HmfList:
			sublen = bencodeSerializeR(f.GetChilds(), ptr, ppos)
		case htsmsg.HmfStr:
			sublen = serializeBytes([]byte(f.GetStrValue()),
				sliceAt(ptr, ppos))
		case htsmsg.HmfBin:
			sublen = serializeBytes(f.GetBinData(), sliceAt(ptr, ppos))
		case htsmsg.HmfDbl:
			// C: if(0) fallthrough — DBL serializes as int64
			n := copy(buf[:], fmt.Sprintf("i%de",
				int64(f.GetDblValue())))
			sublen = n
			if ptr != nil {
				copy(ptr[ppos:], buf[:n])
			}
		case htsmsg.HmfS64:
			n := copy(buf[:], fmt.Sprintf("i%de", f.GetS64Value()))
			sublen = n
			if ptr != nil {
				copy(ptr[ppos:], buf[:n])
			}
		default:
			panic("bencode_serialize_r: bad field type") // C: abort()
		}

		if ptr != nil {
			ppos += sublen
		}
		length += sublen
	}

	if ptr != nil {
		ptr[ppos] = 'e'
		ppos++
	}
	length++
	return length
}

func sliceAt(b []byte, pos int) []byte {
	if b == nil {
		return nil
	}
	return b[pos:]
}

// BencodeSerialize — C: bencode_serialize (bencode.c:443-451)
func BencodeSerialize(src *htsmsg.HTSMsg) *misc.Buf {
	// get size first
	length := bencodeSerializeR(src, nil, 0)
	b := misc.BufCreate(length)
	bencodeSerializeR(src, b.C8(), 0)
	return b
}
