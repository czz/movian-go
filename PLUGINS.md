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

## Examples

`plugin_examples/` ships working sketches:

- `music` — service + directory page + audio items
- `async_page_load` — async page population
- `settings` — `createSettings`
- `itemhook` — item hooks
- `subscriptions` — subscription source
- `webpopupplugin` — `require('popup')` flows
- `wptest` — misc harness


