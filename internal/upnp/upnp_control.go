package upnp

import (
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/api/soap"
	"github.com/czz/movian-go/internal/htsmsg"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// UPNPControlInvalidArgs is the sentinel a method returns on bad args.
// C: UPNP_CONTROL_INVALID_ARGS ((void *)-1)
// upnpControlInvalidArgs — sentinel compared by pointer identity.
// C: UPNP_CONTROL_INVALID_ARGS ((void *)-1, upnp.h:153)
var upnpControlInvalidArgs = &htsmsg.HTSMsg{}

// controlDispatchMethod dispatches one SOAP method on a local service.
// C: control_dispatch_method (upnp_control.c:34-99)
func (uls *UPNPLocalService) controlDispatchMethod(usm *UPNPServiceMethod,
	hc *httpnet.HTTPConnection, inargs *htsmsg.HTSMsg) int {

	httpCode := 200

	inargs = inargs.GetMap("tags")
	in := htsmsg.NewMap()
	if inargs != nil {
		// Convert args from XML style to more compact style
		for _, f := range inargs.GetFields() {
			a := f.GetMap()
			if a == nil {
				continue
			}
			if s := a.GetStr("cdata"); s != "" {
				in.AddStr(f.GetName(), s)
			}
		}
	}

	out := usm.fn(hc, in, hc.HTTPGetMyHost(), hc.HTTPGetMyPort())
	in.Release()

	var xml strings.Builder

	xml.WriteString("<?xml version=\"1.0\"?>" +
		"<s:Envelope xmlns:s=\"http://schemas.xmlsoap.org/soap/envelope/\" s:encodingStyle=\"http://schemas.xmlsoap.org/soap/encoding/\">" +
		"<s:Body>")

	if out == upnpControlInvalidArgs {
		xml.WriteString("<s:Fault>" +
			"<s:faultcode>500</s:faultcode></s:Fault>")
		httpCode = 500
	} else {
		fmt.Fprintf(&xml,
			"<u:%sResponse "+
				"xmlns:u=\"urn:schemas-upnp-org:service:%s:%d\">",
			usm.name,
			uls.name,
			uls.version)

		if out != nil {
			soapEncodeArgsHTSMsg(&xml, out)
			out.Release()
		}

		fmt.Fprintf(&xml, "</u:%sResponse>", usm.name)
	}

	xml.WriteString("</s:Body></s:Envelope>")

	hc.HTTPSetResponseHdr("EXT", "")
	return hc.HTTPSendReply(httpCode, "text/xml; charset=\"utf-8\"",
		"", "", 0, []byte(xml.String()))
}

// soapEncodeArgsHTSMsg encodes an htsmsg map's fields as XML args.
// C: soap_encode_args(xml, out) iterating HTSMSG fields.
func soapEncodeArgsHTSMsg(xml *strings.Builder, out *htsmsg.HTSMsg) {
	for _, f := range out.GetFields() {
		soap.SoapEncodeArg(xml, f)
	}
}

// controlParseSoap parses the SOAP envelope and dispatches to a method.
// C: control_parse_soap (upnp_control.c:106-162)
func (uls *UPNPLocalService) controlParseSoap(hc *httpnet.HTTPConnection,
	envelope *htsmsg.HTSMsg) int {

	methods := envelope.GetMapMulti(
		"tags",
		"http://schemas.xmlsoap.org/soap/envelope/Envelope",
		"tags",
		"http://schemas.xmlsoap.org/soap/envelope/Body",
		"tags")

	if methods == nil {
		return hc.HTTPError(httpnet.HTTPStatusBadRequest,
			"No methods found in envelope")
	}

	for _, f := range methods.GetFields() {
		m := f.GetMap()
		if m == nil {
			continue
		}

		// This parsing is nasty (C comment)
		s := f.GetName()
		if !strings.HasPrefix(s, "urn:schemas-upnp-org:service:") {
			continue
		}
		s = s[len("urn:schemas-upnp-org:service:"):]

		if !strings.HasPrefix(s, uls.name) {
			continue
		}
		s = s[len(uls.name):]

		if len(s) > 0 && s[0] == ':' {
			s = s[1:]
		}
		if len(s) == 0 {
			continue
		}

		ver := int(s[0] - '0')
		s = s[1:]
		if ver > uls.version {
			continue
		}

		for i := range len(uls.methods) {
			if s == uls.methods[i].name {
				return uls.controlDispatchMethod(&uls.methods[i], hc, m)
			}
		}
		// C: reaching the NULL usm_name terminator → method not found
		return hc.HTTPError(500, "Method %s:%d::%s not found",
			uls.name, uls.version, s)
	}
	return hc.HTTPError(httpnet.HTTPStatusBadRequest,
		"Unable to parse methods")
}

// upnpControl is the HTTP handler for /upnp/<svc>/control.
// C: upnp_control (upnp_control.c:168-190)
func upnpControl(hc *httpnet.HTTPConnection, remain string, opaque any,
	method httpnet.HTTPCmd) int {

	uls := opaque.(*UPNPLocalService)
	xml := hc.HTTPGetPostData(nil, true)

	if len(xml) == 0 {
		return hc.HTTPError(httpnet.HTTPStatusBadRequest, "Missing POST data")
	}

	inenv, err := htsmsg.DeserializeXMLBuf(xml)
	if inenv == nil {
		return hc.HTTPError(httpnet.HTTPStatusBadRequest, "%v", err)
	}

	r := uls.controlParseSoap(hc, inenv)
	inenv.Release()
	return r
}
