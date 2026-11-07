package ui

import (
	"reflect"
	"strings"
	"testing"
)

func delJSON(deleted string) string {
	return `{"deleted":[` + deleted + `],"skipped":{"expired":1,"otherHost":0,"otherPath":0,"filtered":2}}`
}

func TestAndroidDeleteCookiesCountsDeleted(t *testing.T) {
	f := &fakeAndroidJNI{delRaw: delJSON(`"cf_clearance","bb_session"`)}
	b := newTestBridge(f)
	got := b.deleteCookies("Tracker.Example", "/forum", map[string]bool{"cf_clearance": true, "bb_session": true})
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if f.delHost != "tracker.example" || f.delPath != "/forum" {
		t.Errorf("host/path = %q %q", f.delHost, f.delPath)
	}
	if want := []string{"bb_session", "cf_clearance"}; !reflect.DeepEqual(f.delNames, want) {
		t.Errorf("names = %v, want sorted %v", f.delNames, want)
	}
}

func TestAndroidDeleteCookiesNoNamesMeansAll(t *testing.T) {
	f := &fakeAndroidJNI{delRaw: delJSON(`"a"`)}
	b := newTestBridge(f)
	if got := b.deleteCookies("tracker.example", "", nil); got != 1 {
		t.Fatalf("count = %d", got)
	}
	if len(f.delNames) != 0 {
		t.Errorf("names = %v, want none", f.delNames)
	}
	if f.delPath != "/" {
		t.Errorf("empty path should become /, got %q", f.delPath)
	}
}

func TestAndroidDeleteCookiesFailureIsZero(t *testing.T) {
	for _, raw := range []string{"", "not json", `{"deleted":"x"}`, `[]`} {
		f := &fakeAndroidJNI{delRaw: raw}
		if got := newTestBridge(f).deleteCookies("tracker.example", "/", nil); got != 0 {
			t.Errorf("raw %q: count = %d, want 0", raw, got)
		}
	}
}

func TestAndroidDeleteCookiesEmptyHostSkipsJNI(t *testing.T) {
	f := &fakeAndroidJNI{delRaw: delJSON(`"a"`)}
	if got := newTestBridge(f).deleteCookies("", "/", nil); got != 0 || f.delCalls != 0 {
		t.Fatalf("count=%d calls=%d", got, f.delCalls)
	}
}

func TestAndroidDeleteCookiesDebugOffLogsNothing(t *testing.T) {
	logs := clearanceLogSetup(t, false)
	f := &fakeAndroidJNI{delRaw: delJSON(`"cf_clearance"`)}
	newTestBridge(f).deleteCookies("tracker.example", "/", nil)
	if got := logs(); got != "" {
		t.Fatalf("debug off logged:\n%s", got)
	}
}

func TestAndroidDeleteCookiesDebugOnLogsWhatWasDeleted(t *testing.T) {
	logs := clearanceLogSetup(t, true)
	f := &fakeAndroidJNI{delRaw: delJSON(`"cf_clearance","bb_session"`)}
	newTestBridge(f).deleteCookies("tracker.example", "/forum", map[string]bool{"cf_clearance": true})
	out := logs()
	for _, want := range []string{
		"clearance", "android deletecookies", `host="tracker.example"`, `path="/forum"`,
		`names=["cf_clearance"]`, `deleted=["cf_clearance" "bb_session"]`, "count=2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "value=") {
		t.Errorf("log carries a value:\n%s", out)
	}
}

func TestAndroidDeleteCookiesDebugOnLogsFailure(t *testing.T) {
	logs := clearanceLogSetup(t, true)
	f := &fakeAndroidJNI{delRaw: ""}
	newTestBridge(f).deleteCookies("tracker.example", "/", nil)
	if out := logs(); !strings.Contains(out, "android deletecookies failed") {
		t.Errorf("log lacks failure line:\n%s", out)
	}
}

func TestAndroidDeleteOutcome(t *testing.T) {
	d, ok := androidDeleteOutcome(delJSON(`"a","b"`))
	if !ok || !reflect.DeepEqual(d, []string{"a", "b"}) {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := androidDeleteOutcome(""); ok {
		t.Error("empty accepted")
	}
}
