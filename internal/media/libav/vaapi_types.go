package libav

// VAAPI dmabuf descriptor types — kept in an untagged file because
// pkg/ui/glw references VaapiDmabufDesc in shared video-surface state
// (glw_h.go) on all platforms.
// (C: VADRMPIMESurfaceDescriptor, libavutil/hwcontext_vaapi.h)

// VaapiDmabufLayer — one layer of an exported dmabuf surface
// (VA_EXPORT_SURFACE_SEPARATE_LAYERS: R8 luma + GR88 chroma for NV12).
type VaapiDmabufLayer struct {
	FourCC      uint32
	NumPlanes   int
	ObjectIndex [4]uint32
	Offset      [4]uint32
	Pitch       [4]uint32
}

// VaapiDmabufDesc — flat copy of VADRMPIMESurfaceDescriptor.
// Fd entries are valid (>= 0) for NumObjects objects; the caller owns
// them and must close them (FdClose) after the EGLImage is created or
// on failure.
type VaapiDmabufDesc struct {
	FourCC     uint32
	Width      int
	Height     int
	NumObjects int
	NumLayers  int
	Fd         [4]int32
	Modifier   [4]uint64
	Layer      [4]VaapiDmabufLayer
}
