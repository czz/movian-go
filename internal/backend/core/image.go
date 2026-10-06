package core

// Split from backend.go — Fase 4 pure-move refactor.

import (
	"encoding/json"
	"errors"
	"github.com/czz/movian-go/internal/gconf"
	imagepkg "github.com/czz/movian-go/internal/image"
	"slices"
	"strings"
)

func (bs *BackendSystem) Imageloader(url string, imageMeta any,
	cacheControl *int, cancellable any, backend *Backend) (any, error) {

	var err error

	// C: if(im0->im_req_width < -1 || im0->im_req_height < -1) (backend.c:263-266)
	// Dimension validation — return error for invalid dimensions
	if im, ok := imageMeta.(*ImageMeta); ok {
		if im.ReqWidth < -1 || im.ReqHeight < -1 {
			return nil, errors.New("Invalid dimensions")
		}
	}

	// C: if(!strncmp(url, "thumb://", 8)) { url += 8; im.im_want_thumb = 1; } (backend.c:270-273)
	wantThumb := false
	if strings.HasPrefix(url, "thumb://") {
		url = url[8:]
		wantThumb = true
	}

	// C: if(!strncmp(url, "imageset:", 9)) { ... } (backend.c:275-333)
	// Parse imageset JSON and select best image from set
	if strings.HasPrefix(url, "imageset:") {
		bestURL := selectBestFromImageSet(url[9:], imageMeta)
		if bestURL == "" {
			return nil, errors.New("No image in set")
		}
		url = bestURL
	}

	// C: im.im_margin = MAX(im.im_shadow * 2, im.im_margin) (backend.c:338)
	if im, ok := imageMeta.(*ImageMeta); ok {
		shadowMargin := im.Shadow * 2
		if shadowMargin > im.Margin {
			im.Margin = shadowMargin
		}
	}

	bs.imageloaderMu.Lock()

	// Find existing loading image
	var li *LoadingImage
	for _, li2 := range bs.loadingImages {
		if li2.url == url {
			li = li2
			break
		}
	}

	if li == nil {
		// Create new loading image
		li = &LoadingImage{
			url:     url,
			waiters: 1,
			done:    false,
		}
		bs.loadingImages = append(bs.loadingImages, li)
	} else {
		// Image already loading or loaded
		if li.waiters == 0 {
			// Remove from cache
			bs.numCachedImages--
			for i, ci := range bs.cachedImages {
				if ci == li {
					bs.cachedImages = slices.Delete(bs.cachedImages, i, i+1)
					break
				}
			}
		}

		li.waiters++

		// C: hts_cond_wait(&imageloader_cond, &imageloader_mutex)
		// sync.Cond.Wait atomically unlocks and blocks, then re-locks on wake.
		for !li.done {
			bs.imageDoneCond.Wait()
		}

		// Check cache control
		// C: if(cacheControl == BYPASS_CACHE) li->li_image = NULL (backend.c:370)
		// BYPASS_CACHE in C is a sentinel pointer (int *)-1, not value 1.
		// We use a dedicated sentinel value here for parity.
		if cacheControl != nil && isBypassCache(cacheControl) {
			li.image = nil
		} else {
			// C: if(li->li_image != NULL && !im_want_thumb) {
			//       img = image_retain(li->li_image);  // retain cached raw image
			//       if(!im.im_no_decoding) img = image_decode(img, &im, ...);
			//       goto done;                          // return decoded image
			//     } (backend.c:415-420, 393-401)
			// Return cached image if available (and not wanting thumb)
			if li.image != nil && !wantThumb {
				img := retainImage(li.image)
				bs.imageloaderMu.Unlock()
				// C: image_decode(img, &im, errbuf, errlen) — post-decode
				if im, ok := imageMeta.(*ImageMeta); ok && img != nil && !im.NoDecoding {
					return decodeImage(img, im, bs.Gconf())
				}
				return img, nil
			}
		}
	}

	li.done = false
	bs.imageloaderMu.Unlock()

	var img any

	// C: if(be != NULL) { img = be->be_imageloader(...); } (backend.c:382-391)
	// Load image via backend
	if backend != nil && backend.Imageloader != nil {
		img, err = backend.Imageloader(url, imageMeta, cacheControl, cancellable, backend)
		// C: if(cancellable_is_cancelled(c)) { ... img = NULL; goto done; } (backend.c:385-391)
		if isCancelled(cancellable) {
			err = errors.New("Cancelled")
			// C: if(img != NOT_MODIFIED) image_release(img);
			if img != nil && img != NotModifiedImage {
				// Decrement refcount on the image returned by be_imageloader.
				releaseImage(img)
			}
			img = nil
			goto done
		}
	}

	// C: if(img == NULL) { be = backend_canhandle(url); ... } (backend.c:394-403)
	// Fallback to finding backend by URL
	if img == nil {
		backend = bs.CanHandle(url)
		if backend == nil || backend.Imageloader == nil {
			// C: snprintf(errbuf, errlen, "No backend for URL")
			err = errors.New("No backend for URL")
		} else {
			defer bs.Release(backend)
			img, err = backend.Imageloader(url, imageMeta, cacheControl, cancellable, backend)
		}
	}

	// C: if(cancellable_is_cancelled(c)) { ... img = NULL; } (backend.c:406-411)
	if isCancelled(cancellable) {
		err = errors.New("Cancelled")
		// C: if(img != NOT_MODIFIED) image_release(img);
		if img != nil && img != NotModifiedImage {
			// Decrement refcount on the image returned by be_imageloader.
			releaseImage(img)
		}
		img = nil
	}

	// C: if(img != NULL && img != NOT_MODIFIED) { ... } (backend.c:413-422)
	// image_retain for caching + image_decode post-processing
	if img != nil && img != NotModifiedImage {
		// C: if(!(img->im_flags & IMAGE_ADAPTED) && li->li_image == NULL)
		//     li->li_image = image_retain(img); (backend.c:415-416)
		// Cache the raw (pre-decode) image for future waiters.
		if li.image == nil {
			if retained := retainImage(img); retained != nil {
				li.image = retained
			}
		}

		// C: if(!im.im_no_decoding) { img = image_decode(img, &im, errbuf, errlen); } (backend.c:418-420)
		if im, ok := imageMeta.(*ImageMeta); ok {
			if !im.NoDecoding {
				var derr error
				img, derr = decodeImage(img, im, bs.Gconf())
				if derr != nil {
					err = derr
				}
			}
		}
	}

done:
	bs.imageloaderMu.Lock()
	defer bs.imageloaderMu.Unlock()

	li.done = true
	li.waiters--
	// C: li->li_image is the pre-decode cached image (set at backend.c:415-416).
	// C: img is the post-decode image (set at backend.c:419).
	// Go: li.image was set to the pre-decode retained image above.
	// Do NOT overwrite li.image with the post-decode img — the cache
	// holds the pre-decode image so future waiters can decode it.
	// Only set li.image if it wasn't set (e.g. error path or NOT_MODIFIED).
	if li.image == nil && img != nil && img != NotModifiedImage {
		li.image = img
	}

	if li.waiters == 0 {
		if li.image != nil {
			// C: prune_image_cache() + num_cached_images++ + TAILQ_INSERT_TAIL
			// Add to cache
			bs.cachedImages = append(bs.cachedImages, li)
			bs.numCachedImages++
			bs.pruneImageCache()
		} else {
			// C: LIST_REMOVE(li, li_link) + free(li)
			// Remove from loadingImages since there are no waiters and no image
			for i, li2 := range bs.loadingImages {
				if li2 == li {
					bs.loadingImages = slices.Delete(bs.loadingImages, i, i+1)
					break
				}
			}
		}
	} else {
		// C: hts_cond_broadcast(&imageloader_cond) — wake up ALL waiters
		// sync.Cond.Broadcast wakes all goroutines waiting on the cond.
		bs.imageDoneCond.Broadcast()
	}

	return img, err
}

// isBypassCache checks if cacheControl points to the BYPASS_CACHE sentinel.
// C: #define BYPASS_CACHE ((int *)-1) — sentinel pointer compared by identity.
// Go: app.BypassCache = -1; we treat *cacheControl == -1 as bypass.
func isBypassCache(cacheControl *int) bool {
	return cacheControl != nil && *cacheControl == -1
}

// isCancelled checks if a cancellable object is cancelled.
// Supports *async.AsyncOperation and nil (never cancelled).
func isCancelled(cancellable any) bool {
	if cancellable == nil {
		return false
	}
	type cancellableIface interface {
		IsCancelled() bool
	}
	if c, ok := cancellable.(cancellableIface); ok {
		return c.IsCancelled()
	}
	return false
}

// retainImage retains an image for caching, matching C's image_retain.
// C: image_retain(img) increments refcount and returns img.
// Go: *imagepkg.Image has Retain() method.
func retainImage(img any) any {
	if img == nil {
		return nil
	}
	if i, ok := img.(*imagepkg.Image); ok {
		if i == nil {
			return nil
		}
		return i.Retain()
	}
	// Non-image types (e.g. test stubs) — return as-is
	return img
}

// releaseImage releases an image, matching C's image_release.
// C: image_release(img) decrements refcount; if 0, frees the image.
// Go: *imagepkg.Image has Release() method.
func releaseImage(img any) {
	if img == nil {
		return
	}
	if i, ok := img.(*imagepkg.Image); ok {
		if i == nil {
			return
		}
		i.Release()
	}
	// Non-image types (e.g. test stubs) — nothing to release
}

// decodeImage decodes an image, matching C's image_decode.
// C: image_decode(img, &im, errbuf, errlen) (image.c:305)
// Go: imagepkg.Decode(img, meta)
func decodeImage(img any, meta *ImageMeta, g *gconf.T) (any, error) {
	if img == nil {
		return nil, nil
	}
	i, ok := img.(*imagepkg.Image)
	if !ok {
		return img, nil
	}
	if i == nil {
		return nil, nil
	}
	// Convert backend.ImageMeta to imagepkg.ImageMeta
	im := &imagepkg.ImageMeta{
		ReqAspect:            meta.ReqAspect,
		ReqWidth:             meta.ReqWidth,
		ReqHeight:            meta.ReqHeight,
		MaxWidth:             meta.MaxWidth,
		MaxHeight:            meta.MaxHeight,
		CanMono:              meta.CanMono,
		NoDecoding:           meta.NoDecoding,
		Bit32Swizzle:         meta.Bit32Swizzle,
		WantThumb:            meta.WantThumb,
		IntensityAnalysis:    meta.IntensityAnalysis,
		PrimaryColorAnalysis: meta.PrimaryColorAnalysis,
		ForceLocalLoad:       meta.ForceLocal,
		CornerSelection:      meta.CornerSelection,
		CornerRadius:         meta.CornerRadius,
		Shadow:               meta.Shadow,
		Margin:               meta.Margin,
		Opaque:               meta.Opaque,
		Incremental:          meta.Incremental,
	}
	r, err := imagepkg.Decode(i, im, g)
	if r != nil {
		return r, nil
	}
	return nil, err
}

// selectBestFromImageSet parses an imageset JSON string and selects the
// best image URL based on requested dimensions.
// C: backend_imageloader imageset handling (backend.c:275-333)
func selectBestFromImageSet(jsonStr string, imageMeta any) string {
	// Parse JSON: [{"width":W,"height":H,"url":"U"}, ...]
	type imgEntry struct {
		Width  int    `json:"width"`
		Height int    `json:"height"`
		URL    string `json:"url"`
	}
	var entries []imgEntry
	if err := json.Unmarshal([]byte(jsonStr), &entries); err != nil {
		return ""
	}

	// C: defaults to 10000 if not specified
	for i := range entries {
		if entries[i].Width == 0 {
			entries[i].Width = 10000
		}
		if entries[i].Height == 0 {
			entries[i].Height = 10000
		}
	}

	var reqW, reqH int = -1, -1
	if im, ok := imageMeta.(*ImageMeta); ok {
		reqW = im.ReqWidth
		reqH = im.ReqHeight
	}

	bestURL := ""
	bestW, bestH := -1, -1

	for _, e := range entries {
		if e.URL == "" {
			continue
		}
		better := false
		if bestURL == "" {
			better = true
		} else if reqW != -1 {
			if e.Width >= reqW && (e.Width < bestW || bestW < reqW) {
				better = true
			} else if e.Width < reqW && e.Width > bestW {
				better = true
			}
		} else if reqH != -1 {
			if e.Height >= reqH && (e.Height < bestH || bestH < reqH) {
				better = true
			} else if e.Height < reqH && e.Height > bestH {
				better = true
			}
		} else {
			if e.Width > bestW {
				better = true
			} else if e.Height > bestH {
				better = true
			}
		}
		if better {
			bestURL = e.URL
			bestW = e.Width
			bestH = e.Height
		}
	}

	return bestURL
}

// CanHandle finds a backend that can handle the given URL
// C: backend_canhandle (backend.c:479-494) — iterates ONLY the static
// backends list (not dynamic backends), keeps the one with the highest
// score (int > 0), not first-match. Dynamic backends are handled by
// backend_resolve (used in Open, PlayAudio).
func (bs *BackendSystem) pruneImageCacheItem(li *LoadingImage) {
	bs.numCachedImages--
	for i, ci := range bs.cachedImages {
		if ci == li {
			bs.cachedImages = slices.Delete(bs.cachedImages, i, i+1)
			break
		}
	}
	// Remove from loadingImages
	for i, li2 := range bs.loadingImages {
		if li2 == li {
			bs.loadingImages = slices.Delete(bs.loadingImages, i, i+1)
			break
		}
	}
	// C: image_release(li->li_image) — decrement refcount on cached image
	releaseImage(li.image)
}

// pruneImageCache prunes the image cache to keep at most 3 items
func (bs *BackendSystem) pruneImageCache() {
	for bs.numCachedImages > 3 {
		if len(bs.cachedImages) == 0 {
			break
		}
		li := bs.cachedImages[0]
		if li.waiters != 0 || !li.done || li.image == nil {
			break
		}
		bs.pruneImageCacheItem(li)
	}
}

// registerPageBackend registers the page backend for handling page: URLs
