package htsmsg

// Canonical port of src/htsmsg/htsmsg_json.c — JSON serialization
// (htsbuf-based writer) and deserialization via misc/json.c's
// callback parser (pkg/misc/json.go).

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/misc"
)

// jsonIndentor — C: indentor (htsmsg_json.c:42). Up to 16 chars are
// appended: '\n' followed by tabs.
const jsonIndentor = "\n\t\t\t\t\t\t\t\t\t\t\t\t\t\t\t\t"

// jsonWrite — C: htsmsg_json_write (htsmsg_json.c:37-94).
func jsonWrite(sb *strings.Builder, msg *HTSMsg, isArray bool, indent int, pretty bool) {
	if isArray {
		sb.WriteByte('[')
	} else {
		sb.WriteByte('{')
	}

	for i, f := range msg.fields {
		if pretty {
			n := min(indent, 16)
			sb.WriteString(jsonIndentor[:n])
		}

		if !isArray {
			name := f.name
			if name == "" {
				name = "noname" // C: f->hmf_name ? f->hmf_name : "noname"
			}
			sb.WriteString(escapeJSONString(name))
			sb.WriteString(": ")
		}

		switch f.fieldType {
		case HmfMap:
			if f.childs != nil {
				jsonWrite(sb, f.childs, false, indent+1, pretty)
			} else {
				sb.WriteString("{}") // C: NULL childs would crash; safe empty
			}
		case HmfList:
			if f.childs != nil {
				jsonWrite(sb, f.childs, true, indent+1, pretty)
			} else {
				sb.WriteString("[]")
			}
		case HmfStr:
			sb.WriteString(escapeJSONString(f.strValue))
		case HmfBin:
			sb.WriteString(escapeJSONString("binary"))
		case HmfS64:
			sb.WriteString(strconv.FormatInt(f.s64Value, 10))
		case HmfDbl:
			sb.WriteString(misc.MyDouble2str(100, f.dblValue))
		}

		if i < len(msg.fields)-1 {
			sb.WriteByte(',')
		}
	}

	if pretty {
		n := min(indent-1, 16)
		if n > 0 {
			sb.WriteString(jsonIndentor[:n])
		}
	}
	if isArray {
		sb.WriteByte(']')
	} else {
		sb.WriteByte('}')
	}
}

// SerializeJSON — C: htsmsg_json_serialize + htsmsg_json_serialize_to_str
// (htsmsg_json.c:100-120). pretty mode appends a trailing newline.
func SerializeJSON(msg *HTSMsg, pretty bool) (string, error) {
	if msg == nil {
		return "", fmt.Errorf("nil message")
	}

	var sb strings.Builder
	jsonWrite(&sb, msg, msg.isList, 2, pretty)
	if pretty {
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// SerializeJSONToRstr — C: htsmsg_json_serialize_to_rstr
// (htsmsg_json.c:128-138). Returns "" (C's NULL) for nil/empty msg.
func SerializeJSONToRstr(msg *HTSMsg, prefix string) (string, error) {
	if msg == nil || len(msg.fields) == 0 {
		return "", nil
	}

	jsonStr, err := SerializeJSON(msg, false)
	if err != nil {
		return "", err
	}
	return prefix + jsonStr, nil
}

// escapeJSONString — C: htsbuf_append_and_escape_jsonstr
// (htsbuf.c:406-447). Only " \ \n \r \t are escaped; every other
// byte is emitted verbatim.
func escapeJSONString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for i := range len(s) {
		c := s[i]
		switch c {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// AppendAndEscapeJSONString appends an escaped JSON string to a builder
func AppendAndEscapeJSONString(builder *strings.Builder, s string) {
	builder.WriteString(escapeJSONString(s))
}

// jsonToHTSMsg — C: json_to_htsmsg (htsmsg_json.c:202-212).
var jsonToHTSMsg = &misc.JSONDeserializer{
	CreateMap: func(opaque any) any {
		return NewMap()
	},
	CreateList: func(opaque any) any {
		return NewList()
	},
	DestroyObj: func(opaque, obj any) {
		obj.(*HTSMsg).Release()
	},
	AddObj: func(opaque, parent any, name string, child any) {
		parent.(*HTSMsg).AddMsg(name, child.(*HTSMsg))
	},
	AddString: func(opaque, parent any, name string, str string) {
		parent.(*HTSMsg).AddStr(name, str)
	},
	AddLong: func(opaque, parent any, name string, v int64) {
		parent.(*HTSMsg).AddS64(name, v)
	},
	AddDouble: func(opaque, parent any, name string, v float64) {
		parent.(*HTSMsg).AddDbl(name, v)
	},
	AddBool: func(opaque, parent any, name string, v int) {
		parent.(*HTSMsg).AddU32(name, uint32(v))
	},
	AddNull: func(opaque, parent any, name string) {
		// C: add_null — no-op, the field is skipped entirely.
	},
}

// DeserializeJSON — C: htsmsg_json_deserialize / htsmsg_json_deserialize2
// (htsmsg_json.c:218-231). Uses the canonical misc/json.c parser
// (lenient escapes, strtol integers, bytes<33 whitespace, no
// surrogate combining) rather than encoding/json.
func DeserializeJSON(src string) (*HTSMsg, error) {
	v, errmsg := misc.JSONDeserialize(src, jsonToHTSMsg, nil)
	if v == nil {
		if errmsg == "" {
			errmsg = "JSON parse error"
		}
		return nil, errors.New(errmsg)
	}
	return v.(*HTSMsg), nil
}
