// fa_http3.go — Go extension (no upstream C counterpart): HTTP/3 over
// QUIC (RFC 9000/9114) via quic-go, layered on the modern https path
// from fa_http2.go.
//
// Gated by gconf.FAHTTP3 ("Use HTTP/3 (QUIC) for HTTPS" setting in
// settings:http, default on). QUIC is attempted per host inside
// h2RoundTrip before TCP: a successful handshake marks the host
// h3-capable; a failure negative-caches it for the process lifetime —
// UDP-blocked networks pay one bounded handshake (~4s,
// HandshakeIdleTimeout) per host, then stay on TCP. No Alt-Svc
// handling: QUIC is attempted directly on :443.

package fileaccess

import (
	"crypto/tls"
	"net/http"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// h3Pool — fam-owned QUIC/HTTP3 state: shared *http3.Transport per
// TLS-verify mode plus the per-host capability cache. hosts[host]:
// true = QUIC worked, false = handshake failed → TCP only for the
// process lifetime (documented: no TTL — a permanently blocked UDP
// path must not cost a handshake timeout on every request).
type h3Pool struct {
	mu    sync.Mutex
	ts    map[bool]*http3.Transport
	hosts map[string]bool
}

// transport lazily builds the shared HTTP/3 transport.
func (p *h3Pool) transport(verify bool) *http3.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ts == nil {
		p.ts = make(map[bool]*http3.Transport)
	}
	if t := p.ts[verify]; t != nil {
		return t
	}
	t := &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verify},
		QUICConfig: &quic.Config{
			// Bounded so UDP-blocked networks fail fast into the
			// negative cache rather than stalling the open.
			HandshakeIdleTimeout: 4 * time.Second,
		},
	}
	p.ts[verify] = t
	return t
}

// try reports whether QUIC should be attempted for host (host:port).
func (p *h3Pool) try(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok, seen := p.hosts[host]
	return !seen || ok
}

// mark records the QUIC outcome for host.
func (p *h3Pool) mark(host string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hosts == nil {
		p.hosts = make(map[string]bool)
	}
	p.hosts[host] = ok
}

// h2RoundTrip — QUIC/HTTP3 first for hosts not negative-cached, then
// TCP (h2/h1). Fallback happens only on RoundTrip failure (pre-headers
// by definition — an error means no response was received).
func (hf *h2File) h2RoundTrip(req *http.Request) (*http.Response, error) {
	fam := hf.fam
	if fam.Gconf().FAHTTP3.Load() && fam.h3.try(req.URL.Host) {
		if resp, err := fam.h3.transport(hf.verify).RoundTrip(req); err == nil {
			fam.h3.mark(req.URL.Host, true)
			return resp, nil
		} else {
			fam.h3.mark(req.URL.Host, false)
			hf.h2Trace("QUIC failed for %s: %v — TCP fallback",
				req.URL.Host, err)
		}
	}
	return hf.client.Do(req)
}
