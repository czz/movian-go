package image

// Canonical port of src/image/svg.c — the hand-rolled SVG parser
// emitting image_component_vector_t commands.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/htsmsg"
	misc "github.com/czz/movian-go/internal/misc"
)

// svgState — C: svg_state_t (svg.c:33-41).
type svgState struct {
	first    [2]float32
	cur      [2]float32
	lastCtrl [2]float32 // For s/S command
	ctm      [9]float32
	icv      *VectorComponent
	scaling  float32
	closed   bool
}

// svgMtxIdentity — C: svg_mtx_identity (svg.c:48-60).
func svgMtxIdentity(mtx *[9]float32) {
	mtx[0] = 1
	mtx[1] = 0
	mtx[2] = 0
	mtx[3] = 0
	mtx[4] = 1
	mtx[5] = 0
	mtx[6] = 0
	mtx[7] = 0
	mtx[8] = 1
}

// svgMtxScale — C: svg_mtx_scale (svg.c:62-74).
func svgMtxScale(mtx *[9]float32, x, y float32) {
	mtx[0] *= x
	mtx[3] *= y

	mtx[1] *= x
	mtx[4] *= y

	mtx[2] *= x
	mtx[5] *= y
}

// svgMtxTranslate — C: svg_mtx_translate (svg.c:76-82).
func svgMtxTranslate(mtx *[9]float32, x, y float32) {
	mtx[6] += mtx[0]*x + mtx[3]*y
	mtx[7] += mtx[4]*x + mtx[5]*y
}

// svgMtxVecMul — C: svg_mtx_vec_mul (svg.c:84-92).
func svgMtxVecMul(dst *[2]float32, mtx *[9]float32, a [2]float32) {
	dst[0] = mtx[0]*a[0] + mtx[3]*a[1] + mtx[6]
	dst[1] = mtx[1]*a[0] + mtx[4]*a[1] + mtx[7]
}

// svgStateApplyMatrix — C: svg_state_apply_matrix (svg.c:94-125).
func svgStateApplyMatrix(state *svgState, B []float32) {
	var t, b [9]float32

	b[0] = B[0]
	b[1] = B[1]
	b[2] = 0
	b[3] = B[2]
	b[4] = B[3]
	b[5] = 0
	b[6] = B[4]
	b[7] = B[5]
	b[8] = 1

	a := state.ctm

	t[0] = a[0]*b[0] + a[3]*b[1] + a[6]*b[2]
	t[3] = a[0]*b[3] + a[3]*b[4] + a[6]*b[5]
	t[6] = a[0]*b[6] + a[3]*b[7] + a[6]*b[8]

	t[1] = a[1]*b[0] + a[4]*b[1] + a[7]*b[2]
	t[4] = a[1]*b[3] + a[4]*b[4] + a[7]*b[5]
	t[7] = a[1]*b[6] + a[4]*b[7] + a[7]*b[8]

	t[2] = a[2]*b[0] + a[5]*b[1] + a[8]*b[2]
	t[5] = a[2]*b[3] + a[5]*b[4] + a[8]*b[5]
	t[8] = a[2]*b[6] + a[5]*b[7] + a[8]*b[8]

	state.ctm = t
}

// svgStateTranslate — C: svg_state_translate (svg.c:127-132).
func svgStateTranslate(state *svgState, p []float32) {
	state.ctm[6] += state.ctm[0]*p[0] + state.ctm[3]*p[1]
	state.ctm[7] += state.ctm[1]*p[0] + state.ctm[4]*p[1]
}

// skipWS — C: while(*t < 33 && *t) t++
func skipWS(s string, i int) int {
	for i < len(s) && s[i] < 33 {
		i++
	}
	return i
}

// svgParseTransform — C: svg_parse_transform (svg.c:146-185).
func svgParseTransform(s *svgState, t string) {
	var values [6]float32
	var nargs int
	var fn func(s *svgState, p []float32)

	i := skipWS(t, 0)
	if i >= len(t) {
		return
	}
	rest := t[i:]
	if strings.HasPrefix(rest, "matrix") {
		fn = svgStateApplyMatrix
		nargs = 6
		i += len("matrix")
	} else if strings.HasPrefix(rest, "translate") {
		fn = svgStateTranslate
		nargs = 2
		i += len("translate")
	} else {
		return
	}

	i = skipWS(t, i)
	if i >= len(t) || t[i] != '(' {
		return
	}
	i++
	for j := range nargs {
		v, end := misc.MyStr2double(t[i:])
		if end == 0 {
			return
		}
		values[j] = float32(v)
		i += end
		i = skipWS(t, i)
		if i < len(t) && t[i] == ',' {
			i++
		}
	}
	fn(s, values[:])
}

// cmdMove — C: cmd_move (svg.c:189-198).
func cmdMove(state *svgState) {
	var pt [2]float32
	if state.closed {
		state.first[0] = state.cur[0]
		state.first[1] = state.cur[1]
		state.closed = false
	}
	svgMtxVecMul(&pt, &state.ctm, state.cur)
	VecEmitF1(state.icv, VcMoveTo, pt)
}

// cmdMoveAbs — C: cmd_move_abs (svg.c:203-208).
func cmdMoveAbs(state *svgState, p []float32) {
	state.cur[0] = p[0]
	state.cur[1] = p[1]
	cmdMove(state)
}

// cmdMoveRel — C: cmd_move_rel (svg.c:211-217).
func cmdMoveRel(state *svgState, p []float32) {
	state.cur[0] += p[0]
	state.cur[1] += p[1]
	cmdMove(state)
}

// cmdCurve — C: cmd_curve (svg.c:223-227).
func cmdCurve(state *svgState, s, c, d, e [2]float32) {
	VecEmitF3(state.icv, VcCubicTo, c, d, e)
}

// cmdCurvetoRel — C: cmd_curveto_rel (svg.c:230-258).
func cmdCurvetoRel(state *svgState, p []float32) {
	var s, c, d, e [2]float32

	s[0] = state.cur[0]
	s[1] = state.cur[1]

	c[0] = state.cur[0] + p[0]
	c[1] = state.cur[1] + p[1]

	state.lastCtrl[0] = state.cur[0] + p[2]
	d[0] = state.lastCtrl[0]
	state.lastCtrl[1] = state.cur[1] + p[3]
	d[1] = state.lastCtrl[1]

	e[0] = state.cur[0] + p[4]
	e[1] = state.cur[1] + p[5]

	state.cur[0] = e[0]
	state.cur[1] = e[1]

	var ts, tc, td, te [2]float32

	svgMtxVecMul(&ts, &state.ctm, s)
	svgMtxVecMul(&tc, &state.ctm, c)
	svgMtxVecMul(&td, &state.ctm, d)
	svgMtxVecMul(&te, &state.ctm, e)

	cmdCurve(state, ts, tc, td, te)
}

// cmdCurvetoAbs — C: cmd_curveto_abs (svg.c:261-287).
func cmdCurvetoAbs(state *svgState, p []float32) {
	var s, c, d, e [2]float32

	s[0] = state.cur[0]
	s[1] = state.cur[1]

	c[0] = p[0]
	c[1] = p[1]

	state.lastCtrl[0] = p[2]
	d[0] = state.lastCtrl[0]
	state.lastCtrl[1] = p[3]
	d[1] = state.lastCtrl[1]

	e[0] = p[4]
	e[1] = p[5]

	state.cur[0] = e[0]
	state.cur[1] = e[1]

	var ts, tc, td, te [2]float32

	svgMtxVecMul(&ts, &state.ctm, s)
	svgMtxVecMul(&tc, &state.ctm, c)
	svgMtxVecMul(&td, &state.ctm, d)
	svgMtxVecMul(&te, &state.ctm, e)

	cmdCurve(state, ts, tc, td, te)
}

// cmdShorthandRel — C: cmd_shorthand_rel (svg.c:293-320).
func cmdShorthandRel(state *svgState, p []float32) {
	var s, c, d, e [2]float32

	s[0] = state.cur[0]
	s[1] = state.cur[1]

	c[0] = state.cur[0] + (state.cur[0] - state.lastCtrl[0])
	c[1] = state.cur[1] + (state.cur[1] - state.lastCtrl[1])

	state.lastCtrl[0] = state.cur[0] + p[0]
	d[0] = state.lastCtrl[0]
	state.lastCtrl[1] = state.cur[1] + p[1]
	d[1] = state.lastCtrl[1]

	e[0] = state.cur[0] + p[2]
	e[1] = state.cur[1] + p[3]

	state.cur[0] = e[0]
	state.cur[1] = e[1]

	var ts, tc, td, te [2]float32

	svgMtxVecMul(&ts, &state.ctm, s)
	svgMtxVecMul(&tc, &state.ctm, c)
	svgMtxVecMul(&td, &state.ctm, d)
	svgMtxVecMul(&te, &state.ctm, e)

	cmdCurve(state, ts, tc, td, te)
}

// cmdShorthandAbs — C: cmd_shorthand_abs (svg.c:323-350).
func cmdShorthandAbs(state *svgState, p []float32) {
	var s, c, d, e [2]float32

	s[0] = state.cur[0]
	s[1] = state.cur[1]

	c[0] = state.cur[0] + (state.cur[0] - state.lastCtrl[0])
	c[1] = state.cur[1] + (state.cur[1] - state.lastCtrl[1])

	state.lastCtrl[0] = p[0]
	d[0] = state.lastCtrl[0]
	state.lastCtrl[1] = p[1]
	d[1] = state.lastCtrl[1]

	e[0] = p[2]
	e[1] = p[3]

	state.cur[0] = e[0]
	state.cur[1] = e[1]

	var ts, tc, td, te [2]float32

	svgMtxVecMul(&ts, &state.ctm, s)
	svgMtxVecMul(&tc, &state.ctm, c)
	svgMtxVecMul(&td, &state.ctm, d)
	svgMtxVecMul(&te, &state.ctm, e)

	cmdCurve(state, ts, tc, td, te)
}

// cmdLineto — C: cmd_lineto (svg.c:356-362).
func cmdLineto(state *svgState) {
	var pt [2]float32
	svgMtxVecMul(&pt, &state.ctm, state.cur)
	VecEmitF1(state.icv, VcLineTo, pt)
}

// cmdLinetoRel — C: cmd_lineto_rel (svg.c:366-372).
func cmdLinetoRel(state *svgState, p []float32) {
	state.cur[0] += p[0]
	state.cur[1] += p[1]
	cmdLineto(state)
}

// cmdLinetoAbs — C: cmd_lineto_abs (svg.c:378-384).
func cmdLinetoAbs(state *svgState, p []float32) {
	state.cur[0] = p[0]
	state.cur[1] = p[1]
	cmdLineto(state)
}

// cmdHorizontalRel — C: cmd_horizontal_rel (svg.c:388-393).
func cmdHorizontalRel(state *svgState, p []float32) {
	state.cur[0] += p[0]
	cmdLineto(state)
}

// cmdHorizontalAbs — C: cmd_horizontal_abs (svg.c:397-402).
func cmdHorizontalAbs(state *svgState, p []float32) {
	state.cur[0] = p[0]
	cmdLineto(state)
}

// cmdVerticalRel — C: cmd_vertical_rel (svg.c:406-411).
func cmdVerticalRel(state *svgState, p []float32) {
	state.cur[1] += p[0]
	cmdLineto(state)
}

// cmdVerticalAbs — C: cmd_vertical_abs (svg.c:415-420).
func cmdVerticalAbs(state *svgState, p []float32) {
	state.cur[1] = p[0]
	cmdLineto(state)
}

// cmdClose — C: cmd_close (svg.c:426-435).
func cmdClose(state *svgState) {
	VecEmit0(state.icv, VcClose)

	state.cur[0] = state.first[0]
	state.cur[1] = state.first[1]
	state.closed = true
}

// strokePath — C: stroke_path (svg.c:440-553).
func strokePath(state *svgState, str string) {
	var values [6]float32
	numParams := 0
	curParam := 0
	var curCmd, nextCmd func(state *svgState, params []float32)

	i := 0
	for i < len(str) {
		if str[i] < 33 {
			i++
			continue
		}

		if curCmd != nil {
			d, end := misc.MyStr2double(str[i:])
			if end != 0 {
				values[curParam] = float32(d)
				curParam++
				if curParam == numParams {
					curCmd(state, values[:])
					curCmd = nextCmd
					curParam = 0
				}

				i += end
				i = skipWS(str, i)
				if i < len(str) && str[i] == ',' {
					i++
				}
				i = skipWS(str, i)
				continue
			}
		}

		i = skipWS(str, i)
		if i >= len(str) {
			break
		}

		mode := str[i]
		i++
		switch mode {
		case 'M':
			curCmd = cmdMoveAbs
			nextCmd = cmdLinetoAbs
			numParams = 2
		case 'm':
			curCmd = cmdMoveRel
			nextCmd = cmdLinetoRel
			numParams = 2
		case 'c':
			curCmd = cmdCurvetoRel
			nextCmd = curCmd
			numParams = 6
		case 'C':
			curCmd = cmdCurvetoAbs
			nextCmd = curCmd
			numParams = 6
		case 's':
			curCmd = cmdShorthandRel
			nextCmd = curCmd
			numParams = 4
		case 'S':
			curCmd = cmdShorthandAbs
			nextCmd = curCmd
			numParams = 4
		case 'l':
			curCmd = cmdLinetoRel
			nextCmd = curCmd
			numParams = 2
		case 'L':
			curCmd = cmdLinetoAbs
			nextCmd = curCmd
			numParams = 2
		case 'v':
			curCmd = cmdVerticalRel
			nextCmd = curCmd
			numParams = 1
		case 'V':
			curCmd = cmdVerticalAbs
			nextCmd = curCmd
			numParams = 1
		case 'h':
			curCmd = cmdHorizontalRel
			nextCmd = curCmd
			numParams = 1
		case 'H':
			curCmd = cmdHorizontalAbs
			nextCmd = curCmd
			numParams = 1
		case 'z', 'Z':
			curCmd = nil
			numParams = 0
			cmdClose(state)
		default:
			// C: printf("Cant handle mode %c\n", mode); return
			return
		}
	}
}

// strokePathElement — C: stroke_path_element (svg.c:559-568).
func strokePathElement(s *svgState, attribs *htsmsg.HTSMsg) int {
	d := attribs.GetStr("d")
	if d == "" {
		return -1
	}
	strokePath(s, d)
	return 0
}

// strokeRectElement — C: stroke_rect_element (svg.c:574-605).
func strokeRectElement(s *svgState, attribs *htsmsg.HTSMsg) int {
	strWidth := attribs.GetStr("width")
	strHeight := attribs.GetStr("height")
	strX := attribs.GetStr("x")
	strY := attribs.GetStr("y")

	if strWidth == "" || strHeight == "" || strX == "" || strY == "" {
		return -1
	}

	width, _ := misc.MyStr2double(strWidth)
	height, _ := misc.MyStr2double(strHeight)
	x, _ := misc.MyStr2double(strX)
	y, _ := misc.MyStr2double(strY)

	var v [2]float32

	v[0] = float32(x)
	v[1] = float32(y)
	cmdMoveAbs(s, v[:])
	v[0] += float32(width)
	cmdLinetoAbs(s, v[:])
	v[1] += float32(height)
	cmdLinetoAbs(s, v[:])
	v[0] = float32(x)
	cmdLinetoAbs(s, v[:])
	cmdClose(s)
	return 0
}

// strokePolygonElement — C: stroke_polygon_element
// (svg.c:611-646).
func strokePolygonElement(s *svgState, attribs *htsmsg.HTSMsg) int {
	str := attribs.GetStr("points")
	if str == "" {
		return -1
	}

	num := 0
	i := 0
	for {
		var v [2]float32
		i = skipWS(str, i)
		x0, end := misc.MyStr2double(str[i:])
		if end == 0 {
			break
		}
		v[0] = float32(x0)
		i += end
		i = skipWS(str, i)
		if i < len(str) && str[i] == ',' {
			i++
		}
		i = skipWS(str, i)
		y0, end := misc.MyStr2double(str[i:])
		if end == 0 {
			break
		}
		v[1] = float32(y0)
		i += end

		if num == 0 {
			cmdMoveAbs(s, v[:])
		} else {
			cmdLinetoAbs(s, v[:])
		}
		num++
	}
	cmdClose(s)
	return 0
}

// svgParseElement — C: svg_parse_element (svg.c:649-738).
func svgParseElement(s0 *svgState, element *htsmsg.HTSMsg,
	elementParser func(s *svgState, element *htsmsg.HTSMsg) int) {
	s := *s0

	fillColor := uint32(0xffffffff)
	strokeColor := uint32(0xffffffff)
	strokeWidth := 0

	a := element
	if a == nil {
		return
	}

	st := a.GetStr("style")
	if st != "" {
		for attr := range strings.SplitSeq(st, ";") {
			before, after, ok := strings.Cut(attr, ":")
			if !ok {
				continue
			}
			name := before
			value := after
			for len(value) > 0 && value[0] < 33 {
				value = value[1:]
			}

			switch name {
			case "fill":
				if value == "none" {
					fillColor = 0
				} else {
					fillColor = (fillColor & 0xff000000) |
						misc.HtmlMakecolor(value)
				}
			case "stroke":
				if value == "none" {
					strokeColor = 0
				} else {
					strokeColor = (strokeColor & 0xff000000) |
						misc.HtmlMakecolor(value)
				}
			case "stroke-width":
				strokeWidth, _ = strconv.Atoi(value)
			}
		}
	}

	fill := a.GetStr("fill")
	if fill != "" {
		if fill == "none" {
			fillColor = 0
		} else {
			fillColor = (fillColor & 0xff000000) |
				misc.HtmlMakecolor(fill)
		}
	}

	if s.icv == nil {
		return
	}

	transform := a.GetStr("transform")
	if transform != "" {
		svgParseTransform(&s, transform)
	}

	VecEmit0(s.icv, VcBegin)

	if fillColor != 0 {
		VecEmitI1(s.icv, VcSetFillEnable, 1)
		VecEmitI1(s.icv, VcSetFillColor, int32(fillColor))
	}

	if strokeWidth != 0 {
		VecEmitI1(s.icv, VcSetStrokeWidth, int32(strokeWidth))
		VecEmitI1(s.icv, VcSetStrokeColor, int32(strokeColor))
	}

	s.cur[0] = 0
	s.cur[1] = 0

	if elementParser(&s, a) != 0 {
		return
	}

	cmdClose(&s)
	VecEmit0(s.icv, VcEnd)
}

// svgParseRoot — C: svg_parse_root (svg.c:748-770).
func svgParseRoot(s *svgState, tags *htsmsg.HTSMsg) {
	for _, f := range tags.GetFields() {
		c := f.GetMap()
		if c == nil {
			continue
		}
		s.closed = true
		switch f.GetName() {
		case "path":
			svgParseElement(s, c, strokePathElement)
		case "rect":
			svgParseElement(s, c, strokeRectElement)
		case "polygon":
			svgParseElement(s, c, strokePolygonElement)
		case "g":
			svgParseG(s, c)
		}
	}
}

// svgParseG — C: svg_parse_g (svg.c:772-784).
func svgParseG(s0 *svgState, c *htsmsg.HTSMsg) {
	s := *s0
	transform := c.GetStr("transform")
	if transform != "" {
		svgParseTransform(&s, transform)
	}
	svgParseRoot(&s, c)
}

// svgDecode1 — C: svg_decode1 (svg.c:787-850).
func svgDecode1(doc *htsmsg.HTSMsg, meta *ImageMeta) (*Image, error) {
	var state svgState
	svg := doc.GetMap("svg")
	if svg == nil {
		return nil, errors.New("Missing SVG tag")
	}

	offsetX := float32(0)
	offsetY := float32(0)
	var origWidth, origHeight int

	viewbox := svg.GetStr("viewBox")
	if viewbox != "" {
		x1, e1 := misc.MyStr2double(viewbox)
		y1, e2 := misc.MyStr2double(viewbox[e1:])
		x2, e3 := misc.MyStr2double(viewbox[e1+e2:])
		y2, _ := misc.MyStr2double(viewbox[e1+e2+e3:])
		_ = y1
		origWidth = int(x2)
		origHeight = int(y2)

		offsetX = float32(-x1)
		offsetY = float32(-y1)
	} else {
		origWidth = int(svg.GetU32OrDefault("width", 0))
		origHeight = int(svg.GetU32OrDefault("height", 0))
	}

	if origWidth < 1 || origHeight < 1 {
		return nil, fmt.Errorf("Invalid SVG dimensions (%d x %d)",
			origWidth, origHeight)
	}

	var w, h int
	if meta.ReqWidth != -1 && meta.ReqHeight != -1 {
		return nil, errors.New("Aspect distortion not supported for SVG")
	} else if meta.ReqWidth != -1 {
		w = meta.ReqWidth
		h = meta.ReqWidth * origHeight / origWidth
	} else if meta.ReqHeight != -1 {
		w = meta.ReqHeight * origWidth / origHeight
		h = meta.ReqHeight
	} else {
		w = origWidth
		h = origHeight
	}

	state.scaling = float32(w) / float32(origWidth)
	svgMtxIdentity(&state.ctm)
	svgMtxScale(&state.ctm, float32(w)/float32(origWidth),
		float32(h)/float32(origHeight))
	svgMtxTranslate(&state.ctm, offsetX, offsetY)

	img := CreateVector(w, h, int(meta.Margin))
	state.icv = &img.Components[0].Vector
	svgParseRoot(&state, svg)
	return img, nil
}

// SvgDecode — C: svg_decode (svg.c:855-867). The retained buf is
// released to mirror htsmsg_xml_deserialize_buf consuming it.
func SvgDecode(buf *misc.Buf, meta *ImageMeta) (*Image, error) {
	buf = buf.Retain()
	doc, err := htsmsg.DeserializeXMLBuf(buf.C8())
	buf.Release()
	if doc == nil || err != nil {
		return nil, errors.New("Unable to parse XML")
	}
	img, derr := svgDecode1(doc, meta)
	doc.Release()
	return img, derr
}
