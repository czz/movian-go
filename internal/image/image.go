package image

// Canonical port of src/image/image.{c,h} — the image_t container,
// components, and the decode dispatch.

import (
	"sync/atomic"

	"github.com/czz/movian-go/internal/gconf"
	misc "github.com/czz/movian-go/internal/misc"
)

// ImageMeta — C: image_meta_t (image.h:33-51). Control struct for
// loading images.
type ImageMeta struct {
	ReqAspect            float64            // C: im_req_aspect
	ReqWidth             int                // C: im_req_width
	ReqHeight            int                // C: im_req_height
	MaxWidth             int                // C: im_max_width
	MaxHeight            int                // C: im_max_height
	CanMono              bool               // C: im_can_mono
	NoDecoding           bool               // C: im_no_decoding
	Bit32Swizzle         bool               // C: im_32bit_swizzle
	WantThumb            bool               // C: im_want_thumb
	IntensityAnalysis    bool               // C: im_intensity_analysis
	PrimaryColorAnalysis bool               // C: im_primary_color_analysis
	ForceLocalLoad       bool               // C: im_force_local_load
	CornerSelection      uint8              // C: im_corner_selection
	CornerRadius         uint16             // C: im_corner_radius
	Shadow               uint16             // C: im_shadow
	Margin               uint16             // C: im_margin
	Opaque               any                // C: im_opaque
	Incremental          func(any, *Pixmap) // C: im_incremental
}

// ComponentType — C: image_component_type (image.h:63-69).
type ComponentType int

const (
	ComponentNone     ComponentType = iota // IMAGE_component_none
	ComponentPixmap                        // IMAGE_PIXMAP
	ComponentCoded                         // IMAGE_CODED
	ComponentVector                        // IMAGE_VECTOR
	ComponentTextInfo                      // IMAGE_TEXT_INFO
)

// CodedType — C: image_coded_type (image.h:75-82). Sent over wire in
// STPP — values must not be changed.
type CodedType int

const (
	CodedNone CodedType = iota // IMAGE_coded_none
	CodedPNG  CodedType = 1    // IMAGE_PNG
	CodedJPEG CodedType = 2    // IMAGE_JPEG
	CodedGIF  CodedType = 3    // IMAGE_GIF
	CodedSVG  CodedType = 4    // IMAGE_SVG
	CodedBMP  CodedType = 5    // IMAGE_BMP
	CodedWEBP CodedType = 6    // Go extension — upstream C has no WebP

)

// CodedComponent — C: image_component_coded (image.h:87-90).
type CodedComponent struct {
	Buf  *misc.Buf // C: icc_buf
	Type CodedType // C: icc_type
}

// VectorComponent — C: image_component_vector (image.h:95-100).
// IntData is the icv_data union: slots are int32 opcodes/args or
// float32 coords bit-cast via VecSetFloat/VecFloat (vector.go).
type VectorComponent struct {
	IntData   []int32 // C: union { int32_t *icv_int; float *icv_flt; }
	Used      int     // C: icv_used
	Capacity  int     // C: icv_capacity
	Colorized bool    // C: icv_colorized
}

// TextInfoComponent — C: image_component_text_info (image.h:106-112).
type TextInfoComponent struct {
	CharPos    []int  // C: ti_charpos
	CharPosLen uint16 // C: ti_charposlen
	Lines      uint16 // C: ti_lines
	Flags      uint16 // C: ti_flags
}

// C: IMAGE_TEXT_WRAPPED / IMAGE_TEXT_TRUNCATED (image.h:110-111).
const (
	TextWrapped   = 0x1
	TextTruncated = 0x2
)

// Component — C: image_component_t (image.h:118-127). The union is
// represented by the Type tag plus one field per member.
type Component struct {
	Type     ComponentType // C: type
	Pixmap   *Pixmap       // C: pm
	Coded    CodedComponent
	Vector   VectorComponent
	TextInfo TextInfoComponent
}

// Image flags — C: im_flags bits (image.h:145-152). Sent over the wire
// in STPP — values must not be changed.
const (
	FlagThumbnail   = 0x1 // IMAGE_THUMBNAIL
	FlagProgressive = 0x2 // IMAGE_PROGRESSIVE
	FlagAdapted     = 0x4 // IMAGE_ADAPTED
)

// Image — C: image_t (image.h:135-162).
type Image struct {
	refcount        int32       // C: im_refcount
	Width           uint16      // C: im_width
	Height          uint16      // C: im_height
	Margin          uint16      // C: im_margin
	NumComponents   uint16      // C: im_num_components
	Flags           uint16      // C: im_flags
	ColorPlanes     uint8       // C: im_color_planes
	OriginCodedType uint8       // C: im_origin_coded_type
	Orientation     uint8       // C: im_orientation
	Components      []Component // C: im_components[0]
}

// accelImageDecode — C: accel_image_decode global hook (image.c:27-31).
var accelImageDecode func(cType CodedType, buf *misc.Buf,
	meta *ImageMeta, img *Image) (*Pixmap, error)

// SetAccelImageDecode wires the accelerated image decoder
// (C: hw decode provider — e.g. rpi_pixmap.c under linux_rpi).
func SetAccelImageDecode(fn func(cType CodedType, buf *misc.Buf,
	meta *ImageMeta, img *Image) (*Pixmap, error)) {
	accelImageDecode = fn
}

// Alloc — C: image_alloc (image.c:38-46).
func Alloc(numComponents int) *Image {
	img := &Image{
		refcount:      1,
		NumComponents: uint16(numComponents),
		Components:    make([]Component, numComponents),
	}
	return img
}

// Retain — C: image_retain (image.c:52-57).
func (img *Image) Retain() *Image {
	atomic.AddInt32(&img.refcount, 1)
	return img
}

// ClearComponent — C: image_clear_component (image.c:63-88).
func ClearComponent(ic *Component) {
	switch ic.Type {
	case ComponentNone:
	case ComponentPixmap:
		PixmapRelease(ic.Pixmap)
	case ComponentCoded:
		ic.Coded.Buf.Release()
	case ComponentVector:
		ic.Vector.IntData = nil
	case ComponentTextInfo:
		ic.TextInfo.CharPos = nil
	}
	ic.Type = ComponentNone
}

// Release — C: image_release (image.c:94-107).
func (img *Image) Release() {
	if img == nil {
		return
	}
	if atomic.AddInt32(&img.refcount, -1) != 0 {
		return
	}
	for i := range int(img.NumComponents) {
		ClearComponent(&img.Components[i])
	}
}

// Dump — C: image_dump (image.c:113-160).
func (img *Image) Dump(prefix string) {
	if img == nil {
		return
	}
	// C tracelog output — no-op at this trace level
}

// CodedAlloc — C: image_coded_alloc (image.c:166-183). Returns the
// image and the buf's data as a writable byte slice (*datap).
func CodedAlloc(size int, cType CodedType) (*Image, []byte) {
	b := misc.BufCreate(size)
	if b == nil {
		return nil, nil
	}
	img := Alloc(1)
	img.Components[0].Type = ComponentCoded
	img.Components[0].Coded.Buf = b
	img.Components[0].Coded.Type = cType
	return img, b.C8()
}

// CodedCreateFromData — C: image_coded_create_from_data
// (image.c:189-198).
func CodedCreateFromData(data []byte, cType CodedType) *Image {
	img, mem := CodedAlloc(len(data), cType)
	if img != nil {
		copy(mem, data)
	}
	return img
}

// CodedCreateFromBuf — C: image_coded_create_from_buf
// (image.c:204-215).
func CodedCreateFromBuf(b *misc.Buf, cType CodedType) *Image {
	img := Alloc(1)
	img.Components[0].Type = ComponentCoded
	img.Components[0].Coded.Buf = b.Retain()
	img.Components[0].Coded.Type = cType
	return img
}

// CreateFromPixmap — C: image_create_from_pixmap (image.c:221-233).
func CreateFromPixmap(pm *Pixmap) *Image {
	img := Alloc(1)
	img.Components[0].Type = ComponentPixmap
	img.Components[0].Pixmap = PixmapDup(pm)

	img.Width = uint16(pm.Width)
	img.Height = uint16(pm.Height)
	img.Margin = uint16(pm.Margin)
	return img
}

// imageDecodeCoded — C: image_decode_coded (image.c:239-274).
func imageDecodeCoded(src *Image, meta *ImageMeta, g *gconf.T) (*Image, error) {
	ic := &src.Components[0]
	icc := &ic.Coded

	if icc.Type == CodedSVG {
		return NanosvgDecode(icc.Buf, meta)
	}

	var pm *Pixmap
	var err error

	if accelImageDecode != nil {
		pm, err = accelImageDecode(icc.Type, icc.Buf, meta, src)
	}

	if pm == nil {
		pm, err = ImageDecodeLibAV(icc.Type, icc.Buf, meta, g)
	}

	if pm == nil {
		return nil, err
	}

	// Invert aspect ratio for orientations that rotate image
	// 90/270 deg, etc. Might seem strange, but it does the right
	// thing.
	if src.Orientation >= misc.LAYOUT_ORIENTATION_TRANSPOSE {
		pm.Aspect = 1.0 / pm.Aspect
	}

	newImg := CreateFromPixmap(pm)
	newImg.OriginCodedType = uint8(icc.Type)
	newImg.Orientation = src.Orientation
	PixmapRelease(pm)
	return newImg, nil
}

// imagePostprocessPixmap — C: image_postprocess_pixmap
// (image.c:280-299).
func imagePostprocessPixmap(img *Image, meta *ImageMeta) {
	ic := &img.Components[0]

	if meta.Shadow != 0 {
		PixmapDropShadow(ic.Pixmap, int(meta.Shadow), int(meta.Shadow))
	}

	if meta.CornerRadius != 0 {
		ic.Pixmap = PixmapRoundedCorners(ic.Pixmap, int(meta.CornerRadius),
			int(meta.CornerSelection))
	}

	if meta.IntensityAnalysis {
		PixmapIntensityAnalysis(ic.Pixmap)
	}

	if meta.PrimaryColorAnalysis {
		DominantColor(ic.Pixmap)
	}
}

// Decode — C: image_decode (image.c:305-337). Consumes im (releases it
// unless it is returned).
func Decode(img *Image, meta *ImageMeta, g *gconf.T) (*Image, error) {
	var r *Image
	var err error
	ic := &img.Components[0]

	switch ic.Type {
	case ComponentNone:
		return img, nil

	case ComponentPixmap:
		imagePostprocessPixmap(img, meta)
		return img, nil

	case ComponentCoded:
		r, err = imageDecodeCoded(img, meta, g)
		if r == nil {
			break
		}
		r, err = Decode(r, meta, g)

	case ComponentVector:
		r = ImageRasterizeFT(ic, int(img.Width), int(img.Height),
			int(img.Margin))

	default:
		panic("image_decode: unknown component type") // C: abort()
	}

	img.Release()
	return r, err
}

// CreateVector — C: image_create_vector (image.c:344-356).
func CreateVector(width, height, margin int) *Image {
	img := Alloc(1)
	img.Components[0].Type = ComponentVector
	icv := &img.Components[0].Vector
	img.Width = uint16(width)
	img.Height = uint16(height)
	img.Margin = uint16(margin)

	icv.Capacity = 256
	icv.IntData = make([]int32, icv.Capacity)
	return img
}

// FindComponent — C: image_find_component (image.h:230-239).
func (img *Image) FindComponent(ct ComponentType) *Component {
	if img == nil {
		return nil
	}
	for i := range img.Components {
		if img.Components[i].Type == ct {
			return &img.Components[i]
		}
	}
	return nil
}

// gcfg.EnableImageDebug — C: gconf.enable_image_debug (main.h:252).

// EnableImageDebug — C: gconf.enable_image_debug (cross-pkg readers).
// Parametric: callers pass the process gconf.
func EnableImageDebug(g *gconf.T) bool { return g != nil && g.EnableImageDebug.Load() }
