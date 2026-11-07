// fa_http_clearance.go — imported clearance cookies (no C counterpart).
//
// A plugin that solved a bot check in a browser hands the resulting cookie
// to Core through HTTPImportClearance. The record lives in the shared
// cookie jar but is never sent by the generic jar: only a request that
// carries FaImportedCookies and names the importing owner receives it.

package fileaccess

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// ImportedCookieMaxLifetime caps how far ahead an imported cookie may
// expire; a later expiry is clamped to it.
const ImportedCookieMaxLifetime = 30 * 24 * time.Hour

const (
	importedMaxName  = 256
	importedMaxValue = 4096
	importedMaxUA    = 1024
	importedMaxOwner = 256
	importedMaxPath  = 1024
)

// ImportedCookie is a clearance cookie handed over by a plugin. Value is a
// secret: it is logged only in debug mode (see clearanceTrace).
type ImportedCookie struct {
	Owner  string // plugin/context ID; set by Core from the caller, not by a plugin
	Origin string // https://host of the site it was solved for
	Name   string
	Value  string
	UA     string // User-Agent the cookie is bound to
	Path   string // "" = "/"
	Expire int64  // Unix seconds
}

// validImportedHost checks the host of an imported record: ASCII, not an IP
// literal, and not a public suffix.
func validImportedHost(host string) error {
	if host == "" || host != strings.ToLower(host) {
		return errors.New("origin host is empty or not lower case")
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if c >= 0x80 || c <= ' ' || c == 0x7f || strings.IndexByte("/\\?#@:[]%", c) >= 0 {
			return errors.New("origin host has invalid characters")
		}
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return errors.New("origin host is an IP literal")
	}
	if suffix, _ := publicsuffix.PublicSuffix(host); suffix == host {
		return errors.New("origin host is a public suffix")
	}
	return nil
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// importedCookieNameOK reports whether name is an RFC 6265 token.
func importedCookieNameOK(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return name != ""
}

// normalizeImportedOrigin returns the canonical "https://host" of origin and
// its host, or an error.
func normalizeImportedOrigin(origin string) (canon, host string, err error) {
	u, perr := url.Parse(origin)
	if perr != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Host == "" {
		return "", "", errors.New("origin must be https://host")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", "", errors.New("origin must use https")
	}
	if p := u.Port(); p != "" && p != "443" {
		return "", "", errors.New("origin must use the default https port")
	}
	host = strings.ToLower(u.Hostname())
	if strings.HasPrefix(u.Host, "[") {
		return "", "", errors.New("origin host is an IP literal")
	}
	if err := validImportedHost(host); err != nil {
		return "", "", err
	}
	return "https://" + host, host, nil
}

// validateImportedCookie checks ic and returns its normalized form. Expire
// is clamped to now+ImportedCookieMaxLifetime when clamp is set, rejected
// otherwise. Errors never carry the cookie value.
func validateImportedCookie(ic ImportedCookie, now int64, clamp bool) (ImportedCookie, error) {
	var err error
	if ic.Owner == "" || len(ic.Owner) > importedMaxOwner || hasControl(ic.Owner) {
		return ic, errors.New("invalid owner")
	}
	if ic.Origin, _, err = normalizeImportedOrigin(ic.Origin); err != nil {
		return ic, err
	}
	if len(ic.Name) > importedMaxName || !importedCookieNameOK(ic.Name) {
		return ic, errors.New("invalid cookie name")
	}
	if ic.Value == "" || len(ic.Value) > importedMaxValue || hasControl(ic.Value) ||
		strings.ContainsAny(ic.Value, ";\\\" ,") {
		return ic, errors.New("invalid cookie value")
	}
	if ic.UA == "" || len(ic.UA) > importedMaxUA || hasControl(ic.UA) {
		return ic, errors.New("invalid user agent")
	}
	if ic.Path == "" {
		ic.Path = "/"
	}
	if len(ic.Path) > importedMaxPath || !strings.HasPrefix(ic.Path, "/") ||
		hasControl(ic.Path) || strings.ContainsAny(ic.Path, ";?#") {
		return ic, errors.New("invalid cookie path")
	}
	if ic.Expire <= now {
		return ic, errors.New("cookie is expired or has no expiry")
	}
	limit := now + int64(ImportedCookieMaxLifetime/time.Second)
	if ic.Expire > limit {
		if !clamp {
			return ic, errors.New("cookie expiry beyond the allowed lifetime")
		}
		ic.Expire = limit
	}
	return ic, nil
}

func (hc *httpCookie) importedRecord() ImportedCookie {
	return ImportedCookie{Owner: hc.scopeOwner, Origin: hc.scopeOrigin, Name: hc.name,
		Value: hc.value, UA: hc.ua, Path: hc.path, Expire: hc.expire}
}

// findImportedLocked returns the index of the imported record for
// (origin, name), or -1. Caller holds httpCookies.mu.
func (fam *FileAccessManager) findImportedLocked(origin, name string) int {
	for i, x := range fam.httpCookies.list {
		if x.imported && x.scopeOrigin == origin && x.name == name {
			return i
		}
	}
	return -1
}

// importedHostUALocked returns the UA other imported records on host
// require, or "" when there are none. skip is an index to ignore.
func (fam *FileAccessManager) importedHostUALocked(host string, skip int) string {
	for i, x := range fam.httpCookies.list {
		if i != skip && x.imported && x.domain == host {
			return x.ua
		}
	}
	return ""
}

// unpinImportedUALocked releases the UA pin of a record that is leaving the
// jar (list[idx] must still be present). The pin stays while another
// imported record on the host needs it; a record that set it hands that
// duty over. Caller holds httpCookies.mu.
func (fam *FileAccessManager) unpinImportedUALocked(idx int) {
	hc := fam.httpCookies.list[idx]
	if !hc.uaPinned {
		return
	}
	for i, x := range fam.httpCookies.list {
		if i != idx && x.imported && x.domain == hc.domain && x.ua == hc.ua {
			x.uaPinned = true
			return
		}
	}
	if fam.httpDomainUAGet(hc.domain) == hc.ua {
		fam.HTTPDomainSetUserAgent(hc.domain, "")
	}
}

// purgeExpiredImportedLocked drops expired imported records and releases
// their UA pins, so a dead record never blocks a new import. Caller holds
// httpCookies.mu.
func (fam *FileAccessManager) purgeExpiredImportedLocked(now int64) {
	for i := 0; i < len(fam.httpCookies.list); i++ {
		if hc := fam.httpCookies.list[i]; hc.imported && hc.expire <= now {
			fam.unpinImportedUALocked(i)
			fam.httpCookies.list = slices.Delete(fam.httpCookies.list, i, i+1)
			i--
		}
	}
}

// requestUA is the pinned User-Agent for a request to host. An imported
// record that has expired since the last jar change is purged first, so its
// pin does not outlive the cookie.
func (fam *FileAccessManager) requestUA(host string) string {
	now := time.Now().Unix()
	fam.httpCookies.mu.Lock()
	fam.purgeExpiredImportedLocked(now)
	fam.httpCookies.mu.Unlock()
	return fam.httpDomainUAGet(host)
}

// HTTPImportClearance stores ic as an imported record: sent only to an
// opted-in request of ic.Owner, pinned to ic.UA. A second import for the
// same owner, origin and name replaces the first; another owner for that
// origin and name is rejected, as is a UA that differs from the one another
// imported record on the host already needs.
func (fam *FileAccessManager) HTTPImportClearance(ic ImportedCookie) error {
	in := ic
	err := fam.importClearance(&ic)
	if fam.ClearanceDebug() {
		if err != nil {
			fam.ClearanceTrace("import rejected: %v owner=%q origin=%q name=%q value=%q ua=%q path=%q expire=%d",
				err, in.Owner, in.Origin, in.Name, in.Value, in.UA, in.Path, in.Expire)
		} else {
			fam.ClearanceTrace("import accepted owner=%q origin=%q name=%q value=%q ua=%q path=%q expire=%d->%d clamped=%t",
				ic.Owner, ic.Origin, ic.Name, ic.Value, ic.UA, ic.Path, in.Expire, ic.Expire, in.Expire != ic.Expire)
		}
	}
	return err
}

// importClearance does the work of HTTPImportClearance; on success *icp
// holds the normalized record.
func (fam *FileAccessManager) importClearance(icp *ImportedCookie) error {
	now := time.Now().Unix()
	ic := *icp
	ic, err := validateImportedCookie(ic, now, true)
	if err != nil {
		return fmt.Errorf("import clearance: %w", err)
	}
	*icp = ic
	host := strings.TrimPrefix(ic.Origin, "https://")
	nc := httpCookie{
		name: ic.Name, path: ic.Path, domain: host, value: ic.Value,
		expire: ic.Expire, secure: true, hostOnly: true,
		imported: true, ua: ic.UA, scopeOrigin: ic.Origin, scopeOwner: ic.Owner,
	}

	fam.httpCookies.mu.Lock()
	fam.purgeExpiredImportedLocked(now)
	idx := fam.findImportedLocked(ic.Origin, ic.Name)
	if idx >= 0 && fam.httpCookies.list[idx].scopeOwner != ic.Owner {
		fam.httpCookies.mu.Unlock()
		return errors.New("import clearance: record belongs to another owner")
	}
	if ua := fam.importedHostUALocked(host, idx); ua != "" && ua != ic.UA {
		fam.httpCookies.mu.Unlock()
		return errors.New("import clearance: host already has imported cookies bound to another user agent")
	}
	// A Set-Cookie cookie with the same name, domain and path is replaced.
	for i, x := range fam.httpCookies.list {
		if i != idx && !x.imported && x.name == nc.name && x.path == nc.path && x.domain == nc.domain {
			fam.httpCookies.list = append(fam.httpCookies.list[:i], fam.httpCookies.list[i+1:]...)
			if idx > i {
				idx--
			}
			break
		}
	}
	prior := fam.httpDomainUAGet(host)
	if idx >= 0 {
		old := fam.httpCookies.list[idx]
		nc.uaPinned = old.uaPinned || prior != ic.UA
		*old = nc
	} else {
		nc.uaPinned = prior != ic.UA
		fam.httpCookies.list = append([]*httpCookie{&nc}, fam.httpCookies.list...)
	}
	fam.HTTPDomainSetUserAgent(host, ic.UA)
	fam.httpCookies.mu.Unlock()
	fam.armCookiePersist()
	return nil
}

// HTTPGetImportedClearance returns the unexpired record the owner imported
// for origin and name. The value is a secret: never log it.
func (fam *FileAccessManager) HTTPGetImportedClearance(owner, origin, name string) (ImportedCookie, bool) {
	canon, _, err := normalizeImportedOrigin(origin)
	if err != nil || owner == "" {
		return ImportedCookie{}, false
	}
	now := time.Now().Unix()
	fam.httpCookies.mu.Lock()
	defer fam.httpCookies.mu.Unlock()
	idx := fam.findImportedLocked(canon, name)
	if idx < 0 {
		return ImportedCookie{}, false
	}
	hc := fam.httpCookies.list[idx]
	if hc.scopeOwner != owner || hc.expire <= now {
		return ImportedCookie{}, false
	}
	return hc.importedRecord(), true
}

// HTTPDeleteImportedClearance removes the owner's record and releases its
// UA pin if the record set it. It reports whether a record was removed.
func (fam *FileAccessManager) HTTPDeleteImportedClearance(owner, origin, name string) bool {
	canon, _, err := normalizeImportedOrigin(origin)
	if err != nil || owner == "" {
		return false
	}
	fam.httpCookies.mu.Lock()
	idx := fam.findImportedLocked(canon, name)
	if idx < 0 || fam.httpCookies.list[idx].scopeOwner != owner {
		fam.httpCookies.mu.Unlock()
		return false
	}
	fam.unpinImportedUALocked(idx)
	fam.httpCookies.list = append(fam.httpCookies.list[:idx], fam.httpCookies.list[idx+1:]...)
	fam.httpCookies.mu.Unlock()
	fam.armCookiePersist()
	return true
}

func (fam *FileAccessManager) armCookiePersist() {
	if cs := fam.CalloutSystem(); cs != nil {
		cs.Arm(&fam.httpCookies.persistTimer, fam.cookiePersist, nil, 5)
	}
}

// cookiePair is a name/value ready for a Cookie header.
type cookiePair struct{ name, value string }

// selectCookies returns the jar cookies a request may carry, in jar order.
// importedOwner is the owner of a request that opted in with
// FaImportedCookies ("" = no opt-in); an imported cookie is sent only to
// that owner and replaces a same-named generic one. HTTP/1 and HTTP/2/3
// build their Cookie header from it so the rules cannot drift. dbg is the
// request's debug flag: it traces each imported record's fate.
func (fam *FileAccessManager) selectCookies(host, path string, secureReq bool,
	importedOwner string, dbg bool, extra func(name string) bool) []cookiePair {
	now := time.Now().Unix()
	fam.httpCookies.mu.Lock()
	defer fam.httpCookies.mu.Unlock()
	var out []cookiePair
	var sentImported map[string]bool
	trace := dbg && importedOwner != "" // only a request that opted in
	for _, hc := range fam.httpCookies.list {
		if !hc.imported {
			continue
		}
		reason := cookieSkipReason(hc, host, path, secureReq, importedOwner, now)
		if reason == "" {
			if sentImported == nil {
				sentImported = map[string]bool{}
			}
			sentImported[hc.name] = true
		}
		if trace {
			if reason == "" && extra(hc.name) {
				reason = "explicit-cookie-override"
			}
			fam.traceImportedSend(hc, host, path, importedOwner, reason)
		}
	}
	for _, hc := range fam.httpCookies.list {
		if !cookieSendable(hc, host, path, secureReq, importedOwner, now) {
			continue
		}
		if !hc.imported && sentImported[hc.name] {
			continue
		}
		if extra(hc.name) {
			continue
		}
		out = append(out, cookiePair{hc.name, hc.value})
	}
	return out
}

// validLoadedImported re-checks a persisted imported record. Anything that
// fails is dropped, never repaired.
func validLoadedImported(r HTTPCookieRecord, now int64) (httpCookie, bool) {
	if !r.Secure || !r.HostOnly {
		return httpCookie{}, false
	}
	ic, err := validateImportedCookie(ImportedCookie{
		Owner: r.ScopeOwner, Origin: r.ScopeOrigin, Name: r.Name, Value: r.Value,
		UA: r.UA, Path: r.Path, Expire: r.Expire,
	}, now, false)
	if err != nil || ic.Origin != r.ScopeOrigin || r.Domain != strings.TrimPrefix(ic.Origin, "https://") {
		return httpCookie{}, false
	}
	return httpCookie{
		name: ic.Name, path: ic.Path, domain: r.Domain, value: ic.Value, expire: ic.Expire,
		secure: true, hostOnly: true,
		imported: true, ua: ic.UA, scopeOrigin: ic.Origin, scopeOwner: ic.Owner,
	}, true
}
