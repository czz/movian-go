package core

// Split from backend.go — Fase 4 pure-move refactor.

import (
	"bytes"
	"fmt"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	propcore "github.com/czz/movian-go/internal/prop"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (bs *BackendSystem) Open(page any, url string, sync bool) error {
	// Phase 1: Dynamic backends with be_open2
	// C: LIST_FOREACH(dynamic_backends) if mystrbegins(url, be_prefix) { retain; break; }
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
		bs.Retain(dynBackend)
		defer bs.Release(dynBackend)

		// C: prop_set(page, "url", PROP_SET_STRING, url) (backend.c:580)
		bs.setPageURL(page, url)

		// C: if(be->be_open2 == NULL) err = 1; else be->be_open2(...) (backend.c:582-587)
		// C: err = 0 when be_open2 != NULL (return value of be_open2 is ignored)
		if dynBackend.Open2 == nil {
			return fmt.Errorf("dynamic backend has no Open2: %s", url)
		}
		// C: be->be_open2(page, url, sync, be->be_opaque); err = 0;
		// The return value of be_open2 is ignored — success is assumed.
		dynBackend.Open2(page, url, sync, dynBackend.Opaque)
		return nil
	}

	// Phase 2: BACKEND_OPEN_CHECKS_URI backends
	// C: LIST_FOREACH(backends) if(be_flags & BACKEND_OPEN_CHECKS_URI) { if be_open(...) continue; return 0; }
	bs.backendsMutex.RLock()
	for _, backend := range bs.backends {
		if backend.Flags&BackendOpenChecksURI != 0 && backend.Open != nil {
			// C: be_open returns non-zero = continue, zero = success
			err := backend.Open(page, url, sync)
			if err == nil {
				bs.backendsMutex.RUnlock()
				return nil
			}
		}
	}
	bs.backendsMutex.RUnlock()

	// Phase 3: backend_canhandle + normalize + be_open
	// C: be = backend_canhandle(url); if be_normalize && !be_normalize() url = urlbuf;
	//     prop_set(page, "url", url); be->be_open(page, url, sync);
	backend := bs.CanHandle(url)
	if backend == nil {
		return fmt.Errorf("no backend for URL: %s", url)
	}
	defer bs.Release(backend)

	// C: if(be->be_normalize != NULL && !be->be_normalize(url, urlbuf, sizeof)) url = urlbuf;
	// C: be_normalize returns 0 = success (use urlbuf), non-zero = failure (keep url)
	finalURL := url
	if backend.Normalize != nil {
		dst := make([]byte, len(url)*2+1024)
		if ret := backend.Normalize(url, dst); ret == 0 {
			finalURL = string(dst[:bytes.IndexByte(dst, 0)])
			if finalURL == "" {
				finalURL = url
			}
		}
	}

	// C: prop_set(page, "url", PROP_SET_STRING, url) (backend.c:607)
	bs.setPageURL(page, finalURL)

	// C: be->be_open(page, url, sync); return 0; (backend.c:609-610)
	// C ignores be_open's return value in Phase 3 — always returns 0 (success).
	if backend.Open != nil {
		backend.Open(page, finalURL, sync)
	}
	return nil
}

// setPageURL sets the "url" child prop on a page prop, matching C's
// prop_set(page, "url", PROP_SET_STRING, url).
func (bs *BackendSystem) setPageURL(page any, url string) {
	if pageProp, ok := page.(*propcore.Prop); ok && pageProp != nil {
		urlChild := pageProp.FindChild("url")
		if urlChild == nil {
			urlChild = bs.propManager.CreateEx(pageProp, "url", nil, false, false)
		}
		urlChild.SetString(url)
	}
}

// checkForDVD checks if a URL points to a DVD directory or ISO
// Returns a dvd: URL if detected, empty string otherwise
func (bs *BackendSystem) checkForDVD(url string) string {
	// Check for DVD directory structure
	path := strings.TrimPrefix(url, "file://")
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		// Check for VIDEO_TS directory
		videoTSPath := filepath.Join(path, "VIDEO_TS")
		videoTSInfo, err := os.Stat(videoTSPath)
		if err == nil && videoTSInfo.IsDir() {
			// Check for VIDEO_TS.IFO file
			ifoPath := filepath.Join(videoTSPath, "VIDEO_TS.IFO")
			if _, err := os.Stat(ifoPath); err == nil {
				// DVD directory detected
				return "dvd:" + path
			}
		}
	}

	// Check for ISO file
	if !strings.HasSuffix(strings.ToLower(path), ".iso") {
		return ""
	}

	// Try to open and check ISO structure
	fh, err := fileaccesscore.Open(bs.fileAccessManager, url)
	if err != nil || fh == nil {
		return ""
	}
	defer fh.Close()

	// Check if it's a DVD ISO using the function from dvd_backend
	// We need to access it through the backend package
	// For now, do a basic check
	if _, err := fileaccesscore.Seek(fh, 32768, io.SeekStart); err != nil {
		return ""
	}

	buf := make([]byte, 2048)
	n, err := fh.Read(buf)
	if err != nil || n < 6 {
		return ""
	}

	// Check for ISO 9660 signature
	if string(buf[1:6]) == "CD001" {
		// Could be DVD ISO - let DVD backend verify further
		return "dvd:" + path
	}

	return ""
}

// checkForHLS checks if a URL points to an HLS playlist
// Returns the same URL if HLS detected, empty string otherwise
func (bs *BackendSystem) checkForHLS(url string) string {
	// Try to open and check for HLS signature
	fh, err := fileaccesscore.Open(bs.fileAccessManager, url)
	if err != nil || fh == nil {
		return ""
	}
	defer fh.Close()

	// Seek to beginning
	if _, err := fileaccesscore.Seek(fh, 0, io.SeekStart); err != nil {
		return ""
	}

	// Read first 1023 bytes to check for HLS signature
	buf := make([]byte, 1023)
	n, err := fh.Read(buf)
	if err != nil || n < 7 {
		return ""
	}

	content := string(buf[:n])

	// Check for #EXTM3U
	if !strings.HasPrefix(content, "#EXTM3U") {
		return ""
	}

	// Check for HLS-specific tags (#EXT-X-STREAM-INF or #EXTINF)
	if strings.Contains(content, "#EXT-X-STREAM-INF") || strings.Contains(content, "#EXTINF") {
		return url
	}

	return ""
}

// PlayVideo plays a video using a backend
// C: backend_play_video (backend.c:149-162) — uses backend_canhandle,
// returns "No backend for URL" as the error if no handler, returns NULL.
func (bs *BackendSystem) PageOpen(root any, url string, sync bool) error {
	return bs.Open(root, url, sync)
}

// pruneImageCacheItem removes a single item from the image cache
// C: prune_image_cache_item (backend.c:226-233) — releases the cached image
// and frees the loading_image_t.
func (bs *BackendSystem) backendPageOpen(propRoot *propcore.Prop, url string) {
	// C: prop_t *src = prop_create(root, "model");
	modelProp := bs.propManager.CreateEx(propRoot, "model", nil, false, false)

	// C: prop_t *metadata = prop_create(src, "metadata");
	metadataProp := bs.propManager.CreateEx(modelProp, "metadata", nil, false, false)

	// C: char *cap = mystrdupa(url0 + strlen("page:"));
	if len(url) > 5 {
		pageName := url[5:]

		// C: prop_set_string(prop_create(src, "type"), cap);
		typeProp := bs.propManager.CreateEx(modelProp, "type", nil, false, false)
		bs.propManager.SetStringEx(typeProp, nil, pageName, propcore.StringUTF8)

		// C: cap[0] = toupper((int)cap[0]);
		//     prop_set_string(prop_create(metadata, "title"), cap);
		title := pageName
		if len(title) > 0 && title[0] >= 'a' && title[0] <= 'z' {
			title = string(title[0]-32) + title[1:]
		}
		titleProp := bs.propManager.CreateEx(metadataProp, "title", nil, false, false)
		bs.propManager.SetStringEx(titleProp, nil, title, propcore.StringUTF8)

		// C: backend_page_open does NOT set loading or link services here.
		// Those are handled by the navigator and the home view respectively.
	}
}

// registerUpgradeBackend — C: BE_REGISTER(upgrade) (upgrade.c:1384-1391).
// be_upgrade.canhandle matches exactly "showtime:upgrade"; be_open calls
// usage_page_open + backend_page_open("page:upgrade") + upgrade_refresh,
// then sets directClose.
