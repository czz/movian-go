package core

import (
	"errors"
	"fmt"
	"strings"

	imagepkg "github.com/czz/movian-go/internal/image"
)

// bePixmapLoader — C: be_pixmap_loader (src/image/pixmap.c:1061-1094).
func bePixmapLoader(url string, imageMeta any,
	cacheControl *int, cancellable any, backend *Backend) (any, error) {

	var img *imagepkg.Image
	im, _ := imageMeta.(*ImageMeta)
	w, h := -1, -1
	var margin uint16
	if im != nil {
		w = im.ReqWidth
		h = im.ReqHeight
		margin = im.Margin
	}

	if s, ok := strings.CutPrefix(url, "pixmap:gradient:"); ok {
		if w == -1 {
			w = 128
		}
		if h == -1 {
			h = 128
		}
		t := [4]int{0, 0, 0, 255}
		b := [4]int{0, 0, 0, 255}
		n, _ := fmt.Sscanf(s, "%d,%d,%d:%d,%d,%d",
			&t[0], &t[1], &t[2], &b[0], &b[1], &b[2])
		if n != 6 {
			return nil, errors.New("Invalid RGB codes")
		}

		pm := imagepkg.PixmapCreate(w, h, imagepkg.PixmapBGR32, int(margin))
		pm.Flags |= imagepkg.PixmapOpaque
		imagepkg.PixmapHorizontalGradient(pm, t[:], b[:])
		img = imagepkg.CreateFromPixmap(pm)
		imagepkg.PixmapRelease(pm)
		img.Flags |= imagepkg.FlagAdapted

	} else {
		return nil, errors.New("Invalid URL")
	}
	return img, nil
}

// bePixmapCanhandle — C: be_pixmap_canhandle (src/image/pixmap.c:1100-1106).
func bePixmapCanhandle(url string) int {
	if strings.HasPrefix(url, "pixmap:") {
		return 1
	}
	return 0
}

// registerPixmapBackend — C: static backend_t be_pixmap + BE_REGISTER(pixmap)
// (src/image/pixmap.c:1111-1117).
func (bs *BackendSystem) registerPixmapBackend() {
	be := &Backend{
		CanHandle:   bePixmapCanhandle,
		Imageloader: bePixmapLoader,
	}
	bs.Register(be)
}
