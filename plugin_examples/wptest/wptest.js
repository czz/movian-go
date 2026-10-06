/*
 * wptest — webpopup chain test (no API keys needed).
 *
 * Exercises the whole popup contract against https://httpbin.org:
 *
 *   1. webpopup    — trap-URL capture + query args (httpbin redirect)
 *   2. webuseragent— the UA the webview browsed with
 *   3. webcookies  — read the webview cookie jar (native jar where
 *                    the backend exposes it: libsoup linux, CDP
 *                    windows — HttpOnly included there)
 *   4. injectcookies — plant a cookie into movian's HTTP jar and prove
 *                    it rides OUR stack (httpbin /cookies echoes what
 *                    the client sent)
 *   5. webbrowser  — detached, non-blocking browser window
 *
 * Load: movian -p plugin_examples/wptest wptest:start
 * (windows: movian32.exe -p <plugin dir> wptest:start)
 */

var page  = require('movian/page');
var http  = require('movian/http');
var popup = require('native/popup');

var BASE = "https://httpbin.org";
var TRAP = BASE + "/movian-done";  // trap prefix — httpbin 404s it,
                                   // but navigation still lands there

var n = 0;

function say(page, title, extra) {
  print("[wptest] " + title + (extra ? " — " + extra : ""));
  page.appendItem("wptest:noop:" + (n++), "separator", {
    title: title + (extra ? " — " + extra : "")
  });
}

new page.Route("wptest:start", function(page) {
  page.type = "directory";
  page.metadata.title = "webpopup test";

  // -- 1. trap URL + args --------------------------------------------------
  var dest = TRAP + "?code=TEST-ARG-42&state=ok";
  var r = popup.webpopup(
    BASE + "/redirect-to?url=" + encodeURIComponent(dest),
    "wptest — webpopup",
    TRAP);

  say(page, "webpopup result", r.result);
  if (r.result == "unsupported") {
    say(page, "webpopup not built in", "tag off — nothing else to test");
    return;
  }
  if (r.result == "trapped") {
    say(page, "trappedUrl", r.trappedUrl);
    say(page, "args.code", String(r.args.code));
    say(page, "args.state", String(r.args.state));
  }

  // -- 2. webview user agent -----------------------------------------------
  var ua = popup.webuseragent();
  say(page, "webuseragent", ua);

  // -- 3. webview cookie jar -------------------------------------------------
  // What's in the jar depends on the site the popup visited. (Some
  // origins' cookies are dropped by WebKit policy — an empty jar is
  // still a correct answer.)
  var jar = popup.webcookies(BASE + "/");
  var keys = [];
  for (var k in jar) keys.push(k);
  say(page, "webcookies jar", keys.length ? JSON.stringify(jar)
                                        : "(empty)");

  // -- 4. inject into movian's HTTP stack and verify ------------------------
  // The injected cookie must come back in movian's own request — this
  // is the cf_clearance handoff shape ($ua pins the UA per domain).
  var inj = { wptest_injected: "yes", $ua: ua };
  popup.injectcookies(BASE + "/", inj);
  var resp = http.request(BASE + "/cookies", {});
  say(page, "movian http /cookies", String(resp).slice(0, 400));
});

new page.Route("wptest:browser", function(page) {
  page.type = "directory";
  page.metadata.title = "webbrowser test";
  popup.webbrowser(BASE + "/html", "wptest — detached browser");
  say(page, "detached window opened", BASE + "/html");
});

// Interactive input test — opens a real HTML form and stays open
// until it is submitted (POST → /post). Drive it with Movian's own
// input: remote arrows/OSK/keyboard all land in the embedded page.
//   - Tab / nav keys: move between fields
//   - type: OSK or physical keyboard fills the focused input
//   - Enter on "Submit order": navigates to the trap → "trapped"
//   - Back/Cancel: closes → "userclose"
new page.Route("wptest:manual", function(page) {
  page.type = "directory";
  page.metadata.title = "webpopup input test";
  var r = popup.webpopup(BASE + "/forms/post",
                         "wptest — type + submit", BASE + "/post");
  say(page, "webpopup result", r.result);
  if (r.result == "trapped")
    say(page, "trappedUrl", r.trappedUrl);
});

// wptest:input — input-visibility harness: a data: page that
// console.log's every DOM event (keydown/click/focusin + the
// focused element id). Console messages surface in the CEF log as
// [INFO:CONSOLE..] — proves events cross Movian → CEF → DOM.
new page.Route("wptest:input", function(page) {
  page.type = "directory";
  page.metadata.title = "webpopup input probe";
  var doc = "<!doctype html><body style='background:#204080;color:#fff'>" +
    "<input id='a' placeholder='a'><input id='b' placeholder='b'>" +
    "<button id='ok' onclick=\"location='https://movian.done/finish'\">done</button>" +
    "<scr" + "ipt>" +
    "addEventListener('keydown',e=>console.log('EVKEY '+e.key+' act='+(document.activeElement||{}).id));" +
    "addEventListener('click',e=>console.log('EVCLICK '+(e.target||{}).id),true);" +
    "addEventListener('input',e=>console.log('EVVAL '+(e.target||{}).id+'='+e.target.value));" +
    "addEventListener('focusin',e=>console.log('EVFOCUS '+(e.target||{}).id));" +
    "document.getElementById('a').focus();" +
    "</scr" + "ipt></body>";
  var r = popup.webpopup("data:text/html," + encodeURIComponent(doc),
                         "wptest input", "https://movian.done/");
  say(page, "webpopup result", r.result);
  if (r.result == "trapped")
    say(page, "trappedUrl", r.trappedUrl);
});

new page.Route("wptest:noop:(.*)", function(page, x) {
  page.type = "directory";
  page.metadata.title = "wptest";
});
