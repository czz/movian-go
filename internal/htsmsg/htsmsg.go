package htsmsg

// HTSMSG - Message serialization system

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync/atomic"
)

// Error codes
const (
	HtsmsgErrFieldNotFound        = -1
	HtsmsgErrConversionImpossible = -2
)

// Field types
const (
	HmfMap  = 1
	HmfS64  = 2
	HmfStr  = 3
	HmfBin  = 4
	HmfList = 5
	HmfDbl  = 6
)

// Field flags
const (
	HmfAlloced      = 0x1
	HmfNameAlloced  = 0x2
	HmfXmlAttribute = 0x4
)

// HTSMsg represents a message (map or list)
type HTSMsg struct {
	fields       []*HTSMsgField
	backingStore any // buf_t equivalent
	isList       bool
	refcount     int32
}

// HTSMsgField represents a field in a message
type HTSMsgField struct {
	name      string
	fieldType uint8
	flags     uint8
	namespace string // rstr equivalent

	// Union data
	s64Value int64
	strValue string
	binData  []byte
	dblValue float64

	childs *HTSMsg
}

// IsList — C: msg->hm_islist
func (m *HTSMsg) IsList() bool { return m.isList }

// GetMap returns the child map if this field is a map type
func (f *HTSMsgField) GetMap() *HTSMsg {
	if f == nil || f.fieldType != HmfMap {
		return nil
	}
	return f.childs
}

// NewMap creates a new map message
func NewMap() *HTSMsg {
	return &HTSMsg{
		fields:   make([]*HTSMsgField, 0),
		isList:   false,
		refcount: 1,
	}
}

// NewList creates a new list message
func NewList() *HTSMsg {
	return &HTSMsg{
		fields:   make([]*HTSMsgField, 0),
		isList:   true,
		refcount: 1,
	}
}

// Retain increases the reference count
func (m *HTSMsg) Retain() *HTSMsg {
	atomic.AddInt32(&m.refcount, 1)
	return m
}

// Release decreases the reference count and frees if zero
func (m *HTSMsg) Release() {
	if m == nil {
		return
	}

	newCount := atomic.AddInt32(&m.refcount, -1)
	if newCount > 0 {
		return
	}

	// Destroy all fields
	for _, f := range m.fields {
		m.destroyField(f)
	}

	m.fields = nil
}

// destroyField destroys a field
func (m *HTSMsg) destroyField(f *HTSMsgField) {
	if f.childs != nil {
		f.childs.Release()
	}

	// Clear references
	f.childs = nil
	f.binData = nil
	f.strValue = ""
}

// fieldAdd adds a new field to the message
func (m *HTSMsg) fieldAdd(name string, fieldType uint8, flags uint8) *HTSMsgField {
	if m.isList {
		name = "" // Lists don't have named fields
	}

	f := &HTSMsgField{
		name:      name,
		fieldType: fieldType,
		flags:     flags,
	}

	m.fields = append(m.fields, f)
	return f
}

// fieldFind finds a field by name or index
// FieldFind — C: htsmsg_field_find (htsmsg.c:89). For lists, name is an
// index; for maps, a name lookup.
func (m *HTSMsg) FieldFind(name string) *HTSMsgField {
	return m.fieldFind(name)
}

func (m *HTSMsg) fieldFind(name string) *HTSMsgField {
	if m.isList {
		// For lists, name is actually an index
		if idx, err := strconv.Atoi(name); err == nil {
			if idx >= 0 && idx < len(m.fields) {
				return m.fields[idx]
			}
		}
		return nil
	}

	// For maps, find by name
	for _, f := range m.fields {
		if f.name == name {
			return f
		}
	}
	return nil
}

// DeleteField removes a field from the message
func (m *HTSMsg) DeleteField(name string) int {
	f := m.fieldFind(name)
	if f == nil {
		return HtsmsgErrFieldNotFound
	}

	// Find and remove field
	for i, field := range m.fields {
		if field == f {
			m.fields = slices.Delete(m.fields, i, i+1)
			m.destroyField(f)
			return 0
		}
	}

	return HtsmsgErrFieldNotFound
}

// AddU32 adds an unsigned 32-bit integer field
func (m *HTSMsg) AddU32(name string, u32 uint32) {
	f := m.fieldAdd(name, HmfS64, HmfNameAlloced)
	f.s64Value = int64(u32)
}

// AddS32 adds a signed 32-bit integer field
func (m *HTSMsg) AddS32(name string, s32 int32) {
	f := m.fieldAdd(name, HmfS64, HmfNameAlloced)
	f.s64Value = int64(s32)
}

// AddS64 adds a signed 64-bit integer field
func (m *HTSMsg) AddS64(name string, s64 int64) {
	f := m.fieldAdd(name, HmfS64, HmfNameAlloced)
	f.s64Value = s64
}

// S32Inc increments a signed 32-bit integer field
func (m *HTSMsg) S32Inc(name string, s32 int32) {
	f := m.fieldFind(name)
	if f != nil && f.fieldType != HmfS64 {
		m.DeleteField(name)
		f = nil
	}

	if f == nil {
		m.AddS32(name, s32)
	} else {
		f.s64Value += int64(s32)
	}
}

// AddDbl adds a double field
func (m *HTSMsg) AddDbl(name string, dbl float64) {
	f := m.fieldAdd(name, HmfDbl, HmfNameAlloced)
	f.dblValue = dbl
}

// AddStr adds a string field
func (m *HTSMsg) AddStr(name string, str string) {
	f := m.fieldAdd(name, HmfStr, HmfAlloced|HmfNameAlloced)
	f.strValue = str
}

// AddBin adds a binary field (data is copied)
func (m *HTSMsg) AddBin(name string, bin []byte) {
	f := m.fieldAdd(name, HmfBin, HmfAlloced|HmfNameAlloced)
	f.binData = make([]byte, len(bin))
	copy(f.binData, bin)
}

// AddBinPtr adds a binary field (data is not copied, caller must keep it valid)
func (m *HTSMsg) AddBinPtr(name string, bin []byte) {
	f := m.fieldAdd(name, HmfBin, HmfNameAlloced)
	f.binData = bin
}

// AddMsg adds a sub-message (map or list)
func (m *HTSMsg) AddMsg(name string, sub *HTSMsg) {
	fieldType := uint8(HmfMap)
	if sub.isList {
		fieldType = uint8(HmfList)
	}

	f := m.fieldAdd(name, fieldType, HmfNameAlloced)
	f.childs = sub
}

// AddMsgExtName adds a sub-message without copying the name
func (m *HTSMsg) AddMsgExtName(name string, sub *HTSMsg) {
	fieldType := uint8(HmfMap)
	if sub.isList {
		fieldType = uint8(HmfList)
	}

	f := m.fieldAdd(name, fieldType, 0)
	f.childs = sub
}

// strtoll — C: strtoll(s, NULL, 0) (htsmsg.c:326). Skips leading
// whitespace, accepts +/-, base 0 detects 0x hex / 0 octal / decimal,
// stops at the first invalid digit (partial parse → accumulated value,
// no digits → 0), clamps on overflow.
func strtoll(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' ||
		s[i] == '\v' || s[i] == '\f' || s[i] == '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	base := int64(10)
	start := i
	if i < len(s) && s[i] == '0' {
		if i+1 < len(s) && (s[i+1] == 'x' || s[i+1] == 'X') &&
			isDigitForBase(s[i+2:], 16) {
			base = 16
			i += 2
		} else {
			base = 8
		}
	}
	var v int64
	digits := 0
	for ; i < len(s); i++ {
		var d int64
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case c >= 'a' && c <= 'z':
			d = int64(c-'a') + 10
		case c >= 'A' && c <= 'Z':
			d = int64(c-'A') + 10
		default:
			d = base
		}
		if d >= base {
			break
		}
		digits++
		v = v*base + d
		if v < 0 { // overflow
			if neg {
				return math.MinInt64
			}
			return math.MaxInt64
		}
	}
	if digits == 0 && i == start {
		return 0
	}
	if neg {
		return -v
	}
	return v
}

func isDigitForBase(s string, base int64) bool {
	if len(s) == 0 {
		return false
	}
	c := s[0]
	var d int64
	switch {
	case c >= '0' && c <= '9':
		d = int64(c - '0')
	case c >= 'a' && c <= 'z':
		d = int64(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		d = int64(c-'A') + 10
	default:
		return false
	}
	return d < base
}

// GetS64 — C: htsmsg_get_s64 (htsmsg.c:320-342). HMF_STR converts via
// strtoll(str, NULL, 0) — partial parses and whitespace are accepted.
func (m *HTSMsg) GetS64(name string) (int64, error) {
	f := m.fieldFind(name)
	if f == nil {
		return 0, fmt.Errorf("field not found")
	}

	switch f.fieldType {
	case HmfStr:
		return strtoll(f.strValue), nil
	case HmfS64:
		return f.s64Value, nil
	default:
		return 0, fmt.Errorf("conversion impossible")
	}
}

// GetU32 gets an unsigned 32-bit integer field
func (m *HTSMsg) GetU32(name string) (uint32, error) {
	s64, err := m.GetS64(name)
	if err != nil {
		return 0, err
	}

	if s64 < 0 || s64 > 0xffffffff {
		return 0, fmt.Errorf("conversion impossible")
	}

	return uint32(s64), nil
}

// GetU32OrDefault gets an unsigned 32-bit integer field or returns default
func (m *HTSMsg) GetU32OrDefault(name string, def uint32) uint32 {
	val, err := m.GetU32(name)
	if err != nil {
		return def
	}
	return val
}

// GetS32 gets a signed 32-bit integer field
func (m *HTSMsg) GetS32(name string) (int32, error) {
	s64, err := m.GetS64(name)
	if err != nil {
		return 0, err
	}

	if s64 < -0x80000000 || s64 > 0x7fffffff {
		return 0, fmt.Errorf("conversion impossible")
	}

	return int32(s64), nil
}

// GetS32OrDefault gets a signed 32-bit integer field or returns default
func (m *HTSMsg) GetS32OrDefault(name string, def int32) int32 {
	val, err := m.GetS32(name)
	if err != nil {
		return def
	}
	return val
}

// GetDbl gets a double field
func (m *HTSMsg) GetDbl(name string) (float64, error) {
	f := m.fieldFind(name)
	if f == nil {
		return 0, fmt.Errorf("field not found")
	}

	switch f.fieldType {
	case HmfDbl:
		return f.dblValue, nil
	case HmfS64:
		return float64(f.s64Value), nil
	default:
		return 0, fmt.Errorf("conversion impossible")
	}
}

// GetDblOrDefault gets a double field or returns default
func (m *HTSMsg) GetDblOrDefault(name string, def float64) float64 {
	val, err := m.GetDbl(name)
	if err != nil {
		return def
	}
	return val
}

// FieldGetString — C: htsmsg_field_get_string (htsmsg.c:455-472).
// HMF_S64 fields are converted to HMF_STR in place (the field type
// changes, as in C). Returns ("", false) for other types (C's NULL).
func (f *HTSMsgField) FieldGetString() (string, bool) {
	switch f.fieldType {
	case HmfStr:
		return f.strValue, true
	case HmfS64:
		f.strValue = strconv.FormatInt(f.s64Value, 10)
		f.fieldType = HmfStr
		return f.strValue, true
	default:
		return "", false
	}
}

// GetStr — C: htsmsg_get_str (htsmsg.c:477-484) — NULL when the field
// is missing or not string-convertible; "" in Go.
func (m *HTSMsg) GetStr(name string) string {
	f := m.fieldFind(name)
	if f == nil {
		return ""
	}
	s, _ := f.FieldGetString()
	return s
}

// GetBin gets a binary field
func (m *HTSMsg) GetBin(name string) ([]byte, error) {
	f := m.fieldFind(name)
	if f == nil {
		return nil, fmt.Errorf("field not found")
	}

	switch f.fieldType {
	case HmfStr:
		return []byte(f.strValue), nil
	case HmfBin:
		return f.binData, nil
	default:
		return nil, fmt.Errorf("conversion impossible")
	}
}

// GetMap gets a map field
func (m *HTSMsg) GetMap(name string) *HTSMsg {
	f := m.fieldFind(name)
	if f == nil || f.fieldType != HmfMap {
		return nil
	}
	return f.childs
}

// GetList gets a list field
func (m *HTSMsg) GetList(name string) *HTSMsg {
	f := m.fieldFind(name)
	if f == nil || f.fieldType != HmfList {
		return nil
	}
	return f.childs
}

// GetMapMulti traverses a hierarchy to find a specific child map
func (m *HTSMsg) GetMapMulti(names ...string) *HTSMsg {
	current := m
	for _, name := range names {
		if current == nil {
			return nil
		}
		current = current.GetMap(name)
	}
	return current
}

// GetStrMulti traverses a hierarchy to find a specific string
func (m *HTSMsg) GetStrMulti(names ...string) string {
	current := m
	for i, name := range names {
		if current == nil {
			return ""
		}

		f := current.fieldFind(name)
		if f == nil {
			return ""
		}

		if f.fieldType == HmfStr {
			return f.strValue
		}

		if f.childs != nil {
			current = f.childs
		} else {
			return ""
		}

		// If this is the last name and we didn't return, we failed
		if i == len(names)-1 {
			return ""
		}
	}
	return ""
}

// DetachSubmsg detaches a sub-message, making it standalone
func (m *HTSMsg) DetachSubmsg(name string) *HTSMsg {
	f := m.fieldFind(name)
	if f == nil || f.childs == nil {
		return nil
	}

	return f.childs.Retain()
}

// Copy creates a deep copy of the message
func (m *HTSMsg) Copy() *HTSMsg {
	if m == nil {
		return nil
	}

	var dst *HTSMsg
	if m.isList {
		dst = NewList()
	} else {
		dst = NewMap()
	}

	for _, f := range m.fields {
		switch f.fieldType {
		case HmfMap, HmfList:
			var sub *HTSMsg
			if f.fieldType == HmfList {
				sub = NewList()
			} else {
				sub = NewMap()
			}
			if f.childs != nil {
				subCopy := f.childs.Copy()
				dst.AddMsg(f.name, subCopy)
			} else {
				dst.AddMsg(f.name, sub)
			}
		case HmfStr:
			dst.AddStr(f.name, f.strValue)
		case HmfS64:
			dst.AddS64(f.name, f.s64Value)
		case HmfBin:
			dst.AddBin(f.name, f.binData)
		case HmfDbl:
			dst.AddDbl(f.name, f.dblValue)
		}
	}

	return dst
}

// GetMapInList — C: htsmsg_get_map_in_list (htsmsg.c:697-706).
// num is 1-based over ALL fields (not only maps); returns the field's
// childs (NULL for scalar fields) via htsmsg_get_map_by_field.
func (m *HTSMsg) GetMapInList(num int) *HTSMsg {
	for _, f := range m.fields {
		num--
		if num == 0 {
			return f.childs
		}
	}
	return nil
}

// FieldGetByIndex — C: htsmsg_field_find(msg, HTSMSG_INDEX(i))
// (htsmsg.c:94-100, htsmsg.h HTSMSG_INDEX). Returns the i-th field
// (0-based) of a map or list — the Go spelling of C's negative-
// pointer index sentinel, since names are strings here.
func (m *HTSMsg) FieldGetByIndex(i int) *HTSMsgField {
	if i < 0 || i >= len(m.fields) {
		return nil
	}
	return m.fields[i]
}

// GetMapByFieldIfName gets a map from a field if the name matches
func (f *HTSMsgField) GetMapByFieldIfName(name string) *HTSMsg {
	if f.name != name {
		return nil
	}
	return f.childs
}

// GetChildren returns the number of children in the message
func (m *HTSMsg) GetChildren() int {
	return len(m.fields)
}

// GetFields returns all fields in the message (C: TAILQ_HEAD hmf_fields;
// HTSMSG_FOREACH iterates this list in insertion order)
func (m *HTSMsg) GetFields() []*HTSMsgField {
	return m.fields
}

// GetName returns the field name
func (f *HTSMsgField) GetName() string {
	return f.name
}

// GetType returns the field type
func (f *HTSMsgField) GetType() uint8 {
	return f.fieldType
}

// GetChilds returns the child message
func (f *HTSMsgField) GetChilds() *HTSMsg {
	return f.childs
}

// GetStrValue returns the string value
func (f *HTSMsgField) GetStrValue() string {
	return f.strValue
}

// GetS64Value returns the signed 64-bit integer value
func (f *HTSMsgField) GetS64Value() int64 {
	return f.s64Value
}

// GetDblValue returns the double value
func (f *HTSMsgField) GetDblValue() float64 {
	return f.dblValue
}

// GetBinData returns the binary data value of the field
func (f *HTSMsgField) GetBinData() []byte {
	return f.binData
}

// GetFlags returns the field flags — C: hmf_flags (HMF_XML_ATTRIBUTE etc.)
func (f *HTSMsgField) GetFlags() uint8 {
	return f.flags
}

// Print prints the message structure for debugging
func (m *HTSMsg) Print(prefix string) {
	m.print0(prefix, 0)
}

// print0 is the recursive implementation of Print
func (m *HTSMsg) print0(prefix string, indent int) {
	for _, f := range m.fields {
		var payload, typeStr, sep string

		switch f.fieldType {
		case HmfMap:
			typeStr = "map"
			sep = "} { "
		case HmfList:
			typeStr = "list"
			sep = "] [ "
		case HmfStr:
			typeStr = "str"
			payload = f.strValue
		case HmfBin:
			typeStr = "bin"
			payload = fmt.Sprintf("[%d bytes data]", len(f.binData))
		case HmfS64:
			typeStr = "int"
			payload = strconv.FormatInt(f.s64Value, 10)
		case HmfDbl:
			typeStr = "dbl"
			payload = fmt.Sprintf("%f", f.dblValue)
		}

		name := f.name
		if m.isList {
			name = ""
		}

		fmt.Printf("%s%*s\"%s\" = (%s)%s%s\n",
			prefix, indent, "", name, typeStr, payload,
			func() string {
				if f.childs != nil {
					return sep[1:]
				}
				return ""
			}())

		if f.childs != nil {
			f.childs.print0(prefix, indent+2)
			fmt.Printf("%s%*s%c\n", prefix, indent, "", sep[0])
		}
	}
}
