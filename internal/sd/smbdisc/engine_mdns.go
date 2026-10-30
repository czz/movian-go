package smbdisc

import (
	"context"
	"crypto/rand"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/sd/smbdisc/lan"
	"golang.org/x/net/dns/dnsmessage"
)

const (
	maxInstances       = 64 // mDNS service instances a round handles
	maxSourceInstances = 16 // of those, instances one address may name
)

// mdnsGroup is the mDNS IPv4 multicast group.
var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// mdnsRun is what one mDNS round has heard.
type mdnsRun struct {
	id        uint16                   // the ID of each query the round sends
	instances map[string]*mdnsInstance // by lower-case instance name
	order     []*mdnsInstance          // in the order heard of
	named     map[netip.Addr]int       // instances each address has named
}

// mdnsInstance is one _smb._tcp service instance heard of in a round.
type mdnsInstance struct {
	name   string     // e.g. "NAS._smb._tcp.local."
	target string     // its SRV target; "" until the SRV record arrives
	port   uint16     // from SRV
	src    netip.Addr // the address the SRV record came from
	addr   netip.Addr // the A record of target picked so far
	listed netip.Addr // the address the instance was last reported at
	txt    []string

	askedSRV, askedA bool
}

// mdnsRound sends a one-shot PTR query for _smb._tcp.local. through
// each interface, on sockets made as wsdRound makes its own, and
// handles the replies until the collection window that opens at start
// closes. Every query of a round, follow-ups included, carries the
// round's random ID, and only replies that repeat it count. Record TTLs
// play no part: a legacy unicast answer caps them at 10 s (RFC 6762
// §6.7), and hosts age by hostTTL instead.
//
// C: the Buksa/movian movian6 fork browses _smb._tcp through
// avahi-daemon (src/sd/avahi.c: avahi_thread, resolve_callback), and
// sd_add_service_smb (src/sd/sd.c) lists each resolved instance under
// its instance name.
// Go: MGO's Avahi and Bonjour backends browse no _smb._tcp, and Windows
// and Android builds have neither, so the engine asks the network
// itself with one-shot queries (RFC 6762 §5.1) from ephemeral ports.
// It names a server after the first label of its SRV target, a DNS host
// label, since an instance name is free text such as "Ann's NAS",
// which hostname.Valid refuses.
func (e *Engine) mdnsRound(ctx context.Context, start time.Time) {
	defer e.wg.Done()
	run := &mdnsRun{id: newMDNSID(), instances: map[string]*mdnsInstance{}, named: map[netip.Addr]int{}}
	conns := e.mdnsQuery(run.id)
	if len(conns) == 0 {
		return
	}

	window := e.clock.After(start.Add(collectWindow).Sub(e.clock.Now()))
	go func() {
		select {
		case <-window:
		case <-ctx.Done():
		}
		for _, conn := range conns {
			conn.Close()
		}
	}()
	var mu sync.Mutex
	var readers sync.WaitGroup
	for _, conn := range conns {
		readers.Go(func() {
			buf := make([]byte, 65536)
			for {
				n, from, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				mu.Lock()
				e.mdnsReply(ctx, run, conn, buf[:n], from)
				mu.Unlock()
			}
		})
	}
	readers.Wait()
	if ctx.Err() != nil {
		return
	}

	// A server whose host name has no A record is at the address its SRV
	// record came from.
	for _, in := range run.order {
		if !in.listed.IsValid() && in.target != "" {
			e.mdnsList(in, in.src)
		}
	}
}

// mdnsQuery sends the PTR query with ID id through every interface
// and returns the sockets it went out on.
func (e *Engine) mdnsQuery(id uint16) []*net.UDPConn {
	q, err := mdnsQuestionPacket(id, smbService, dnsmessage.TypePTR)
	if err != nil {
		e.ts.Debug(traceSubsys, "mDNS: query: %v", err)
		return nil
	}
	ifs, err := e.ifs()
	if err != nil {
		e.ts.Debug(traceSubsys, "mDNS: interfaces: %v", err)
	}
	var conns []*net.UDPConn
	for _, ifc := range ifs {
		conn, err := lan.Listen(ifc)
		if err != nil {
			e.ts.Debug(traceSubsys, "mDNS: %s: bind %v: %v", ifc.Name, ifc.Addr, err)
			continue
		}
		if _, err := conn.WriteToUDP(q, e.mdns); err != nil {
			e.ts.Debug(traceSubsys, "mDNS: %s: query to %v: %v", ifc.Name, e.mdns, err)
			conn.Close()
			continue
		}
		e.ts.Debug(traceSubsys, "mDNS: PTR %s query %#04x sent to %v from %v on %s",
			smbService, id, e.mdns, conn.LocalAddr(), ifc.Name)
		e.sent.Add(1)
		conns = append(conns, conn)
	}
	return conns
}

// mdnsReply handles one datagram received on conn in round run. What
// it leaves missing is asked for through conn: the SRV record of an
// instance with none, then the A record of an SRV target with none.
func (e *Engine) mdnsReply(ctx context.Context, run *mdnsRun, conn *net.UDPConn, b []byte, from *net.UDPAddr) {
	src, ok := netip.AddrFromSlice(from.IP)
	if src = src.Unmap(); !ok || !src.Is4() {
		return
	}
	resp, err := parseMDNSResponse(b)
	if err != nil {
		e.ts.Debug(traceSubsys, "mDNS: %v: unreadable reply: %v", src, err)
		return
	}
	if resp.id != run.id {
		return
	}
	for _, name := range resp.ptrs {
		run.instance(name, src)
	}
	for _, s := range resp.srvs {
		if in := run.instance(s.instance, src); in != nil && in.target == "" {
			in.target, in.port, in.src = s.target, s.port, src
		}
	}
	for _, t := range resp.txts {
		if in := run.instances[strings.ToLower(t.instance)]; in != nil && in.txt == nil {
			in.txt = t.txt
		}
	}
	// The address is the target's A record that is the address its SRV
	// record came from, else its first A record.
	for _, a := range resp.as {
		for _, in := range run.order {
			if in.target != "" && strings.EqualFold(in.target, a.host) && (!in.addr.IsValid() || a.addr == in.src) {
				in.addr = a.addr
			}
		}
	}
	for _, in := range run.order {
		switch {
		case in.listed.IsValid():
			// The preferred A record can come in a later datagram than the
			// one that listed the instance at another of the host's addresses.
			if in.addr.IsValid() && in.addr != in.listed {
				e.mdnsList(in, in.addr)
			}
		case in.target == "":
			if !in.askedSRV {
				in.askedSRV = true
				e.mdnsAsk(conn, run.id, in.name, dnsmessage.TypeSRV)
			}
		case !in.addr.IsValid():
			if !in.askedA {
				in.askedA = true
				e.mdnsAsk(conn, run.id, in.target, dnsmessage.TypeA)
			}
		default:
			e.mdnsList(in, in.addr)
		}
	}
}

// mdnsAsk sends a follow-up query with ID id for the records of type
// qtype called name through conn.
func (e *Engine) mdnsAsk(conn *net.UDPConn, id uint16, name string, qtype dnsmessage.Type) {
	q, err := mdnsQuestionPacket(id, name, qtype)
	if err == nil {
		_, err = conn.WriteToUDP(q, e.mdns)
	}
	if err != nil {
		e.ts.Debug(traceSubsys, "mDNS: %v query for %s: %v", qtype, name, err)
		return
	}
	e.ts.Debug(traceSubsys, "mDNS: %v query for %s sent from %v", qtype, name, conn.LocalAddr())
}

// instance returns the instance called name, which src names. A new
// one is added while the round has fewer than maxInstances and src has
// named fewer than maxSourceInstances; otherwise it returns nil.
//
// Go extension: a round bounds what it keeps, so a device that lists
// many instances grows neither the round's state nor the follow-up
// queries it sends, and cannot take every place from the other servers.
func (run *mdnsRun) instance(name string, src netip.Addr) *mdnsInstance {
	key := strings.ToLower(name)
	in := run.instances[key]
	if in == nil && len(run.order) < maxInstances && run.named[src] < maxSourceInstances {
		in = &mdnsInstance{name: name}
		run.instances[key] = in
		run.order = append(run.order, in)
		run.named[src]++
	}
	return in
}

// mdnsList reports the server behind instance in at addr.
//
// An instance listed again moves the entry that mDNS alone placed at the
// address it was listed at to the new one. An entry WS-Discovery saw at
// its address stays there: that address is the host's own answer, and
// would otherwise flip with every round.
func (e *Engine) mdnsList(in *mdnsInstance, addr netip.Addr) {
	again := in.listed
	in.listed = addr
	name := mdnsHostName(in.target)
	e.ts.Debug(traceSubsys, "mDNS: %s is %s at %v port %d, TXT %q", in.name, in.target, addr, in.port, in.txt)
	e.sight(sighting{from: fromMDNS, name: name, addr: addr, port: int(in.port), again: again})
}

// newMDNSID returns a random non-zero query ID.
func newMDNSID() uint16 {
	for {
		var b [2]byte
		rand.Read(b[:])
		if id := uint16(b[0])<<8 | uint16(b[1]); id != 0 {
			return id
		}
	}
}
