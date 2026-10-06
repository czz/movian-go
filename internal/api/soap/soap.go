package soap

import (
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/htsmsg"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// SoapEncodeArg encodes one htsmsg field as a SOAP argument element.
// C: soap_encode_arg (soap.c:30-51)
func SoapEncodeArg(xml *strings.Builder, f *htsmsg.HTSMsgField) {
	switch f.GetType() {
	case htsmsg.HmfS64:
		fmt.Fprintf(xml, "<%s>%d</%s>", f.GetName(), f.GetS64Value(), f.GetName())

	case htsmsg.HmfStr:
		s := f.GetStrValue()
		if s == "" {
			fmt.Fprintf(xml, "<%s/>", f.GetName())
			return
		}
		fmt.Fprintf(xml, "<%s>", f.GetName())
		xml.WriteString(EscapeXML(s)) // C: htsbuf_append_and_escape_xml
		fmt.Fprintf(xml, "</%s>", f.GetName())
	}
}

// SoapEncodeArgs encodes all fields of a map as SOAP arguments.
// C: soap_encode_args (soap.c:57-62)
func SoapEncodeArgs(xml *strings.Builder, args *htsmsg.HTSMsg) {
	for _, f := range args.GetFields() {
		SoapEncodeArg(xml, f)
	}
}

// SoapExec executes a SOAP request and returns the response args map.
// C: soap_exec (soap.c:68-118) — r != 0 → err non-nil.
func SoapExec(uri string, service string, version int, method string,
	in *htsmsg.HTSMsg) (*htsmsg.HTSMsg, error) {

	var post strings.Builder

	fmt.Fprintf(&post,
		"<?xml version=\"1.0\" encoding=\"utf-8\"?>"+
			"<s:Envelope s:encodingStyle=\"http://schemas.xmlsoap.org/soap/encoding/\" xmlns:s=\"http://schemas.xmlsoap.org/soap/envelope/\">"+
			"<s:Body><ns0:%s xmlns:ns0=\"urn:schemas-upnp-org:service:%s:%d\">",
		method, service, version)

	SoapEncodeArgs(&post, in)
	fmt.Fprintf(&post, "</ns0:%s></s:Body></s:Envelope>", method)

	soapAction := fmt.Sprintf("\"urn:schemas-upnp-org:service:%s:%d#%s\"",
		service, version, method)

	client := httpnet.NewHTTPClient()
	req := &httpnet.HTTPRequest{
		URL:    uri,
		Method: httpnet.HTTPCmdPost,
		Headers: httpnet.HTTPHeaders{
			{Key: "Content-Type", Value: "text/xml; charset=\"utf-8\""},
			{Key: "SOAPACTION", Value: soapAction},
		},
		Body: []byte(post.String()),
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	out, err := htsmsg.DeserializeXMLBuf(resp.Body)
	if out == nil {
		return nil, err
	}

	responseName := fmt.Sprintf("%sResponse", method)
	outargs := out.GetMapMulti("Envelope", "Body", responseName)

	var out2 *htsmsg.HTSMsg
	if outargs != nil {
		out2 = htsmsg.NewMap()
		// Convert args from XML style to more compact style
		for _, f := range outargs.GetFields() {
			if f.GetType() == htsmsg.HmfStr {
				out2.AddStr(f.GetName(), f.GetStrValue())
			}
		}
	}
	out.Release()
	return out2, nil
}

// EscapeXML escapes XML special characters.
// C: htsbuf_append_and_escape_xml (htsbuf.c:302-331)
func EscapeXML(s string) string {
	var b strings.Builder
	for i := range len(s) {
		switch s[i] {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '\'':
			b.WriteString("&apos;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
