//go:build (linux && (glfw || rpi || sunxi)) || android || ((darwin || windows) && glfw)

package main

import (
	"image"

	"github.com/czz/movian-go/internal/app"
	imagepkg "github.com/czz/movian-go/internal/image"
	navcore "github.com/czz/movian-go/internal/navigator"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
	uiglw "github.com/czz/movian-go/internal/ui/glw"
)

// glwFrontendEarlyStart — canonical path: wires the pkg/ui/glw global
// seams whose deps already exist at this point in init (prop manager,
// settings manager, fa manager, kvstore — C resolves them via
// gconf/globals at link time). Shared by the X11 and GLFW frontends:
// pkg/ui/glw is one package, so these seams must be wired whenever
// ANY GLW backend is built in. The canonical glw_init creates the
// "userinterfaces" prop tree itself inside the glw thread (glw_init4),
// so nothing is created here.
func glwFrontendEarlyStart(ctx *appContext, settingsManager *settingscore.SettingsManager) {
	uiglw.SetPropManager(ctx.propManager)

	// C: gconf.* (debug_glw / skin / fullscreen / convert_pointer_to_touch)
	// are read from gconf.G directly by glw.c readers.

	// C: nav_spawn() — called inside glw_x11_start when no previous UI
	// handed over a navigator root. Lazy: navSystem is created later.
	uiglw.GlwX11NavSpawnFn = func() *propcore.Prop {
		return ctx.navSystem.Spawn(ctx.propManager).PropRoot()
	}

	uiglw.SetDeps(app.AppDataRoot,
		// Lazy: ctx.runControl is assigned after newAppContext returns.
		func() {
			if ctx.runControl != nil {
				ctx.runControl.Activity()
			}
		},
		// navCourier drain — C dispatches nav_eventsink via GLOBAL
		// dispatch; the Go port drains its dedicated courier in the
		// render thread (glw_prepare_frame). Lazy: navSystem is
		// created later in init.
		func() {
			if ctx.navSystem != nil {
				ctx.navSystem.ProcessNavCourier()
			}
		})

	// C: screenshot_deliver (src/api/screenshot.c:285) — pixmap based.
	// Lazy closure: ctx.screenshotHandler is created later in init.
	uiglw.GlwSetScreenshotDeliver(func(pm *imagepkg.Pixmap) {
		if ctx.screenshotHandler == nil {
			return
		}
		ctx.screenshotHandler.Deliver(pixmapToImage(pm))
	})

	// C: glw_settings_init deps — settings manager, fileaccess (custom
	// background dir scan), kvstore (Bing image cache).
	uiglw.GlwSettingsSetDeps(settingsManager, ctx.faManager)
}

// glwFrontendSettingsStart — C: glw_settings_init (main.c:432), called
// right after init_group(INIT_GROUP_GRAPHICS) and before media_init.
func glwFrontendSettingsStart(ctx *appContext) {
	uiglw.GlwSettingsStart()
}

// glwFrontendLateStart — canonical path: wires the seams whose deps are
// only created after InitGroupGraphics (event manager, backend system).
// glw_x11_start creates gr_prop_ui inside the glw thread and obtains
// the navigator root via nav_spawn (GlwX11NavSpawnFn above).
func glwFrontendLateStart(ctx *appContext, defaultNav *navcore.Navigator, settingsManager *settingscore.SettingsManager) {
	uiglw.SetEventManager(ctx.eventManager)
	uiglw.SetKVStore(ctx.kvstore)
	uiglw.SetTraceSystem(ctx.traceSystem)
	uiglw.SetAppShutdown(func(retcode int) { app_shutdown(ctx, retcode) })
	// C: backend_imageloader resolves via the registered backend system.
	uiglw.GlwSetBackendSystem(ctx.backendSystem)
	uiglw.GlwSetClipboard(ctx.clipboard)
	uiglw.SetAsyncIO(ctx.asyncIO)
}

// pixmapToImage — converts an image.Pixmap to stdlib image.Image for
// the screenshot HTTP handler. GL readback is BGR32/RGBA; other formats
// are handled minimally.
func pixmapToImage(pm *imagepkg.Pixmap) image.Image {
	if pm == nil {
		return nil
	}
	w, h := pm.Width, pm.Height
	// PIXMAP_VFLIP — C: pixmap rows are bottom-up (GL readback); the image
	// encode path honors pm_flags & PIXMAP_VFLIP by emitting rows reversed.
	vflip := pm.Flags&imagepkg.PixmapVflip != 0
	srcY := func(y int) int {
		if vflip {
			return h - 1 - y
		}
		return y
	}
	switch pm.Type {
	case imagepkg.PixmapBGR32, imagepkg.PixmapRGBA, imagepkg.PixmapBGRA:
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			src := srcY(y) * pm.Stride
			dst := y * img.Stride
			for x := 0; x < w; x++ {
				s := src + x*4
				d := dst + x*4
				if pm.Type == imagepkg.PixmapBGR32 || pm.Type == imagepkg.PixmapBGRA {
					// BGRA → RGBA
					img.Pix[d+0] = pm.Data[s+2]
					img.Pix[d+1] = pm.Data[s+1]
					img.Pix[d+2] = pm.Data[s+0]
				} else {
					img.Pix[d+0] = pm.Data[s+0]
					img.Pix[d+1] = pm.Data[s+1]
					img.Pix[d+2] = pm.Data[s+2]
				}
				img.Pix[d+3] = 0xff
			}
		}
		return img
	case imagepkg.PixmapRGB24:
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			src := srcY(y) * pm.Stride
			dst := y * img.Stride
			for x := 0; x < w; x++ {
				s := src + x*3
				d := dst + x*4
				img.Pix[d+0] = pm.Data[s+0]
				img.Pix[d+1] = pm.Data[s+1]
				img.Pix[d+2] = pm.Data[s+2]
				img.Pix[d+3] = 0xff
			}
		}
		return img
	case imagepkg.PixmapI:
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			src := y * pm.Stride
			dst := y * img.Stride
			copy(img.Pix[dst:dst+w], pm.Data[src:src+w])
		}
		return img
	case imagepkg.PixmapIA:
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			src := y * pm.Stride
			dst := y * img.Stride
			for x := 0; x < w; x++ {
				s := src + x*2
				d := dst + x*4
				img.Pix[d+0] = pm.Data[s+0]
				img.Pix[d+1] = pm.Data[s+0]
				img.Pix[d+2] = pm.Data[s+0]
				img.Pix[d+3] = pm.Data[s+1]
			}
		}
		return img
	}
	return nil
}
