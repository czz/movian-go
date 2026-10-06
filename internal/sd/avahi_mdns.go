//go:build linux && !android && !avahicgo

package sd

// avahi_mdns.go — pure-Go mDNS variant of the avahi backend (Phase 10
// experiment). Browses _webdav._tcp and _htsp._tcp over
// github.com/grandcat/zeroconf instead of libavahi-client. Default backend on linux/desktop; the canonical cgo
// backend is selected with -tags avahicgo.
//
// Mapping vs avahi.go:
//   AvahiSimplePoll thread        → one goroutine + select over two
//                                   browse channels + a TTL reaper
//   service_browser + resolver    → zeroconf.Browse (browse+resolve are
//                                   merged: each ServiceEntry is already
//                                   resolved)
//   AVAHI_BROWSER_REMOVE          → TTL expiry — zeroconf does not
//                                   surface mDNS goodbye packets, so
//                                   removal is approximated by expiring
//                                   entries not re-announced within TTL
//   AVAHI_RESOLVER_FAILURE        → entry with no usable address
//   avahi_client_set_host_name    → no-op (no daemon to rename; STOS/
//                                   mgos path)
//   airplay publish (enableAirplay=false upstream) → omitted

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/czz/movian-go/internal/trace"
)

// avahiDaemon — same role as the cgo struct in avahi.go (C: avahi.c
// file statics): owns the browse state for the sd System.
type avahiDaemon struct {
	sys      *System
	services []*serviceInstance
	seen     map[*serviceInstance]seenEntry // TTL bookkeeping
}

// seenEntry tracks the last announcement and TTL of a service so the
// reaper can emulate AVAHI_BROWSER_REMOVE.
type seenEntry struct {
	last time.Time
	ttl  time.Duration
}

// txtFind — C: avahi_string_list_find + avahi_string_list_get_pair.
// zeroconf delivers TXT records as "key=value" strings.
func txtFind(txt []string, key string) string {
	prefix := key + "="
	for _, kv := range txt {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// entryFullname — key for a browsed service, counterpart of C's
// "%s.%s.%s.%d.%d" (name.type.domain.iface.protocol). zeroconf does not
// expose iface/protocol — instance.service.domain is unambiguous here.
func entryFullname(e *zeroconf.ServiceEntry) string {
	return fmt.Sprintf("%s.%s.%s", e.Instance, e.Service, e.Domain)
}

// entryAddr — pick the preferred address (C resolves with
// AVAHI_PROTO_INET → IPv4 first, then IPv6).
func entryAddr(e *zeroconf.ServiceEntry) string {
	if len(e.AddrIPv4) > 0 {
		return e.AddrIPv4[0].String()
	}
	if len(e.AddrIPv6) > 0 {
		return e.AddrIPv6[0].String()
	}
	return ""
}

// entryFound — merged C browserCallback(NEW) + resolveCallback(FOUND):
// zeroconf entries arrive already resolved.
func (d *avahiDaemon) entryFound(class int, e *zeroconf.ServiceEntry) {
	fullname := entryFullname(e)
	addr := entryAddr(e)

	if addr == "" {
		// C: AVAHI_RESOLVER_FAILURE → si_destroy
		d.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
			"Failed to resolve service '%s' of type '%s' in domain '%s'\n",
			e.Instance, e.Service, e.Domain)
		if si := siFind(d.services, fullname); si != nil {
			d.sys.siDestroy(&d.services, si)
			delete(d.seen, si)
		}
		return
	}

	d.sys.ts.Trace(trace.TRACE_DEBUG, "AVAHI",
		"Found service '%s' of type '%s' at %s:%d (%s)",
		e.Instance, e.Service, addr, e.Port, e.HostName)

	si := siFind(d.services, fullname)
	if si == nil {
		si = &serviceInstance{id: fullname}
		// C: LIST_INSERT_HEAD(&services, si, si_link)
		d.services = append([]*serviceInstance{si}, d.services...)
	}
	d.seen[si] = seenEntry{last: time.Now(),
		ttl: time.Duration(e.TTL) * time.Second}

	switch class {
	case ServiceHtsp:
		d.sys.sdAddServiceHtsp(si, e.Instance, addr, e.Port)

	case ServiceWebdav:
		// C: avahi_string_list_find(txt, "path"/"contents")
		d.sys.sdAddServiceWebdav(si, e.Instance, addr, e.Port,
			txtFind(e.Text, "path"), txtFind(e.Text, "contents"))
	}
}

// reap — C: AVAHI_BROWSER_REMOVE, approximated by TTL expiry.
func (d *avahiDaemon) reap() {
	now := time.Now()
	for si, se := range d.seen {
		if now.Sub(se.last) > se.ttl {
			d.sys.siDestroy(&d.services, si)
			delete(d.seen, si)
		}
	}
}

// browseType — C: service_type_add. Runs one zeroconf browse for the
// service type, forwarding resolved entries into the daemon loop.
func (d *avahiDaemon) browseType(ctx context.Context, service string,
	class int, ch chan<- *zeroconf.ServiceEntry) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		d.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
			"Failed to create resolver: %s", err)
		return
	}
	if err := resolver.Browse(ctx, service, "local.", ch); err != nil {
		d.sys.ts.Trace(trace.TRACE_ERROR, "AVAHI",
			"Failed to browse %s: %s", service, err)
	}
}

// mdnsLoop — C: avahi_thread (avahi.c:423-453): one goroutine consuming
// both browse channels + the TTL reaper — the counterpart of the
// single-threaded avahi_simple_poll loop.
func (d *avahiDaemon) mdnsLoop() {
	ctx := context.Background()
	webdavCh := make(chan *zeroconf.ServiceEntry)
	htspCh := make(chan *zeroconf.ServiceEntry)
	go d.browseType(ctx, "_webdav._tcp", ServiceWebdav, webdavCh)
	go d.browseType(ctx, "_htsp._tcp", ServiceHtsp, htspCh)

	// C: enableAirplay == false → no publish client (ENABLE_AIRPLAY
	// compiled out upstream).

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case e := <-webdavCh:
			if e != nil {
				d.entryFound(ServiceWebdav, e)
			}
		case e := <-htspCh:
			if e != nil {
				d.entryFound(ServiceHtsp, e)
			}
		case <-ticker.C:
			d.reap()
		}
	}
}

// AvahiUpdateHostname — C: avahi_update_hostname (avahi.c:443-451),
// #if STOS. No-op under pure-Go mDNS: there is no avahi daemon whose
// host name can be re-announced.
func (s *System) AvahiUpdateHostname() {}

// avahiStart — C: avahi_init (avahi.c:459-467).
func (s *System) avahiStart() {
	a := &avahiDaemon{sys: s, seen: map[*serviceInstance]seenEntry{}}
	s.avahi = a
	// C: hts_thread_create_detached("AVAHI", avahi_thread, NULL,
	//                             THREAD_PRIO_BGTASK)
	go a.mdnsLoop()
}
