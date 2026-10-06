package glw

// Canonical port of src/ui/glw/glw_popup.c — the `popup` widget class
// (anchored overlay positioned via screen coords or centered).

import "unsafe"

// C: glw_popup_t
type glwPopup struct {
	w Glw

	width, height int16

	screenX float32
	screenY float32
	aspect  float32

	screenCordSet bool
}

// C: popup_layout
func popupLayout(w *Glw, rc *glwRctx) {
	p := (*glwPopup)(unsafe.Pointer(w))
	var rc0 glwRctx

	c := w.glwChilds.tqhFirst
	if c == nil || c.glwFlags&glwHidden != 0 {
		return
	}

	if p.aspect > 0 {
		p.height = rc.rcHeight / 2
		p.width = int16(min(int(float32(p.height)*p.aspect), int(rc.rcWidth)))
	} else {
		f := glwFilterConstraints(c)
		if f&glwConstraintX != 0 {
			rw := glwReqWidth(c)
			p.width = int16(min(rw, int(rc.rcWidth)))
		} else {
			p.width = rc.rcWidth / 2
		}
		if f&glwConstraintY != 0 {
			rh := glwReqHeight(c)
			p.height = int16(min(rh, int(rc.rcHeight)))
		} else {
			p.height = rc.rcHeight / 2
		}
	}

	rc0 = *rc
	rc0.rcWidth = p.width
	rc0.rcHeight = p.height

	glwLayout0(c, &rc0)
}

// C: popup_render
func popupRender(w *Glw, rc *glwRctx) {
	p := (*glwPopup)(unsafe.Pointer(w))
	var rc0 glwRctx

	glwStoreMatrix(w, rc)

	rc0 = *rc
	rc0.rcAlpha *= w.glwAlpha

	if rc0.rcAlpha < glwAlphaEpsilon {
		return
	}

	c := w.glwChilds.tqhFirst
	if c == nil || c.glwFlags&glwHidden != 0 {
		return
	}

	var point, dir Vec3
	point = glwVec3Make(p.screenX, p.screenY, -2.41)
	glwVec3Sub(&dir, &point,
		&Vec3{p.screenX * 42.38, p.screenY * 42.38, -100})
	var x, y float32

	glwWidgetUnproject(w.glwMatrix, &x, &y, point, dir)

	var x1, y1 int

	if p.screenCordSet {
		x1 = int((x + 1.0) * 0.5 * float32(rc.rcWidth))
		y1 = int((y+1.0)*0.5*float32(rc.rcHeight) - float32(p.height))
	} else {
		x1 = int(rc.rcWidth)/2 - int(p.width)/2
		y1 = int(rc.rcHeight)/2 - int(p.height)/2
	}

	x2 := x1 + int(p.width)
	y2 := y1 + int(p.height)

	if x2 > int(rc.rcWidth) {
		spill := x2 - int(rc.rcWidth)
		x1 -= spill
		x2 -= spill
	}

	if y1 < 0 {
		y2 -= y1
		y1 -= y1
	}
	glwReposition(&rc0, x1, y2, x2, y1)

	glwRender0(c, &rc0)
}

// C: glw_popup_set_float
func glwPopupSetFloat(w *Glw, attrib glwAttribute, value float32, gs *GlwStyle) int {
	p := (*glwPopup)(unsafe.Pointer(w))

	if attrib == glwAttribAspect {
		if p.aspect == value {
			return 0
		}
		p.aspect = value
		return 1
	}
	return -1
}

// C: glw_popup_set_float_unresolved
func glwPopupSetFloatUnresolved(w *Glw, a string, value float32, gs *GlwStyle) int {
	p := (*glwPopup)(unsafe.Pointer(w))

	if a == "screenPositionX" {
		p.screenX = value
		p.screenCordSet = true
		return glwSetRerenderRequired
	}

	if a == "screenPositionY" {
		p.screenY = value
		p.screenCordSet = true
		return glwSetRerenderRequired
	}

	return glwSetNotResponding
}

// C: glw_class_t glw_popup
var glwPopupClass = &glwClass{
	gcName:         "popup",
	gcInstanceSize: int(unsafe.Sizeof(glwPopup{})),
	gcNew: func(parent *Glw) *Glw {
		p := &glwPopup{}
		return &p.w
	},
	gcLayout:             popupLayout,
	gcRender:             popupRender,
	gcSetFloatUnresolved: glwPopupSetFloatUnresolved,
	gcSetFloat:           glwPopupSetFloat,
}

func registerPopup() {
	glwRegisterClass(glwPopupClass)
}
