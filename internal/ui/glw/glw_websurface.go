//go:build cef

package glw

// glw_websurface.go — `websurface` widget: the embedded CEF webview
// presented inside Movian's own window. The widget is a fullscreen
// quad fed by the session's newest OSR frame; while it is focused,
// Movian's input flows to the page: EVENT_UNICODE/OSK text becomes
// typed characters, nav actions become page keys or the close
// gesture.
//
// No C counterpart: upstream never embedded webkit (its popup was a
// separate GTK window). The render path mirrors glw_video_opengl's
// texture-quad pattern.
//
// Threading: WebsurfaceShow/Hide may run on any thread (grMutex
// held); the Hooks are invoked on the UI thread only.

import (
	"sync"
	"unsafe"

	eventpkg "github.com/czz/movian-go/internal/event"
	imagepkg "github.com/czz/movian-go/internal/image"
	tracepkg "github.com/czz/movian-go/internal/trace"
)

// WebsurfaceHooks — the seam to the CEF session (implemented by
// package ui in webpopup_cef.go). Key takes an XKB keysym; Text
// takes a decoded unicode char — translation to key events happens
// on the session side (libcef is cgo-bound).
type WebsurfaceHooks interface {
	// Frame returns the newest frame (BGRA bytes, stride in bytes)
	// plus a sequence number so the widget skips unchanged uploads.
	// data may be nil before the first frame.
	Frame() (data []byte, w, h, stride int, seq uint32)
	// Text delivers a typed character (press+release).
	Text(r rune)
	// Key delivers a named key (press+release); keyCode is a WPE
	// keysym.
	Key(keyCode uint32)
	// Pointer delivers a mouse event (1=motion, 2=button).
	Pointer(typ uint8, x, y int32, button, state, mods uint32)
	// SetSize forwards the surface size to the view backend.
	SetSize(w, h int)
	// OskField reports whether a DOM text field is focused (tracked
	// by the session's injected focusin/focusout script) — on
	// remote-only setups OK opens Movian's OSK instead of Enter.
	OskField() (value string, password, ok bool)
	// SetText writes the OSK's live string into the focused field.
	SetText(str string)
	// CloseRequest — the user backed out (Back/Esc).
	CloseRequest()
	// Detached — the widget is gone; stop feeding it.
	Detached()
}

var (
	glwWebMu      sync.Mutex
	glwWebHooks   WebsurfaceHooks
	glwWebCurRoot *glwRoot
	glwWebW       *Glw
)

// glwWebsurface — widget instance; Glw embeds first like the C class
// structs embed glw_t.
type glwWebsurface struct {
	w       Glw
	hooks   WebsurfaceHooks
	tex     GlwBackendTexture
	quad    glwRenderer
	pm      *imagepkg.Pixmap
	lastSeq uint32
	pxW     int // widget rect px — localX/localY are normalized
	pxH     int // (-1..1) and must be scaled to surface pixels
}

var glwWebsurfaceClass = glwClass{
	gcName:         "websurface",
	gcInstanceSize: int(unsafe.Sizeof(glwWebsurface{})),
	gcNew: func(parent *Glw) *Glw {
		ws := &glwWebsurface{}
		return &ws.w
	},
	gcCtor:         websurfaceCtor,
	gcDtor:         websurfaceDtor,
	gcLayout:       websurfaceLayout,
	gcRender:       websurfaceRender,
	gcSendEvent:    websurfaceEvent,
	gcPointerEvent: websurfacePointer,
	gcUpdateText:   websurfaceUpdateText,
	gcBubbleEvent:  websurfaceBubble,
}

func websurfaceCtor(w *Glw) {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	glwWebMu.Lock()
	ws.hooks = glwWebHooks
	glwWebMu.Unlock()
	glwRendererSetupQuad(&ws.quad)
	if ws.hooks != nil {
		// Outrank any 'focusable:' weight in the underlying UI —
		// an autofocus contender there would otherwise steal the
		// focus path and swallow every event meant for the page.
		w.glwFocusWeight = 10
		glwWebW = w
		glwWebFocusGrab = w
		glwFocusSet(w.glwRoot, w, glwFocusSetInteractive,
			"websurface")
	}
	// Positions are widget-normalized (-1..1) — the rc matrix maps
	// them onto the widget rect; without them the quad is a point.
	glwRendererVtxPos(&ws.quad, 0, -1.0, -1.0, 0.0)
	glwRendererVtxPos(&ws.quad, 1, 1.0, -1.0, 0.0)
	glwRendererVtxPos(&ws.quad, 2, 1.0, 1.0, 0.0)
	glwRendererVtxPos(&ws.quad, 3, -1.0, 1.0, 0.0)
	// Same texture orientation as the video quad (top-left origin
	// SHM data → flip T).
	glwRendererVtxSt(&ws.quad, 0, 0, 1)
	glwRendererVtxSt(&ws.quad, 1, 1, 1)
	glwRendererVtxSt(&ws.quad, 2, 1, 0)
	glwRendererVtxSt(&ws.quad, 3, 0, 0)
}

func websurfaceDtor(w *Glw) {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	if glwWebW == w {
		glwWebW = nil
	}
	if glwWebFocusGrab == w {
		glwWebFocusGrab = nil
	}
	glwTexDestroy(w.glwRoot, &ws.tex)
	glwRendererFree(&ws.quad)
}

// websurfaceLayout — the surface covers its whole rect and forwards
// the size to the view backend.
func websurfaceLayout(w *Glw, rc *glwRctx) {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	ws.pxW = int(rc.rcWidth)
	ws.pxH = int(rc.rcHeight)
	if ws.hooks != nil {
		ws.hooks.SetSize(ws.pxW, ws.pxH)
	}
}

// websurfaceRender — upload the newest frame and draw it stretched
// over the widget rect; repaints keep flowing while the surface is
// alive (JS animations, live pages).
var wsRenderLogged int
var wsPointerLogged int

func websurfaceRender(w *Glw, rc *glwRctx) {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	if glwIsFocusableOrClickable(w) {
		// C pattern (glw_dummy_render etc): the hit-test matrix
		// lives in the render ctx — without it pointer events can
		// never be unprojected onto this widget (clicks went to
		// nothing).
		glwStoreMatrix(w, rc)
	}
	if ws.hooks == nil {
		return
	}
	data, fw, fh, stride, seq := ws.hooks.Frame()
	if wsRenderLogged < 8 || (ws.tex.Textures[0] != 0 && wsRenderLogged < 20) {
		wsRenderLogged++
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW",
			"websurface render rc=%dx%d a=%.2f frame=%dx%d seq=%d tex=%d",
			int(rc.rcWidth), int(rc.rcHeight), rc.rcAlpha*w.glwAlpha,
			fw, fh, seq, ws.tex.Textures[0])
	}
	if data != nil && seq != ws.lastSeq {
		ws.lastSeq = seq
		pm := ws.pm
		if pm == nil || pm.Width != fw || pm.Height != fh {
			pm = imagepkg.PixmapCreate(fw, fh, imagepkg.PixmapBGRA, 0)
			pm.Flags = imagepkg.PixmapOpaque
			ws.pm = pm
		}
		bpp := fw * 4
		if stride == bpp && len(data) >= fh*bpp {
			copy(pm.Data, data[:fh*bpp])
		} else {
			for y := 0; y < fh; y++ {
				copy(pm.Data[y*bpp:y*bpp+bpp],
					data[y*stride:y*stride+bpp])
			}
		}
		glwTexUpload(w.glwRoot, &ws.tex, pm, 0)
	}
	// Repaints must keep flowing even before the first frame — an
	// early return here would stop the render loop forever and the
	// surface would stay black no matter what arrives later.
	glwNeedRefresh(w.glwRoot, 0)
	if ws.tex.Textures[0] == 0 {
		return
	}
	glwRendererDraw(&ws.quad, w.glwRoot, rc, &ws.tex, nil, nil, nil,
		rc.rcAlpha*w.glwAlpha, 0, nil)
}

// XKB keysyms (the transport key space shared with the backend).
const (
	wsKeyBackSpace = 0xff08
	wsKeyTab       = 0xff09
	wsKeyReturn    = 0xff0d
	wsKeyEscape    = 0xff1b
	wsKeyDelete    = 0xffff
	wsKeyHome      = 0xff50
	wsKeyLeft      = 0xff51
	wsKeyUp        = 0xff52
	wsKeyRight     = 0xff53
	wsKeyDown      = 0xff54
	wsKeyPageUp    = 0xff55
	wsKeyPageDown  = 0xff56
	wsKeyEnd       = 0xff57
)

// websurfaceEvent — Movian events → page input. Everything is
// consumed while the surface is focused; Back/Cancel closes it.
var wsEventLogged int

func websurfaceEvent(w *Glw, e *eventpkg.Event) int {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	if ws.hooks == nil {
		return 0
	}
	if w.glwRoot != nil && w.glwRoot.grOskWidget == w {
		// Movian's OSK is open for this surface: text-entry events
		// are redirected to the requesting widget (here) but the
		// authoritative string flows through gcUpdateText —
		// swallow these so Enter can't submit the form mid-edit.
		return 1
	}
	if wsEventLogged < 30 {
		wsEventLogged++
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW",
			"websurface event type=%d payload=%q actions=%v",
			e.Type, e.Payload, e.Actions)
	}

	switch e.Type {
	case eventpkg.EVENT_UNICODE:
		if ei, ok := e.Concrete().(*eventpkg.EventInt); ok &&
			ei.Val > 0 {
			ws.hooks.Text(rune(ei.Val))
		}
		return 1

	case eventpkg.EVENT_INSERT_STRING, eventpkg.EVENT_KEYDESC:
		s := e.Payload
		if e.Type == eventpkg.EVENT_KEYDESC {
			if kc := websurfaceKeydesc(s); kc != 0 {
				ws.hooks.Key(kc)
			}
		} else {
			for _, r := range s {
				ws.hooks.Text(r)
			}
		}
		return 1
	}

	switch {
	case e.IsAction(eventpkg.ACTION_BS):
		// Keyboard Backspace arrives as [BS, NAV_BACK] — it must edit
		// the field, not close the popup. Checked before NAV_BACK.
		ws.hooks.Key(wsKeyBackSpace)
	case e.IsAction(eventpkg.ACTION_NAV_BACK),
		e.IsAction(eventpkg.ACTION_CANCEL):
		ws.hooks.CloseRequest()
	case e.IsAction(eventpkg.ACTION_LEFT):
		ws.hooks.Key(wsKeyLeft)
	case e.IsAction(eventpkg.ACTION_RIGHT):
		ws.hooks.Key(wsKeyRight)
	case e.IsAction(eventpkg.ACTION_UP):
		ws.hooks.Key(wsKeyUp)
	case e.IsAction(eventpkg.ACTION_DOWN):
		ws.hooks.Key(wsKeyDown)
	case e.IsAction(eventpkg.ACTION_ACTIVATE),
		e.IsAction(eventpkg.ACTION_ENTER),
		e.IsAction(eventpkg.ACTION_OK):
		// Remote-only input: a focused DOM text field turns OK
		// into "open Movian's OSK" — Enter would just submit the
		// form. Buttons/links still get the activation key.
		if val, pw, ok := ws.hooks.OskField(); ok && w.glwRoot != nil {
			pwi := 0
			if pw {
				pwi = 1
			}
			glwOskOpen(w.glwRoot, "", val, w, pwi)
		} else {
			ws.hooks.Key(wsKeyReturn)
		}
	case e.IsAction(eventpkg.ACTION_DELETE):
		ws.hooks.Key(wsKeyDelete)
	case e.IsAction(eventpkg.ACTION_FOCUS_NEXT),
		e.IsAction(eventpkg.ACTION_FOCUS_PREV):
		ws.hooks.Key(wsKeyTab)
	case e.IsAction(eventpkg.ACTION_PAGE_UP):
		ws.hooks.Key(wsKeyPageUp)
	case e.IsAction(eventpkg.ACTION_PAGE_DOWN):
		ws.hooks.Key(wsKeyPageDown)
	case e.IsAction(eventpkg.ACTION_TOP):
		ws.hooks.Key(wsKeyHome)
	case e.IsAction(eventpkg.ACTION_BOTTOM):
		ws.hooks.Key(wsKeyEnd)
	}
	return 1
}

// websurfaceKeydesc — Movian keydesc names → WPE keysyms.
func websurfaceKeydesc(s string) uint32 {
	switch s {
	case "Backspace":
		return wsKeyBackSpace
	case "Tab":
		return wsKeyTab
	case "Return", "Enter":
		return wsKeyReturn
	case "Escape":
		return wsKeyEscape
	case "Delete":
		return wsKeyDelete
	case "Left":
		return wsKeyLeft
	case "Right":
		return wsKeyRight
	case "Up":
		return wsKeyUp
	case "Down":
		return wsKeyDown
	case "Home":
		return wsKeyHome
	case "End":
		return wsKeyEnd
	case "Page_Up", "PageUp":
		return wsKeyPageUp
	case "Page_Down", "PageDown":
		return wsKeyPageDown
	}
	return 0
}

// websurfaceUpdateText — the OSK pushes its live string here (and
// the pre-edit value again on cancel); the session writes it into
// the focused DOM field.
func websurfaceUpdateText(w *Glw, str string) {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	if ws.hooks != nil {
		ws.hooks.SetText(str)
	}
}

// websurfaceBubble — ascend-phase delivery. While the OSK is open
// for this surface, glwDispatchEvent redirects INSERT_STRING /
// ENTER / BS to the requester widget (us); the surface is out of
// the focus path by then, so those events arrive here instead of
// gcSendEvent.
func websurfaceBubble(w *Glw, e *eventpkg.Event) int {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	if ws.hooks == nil {
		return 0
	}
	gr := w.glwRoot
	if gr == nil || gr.grOskWidget != w {
		return 0
	}
	// OSK keys fire on 'activate': a remote-OK vector that also
	// carries ENTER dies in the dispatch redirect before the
	// focused key can see it — hand activation to the focused
	// widget ourselves.
	if f := gr.grCurrentFocus; f != nil && f != w &&
		(e.IsAction(eventpkg.ACTION_ACTIVATE) ||
			e.IsAction(eventpkg.ACTION_ENTER) ||
			e.IsAction(eventpkg.ACTION_OK)) {
		return glwEventToWidget(f, e)
	}
	// Text typed while the OSK is up edits the DOM field directly
	// (C parity: the requester edits its own text while the OSK's
	// buffer is unaware until the next updateText).
	switch e.Type {
	case eventpkg.EVENT_UNICODE:
		if ei, ok := e.Concrete().(*eventpkg.EventInt); ok &&
			ei.Val > 0 {
			ws.hooks.Text(rune(ei.Val))
		}
	case eventpkg.EVENT_INSERT_STRING:
		for _, r := range e.Payload {
			ws.hooks.Text(r)
		}
	default:
		if e.IsAction(eventpkg.ACTION_BS) {
			ws.hooks.Key(wsKeyBackSpace)
		}
	}
	return 1
}

// websurfacePointer — GLW pointer events → backend pointer events
// (WPE types: 1=motion, 2=button; buttons 1=left, 3=right).
// localX/localY are widget-normalized (-1..1): scale to surface
// pixels or every event would land at (0,0).
func websurfacePointer(w *Glw, gpe *glwPointerEventT) int {
	ws := (*glwWebsurface)(unsafe.Pointer(w))
	if ws.hooks == nil {
		return 0
	}
	// GLW local space has +Y = top edge; CEF/OSR mouse coords have
	// y=0 at the top — X maps straight, Y must be flipped.
	x := int32((gpe.localX + 1) * 0.5 * float32(ws.pxW))
	y := int32((1 - gpe.localY) * 0.5 * float32(ws.pxH))
	if wsPointerLogged < 10 {
		wsPointerLogged++
		glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW",
			"websurface pointer typ=%d local=%.2f,%.2f px=%d,%d",
			gpe.typ, gpe.localX, gpe.localY, x, y)
	}
	switch gpe.typ {
	case glwPointerMotionUpdate, glwPointerMotionRefresh,
		glwPointerFocusMotion:
		ws.hooks.Pointer(1, x, y, 0, 0, 0)
	case glwPointerLeftPress, glwPointerRightPress:
		btn := uint32(1)
		if gpe.typ == glwPointerRightPress {
			btn = 3
		}
		ws.hooks.Pointer(2, x, y, btn, 1, 0)
	case glwPointerLeftRelease, glwPointerRightRelease:
		btn := uint32(1)
		if gpe.typ == glwPointerRightRelease {
			btn = 3
		}
		ws.hooks.Pointer(2, x, y, btn, 0, 0)
	}
	return 1
}

// ------------------------------------------------------------------
// Present/dismiss — callable from any thread (grMutex held, like
// every other cross-thread glw op).
// ------------------------------------------------------------------

// websurfaceInit — called from root init: registers the class (the
// skin instantiates `widget(websurface)` through a prop-driven
// loader) and the root as the surface host (one UI root at a time).
var wsClassRegistered bool

func websurfaceInit(gr *glwRoot) {
	if !wsClassRegistered {
		glwRegisterClass(&glwWebsurfaceClass)
		wsClassRegistered = true
	}
	glwWebCurRoot = gr
}

// websurfaceFini — called from glwFini: drops the host root and
// detaches a live session.
func websurfaceFini(gr *glwRoot) {
	if glwWebCurRoot == gr {
		glwWebCurRoot = nil
	}
	if glwWebW != nil && glwWebRoot() == gr {
		glwWebW = nil
		glwWebFocusGrab = nil
		glwWebMu.Lock()
		h := glwWebHooks
		glwWebHooks = nil
		glwWebMu.Unlock()
		if h != nil {
			h.Detached()
		}
	}
}

func glwWebRoot() *glwRoot {
	if glwWebW == nil {
		return nil
	}
	return glwWebW.glwRoot
}

// WebsurfaceShow presents the websurface: the skin's loader
// (`$ui.websurface.show` → websurface.view, slotted above the page
// stack and under the OSK/popups layers) instantiates the widget,
// which then grabs hooks and focus in its ctor. Returns false when
// no UI root is live.
func WebsurfaceShow(h WebsurfaceHooks) bool {
	gr := glwWebCurRoot
	if gr == nil {
		return false
	}
	glwWebMu.Lock()
	glwWebHooks = h
	glwWebMu.Unlock()

	glwLock(gr)
	p := glwDeps.pm.CreateEx(gr.grPropUi, "websurface", nil, false, false)
	p.CreateInt("show", 1)
	glwNeedRefresh(gr, 0)
	glwUnlock(gr)
	return true
}

// WebsurfaceHide clears the prop — the loader unloads the widget —
// and detaches the session. Called on trap completion and from
// session teardown.
func WebsurfaceHide() {
	glwWebMu.Lock()
	h := glwWebHooks
	glwWebHooks = nil
	glwWebMu.Unlock()

	gr := glwWebCurRoot
	if gr != nil {
		glwLock(gr)
		p := glwDeps.pm.CreateEx(gr.grPropUi, "websurface", nil,
			false, false)
		p.CreateInt("show", 0)
		glwNeedRefresh(gr, 0)
		glwUnlock(gr)
	}
	if h != nil {
		h.Detached()
	}
}
