package htsmsg

// Canonical port of src/htsmsg/htsmsg_xml.c — the hand-written XML
// parser (UTF-8 and ISO-8859-1). Not encoding/xml: the C parser is
// deliberately lenient (recovering entity failures, bytes <32 skipped
// at segment start, per-segment whitespace trimming, no XML-name
// validation) and supports the xmlns:prefix → hmf_namespace mapping.

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/misc"
)

const (
	xmlEncUTF8  = 0
	xmlEnc88591 = 1
)

// xmlNs — C: xmlns_t (htsmsg_xml.c:62-71).
type xmlNs struct {
	prefix     string
	normalized string
}

// xmlParser — C: xmlparser_t (htsmsg_xml.c:73-87).
type xmlParser struct {
	src        []byte
	encoding   int
	errmsg     string
	errpos     int
	errline    int
	trimWS     bool
	namespaces []*xmlNs // global list, most recent first
}

// ccSegment — C: cdata_content_t (htsmsg_xml.c:96-101). Either a
// (start,end) slice of the source document or an owned buf produced
// by add_unicode.
type ccSegment struct {
	encoding   int
	start, end int
	buf        []byte
}

func (c *ccSegment) bytes(src []byte) []byte {
	if c.buf != nil {
		return c.buf
	}
	return src[c.start:c.end]
}

// xmlerr — C: xmlerr2 (htsmsg_xml.c:89-93).
func (p *xmlParser) xmlerr(pos int, line int, format string, args ...any) {
	p.errmsg = fmt.Sprintf(format, args...)
	p.errpos = pos
	p.errline = line
}

// eof — C: *src == 0 (input is treated as NUL-terminated).
func (p *xmlParser) eof(pos int) bool {
	return pos >= len(p.src) || p.src[pos] == 0
}

// byteAt — C: *src — 0 at end of input.
func (p *xmlParser) byteAt(pos int) byte {
	if pos >= len(p.src) {
		return 0
	}
	return p.src[pos]
}

// isXMLWS — C: is_xmlws (htsmsg_xml.c:193-197): c > 0 && c <= 32.
func isXMLWS(c byte) bool {
	return c > 0 && c <= 32
}

// xmlIsCCWS — C: xml_is_cc_ws (htsmsg_xml.c:108-117): all bytes <= 32.
func xmlIsCCWS(cc *ccSegment, src []byte) bool {
	for _, b := range cc.bytes(src) {
		if b > 32 {
			return false
		}
	}
	return true
}

// addUnicode — C: add_unicode (htsmsg_xml.c:124-138).
func addUnicode(ccq *[]*ccSegment, c int) {
	buf := make([]byte, 6)
	n := misc.Utf8Put(buf, c)
	*ccq = append(*ccq, &ccSegment{
		encoding: xmlEncUTF8,
		buf:      buf[:n],
	})
}

// decodeCharacterReference — C: decode_character_reference
// (htsmsg_xml.c:144-187). Returns (value, newpos); 0 on failure.
func decodeCharacterReference(src []byte, pos int) (int, int) {
	v := 0
	at := func(i int) byte {
		if i >= len(src) {
			return 0
		}
		return src[i]
	}
	if at(pos) == 'x' {
		pos++
		for {
			c := at(pos)
			switch {
			case c >= '0' && c <= '9':
				v = v*0x10 + int(c-'0')
			case c >= 'a' && c <= 'f':
				v = v*0x10 + int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v = v*0x10 + int(c-'A') + 10
			case c == ';':
				return v, pos + 1
			default:
				return 0, pos
			}
			pos++
		}
	}
	for {
		c := at(pos)
		switch {
		case c >= '0' && c <= '9':
			v = v*10 + int(c-'0')
		case c == ';':
			return v, pos + 1
		default:
			return 0, pos
		}
		pos++
	}
}

// addXMLField — C: add_xml_field (htsmsg_xml.c:216-235). Resolves a
// declared namespace prefix: field name becomes the local part and
// hmf_namespace gets the normalized URI. Undeclared prefixes (or
// "prefix:" with empty local part) keep the full tag name.
func (p *xmlParser) addXMLField(parent *HTSMsg, tagname string, fieldType uint8, flags uint8) *HTSMsgField {
	i := strings.IndexByte(tagname, ':')
	if i >= 0 && i+1 < len(tagname) {
		for _, ns := range p.namespaces {
			if len(ns.prefix) == i && ns.prefix == tagname[:i] {
				f := parent.fieldAdd(tagname[i+1:], fieldType, flags)
				f.namespace = ns.normalized
				return f
			}
		}
	}
	return parent.fieldAdd(tagname, fieldType, flags)
}

// parseAttrib — C: htsmsg_xml_parse_attrib (htsmsg_xml.c:242-355).
// scope is nil for processing-instruction attributes (where xmlns:*
// is treated as a normal attribute).
func (p *xmlParser) parseAttrib(msg *HTSMsg, pos int, scope *[]*xmlNs) (int, bool) {
	attribStart := pos
	for {
		if p.eof(pos) {
			p.xmlerr(pos, 0, "Unexpected end of file during attribute name parsing")
			return pos, false
		}
		if isXMLWS(p.src[pos]) || p.src[pos] == '=' {
			break
		}
		pos++
	}
	attriblen := pos - attribStart
	if attriblen < 1 || attriblen > 65535 {
		p.xmlerr(attribStart, 0, "Invalid attribute name")
		return pos, false
	}
	attribname := string(p.src[attribStart:pos])

	for p.byteAt(pos) != 0 && isXMLWS(p.src[pos]) {
		pos++
	}
	if p.byteAt(pos) != '=' {
		p.xmlerr(pos, 0, "Expected '=' in attribute parsing")
		return pos, false
	}
	pos++
	for p.byteAt(pos) != 0 && isXMLWS(p.src[pos]) {
		pos++
	}

	quote := p.byteAt(pos)
	pos++
	if quote != '"' && quote != '\'' {
		p.xmlerr(pos-1, 0, "Expected ' or \" before attribute value")
		return pos, false
	}

	payloadStart := pos
	for {
		if p.eof(pos) {
			p.xmlerr(pos, 0, "Unexpected end of file during attribute value parsing")
			return pos, false
		}
		if p.src[pos] == quote {
			break
		}
		pos++
	}
	payloadlen := pos - payloadStart
	if payloadlen < 0 || payloadlen > 65535 {
		p.xmlerr(payloadStart, 0, "Invalid attribute value")
		return pos, false
	}
	payload := string(p.src[payloadStart:pos])

	pos++
	for p.byteAt(pos) != 0 && isXMLWS(p.src[pos]) {
		pos++
	}

	if scope != nil && attriblen > 6 && attribname[:6] == "xmlns:" {
		ns := &xmlNs{
			prefix:     attribname[6:],
			normalized: payload,
		}
		// LIST_INSERT_HEAD on both the global and scope lists.
		p.namespaces = slices.Insert(p.namespaces, 0, ns)
		*scope = append([]*xmlNs{ns}, *scope...)
		return pos, true
	}

	// C checks the byte AFTER name/value for '\n' (which it would
	// otherwise overwrite with NUL); we only reproduce the flag
	// difference since the buffer is never mutated.
	allocFlags := uint8(HmfXmlAttribute)
	if p.byteAt(attribStart+attriblen) == '\n' || p.byteAt(payloadStart+payloadlen) == '\n' {
		allocFlags |= HmfNameAlloced | HmfAlloced
	}
	f := p.addXMLField(msg, attribname, HmfStr, allocFlags)
	f.strValue = payload
	if allocFlags&HmfAlloced == 0 && msg.backingStore == nil {
		// C: htsmsg_set_backing_store — retain once (asserts same buf
		// on repeat, which is structurally guaranteed here).
		msg.backingStore = p.src
	}
	return pos, true
}

// destroyNsScope — C: xmlns_destroy over a scope list
// (htsmsg_xml.c:204-211, 441-443): removes the entries from the
// parser's global namespace list too.
func (p *xmlParser) destroyNsScope(scope []*xmlNs) {
	for _, ns := range scope {
		for i, g := range p.namespaces {
			if g == ns {
				p.namespaces = slices.Delete(p.namespaces, i, i+1)
				break
			}
		}
	}
}

// parseTag — C: htsmsg_xml_parse_tag (htsmsg_xml.c:361-445).
func (p *xmlParser) parseTag(parent *HTSMsg, pos int) (int, bool) {
	var nslist []*xmlNs
	tagnameStart := pos

	m := NewMap()

	for {
		if p.eof(pos) {
			p.xmlerr(pos, 0, "Unexpected end of file during tag name parsing")
			m.Release()
			return pos, false
		}
		if isXMLWS(p.src[pos]) || p.src[pos] == '>' || p.src[pos] == '/' {
			break
		}
		pos++
	}
	taglen := pos - tagnameStart
	if taglen < 1 || taglen > 65535 {
		p.xmlerr(tagnameStart, 0, "Invalid tag name")
		m.Release()
		return pos, false
	}
	tagname := string(p.src[tagnameStart:pos])

	empty := false
	for {
		for p.byteAt(pos) != 0 && isXMLWS(p.src[pos]) {
			pos++
		}
		if p.eof(pos) {
			p.xmlerr(pos, 0, "Unexpected end of file in tag")
			m.Release()
			p.destroyNsScope(nslist)
			return pos, false
		}
		if p.byteAt(pos) == '/' && p.byteAt(pos+1) == '>' {
			empty = true
			pos += 2
			break
		}
		if p.byteAt(pos) == '>' {
			pos++
			break
		}
		npos, ok := p.parseAttrib(m, pos, &nslist)
		if !ok {
			m.Release()
			p.destroyNsScope(nslist)
			return pos, false
		}
		pos = npos
	}

	flags := uint8(0)
	if p.byteAt(tagnameStart+taglen) == '\n' {
		flags = HmfNameAlloced
	}
	f := p.addXMLField(parent, tagname, HmfMap, flags)

	if !empty {
		npos, ok := p.parseCD(m, f, pos)
		if !ok {
			p.destroyNsScope(nslist)
			return pos, false
		}
		pos = npos
	}

	if len(m.fields) > 0 {
		f.childs = m
	} else {
		m.Release()
	}
	p.destroyNsScope(nslist)
	return pos, true
}

// parsePI — C: htsmsg_xml_parse_pi (htsmsg_xml.c:455-516). parent is
// nil inside elements (PIs are then parsed but discarded).
func (p *xmlParser) parsePI(parent *HTSMsg, pos int) (int, bool) {
	start := pos
	for {
		if p.eof(pos) {
			p.xmlerr(pos, 0, "Unexpected end of file during parsing of Processing instructions")
			return pos, false
		}
		if isXMLWS(p.src[pos]) || p.src[pos] == '?' {
			break
		}
		pos++
	}
	l := pos - start
	if l < 1 || l > 1024 {
		p.xmlerr(pos, 0, "Invalid 'Processing instructions' name")
		return pos, false
	}
	piname := string(p.src[start:pos])

	attrs := NewMap()
	for {
		for p.byteAt(pos) != 0 && isXMLWS(p.src[pos]) {
			pos++
		}
		if p.eof(pos) {
			attrs.Release()
			p.xmlerr(pos, 0, "Unexpected end of file during parsing of Processing instructions")
			return pos, false
		}
		if p.byteAt(pos) == '?' && p.byteAt(pos+1) == '>' {
			pos += 2
			break
		}
		npos, ok := p.parseAttrib(attrs, pos, nil)
		if !ok {
			attrs.Release()
			return pos, false
		}
		pos = npos
	}

	if len(attrs.fields) > 0 && parent != nil {
		parent.AddMsg(piname, attrs)
	} else {
		attrs.Release()
	}
	return pos, true
}

// parseComment — C: xml_parse_comment (htsmsg_xml.c:523-537).
func (p *xmlParser) parseComment(pos int) (int, bool) {
	start := pos
	for {
		if p.eof(pos) {
			p.xmlerr(start, 0, "Unexpected end of file inside a comment")
			return pos, false
		}
		if p.byteAt(pos) == '-' && p.byteAt(pos+1) == '-' && p.byteAt(pos+2) == '>' {
			return pos + 3, true
		}
		pos++
	}
}

// decodeLabelReference — C: decode_label_reference
// (htsmsg_xml.c:543-585).
func (p *xmlParser) decodeLabelReference(ccq *[]*ccSegment, pos int) (int, bool) {
	start := pos
	s := pos
	for !p.eof(pos) && p.src[pos] != ';' {
		pos++
	}
	if p.eof(pos) {
		p.xmlerr(start, 0, "Unexpected end of file during parsing of label reference")
		return pos, false
	}
	l := pos - s
	if l < 1 {
		p.xmlerr(s, 0, "Too short label reference")
		return pos, false
	}
	if l > 1024 {
		p.xmlerr(s, 0, "Too long label reference")
		return pos, false
	}
	label := string(p.src[s:pos])
	pos++

	code := misc.HtmlEntityLookup(label)
	if code != -1 {
		addUnicode(ccq, code)
	} else {
		p.xmlerr(start, 0, "Unknown label referense: \"&%s;\"\n", label)
		return pos, false
	}
	return pos, true
}

// parseCD0 — C: htsmsg_xml_parse_cd0 (htsmsg_xml.c:591-706).
// pis is nil inside elements.
func (p *xmlParser) parseCD0(ccq *[]*ccSegment, tags, pis *HTSMsg, pos int, raw bool) (int, bool) {
	var cc *ccSegment

	for !p.eof(pos) {
		if raw && p.byteAt(pos) == ']' && p.byteAt(pos+1) == ']' && p.byteAt(pos+2) == '>' {
			if cc != nil {
				cc.end = pos
			}
			cc = nil
			pos += 3
			break
		}

		if p.src[pos] == '<' && !raw {
			if cc != nil {
				cc.end = pos
			}
			cc = nil

			pos++
			if p.byteAt(pos) == '?' {
				pos++
				npos, ok := p.parsePI(pis, pos)
				if !ok {
					return pos, false
				}
				pos = npos
				continue
			}

			if p.byteAt(pos) == '!' {
				pos++
				if p.byteAt(pos) == '-' && p.byteAt(pos+1) == '-' {
					npos, ok := p.parseComment(pos + 2)
					if !ok {
						return pos, false
					}
					pos = npos
					continue
				}
				if pos+7 <= len(p.src) && string(p.src[pos:pos+7]) == "[CDATA[" {
					pos += 7
					npos, ok := p.parseCD0(ccq, tags, pis, pos, true)
					if !ok {
						return pos, false
					}
					pos = npos
					continue
				}
				end := min(pos+10, len(p.src))
				p.xmlerr(pos, 0, "Unknown syntatic element: <!%.10s", p.src[pos:end])
				return pos, false
			}

			if p.byteAt(pos) == '/' {
				// End-tag
				pos++
				for p.byteAt(pos) != '>' {
					if p.eof(pos) {
						p.xmlerr(pos, 0, "Unexpected end of file inside close tag")
						return pos, false
					}
					pos++
				}
				pos++
				break
			}

			npos, ok := p.parseTag(tags, pos)
			if !ok {
				return pos, false
			}
			pos = npos
			continue
		}

		if p.src[pos] == '&' && !raw {
			if cc != nil {
				cc.end = pos
			}
			pos++
			if p.byteAt(pos) == '#' {
				start := pos
				pos++
				c, npos := decodeCharacterReference(p.src, pos)
				if c != 0 {
					addUnicode(ccq, c)
					pos = npos
				} else {
					p.xmlerr(start, 0, "Invalid character reference")
					return pos, false
				}
				cc = nil
			} else {
				npos, ok := p.decodeLabelReference(ccq, pos)
				if ok {
					pos = npos
					cc = nil
				} else {
					// C: continue — parse carries on after a failed
					// entity; the closed cc is extended again by the
					// next '<'/'&' (keeping the text verbatim).
					continue
				}
			}
			continue
		}

		if cc == nil {
			if p.src[pos] < 32 {
				pos++
				continue
			}
			cc = &ccSegment{encoding: p.encoding, start: pos}
			*ccq = append(*ccq, cc)
		}
		pos++
	}

	if cc != nil {
		cc.end = pos
	}
	return pos, true
}

// parseCD — C: htsmsg_xml_parse_cd (htsmsg_xml.c:712-808).
func (p *xmlParser) parseCD(msg *HTSMsg, field *HTSMsgField, pos int) (int, bool) {
	var ccq []*ccSegment

	npos, ok := p.parseCD0(&ccq, msg, nil, pos, false)
	pos = npos
	if !ok {
		return pos, false
	}

	if p.trimWS {
		for len(ccq) > 0 && xmlIsCCWS(ccq[0], p.src) {
			ccq = ccq[1:]
		}
		for len(ccq) > 0 && xmlIsCCWS(ccq[len(ccq)-1], p.src) {
			ccq = ccq[:len(ccq)-1]
		}
	}

	// Measure the assembled body
	c := 0
	y := 0
	for _, cc := range ccq {
		switch cc.encoding {
		case xmlEncUTF8:
			c += cc.end - cc.start
			if cc.buf != nil {
				c += len(cc.buf) - (cc.end - cc.start)
			}
			y++
		case xmlEnc88591:
			l := 0
			for _, b := range cc.bytes(p.src) {
				l += 1
				if b >= 0x80 {
					l++
				}
			}
			c += l
			if l != len(cc.bytes(p.src)) {
				y += 2
			} else {
				y++
			}
		}
	}

	if field != nil && y == 1 && c > 0 && p.byteAt(ccq[0].end) != '\n' {
		// One segment UTF-8 (or 7bit ASCII) — use data directly.
		cc := ccq[0]
		field.strValue = string(cc.bytes(p.src))
		field.fieldType = HmfStr
	} else if field != nil && c > 1 {
		var body []byte
		for _, cc := range ccq {
			switch cc.encoding {
			case xmlEncUTF8:
				body = append(body, cc.bytes(p.src)...)
			case xmlEnc88591:
				for _, b := range cc.bytes(p.src) {
					var tmp [8]byte
					n := misc.Utf8Put(tmp[:], int(b))
					body = append(body, tmp[:n]...)
				}
			}
		}
		field.strValue = string(body)
		field.fieldType = HmfStr
		field.flags |= HmfAlloced
	}
	return pos, true
}

// parseProlog — C: htsmsg_parse_prolog (htsmsg_xml.c:815-873).
func (p *xmlParser) parseProlog(pos int) (int, bool) {
	pis := NewMap()

	for {
		if p.eof(pos) {
			break
		}
		for p.byteAt(pos) != 0 && isXMLWS(p.src[pos]) {
			pos++
		}

		if p.byteAt(pos) == '<' && p.byteAt(pos+1) == '?' {
			pos += 2
			npos, ok := p.parsePI(pis, pos)
			if !ok {
				pis.Release()
				return pos, false
			}
			pos = npos
			continue
		}

		if pos+4 <= len(p.src) && string(p.src[pos:pos+4]) == "<!--" {
			npos, ok := p.parseComment(pos + 4)
			if !ok {
				pis.Release()
				return pos, false
			}
			pos = npos
			continue
		}

		if pos+9 <= len(p.src) && string(p.src[pos:pos+9]) == "<!DOCTYPE" {
			depth := 0
			for !p.eof(pos) {
				if p.src[pos] == '<' {
					depth++
				} else if p.src[pos] == '>' {
					pos++
					depth--
					if depth == 0 {
						break
					}
				}
				pos++
			}
			continue
		}
		break
	}

	if xmlpi := pis.GetMap("xml"); xmlpi != nil {
		enc := xmlpi.GetStr("encoding")
		low := strings.ToLower(enc)
		if low == "iso-8859-1" || low == "iso-8859_1" ||
			low == "iso_8859-1" || low == "iso_8859_1" {
			p.encoding = xmlEnc88591
		}
	}
	pis.Release()
	return pos, true
}

// getLineCol — C: get_line_col (htsmsg_xml.c:880-902).
func getLineCol(src []byte, pos int) (int, int) {
	line := 1
	column := 0
	for i := range src {
		column++
		if src[i] == '\n' {
			column = 0
			line++
		} else if src[i] == '\r' {
			column = 0
		}
		if i == pos {
			break
		}
	}
	return line, column
}

// DeserializeXMLBuf — C: htsmsg_xml_deserialize_buf
// (htsmsg_xml.c:909-957).
func DeserializeXMLBuf(data []byte) (*HTSMsg, error) {
	p := &xmlParser{
		src:      data,
		encoding: xmlEncUTF8,
		trimWS:   true,
	}

	pos, ok := p.parseProlog(0)
	if !ok {
		return nil, p.error(data)
	}

	m := NewMap()
	if _, ok := p.parseCD(m, nil, pos); !ok {
		m.Release()
		return nil, p.error(data)
	}
	return m, nil
}

// error — C: the err: label of htsmsg_xml_deserialize_buf.
func (p *xmlParser) error(data []byte) error {
	line, col := getLineCol(data, p.errpos)
	msg := fmt.Sprintf("%s at line %d column %d (XML error %d at byte %d)",
		p.errmsg, line, col, p.errline, p.errpos)
	// C truncates errmsg at the first byte <32
	for i := range len(msg) {
		if msg[i] < 32 {
			msg = msg[:i]
			break
		}
	}
	return errors.New(msg)
}

// DeserializeXML — C: htsmsg_xml_deserialize_cstr (htsmsg_xml.c:964-969).
func DeserializeXML(str string) (*HTSMsg, error) {
	return DeserializeXMLBuf([]byte(str))
}

// DeserializeXML2 — deserialize with C-style error string.
func DeserializeXML2(str string) (*HTSMsg, string) {
	msg, err := DeserializeXML(str)
	if err != nil {
		return nil, err.Error()
	}
	return msg, ""
}

// ParseXMLReader parses XML from an io.Reader
func ParseXMLReader(r io.Reader) (*HTSMsg, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return DeserializeXMLBuf(data)
}

// HTMLEntityLookup — C: html_entity_lookup (misc/str.c).
func HTMLEntityLookup(name string) (rune, error) {
	code := misc.HtmlEntityLookup(name)
	if code == -1 {
		return 0, fmt.Errorf("unknown entity: %s", name)
	}
	return rune(code), nil
}

// EscapeXMLEscapes escapes XML special characters
func EscapeXMLEscapes(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	for i := range len(s) {
		switch s[i] {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		case '"':
			buf.WriteString("&quot;")
		case '\'':
			buf.WriteString("&apos;")
		default:
			buf.WriteByte(s[i])
		}
	}
	return buf.String()
}

// --- XML serialization (Go-side convenience; C's htsmsg_xml.c only
// deserializes) ---------------------------------------------------

// XMLNode represents an XML node for serialization
type XMLNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Content []byte     `xml:",innerxml"`
	Nodes   []*XMLNode `xml:",any"`
}

// SerializeXML serializes a message to XML format
func SerializeXML(msg *HTSMsg, rootTag string) (string, error) {
	if msg == nil {
		return "", errors.New("nil message")
	}

	if rootTag == "" {
		rootTag = "root"
	}

	node := htsmsgToXMLNode(msg, rootTag)

	data, err := xml.MarshalIndent(node, "", "  ")
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// htsmsgToXMLNode converts an HTSMsg to an XML node
func htsmsgToXMLNode(msg *HTSMsg, name string) *XMLNode {
	node := &XMLNode{
		XMLName: xml.Name{Local: name},
	}

	for _, f := range msg.fields {
		if f.flags&HmfXmlAttribute != 0 {
			node.Attrs = append(node.Attrs, xml.Attr{
				Name:  xml.Name{Local: f.name},
				Value: f.strValue,
			})
		} else {
			switch f.fieldType {
			case HmfMap:
				if f.childs != nil {
					node.Nodes = append(node.Nodes, htsmsgToXMLNode(f.childs, f.name))
				}

			case HmfList:
				if f.childs != nil {
					for _, listField := range f.childs.fields {
						if listField.fieldType == HmfMap && listField.childs != nil {
							node.Nodes = append(node.Nodes, htsmsgToXMLNode(listField.childs, f.name))
						}
					}
				}

			case HmfStr:
				node.Nodes = append(node.Nodes, &XMLNode{
					XMLName: xml.Name{Local: f.name},
					Content: []byte(f.strValue),
				})

			case HmfS64:
				node.Nodes = append(node.Nodes, &XMLNode{
					XMLName: xml.Name{Local: f.name},
					Content: []byte(strconv.FormatInt(f.s64Value, 10)),
				})

			case HmfDbl:
				node.Nodes = append(node.Nodes, &XMLNode{
					XMLName: xml.Name{Local: f.name},
					Content: []byte(fmt.Sprintf("%f", f.dblValue)),
				})

			case HmfBin:
				node.Nodes = append(node.Nodes, &XMLNode{
					XMLName: xml.Name{Local: f.name},
					Content: []byte("binary"),
				})
			}
		}
	}

	return node
}
