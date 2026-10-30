package wsd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
)

// WS-Discovery wire format (WS-Discovery 2005/04, DPWS 2006/02,
// MS-PBSD).
// Go: the WS-Discovery client of the Buksa/movian movian6 fork
// (src/sd/wsd.c: wsd_send_probe, wsd_handle_probematch,
// wsd_split_computer) probes Types pub:Computer, which wsdd never
// answers, drops a ProbeMatch without XAddrs, which is all wsdd sends,
// and splits pub:Computer on '/' only. Here the Probe asks for
// wsdp:Device, such a ProbeMatch gets a Resolve, and both separators
// split.

const (
	probeMatchesAction   = "http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches"
	resolveMatchesAction = "http://schemas.xmlsoap.org/ws/2005/04/discovery/ResolveMatches"
	pubNamespace         = "http://schemas.microsoft.com/windows/pub/2005/07"
)

// multicast is the WS-Discovery IPv4 multicast group.
var multicast = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}

// pubComputer is the pub:Computer QName.
var pubComputer = xml.Name{Space: pubNamespace, Local: "Computer"}

// probeMessage is the Probe sent each round. It asks for wsdp:Device,
// which both Windows and wsdd answer.
func probeMessage(messageID string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" ` +
		`xmlns:wsd="http://schemas.xmlsoap.org/ws/2005/04/discovery" ` +
		`xmlns:wsdp="http://schemas.xmlsoap.org/ws/2006/02/devprof" ` +
		`xmlns:pub="` + pubNamespace + `">` +
		`<soap:Header>` +
		`<wsa:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</wsa:To>` +
		`<wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</wsa:Action>` +
		`<wsa:MessageID>` + messageID + `</wsa:MessageID>` +
		`</soap:Header>` +
		`<soap:Body><wsd:Probe><wsd:Types>wsdp:Device</wsd:Types></wsd:Probe></soap:Body>` +
		`</soap:Envelope>`)
}

// resolveMessage is the Resolve that asks endpoint epr for its XAddrs,
// which wsdd leaves out of its ProbeMatch.
func resolveMessage(epr, messageID string) []byte {
	var addr bytes.Buffer
	xml.EscapeText(&addr, []byte(epr))
	return []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" ` +
		`xmlns:wsd="http://schemas.xmlsoap.org/ws/2005/04/discovery">` +
		`<soap:Header>` +
		`<wsa:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</wsa:To>` +
		`<wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Resolve</wsa:Action>` +
		`<wsa:MessageID>` + messageID + `</wsa:MessageID>` +
		`</soap:Header>` +
		`<soap:Body><wsd:Resolve><wsa:EndpointReference><wsa:Address>` + addr.String() + `</wsa:Address>` +
		`</wsa:EndpointReference></wsd:Resolve></soap:Body>` +
		`</soap:Envelope>`)
}

// getMessage is the WS-Transfer Get that asks endpoint epr for its
// metadata.
func getMessage(epr, messageID string) []byte {
	var to bytes.Buffer
	xml.EscapeText(&to, []byte(epr))
	return []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" ` +
		`xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing">` +
		`<soap:Header>` +
		`<wsa:To>` + to.String() + `</wsa:To>` +
		`<wsa:Action>http://schemas.xmlsoap.org/ws/2004/09/transfer/Get</wsa:Action>` +
		`<wsa:MessageID>` + messageID + `</wsa:MessageID>` +
		`<wsa:ReplyTo><wsa:Address>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</wsa:Address></wsa:ReplyTo>` +
		`</soap:Header><soap:Body/></soap:Envelope>`)
}

// message is what a WS-Discovery reply says.
type message struct {
	action         string
	relatesTo      string
	probeMatches   []probeMatch
	resolveMatches []probeMatch
}

// probeMatch is one endpoint in a ProbeMatches or ResolveMatches
// reply.
type probeMatch struct {
	epr             string
	types           []xml.Name // namespace-resolved QNames
	xaddrs          []string
	metadataVersion string
}

// isComputer reports whether the endpoint is a pub:Computer, the type
// Windows and wsdd give a host that shares files. Printers and other
// devices also answer a wsdp:Device Probe.
func (m probeMatch) isComputer() bool {
	return slices.Contains(m.types, pubComputer)
}

// parseMessage parses a WS-Discovery reply.
func parseMessage(b []byte) (message, error) {
	var msg message
	var cur probeMatch
	const (
		probe   = "Envelope/Body/ProbeMatches/ProbeMatch"
		resolve = "Envelope/Body/ResolveMatches/ResolveMatch"
	)
	err := walkXML(b, func(path string, _ xml.Name, text string, ns func(string) string) {
		switch path {
		case "Envelope/Header/Action":
			msg.action = text
		case "Envelope/Header/RelatesTo":
			msg.relatesTo = text
		case probe:
			msg.probeMatches = append(msg.probeMatches, cur)
			cur = probeMatch{}
		case resolve:
			msg.resolveMatches = append(msg.resolveMatches, cur)
			cur = probeMatch{}
		}
		field, ok := strings.CutPrefix(path, probe+"/")
		if !ok {
			field, ok = strings.CutPrefix(path, resolve+"/")
		}
		if !ok {
			return
		}
		switch field {
		case "EndpointReference/Address":
			cur.epr = text
		case "Types":
			for _, q := range strings.Fields(text) {
				prefix, local, ok := strings.Cut(q, ":")
				if !ok {
					prefix, local = "", q
				}
				cur.types = append(cur.types, xml.Name{Space: ns(prefix), Local: local})
			}
		case "XAddrs":
			cur.xaddrs = strings.Fields(text)
		case "MetadataVersion":
			cur.metadataVersion = text
		}
	})
	return msg, err
}

// metadata is what a GetResponse says about a host.
type metadata struct {
	computer     string // pub:Computer
	friendlyName string
}

// parseGetResponse parses the metadata a WS-Transfer Get returns.
func parseGetResponse(b []byte) (metadata, error) {
	var md metadata
	err := walkXML(b, func(path string, name xml.Name, text string, _ func(string) string) {
		switch {
		case name == pubComputer:
			if md.computer == "" {
				md.computer = text
			}
		case strings.HasSuffix(path, "/ThisDevice/FriendlyName"):
			md.friendlyName = text
		}
	})
	return md, err
}

// splitComputer splits a pub:Computer value at its first '/' or '\'
// into the computer name and what follows: "Workgroup:<WG>",
// "Domain:<D>" or "NotJoined". Windows and wsdd send '/'; MS-PBSD
// also allows '\'.
func splitComputer(s string) (name, membership string) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, `/\`); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// maxXMLDepth bounds element nesting. WS-Discovery and DPWS messages
// nest about ten deep; walking a hostile datagram nested thousands
// deep would take about a second of CPU.
const maxXMLDepth = 32

// walkXML decodes b and calls visit as each element ends, with the
// slash-joined local names from the root down to it, its name, its
// trimmed text, and a resolver for the namespace prefixes in scope.
func walkXML(b []byte, visit func(path string, name xml.Name, text string, ns func(prefix string) string)) error {
	type open struct {
		name  xml.Name
		text  strings.Builder
		names map[string]string // prefix → namespace declared here
	}
	var stack []*open
	ns := func(prefix string) string {
		for i := len(stack) - 1; i >= 0; i-- {
			if v, ok := stack[i].names[prefix]; ok {
				return v
			}
		}
		return ""
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			if len(stack) != 0 {
				return fmt.Errorf("smbdisc: unexpected end of XML")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == maxXMLDepth {
				return fmt.Errorf("smbdisc: XML nested deeper than %d", maxXMLDepth)
			}
			o := &open{name: t.Name}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "xmlns":
					o.names = mapSet(o.names, a.Name.Local, a.Value)
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					o.names = mapSet(o.names, "", a.Value)
				}
			}
			stack = append(stack, o)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.EndElement:
			names := make([]string, len(stack))
			for i, o := range stack {
				names[i] = o.name.Local
			}
			o := stack[len(stack)-1]
			visit(strings.Join(names, "/"), o.name, strings.TrimSpace(o.text.String()), ns)
			stack = stack[:len(stack)-1]
		}
	}
}

func mapSet(m map[string]string, k, v string) map[string]string {
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}
