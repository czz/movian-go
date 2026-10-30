package smbdisc

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/sd/smbdisc/internal/hostname"
	"github.com/czz/movian-go/internal/trace"
)

const hostTTL = 60 * time.Second // an entry no source has seen this long goes

// listing is what the host table changes on Local network. An entry's
// handle stands for it from add to remove: during a fold or an address
// takeover two entries pass through the same id, so the id cannot.
type listing interface {
	// add lists a new entry and returns its handle, which is not nil.
	add(id, title, url string) listingHandle
	// setURL changes the URL of the entry h stands for; id and settings stay.
	setURL(h listingHandle, url string)
	// remove takes the entry h stands for off Local network.
	remove(h listingHandle)
}

// listingHandle is opaque to the host table.
type listingHandle any

// hostTable owns the host entries and the rules that merge what the
// sources report into them, and keeps the listing in step.
//
// It is synchronous and has no mutex, context or clock: the caller
// serializes calls, checks its context and passes the time. It calls the
// listing in the order the registry needs, an entry removed before
// another takes its id.
type hostTable struct {
	list    listing
	ts      *trace.TraceSystem
	hosts   map[netip.Addr]*host
	netbios map[string]*host // the hosts NetBIOS lists, by netbiosName
}

func newHostTable(list listing, ts *trace.TraceSystem) *hostTable {
	return &hostTable{
		list:    list,
		ts:      ts,
		hosts:   map[netip.Addr]*host{},
		netbios: map[string]*host{},
	}
}

// host is one SMB server.
type host struct {
	addr     netip.Addr // invalid while only NetBIOS lists it
	port     int
	name     string          // "" until a source reports one
	nameBy   source          // the source that reported name
	aliases  map[string]bool // every name a source has reported for it, as netbiosName writes it
	nbName   string          // the name NetBIOS lists it by; "" while it does not
	nbGroups map[string]bool // the workgroups NetBIOS lists it in
	seen     time.Time       // when a source last saw its address

	// What the host table has listed for it, which publish compares with.
	handle   listingHandle // nil while unlisted
	listedID string
	listedAs string // the title
	listedAt string // the URL
}

// sighting is a source's sighting of a host.
type sighting struct {
	from  source
	name  string // "" when unknown
	addr  netip.Addr
	port  int
	again netip.Addr // the address the same mDNS instance was listed at before, if any
}

// source is where a sighting comes from. Sources are listed in the
// order their names are preferred: a host keeps the name of the most
// preferred source that reported one. NetBIOS names a host apart in
// nbName, and title ranks it between the two.
type source int

const (
	fromWSD  source = iota // WS-Discovery: the pub:Computer name
	fromMDNS               // mDNS: the host name of the SRV target
)

// sight merges a sighting into the host entries and brings Local
// network up to date.
func (t *hostTable) sight(o sighting, now time.Time) {
	h := t.hosts[o.addr]
	// A sighting that lists an mDNS instance at a new address leaves a host
	// WS-Discovery named where it is.
	if prev := t.hosts[o.again]; prev != nil && prev.nameBy == fromWSD {
		return
	}
	// Go extension: a multihomed server WS-Discovery named answers mDNS at
	// its other addresses too. Such a sighting, under a name WS-Discovery's
	// host has been called by, is dropped while that host is current:
	// WS-Discovery's address is the host's own answer.
	if w := t.wsdHostCalled(o, now); w != nil {
		t.ts.Debug(traceSubsys, "%s: %s at %v is another address of %s", o.name, w.title(), o.addr, w.addr)
		return
	}
	var left *host // the NetBIOS-listed host that o's computer displaced
	if o.name != "" && h.takesName(o) {
		named := t.hostNamed(o.name)
		if nb := t.netbios[netbiosName(o.name)]; named == nil && nb != nil && !nb.addr.IsValid() {
			// A server NetBIOS lists by name alone gains the address.
			t.ts.Debug(traceSubsys, "%s: NetBIOS name now at %v", nb.title(), o.addr)
			named = nb
		}
		if named != nil && named != h {
			// The same computer at a new address keeps its entry and id;
			// whatever was listed at the new address folds into it.
			moved := named.addr.IsValid()
			if moved {
				t.ts.Debug(traceSubsys, "%s moved from %v to %v", named.name, named.addr, o.addr)
			}
			if h != nil {
				t.loseAddress(h, named.title()+" is there now")
				// A host that had no address is the same computer as what
				// was listed here, so it keeps that entry's port and the
				// names it was known by. A host that moved here displaced
				// another computer, and keeps its own.
				if !moved {
					named.port = h.port
					for a := range h.aliases {
						named.addAlias(a)
					}
				}
			}
			delete(t.hosts, named.addr)
			named.addr = o.addr
			t.hosts[o.addr] = named
			h = named
		} else if named == nil && h != nil && h.name != "" && o.from == h.nameBy && !strings.EqualFold(h.name, o.name) {
			// Go extension: the source that named the host at this address
			// now gives another name no source has listed, so another
			// computer has taken the address. It does not inherit the old
			// one's port; and a host NetBIOS lists stays, by name, while
			// the entry at the address is the new one's. A more preferred
			// source naming the host differently is the same computer.
			h.port, h.aliases = 0, nil
			if h.nbName != "" && netbiosName(o.name) != h.nbName {
				t.ts.Debug(traceSubsys, "%s: %s is at %v now; NetBIOS lists %s, so it stays by name",
					h.nbName, o.name, o.addr, h.nbName)
				left = &host{nbName: h.nbName, nbGroups: h.nbGroups}
				t.netbios[left.nbName] = left
				h.nbName, h.nbGroups = "", nil
			}
		}
	}
	if h == nil {
		h = &host{addr: o.addr}
		t.hosts[o.addr] = h
	}
	if o.name != "" {
		a := netbiosName(o.name)
		h.addAlias(a)
		t.foldNameOnly(h, a)
	}
	if o.name != "" && h.takesName(o) {
		h.name, h.nameBy = o.name, o.from
	}
	// Only mDNS reads the port off the server. WS-Discovery tells none,
	// so its 445 sets the port only of a host that has none yet.
	if h.port == 0 || o.from == fromMDNS {
		h.port = o.port
	}
	h.seen = now
	t.publish(h)
	if left != nil {
		t.publish(left) // after h, which may have just freed its id
	}
}

// wsdHostCalled returns the host WS-Discovery named, at another address
// than o's and seen within hostTTL, that o's mDNS name is an alias of. A
// name some host bears itself is not an alias. A name netbiosName
// shortens is no proof of the host: other names share its key, so it
// matches nothing.
func (t *hostTable) wsdHostCalled(o sighting, now time.Time) *host {
	if o.from != fromMDNS || o.name == "" || t.hostNamed(o.name) != nil {
		return nil
	}
	a := netbiosName(o.name)
	if label, _, _ := strings.Cut(o.name, "."); len([]rune(label)) > len([]rune(a)) {
		return nil
	}
	for _, h := range t.hosts {
		if h.name != "" && h.nameBy == fromWSD && h.addr != o.addr && h.aliases[a] && now.Sub(h.seen) < hostTTL {
			return h
		}
	}
	return nil
}

// netbiosFound handles NetBIOS listing the server name in workgroup.
//
// Go extension: the server joins the host entries instead of getting a
// service of its own. A listed host of that name takes it; otherwise
// the entry is the name alone, at smb://<NAME>, until another source
// sees that name at an address.
func (t *hostTable) netbiosFound(name, workgroup string) {
	n := netbiosName(name)
	if !hostname.Valid(n) {
		t.ts.Debug(traceSubsys, "NetBIOS: no usable name in %q", name)
		return
	}
	h := t.netbios[n]
	if h == nil {
		if h = t.hostNamedForNetBIOS(n); h == nil {
			h = &host{}
		}
		h.nbName, h.nbGroups = n, map[string]bool{}
		t.netbios[n] = h
		t.ts.Debug(traceSubsys, "NetBIOS lists %s in %s", n, workgroup)
	}
	h.nbGroups[strings.ToUpper(workgroup)] = true
	t.publish(h)
}

// netbiosLost handles NetBIOS no longer listing the server name in
// workgroup. Once no workgroup lists it, its entry stays only while
// another source has seen it within hostTTL.
//
// Go extension: NetBIOS's own mark-and-sweep (query_master_browser,
// nmb.c) ages what NetBIOS reports; the engine ages only what it sees
// itself.
func (t *hostTable) netbiosLost(name, workgroup string, now time.Time) {
	n := netbiosName(name)
	h := t.netbios[n]
	if h == nil {
		return
	}
	delete(h.nbGroups, strings.ToUpper(workgroup))
	if len(h.nbGroups) > 0 {
		return
	}
	delete(t.netbios, n)
	h.nbName, h.nbGroups = "", nil
	if h.addr.IsValid() && now.Sub(h.seen) < hostTTL {
		t.ts.Debug(traceSubsys, "%s: NetBIOS lists it no more; seen at %v", h.title(), h.addr)
		t.publish(h)
		return
	}
	t.ts.Debug(traceSubsys, "%s: NetBIOS lists it no more, removed", n)
	t.unpublish(h)
	if h.addr.IsValid() {
		delete(t.hosts, h.addr)
	}
}

// age drops the hosts no source has seen for hostTTL.
func (t *hostTable) age(now time.Time) {
	for addr, h := range t.hosts {
		if now.Sub(h.seen) < hostTTL {
			continue
		}
		delete(t.hosts, addr)
		t.loseAddress(h, fmt.Sprintf("not seen for %v", now.Sub(h.seen)))
	}
}

// foldNameOnly folds into h the entry NetBIOS lists by the name a alone,
// once h, which NetBIOS does not list yet, has gained that name: h takes
// its NetBIOS name and workgroups, and the name-only service goes.
func (t *hostTable) foldNameOnly(h *host, a string) {
	nb := t.netbios[a]
	if nb == nil || nb == h || nb.addr.IsValid() || h.nbName != "" || !h.addr.IsValid() {
		return
	}
	t.ts.Debug(traceSubsys, "%s: NetBIOS name now at %v", nb.title(), h.addr)
	t.unpublish(nb) // before h is published, which may take its id
	h.nbName, h.nbGroups = nb.nbName, nb.nbGroups
	t.netbios[a] = h
}

// takesName reports whether o names h: h is new or has no name yet, or
// o's source is preferred to, or is, the one that named it.
func (h *host) takesName(o sighting) bool {
	return h == nil || h.name == "" || o.from <= h.nameBy
}

// addAlias records that a source has called h name, which is in the form
// netbiosName returns.
func (h *host) addAlias(name string) {
	if h.aliases == nil {
		h.aliases = map[string]bool{}
	}
	h.aliases[name] = true
}

// hostNamed returns the host called name, if any.
func (t *hostTable) hostNamed(name string) *host {
	for _, h := range t.hosts {
		if strings.EqualFold(h.name, name) {
			return h
		}
	}
	return nil
}

// hostNamedForNetBIOS returns a host with an address that a source has
// called n, as NetBIOS writes it, unless NetBIOS lists that host already.
// A host keeps every name it was reported under, so one that the sources
// name differently is still found by the name NetBIOS lists it by.
func (t *hostTable) hostNamedForNetBIOS(n string) *host {
	for _, h := range t.hosts {
		if h.nbName == "" && h.aliases[n] {
			return h
		}
	}
	return nil
}

// publish makes h's entry match h: listed on first sight, listed anew
// when its title changes because a name became known or a more preferred
// source renamed it, and its URL updated in place when only the address
// or port changed. It compares with what it listed, not with the listing.
//
// Go: a managed service's title is bound to its Name setting, whose
// default is the title it was created with, so a new title means a new
// service. Its settings are stored by id, so a rename that keeps the id
// keeps them.
func (t *hostTable) publish(h *host) {
	id, url := h.serviceID(), h.url()
	if h.handle != nil && h.listedID == id && h.listedAs == h.title() {
		if h.listedAt != url {
			t.ts.Debug(traceSubsys, "%s: URL now %s", id, url)
			t.list.setURL(h.handle, url)
			h.listedAt = url
		}
		return
	}
	t.unpublish(h)
	t.ts.Debug(traceSubsys, "%s: listed as %s at %s", id, h.title(), url)
	h.handle = t.list.add(id, h.title(), url)
	h.listedID, h.listedAs, h.listedAt = id, h.title(), url
}

// unpublish removes h's entry from Local network.
func (t *hostTable) unpublish(h *host) {
	if h.handle != nil {
		t.list.remove(h.handle)
		h.handle = nil
	}
}

// loseAddress handles h, already out of the hosts by address, once no
// source vouches for its address: a host NetBIOS lists stays, by name,
// and any other leaves Local network.
func (t *hostTable) loseAddress(h *host, why string) {
	if h.nbName != "" {
		t.ts.Debug(traceSubsys, "%s at %v: %s; NetBIOS lists it, so it stays by name", h.title(), h.addr, why)
		h.addr, h.port = netip.Addr{}, 0 // the port was the address's evidence too
		t.publish(h)
		return
	}
	t.ts.Debug(traceSubsys, "%s at %v: %s, removed", h.title(), h.addr, why)
	t.unpublish(h)
}

// title is the host's name: the WS-Discovery name, else the NetBIOS
// name, else the mDNS name, else the address.
func (h *host) title() string {
	switch {
	case h.name != "" && h.nameBy == fromWSD:
		return h.name
	case h.nbName != "":
		return h.nbName
	case h.name != "":
		return h.name
	}
	return h.addr.String()
}

// url is the share-browsing URL of the host: by address once a source
// has given one, by NetBIOS name until then.
func (h *host) url() string {
	if !h.addr.IsValid() {
		return "smb://" + h.nbName
	}
	return smbURL(h.addr, h.port)
}

func (h *host) serviceID() string { return "smb/" + strings.ToUpper(h.title()) }

// smbURL is the share-browsing URL of a server: the address that
// answered, with the port only when it is not 445.
func smbURL(addr netip.Addr, port int) string {
	if port == smbPort {
		return "smb://" + addr.String()
	}
	return "smb://" + netip.AddrPortFrom(addr, uint16(port)).String()
}

// netbiosName is name as NetBIOS writes it, the form names merge in:
// upper case, the first DNS label (so no .local), at most 15
// characters.
func netbiosName(name string) string {
	name, _, _ = strings.Cut(name, ".")
	name = strings.ToUpper(name)
	if r := []rune(name); len(r) > 15 {
		name = string(r[:15])
	}
	return name
}
