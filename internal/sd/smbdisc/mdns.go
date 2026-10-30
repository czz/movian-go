package smbdisc

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/czz/movian-go/internal/sd/smbdisc/internal/hostname"
	"golang.org/x/net/dns/dnsmessage"
)

// mDNS wire format (RFC 6762, DNS-SD RFC 6763).

const (
	// smbService is the DNS-SD service type SMB servers advertise.
	smbService = "_smb._tcp.local."
	// cacheFlush is the cache-flush bit of a record's class
	// (RFC 6762 §10.2).
	cacheFlush = 0x8000
)

// mdnsQuestionPacket is a one-shot mDNS query (RFC 6762 §5.1) with ID
// id for the records of type qtype called name.
func mdnsQuestionPacket(id uint16, name string, qtype dnsmessage.Type) ([]byte, error) {
	n, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, err
	}
	m := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: id},
		Questions: []dnsmessage.Question{{Name: n, Type: qtype, Class: dnsmessage.ClassINET}},
	}
	return m.Pack()
}

// mdnsResponse is what an mDNS response says about SMB servers: its ID
// and the _smb._tcp records it carries, plus its A records.
type mdnsResponse struct {
	id   uint16
	ptrs []string // the service instances _smb._tcp.local. lists
	srvs []mdnsSRV
	txts []mdnsTXT
	as   []mdnsA
}

// mdnsSRV is the SRV record of a service instance.
type mdnsSRV struct {
	instance string // e.g. "NAS._smb._tcp.local."
	target   string // the host name, e.g. "nas.local."
	port     uint16
}

// mdnsTXT is the TXT record of a service instance.
type mdnsTXT struct {
	instance string
	txt      []string
}

// mdnsA is an A record.
type mdnsA struct {
	host string
	addr netip.Addr
}

// parseMDNSResponse parses an mDNS response. It reads the records of
// the answer and additional sections alike: a legacy unicast answer
// from TrueNAS carries every record in the answer section, while
// RFC 6763 §12 puts SRV, TXT and A in the additional section. Names keep
// the case they came in, with a trailing dot. A label with a dot in it,
// which DNS-SD allows in an instance name, makes the response
// unreadable: dnsmessage refuses it.
func parseMDNSResponse(b []byte) (mdnsResponse, error) {
	var resp mdnsResponse
	var p dnsmessage.Parser
	hdr, err := p.Start(b)
	if err != nil {
		return resp, err
	}
	// RFC 6762 §18.3, §18.11: a message with a non-zero opcode or
	// response code is ignored.
	if !hdr.Response || hdr.OpCode != 0 || hdr.RCode != dnsmessage.RCodeSuccess {
		return resp, fmt.Errorf("smbdisc: mDNS: not a response: response %v, opcode %d, rcode %v",
			hdr.Response, hdr.OpCode, hdr.RCode)
	}
	resp.id = hdr.ID
	if err := p.SkipAllQuestions(); err != nil {
		return resp, err
	}
	sections := []struct {
		header func() (dnsmessage.ResourceHeader, error)
		skip   func() error
	}{
		{p.AnswerHeader, p.SkipAnswer},
		{p.AdditionalHeader, p.SkipAdditional},
	}
	for i, sec := range sections {
		if i == 1 {
			if err := p.SkipAllAuthorities(); err != nil {
				return resp, err
			}
		}
		for {
			rh, err := sec.header()
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				break
			}
			if err != nil {
				return resp, err
			}
			if err := resp.read(&p, rh, sec.skip); err != nil {
				return resp, err
			}
		}
	}
	return resp, nil
}

// read reads the record whose header p has just read, or skips it with
// skip. It keeps the PTR, SRV and TXT records of _smb._tcp instances
// and every A record of an address a server can have, and skips the
// rest.
func (resp *mdnsResponse) read(p *dnsmessage.Parser, rh dnsmessage.ResourceHeader, skip func() error) error {
	owner := rh.Name.String()
	if rh.Class&^cacheFlush != dnsmessage.ClassINET {
		return skip()
	}
	switch {
	case rh.Type == dnsmessage.TypePTR && strings.EqualFold(owner, smbService):
		r, err := p.PTRResource()
		if err != nil {
			return err
		}
		if instance := r.PTR.String(); isSMBInstance(instance) {
			resp.ptrs = append(resp.ptrs, instance)
		}
	case rh.Type == dnsmessage.TypeSRV && isSMBInstance(owner):
		r, err := p.SRVResource()
		if err != nil {
			return err
		}
		if r.Port != 0 {
			resp.srvs = append(resp.srvs, mdnsSRV{instance: owner, target: r.Target.String(), port: r.Port})
		}
	case rh.Type == dnsmessage.TypeTXT && isSMBInstance(owner):
		r, err := p.TXTResource()
		if err != nil {
			return err
		}
		resp.txts = append(resp.txts, mdnsTXT{instance: owner, txt: r.TXT})
	case rh.Type == dnsmessage.TypeA:
		r, err := p.AResource()
		if err != nil {
			return err
		}
		if a := netip.AddrFrom4(r.A); a.IsGlobalUnicast() || a.IsLinkLocalUnicast() || a.IsLoopback() {
			resp.as = append(resp.as, mdnsA{host: owner, addr: a})
		}
	default:
		return skip()
	}
	return nil
}

// isSMBInstance reports whether name is a service instance of type
// _smb._tcp.local.
func isSMBInstance(name string) bool {
	return len(name) > len(smbService)+1 && strings.EqualFold(name[len(name)-len(smbService)-1:], "."+smbService)
}

// mdnsHostName returns the name an SRV target gives its server: the
// first label, which drops ".local", or "" when that is no usable
// name (see hostname.Valid).
func mdnsHostName(target string) string {
	name, _, _ := strings.Cut(target, ".")
	if !hostname.Valid(name) {
		return ""
	}
	return name
}
