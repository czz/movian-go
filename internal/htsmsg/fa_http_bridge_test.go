package htsmsg

import (
	"testing"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
)

func TestHTTPCookieStoreRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir())
	in := []fileaccesscore.HTTPCookieRecord{
		{Name: "a", Path: "/", Domain: "example.test", Value: "1", Expire: 2000000000, Secure: true, HostOnly: true},
		{Name: "b", Path: "/x", Domain: "example.test", Value: "2", Expire: 2000000001},
		{Name: "far", Path: "/", Domain: "example.test", Value: "3", Expire: 1 << 40},
		{Name: "cf", Path: "/", Domain: "example.test", Value: "4", Expire: 2000000002,
			Secure: true, HostOnly: true, Imported: true, UA: "UA/1",
			ScopeOrigin: "https://example.test", ScopeOwner: "plugin.a"},
	}
	HTTPCookieStoreSave(s, in)
	out := HTTPCookieStoreLoad(s)
	if len(out) != 4 {
		t.Fatalf("loaded %d records, want 4", len(out))
	}
	if out[0] != in[0] || out[1] != in[1] || out[3] != in[3] {
		t.Fatalf("round trip changed records: %+v", out)
	}
	if out[2].Expire != 2147483647 {
		t.Fatalf("far-future expiry %d, want clamp to int32 max", out[2].Expire)
	}
}

// A record written before secure/hostonly existed has neither field.
func TestHTTPCookieStoreLoadsOldFormat(t *testing.T) {
	s := NewStore(t.TempDir())
	m := NewList()
	c := NewMap()
	c.AddStr("name", "old")
	c.AddStr("path", "/")
	c.AddStr("domain", "example.test")
	c.AddStr("value", "v")
	c.AddS32("expire", 2000000000)
	m.AddMsg("", c)
	if err := s.Save(m, "httpcookies"); err != nil {
		t.Fatal(err)
	}
	m.Release()
	out := HTTPCookieStoreLoad(s)
	if len(out) != 1 || out[0].Secure || out[0].HostOnly || out[0].Name != "old" || out[0].Expire != 2000000000 {
		t.Fatalf("old-format record: %+v", out)
	}
}
