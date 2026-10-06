// pssh.go — ISO BMFF 'pssh' (Protection System Specific Header) box
// parser. Extracts key-system IDs, KIDs and init data from mp4 boxes.
// New in Go — no C counterpart (upstream Movian has no DRM support).
package cenc

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Well-known DRM system IDs (ISO 23001-7 / registry).
var (
	SystemIDWidevine  = [16]byte{0xed, 0xef, 0x8b, 0xa9, 0x79, 0xd6, 0x4a, 0xce, 0xa3, 0xc8, 0x27, 0xdc, 0xd5, 0x1d, 0x21, 0xed}
	SystemIDPlayReady = [16]byte{0x9a, 0x04, 0xf0, 0x79, 0x98, 0x40, 0x42, 0x86, 0xab, 0x92, 0xe6, 0x5b, 0xe0, 0x88, 0x5f, 0x95}
	SystemIDFairPlay  = [16]byte{0x94, 0xce, 0x86, 0xfb, 0x07, 0xff, 0x4f, 0x43, 0xad, 0xb8, 0x93, 0xd2, 0xfa, 0x96, 0x8c, 0xa2}
	SystemIDClearKey  = [16]byte{0xe2, 0x71, 0x9d, 0x58, 0xa9, 0x85, 0xb3, 0xc9, 0x78, 0x1a, 0xb0, 0x30, 0xaf, 0x78, 0xd3, 0x0e}
)

// PSSHBox is a parsed 'pssh' full box.
type PSSHBox struct {
	SystemID [16]byte
	KIDs     [][16]byte // v1 only; nil for v0
	Data     []byte     // key-system specific init data
	Raw      []byte     // whole box, sent verbatim to the CDM
}

// ParsePSSH parses a single 'pssh' box (with or without the box header).
func ParsePSSH(raw []byte) (*PSSHBox, error) {
	body := raw
	// Strip the box header if present.
	if len(raw) >= 8 && string(raw[4:8]) == "pssh" {
		sz := binary.BigEndian.Uint32(raw)
		if int(sz) > len(raw) {
			return nil, fmt.Errorf("pssh: truncated box (%d > %d)", sz, len(raw))
		}
		body = raw[8:sz]
	}
	if len(body) < 24 {
		return nil, fmt.Errorf("pssh: box too small (%d)", len(body))
	}
	version := body[0]
	b := &PSSHBox{Raw: raw}
	copy(b.SystemID[:], body[4:20])
	pos := 20
	if version == 1 {
		if len(body) < pos+4 {
			return nil, fmt.Errorf("pssh: v1 missing KID count")
		}
		n := int(binary.BigEndian.Uint32(body[pos:]))
		pos += 4
		if len(body) < pos+16*n {
			return nil, fmt.Errorf("pssh: v1 KID list truncated")
		}
		for i := range n {
			var k [16]byte
			copy(k[:], body[pos+16*i:])
			b.KIDs = append(b.KIDs, k)
		}
		pos += 16 * n
	}
	if len(body) < pos+4 {
		return nil, fmt.Errorf("pssh: missing data size")
	}
	dsz := int(binary.BigEndian.Uint32(body[pos:]))
	pos += 4
	if len(body) < pos+dsz {
		return nil, fmt.Errorf("pssh: data truncated (%d > %d)", pos+dsz, len(body))
	}
	b.Data = body[pos : pos+dsz]
	return b, nil
}

// FindPSSHBoxes scans a byte stream (mp4 file head or init segment)
// for top-level and moov-level 'pssh' boxes. Stops at 'mdat'.
func FindPSSHBoxes(data []byte) []*PSSHBox {
	var out []*PSSHBox
	walk := data
	for len(walk) >= 8 {
		sz := int(binary.BigEndian.Uint32(walk))
		if sz == 1 && len(walk) >= 16 {
			sz = int(binary.BigEndian.Uint64(walk[8:]))
		} else if sz == 0 {
			sz = len(walk)
		}
		if sz < 8 || sz > len(walk) {
			break
		}
		typ := string(walk[4:8])
		switch typ {
		case "pssh":
			if b, err := ParsePSSH(walk[:sz]); err == nil {
				out = append(out, b)
			}
		case "moov", "moof", "traf":
			off := 8
			if typ == "moof" || typ == "traf" {
				// moof children start after header; traf same.
			}
			out = append(out, FindPSSHBoxes(walk[off:sz])...)
		case "mdat":
			return out
		}
		walk = walk[sz:]
	}
	return out
}

// KIDFromWidevineData extracts the KID from a Widevine PSSH Data field.
// The Data field is a protobuf (WidevinePsshData); field 2 (key_id) is a
// repeated bytes field with tag 0x12. Minimal wire parse, no protobuf dep.
func KIDFromWidevineData(data []byte) ([][16]byte, error) {
	var kids [][16]byte
	for len(data) >= 2 {
		tag := data[0]
		if tag>>3 == 0 {
			return nil, fmt.Errorf("pssh: bad protobuf wire")
		}
		field := tag >> 3
		wire := tag & 7
		if wire != 2 {
			// varint/skip unsupported non-LEN fields
			return nil, fmt.Errorf("pssh: unexpected wire type %d", wire)
		}
		l, n := binary.Uvarint(data[1:])
		if n <= 0 {
			return nil, fmt.Errorf("pssh: bad varint")
		}
		v := data[1+n:]
		if uint64(len(v)) < l {
			return nil, fmt.Errorf("pssh: field truncated")
		}
		if field == 2 && l == 16 {
			var k [16]byte
			copy(k[:], v[:16])
			kids = append(kids, k)
		}
		data = v[l:]
	}
	return kids, nil
}

// Equal reports whether two system IDs match.
func SystemIDEqual(a, b [16]byte) bool { return bytes.Equal(a[:], b[:]) }
