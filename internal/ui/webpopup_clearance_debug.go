// webpopup_clearance_debug.go — debug-only trace of the Android clearance
// bridge. The flag is the fileaccess manager's "Debug all HTTP requests"
// setting, read through webpopupDeps.fam (injected by SetWebpopupFAM); with
// it off nothing is logged. With it on, cookie values are logged in full.
package ui

import "strconv"

func clearanceDebug() bool { return webpopupDeps.fam.ClearanceDebug() }

// clearanceTrace writes a TRACE_DEBUG line to the "clearance" subsystem.
// Callers check clearanceDebug first so the arguments cost nothing when off.
func clearanceTrace(format string, args ...any) {
	webpopupDeps.fam.ClearanceTrace(format, args...)
}

func traceClearanceOutcome(c *AndroidClearance, reason string) {
	if c == nil || c.Cookie == nil {
		clearanceTrace("android select rejected: %s", reason)
		return
	}
	k := c.Cookie
	clearanceTrace("android select accepted name=%q value=%q domain=%q path=%q expire=%d secure=%t httponly=%t partitioned(first-party-limited)=%s origin=%q owner=%q",
		k.Name, k.Value, k.Domain, k.Path, k.Expires, k.Secure, k.HTTPOnly,
		strconv.FormatBool(c.Context.FirstPartyLimited), c.Context.Origin, c.Context.Owner)
}
