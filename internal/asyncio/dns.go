package asyncio

// C: src/networking/asyncio_posix.c — DNS section
// (asyncio_posix.c:1238-1311)

import (
	netcore "github.com/czz/movian-go/internal/networking/core"
)

// C: ASYNCIO_DNS_STATUS_* (asyncio.h:178-181) — enum in C
type DNSStatus int

const (
	DNSStatusQueued    DNSStatus = 1 // C: ASYNCIO_DNS_STATUS_QUEUED
	DNSStatusPending   DNSStatus = 2 // C: ASYNCIO_DNS_STATUS_PENDING
	DNSStatusCompleted DNSStatus = 3 // C: ASYNCIO_DNS_STATUS_COMPLETED
	DNSStatusFailed    DNSStatus = 4 // C: ASYNCIO_DNS_STATUS_FAILED
)

// C: typedef asyncio_dns_callback (asyncio.h:183-186) — data is
// *netcore.NetAddr on completion, errmsg string on failure
type DNSCallback func(opaque any, status DNSStatus, data any)

// C: struct asyncio_dns_req (asyncio_posix.c:1238-1249)
type DNSReq struct {
	hostname  string // C: adr_hostname
	opaque    any
	cb        DNSCallback     // C: adr_cb
	status    DNSStatus       // C: adr_status
	cancelled bool            // C: adr_cancelled
	data      any             // C: adr_data — *NetAddr or errmsg
	addr      netcore.NetAddr // C: adr_addr
}

// dnsState — C: static hts_mutex_t asyncio_dns_mutex;
// static int asyncio_dns_worker;
// static struct asyncio_dns_req_queue asyncio_dns_pending/completed;
// static int adr_resolver_running (asyncio_posix.c — single resolver
// queue per process).
// dnsMu/dnsPending/dnsCompleted/dnsResolverRun/dnsWorker live on
// AsyncIO (C: static asyncio_dns_mutex + queue globals).

// C: static int adr_resolve (asyncio_posix.c:1257-1260)
func adrResolve(adr *DNSReq) error {
	na, err := netcore.NetResolve(adr.hostname) // C: net_resolve
	if err != nil {
		return err
	}
	adr.addr = *na
	return nil
}

// C: static void *adr_resolver (asyncio_posix.c:1266-1289)
func (aio *AsyncIO) adrResolver() {
	aio.dns.mu.Lock()
	for len(aio.dns.pending) > 0 {
		adr := aio.dns.pending[0]
		aio.dns.pending = aio.dns.pending[1:]

		aio.dns.mu.Unlock()

		if err := adrResolve(adr); err != nil {
			adr.status = DNSStatusFailed
			adr.data = err.Error() // C: adr_data = adr_errmsg
		} else {
			adr.status = DNSStatusCompleted
			adr.data = &adr.addr
		}
		aio.dns.mu.Lock()
		aio.dns.completed = append(aio.dns.completed, adr)
		aio.WakeupWorker(aio.dns.worker)
	}
	aio.dns.resolverRun = false
	aio.dns.mu.Unlock()
}

// C: asyncio_dns_req_t *asyncio_dns_lookup_host (asyncio_posix.c:1295-1316)
func (aio *AsyncIO) DNSLookupHost(hostname string, cb DNSCallback,
	opaque any) *DNSReq {
	adr := &DNSReq{
		hostname: hostname, // C: strdup
		cb:       cb,
		opaque:   opaque,
		status:   DNSStatusQueued,
	}

	aio.dns.mu.Lock()
	aio.dns.pending = append(aio.dns.pending, adr)
	if !aio.dns.resolverRun {
		aio.dns.resolverRun = true
		go aio.adrResolver() // C: hts_thread_create_detached("DNS resolver", ...)
	}
	aio.dns.mu.Unlock()
	return adr
}

// C: static void adr_deliver_cb (asyncio_posix.c:1283-1300)
func (aio *AsyncIO) adrDeliverCb() {
	aio.dns.mu.Lock()
	for len(aio.dns.completed) > 0 {
		adr := aio.dns.completed[0]
		aio.dns.completed = aio.dns.completed[1:]
		aio.dns.mu.Unlock()
		if !adr.cancelled {
			adr.cb(adr.opaque, adr.status, adr.data)
		}
		aio.dns.mu.Lock()
	}
	aio.dns.mu.Unlock()
}

// C: void asyncio_dns_cancel (asyncio_posix.c:1306-1311)
func DNSCancel(adr *DNSReq) {
	// C: asyncio_verify_thread()
	adr.cancelled = true
}

// Cancel — method form used by canonical-style callers
func (adr *DNSReq) Cancel() { DNSCancel(adr) }
