package core

// Split from backend.go — Fase 4 pure-move refactor.

import (
	"errors"
)

func (bs *BackendSystem) PlayVideo(url string, mediaPipe any,
	videoQueue any, vsourceList any, va *VideoArgs) (any, error) {

	// C: backend_canhandle(url) — score-based selection only
	backend := bs.CanHandle(url)
	if backend == nil {
		// C: snprintf(errbuf, errlen, "No backend for URL")
		return nil, errors.New("No backend for URL")
	}
	defer bs.Release(backend)

	if backend.PlayVideo != nil {
		return backend.PlayVideo(url, mediaPipe, videoQueue, vsourceList, va)
	}
	return nil, nil
}

// PlayAudio plays audio using a backend
// C: backend_play_audio (backend.c:165-183) — uses backend_resolve (dynamic prefix + canhandle),
// plays, then releases. Passes be_opaque to the backend.
func (bs *BackendSystem) PlayAudio(url string, mediaPipe any, paused bool,
	mimetype string) (any, error) {

	// C: backend_resolve(url) — tries dynamic prefix backends first, then canhandle
	backend := bs.Resolve(url)
	if backend == nil {
		return nil, nil
	}
	defer bs.Release(backend)

	if backend.PlayAudio != nil {
		// C: be->be_play_audio(url, mp, errbuf, errlen, paused, mimetype, be->be_opaque)
		return backend.PlayAudio(url, mediaPipe, paused, mimetype, backend.Opaque)
	}
	return nil, nil
}

// Imageloader loads an image using a backend
// Imageloader loads an image from a URL.
// C: backend_imageloader (backend.c:255-430) — handles thumb:// prefix,
// imageset: JSON, dimension validation, caching, and cancellation.
