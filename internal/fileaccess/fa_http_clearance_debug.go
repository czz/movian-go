// fa_http_clearance_debug.go — debug-only trace of imported clearance
// cookies (no C counterpart). Everything here is gated on the "Debug all HTTP
// requests" setting (gconf.EnableHTTPDebug) or a request's FaDebug flag.
// With debug on, cookie values are logged in full; with it off nothing is
// logged at all.

package fileaccess

import "github.com/czz/movian-go/internal/trace"

// ClearanceSubsys is the trace subsystem of all clearance debug lines.
const ClearanceSubsys = "clearance"

// ClearanceDebug reports whether the global HTTP debug setting is on.
func (fam *FileAccessManager) ClearanceDebug() bool {
	return fam != nil && fam.Gconf().EnableHTTPDebug.Load()
}

// ClearanceTrace writes one TRACE_DEBUG line to the "clearance" subsystem.
// The caller checks ClearanceDebug (or its request's debug flag) first.
func (fam *FileAccessManager) ClearanceTrace(format string, args ...any) {
	fam.TraceSystem().Trace(trace.TRACE_DEBUG, ClearanceSubsys, format, args...)
}

// traceImportedSend logs whether an imported record rides a request that
// opted in, and why not if it does not. Caller holds httpCookies.mu.
func (fam *FileAccessManager) traceImportedSend(hc *httpCookie, host, path, owner, skip string) {
	if skip == "" {
		fam.ClearanceTrace("send attached owner=%q host=%q path=%q name=%q value=%q expire=%d flag=imported",
			owner, host, path, hc.name, hc.value, hc.expire)
		return
	}
	fam.ClearanceTrace("send skipped (%s) owner=%q host=%q path=%q name=%q record-owner=%q record-domain=%q record-path=%q expire=%d",
		skip, owner, host, path, hc.name, hc.scopeOwner, hc.domain, hc.path, hc.expire)
}
