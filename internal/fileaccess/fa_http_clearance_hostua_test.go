package fileaccess

import (
	"net/url"
	"testing"

	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// An imported record carries its own User-Agent. It never changes the host
// User-Agent that every other request to the host uses.

// hostUASend returns the User-Agent an HTTP/1 and an HTTP/2 request to target
// must be forced to by the imported records it sends ("" = none).
func hostUASend(fam *FileAccessManager, target, owner string) (h1, h2 string) {
	u, _ := url.Parse(target)
	var extra, headers httpnet.HTTPHeaderList
	h1 = httpCookieAppend(u.Hostname(), urlPortNum(u), &httpFile{
		fam: fam, path: u.EscapedPath(), url: target, importedOwner: owner,
	}, &headers, &extra)
	_, h2 = (&h2File{fam: fam, url: target, importedOwner: owner}).
		h2CookieHeaderFor(u.Hostname(), urlPortNum(u), u.EscapedPath(), cookieURLSecure(target), &extra)
	return h1, h2
}

func TestImportDoesNotChangeHostUserAgent(t *testing.T) {
	for _, pre := range []string{"", "Host/1.0"} {
		fam := &FileAccessManager{}
		if pre != "" {
			fam.HTTPDomainSetUserAgent("example.test", pre)
		}
		if err := fam.HTTPImportClearance(clrCookie()); err != nil {
			t.Fatal(err)
		}
		if got := fam.httpDomainUAGet("example.test"); got != pre {
			t.Fatalf("host UA = %q after import, want %q untouched", got, pre)
		}
		for _, tc := range []struct{ name, target, owner string }{
			{"no opt-in", "https://example.test/", ""},
			{"another owner", "https://example.test/", "plugin.b"},
			{"plain http", "http://example.test/", clrOwner},
			{"another port", "https://example.test:8443/", clrOwner},
			{"another host", "https://other.test/", clrOwner},
		} {
			if h1, h2 := hostUASend(fam, tc.target, tc.owner); h1 != "" || h2 != "" {
				t.Errorf("pre %q, %s: forced UA %q/%q, want the host UA to apply", pre, tc.name, h1, h2)
			}
		}
		if h1, h2 := hostUASend(fam, "https://example.test/", clrOwner); h1 != clrUA || h2 != clrUA {
			t.Errorf("pre %q: opted-in request UA %q/%q, want the record's %q", pre, h1, h2, clrUA)
		}
		if !fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "cf_clearance") {
			t.Fatal("delete failed")
		}
		if got := fam.httpDomainUAGet("example.test"); got != pre {
			t.Fatalf("host UA = %q after delete, want %q", got, pre)
		}
	}
}

func TestLoadingImportDoesNotChangeHostUserAgent(t *testing.T) {
	src := clrFAM(t)
	var saved []HTTPCookieRecord
	src.httpCookies.cookieSave = func(_ any, r []HTTPCookieRecord) { saved = r }
	src.cookiePersist(nil, nil)
	for _, pre := range []string{"", "Host/1.0"} {
		dst := &FileAccessManager{}
		if pre != "" {
			dst.HTTPDomainSetUserAgent("example.test", pre)
		}
		dst.httpCookies.cookieLoad = func(any) []HTTPCookieRecord { return saved }
		dst.loadCookies()
		if _, ok := dst.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance"); !ok {
			t.Fatal("record not loaded")
		}
		if got := dst.httpDomainUAGet("example.test"); got != pre {
			t.Fatalf("host UA = %q after load, want %q", got, pre)
		}
		if h1, h2 := hostUASend(dst, "https://example.test/", clrOwner); h1 != clrUA || h2 != clrUA {
			t.Fatalf("loaded record's UA %q/%q, want %q", h1, h2, clrUA)
		}
	}
}

// The UA belongs to the record, so two owners may bind different ones on one
// host, while one owner keeps a single UA per host.
func TestImportedUserAgentIsPerOwner(t *testing.T) {
	fam := &FileAccessManager{}
	if err := fam.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatal(err)
	}
	b := clrCookie()
	b.Owner, b.Name, b.UA = "plugin.b", "other_clearance", "Other/2.0"
	if err := fam.HTTPImportClearance(b); err != nil {
		t.Fatalf("another owner's UA was refused: %v", err)
	}
	if h1, _ := hostUASend(fam, "https://example.test/", clrOwner); h1 != clrUA {
		t.Fatalf("owner A UA = %q", h1)
	}
	if h1, _ := hostUASend(fam, "https://example.test/", "plugin.b"); h1 != "Other/2.0" {
		t.Fatalf("owner B UA = %q", h1)
	}
	again := clrCookie()
	again.Name, again.UA = "second", "Third/3.0"
	if err := fam.HTTPImportClearance(again); err == nil {
		t.Fatal("one owner bound two UAs on one host")
	}
}
