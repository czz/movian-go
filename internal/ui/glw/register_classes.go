package glw

// RegisterBuiltinClasses registers all built-in GLW widget classes and the
// untagged video engine. C-parity: GLW_REGISTER_CLASS/GLW_REGISTER_GVE
// constructors ran pre-main; Go calls this explicitly from the composition
// root. Order = the previous per-file init() order (alphabetical file order),
// which the registry list inverts (prepend). Tagged platform registrations
// (video_opengl/vaapi/vdpau/rpi/sunxi/android, glw_rec) keep their init() as
// platform seams and run at package load.
func RegisterBuiltinClasses() {
	if glwBuiltinRegistered {
		return
	}
	glwBuiltinRegistered = true
	registerArray()
	registerBar()
	registerBloom()
	registerClip()
	registerClist()
	registerContainer()
	registerCoverflow()
	registerCube()
	registerCursor()
	registerDeck()
	registerDetachable()
	registerDisplacement()
	registerDummy()
	registerExpander()
	registerFlicker()
	registerFreefloat()
	registerImage()
	registerKeyintercept()
	registerLayer()
	registerList()
	registerMirror()
	registerPlayfield()
	registerPopup()
	registerPrimitives()
	registerResizer()
	registerRotator()
	registerSegway()
	registerSlider()
	registerSlideshow()
	registerStyle()
	registerTextBitmap()
	registerThrobber()
	registerUnderscan()
	registerVideoCommona()
	registerVideoCommonb()
	registerViewLoader()
}

var glwBuiltinRegistered bool
