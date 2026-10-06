package image

// Pure-Go FreeType backend — wasm2go-translated FreeType 2.13.3
// (internal/ftwasm). Same FT_* calls as rasterizer_cgo.go.
//
// FT_Span in wasm memory: int16 x, uint16 len, uint8 coverage —
// 6-byte stride, little-endian (see ftimage.h).

import (
	"encoding/binary"
	"sync"

	"github.com/czz/movian-go/internal/ftwasm"
)

var (
	ftwMod      *ftwasm.Module // module owning ftLib
	ftLib       uint32         // C: static FT_Library ft_lib
	ftMutex     sync.Mutex     // C: HTS_MUTEX_DECL(ft_mutex)
	ftStartOnce sync.Once
)

type rtStroker uintptr // C: FT_Stroker
type rtOutline uintptr // C: *FT_Outline (address of a C-space struct)

const (
	rtStrokerBorderLeft    = 0 // FT_STROKER_BORDER_LEFT
	rtStrokerLinecapButt   = 0 // FT_STROKER_LINECAP_BUTT
	rtStrokerLinejoinBevel = 1 // FT_STROKER_LINEJOIN_BEVEL
)

// ftwHost — w2g imports for the image module: only gray_spans is
// reachable (the rasterizer never opens faces).
type ftwHost struct{}

func (ftwHost) FaceRead(int64, int32, int32, int32) int32 { return 0 }
func (ftwHost) FaceClose(int64)                           {}

// ftwGraySpans — C: params.gray_spans dispatch (rasterizer_ft.c:328-336).
func (ftwHost) GraySpans(y, count, spans int32, _ int64) {
	mem := ftwasm.Mem(ftwMod)
	arr := make([]ftSpan, count)
	for i := int32(0); i < count; i++ {
		p := spans + i*6
		arr[i].x = int16(binary.LittleEndian.Uint16(mem[p:]))
		arr[i].len = binary.LittleEndian.Uint16(mem[p+2:])
		arr[i].coverage = mem[p+4]
	}
	rasterizeSpans(int(y), arr)
}

func rtInitLib() int {
	m := ftwasm.NewModule(ftwHost{})
	r := m.Xftw_init()
	if e := int32(r >> 32); e != 0 {
		return int(e)
	}
	ftwMod = m
	ftLib = uint32(r)
	return 0
}

func rtStrokerNew() rtStroker {
	r := ftwMod.Xftw_stroker_create(int32(ftLib))
	return rtStroker(uint32(r))
}

func rtStrokerSet(s rtStroker, radius int64, cap, join uint32, miter int64) {
	ftwMod.Xftw_stroker_set(int32(s), radius, int32(cap), int32(join), miter)
}
func rtStrokerRewind(s rtStroker) {
	ftwMod.Xftw_stroker_rewind(int32(s))
}
func rtStrokerBeginSubPath(s rtStroker, x, y int64, open int) {
	ftwMod.Xftw_stroker_begin(int32(s), x, y, int32(open))
}
func rtStrokerLineTo(s rtStroker, x, y int64) {
	ftwMod.Xftw_stroker_lineto(int32(s), x, y)
}
func rtStrokerCubicTo(s rtStroker, x1, y1, x2, y2, x3, y3 int64) {
	ftwMod.Xftw_stroker_cubicto(int32(s), x1, y1, x2, y2, x3, y3)
}
func rtStrokerEndSubPath(s rtStroker) {
	ftwMod.Xftw_stroker_endsubpath(int32(s))
}
func rtStrokerDone(s rtStroker) {
	ftwMod.Xftw_stroker_done(int32(s))
}

func rtStrokerGetCounts(s rtStroker) (points, contours int) {
	r := ftwMod.Xftw_stroker_get_counts(int32(s))
	return int(uint32(r >> 32)), int(uint32(r))
}
func rtStrokerGetBorderCounts(s rtStroker, border uint32) (points, contours int) {
	r := ftwMod.Xftw_stroker_get_border_counts(int32(s), int32(border))
	return int(uint32(r >> 32)), int(uint32(r))
}
func rtStrokerExport(s rtStroker, ol rtOutline) {
	ftwMod.Xftw_stroker_export(int32(s), int32(ol))
}
func rtStrokerExportBorder(s rtStroker, border uint32, ol rtOutline) {
	ftwMod.Xftw_stroker_export_border(int32(s), int32(border), int32(ol))
}

// rtOutlineNew — C: FT_Outline_New; the outline struct lives in the
// module heap (calloc'd by the glue) so its address is stable.
func rtOutlineNew(points, contours int) rtOutline {
	r := ftwMod.Xftw_outline_new(int32(ftLib), int32(points), int32(contours))
	return rtOutline(uint32(r))
}
func rtOutlineClearCounts(ol rtOutline) {
	ftwMod.Xftw_outline_clear(int32(ol))
}
func rtOutlineRender(ol rtOutline) {
	// glue sets FT_Raster_Params{AA|DIRECT, gray_spans, user=NULL}
	ftwMod.Xftw_outline_render(int32(ftLib), int32(ol))
}
func rtOutlineDone(ol rtOutline) {
	ftwMod.Xftw_outline_done(int32(ftLib), int32(ol))
}
