package fileaccess

// Canonical port of src/networking/asyncio_http.c.
//
// Bridges the async http_req machinery with asyncio workers: request
// completion callbacks are queued on a list and delivered on the
// asyncio thread (a dispatcher goroutine here).
//
// Lives in this package because http_reqv/HTTPReqAux are defined here;
// pkg/asyncio is a leaf so there is no import cycle.

import (
	"slices"
	"sync/atomic"

	"github.com/czz/movian-go/internal/asyncio"
)

// AsyncioHTTPReq — C: asyncio_http_req_t (asyncio_http.c:35-42)
type AsyncioHTTPReq struct {
	fam       *FileAccessManager
	cancelled int32                             // ahr_cancelled
	req       *HTTPReqAux                       // ahr_req
	cb        func(hra *HTTPReqAux, opaque any) // ahr_cb
	opaque    any                               // ahr_opaque
}

// C's asyncio_http file statics live in FileAccessManager.asyncioHTTP.

// asyncioHTTPCb — C: asyncio_http_cb (asyncio_http.c:49-61).
// Arrives on the http_req thread; reschedules delivery to the asyncio
// thread.
func asyncioHTTPCb(hra *HTTPReqAux, opaque any, err int) {
	ahr := opaque.(*AsyncioHTTPReq)
	ahr.req = HTTPReqRetain(hra)
	state := &ahr.fam.asyncioHTTP

	state.mu.Lock()
	state.completed = slices.Insert(state.completed, 0, ahr)
	state.mu.Unlock()

	if state.aio != nil {
		state.aio.WakeupWorker(state.worker)
	}
}

// NewAsyncioHTTPReq — C: asyncio_http_req (asyncio_http.c:68-83).
// Variadic HTTPTag list is passed as a flat args slice to HTTPReqv.
func (fam *FileAccessManager) NewAsyncioHTTPReq(url string,
	cb func(hra *HTTPReqAux, opaque any),
	opaque any, args ...any) *AsyncioHTTPReq {

	ahr := &AsyncioHTTPReq{fam: fam, cb: cb, opaque: opaque}
	fam.HTTPReqv(url, args, asyncioHTTPCb, ahr)
	return ahr
}

// AsyncioHTTPCancel — C: asyncio_http_cancel (asyncio_http.c:89-92)
func (ahr *AsyncioHTTPReq) AsyncioHTTPCancel() {
	atomic.StoreInt32(&ahr.cancelled, 1)
}

// ahrDeliverCb — C: ahr_deliver_cb (asyncio_http.c:99-119).
// Runs on the asyncio thread.
func (fam *FileAccessManager) ahrDeliverCb() {
	state := &fam.asyncioHTTP
	state.mu.Lock()

	for len(state.completed) > 0 {
		ahr := state.completed[0]
		state.completed = state.completed[1:]
		state.mu.Unlock()

		if atomic.LoadInt32(&ahr.cancelled) == 0 {
			ahr.cb(ahr.req, ahr.opaque)
		}
		HTTPReqRelease(ahr.req)
		// C: free(ahr) — GC

		state.mu.Lock()
	}

	state.mu.Unlock()
}

// AsyncioHTTPStart — C: asyncio_http_init (asyncio_http.c:125-129),
// INITME(INIT_GROUP_ASYNCIO, asyncio_http_init, NULL, 0).
func (fam *FileAccessManager) AsyncioHTTPStart(aio *asyncio.AsyncIO) {
	state := &fam.asyncioHTTP
	state.mu.Lock()
	state.aio = aio
	state.mu.Unlock()
	state.worker = aio.AddWorker(fam.ahrDeliverCb)
}
