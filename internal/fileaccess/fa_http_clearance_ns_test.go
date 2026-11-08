package fileaccess

import (
	"net/url"
	"strings"
	"testing"
)

// Ordinary jar cookies and imported clearance records are separate
// namespaces: an import never removes or blocks an ordinary cookie, and an
// ordinary cookie never touches the import.

func nsCount(header, name string) int {
	n := 0
	for _, p := range strings.Split(header, "; ") {
		if strings.HasPrefix(p, name+"=") {
			n++
		}
	}
	return n
}

// nsSend returns the Cookie headers of an HTTP/1 and an HTTP/2 request.
func nsSend(fam *FileAccessManager, owner string) (h1, h2 string) {
	return clrSend(fam, "https://example.test/", owner)
}

// nsSet gives the ordinary jar a cf_clearance through both transports.
func nsSetOrdinary(fam *FileAccessManager, value string) {
	testCookieSetOn(fam, "https://example.test/", "cf_clearance="+value+"; Path=/; Max-Age=3600")
}

func nsEach(t *testing.T, name string, f func(t *testing.T, set func(*FileAccessManager, string))) {
	t.Run(name+"/http1", func(t *testing.T) { f(t, nsSetOrdinary) })
	t.Run(name+"/http2", func(t *testing.T) {
		f(t, func(fam *FileAccessManager, v string) {
			u, _ := url.Parse("https://example.test/")
			testH2SetCookie(fam, u, "cf_clearance="+v+"; Path=/; Max-Age=3600")
		})
	})
}

func TestImportKeepsOrdinaryCookie(t *testing.T) {
	nsEach(t, "import", func(t *testing.T, set func(*FileAccessManager, string)) {
		fam := &FileAccessManager{}
		set(fam, "ordinary")
		if err := fam.HTTPImportClearance(clrCookie()); err != nil {
			t.Fatal(err)
		}
		if n := len(fam.httpCookies.list); n != 2 {
			t.Fatalf("jar has %d records, want ordinary + imported", n)
		}
		// Another owner and a request that did not opt in see the ordinary one.
		for _, owner := range []string{"", "plugin.b"} {
			h1, h2 := nsSend(fam, owner)
			for _, h := range []string{h1, h2} {
				if !hasCookie(h, "cf_clearance=ordinary") || nsCount(h, "cf_clearance") != 1 {
					t.Fatalf("owner %q got %q, want the ordinary cookie once", owner, h)
				}
			}
		}
		// The opted-in owner sends the imported value instead, never both.
		h1, h2 := nsSend(fam, clrOwner)
		for _, h := range []string{h1, h2} {
			if !hasCookie(h, "cf_clearance=synthetic-value") || nsCount(h, "cf_clearance") != 1 {
				t.Fatalf("owner got %q, want the imported cookie once", h)
			}
		}
	})
}

func TestOrdinaryCookieUpdatesWhileImportExists(t *testing.T) {
	nsEach(t, "refresh", func(t *testing.T, set func(*FileAccessManager, string)) {
		fam := &FileAccessManager{}
		set(fam, "ordinary")
		if err := fam.HTTPImportClearance(clrCookie()); err != nil {
			t.Fatal(err)
		}
		set(fam, "refreshed")
		if h1, h2 := nsSend(fam, ""); !hasCookie(h1, "cf_clearance=refreshed") || !hasCookie(h2, "cf_clearance=refreshed") {
			t.Fatalf("Set-Cookie did not refresh the ordinary cookie: %q %q", h1, h2)
		}
		if got, ok := fam.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance"); !ok || got.Value != "synthetic-value" {
			t.Fatalf("a refresh touched the import: ok=%v %+v", ok, got)
		}
		// Max-Age=0 removes the ordinary record only.
		testCookieSetOn(fam, "https://example.test/", "cf_clearance=; Path=/; Max-Age=0")
		u, _ := url.Parse("https://example.test/")
		testH2SetCookie(fam, u, "cf_clearance=; Path=/; Max-Age=0")
		if h1, _ := nsSend(fam, ""); nsCount(h1, "cf_clearance") != 0 {
			t.Fatalf("expired ordinary cookie still sent: %q", h1)
		}
		if h1, h2 := nsSend(fam, clrOwner); !hasCookie(h1, "cf_clearance=synthetic-value") || !hasCookie(h2, "cf_clearance=synthetic-value") {
			t.Fatalf("the import vanished with the ordinary cookie: %q %q", h1, h2)
		}
	})
}

func TestInjectAndDeleteCookiesLeaveImportAlone(t *testing.T) {
	fam := &FileAccessManager{}
	nsSetOrdinary(fam, "ordinary")
	if err := fam.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatal(err)
	}
	fam.HTTPInjectCookies("example.test", "/", map[string]string{"cf_clearance": "injected"})
	if h1, h2 := nsSend(fam, "plugin.b"); !hasCookie(h1, "cf_clearance=injected") || !hasCookie(h2, "cf_clearance=injected") {
		t.Fatalf("injectcookies did not reach the ordinary cookie: %q %q", h1, h2)
	}
	if got, _ := fam.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance"); got.Value != "synthetic-value" {
		t.Fatalf("injectcookies changed the import: %+v", got)
	}
	if n := fam.HTTPDeleteCookies("example.test", "/", nil); n != 1 {
		t.Fatalf("HTTPDeleteCookies removed %d, want the ordinary cookie only", n)
	}
	if _, ok := fam.HTTPGetImportedClearance(clrOwner, clrOrigin, "cf_clearance"); !ok {
		t.Fatal("HTTPDeleteCookies removed the import")
	}
}

func TestDeletingImportLeavesOrdinaryCookie(t *testing.T) {
	fam := &FileAccessManager{}
	nsSetOrdinary(fam, "ordinary")
	if err := fam.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatal(err)
	}
	if !fam.HTTPDeleteImportedClearance(clrOwner, clrOrigin, "cf_clearance") {
		t.Fatal("import not deleted")
	}
	if n := len(fam.httpCookies.list); n != 1 {
		t.Fatalf("jar has %d records, want the ordinary one", n)
	}
	for _, owner := range []string{"", clrOwner} {
		h1, h2 := nsSend(fam, owner)
		if !hasCookie(h1, "cf_clearance=ordinary") || !hasCookie(h2, "cf_clearance=ordinary") {
			t.Fatalf("owner %q lost the ordinary cookie: %q %q", owner, h1, h2)
		}
	}
}

func TestImportThenOrdinaryCookieArrives(t *testing.T) {
	// The import came first; a server then sets the same name.
	fam := clrFAM(t)
	nsSetOrdinary(fam, "ordinary")
	if n := len(fam.httpCookies.list); n != 2 {
		t.Fatalf("jar has %d records, want 2", n)
	}
	if h1, _ := nsSend(fam, ""); !hasCookie(h1, "cf_clearance=ordinary") {
		t.Fatalf("ordinary cookie not stored next to the import: %q", h1)
	}
	if h1, h2 := nsSend(fam, clrOwner); nsCount(h1, "cf_clearance") != 1 || nsCount(h2, "cf_clearance") != 1 ||
		!hasCookie(h1, "cf_clearance=synthetic-value") || !hasCookie(h2, "cf_clearance=synthetic-value") {
		t.Fatalf("opted-in request must send the import once: %q %q", h1, h2)
	}
}

func TestBothNamespacesSurvivePersistence(t *testing.T) {
	src := &FileAccessManager{}
	nsSetOrdinary(src, "ordinary")
	if err := src.HTTPImportClearance(clrCookie()); err != nil {
		t.Fatal(err)
	}
	var saved []HTTPCookieRecord
	src.httpCookies.cookieSave = func(_ any, r []HTTPCookieRecord) { saved = r }
	src.cookiePersist(nil, nil)
	if len(saved) != 2 {
		t.Fatalf("saved %d records, want both", len(saved))
	}
	load := func(recs []HTTPCookieRecord) *FileAccessManager {
		dst := &FileAccessManager{}
		dst.httpCookies.cookieLoad = func(any) []HTTPCookieRecord { return recs }
		dst.loadCookies()
		return dst
	}
	// Whatever the order in the store, both come back.
	rev := []HTTPCookieRecord{saved[1], saved[0]}
	for name, recs := range map[string][]HTTPCookieRecord{"saved order": saved, "reversed": rev} {
		dst := load(recs)
		if n := len(dst.httpCookies.list); n != 2 {
			t.Fatalf("%s: loaded %d records, want 2", name, n)
		}
		if h1, h2 := nsSend(dst, "plugin.b"); !hasCookie(h1, "cf_clearance=ordinary") || !hasCookie(h2, "cf_clearance=ordinary") {
			t.Fatalf("%s: ordinary cookie lost: %q %q", name, h1, h2)
		}
		if h1, h2 := nsSend(dst, clrOwner); nsCount(h1, "cf_clearance") != 1 || !hasCookie(h1, "cf_clearance=synthetic-value") || !hasCookie(h2, "cf_clearance=synthetic-value") {
			t.Fatalf("%s: import lost or doubled: %q %q", name, h1, h2)
		}
	}
}
