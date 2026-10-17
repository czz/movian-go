package glw

import (
	"errors"
	"testing"

	backendcore "github.com/czz/movian-go/internal/backend/core"
	imagepkg "github.com/czz/movian-go/internal/image"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

func TestGLWTextureFlagsMatchCBitAssignments(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want uint32
	}{
		{name: "repeat", got: glwTexRepeat, want: 0x80000000},
		{name: "intensity analysis", got: glwTexIntensityAnalysis, want: 0x40000000},
		{name: "primary color analysis", got: glwTexPrimaryColorAnalysis, want: 0x20000000},
		{name: "corner top-left", got: glwImageCornerTopleft, want: 0x1},
		{name: "corner top-right", got: glwImageCornerTopright, want: 0x2},
		{name: "corner bottom-left", got: glwImageCornerBottomleft, want: 0x4},
		{name: "corner bottom-right", got: glwImageCornerBottomright, want: 0x8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := uint32(tc.got); got != tc.want {
				t.Fatalf("flag bits = %#08x, want %#08x", got, tc.want)
			}
		})
	}
}

func TestGLWImageCornerFlagsSetAndClear(t *testing.T) {
	corners := glwImageCornerTopleft | glwImageCornerTopright |
		glwImageCornerBottomleft | glwImageCornerBottomright
	gi := &GlwImage{}

	modImageFlags(&gi.w, corners|glwImageBorderLeft, 0, nil)
	if want := corners | glwImageBorderLeft; gi.giBitmapFlags != want {
		t.Fatalf("set corners: flags = %#x, want %#x", gi.giBitmapFlags, want)
	}

	modImageFlags(&gi.w, 0, glwImageCornerTopleft, nil)
	if want := (corners &^ glwImageCornerTopleft) | glwImageBorderLeft; gi.giBitmapFlags != want {
		t.Fatalf("clear top-left: flags = %#x, want %#x", gi.giBitmapFlags, want)
	}

	modImageFlags(&gi.w, 0, corners, nil)
	if gi.giBitmapFlags != glwImageBorderLeft {
		t.Fatalf("clear all corners: flags = %#x, want %#x",
			gi.giBitmapFlags, glwImageBorderLeft)
	}
}

func findTokenAttrib(t *testing.T, name string) *tokenAttrib {
	t.Helper()
	for i := range attribtab {
		if attribtab[i].name == name {
			return &attribtab[i]
		}
	}
	t.Fatalf("no view attribute named %q", name)
	return nil
}

func TestGLWCornerViewAttributesSetAndClearImageCorners(t *testing.T) {
	for _, tc := range []struct {
		name string
		bit  int
	}{
		{name: "cornerTopLeft", bit: glwImageCornerTopleft},
		{name: "cornerTopRight", bit: glwImageCornerTopright},
		{name: "cornerBottomLeft", bit: glwImageCornerBottomleft},
		{name: "cornerBottomRight", bit: glwImageCornerBottomright},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := findTokenAttrib(t, tc.name)
			if a.attrib != tc.bit || !fnIs(a.fn, modImgFlags) {
				t.Fatalf("attrib = %#x, modImgFlags = %t; want %#x via modImgFlags",
					a.attrib, fnIs(a.fn, modImgFlags), tc.bit)
			}

			gi := &GlwImage{}
			gi.w.glwClass = &glwClass{gcModImageFlags: modImageFlags}
			gi.w.glwRoot = &glwRoot{}
			ec := &glwViewEvalContext{w: &gi.w}

			modFlag(ec, a, &Token{typ: tokenInt, tInt: 1})
			if gi.giBitmapFlags != tc.bit {
				t.Fatalf("set: flags = %#x, want %#x", gi.giBitmapFlags, tc.bit)
			}
			modFlag(ec, a, &Token{typ: tokenInt, tInt: 0})
			if gi.giBitmapFlags != 0 {
				t.Fatalf("clear: flags = %#x, want 0", gi.giBitmapFlags)
			}
		})
	}
}

func TestGLWTextureLoaderPassesCornerSelectionToPixels(t *testing.T) {
	gr := &glwRoot{grWidth: 8, grHeight: 8, grTexThreadsRunning: 1}
	for i := range gr.grTexLoadQueue {
		q := &gr.grTexLoadQueue[i]
		q.tqhLast = &q.tqhFirst
	}
	gr.grTexRelQueue.tqhLast = &gr.grTexRelQueue.tqhFirst
	for i := range gr.grTexStash {
		q := &gr.grTexStash[i].q
		q.tqhLast = &q.tqhFirst
		gr.grTexStash[i].limit = 0
	}

	var gotMeta backendcore.ImageMeta
	be := &backendcore.Backend{
		Imageloader: func(_ string, imageMeta any, _ *int, _ any,
			_ *backendcore.Backend) (any, error) {
			meta, ok := imageMeta.(*backendcore.ImageMeta)
			if !ok {
				return nil, errors.New("GLW did not pass ImageMeta")
			}
			gotMeta = *meta
			gr.grTexThreadsRunning = 0

			pm := imagepkg.PixmapCreate(8, 8, imagepkg.PixmapBGR32, 0)
			for y := range pm.Height {
				for x := range pm.Width {
					pm.Data[y*pm.Stride+x*4+3] = 0xff
				}
			}
			img := imagepkg.CreateFromPixmap(pm)
			imagepkg.PixmapRelease(pm)
			return img, nil
		},
	}
	bs := backendcore.NewBackendSystem(propcore.NewPropManager(), nil, nil, nil)
	previousBackendSystem := glwDeps.bs
	glwDeps.bs = bs
	defer func() { glwDeps.bs = previousBackendSystem }()

	gi := &GlwImage{
		w: Glw{
			glwRoot:  gr,
			glwScope: &glwScope{gsBackend: be},
		},
	}
	glwImageCtor(&gi.w)
	modImageFlags(&gi.w, 0, glwTexCornerTopright|glwTexCornerBottomleft, nil)
	gi.giRadius = 4

	url := miscpkg.RstrAllocStr("rounded-corners:test")
	glt := glwImageTexLoad(gi, url, 8, 8)
	defer func() {
		glt.gltState = gltStateInactive
		glwTexDeref(gr, glt)
		glwTexPurge(gr)
		for i := range gr.grTexStash {
			glwTexPurgeStash(gr, i)
		}
		miscpkg.RstrRelease(url)
	}()

	gltEnqueue(gr, glt, lqOther)
	loaderThread(&loaderaux{laGr: gr})

	if glt.gltState != gltStateValid {
		t.Fatalf("texture state = %d, want valid", glt.gltState)
	}
	if got, want := gotMeta.CornerSelection,
		uint8(imagepkg.PixmapCornerTopLeft|imagepkg.PixmapCornerBottomRight); got != want {
		t.Errorf("corner selection = %#x, want %#x", got, want)
	}
	if gotMeta.CornerRadius != 4 {
		t.Errorf("corner radius = %d, want 4", gotMeta.CornerRadius)
	}
	if gotMeta.IntensityAnalysis || gotMeta.PrimaryColorAnalysis {
		t.Errorf("corner selection enabled unrelated image analysis: intensity=%t primary=%t",
			gotMeta.IntensityAnalysis, gotMeta.PrimaryColorAnalysis)
	}
	if glt.gltFlags&glwTexRepeat != 0 {
		t.Errorf("corner selection enabled texture repeat: flags=%#x", uint32(glt.gltFlags))
	}
	if glt.gltFlags&glwTexIntensityAnalysis != 0 ||
		glt.gltFlags&glwTexPrimaryColorAnalysis != 0 {
		t.Errorf("corner selection enabled texture analysis: flags=%#x", uint32(glt.gltFlags))
	}

	pm := glt.gltPixmap
	if pm == nil {
		t.Fatal("texture loader did not produce a pixmap")
	}
	alphaAt := func(x, y int) byte { return pm.Data[y*pm.Stride+x*4+3] }
	for _, pixel := range []struct {
		x, y int
		want byte
	}{
		{x: 0, y: 0, want: 0},
		{x: 7, y: 7, want: 0},
		{x: 7, y: 0, want: 0xff},
		{x: 0, y: 7, want: 0xff},
		{x: 3, y: 3, want: 0xff},
	} {
		if got := alphaAt(pixel.x, pixel.y); got != pixel.want {
			t.Errorf("alpha at (%d,%d) = %#02x, want %#02x",
				pixel.x, pixel.y, got, pixel.want)
		}
	}
}
