package fileaccess

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// uaProbe records the User-Agent and Cookie of the last request.
type uaProbe struct {
	mu         sync.Mutex
	ua, cookie string
}

func (p *uaProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.ua, p.cookie = r.Header.Get("User-Agent"), r.Header.Get("Cookie")
	p.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (p *uaProbe) seen() (ua, cookie string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ua, p.cookie
}

// An opted-in request that carries an imported record uses the record's
// bound UA, whatever a later HTTPDomainSetUserAgent (injectcookies "$ua") or
// an explicit User-Agent header of the caller says. A request that does not
// carry the record is left alone.
func TestImportedClearanceRequestUsesBoundUA(t *testing.T) {
	const other = "Other/2.0"
	type run func(t *testing.T, fam *FileAccessManager, srvURL string, owner, hdrUA string)
	h1 := func(t *testing.T, fam *FileAccessManager, srvURL, owner, hdrUA string) {
		var errbuf [256]byte
		args := []any{HTTPTagErrbuf, errbuf[:], HTTPTagMethod, "GET", HTTPTagFlags, FaImportedCookies}
		if owner != "" {
			args = append(args, HTTPTagImportedOwner, owner)
		}
		if hdrUA != "" {
			args = append(args, HTTPTagRequestHeader, "User-Agent", hdrUA)
		}
		if rc := fam.HTTPReqv(srvURL+"/", args, nil, nil); rc != 0 {
			t.Fatalf("HTTPReqv rc=%d: %s", rc, misc.CStr(errbuf[:]))
		}
	}
	h2 := func(t *testing.T, fam *FileAccessManager, srvURL, owner, hdrUA string) {
		foe := &OpenExtra{ImportedOwner: owner}
		if hdrUA != "" {
			foe.RequestHeaders = map[string]string{"User-Agent": hdrUA}
		}
		h, err := h2Open(&FAProtocol{fam: fam}, srvURL, FaImportedCookies, foe)
		if err != nil {
			t.Fatalf("h2Open: %v", err)
		}
		h.Close()
	}
	for _, proto := range []struct {
		name string
		tls  *tls.Config
		do   run
	}{
		{"http1", &tls.Config{NextProtos: []string{"http/1.1"}}, h1},
		{"http2", &tls.Config{}, h2},
	} {
		for _, c := range []struct {
			name       string
			owner      string
			domainUA   string
			hdrUA      string
			wantUA     string
			wantCookie bool
		}{
			{"domain UA changed after import", clrOwner, other, "", clrUA, true},
			{"explicit header", clrOwner, "", "Caller/3.0", clrUA, true},
			{"domain UA and explicit header", clrOwner, other, "Caller/3.0", clrUA, true},
			{"other owner keeps the caller header", "plugin.b", other, "Caller/3.0", "Caller/3.0", false},
			{"other owner keeps the domain UA", "plugin.b", other, "", other, false},
		} {
			t.Run(proto.name+"/"+c.name, func(t *testing.T) {
				p := &uaProbe{}
				srv := httptest.NewUnstartedServer(p)
				srv.TLS = proto.tls
				srv.StartTLS()
				defer srv.Close()
				fam := NewFileAccessManager(nil, nil)
				clrPlant(fam, srv.URL)
				if c.domainUA != "" {
					fam.HTTPDomainSetUserAgent("127.0.0.1", c.domainUA)
				}
				proto.do(t, fam, srv.URL, c.owner, c.hdrUA)
				ua, cookie := p.seen()
				if ua != c.wantUA {
					t.Errorf("User-Agent = %q, want %q", ua, c.wantUA)
				}
				if got := hasCookie(cookie, "cf_clearance=synthetic-value"); got != c.wantCookie {
					t.Errorf("cookie sent = %v, want %v (%q)", got, c.wantCookie, cookie)
				}
			})
		}
	}
}

// The selection hands back the record's UA with the cookies, and httpHeadersSend
// puts it over the merged caller headers.
func TestHTTPHeadersSendForcedUA(t *testing.T) {
	var q misc.HtsbufQueue
	q.HtsbufQueueSetup(0)
	var def, user httpnet.HTTPHeaderList
	def.Add("User-Agent", "Default/1", false)
	user.Add("User-Agent", "Caller/3", false)
	httpHeadersSend(&q, &def, &user, clrUA)
	out := q.String()
	if want := "User-Agent: " + clrUA + "\r\n"; !strings.Contains(out, want) || strings.Contains(out, "Caller/3") {
		t.Fatalf("headers = %q", out)
	}
}
