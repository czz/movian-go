// Canonical 1:1 port of src/ecmascript/es_misc.c
//
// Misc + popup native esState.modules. ENABLE_WEBPOPUP is 0 in this port, so
// es_webpopup takes the #else branch — the same outcome the C configure
// produces on this system: linux_webpopup.c requires webkit-1.0 (GTK2
// WebKit), which no longer exists; only webkit2gtk-4.1 (GTK3) is
// available and the GTK2 frontend itself has been removed (GTK2 is
// EOL). ENABLE_PLUGINS is 1.
package ecmascript

import (
	"fmt"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/blobcache"
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/keyring"
	misc "github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
	"github.com/czz/movian-go/internal/notifications"
	"github.com/czz/movian-go/internal/ui"
)

// Seam: blobcache singleton (C: blobcache_* operate on the global cache).

// Seam: plugin_select_view — C: plugins.h. Set by the plugins package
// init wiring (plugins imports ecmascript, so the call goes through a
// function variable to avoid the import cycle).
// ---------------------------------------------------------------------------
// es_webpopup — C: es_misc.c:41-89
// When built with the webpopup tag (linux + webkit2gtk-4.1) the popup
// is live; otherwise the C configure's ENABLE_WEBPOPUP=0 #else branch.
// ---------------------------------------------------------------------------

func esWebpopup(ctx *gaftape.Context) int {
	ctx.PushObject()

	if !ui.WebpopupAvailable {
		ctx.PushString("unsupported")
		ctx.PutPropString(-2, "result")
		return 1
	}

	url := ctx.SafeToString(0)
	title := ctx.SafeToString(1)
	trap := ctx.SafeToString(2)

	wr := ui.WebpopupCreate(url, title, trap)

	var t string
	switch wr.ResultCode {
	case ui.WebpopupTrappedURL:
		t = "trapped"
	case ui.WebpopupClosedByUser:
		t = "userclose"
	case ui.WebpopupLoadError:
		t = "neterror"
	default:
		t = "error"
	}
	ctx.PushString(t)
	ctx.PutPropString(-2, "result")

	if wr.Trapped.URL != "" {
		ctx.PushString(wr.Trapped.URL)
		ctx.PutPropString(-2, "trappedUrl")
	}

	ctx.PushObject()
	if wr.Trapped.QArgs != nil {
		for _, hh := range wr.Trapped.QArgs.List {
			ctx.PushString(hh.Value)
			ctx.PutPropString(-2, hh.Key)
		}
	}
	ctx.PutPropString(-2, "args")

	ui.WebpopupResultFree(wr)
	return 1
}

// es_webbrowser — C: webbrowser_open. Non-blocking browser window.
func esWebbrowser(ctx *gaftape.Context) int {
	ui.WebbrowserOpen(ctx.SafeToString(0), ctx.SafeToString(1))
	return 0
}

// es_webcookies — the shared webview jar's cookies for a URL
// (HttpOnly included; used to harvest cf_clearance after the user
// passes a challenge in the popup).
func esWebcookies(ctx *gaftape.Context) int {
	cookies := ui.WebpopupCookies(ctx.SafeToString(0))
	ctx.PushObject()
	for k, v := range cookies {
		ctx.PushString(v)
		ctx.PutPropString(-2, k)
	}
	return 1
}

// es_webuseragent — UA string of the last popup webview (cookies
// like cf_clearance are bound to it; pin it per-domain).
func esWebUserAgent(ctx *gaftape.Context) int {
	ctx.PushString(ui.WebpopupUserAgent())
	return 1
}

// es_injectcookies — inject {name:value} into the shared HTTP jar for
// the URL's host, and pin the webview UA on the host when the object
// carries one under "$ua" (cf_clearance is bound to the solving UA).
func esInjectCookies(ctx *gaftape.Context) int {
	url := ctx.SafeToString(0)
	var hbuf [256]byte
	var pbuf [512]byte
	var port int
	misc.UrlSplit(nil, 0, nil, 0, hbuf[:], len(hbuf),
		&port, pbuf[:], len(pbuf), url)
	host := misc.CStr(hbuf[:])
	path := misc.CStr(pbuf[:])
	if q := strings.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	if path == "" {
		path = "/"
	}
	cookies := map[string]string{}
	ua := ""
	if ctx.IsObject(1) {
		ctx.Enum(1, 0)
		for ctx.Next(-1, true) {
			k := ctx.SafeToString(-2)
			v := ctx.SafeToString(-1)
			if k == "$ua" {
				ua = v
			} else {
				cookies[k] = v
			}
			ctx.Pop2()
		}
		ctx.Pop()
	}
	if len(cookies) > 0 {
		esEnv.fam.HTTPInjectCookies(host, path, cookies)
	}
	if ua != "" {
		esEnv.fam.HTTPDomainSetUserAgent(host, ua)
	}
	return 0
}

// ---------------------------------------------------------------------------
// es_getAuthCredentials — C: es_misc.c:90-129
// ---------------------------------------------------------------------------

func esGetAuthCredentials(ctx *gaftape.Context) int {
	source := ctx.SafeToString(0)
	reason := ctx.SafeToString(1)
	query := ctx.ToBoolean(2)
	forcetmp := ctx.ToBoolean(4)

	var id string
	hasID := ctx.IsString(3)
	if hasID {
		id = ctx.ToString(3)
	}

	ec := EsGet(ctx)

	var buf string
	if hasID {
		buf = fmt.Sprintf("plugin-%s-%s", ec.ecID, id)
	} else {
		buf = fmt.Sprintf("plugin-%s", ec.ecID)
	}

	flags := 0
	if query {
		flags |= keyring.FlagQueryUser
	}
	if !forcetmp {
		flags |= keyring.FlagShowRememberMe | keyring.FlagRememberMeSet
	}

	var username, password string
	r := keyring.NotFound
	if esEnv.keyring != nil {
		r = esEnv.keyring.Lookup(buf, &username, &password, nil, nil,
			source, reason, flags)
	}

	if r == 1 {
		ctx.PushFalse()
		return 1
	}

	ctx.PushObject()

	if r == -1 {
		ctx.PushTrue()
		ctx.PutPropString(-2, "rejected")
	} else {
		ctx.PushString(username)
		ctx.PutPropString(-2, "username")

		ctx.PushString(password)
		ctx.PutPropString(-2, "password")
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_message — C: es_misc.c:135-163
// ---------------------------------------------------------------------------

func esMessage(ctx *gaftape.Context) int {
	message := ctx.ToString(0)
	ok := ctx.ToBoolean(1)
	cancel := ctx.ToBoolean(2)

	if !ok && !cancel {
		ok = true // We need to show one button at least
	}

	mflags := notifications.MessagePopupRichText
	if ok {
		mflags |= notifications.MessagePopupOK
	}
	if cancel {
		mflags |= notifications.MessagePopupCancel
	}

	r := 0
	if esEnv.notifMgr != nil {
		r = esEnv.notifMgr.MessagePopup(message, mflags, nil)
	}

	switch r {
	case notifications.MessagePopupOK:
		ctx.PushTrue()
	case notifications.MessagePopupCancel:
		ctx.PushFalse()
	default:
		ctx.PushInt(r)
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_textDialog — C: es_misc.c:169-199
// ---------------------------------------------------------------------------

func esTextDialog(ctx *gaftape.Context) int {
	message := ctx.ToString(0)
	ok := ctx.ToBoolean(1)
	cancel := ctx.ToBoolean(2)

	mflags := notifications.MessagePopupRichText
	if ok {
		mflags |= notifications.MessagePopupOK
	}
	if cancel {
		mflags |= notifications.MessagePopupCancel
	}

	var input string
	r := -1
	if esEnv.notifMgr != nil {
		r = esEnv.notifMgr.TextDialog(message, &input, mflags)
	}

	if r == 1 {
		ctx.PushFalse()
		return 1
	}

	ctx.PushObject()

	if r == -1 || input == "" {
		ctx.PushTrue()
		ctx.PutPropString(-2, "rejected")
	} else {
		ctx.PushString(input)
		ctx.PutPropString(-2, "input")
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_notify — C: es_misc.c:205-212
// ---------------------------------------------------------------------------

func esNotify(ctx *gaftape.Context) int {
	text := ctx.ToString(0)
	delay := int(ctx.ToUint(1))
	icon := ctx.GetString(2)
	if esEnv.notifMgr != nil {
		esEnv.notifMgr.NotifyAdd(nil, notifications.NotifyInfo, icon, delay,
			"%s", text)
	}
	return 0
}

// ---------------------------------------------------------------------------
// es_cachePut / es_cacheGet — C: es_misc.c:218-249
// ---------------------------------------------------------------------------

func esCachePut(ctx *gaftape.Context) int {
	stash := ctx.ToString(0)
	key := ctx.ToString(1)
	buf := ctx.ToBuffer(2)
	maxage := int(ctx.GetUint(3))
	if bc := esEnv.blobCache; bc != nil {
		b := &blobcache.Buf{Ptr: buf, Size: len(buf)}
		bc.Put(key, stash, b, maxage, "", time.Time{}, 0)
	}
	return 0
}

func esCacheGet(ctx *gaftape.Context) int {
	stash := ctx.ToString(0)
	key := ctx.ToString(1)
	var b *blobcache.Buf
	if bc := esEnv.blobCache; bc != nil {
		b = bc.Get(key, stash, 0, nil, nil, nil)
	}
	if b == nil {
		ctx.PushNull()
	} else {
		v := ctx.PushFixedBuffer(b.Size)
		copy(v, b.Ptr[:b.Size])
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_system_ip — C: es_misc.c:255-268
// ---------------------------------------------------------------------------

func esSystemIP(ctx *gaftape.Context) int {
	nis, err := netcore.NetGetInterfaces()
	if err != nil || len(nis) == 0 {
		return 0
	}
	ni := nis[0]

	ctx.PushSprintf("%d.%d.%d.%d",
		ni.IPv4Addr[0], ni.IPv4Addr[1], ni.IPv4Addr[2], ni.IPv4Addr[3])

	return 1
}

// ---------------------------------------------------------------------------
// es_select_view — C: es_misc.c:274-282 (#if ENABLE_PLUGINS)
// ---------------------------------------------------------------------------

func esSelectView(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	filename := ctx.ToString(0)
	if esEnv.pluginSelectView != nil {
		esEnv.pluginSelectView(ec.ecID, filename)
	}
	return 0
}

// ---------------------------------------------------------------------------
// fnlist + modules — C: es_misc.c:309-335
// ---------------------------------------------------------------------------

var fnlistMisc = []gaftape.FunctionListEntry{
	{Key: "cachePut", Value: esCachePut, Nargs: 4},
	{Key: "cacheGet", Value: esCacheGet, Nargs: 2},
	{Key: "systemIpAddress", Value: esSystemIP, Nargs: 0},
	{Key: "selectView", Value: esSelectView, Nargs: 1},
}

var fnlistPopup = []gaftape.FunctionListEntry{
	{Key: "webpopup", Value: esWebpopup, Nargs: 3},
	{Key: "webbrowser", Value: esWebbrowser, Nargs: 2},
	{Key: "webcookies", Value: esWebcookies, Nargs: 1},
	{Key: "webuseragent", Value: esWebUserAgent, Nargs: 0},
	{Key: "injectcookies", Value: esInjectCookies, Nargs: 2},
	{Key: "getAuthCredentials", Value: esGetAuthCredentials, Nargs: 5},
	{Key: "message", Value: esMessage, Nargs: 3},
	{Key: "textDialog", Value: esTextDialog, Nargs: 3},
	{Key: "notify", Value: esNotify, Nargs: 3},
}

// ES_MODULE("misc", fnlist_misc) + ES_MODULE("popup", fnlist_popup)
func registerEsMisc() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "misc",
		Functions: fnlistMisc,
	})
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "popup",
		Functions: fnlistPopup,
	})
}
