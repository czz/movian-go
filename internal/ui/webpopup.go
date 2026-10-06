// webpopup.go — C: src/ui/webpopup.c + webpopup.h (1:1).
//
// Result handling for the platform webpopup/browser glue. The popup
// itself (webpopup_create, webbrowser_open) lives in the per-arch layer
// (src/arch/linux/linux_webpopup.c), which is intentionally not ported:
// it requires webkit-1.0 (the GTK2 WebKit), extinct on modern systems —
// only webkit2gtk-4.1 (GTK3) exists, and the GTK2 frontend itself has
// been removed (GTK2 is EOL). The C configure emits CONFIG_WEBPOPUP=0
// for the same reason, so es_webpopup takes its "unsupported" branch.
// This file carries the shared result struct and the trapped-URL
// query-argument finalization used by the enabled path if a suitable
// webkit ever becomes available.
package ui

import (
	"slices"
	"strings"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/trace"
)

// webpopupDeps — dependency seam; C resolves trace() via a global.
// SetWebpopupTrace wires it at init; fam serves the windows CDP
// cookie channel (fetching DevToolsActivePort + /json/list through
// the canonical fileaccess stack).
var webpopupDeps struct {
	ts  *trace.TraceSystem
	fam *facore.FileAccessManager
	// mainloopWake pokes the blocked UI mainloop so a freshly
	// dispatched webpopup job is drained promptly — GTK/webkit jobs
	// can only run on the process main thread (WpMainCheck slot).
	mainloopWake func()
}

// SetWebpopupTrace injects the trace system (C: trace() global).
func SetWebpopupTrace(ts *trace.TraceSystem) { webpopupDeps.ts = ts }

// SetWebpopupFAM injects the fileaccess manager (C: fa globals).
func SetWebpopupFAM(fam *facore.FileAccessManager) { webpopupDeps.fam = fam }

// SetWebpopupMainloopWake injects the arch mainloop waker
// (C: g_main_context_wakeup on glibcourier dispatch).
func SetWebpopupMainloopWake(fn func()) { webpopupDeps.mainloopWake = fn }

// C: webpopup.h result codes
const (
	WebpopupTrappedURL   = 0 // C: WEBPOPUP_TRAPPED_URL
	WebpopupClosedByUser = 1 // C: WEBPOPUP_CLOSED_BY_USER
	WebpopupLoadError    = 2 // C: WEBPOPUP_LOAD_ERROR
)

// WebpopupResult — C: webpopup_result_t
type WebpopupResult struct {
	ResultCode int // C: wr_resultcode

	Trapped struct {
		URL      string                  // C: wr_trapped.url
		Hostname string                  // C: wr_trapped.hostname
		Path     string                  // C: wr_trapped.path
		Port     int                     // C: wr_trapped.port
		QArgs    *httpnet.HTTPHeaderList // C: wr_trapped.qargs
	}
}

// WebpopupFinalizeResult — C: webpopup_finalize_result
//
// Splits the trapped URL into hostname/port/path and parses the query
// string into a header list. Entries are inserted at the list head,
// matching C's LIST_INSERT_HEAD (final order is reversed vs the query).
func WebpopupFinalizeResult(wr *WebpopupResult) {
	if wr.Trapped.URL == "" {
		return
	}
	webpopupDeps.ts.Trace(trace.TRACE_DEBUG, "webpopup", "Trapped URL: %s", wr.Trapped.URL)

	var hn [256]byte
	var path [4096]byte
	port := 0

	misc.UrlSplit(nil, 0, nil, 0, hn[:], len(hn),
		&port, path[:], len(path), wr.Trapped.URL)

	wr.Trapped.Port = port
	wr.Trapped.Hostname = misc.CStr(hn[:])
	wr.Trapped.Path = misc.CStr(path[:])

	if wr.Trapped.QArgs == nil {
		wr.Trapped.QArgs = &httpnet.HTTPHeaderList{}
	}

	// C: char *k = strchr(path, '?'); while(k) { *k++ = 0; parse k/v; k = n; }
	// Each segment is a key with an optional '='-delimited value.
	rest := wr.Trapped.Path
	k := strings.IndexByte(rest, '?')
	for k >= 0 {
		rest = rest[k+1:]
		v := strings.IndexByte(rest, '=')
		n := strings.IndexByte(rest, '&')

		var key string
		var value *string
		if v < 0 {
			if n < 0 {
				key = rest
			} else {
				key = rest[:n]
			}
		} else {
			key = rest[:v]
			v++
			if n < 0 {
				s := rest[v:]
				value = &s
			} else {
				s := rest[v:n]
				value = &s
			}
		}

		hh := &httpnet.HTTPHeaderEntry{Key: key}
		if value != nil {
			hh.Value = *value
		}
		// C: LIST_INSERT_HEAD(&wr->wr_trapped.qargs, hh, hh_link)
		wr.Trapped.QArgs.List = slices.Insert(wr.Trapped.QArgs.List, 0, hh)

		k = n
	}
}

// WebpopupResultFree — C: webpopup_result_free
func WebpopupResultFree(wr *WebpopupResult) {
	if wr == nil {
		return
	}
	// C: free(url), free(hostname), free(path), http_headers_free(qargs), free(wr)
	wr.Trapped.URL = ""
	wr.Trapped.Hostname = ""
	wr.Trapped.Path = ""
	if wr.Trapped.QArgs != nil {
		wr.Trapped.QArgs.Free()
		wr.Trapped.QArgs = nil
	}
}
