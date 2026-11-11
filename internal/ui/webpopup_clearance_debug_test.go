package ui

import (
	"strings"
	"sync"
	"testing"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// clearanceLogSetup wires webpopupDeps to a manager whose trace output is
// captured; debug is the "Debug all HTTP requests" setting.
func clearanceLogSetup(t *testing.T, debug bool) func() string {
	t.Helper()
	fam := facore.NewFileAccessManager(nil, nil)
	g := gconf.New()
	g.EnableHTTPDebug.Store(debug)
	fam.SetGconf(g)
	ts := trace.NewTraceSystem(propcore.NewPropManager())
	ts.Start(t.TempDir(), "clearance-ui-test")
	t.Cleanup(ts.Fini)
	var mu sync.Mutex
	var lines []string
	ts.SetPlatformLogger(func(_ int, prefix, line string) {
		mu.Lock()
		lines = append(lines, prefix+" "+line)
		mu.Unlock()
	})
	fam.SetTraceSystem(ts)
	old := webpopupDeps.fam
	webpopupDeps.fam = fam
	t.Cleanup(func() { webpopupDeps.fam = old })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
}

func partitionedLine() string { return liveLine() + "; Partitioned" }

// Debug off: popup, cookie read, accept and reject write nothing.
func TestAndroidBridgeDebugOffLogsNothing(t *testing.T) {
	logs := clearanceLogSetup(t, false)
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA,
		cookies: []string{liveLine()}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 1 {
		t.Fatalf("accept path broken: %v", got)
	}
	f.cookies = []string{partitionedLine(), "x=y; Path=/"}
	readCookies(b, policyOwner, policyTarget, policyName)
	readCookies(b, "plugin.other", policyTarget, policyName)
	f.cookieRaw = "not json"
	readCookies(b, policyOwner, policyTarget, policyName)
	if got := logs(); got != "" {
		t.Fatalf("debug off logged:\n%s", got)
	}
}

// Debug on, accept path: popup result, every raw cookie-info line and the
// accepted cookie appear in full.
func TestAndroidBridgeDebugOnAccept(t *testing.T) {
	logs := clearanceLogSetup(t, true)
	line := liveLine()
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA,
		cookies: []string{"other=zzz; Path=/", line}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())
	readCookies(b, policyOwner, policyTarget, policyName)
	out := logs()
	for _, want := range []string{
		"clearance", "android popup result", policyOrigin, policyOwner,
		"other=zzz", line, "android select accepted", `name="cf_clearance"`, `value="tok"`,
		`domain="tracker.example"`, `path="/"`, "expire=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// Debug on, reject path: a Partitioned cookie names its reason (strict build)
// and still shows the raw line.
func TestAndroidBridgeDebugOnRejectPartitioned(t *testing.T) {
	if androidFirstPartyVariant {
		t.Skip("strict variant only")
	}
	logs := clearanceLogSetup(t, true)
	line := partitionedLine()
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA,
		cookies: []string{line}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())
	if got := readCookies(b, policyOwner, policyTarget, policyName); len(got) != 0 {
		t.Fatalf("partitioned cookie delivered: %v", got)
	}
	out := logs()
	for _, want := range []string{"android select rejected: partitioned-not-allowed", line} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// First-party variant: the Partitioned cookie is accepted and flagged.
func TestAndroidBridgeDebugOnAcceptPartitionedFirstParty(t *testing.T) {
	if !androidFirstPartyVariant {
		t.Skip("first-party variant only")
	}
	logs := clearanceLogSetup(t, true)
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA,
		cookies: []string{partitionedLine()}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())
	readCookies(b, policyOwner, policyTarget, policyName)
	if out := logs(); !strings.Contains(out, "first-party-limited)=true") {
		t.Errorf("log lacks the flag:\n%s", out)
	}
}

// Debug on, other rejects: a foreign owner and unreadable JSON are logged.
func TestAndroidBridgeDebugOnOtherRejects(t *testing.T) {
	logs := clearanceLogSetup(t, true)
	f := &fakeAndroidJNI{popupRaw: popupJSON("cookie", "", policyOrigin), ua: policyUA,
		cookies: []string{liveLine()}}
	b := newTestBridge(f)
	runTestPopup(b, policyOwner, cookieWait())
	readCookies(b, "plugin.other", policyTarget, policyName)
	f.cookieRaw = "not json"
	readCookies(b, policyOwner, policyTarget, policyName)
	out := logs()
	for _, want := range []string{"android cookie read refused", "android cookies unreadable", "not json"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}
