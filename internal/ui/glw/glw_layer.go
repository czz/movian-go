package glw

// Canonical port of src/ui/glw/glw_layer.c — the `layer` widget class
// (stacked children with per-child alpha/z fade on hide/retire).

import (
	"unsafe"
)

// C: glw_layer_item_t — per-child parent data
type glwLayerItem struct {
	alpha float32
	z     float32
	layer int
}

func layerItemData(w *Glw) *glwLayerItem {
	return w.glwParentData.(*glwLayerItem)
}

// C: glw_layer_select_child
func glwLayerSelectChild(w *Glw) {
	// C: TAILQ_FOREACH_REVERSE — last non-hidden/non-retired child
	var c *Glw
	for c = glwTAILQLast(w); c != nil; c = glwTAILQPrev(c) {
		if c.glwFlags&(glwHidden|glwRetired) == 0 {
			break
		}
	}

	w.glwSelected = c

	if c != nil {
		glwFocusOpenPathCloseAllOther(c)
	}
}

// C: glw_layer_layout
func glwLayerLayout(w *Glw, rc *glwRctx) {
	var rc0 glwRctx
	var z, a float32
	layer := 0

	if w.glwAlpha < glwAlphaEpsilon {
		return
	}
	rc0 = *rc

	// C: for(c = TAILQ_LAST; c; c = TAILQ_PREV)
	for c := glwTAILQLast(w); c != nil; {
		p := glwTAILQPrev(c)

		z = 1.0
		a = 1.0

		cd := layerItemData(c)
		cd.layer = layer

		if c.glwFlags&glwRetired != 0 {
			a = 0
			if cd.z > 0.99 {
				glwDestroy(c)
				c = p
				continue
			}
		} else if c.glwFlags&glwHidden == 0 {
			layer++
			z = 0.0
			a = 1
		} else {
			a = 0
		}

		glwLp(&cd.z, w.glwRoot, z, 0.25)
		glwLp(&cd.alpha, w.glwRoot, a, 0.8)

		rc0.rcLayer = uint8(cd.layer + int(rc.rcLayer))

		if cd.alpha > glwAlphaEpsilon {
			glwLayout0(c, &rc0)
		}
		c = p
	}
}

// C: glw_layer_callback
func glwLayerCallback(w *Glw, opaque any, signal glwSignal, extra any) int {
	switch signal {
	case glwSignalChildCreated:
		layerItemData(extra.(*Glw)).z = 1.0
		glwLayerSelectChild(w)

	case glwSignalChildHidden, glwSignalChildUnhidden:
		glwLayerSelectChild(w)
	}
	return 0
}

// C: glw_layer_retire_child
func glwLayerRetireChild(w *Glw, c *Glw) {
	c.glwFlags |= glwRetired
	glwLayerSelectChild(w)
}

// C: glw_layer_render
func glwLayerRender(w *Glw, rc *glwRctx) {
	zmax := 0
	rc0 := *rc

	rc0.rcZmax = &zmax

	for c := w.glwChilds.tqhFirst; c != nil; c = c.glwParentLinkNext {
		cd := layerItemData(c)

		rc0.rcAlpha = rc.rcAlpha * cd.alpha * w.glwAlpha
		if rc0.rcAlpha < glwAlphaEpsilon {
			continue
		}
		rc0.rcLayer = uint8(cd.layer + int(rc.rcLayer))
		//    glw_Translatef(&rc0, 0, 0, cd->z);

		if zmax > int(rc.rcZindex) {
			rc0.rcZindex = int16(zmax)
		} else {
			rc0.rcZindex = rc.rcZindex
		}
		glwRender0(c, &rc0)
		glwZinc(&rc0)
	}
	if zmax > *rc.rcZmax {
		*rc.rcZmax = zmax
	}
}

// C: glw_class_t glw_layer
var glwLayerClass = &glwClass{
	gcName:           "layer",
	gcFlags:          glwCanHideChilds,
	gcInstanceSize:   int(unsafe.Sizeof(Glw{})),
	gcParentDataSize: int(unsafe.Sizeof(glwLayerItem{})),
	gcNewParentData:  func() any { return &glwLayerItem{} },
	gcNew: func(parent *Glw) *Glw {
		return &Glw{}
	},
	gcLayout:        glwLayerLayout,
	gcRender:        glwLayerRender,
	gcRetireChild:   glwLayerRetireChild,
	gcSignalHandler: glwLayerCallback,
}

func registerLayer() {
	glwRegisterClass(glwLayerClass)
}
