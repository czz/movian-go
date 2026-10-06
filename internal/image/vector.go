package image

// Canonical port of src/image/vector.{c,h}.
//
// The vector component is a command buffer: icv_data is a single
// buffer of 32-bit slots, interpreted as int32 (opcodes/int args) or
// float32 (coords) via the C union icv_int/icv_flt. In Go the buffer
// is []int32 and floats are stored/loaded with math.Float32bits /
// math.Float32frombits.

import "math"

// VecCmd — C: vec_cmd_t (vector.h).
type VecCmd int32

const (
	VcSetFillEnable  VecCmd = iota // VC_SET_FILL_ENABLE
	VcSetFillColor                 // VC_SET_FILL_COLOR
	VcSetStrokeWidth               // VC_SET_STROKE_WIDTH
	VcSetStrokeColor               // VC_SET_STROKE_COLOR
	VcBegin                        // VC_BEGIN
	VcEnd                          // VC_END
	VcMoveTo                       // VC_MOVE_TO
	VcLineTo                       // VC_LINE_TO
	VcCubicTo                      // VC_CUBIC_TO
	VcClose                        // VC_CLOSE
	VcNum                          // VC_num
)

// vecCmdLen — C: vec_cmd_len[VC_num] (vector.c:25-33).
var vecCmdLen = [VcNum]int{
	VcSetFillEnable:  1,
	VcSetFillColor:   1,
	VcSetStrokeWidth: 1,
	VcSetStrokeColor: 1,
	VcMoveTo:         2,
	VcLineTo:         2,
	VcCubicTo:        6,
}

// VecFloat — C: icv_flt[] read — reinterpret int32 slot as float32.
func VecFloat(icv *VectorComponent, i int) float32 {
	return math.Float32frombits(uint32(icv.IntData[i]))
}

// VecSetFloat — C: icv_flt[i] = f.
func VecSetFloat(icv *VectorComponent, i int, f float32) {
	icv.IntData[i] = int32(math.Float32bits(f))
}

// vecResize — C: vec_resize (vector.c:39-47).
func vecResize(icv *VectorComponent, cmd VecCmd) {
	constLen := vecCmdLen[cmd] + 1
	if icv.Used+constLen > icv.Capacity {
		icv.Capacity = 2*icv.Capacity + constLen + 16
		nd := make([]int32, icv.Capacity)
		copy(nd, icv.IntData)
		icv.IntData = nd
	}
}

// VecEmit0 — C: vec_emit_0 (vector.c:53-58).
func VecEmit0(icv *VectorComponent, cmd VecCmd) {
	vecResize(icv, cmd)
	icv.IntData[icv.Used] = int32(cmd)
	icv.Used++
}

// VecEmitI1 — C: vec_emit_i1 (vector.c:64-80).
func VecEmitI1(icv *VectorComponent, cmd VecCmd, i int32) {
	vecResize(icv, cmd)

	switch cmd {
	case VcSetFillColor, VcSetStrokeColor:
		icv.Colorized = true
	}

	icv.IntData[icv.Used] = int32(cmd)
	icv.Used++
	icv.IntData[icv.Used] = i
	icv.Used++
}

// VecEmitF1 — C: vec_emit_f1 (vector.c:86-93).
func VecEmitF1(icv *VectorComponent, cmd VecCmd, a [2]float32) {
	vecResize(icv, cmd)
	icv.IntData[icv.Used] = int32(cmd)
	icv.Used++
	VecSetFloat(icv, icv.Used, a[0])
	icv.Used++
	VecSetFloat(icv, icv.Used, a[1])
	icv.Used++
}

// VecEmitF3 — C: vec_emit_f3 (vector.c:99-113).
func VecEmitF3(icv *VectorComponent, cmd VecCmd, a, b, c [2]float32) {
	vecResize(icv, cmd)
	ptr := icv.Used
	icv.IntData[ptr+0] = int32(cmd)
	VecSetFloat(icv, ptr+1, a[0])
	VecSetFloat(icv, ptr+2, a[1])
	VecSetFloat(icv, ptr+3, b[0])
	VecSetFloat(icv, ptr+4, b[1])
	VecSetFloat(icv, ptr+5, c[0])
	VecSetFloat(icv, ptr+6, c[1])
	icv.Used = ptr + 7
}
