package glw

// C: src/ui/glw/glw_texture_loader.c — canonical 1:1 port.

import (
	"strings"

	"sync"

	archpkg "github.com/czz/movian-go/internal/arch"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	tracepkg "github.com/czz/movian-go/internal/trace"
	uipkg "github.com/czz/movian-go/internal/ui"
)

// wired by the UI init path.

// GlwSetBackendSystem wires the BackendSystem used by backend_imageloader
// (C: gconf.backend).
func GlwSetBackendSystem(bs *backendcore.BackendSystem) { glwDeps.bs = bs }

// GlwSetClipboard injects the clipboard (C: clipboard.c statics).
func GlwSetClipboard(clip *uipkg.Clipboard) { glwDeps.clip = clip }

func syncCond(mu *sync.Mutex) sync.Cond { return sync.Cond{L: mu} }

// C: enum GLT_STATE_* (glw_texture.h:47-55)
const (
	gltStateInactive  = 0
	gltStateQueued    = 1
	gltStateLoading   = 2
	gltStateValid     = 3
	gltStateError     = 4
	gltStateLoadAbort = 5
	gltStateStashed   = 6
)

// C: GLW_TEX_* flags (glw.h)
const (
	glwTexRepeat               = 0x1
	glwTexMipmapped            = 0x2
	glwTexIntensityAnalysis    = 0x4
	glwTexPrimaryColorAnalysis = 0x8
	glwTexCornerTopleft        = 0x10
	glwTexCornerTopright       = 0x20
	glwTexCornerBottomleft     = 0x40
	glwTexCornerBottomright    = 0x80
)

// C: glt_set_state(a, b) (a)->glt_state = b
func gltSetState(glt *GlwLoadableTexture, state uint8) { glt.gltState = state }

// ---------------------------------------------------------------------------
// LIST/TAILQ helpers for glt_global_link (LIST), glt_flush_link (LIST),
// glt_work_link (TAILQ).

func gltGlobalInsertHead(h *glwLoadableTextureList, glt *GlwLoadableTexture) {
	glt.gltGlobalLinkNext = h.lhFirst
	if h.lhFirst != nil {
		h.lhFirst.gltGlobalLinkPrev = &glt.gltGlobalLinkNext
	}
	h.lhFirst = glt
	glt.gltGlobalLinkPrev = &h.lhFirst
}

func gltGlobalRemove(glt *GlwLoadableTexture) {
	if glt.gltGlobalLinkNext != nil {
		glt.gltGlobalLinkNext.gltGlobalLinkPrev = glt.gltGlobalLinkPrev
	}
	*glt.gltGlobalLinkPrev = glt.gltGlobalLinkNext
	glt.gltGlobalLinkNext = nil
	glt.gltGlobalLinkPrev = nil
}

func gltFlushInsertHead(h *glwLoadableTextureList, glt *GlwLoadableTexture) {
	glt.gltFlushLinkNext = h.lhFirst
	if h.lhFirst != nil {
		h.lhFirst.gltFlushLinkPrev = &glt.gltFlushLinkNext
	}
	h.lhFirst = glt
	glt.gltFlushLinkPrev = &h.lhFirst
}

func gltFlushRemove(glt *GlwLoadableTexture) {
	if glt.gltFlushLinkNext != nil {
		glt.gltFlushLinkNext.gltFlushLinkPrev = glt.gltFlushLinkPrev
	}
	*glt.gltFlushLinkPrev = glt.gltFlushLinkNext
	glt.gltFlushLinkNext = nil
	glt.gltFlushLinkPrev = nil
}

func gltWorkInsertTail(q *glwLoadableTextureQueue, glt *GlwLoadableTexture) {
	glt.gltWorkLinkNext = nil
	glt.gltWorkLinkPrevP = q.tqhLast
	*q.tqhLast = glt
	q.tqhLast = &glt.gltWorkLinkNext
}

func gltWorkRemove(q *glwLoadableTextureQueue, glt *GlwLoadableTexture) {
	if glt.gltWorkLinkNext != nil {
		glt.gltWorkLinkNext.gltWorkLinkPrevP = glt.gltWorkLinkPrevP
	} else {
		q.tqhLast = glt.gltWorkLinkPrevP
	}
	*glt.gltWorkLinkPrevP = glt.gltWorkLinkNext
	glt.gltWorkLinkNext = nil
	glt.gltWorkLinkPrevP = nil
}

func gltWorkRemoveViaQ(glt *GlwLoadableTexture) { gltWorkRemove(glt.gltQ, glt) }

// ---------------------------------------------------------------------------

// C: glt_destroy (glw_texture_loader.c:50-56)
func gltDestroy(glt *GlwLoadableTexture) {
	miscpkg.CancellableRelease(glt.gltCancellable)
	if glt.gltBackend != nil {
		backendcore.BackendRelease(glt.gltBackend)
	}
}

// C: glt_cancel (glw_texture_loader.c:61-64)
func gltCancel(glt *GlwLoadableTexture) {
	miscpkg.CancellableCancel(glt.gltCancellable)
}

// C: glw_tex_purge_stash (glw_texture_loader.c:69-93)
func glwTexPurgeStash(gr *glwRoot, stash int) {
	for gr.grTexStash[stash].size > gr.grTexStash[stash].limit {
		glt := gr.grTexStash[stash].q.tqhFirst
		if glt == nil {
			break
		}
		// assert(glt->glt_q == &gr->gr_tex_stash[stash].q)
		gltWorkRemove(glt.gltQ, glt)
		gr.grTexStash[stash].size -= glt.gltSize

		glwTexBackendFreeLoaderResources(glt)
		glwTexBackendFreeRenderResources(gr, glt)
		gltSetState(glt, gltStateInactive)
		if glt.gltRefcnt == 0 {
			if glt.gltURL != nil {
				miscpkg.RstrRelease(glt.gltURL)
				glt.gltURL = nil
				gltGlobalRemove(glt)
			}
			gltDestroy(glt)
		}
	}
}

// C: glw_tex_stash (glw_texture_loader.c:98-116)
func glwTexStash(gr *glwRoot, glt *GlwLoadableTexture, unreferenced int) int {
	if glt.gltURL == nil {
		return 1
	}
	gltSetState(glt, gltStateStashed)

	stash := 0
	if glt.gltOriginType == uint8(imagepkg.CodedJPEG) {
		stash = 1
	}
	glt.gltStash = uint8(stash)
	glt.gltQ = &gr.grTexStash[stash].q

	gltWorkInsertTail(glt.gltQ, glt)
	gr.grTexStash[stash].size += glt.gltSize

	glwTexPurgeStash(gr, stash)
	return 0
}

// C: glw_tex_unstash (glw_texture_loader.c:119-126)
func glwTexUnstash(gr *glwRoot, glt *GlwLoadableTexture) {
	stash := int(glt.gltStash)
	gltWorkRemove(glt.gltQ, glt)
	gr.grTexStash[stash].size -= glt.gltSize
}

// C: glw_tex_autoflush (glw_texture_loader.c:130-179)
func glwTexAutoflush(gr *glwRoot) {
	for {
		glt := gr.grTexFlushList.lhFirst
		if glt == nil {
			break
		}
		gltFlushRemove(glt)
		switch glt.gltState {
		case gltStateValid:
			if glwTexStash(gr, glt, 0) != 0 {
				glwTexBackendFreeRenderResources(gr, glt)
				gltSetState(glt, gltStateInactive)
			}
		case gltStateQueued:
			gltSetState(glt, gltStateInactive)
			gltWorkRemoveViaQ(glt)
			glwTexDeref(gr, glt) // beware! glt may be free'd here
		case gltStateLoading:
			gltSetState(glt, gltStateLoadAbort)
			gltCancel(glt)
		default:
			// C: printf + abort for unexpected states
			panic("glw_tex_autoflush: unexpected state")
		}
	}

	// C: LIST_MOVE(&gr->gr_tex_flush_list, &gr->gr_tex_active_list, glt_flush_link)
	//      LIST_INIT(&gr->gr_tex_active_list)
	gr.grTexFlushList.lhFirst = gr.grTexActiveList.lhFirst
	for glt := gr.grTexFlushList.lhFirst; glt != nil; glt = glt.gltFlushLinkNext {
		glt.gltFlushLinkPrev = func() **GlwLoadableTexture {
			if glt.gltFlushLinkPrev != nil {
				return glt.gltFlushLinkPrev
			}
			return nil
		}()
	}
	// Fix prev pointers after list move
	if gr.grTexFlushList.lhFirst != nil {
		gr.grTexFlushList.lhFirst.gltFlushLinkPrev = &gr.grTexFlushList.lhFirst
	}
	gr.grTexActiveList.lhFirst = nil
}

// ---------------------------------------------------------------------------
// C: loaderaux_t (glw_texture_loader.c:185-188)
type loaderaux struct {
	laGr       *glwRoot
	laOnlyFast int
}

// C: loader_get_work (glw_texture_loader.c:193-210)
func loaderGetWork(la *loaderaux) *GlwLoadableTexture {
	gr := la.laGr
	lastQueue := lqRefresh
	if la.laOnlyFast != 0 {
		lastQueue = lqTentative
	}
	for {
		if gr.grTexThreadsRunning == 0 {
			return nil
		}
		for i := range lastQueue + 1 {
			if glt := gr.grTexLoadQueue[i].tqhFirst; glt != nil {
				return glt
			}
		}
		gr.grTexLoadCond.Wait()
	}
}

// C: glt_enqueue (glw_texture_loader.c:215-228)
func gltEnqueue(gr *glwRoot, glt *GlwLoadableTexture, q int) {
	glt.gltRefcnt++
	glt.gltQ = &gr.grTexLoadQueue[q]
	gltWorkInsertTail(&gr.grTexLoadQueue[q], glt)
	gltSetState(glt, gltStateQueued)
	if q > lqTentative {
		gr.grTexLoadCond.Broadcast()
	} else {
		gr.grTexLoadCond.Signal()
	}
}

// C: glt_incremental_update (glw_texture_loader.c:233-252)
func gltIncrementalUpdate(opaque any, pm *imagepkg.Pixmap) {
	glt := opaque.(*GlwLoadableTexture)
	gr := glt.gltGr
	glwLock(gr)

	glt.gltAspect = float32(pm.Aspect)
	glt.gltMargin = int16(pm.Margin)
	glt.gltXs = int16(pm.Width)
	glt.gltYs = int16(pm.Height)

	glt.gltIntensity = pm.Intensity

	glt.gltSize = glwTexBackendLoad(gr, glt, pm)
	glwNeedRefresh(gr, 0)

	glwUnlock(gr)
}

// bypassCacheSentinel — C: BYPASS_CACHE ((int *)-1). Go convention: *cacheControl == -1.
var bypassCacheSentinel = -1

// C: loader_thread (glw_texture_loader.c:258-444)
func loaderThread(aux any) any {
	la := aux.(*loaderaux)
	gr := la.laGr

	glwLock(gr)

	for {
		glt := loaderGetWork(la)
		if glt == nil {
			break
		}

		gltWorkRemoveViaQ(glt)
		gltSetState(glt, gltStateLoading)

		if glt.gltRefcnt > 1 {
			url := miscpkg.RstrDup(glt.gltURL)

			im := &backendcore.ImageMeta{
				ReqWidth:             int(glt.gltReqXs),
				ReqHeight:            int(glt.gltReqYs),
				MaxWidth:             gr.grWidth,
				MaxHeight:            gr.grHeight,
				CanMono:              true,
				CornerRadius:         uint16(glt.gltRadius),
				ForceLocal:           glt.gltSourceFlags&glwSourceFlagAlwaysLocal != 0,
				IntensityAnalysis:    glt.gltFlags&glwTexIntensityAnalysis != 0,
				PrimaryColorAnalysis: glt.gltFlags&glwTexPrimaryColorAnalysis != 0,
				CornerSelection: uint8(glt.gltFlags & (glwTexCornerTopleft |
					glwTexCornerTopright | glwTexCornerBottomleft |
					glwTexCornerBottomright)),
				Shadow:    uint16(glt.gltShadow),
				ReqAspect: float64(glt.gltReqAspect),
				Opaque:    glt,
			}
			im.Incremental = gltIncrementalUpdate

			cacheControl := 0
			var ccptr *int
			if glt.gltQ == &gr.grTexLoadQueue[lqTentative] {
				ccptr = &cacheControl
			} else if glt.gltQ == &gr.grTexLoadQueue[lqRefresh] {
				ccptr = &bypassCacheSentinel
			} else {
				ccptr = nil
			}

			miscpkg.CancellableReset(glt.gltCancellable)

			glwUnlock(gr)
			img, ierr := glwDeps.bs.Imageloader(miscpkg.RstrGet(url), im,
				ccptr, glt.gltCancellable, glt.gltBackend)
			errStr := ""
			if ierr != nil {
				errStr = ierr.Error()
			}
			glwLock(gr)

			if glt.gltState == gltStateLoadAbort {
				if img != nil && img != backendcore.NotModifiedImage {
					if im2, ok := img.(*imagepkg.Image); ok {
						im2.Release()
					}
				}
				if imagepkg.EnableImageDebug(glwDeps.gconf) {
					glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Load of %s was aborted", miscpkg.RstrGet(url))
				}
				gltSetState(glt, gltStateInactive)
			} else if img == nil {
				if glt.gltQ == &gr.grTexLoadQueue[lqTentative] {
					if glt.gltState == gltStateLoading {
						gltEnqueue(gr, glt, lqOther)
					}
				} else if glt.gltQ == &gr.grTexLoadQueue[lqRefresh] {
					if imagepkg.EnableImageDebug(glwDeps.gconf) {
						glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Unable to load image %s -- %s -- using cached copy",
							miscpkg.RstrGet(url), errStr)
					}
					gltSetState(glt, gltStateValid)
				} else {
					if imagepkg.EnableImageDebug(glwDeps.gconf) {
						if glt.gltURL != nil {
							glwDeps.ts.Trace(tracepkg.TRACE_ERROR, "GLW", "Unable to load image %s -- %s",
								miscpkg.RstrGet(url), errStr)
						} else {
							glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Aborted load of %s", miscpkg.RstrGet(url))
						}
					}
					gltSetState(glt, gltStateError)
					gltFlushRemove(glt)
					glwNeedRefresh(gr, 0)
				}
			} else {
				if glt.gltState == gltStateLoading {
					if glt.gltQ == &gr.grTexLoadQueue[lqTentative] && cacheControl == 1 {
						gltEnqueue(gr, glt, lqRefresh)
					} else {
						gltSetState(glt, gltStateValid)
					}

					if img != backendcore.NotModifiedImage {
						// Actually upload the texture to the render backend
						i := img.(*imagepkg.Image)
						ic := i.FindComponent(imagepkg.ComponentPixmap)
						if ic == nil {
							panic("glw texture load: no pixmap component")
						}
						pm := ic.Pixmap

						glt.gltAspect = float32(pm.Aspect)
						glt.gltMargin = int16(pm.Margin)
						glt.gltXs = int16(pm.Width)
						glt.gltYs = int16(pm.Height)

						glt.gltOriginType = i.OriginCodedType
						glt.gltOrientation = i.Orientation
						glt.gltIntensity = pm.Intensity
						glt.gltPrimaryColor = pm.PrimaryColor
						if pm.Flags&imagepkg.PixmapOpaque != 0 {
							glt.gltOpaque = 1
						} else {
							glt.gltOpaque = 0
						}

						if imagepkg.EnableImageDebug(glwDeps.gconf) {
							glwDeps.ts.Trace(tracepkg.TRACE_DEBUG, "GLW", "Loaded %s (%d x %d)",
								miscpkg.RstrGet(url), pm.Width, pm.Height)
						}

						glt.gltSize = glwTexBackendLoad(gr, glt, pm)
						glwNeedRefresh(gr, 0)
					}
				}

				if img != backendcore.NotModifiedImage {
					if i, ok := img.(*imagepkg.Image); ok {
						i.Release()
					}
				}
			}
			miscpkg.RstrRelease(url)
		}
		glwTexDeref(gr, glt)
	}

	glwUnlock(gr)
	return nil
}

// C: spawn_loader (glw_texture_loader.c:446-456)
func spawnLoader(gr *glwRoot, onlyFast int, idx int) {
	la := &loaderaux{laGr: gr, laOnlyFast: onlyFast}
	gr.grTexThreads[idx] = archpkg.ThreadCreateJoinable("GLW texture loader",
		loaderThread, la, 0)
}

// C: glw_tex_init (glw_texture_loader.c:461-480)
func glwTexStart(gr *glwRoot) {
	gr.grTexThreadsRunning = 1
	gr.grTexLoadCond = syncCond(&gr.grMutex)

	gr.grTexRelQueue.tqhFirst = nil
	gr.grTexRelQueue.tqhLast = &gr.grTexRelQueue.tqhFirst
	gr.grTexStash[0].q.tqhFirst = nil
	gr.grTexStash[0].q.tqhLast = &gr.grTexStash[0].q.tqhFirst
	gr.grTexStash[1].q.tqhFirst = nil
	gr.grTexStash[1].q.tqhLast = &gr.grTexStash[1].q.tqhFirst

	gr.grTexStash[0].limit = 16 * 1024 * 1024
	gr.grTexStash[1].limit = 16 * 1024 * 1024

	for i := range lqNum {
		gr.grTexLoadQueue[i].tqhFirst = nil
		gr.grTexLoadQueue[i].tqhLast = &gr.grTexLoadQueue[i].tqhFirst
	}

	for i := range glwTextureThreads {
		of := 0
		if i >= 4 {
			of = 1
		}
		spawnLoader(gr, of, i)
	}
}

// C: glw_tex_fini (glw_texture_loader.c:485-496)
func glwTexFini(gr *glwRoot) {
	glwLock(gr)
	gr.grTexThreadsRunning = 0
	gr.grTexLoadCond.Broadcast()
	glwUnlock(gr)

	for i := range glwTextureThreads {
		gr.grTexThreads[i].Join()
	}
}

// C: glw_tex_flush_all (glw_texture_loader.c:501-560)
func glwTexFlushAll(gr *glwRoot) {
	for glt := gr.grTexList.lhFirst; glt != nil; {
		next := glt.gltGlobalLinkNext
		switch glt.gltState {
		case gltStateInactive:
		case gltStateStashed:
			glwTexUnstash(gr, glt)
			glwTexBackendFreeRenderResources(gr, glt)
		case gltStateValid:
			gltFlushRemove(glt)
			glwTexBackendFreeRenderResources(gr, glt)
		case gltStateQueued:
			gltFlushRemove(glt)
			gltWorkRemoveViaQ(glt)
			gltSetState(glt, gltStateInactive)
			glwTexDeref(gr, glt)
			glt = next
			continue
		case gltStateLoading:
			gltFlushRemove(glt)
		case gltStateLoadAbort:
			glt = next
			continue
		case gltStateError:
		}
		gltSetState(glt, gltStateInactive)

		if glt.gltRefcnt == 0 {
			if glt.gltURL != nil {
				miscpkg.RstrRelease(glt.gltURL)
				glt.gltURL = nil
				gltGlobalRemove(glt)
			}
			gltDestroy(glt)
		}
		glt = next
	}
}

// C: glw_tex_purge (glw_texture_loader.c:565-574)
func glwTexPurge(gr *glwRoot) {
	for {
		glt := gr.grTexRelQueue.tqhFirst
		if glt == nil {
			break
		}
		gltWorkRemove(&gr.grTexRelQueue, glt)
		glwTexBackendFreeRenderResources(gr, glt)
		gltDestroy(glt)
	}
}

// C: glw_tex_deref (glw_texture_loader.c:579-626)
func glwTexDeref(gr *glwRoot, glt *GlwLoadableTexture) {
	glt.gltRefcnt--

	if glt.gltRefcnt > 0 {
		// Loading state holds a ref, so this means that we're the only one
		if glt.gltRefcnt == 1 && glt.gltState == gltStateLoading {
			goto unlink
		}
		return
	}

	switch glt.gltState {
	case gltStateValid:
		gltFlushRemove(glt)
		if glwTexStash(gr, glt, 1) != 0 {
			break
		}
		return

	case gltStateStashed:
		return

	case gltStateQueued, gltStateLoading:
		gltFlushRemove(glt)
	}

	glwTexBackendFreeLoaderResources(glt)
	gltWorkInsertTail(&gr.grTexRelQueue, glt)

	if glt.gltURL == nil {
		return
	}

unlink:
	if glt.gltState == gltStateLoading {
		gltCancel(glt)
	}
	miscpkg.RstrRelease(glt.gltURL)
	glt.gltURL = nil
	gltGlobalRemove(glt)
}

// C: glw_tex_create (glw_texture_loader.c:632-675)
func glwTexCreate(gr *glwRoot, filename *miscpkg.Rstr, flags int, xs, ys int,
	radius int, shadow int, aspect float32, sourceFlags int,
	be *backendcore.Backend) *GlwLoadableTexture {

	if strings.HasPrefix(miscpkg.RstrGet(filename), "pixmap:") {
		be = nil
	}

	var glt *GlwLoadableTexture
	for glt = gr.grTexList.lhFirst; glt != nil; glt = glt.gltGlobalLinkNext {
		if miscpkg.RstrGet(glt.gltURL) == miscpkg.RstrGet(filename) &&
			glt.gltFlags == flags &&
			int(glt.gltReqXs) == xs &&
			int(glt.gltReqYs) == ys &&
			int(glt.gltRadius) == radius &&
			int(glt.gltShadow) == shadow &&
			glt.gltReqAspect == aspect &&
			glt.gltSourceFlags == sourceFlags &&
			glt.gltBackend == be {
			break
		}
	}

	if glt == nil {
		glt = &GlwLoadableTexture{}
		glt.gltGr = gr
		glt.gltCancellable = miscpkg.CancellableCreate()
		glt.gltURL = miscpkg.RstrDup(filename)
		gltGlobalInsertHead(&gr.grTexList, glt)
		gltSetState(glt, gltStateInactive)
		glt.gltFlags = flags
		glt.gltReqXs = int16(xs)
		glt.gltReqYs = int16(ys)
		glt.gltRadius = int16(radius)
		glt.gltShadow = int16(shadow)
		glt.gltReqAspect = aspect
		glt.gltSourceFlags = sourceFlags
		glt.gltBackend = backendcore.BackendRetain(be)
	}

	glt.gltRefcnt++
	return glt
}

// C: gl_tex_req_load (glw_texture_loader.c:681-692)
func glTexReqLoad(gr *glwRoot, glt *GlwLoadableTexture) {
	q := lqTentative
	if glt.gltSourceFlags&glwSourceFlagAlwaysLocal != 0 {
		q = lqSkin
	}
	gltEnqueue(gr, glt, q)
}

// C: glw_tex_layout (glw_texture_loader.c:697-730)
func glwTexLayout(gr *glwRoot, glt *GlwLoadableTexture) {
	if glt.gltPixmap != nil {
		glwTexBackendLayout(gr, glt)
	}

	switch glt.gltState {
	case gltStateInactive:
		glTexReqLoad(gr, glt)
	case gltStateStashed:
		glwTexUnstash(gr, glt)
		gltSetState(glt, gltStateValid)
	case gltStateValid, gltStateQueued, gltStateLoading:
		gltFlushRemove(glt)
	case gltStateError:
		return
	case gltStateLoadAbort:
		// We need to wait for it to enter inactive state again
		return
	}
	gltFlushInsertHead(&gr.grTexActiveList, glt)
}
