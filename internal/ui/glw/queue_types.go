package glw

// C: src/ui/glw/glw.h — canonical 1:1 port.
// Every constant, enum, struct, inline helper and macro from the header.

import ()

// C: TAILQ_HEAD(glw_queue, glw);
type glwQueue struct {
	tqhFirst *Glw
	tqhLast  **Glw
}

// C: LIST_HEAD(glw_head, glw);
type glwHead struct {
	lhFirst *Glw
}

// C: LIST_HEAD(glw_event_map_list, glw_event_map);
type glwEventMapList struct {
	lhFirst *GlwEventMap
}

// C: SLIST_HEAD(glw_prop_sub_slist, glw_prop_sub);
type glwPropSubSlist struct {
	slhFirst *GlwPropSub
}

// C: LIST_HEAD(glw_loadable_texture_list, glw_loadable_texture);
type glwLoadableTextureList struct {
	lhFirst *GlwLoadableTexture
}

// C: TAILQ_HEAD(glw_loadable_texture_queue, glw_loadable_texture);
type glwLoadableTextureQueue struct {
	tqhFirst *GlwLoadableTexture
	tqhLast  **GlwLoadableTexture
}

// C: LIST_HEAD(glw_video_list, glw_video);
type glwVideoList struct {
	lhFirst *GlwVideo
}

// C: LIST_HEAD(glw_style_list, glw_style);
type glwStyleList struct {
	lhFirst *GlwStyle
}

// C: TAILQ_HEAD(glw_view_load_request_queue, glw_view_load_request);
type glwViewLoadRequestQueue struct {
	tqhFirst *GlwViewLoadRequest
	tqhLast  **GlwViewLoadRequest
}

// C: LIST_HEAD(glw_signal_handler_list, glw_signal_handler); (glw.h:1117)
type glwSignalHandlerList struct {
	lhFirst *glwSignalHandler
}

// C: LIST_HEAD(glw_style_binding_list, glw_style_binding); (glw.h:1134)
type glwStyleBindingList struct {
	lhFirst *glwStyleBinding
}

// C: TAILQ_HEAD(glw_video_surface_queue, glw_video_surface);
// (glw_video_common.h:30)
type glwVideoSurfaceQueue struct {
	tqhFirst *glwVideoSurface
	tqhLast  **glwVideoSurface
}

// C: LIST_HEAD(glw_video_reap_task_list, glw_video_reap_task);
// (glw_video_common.h:31)
type glwVideoReapTaskList struct {
	lhFirst *glwVideoReapTask
}

// C: LIST_HEAD(glw_video_overlay_list, glw_video_overlay);
// (glw_video_common.h:29)
type glwVideoOverlayList struct {
	lhFirst *GlwVideoOverlay
}

// glwRecList — C: LIST_HEAD(, glw_rec) glw_recs (glw_rec.c:75).
type glwRecList struct{ lhFirst *GlwRec }

// forward types for head declarations above
type glwCachedViewList struct{ lhFirst *GlwCachedView }

type glwTextBitmapList struct{ lhFirst *GlwTextBitmap }

type glwTextBitmapQueue struct {
	tqhFirst *GlwTextBitmap
	tqhLast  **GlwTextBitmap
}

type glwImageList struct{ lhFirst *GlwImage }

// C: LIST_HEAD(, glw_gf_ctrl) ggcs element type list (glw.c:1043)
type glwGfCtrlList struct {
	lhFirst *glwGfCtrl
}
