package core

// Split from backend.go — Fase 4 pure-move refactor.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

func (bs *BackendSystem) CanHandle(url string) *Backend {
	bs.backendsMutex.RLock()
	defer bs.backendsMutex.RUnlock()

	// C: backend_canhandle — score-based selection (highest score wins)
	// C: LIST_FOREACH(be, &backends, be_global_link) — static backends only
	var best *Backend
	bestScore := 0
	for _, backend := range bs.backends {
		if backend.CanHandle != nil {
			if s := backend.CanHandle(url); s > bestScore {
				best = backend
				bestScore = s
			}
		}
	}

	if best != nil {
		return bs.Retain(best)
	}
	return nil
}

// Resolve resolves a backend for a URL
// Resolve resolves a URL to a backend.
// C: backend_resolve (backend.c:457-473) — first tries dynamic backends by
// prefix match (retain, return), then falls back to backend_canhandle.
func (bs *BackendSystem) Resolve(url string) *Backend {
	// C: First try dynamic backends by prefix match
	// LIST_FOREACH(dynamic_backends) if mystrbegins(url, be_prefix) { retain; break; }
	bs.dynamicBackendsMutex.RLock()
	var dynBackend *Backend
	for _, backend := range bs.dynamicBackends {
		if backend.Prefix != "" && strings.HasPrefix(url, backend.Prefix) {
			dynBackend = backend
			break
		}
	}
	bs.dynamicBackendsMutex.RUnlock()

	if dynBackend != nil {
		// C: atomic_inc(&be->be_refcount)
		bs.Retain(dynBackend)
		return dynBackend
	}

	// C: return backend_canhandle(url);
	return bs.CanHandle(url)
}

// Probe probes a URL to see if it can be handled
// Probe probes a URL to see if it can be handled
// C: backend_probe (backend.c:501-519) — defaults timeout_ms to 5000,
// writes "No handler for URL" to errbuf if no backend.
func (bs *BackendSystem) Probe(url string, timeoutMs int) (ProbeResult, error) {
	// C: if(timeout_ms <= 0) timeout_ms = 5000;
	if timeoutMs <= 0 {
		timeoutMs = 5000
	}

	backend := bs.CanHandle(url)
	if backend == nil {
		// C: snprintf(errbuf, errlen, "No handler for URL")
		return ProbeNoHandler, errors.New("No handler for URL")
	}
	defer bs.Release(backend)

	if backend.Probe != nil {
		return backend.Probe(url, timeoutMs)
	}
	return ProbeOK, nil
}

// Register registers a backend
// C: backend_register (backend.c:79,121-124) — only inserts into the
// static backends list with refcount=1. Does NOT call be_init; that is
// done separately by backend_init() iterating all backends.
func (bs *BackendSystem) ResolveItem(url string, item any) error {
	// C: backend_resolve_item (backend.c:643-650) uses backend_canhandle,
	// NOT backend_resolve. backend_canhandle is score-based selection.
	backend := bs.CanHandle(url)
	if backend == nil || backend.ResolveItem == nil {
		// C: return -1
		return fmt.Errorf("no handler for URL: %s", url)
	}
	defer bs.Release(backend)

	return backend.ResolveItem(url, item)
}

// Normalize normalizes a URL
// C: backend_normalize (backend.c:621-636) — be_normalize returns 0 = success
// (use normalized tmp), non-zero = failure (keep original url).
// C uses backend_canhandle(url), NOT backend_resolve(url).
func (bs *BackendSystem) Normalize(url string) string {
	backend := bs.CanHandle(url)
	if backend == nil {
		return url
	}
	defer bs.Release(backend)

	if backend.Normalize != nil {
		dst := make([]byte, len(url)*2+1024)
		// C: if(!be->be_normalize(url, tmp, sizeof)) { use tmp }
		// be_normalize returns 0 = success, non-zero = failure
		if ret := backend.Normalize(url, dst); ret == 0 {
			// Success — use normalized URL
			n := bytes.IndexByte(dst, 0)
			if n < 0 {
				n = len(dst)
			}
			return string(dst[:n])
		}
	}
	return url
}

// Search performs a search using all registered backends.
// Reproduces C's backend_search (src/backend/backend.c:657):
//
//	LIST_FOREACH(be, &backends, be_global_link)
//	  if(be->be_search != NULL)
//	    be->be_search(model, url, loading);
//
// Each backend that supports search is called, allowing multiple
// backends (fileaccess, plugins, ecmascript, etc.) to contribute results.
