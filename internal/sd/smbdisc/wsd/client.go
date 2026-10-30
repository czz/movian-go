package wsd

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/sd/smbdisc/internal/hostname"
	"github.com/czz/movian-go/internal/sd/smbdisc/lan"
	"github.com/czz/movian-go/internal/trace"
)

const (
	getTimeout   = 2 * time.Second // per WS-Transfer Get
	maxGetBody   = 64 << 10        // GetResponse size cap; Windows sends ~2 KiB
	maxGets      = 4               // concurrent Gets
	maxEndpoints = 256             // WS-Discovery endpoints whose metadata is kept
	maxAnswered  = 256             // WS-Discovery endpoints a round handles
	maxResolves  = 16              // Resolves a round has unanswered at once
	smbPort      = 445
	traceSubsys  = "SMBDISC"

	// metaTTL is how long an endpoint's metadata is kept unseen. It is the
	// host table's TTL (hostTTL in smbdisc): the metadata of a host the
	// table has dropped goes at the same moment.
	metaTTL = 60 * time.Second
)

// Sighting is a WS-Discovery sighting of an SMB host.
type Sighting struct {
	Name string // "" when the host is known by address only
	Addr netip.Addr
	Port int
}

// Client is a WS-Discovery client: each Round probes the LAN and reports
// the hosts that answer. The zero value works; set the fields before the
// first Round.
type Client struct {
	// Dest receives the Probes and Resolves; nil means the multicast
	// group 239.255.255.250:3702.
	Dest *net.UDPAddr
	// HTTP fetches WS-Transfer metadata; nil means a client without a
	// proxy. Redirects are never followed.
	HTTP *http.Client
	// Trace receives the client's debug lines; nil means none.
	Trace *trace.TraceSystem
	// Now is the time source of the metadata aging; nil means time.Now.
	Now func() time.Time

	once sync.Once
	dest *net.UDPAddr
	http *http.Client
	now  func() time.Time
	gets chan struct{} // Get slots
	wg   sync.WaitGroup

	// mu guards meta. It is never held while a round reports.
	mu   sync.Mutex
	meta map[string]*eprMeta // by WS-Discovery endpoint reference
}

// eprMeta is what Gets told about one WS-Discovery endpoint. The
// client keeps one only while a Get for the endpoint is in flight or
// once a Get has named it.
type eprMeta struct {
	version string // MetadataVersion that name was fetched at
	name    string // "" until a Get returns one
	getting bool   // a Get is in flight
	seen    time.Time
}

// pendingResolve is a Resolve a round has sent and had no ResolveMatch
// for.
type pendingResolve struct {
	epr string
	src netip.Addr // the host whose ProbeMatch named epr
}

// init fills in what the fields leave to a default.
func (c *Client) init() {
	c.once.Do(func() {
		c.dest = c.Dest
		if c.dest == nil {
			c.dest = multicast
		}
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		if c.HTTP != nil {
			h := *c.HTTP
			client = &h
		}
		// A redirect could send the Get to another host.
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		c.http = client
		c.now = c.Now
		if c.now == nil {
			c.now = time.Now
		}
		c.gets = make(chan struct{}, maxGets)
		c.meta = map[string]*eprMeta{}
	})
}

// Wait returns once every Get in flight is done. A Get ends when its
// round's context does, so Wait is called after that.
func (c *Client) Wait() { c.wg.Wait() }

// round is one Round's state.
type round struct {
	c         *Client
	ctx       context.Context
	messageID string
	report    func(Sighting)

	mu        sync.Mutex                // serializes the replies
	answered  map[string]bool           // endpoint references handled this round
	resolving map[string]pendingResolve // by Resolve MessageID
}

// Round sends one Probe through each of ifs and handles the replies
// until window fires or ctx ends. It reports each host to report, and
// returns how many Probes it sent; a round that sent none saw nothing.
// The Gets it starts may outlive it; Wait waits for them, and they report
// to report as they end.
//
// C: wsd_send_probe (movian6 src/sd/wsd.c) sends its Probe from one
// socket on the wildcard address, which multicasts out of the
// default-route interface only.
// Go extension: each interface gets a socket on its own address, as
// stpp_netif_update (src/api/stpp.c) gives STPP. The sockets last one
// round, so each round probes the interfaces as they are then. All of
// a round's Probes carry the same MessageID and its replies are handled
// one at a time, so a host heard through two interfaces is taken once.
// A host whose Resolve is still unanswered as the window closes is
// reported by address, and the next round resolves it again. A round
// that sent ends by dropping the metadata no round has seen for
// metaTTL.
func (c *Client) Round(ctx context.Context, window <-chan time.Time, ifs []lan.Interface, report func(Sighting)) (sent int) {
	c.init()
	r := &round{
		c:         c,
		ctx:       ctx,
		messageID: newMessageID(),
		report:    report,
		answered:  map[string]bool{},
		resolving: map[string]pendingResolve{},
	}
	conns := c.probe(r.messageID, ifs)
	if len(conns) == 0 {
		return 0
	}

	go func() {
		select {
		case <-window:
		case <-ctx.Done():
		}
		for _, conn := range conns {
			conn.Close()
		}
	}()
	var readers sync.WaitGroup
	for _, conn := range conns {
		readers.Go(func() {
			buf := make([]byte, 65536)
			for {
				n, from, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				r.mu.Lock()
				r.reply(conn, buf[:n], from)
				r.mu.Unlock()
			}
		})
	}
	readers.Wait()
	for _, p := range r.resolving {
		c.Trace.Debug(traceSubsys, "WS-Discovery: %v: no ResolveMatch for %s, no Get", p.src, p.epr)
		report(Sighting{Addr: p.src, Port: smbPort})
	}
	if ctx.Err() == nil {
		c.age()
	}
	return len(conns)
}

// age drops the metadata of the endpoints no Get is for and no round has
// seen for metaTTL.
func (c *Client) age() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for epr, md := range c.meta {
		if !md.getting && now.Sub(md.seen) >= metaTTL {
			delete(c.meta, epr)
		}
	}
}

// probe sends Probe messageID through every one of ifs and returns the
// sockets it went out on.
func (c *Client) probe(messageID string, ifs []lan.Interface) []*net.UDPConn {
	var conns []*net.UDPConn
	for _, ifc := range ifs {
		conn, err := lan.Listen(ifc)
		if err != nil {
			c.Trace.Debug(traceSubsys, "WS-Discovery: %s: bind %v: %v", ifc.Name, ifc.Addr, err)
			continue
		}
		if _, err := conn.WriteToUDP(probeMessage(messageID), c.dest); err != nil {
			c.Trace.Debug(traceSubsys, "WS-Discovery: %s: Probe to %v: %v", ifc.Name, c.dest, err)
			conn.Close()
			continue
		}
		c.Trace.Debug(traceSubsys, "WS-Discovery: Probe %s sent to %v from %v on %s",
			messageID, c.dest, conn.LocalAddr(), ifc.Name)
		conns = append(conns, conn)
	}
	return conns
}

// reply handles one datagram received on conn in the round.
//
// C: wsd_handle_probematch (movian6 src/sd/wsd.c) drops a repeat by
// looking its endpoint up in wsd_hosts, which has no bound.
// Go extension: a round handles at most maxAnswered endpoints and
// ignores any more, so a device that answers for many endpoints cannot
// grow the round's state.
func (r *round) reply(conn *net.UDPConn, b []byte, from *net.UDPAddr) {
	src, ok := netip.AddrFromSlice(from.IP)
	if src = src.Unmap(); !ok || !src.Is4() {
		return
	}
	msg, err := parseMessage(b)
	if err != nil {
		r.c.Trace.Debug(traceSubsys, "WS-Discovery: %v: unreadable reply: %v", src, err)
		return
	}
	if msg.action == resolveMatchesAction {
		r.resolveMatch(msg, src)
		return
	}
	if msg.action != probeMatchesAction || msg.relatesTo != r.messageID {
		return
	}
	for _, m := range msg.probeMatches {
		if m.epr == "" || r.answered[m.epr] || len(r.answered) >= maxAnswered || !m.isComputer() {
			continue
		}
		r.answered[m.epr] = true
		r.c.Trace.Debug(traceSubsys, "WS-Discovery: %v answered as %s, XAddrs %v, MetadataVersion %s",
			src, m.epr, m.xaddrs, m.metadataVersion)
		if len(r.answered) == maxAnswered {
			r.c.Trace.Debug(traceSubsys, "WS-Discovery: %d endpoints answered this round; any more are ignored", maxAnswered)
		}
		r.match(m, src, conn)
	}
}

// match reports the host behind one ProbeMatch or ResolveMatch. Its
// name comes from a Get, made once per endpoint and again only when the
// endpoint's MetadataVersion changes. A ProbeMatch without XAddrs comes
// with conn, the socket it came in on, and gets a Resolve instead of the
// Get; a ResolveMatch comes with no conn.
//
// C: wsd_handle_probematch (movian6 src/sd/wsd.c) hands every new
// endpoint to task_run, with no bound on the endpoints it keeps.
// Go extension: a Get starts only on a free slot, and for a new
// endpoint only while fewer than maxEndpoints are kept, so a device
// that answers for many endpoints queues no work and grows no state.
// Otherwise the host is reported by address and a later round asks
// again.
func (r *round) match(m probeMatch, src netip.Addr, conn *net.UDPConn) {
	c := r.c
	c.mu.Lock()
	md := c.meta[m.epr]
	if md != nil {
		md.seen = c.now()
	}
	name := ""
	known := md != nil && md.name != "" && md.version == m.metadataVersion
	if known {
		name = md.name
	}
	busy := md != nil && md.getting
	xaddr, noGet, resolve := "", "", false
	if !known && !busy {
		xaddr = sameHostXAddr(m.xaddrs, src)
		switch {
		case len(m.xaddrs) == 0 && conn != nil && len(r.resolving) >= maxResolves:
			noGet = fmt.Sprintf("%d Resolves unanswered already", maxResolves)
		case len(m.xaddrs) == 0 && conn != nil:
			resolve = true
		case xaddr == "":
			noGet = "no XAddr on the host itself"
		case md == nil && len(c.meta) >= maxEndpoints:
			noGet = fmt.Sprintf("%d endpoints kept already", maxEndpoints)
		case !c.takeGetSlot():
			noGet = "every Get slot busy"
		default:
			if md == nil {
				md = &eprMeta{}
				c.meta[m.epr] = md
			}
			md.getting, md.seen = true, c.now()
		}
	}
	c.mu.Unlock()

	switch {
	case known:
		r.report(Sighting{Name: name, Addr: src, Port: smbPort})
	case busy:
		// The Get in flight reports the host.
	case resolve:
		r.resolve(conn, m.epr, src)
	case noGet != "":
		c.Trace.Debug(traceSubsys, "WS-Discovery: %v: %s, no Get", src, noGet)
		r.report(Sighting{Addr: src, Port: smbPort})
	default:
		c.wg.Add(1)
		go c.fetch(r.ctx, r.report, m, xaddr, src)
	}
}

// resolve multicasts a Resolve for endpoint epr, which src named in
// a ProbeMatch without XAddrs, through conn, the socket that ProbeMatch
// came in on, and adds it to the round's Resolves. The ResolveMatch
// comes back to conn by unicast, as the ProbeMatch did.
//
// C: wsd_handle_probematch (movian6 src/sd/wsd.c) drops a ProbeMatch
// without XAddrs, so it never lists a host that runs wsdd.
// Go extension: the Resolve goes out through the interface the
// ProbeMatch came in through, so it reaches the host's LAN; wsdd
// answers with an XAddr on the address the Resolve reached it at. A
// round has at most maxResolves Resolves unanswered, and match
// reports a host past that by address, so a device that answers for
// many endpoints without XAddrs cannot make the client multicast a
// Resolve for each.
func (r *round) resolve(conn *net.UDPConn, epr string, src netip.Addr) {
	messageID := newMessageID()
	if _, err := conn.WriteToUDP(resolveMessage(epr, messageID), r.c.dest); err != nil {
		r.c.Trace.Debug(traceSubsys, "WS-Discovery: %v: Resolve to %v: %v", src, r.c.dest, err)
		r.report(Sighting{Addr: src, Port: smbPort})
		return
	}
	r.resolving[messageID] = pendingResolve{epr: epr, src: src}
	r.c.Trace.Debug(traceSubsys, "WS-Discovery: %v sent no XAddrs; Resolve %s for %s sent to %v from %v",
		src, messageID, epr, r.c.dest, conn.LocalAddr())
}

// resolveMatch handles a ResolveMatches reply from src. It answers
// a Resolve in the round's Resolves only when it relates to that Resolve,
// comes from the host whose ProbeMatch the Resolve was for, and names the
// endpoint resolved; the match then gets the Get a ProbeMatch with
// XAddrs gets. A repeat finds its Resolve answered already.
func (r *round) resolveMatch(msg message, src netip.Addr) {
	p, ok := r.resolving[msg.relatesTo]
	if !ok || src != p.src {
		return
	}
	for _, m := range msg.resolveMatches {
		if m.epr != p.epr {
			continue
		}
		delete(r.resolving, msg.relatesTo)
		r.c.Trace.Debug(traceSubsys, "WS-Discovery: %v resolved %s, XAddrs %v, MetadataVersion %s",
			src, m.epr, m.xaddrs, m.metadataVersion)
		r.match(m, src, nil)
		return
	}
}

// takeGetSlot takes one of the maxGets Get slots if one is free;
// fetch gives it back.
func (c *Client) takeGetSlot() bool {
	select {
	case c.gets <- struct{}{}:
		return true
	default:
		return false
	}
}

// sameHostXAddr returns the first http XAddr whose host is src. Gets go
// only to the host that answered, so no LAN device can point the client
// at another address.
func sameHostXAddr(xaddrs []string, src netip.Addr) string {
	for _, x := range xaddrs {
		u, err := url.Parse(x)
		if err != nil || u.Scheme != "http" || u.User != nil {
			continue
		}
		if a, err := netip.ParseAddr(u.Hostname()); err == nil && a.Unmap() == src {
			return x
		}
	}
	return ""
}

// fetch gets the metadata of m's endpoint from xaddr on the Get slot
// match took, then reports the host by name, or by address when the
// Get fails or names nothing; the next round then tries again.
func (c *Client) fetch(ctx context.Context, report func(Sighting), m probeMatch, xaddr string, src netip.Addr) {
	defer c.wg.Done()
	name, err := c.getName(ctx, m.epr, xaddr)
	<-c.gets
	c.mu.Lock()
	md := c.meta[m.epr]
	md.getting = false
	switch {
	case name != "":
		md.name, md.version = name, m.metadataVersion
	case md.name == "":
		delete(c.meta, m.epr)
	}
	c.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.Trace.Debug(traceSubsys, "WS-Discovery: %v: Get %s: %v", src, xaddr, err)
	}
	report(Sighting{Name: name, Addr: src, Port: smbPort})
}

// getName runs a WS-Transfer Get on xaddr and returns the computer
// name from its pub:Computer.
func (c *Client) getName(ctx context.Context, epr, xaddr string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, getTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, xaddr,
		bytes.NewReader(getMessage(epr, newMessageID())))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/soap+xml; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxGetBody+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxGetBody {
		return "", fmt.Errorf("GetResponse over %d bytes", maxGetBody)
	}
	info, err := parseGetResponse(body)
	if err != nil {
		return "", err
	}
	name, membership := splitComputer(info.computer)
	if !hostname.Valid(name) {
		return "", fmt.Errorf("no usable name in pub:Computer %q", info.computer)
	}
	c.Trace.Debug(traceSubsys, "WS-Discovery: %s is %s (%s)", xaddr, name, membership)
	return name, nil
}

// newMessageID returns a fresh WS-Addressing MessageID.
func newMessageID() string { return "urn:uuid:" + newUUID() }

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
