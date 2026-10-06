//go:build darwin

// Canonical port of src/fileaccess/fa_spotlight.c — Spotlight (MDQuery)
// search backend for macOS. CONFIG_SPOTLIGHT is only enabled by
// configure.osx; this file is gated by the darwin build tag.
package core

/*
#cgo LDFLAGS: -framework CoreServices
#include <stdlib.h>
#include <string.h>
#include <CoreServices/CoreServices.h>

// mdQueryRun builds the query CFString, creates the MDQuery, sets the
// search scope to the given CFArray of directory CFStrings, executes it
// synchronously and stops it (C: spotlight_searcher query setup,
// fa_spotlight.c:126-146). Returns NULL on failure.
static MDQueryRef mdQueryRun(const char *query, CFArrayRef scope) {
	CFStringRef qs = CFStringCreateWithCString(NULL, query,
		kCFStringEncodingUTF8);
	if(qs == NULL)
		return NULL;
	MDQueryRef q = MDQueryCreate(NULL, qs, NULL, NULL);
	CFRelease(qs);
	if(q == NULL)
		return NULL;
	MDQuerySetSearchScope(q, scope, 0);
	MDQueryExecute(q, kMDQuerySynchronous);
	MDQueryStop(q);
	return q;
}

// mdQueryPathAt — C: MDQueryGetResultAtIndex + MDItemCopyAttribute(item,
// kMDItemPath) + CFStringGetCString (fa_spotlight.c:166-177). Returns a
// malloc'ed UTF-8 path the caller must free(), or NULL.
static char *mdQueryPathAt(MDQueryRef q, CFIndex idx) {
	MDItemRef item = (MDItemRef)MDQueryGetResultAtIndex(q, idx);
	if(item == NULL)
		return NULL;
	CFStringRef pathRef = (CFStringRef)MDItemCopyAttribute(item,
		kMDItemPath);
	if(pathRef == NULL)
		return NULL;
	CFIndex len = CFStringGetMaximumSizeForEncoding(
		CFStringGetLength(pathRef), kCFStringEncodingUTF8) + 1;
	char *path = malloc(len);
	CFStringGetCString(pathRef, path, len, kCFStringEncodingUTF8);
	CFRelease(pathRef);
	return path;
}

// scopeAppend — C: CFArrayAppendValue(directories, path)
static void scopeAppend(CFMutableArrayRef dirs, const char *path) {
	CFStringRef p = CFStringCreateWithCString(NULL, path,
		kCFStringEncodingUTF8);
	CFArrayAppendValue(dirs, p);
	CFRelease(p);
}

static CFMutableArrayRef scopeCreate(void) {
	return CFArrayCreateMutable(NULL, 0, &kCFTypeArrayCallBacks);
}
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	core2 "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// spotlightEnabled — C: static int spotlight_enabled (fa_spotlight.c:40)
var spotlightEnabled int

// spotlightScopeDirectories — C: spotlight_scope_directories
// (fa_spotlight.c:63-83). Collects every service with a file:// URL
// into a CFArray of CFStrings. Caller must CFRelease the array.
func (bs *BackendSystem) spotlightScopeDirectories() C.CFMutableArrayRef {
	directories := C.scopeCreate()

	// C: hts_mutex_lock(&service_mutex); LIST_FOREACH(s, &services, s_link)
	if bs.serviceSystem != nil {
		for _, svc := range bs.serviceSystem.GetAllServices() {
			url := service.ServiceGetURL(svc)
			if !strings.HasPrefix(url, "file://") {
				continue
			}
			p := C.CString(url[len("file://"):])
			C.scopeAppend(directories, p)
			C.free(unsafe.Pointer(p))
		}
	}
	return directories
}

// spotlightSearcher — C: spotlight_searcher (fa_spotlight.c:102-228).
// Runs the MDQuery synchronously and feeds results into the search model.
func (bs *BackendSystem) spotlightSearcher(fas *faSearchT) {
	pm := bs.propManager

	// C: snprintf(iconpath, ..., "%s/res/fileaccess/fs_icon.png",
	//   app_dataroot())
	iconpath := app.AppDataRoot() + "/res/fileaccess/fs_icon.png"

	// C: fas->fas_pc = prop_courier_create_passive()
	fas.pc = prop.NewCourier("spotlight")
	// C: fas->fas_sub = prop_subscribe(PROP_SUB_TRACK_DESTROY,
	//   PROP_TAG_CALLBACK, spotlight_search_nodesub, fas,
	//   PROP_TAG_ROOT, fas->fas_nodes, PROP_TAG_COURIER, fas->fas_pc, NULL)
	// spotlight_search_nodesub is the same DESTROYED→fas_run=0 body as
	// fa_search_nodesub.
	fas.sub = pm.SubscribeWithCourier(fas.nodes, fas.pc,
		faSearchNodesub, fas, prop.SubFlagTrackDestroy)

	// C: CFStringAppendCString query build (fa_spotlight.c:118-137)
	qstr := fmt.Sprintf(
		"kMDItemFSName = '*%[1]s*'cd || "+
			"kMDItemAlbum = '*%[1]s*'cd || "+
			"kMDItemAuthors = '*%[1]s*'cd || "+
			"kMDItemTitle = '*%[1]s*'cd", fas.query)

	// C: MDQueryCreate + MDQuerySetSearchScope + MDQueryExecute(sync) +
	//    MDQueryStop
	directories := bs.spotlightScopeDirectories()
	cq := C.CString(qstr)
	query := C.mdQueryRun(cq, C.CFArrayRef(directories))
	C.free(unsafe.Pointer(cq))
	C.CFRelease(C.CFTypeRef(directories))
	if query == 0 {
		fas.destroy()
		return
	}

	queryCount := C.MDQueryGetResultCount(query)
	var queryIndex C.CFIndex

	var entries [2]*prop.Prop
	var nodes [2]*prop.Prop

	for {
		// C: prop_courier_poll(fas->fas_pc); if(!fas->fas_run) break;
		fas.pc.Poll()

		if !fas.run {
			break
		}

		if queryIndex >= queryCount {
			break
		}

		// C: item = MDQueryGetResultAtIndex(query, query_index++);
		//    pathRef = MDItemCopyAttribute(item, kMDItemPath); ...
		cpath := C.mdQueryPathAt(query, queryIndex)
		queryIndex++
		if cpath == nil {
			continue
		}
		path := C.GoString(cpath)
		C.free(unsafe.Pointer(cpath))

		// C: metadata = prop_create_root("metadata")
		meta := pm.CreateRootEx("metadata", false)

		// C: md = fa_probe_metadata(path, NULL, 0, NULL, NULL)
		md, _ := scanner.FAProbeMetadata(bs.fileAccessManager,
			"file://"+path, "", nil)
		if md == nil {
			continue
		}

		// C: switch(md->md_contenttype) AUDIO→0, VIDEO/DVD→1, else skip
		var t int
		ctype := md.ContentType
		switch ctype {
		case metadata.ContentAudio:
			t = 0
		case metadata.ContentVideo, metadata.ContentDVD:
			t = 1
		default:
			md.Destroy()
			continue
		}
		md.Destroy() // C: metadata_destroy(md)

		// C: if(nodes[t] == NULL) search_class_create(...)
		if nodes[t] == nil {
			classTitle := "Local audio files"
			if t == 1 {
				classTitle = "Local video files"
			}
			var err error
			nodes[t], entries[t], err = bs.searchClassCreate(
				fas.nodes, classTitle, iconpath)
			if err != nil {
				// C: { free(path); break; }
				break
			}
		}

		// C: prop_add_int(entries[t], 1)
		pm.SetIntEx(entries[t], nil, entries[t].GetInt()+1)

		// C: if((type = content2type(ctype)) == NULL) continue;
		typ := metadata.Content2Type(ctype)

		p := pm.CreateRootEx("", false)

		// C: if(prop_set_parent(metadata, p)) prop_destroy(metadata)
		if pm.SetParentEx(meta, p, nil, "") != 0 {
			pm.Destroy(meta)
		}

		// C: prop_set_string(prop_create(p, "url"), path); free(path)
		pm.SetStringEx(pm.CreateEx(p, "url", nil, false, false), nil,
			"file://"+path, prop.StringUTF8)
		pm.SetStringEx(pm.CreateEx(p, "type", nil, false, false), nil,
			typ, prop.StringUTF8)

		// C: if(prop_set_parent(p, nodes[t])) { prop_destroy(p); break; }
		if pm.SetParentEx(p, nodes[t], nil, "") != 0 {
			pm.Destroy(p)
			break
		}
	}

	// C: for(i...) { prop_ref_dec(nodes[i]); prop_ref_dec(entries[i]); }
	for i := 0; i < 2; i++ {
		if nodes[i] != nil {
			nodes[i].Release()
		}
		if entries[i] != nil {
			entries[i].Release()
		}
	}

	// C: CFRelease(query)
	C.CFRelease(C.CFTypeRef(query))

	// C: TRACE(TRACE_DEBUG, "FA", "Searcher: %s: Done", fas->fas_query)
	bs.traceSystem.Trace(trace.TRACE_DEBUG, "FA", "Searcher: %s: Done", fas.query)
	fas.destroy()
}

// spotlightSearch — C: spotlight_search (fa_spotlight.c:234-248)
func (bs *BackendSystem) spotlightSearch(model *prop.Prop, query string,
	loading *prop.Prop) {
	// C: if(!spotlight_enabled) return;
	if spotlightEnabled == 0 {
		return
	}

	// C: fa_search_t *fas = calloc(1, sizeof(*fas))
	fas := &faSearchT{}
	// C: fas->fas_query = s = strdup(query)
	fas.query = query
	fas.run = true
	// C: fas->fas_nodes = prop_ref_inc(prop_create(model, "nodes"))
	fas.nodes = bs.propManager.CreateEx(model, "nodes", nil, false, false)
	if fas.nodes == nil {
		if loading != nil {
			loading.SetInt(0)
		}
		return
	}
	fas.nodes.Retain()
	fas.loading = loading

	// C: hts_thread_create_detached("spotlight search", spotlight_searcher,
	//   fas, THREAD_PRIO_MODEL)
	go bs.spotlightSearcher(fas)
}

// spotlightSetup — C: spotlight_init (fa_spotlight.c:253-266)
func (bs *BackendSystem) spotlightSetup() error {
	if bs.settingsMgr == nil {
		return nil
	}
	// C: prop_t *s = search_get_settings()
	if bs.searchSettings == nil {
		bs.searchSettings = bs.settingsMgr.AddDirCStr(nil, "Search",
			"search", "", "", "settings:search")
	}
	// C: setting_create(SETTING_BOOL, s, SETTINGS_INITIAL_UPDATE,
	//   SETTING_TITLE(_p("Search using spotlight")), SETTING_VALUE(1),
	//   SETTING_WRITE_BOOL(&spotlight_enabled),
	//   SETTING_STORE("spotlight", "enable"), NULL)
	bs.settingsMgr.SettingCreate(core2.SettingBool, bs.searchSettings,
		core2.SettingsInitialUpdate,
		core2.SettingTagTitleCStr, "Search using spotlight",
		core2.SettingTagValue, 1,
		core2.SettingTagWriteInt, &spotlightEnabled,
		core2.SettingTagStore, "spotlight", "enable",
		0)
	return nil
}

// registerSpotlightSearchBackend — C: BE_REGISTER(spotlight)
// (fa_spotlight.c:268-273). be_spotlight provides only be_init +
// be_search; it never handles URLs.
func (bs *BackendSystem) registerSpotlightSearchBackend() {
	backend := &Backend{
		Prefix: "spotlight-search", // Not a URL handler — only be_search
	}
	backend.CanHandle = func(url string) int {
		return 0
	}
	backend.Open = func(page any, url string, sync bool) error {
		return nil
	}
	// C: be_spotlight.be_init = spotlight_init
	backend.Start = func() error {
		return bs.spotlightSetup()
	}
	// C: be_spotlight.be_search = spotlight_search
	backend.Search = func(model any, query string, loading any) {
		modelProp, ok := model.(*prop.Prop)
		if !ok || modelProp == nil {
			return
		}
		loadingProp, _ := loading.(*prop.Prop)
		bs.spotlightSearch(modelProp, query, loadingProp)
	}
	bs.Register(backend)
}
