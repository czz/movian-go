// Canonical 1:1 port of src/api/xmlrpc.c — XML-RPC client over htsmsg.
package xmlrpc

import (
	"errors"
	"fmt"
	"strconv"

	facore "github.com/czz/movian-go/internal/fileaccess"
	htsmsg "github.com/czz/movian-go/internal/htsmsg"
	misc "github.com/czz/movian-go/internal/misc"
)

// xmlrpcParseValue — C: xmlrpc_parse_value (xmlrpc.c:44-99)
func xmlrpcParseValue(dst *htsmsg.HTSMsg, g *htsmsg.HTSMsgField,
	name string) (int, error) {
	var c *htsmsg.HTSMsg

	if g.GetName() == "struct" {
		if c = g.GetMap(); c != nil {
			sub := htsmsg.NewMap()
			if _, err := xmlrpcParseStruct(sub, c); err != nil {
				return -1, err
			}
			dst.AddMsg(name, sub)
			return 0, nil
		}
	} else if g.GetName() == "array" {
		if c = g.GetMap(); c != nil {
			sub := htsmsg.NewList()
			if _, err := xmlrpcParseArray(sub, c); err != nil {
				return -1, err
			}
			dst.AddMsg(name, sub)
			return 0, nil
		}
	}

	var cdata string
	hasCdata := false
	if g.GetType() == htsmsg.HmfStr {
		cdata = g.GetStrValue()
		hasCdata = true
	}

	switch g.GetName() {
	case "string":
		if hasCdata {
			dst.AddStr(name, cdata)
		}
	case "boolean":
		if hasCdata {
			v, _ := strconv.Atoi(cdata) // C: atoi
			dst.AddU32(name, uint32(v))
		}
	case "double":
		if hasCdata {
			v, _ := misc.MyStr2double(cdata)
			dst.AddDbl(name, v)
		}
	case "int":
		if hasCdata {
			v, _ := strconv.ParseInt(cdata, 10, 64) // C: atoll
			dst.AddS64(name, v)
		}
	default:
		return -1, fmt.Errorf("Unknown field type \"%s\" %s = %s",
			g.GetName(), name, cdata)
	}
	return 0, nil
}

// xmlrpcParseStruct — C: xmlrpc_parse_struct (xmlrpc.c:103-126)
func xmlrpcParseStruct(dst *htsmsg.HTSMsg, params *htsmsg.HTSMsg) (int, error) {
	for _, f := range params.GetFields() {
		c := f.GetMapByFieldIfName("member")
		if c == nil {
			continue
		}

		name, ok := htsmsgGetStrOK(c, "name")
		if !ok {
			continue
		}

		c2 := c.GetMap("value")
		if c2 == nil {
			continue
		}

		fields := c2.GetFields()
		if len(fields) == 0 {
			continue
		}
		g := fields[0]

		if _, err := xmlrpcParseValue(dst, g, name); err != nil {
			return -1, err
		}
	}
	return 0, nil
}

// xmlrpcParseArray — C: xmlrpc_parse_array (xmlrpc.c:133-157)
func xmlrpcParseArray(dst *htsmsg.HTSMsg, m *htsmsg.HTSMsg) (int, error) {
	m = m.GetMap("data")
	if m == nil {
		return 0, nil // Empty array
	}

	for _, f := range m.GetFields() {
		var c *htsmsg.HTSMsg
		if f.GetName() == "value" {
			c = f.GetMap()
		}
		if c == nil {
			continue
		}

		fields := c.GetFields()
		if len(fields) == 0 || fields[0].GetType() != htsmsg.HmfMap {
			continue
		}
		g := fields[0]

		if _, err := xmlrpcParseValue(dst, g, ""); err != nil {
			return -1, err
		}
	}
	return 0, nil
}

// xmlrpcConvertResponse — C: xmlrpc_convert_response (xmlrpc.c:163-193)
func xmlrpcConvertResponse(xml *htsmsg.HTSMsg) (*htsmsg.HTSMsg, error) {
	params := xml.GetMapMulti("methodResponse", "params")
	if params == nil {
		return nil, errors.New("No params in reply found")
	}

	dst := htsmsg.NewList()

	for _, f := range params.GetFields() {
		var c *htsmsg.HTSMsg
		if f.GetName() == "param" {
			c = f.GetMap()
		}
		if c == nil {
			continue
		}

		c = c.GetMap("value")
		if c == nil {
			continue
		}

		fields := c.GetFields()
		if len(fields) == 0 || fields[0].GetType() != htsmsg.HmfMap {
			continue
		}
		g := fields[0]

		if _, err := xmlrpcParseValue(dst, g, ""); err != nil {
			return nil, err
		}
	}
	return dst, nil
}

// xmlrpcWriteField — C: xmlrpc_write_field (xmlrpc.c:204-230)
func xmlrpcWriteField(q *misc.HtsbufQueue, f *htsmsg.HTSMsgField,
	pre, post string) {
	switch f.GetType() {
	case htsmsg.HmfS64:
		q.QPrintf("%s<value><int>%d</int></value>%s\n",
			pre, f.GetS64Value(), post)

	case htsmsg.HmfStr:
		q.QPrintf("%s<value><string>%s</string></value>%s\n",
			pre, f.GetStrValue(), post)

	case htsmsg.HmfList:
		q.QPrintf("%s<value><array><data>", pre)
		xmlrpcWriteList(q, f.GetChilds(), "", "")
		q.QPrintf("</data></array></value>%s\n", post)

	case htsmsg.HmfMap:
		q.QPrintf("%s<value><struct>", pre)
		xmlrpcWriteMap(q, f.GetChilds())
		q.QPrintf("</struct></value>%s\n", post)
	}
}

// xmlrpcWriteMap — C: xmlrpc_write_map (xmlrpc.c:236-244)
func xmlrpcWriteMap(q *misc.HtsbufQueue, m *htsmsg.HTSMsg) {
	for _, f := range m.GetFields() {
		q.QPrintf("<member><name>%s</name>", f.GetName())
		xmlrpcWriteField(q, f, "", "")
		q.QPrintf("</member>\n")
	}
}

// xmlrpcWriteList — C: xmlrpc_write_list (xmlrpc.c:250-256)
func xmlrpcWriteList(q *misc.HtsbufQueue, m *htsmsg.HTSMsg,
	pre, post string) {
	for _, f := range m.GetFields() {
		xmlrpcWriteField(q, f, pre, post)
	}
}

// Request — C: xmlrpc_request (xmlrpc.c:261-288)
func Request(fam *facore.FileAccessManager, url, method string, params *htsmsg.HTSMsg) (*htsmsg.HTSMsg, error) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(-1)
	q.QPrintf("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n"+
		"<methodCall>\n"+
		"<methodName>%s</methodName>\n"+
		"<params>\n", method)

	xmlrpcWriteList(&q, params, "<param>", "</param>")
	q.QPrintf("</params></methodCall>\n")

	var result *misc.Buf
	var errbuf [256]byte // C: char errbuf[256] — HTTP_ERRBUF seam
	n := fam.HTTPReq(url,
		facore.HTTPTagResultPtr, &result,
		facore.HTTPTagErrbuf, errbuf[:],
		facore.HTTPTagPostData, &q, "text/xml")

	if n != 0 {
		return nil, errors.New(errbufStr(errbuf[:]))
	}
	var xml *htsmsg.HTSMsg
	var err error
	if result != nil {
		xml, err = htsmsg.DeserializeXMLBuf(result.C8())
	}
	if xml == nil || err != nil {
		return nil, err
	}

	return xmlrpcConvertResponse(xml)
}

// helpers

// errbufStr reads the C-style NUL-terminated errbuf filled by the
// HTTP_ERRBUF seam.
func errbufStr(errbuf []byte) string {
	n := 0
	for n < len(errbuf) && errbuf[n] != 0 {
		n++
	}
	return string(errbuf[:n])
}

// htsmsgGetStrOK — C: htsmsg_get_str returning NULL for absent field
// (distinguishes absent from present-but-empty, unlike GetStr).
func htsmsgGetStrOK(m *htsmsg.HTSMsg, name string) (string, bool) {
	f := m.FieldFind(name)
	if f == nil {
		return "", false
	}
	if f.GetType() != htsmsg.HmfStr {
		return "", false
	}
	return f.GetStrValue(), true
}
