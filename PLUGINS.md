# Movian Go — Plugin Development

Plugins are JavaScript bundles running on **goja** (replacing upstream's

The API mirrors upstream Movian's plugin model: a `plugin.json`
manifest, a `plugin` host object, the `page` responder API and a set of
`require()`-able native modules.

## Layout of a plugin

```
myplugin/
├── plugin.json        # manifest (required)
├── plugin.js          # entry point (named by manifest "file")
└── ...                # any extra files (views, images, js modules)
```

## plugin.json

Verified fields (parsed by `PluginControl` in
`internal/plugins/plugin_manager.go`):

```json
{
  "type": "ecmascript",
  "id": "example_music",
  "file": "plugin.js",
  "apiversion": 2,
  "title": "My plugin",
  "version": "1.0.0",
  "description": "Long description",
  "synopsis": "Short description",
  "author": "you",
  "category": "video",
  "icon": "icon.png",
  "downloadURL": "https://…/myplugin.zip",
  "showtimeVersion": "…",
  "debug": false,
  "memory-size": 0,
  "stack-size": 0,
  "glwviews": [],
  "entitlements": {},
  "control": {}
}
```

- `type`: `ecmascript` (current) or `javascript` (legacy alias)
- `id`: unique reverse-DNS-style id
- `file`: entry JS file relative to the plugin dir
- `apiversion`: use `2`

## The `plugin` object

Inside the entry file, `this` is the plugin object:

```js
(function(plugin) {
  // Service = a tile on the home page.
  // createService(title, uriPrefix, type, enabled)
  plugin.createService("My service", "myplugin:main:", "video", true);

  // URI responder — called when the user browses into the URI prefix.
  plugin.addURI("myplugin:main:", function(page) {
    page.type = "directory";
    page.metadata.title = "Root";
    page.appendItem("http://host/file.mp4", "video", { title: "A video" });
    page.loading = false;
  });

  plugin.createSettings = function(page) { /* settings UI */ };
})(this);
```

Other entry points a plugin can install (see `plugin_examples/`):

- `plugin.createSettings` — settings page for the plugin
- item hooks — transform/annotate items on pages (`itemhook` example)
- searchers — `plugin.addSearcher(title, icon, fn)` to appear in global search
- subscriptions — periodic video-source discovery (`subscriptions` example)

## The `page` object

`page` responders receive a page object:

- `page.type` — `"directory"`, `"video"`, …
- `page.metadata.*` — title, icon, etc.
- `page.appendItem(url, type, metadata)` — add a row
  (`type` matches backend: `video`, `audio`, `image`, `directory`…)
- `page.loading = false` — **required** when the page is complete;
  forgetting it leaves the spinner forever (see `async_page_load`)
- `page.appendPassiveItem(type, metadata)` — non-navigable item
- `page.redirect(url)` — bounce to another URI

## Native modules (`require`)

Registered at startup (`internal/ecmascript/register_modules.go`),
mirroring upstream's modules:

| Module | Purpose |
|---|---|
| `native/prop` | prop tree — the reactive data model behind all UI |
| `native/io` | HTTP(S) requests, file I/O (inspects by URL scheme) |
| `native/fs` | filesystem access (respects entitlements) |
| `native/htsmsg` | binary JSON-like message codec (HTSP etc.) |
| `native/kvstore` | per-URL persistent key/value store |
| `native/string` | string/encoding helpers |
| `native/jambalaya` | per-plugin persistent storage |
| `native/sqlite` | SQL via modernc.org/sqlite |
| `native/subtitles` | subtitle search/load |
| `native/metadata` | media metadata queries |
| `native/websocket` | WebSocket client |
| `native/crypto` | hashing/crypto helpers |
| `native/timer` | timers wired to the UI clock |
| `native/scrobble` | playback scrobbling hooks |
| `native/stats` | counters/usage stats |
| `native/console` | `console.log` → Movian log |
| `native/service` | service lifecycle |
| `native/route` | URI routing (`native/route` = addURI machinery) |
| `native/searcher` | global search integration |
| `native/hook` | event hooks |
| `misc` | misc helpers (`misc` module) |
| `popup` | webpopup — a real browser window for OAuth/Cloudflare |
| `native/faprovider` | custom fileaccess schemes (your own `scheme:`) |
| `native/itemhook` | page item transformation hooks |

Plugin code also gets `RichText` helpers, `require()` of sibling `.js`
files inside the plugin directory and `plugin.store` persistence.

## webpopup (OAuth / Cloudflare)

```js
var popup = require('popup');
popup.webpopup('https://example.com/login', {
  trap: 'https://example.com/done'   // closes the window on match
}, function(result) { /* … */ });
```

`webpopup`, `webbrowser`, `webcookies`, `webuseragent` and
`injectcookies` give plugins a real Chromium browser page — one
backend on every desktop platform: **CEF in off-screen mode**, whose
frames are composited as a GLW texture inside Movian's own window
(the per-platform WPE/WebView2/WKWebView backends were dropped).
Used for OAuth and Cloudflare-gated sites. Requires a `cef.pc`
(pkg-config) at build time; without it the stub reports unsupported.
See `plugin_examples/webpopupplugin/`.

`persistWebCookie(url, name)` copies one Secure, expiring webview cookie into
the Core cookie jar together with the webview User-Agent, as a record owned
by the calling plugin. It returns `{value, ua, expires, path, origin}`
(`expires` is Unix seconds) or `null`: for a non-https URL, an IP-literal or
public-suffix host, a cookie that is not Secure, a session cookie, or when
the webview cannot pick one cookie of that name (same-path twins; with
different paths the longest path wins, as in a browser request). A later expiry than 30 days is
cut to 30 days. The record lives next to the ordinary cookies of the shared
jar and never replaces them: `Set-Cookie`, `injectcookies` and
`deletecookies` keep working on the ordinary cookie of the same name, and a
request of another plugin, or one without `persistedCookies`, still sees
only the ordinary cookies. The plugin's own `persistedCookies` request sends
the record **instead of** a same-named ordinary cookie, so a name is never
sent twice; deleting the record brings the ordinary cookie back into effect. `getPersistedWebCookie(url, name)` returns the same shape for
the plugin's own unexpired record, and `deletePersistedWebCookie(url, name)`
removes it (`true` if there was one). The record is never sent by a plain
`http_req`: pass `persistedCookies: true` in the request options, and it is
sent over https to the exact host only, on matching paths, and only to the
plugin that stored it. Such a request skips the response cache. The webview
User-Agent is pinned on the host while the record exists. CEF does not label
a cookie's partition, so partitioned cookies cannot be told apart.

**Debug logging of clearance cookies.** With the developer setting "Debug all
HTTP requests" on (or the debug flag of a single request), Core logs the clearance
path to the `clearance` trace subsystem at debug level, **cookie values in
full**: the Android popup result and each raw WebView cookie line, why the
Android policy accepted or rejected a cookie (for example
`partitioned-not-allowed`), what `persistWebCookie` took, the import accept or
reject with the expiry before and after the 30-day cut, and for a
`persistedCookies` request which records were attached or skipped and why. The
HTTP/1 request trace also shows the `Cookie` header as sent. With the setting
off nothing of this is logged. Treat such a log as secret: the HTTP control
API serves it at `/api/logfile`, so turn the setting off before sharing a log
or leaving a device reachable.

`popup.supportsBrowserLogin()` returns `true` when the webview backend is a
full browser that can carry a login flow (CEF), and `false` when it is not:
the Android popup is clearance-only, and builds without a webview have none.
It is a build-time property of the backend, so do not guess it from the user
agent or the platform name.

## Development workflow

```bash
# load a dev plugin directory at startup
./movian-go -p /path/to/myplugin

# the dev plugin auto-reloads when its files change
```

Logs go to the Movian log (`log/movian-go-0.log` next to the CWD, or
stdout with the right verbosity). `console.log` inside a plugin lands
there too.

## Distribution

A plugin is a **zip** containing `plugin.json` at its root. Movian
auto-detects zip plugin archives on browse/install
(`ProbeForAutoInstall`). Plugins can also be listed in a repository
JSON served over HTTP(S) and installed from the UI's plugin section.

Plugin identity is `<plugin-id>@<origin>`. Repository origins are the first
eight MD5 bytes of the resolved package URL, encoded as 16 lowercase
hexadecimal characters; local ZIP installs use `local`, and development
plugins use `dev`. Opening a ZIP is intentionally a local installation and
does not search for or replace a repository plugin with the same ID.
Repository install/upgrade actions instead reuse their selected FQID and
origin.


### HTTP Control API

Open `http://127.0.0.1:42000/api/plugin/install` in a browser, or use the
device's network address. Submit a remote ZIP URL or select a local ZIP
file; no installation confirmation is required. The page displays
progress and the installation result without navigating away.

`/api/plugin/install?id=<fqid>` retains repository-based installation.
To install a ZIP directly, use
`/api/plugin/install?url=<URL-encoded-zip-URL>` or POST an
`application/x-www-form-urlencoded` body with a `url` field. The remote URL
must return a ZIP containing a Movian `plugin.json` manifest.

HTTP Control's ZIP URL and upload endpoints do not require authentication by
default. This known exposure is deferred, not resolved here; HTTP Control is
not safe for untrusted networks. Restrict network access to trusted clients.

```sh
curl --get 'http://127.0.0.1:42000/api/plugin/install' \
  --data-urlencode 'url=https://example.com/plugin.zip'
```

To upload a local ZIP, POST its bytes with `Content-Type: application/zip`.
Uploads use the HTTP server's 16 MiB request-body limit; the browser page
checks the selected file size before sending it.

```sh
curl 'http://127.0.0.1:42000/api/plugin/install' \
  -H 'Content-Type: application/zip' \
  --data-binary @plugin.zip
```

HTTP Control uses plain HTTP; limit access to the port to the intended
clients.

Successful installs are saved under `mrp/installed` and loaded immediately.
The shared package-download path enforces a 64 MiB limit for every package
source read through fileaccess, including repository, URL, and local-updater
packages. HTTP ZIP uploads additionally have the HTTP server's 16 MiB
request-body limit.

Replacement writes a transaction journal beside the installed archive. The
transaction is marked committed only after the replacement has loaded. Startup
keeps a valid committed archive and retries transaction cleanup; before commit,
it restores a valid prior archive when one exists, or removes an incomplete
first install. Recovery reports errors while continuing to load other healthy
plugins. These guarantees cover process interruption and use file syncs for
archive/journal updates; directory syncing is best-effort on platforms that do
not support it, so this is not a universal power-loss durability guarantee.
Legacy backup recovery preserves a valid canonical archive rather than
overwriting it with an older backup. If a replacing transaction's backup
cannot be inspected, startup leaves its journal in place for a later retry.
For each FQID, concurrent load/install/remove operations return a busy error;
an unload requested during an active lifecycle transition is applied when that
transition completes. Operations for other FQIDs can proceed independently.

GET without installation parameters displays the page. POST without an
installation source, or with an empty ZIP upload, returns HTTP 400.
Download, archive validation, installation, and load failures return HTTP
500 with an error message.

### Native Movian updater

Plugin updaters do not need the HTTP installer. The
[public update module](https://github.com/Buksa/movian_module/blob/main/update.md)
used by HDRezka reads `Plugin.manifest.downloadURL`, saves the ZIP in
`Core.storagePath/store/updater_package.zip`, and opens its normalized
`search:file:///...` or platform storage URL through the navigator event sink.
After its own confirmation, opening the package installs and reloads it
automatically, matching Movian 6's `plugin_open_file` behavior:
it creates or reuses `<manifest-id>@local` and does not discover a repository
plugin by matching its ID. The public updater sends only the ZIP URL through
the navigator event; that path does not carry the installed plugin's FQID.
It remains a separate local install and can coexist with a loaded repository
plugin of the same manifest ID; it does not upgrade that repository instance.

Go routes package downloads through the registered fileaccess protocols, so
native local-package installs and remote HTTP installs share the same validated
installer with replacement rollback. Installation failures become a native
error page rather than silently leaving an empty page. Repository upgrades use
the repository plugin record directly and preserve its origin; native ZIP
opening retains `@local` semantics because the updater does not provide a safe
origin target.

The updater's JSON check state and post-install marker are written as UTF-8.
`fs.readFileSync` returns the file's byte buffer; use `.toString()` when
parsing JSON. `native/fs.unlink` consumes the post-install marker within the
plugin's normal write ACL. On reload the updater returns to `page:home`.

## Examples

`plugin_examples/` ships working sketches:

- `music` — service + directory page + audio items
- `async_page_load` — async page population
- `settings` — `createSettings`
- `itemhook` — item hooks
- `subscriptions` — subscription source
- `webpopupplugin` — `require('popup')` flows
- `wptest` — misc harness


