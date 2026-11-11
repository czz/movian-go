package ui

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

var policyNow = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

const (
	policyOrigin = "https://tracker.example"
	policyTarget = "https://tracker.example/forum/index.php"
	policyOwner  = "plugin.tracker"
	policyName   = "cf_clearance"
	policyUA     = "Mozilla/5.0 (Linux; Android 14)"
)

func futureExpires() string { return policyNow.Add(24 * time.Hour).Format(http.TimeFormat) }
func pastExpires() string   { return policyNow.Add(-time.Hour).Format(http.TimeFormat) }

func goodLine(extra ...string) string {
	attrs := append([]string{"Path=/", "Expires=" + futureExpires(), "Secure"}, extra...)
	return policyName + "=tok; " + strings.Join(attrs, "; ")
}

func goodCtx() AndroidBridgeContext {
	return AndroidBridgeContext{Origin: policyOrigin, Owner: policyOwner}
}

func TestSelectAndroidClearance(t *testing.T) {
	partitionedWant := ""
	if !androidFirstPartyVariant {
		partitionedWant = "partitioned-not-allowed"
	}
	tests := []struct {
		name   string
		lines  []string
		target string
		ctx    AndroidBridgeContext
		owner  string
		want   string // "" = accepted
	}{
		{"accepted", []string{goodLine()}, policyTarget, goodCtx(), policyOwner, ""},
		{"accepted other cookies ignored", []string{"a=b; Path=/", goodLine()}, policyTarget, goodCtx(), policyOwner, ""},
		{"accepted max-age", []string{policyName + "=tok; Path=/; Secure; Max-Age=3600"}, policyTarget, goodCtx(), policyOwner, ""},
		{"accepted explicit 443 port", []string{goodLine()}, "https://tracker.example:443/x", goodCtx(), policyOwner, ""},
		{"partitioned", []string{goodLine("Partitioned")}, policyTarget, goodCtx(), policyOwner, partitionedWant},

		{"target http", []string{goodLine()}, "http://tracker.example/x", goodCtx(), policyOwner, "target-invalid-https-origin"},
		{"context origin http", []string{goodLine()}, policyTarget, AndroidBridgeContext{Origin: "http://tracker.example", Owner: policyOwner}, policyOwner, "needs-exact-top-level-origin"},
		{"context origin empty", []string{goodLine()}, policyTarget, AndroidBridgeContext{Owner: policyOwner}, policyOwner, "needs-exact-top-level-origin"},
		{"context origin other host", []string{goodLine()}, policyTarget, AndroidBridgeContext{Origin: "https://other.example", Owner: policyOwner}, policyOwner, "needs-exact-top-level-origin"},
		{"context origin other port", []string{goodLine()}, policyTarget, AndroidBridgeContext{Origin: "https://tracker.example:8443", Owner: policyOwner}, policyOwner, "needs-exact-top-level-origin"},
		{"context origin with path", []string{goodLine()}, policyTarget, AndroidBridgeContext{Origin: policyOrigin + "/forum", Owner: policyOwner}, policyOwner, "needs-exact-top-level-origin"},
		{"target subdomain of origin", []string{goodLine()}, "https://sub.tracker.example/", goodCtx(), policyOwner, "needs-exact-top-level-origin"},

		{"context owner missing", []string{goodLine()}, policyTarget, AndroidBridgeContext{Origin: policyOrigin}, policyOwner, "owner-mismatch"},
		{"request owner missing", []string{goodLine()}, policyTarget, goodCtx(), "", "owner-mismatch"},
		{"other owner", []string{goodLine()}, policyTarget, goodCtx(), "plugin.other", "owner-mismatch"},

		{"not secure", []string{policyName + "=tok; Path=/; Expires=" + futureExpires()}, policyTarget, goodCtx(), policyOwner, "secure-required"},
		{"expired", []string{policyName + "=tok; Path=/; Secure; Expires=" + pastExpires()}, policyTarget, goodCtx(), policyOwner, "expires-not-in-future"},
		{"no expiry", []string{policyName + "=tok; Path=/; Secure"}, policyTarget, goodCtx(), policyOwner, "expires-not-in-future"},
		{"max-age zero", []string{policyName + "=tok; Path=/; Secure; Max-Age=0"}, policyTarget, goodCtx(), policyOwner, "expires-not-in-future"},
		{"max-age negative", []string{policyName + "=tok; Path=/; Secure; Max-Age=-5"}, policyTarget, goodCtx(), policyOwner, "expires-not-in-future"},
		{"max-age wins over past expires", []string{policyName + "=tok; Path=/; Secure; Expires=" + pastExpires() + "; Max-Age=60"}, policyTarget, goodCtx(), policyOwner, ""},
		{"max-age expired wins over future expires", []string{policyName + "=tok; Path=/; Secure; Expires=" + futureExpires() + "; Max-Age=0"}, policyTarget, goodCtx(), policyOwner, "expires-not-in-future"},
		{"malformed max-age", []string{policyName + "=tok; Path=/; Secure; Max-Age=soon"}, policyTarget, goodCtx(), policyOwner, "malformed-max-age"},

		{"ambiguous same path", []string{goodLine(), strings.Replace(goodLine(), "tok", "tok2", 1)}, policyTarget, goodCtx(), policyOwner, "ambiguous-cookie"},
		{"ambiguous different path", []string{goodLine(), strings.Replace(goodLine(), "Path=/", "Path=/forum", 1)}, policyTarget, goodCtx(), policyOwner, "ambiguous-cookie"},

		{"none", []string{"a=b; Path=/"}, policyTarget, goodCtx(), policyOwner, "no-matching-cookie"},
		{"path does not match", []string{strings.Replace(goodLine(), "Path=/", "Path=/other", 1)}, policyTarget, goodCtx(), policyOwner, "no-matching-cookie"},
		{"empty value", []string{policyName + "=; Path=/; Secure; Expires=" + futureExpires()}, policyTarget, goodCtx(), policyOwner, "empty-value"},
		{"domain cookie covers subdomain target", []string{goodLine("Domain=.tracker.example")}, "https://sub.tracker.example/", AndroidBridgeContext{Origin: "https://sub.tracker.example", Owner: policyOwner}, policyOwner, ""},
		{"host-only cookie does not cover subdomain", []string{goodLine("Domain=tracker.example")}, "https://sub.tracker.example/", AndroidBridgeContext{Origin: "https://sub.tracker.example", Owner: policyOwner}, policyOwner, "host-mismatch"},
		{"domain cookie of a sibling suffix", []string{goodLine("Domain=.racker.example")}, policyTarget, goodCtx(), policyOwner, "host-mismatch"},
		{"host mismatch", []string{goodLine("Domain=other.example")}, policyTarget, goodCtx(), policyOwner, "host-mismatch"},
		{"malformed empty name", []string{"=x; Path=/"}, policyTarget, goodCtx(), policyOwner, "malformed-cookie"},
		{"nameless cookie ignored", []string{"nameless; domain=tracker.example; path=/", goodLine()}, policyTarget, goodCtx(), policyOwner, ""},
		{"nameless only", []string{"nameless; path=/"}, policyTarget, goodCtx(), policyOwner, "no-matching-cookie"},
		{"path missing", []string{policyName + "=tok; Secure; Expires=" + futureExpires()}, policyTarget, goodCtx(), policyOwner, "path-missing-or-invalid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := selectAndroidClearanceAt(policyNow, tc.lines, tc.target, tc.ctx, tc.owner, policyName, policyUA, androidFirstPartyVariant)
			if reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
			if tc.want != "" {
				if got != nil {
					t.Fatalf("rejected but returned %+v", got)
				}
				return
			}
			if got == nil || got.Cookie == nil {
				t.Fatal("accepted but no cookie")
			}
			c := got.Cookie
			if c.Name != policyName || c.Value == "" || !c.Secure || c.Path == "" || c.Expires <= policyNow.Unix() {
				t.Errorf("cookie = %+v", c)
			}
			if got.Context.Origin != tc.ctx.Origin || got.Context.Owner != policyOwner {
				t.Errorf("context = %+v", got.Context)
			}
			if got.Context.FirstPartyLimited != strings.Contains(strings.Join(tc.lines, ";"), "Partitioned") {
				t.Errorf("FirstPartyLimited = %v", got.Context.FirstPartyLimited)
			}
		})
	}
}

func TestSelectAndroidClearanceMaxAgeExpires(t *testing.T) {
	got, reason := selectAndroidClearanceAt(policyNow, []string{policyName + "=tok; Path=/; Secure; Max-Age=3600"}, policyTarget, goodCtx(), policyOwner, policyName, policyUA, false)
	if reason != "" {
		t.Fatal(reason)
	}
	if want := policyNow.Add(time.Hour).Unix(); got.Cookie.Expires != want {
		t.Errorf("Expires = %d, want %d", got.Cookie.Expires, want)
	}
}

// A Partitioned cookie is never an ordinary jar cookie: the first-party
// variant marks it through the context, and the strict one rejects it.
func TestSelectAndroidClearancePartitionedByVariant(t *testing.T) {
	line := goodLine("Partitioned")
	strict, reason := selectAndroidClearanceAt(policyNow, []string{line}, policyTarget, goodCtx(), policyOwner, policyName, policyUA, false)
	if strict != nil || reason != "partitioned-not-allowed" {
		t.Errorf("strict = %v, %q", strict, reason)
	}
	fp, reason := selectAndroidClearanceAt(policyNow, []string{line}, policyTarget, goodCtx(), policyOwner, policyName, policyUA, true)
	if reason != "" || fp == nil || !fp.Context.FirstPartyLimited {
		t.Errorf("first-party = %+v, %q", fp, reason)
	}
}

func TestSelectAndroidClearanceArguments(t *testing.T) {
	for _, tc := range []struct{ name, cookie, ua, want string }{
		{"empty name", "", policyUA, "cookie-name-empty"},
		{"empty ua", policyName, "", "ua-empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, reason := selectAndroidClearanceAt(policyNow, []string{goodLine()}, policyTarget, goodCtx(), policyOwner, tc.cookie, tc.ua, false)
			if reason != tc.want {
				t.Errorf("reason = %q", reason)
			}
		})
	}
}

func TestSelectAndroidClearanceReasonsLeakNoSecret(t *testing.T) {
	_, reason := selectAndroidClearanceAt(policyNow, []string{policyName + "=SECRETVALUE; Path=/"}, policyTarget, goodCtx(), policyOwner, policyName, policyUA, false)
	if strings.Contains(reason, "SECRETVALUE") {
		t.Fatalf("reason leaks the value: %q", reason)
	}
}

func TestSelectAndroidClearancePublicUsesVariant(t *testing.T) {
	line := policyName + "=tok; Path=/; Secure; Partitioned; Max-Age=3600"
	got, reason := SelectAndroidClearance([]string{line}, policyTarget, goodCtx(), policyOwner, policyName, policyUA)
	if androidFirstPartyVariant {
		if reason != "" || got == nil || !got.Context.FirstPartyLimited {
			t.Errorf("first-party variant: %+v, %q", got, reason)
		}
		return
	}
	if got != nil || reason != "partitioned-not-allowed" {
		t.Errorf("strict variant: %+v, %q", got, reason)
	}
}

func TestSelectAndroidClearanceDomainKind(t *testing.T) {
	for _, tc := range []struct{ attr, want string }{
		{"", "tracker.example"},
		{"Domain=tracker.example", "tracker.example"},
		{"Domain=.tracker.example", ".tracker.example"},
	} {
		line := goodLine()
		if tc.attr != "" {
			line = goodLine(tc.attr)
		}
		got, reason := selectAndroidClearanceAt(policyNow, []string{line}, policyTarget, goodCtx(), policyOwner, policyName, policyUA, false)
		if reason != "" || got.Cookie.Domain != tc.want {
			t.Errorf("%q: domain %v, reason %q, want %q", tc.attr, got, reason, tc.want)
		}
	}
}
