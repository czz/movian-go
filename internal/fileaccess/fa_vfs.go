package fileaccess

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	propcore "github.com/czz/movian-go/internal/prop"
)

const (
	// VFSPrefix is the prefix for VFS URLs
	VFSPrefix = "vfs://"
)

// VFSMapping represents a virtual filesystem mapping.
// C: vfs_mapping_t — vm_vdir is stored "/" + title in Go (C stores the
// bare title and matches against url+1; the "/" prefix is the Go-internal
// equivalent of C's resolve_mapping2 url++ split).
type VFSMapping struct {
	vdir        string // virtual directory path ("/"-prefixed title)
	vdirlen     int
	url         string // real URL ("" = C's NULL vm_url)
	isFS        bool
	exported    bool
	urlWatch    *pathWatch // C: vm_url_sub
	titleWatch  *pathWatch // C: vm_name_sub
	serviceProp *propcore.Prop
}

// VFSProtocol implements the virtual filesystem protocol
type VFSProtocol struct {
	mappings      []*VFSMapping // C: vfs_exported_mappings (sorted by vdir)
	mappingsMutex sync.Mutex    // C: vfs_mutex
	readmeText    string        // C: READMETXT
	svcWatch      *pathWatch    // C: vfs_init's global.services.all sub
	tagKey        string        // C: prop_tag key (&vfs_exported_mappings)
	fileAccessMgr *FileAccessManager
	pm            *propcore.PropManager
}

// pathWatch is a dynamic child-path subscription: it resolves
// root → path[0] → ... → path[n-1] as children appear and disappear,
// then invokes attach() on the terminal prop. Mirrors C's PROP_TAG_NAME
// name-vector resolution, which re-resolves as the tree mutates.
type pathWatch struct {
	path   []string
	attach func(leaf *propcore.Prop) *propcore.Subscription

	mu      sync.Mutex
	dead    bool
	nodes   []*propcore.Prop         // nodes[i] = child matching path[i]
	watches []*propcore.Subscription // watches[i] = sub on parent for path[i]
	leaf    *propcore.Subscription   // attach() result at the terminal
}

// WatchChildPath — exported counterpart of watchChildPath for
// package-external callers needing C's PROP_TAG_NAME path subscription.
func WatchChildPath(root *propcore.Prop, path []string,
	attach func(*propcore.Prop) *propcore.Subscription) *pathWatch {
	return watchChildPath(root, path, attach)
}

// watchChildPath starts resolving path under root.
func watchChildPath(root *propcore.Prop, path []string,
	attach func(*propcore.Prop) *propcore.Subscription) *pathWatch {
	w := &pathWatch{path: path, attach: attach}
	if root != nil {
		w.arm(root, 0)
	}
	return w
}

// arm subscribes to parent's child events watching for path[depth].
// The initial EventAddChildVector replay resolves any existing child.
// C: PROP_TAG_NAME resolution happens synchronously inside prop_core
// under prop_mutex (prop_resolve_tree/prop_subfind at subscribe and at
// every child add/del) — the watcher subs are the same internal
// machinery, hence PROP_SUB_INTERNAL|PROP_SUB_DONTLOCK.
func (w *pathWatch) arm(parent *propcore.Prop, depth int) {
	name := w.path[depth]
	sub := parent.Subscribe(func(_ any, ev propcore.EventType, args ...any) {
		w.event(depth, name, ev, args...)
	}, nil, propcore.SubFlagTrackDestroy,
		propcore.SubFlagInternal|propcore.SubFlagDontLock)

	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		sub.Unsubscribe()
		return
	}
	for len(w.watches) <= depth {
		w.watches = append(w.watches, nil)
		w.nodes = append(w.nodes, nil)
	}
	w.watches[depth] = sub
	w.mu.Unlock()
}

// event handles child add/del/destroy at one resolution level.
func (w *pathWatch) event(depth int, name string, ev propcore.EventType, args ...any) {
	var resolves []*propcore.Prop
	teardownBelow := false
	teardownAll := false

	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		return
	}
	switch ev {
	case propcore.EventAddChild, propcore.EventAddChildBefore:
		if len(args) > 0 {
			if c, ok := args[0].(*propcore.Prop); ok && c.GetName() == name {
				resolves = append(resolves, c)
			}
		}
	case propcore.EventAddChildVector, propcore.EventAddChildVectorBefore,
		propcore.EventAddChildVectorDirect:
		if len(args) > 0 {
			switch vec := args[0].(type) {
			case []*propcore.Prop:
				for _, c := range vec {
					if c.GetName() == name {
						resolves = append(resolves, c)
					}
				}
			case *propcore.PropVec:
				for i := range vec.Len() {
					if c := vec.Get(i); c != nil && c.GetName() == name {
						resolves = append(resolves, c)
					}
				}
			}
		}
	case propcore.EventDelChild:
		if len(args) > 0 {
			if c, ok := args[0].(*propcore.Prop); ok &&
				depth < len(w.nodes) && w.nodes[depth] == c {
				w.nodes[depth] = nil
				teardownBelow = true
			}
		}
	case propcore.EventDestroyed, propcore.EventSetVoid:
		teardownAll = true
	}
	w.mu.Unlock()

	for _, c := range resolves {
		w.resolve(depth, c)
	}
	if teardownBelow {
		w.teardownBelow(depth)
	}
	if teardownAll {
		w.teardownFrom(depth)
	}
}

// resolve records node c for path[depth] and arms/attaches the next level.
func (w *pathWatch) resolve(depth int, c *propcore.Prop) {
	w.mu.Lock()
	if w.dead || (depth < len(w.nodes) && w.nodes[depth] == c) {
		w.mu.Unlock()
		return
	}
	for len(w.nodes) <= depth {
		w.nodes = append(w.nodes, nil)
		w.watches = append(w.watches, nil)
	}
	w.nodes[depth] = c
	w.mu.Unlock()

	if depth+1 == len(w.path) {
		// Terminal: attach to the leaf prop. The leaf sub's initial
		// value event fires synchronously inside Subscribe.
		sub := w.attach(c)
		w.mu.Lock()
		if w.dead || w.nodes[depth] != c {
			w.mu.Unlock()
			if sub != nil {
				sub.Unsubscribe()
			}
			return
		}
		w.leaf = sub
		w.mu.Unlock()
	} else {
		w.arm(c, depth+1)
	}
}

// teardownBelow drops resolution levels deeper than depth.
func (w *pathWatch) teardownBelow(depth int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := depth + 1; i < len(w.watches); i++ {
		if w.watches[i] != nil {
			w.watches[i].Unsubscribe()
			w.watches[i] = nil
		}
		w.nodes[i] = nil
	}
	if w.leaf != nil {
		w.leaf.Unsubscribe()
		w.leaf = nil
	}
}

// teardownFrom drops resolution levels depth and deeper (source died).
func (w *pathWatch) teardownFrom(depth int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := depth; i < len(w.watches); i++ {
		if w.watches[i] != nil {
			w.watches[i].Unsubscribe()
			w.watches[i] = nil
		}
		w.nodes[i] = nil
	}
	if w.leaf != nil {
		w.leaf.Unsubscribe()
		w.leaf = nil
	}
}

// destroy tears down the whole watch (C: prop_unsubscribe of the sub).
func (w *pathWatch) destroy() {
	w.mu.Lock()
	w.dead = true
	subs := w.watches
	leaf := w.leaf
	w.watches = nil
	w.nodes = nil
	w.leaf = nil
	w.mu.Unlock()

	for _, s := range subs {
		if s != nil {
			s.Unsubscribe()
		}
	}
	if leaf != nil {
		leaf.Unsubscribe()
	}
}

// NewVFSProtocol creates a new VFS protocol
func NewVFSProtocol(fam *FileAccessManager, pm *propcore.PropManager) *VFSProtocol {
	return &VFSProtocol{
		mappings: make([]*VFSMapping, 0),
		readmeText: "This is Movian's exported file system\n\n" +
			"Items on home screen and in 'Local network' that contain files and\n" +
			"folders will appear here\n",
		tagKey:        "vfs_exported_mappings",
		fileAccessMgr: fam,
		pm:            pm,
	}
}

// Start initializes the VFS protocol by subscribing to service changes.
// C: vfs_init — prop_subscribe(PROP_TAG_NAME("global","services","all"),
// PROP_TAG_CALLBACK, vfs_service_callback, PROP_TAG_MUTEX, &vfs_mutex).
// Existing children are delivered by the subscription's initial
// ADD_CHILD_VECTOR replay, so no explicit scan is needed.
func (p *VFSProtocol) Setup() {
	globalProp := p.pm.GetGlobal()
	if globalProp == nil {
		return
	}

	p.svcWatch = watchChildPath(globalProp, []string{"services", "all"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			return leaf.Subscribe(p.serviceCallback, p,
				propcore.SubFlagTrackDestroy,
				propcore.SubMutex{Ptr: &p.mappingsMutex})
		})
}

// serviceCallback handles service property changes.
// C: vfs_service_callback — incremental add/del of vfs_mapping_t nodes.
func (p *VFSProtocol) serviceCallback(opaque any, event propcore.EventType, args ...any) {
	switch event {
	case propcore.EventAddChild, propcore.EventAddChildBefore:
		// C: PROP_ADD_CHILD / PROP_ADD_CHILD_BEFORE → vfs_add_node
		if len(args) > 0 {
			if child, ok := args[0].(*propcore.Prop); ok {
				p.addNode(child)
			}
		}
	case propcore.EventAddChildVector, propcore.EventAddChildVectorBefore,
		propcore.EventAddChildVectorDirect:
		// C: PROP_ADD_CHILD_VECTOR(_BEFORE|_DIRECT) → vfs_add_node each
		if len(args) > 0 {
			if vec, ok := args[0].([]*propcore.Prop); ok {
				for _, child := range vec {
					p.addNode(child)
				}
			}
		}
	case propcore.EventDelChild:
		// C: PROP_DEL_CHILD → vfs_del_node(prop_tag_clear(...))
		if len(args) > 0 {
			if child, ok := args[0].(*propcore.Prop); ok {
				p.delNode(p.pm.TagClear(child, p.tagKey))
			}
		}
	}
}

// addNode creates a mapping for a newly added service prop.
// C: vfs_add_node — tag the prop, then NAMED_ROOT+NAME subscriptions on
// the service's "url" and "title" children (the "node" named-root aliases
// the service prop itself; service_create0 puts url/title directly on it).
// Initial value delivery happens through the leaf subscriptions.
func (p *VFSProtocol) addNode(serviceProp *propcore.Prop) {
	vm := &VFSMapping{serviceProp: serviceProp}
	p.pm.TagSet(serviceProp, p.tagKey, vm)

	vm.urlWatch = watchChildPath(serviceProp, []string{"url"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			return leaf.Subscribe(func(_ any, ev propcore.EventType, args ...any) {
				p.mappingValueSet(vm, ev, args, true)
			}, nil, propcore.SubFlagTrackDestroy,
				propcore.SubMutex{Ptr: &p.mappingsMutex})
		})

	vm.titleWatch = watchChildPath(serviceProp, []string{"title"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			return leaf.Subscribe(func(_ any, ev propcore.EventType, args ...any) {
				p.mappingValueSet(vm, ev, args, false)
			}, nil, propcore.SubFlagTrackDestroy,
				propcore.SubMutex{Ptr: &p.mappingsMutex})
		})
}

// delNode destroys a mapping.
// C: vfs_del_node — unsubscribe both subs, LIST_REMOVE if exported, free.
func (p *VFSProtocol) delNode(vmIface any) {
	vm, ok := vmIface.(*VFSMapping)
	if !ok || vm == nil {
		return
	}

	if vm.urlWatch != nil {
		vm.urlWatch.destroy()
	}
	if vm.titleWatch != nil {
		vm.titleWatch.destroy()
	}

	// C: vfs_mutex already held (caller = vfs_service_callback under
	// PROP_TAG_MUTEX dispatch)
	p.removeMappingLocked(vm)
}

// mappingValueSet dispatches leaf value events to the canonical setters.
// C: PROP_TAG_CALLBACK_RSTR delivers the rstr (NULL on void/destroyed).
func (p *VFSProtocol) mappingValueSet(vm *VFSMapping, ev propcore.EventType, args []any, isURL bool) {
	var str string
	hasStr := false
	switch ev {
	case propcore.EventSetRString, propcore.EventSetCString:
		if len(args) > 0 {
			str, hasStr = args[0].(string)
		}
	case propcore.EventSetVoid, propcore.EventDestroyed:
		// NULL rstr
	default:
		return
	}

	// C: vfs_mutex held by the dispatcher (PROP_TAG_MUTEX)
	if isURL {
		p.mappingSetURLLocked(vm, str, hasStr)
	} else {
		p.mappingSetTitleLocked(vm, str, hasStr)
	}
}

// mappingSetURLLocked — C: vfs_mapping_set_url.
// Strips "search:", checks fa_can_handle, normalizes via fa_normalize
// (keeping the original on failure), and re-evaluates the export.
func (p *VFSProtocol) mappingSetURLLocked(vm *VFSMapping, url string, hasURL bool) {
	if hasURL && strings.HasPrefix(url, "search:") {
		url = url[len("search:"):]
	}

	if hasURL {
		// C: vm->vm_is_fs = fa_can_handle(url, NULL, 0)
		_, _, err := p.fileAccessMgr.FAResolveProto(url)
		vm.isFS = err == nil
		if vm.isFS {
			// C: if(!fa_normalize(url, realurl, sizeof(realurl))) url = realurl;
			if norm, nerr := p.fileAccessMgr.FANormalize(url); nerr == nil {
				url = norm
			}
		} else {
			hasURL = false // C: url = NULL
		}
	} else {
		vm.isFS = false
	}

	// C: mystrset(&vm->vm_url, url)
	if hasURL {
		vm.url = url
	} else {
		vm.url = ""
	}

	p.updateExportLocked(vm)
}

// mappingSetTitleLocked — C: vfs_mapping_set_title.
// rstr_set(&vm->vm_vdir, str); vm_vdirlen updated only when str != NULL.
// Go stores the "/" -prefixed form internally; a NULL title yields "".
func (p *VFSProtocol) mappingSetTitleLocked(vm *VFSMapping, title string, hasTitle bool) {
	if hasTitle {
		vm.vdir = "/" + title
		vm.vdirlen = len(title) + 1
	} else {
		vm.vdir = ""
	}
	p.updateExportLocked(vm)
}

// updateExportLocked — C: update_export. Callers hold mappingsMutex
// (C: all callers run under vfs_mutex).
func (p *VFSProtocol) updateExportLocked(vm *VFSMapping) {
	export := vm.isFS && vm.vdir != ""

	if !export {
		if !vm.exported {
			return
		}
		p.removeMappingLocked(vm)
		return
	}
	p.addMappingLocked(vm)
}

// addMappingLocked — C: update_export's export branch.
// Removes a stale list entry if re-exporting, then inserts sorted
// (C: LIST_INSERT_SORTED with vm_compar = strcmp on vdir).
func (p *VFSProtocol) addMappingLocked(vm *VFSMapping) {
	if vm.exported {
		p.removeMappingLocked(vm)
	}

	if vm.isFS && vm.vdir != "" && vm.vdir[0] != 0 {
		vm.exported = true
		p.mappings = append(p.mappings, vm)
		// C: vm_compar — strcmp(rstr_get(vm_vdir)) ascending
		slices.SortFunc(p.mappings, func(a, b *VFSMapping) int { return cmp.Compare(a.vdir, b.vdir) })
	} else {
		vm.exported = false
	}
}

// removeMappingLocked — C: LIST_REMOVE(vm, vm_link) + vm_exported = 0.
func (p *VFSProtocol) removeMappingLocked(vm *VFSMapping) {
	if vm.exported {
		for i, m := range p.mappings {
			if m == vm {
				p.mappings = slices.Delete(p.mappings, i, i+1)
				break
			}
		}
		vm.exported = false
	}
}

// findMapping finds a mapping for a given path
func (p *VFSProtocol) findMapping(path string) (*VFSMapping, string) {
	p.mappingsMutex.Lock()
	defer p.mappingsMutex.Unlock()

	plen := len(path)

	for _, vm := range p.mappings {
		if plen < vm.vdirlen {
			continue
		}

		if !strings.HasPrefix(path, vm.vdir) {
			continue
		}

		if plen == vm.vdirlen {
			return vm, ""
		}

		if path[vm.vdirlen] == '/' {
			return vm, path[vm.vdirlen+1:]
		}
	}

	return nil, ""
}

// resolveMapping resolves a VFS path to a real URL.
// C: resolve_mapping — snprintf("%s/%s", vm_url, remain) — no path
// cleaning (verbatim join, "." and ".." pass through like C).
// The vfsPath argument corresponds to C's path AFTER the leading '/'
// has been stripped; Go keeps the "/" prefix on both sides instead.
func (p *VFSProtocol) resolveMapping(vfsPath string) (string, error) {
	vm, remain := p.findMapping(vfsPath)
	if vm == nil {
		return "", fmt.Errorf("No such file or directory")
	}

	if remain != "" {
		return vm.url + "/" + remain, nil
	}
	return vm.url, nil
}

// resolveMapping2 — C: resolve_mapping2. Requires a leading '/',
// reports "Invalid virtual directory" on resolve failure or when the
// resolved path is empty (vm_url == NULL → empty newpath).
func (p *VFSProtocol) resolveMapping2(vfsPath string) (string, error) {
	if len(vfsPath) == 0 || vfsPath[0] != '/' {
		return "", fmt.Errorf("No such file or directory")
	}

	realURL, err := p.resolveMapping(vfsPath)
	if err != nil {
		return "", fmt.Errorf("Invalid virtual directory")
	}

	if realURL == "" {
		return "", fmt.Errorf("Invalid virtual directory")
	}
	return realURL, nil
}

// Open opens a VFS URL.
// C: vfs_open — README.TXT via memfile_make; everything else resolves
// through resolve_mapping2 then fa_open_ex(newpath, flags, foe).
func (p *VFSProtocol) Open(url string, extra *OpenExtra) (*Handle, error) {
	// Strip vfs:// prefix — C: fa_resolve_proto already removed it;
	// fap_open receives the post-scheme path.
	if !strings.HasPrefix(url, VFSPrefix) {
		return nil, fmt.Errorf("not a VFS URL")
	}

	return p.FapOpen(url[len(VFSPrefix):], extra)
}

// FapOpen — C: vfs_open (fa_vfs.c:212-226). Receives the post-scheme
// path ("/test/x"), exactly what the FTP server passes to
// fa_protocol_vfs.fap_open.
func (p *VFSProtocol) FapOpen(vfsPath string, extra *OpenExtra) (*Handle, error) {
	// Handle README.TXT — C: memfile_make(READMETXT, strlen(READMETXT))
	if vfsPath == "/README.TXT" {
		if p.fileAccessMgr != nil && p.fileAccessMgr.bundleManager != nil {
			return p.fileAccessMgr.bundleManager.MemFileMake(
				[]byte(p.readmeText)), nil
		}
		return nil, fmt.Errorf("file access manager not available")
	}

	// C: resolve_mapping2(url, newpath, ...) then fa_open_ex
	realURL, err := p.resolveMapping2(vfsPath)
	if err != nil {
		return nil, err
	}

	// C: fa_open_ex(newpath, errbuf, errlen, flags, foe)
	if p.fileAccessMgr != nil {
		flags := 0
		if extra != nil {
			flags = extra.Flags
		}
		return FAOpenEx(p.fileAccessMgr, realURL, flags, extra)
	}
	return nil, fmt.Errorf("file access manager not available")
}

// Stat returns file statistics for a VFS URL.
// C: vfs_stat — memset(fs,0); "/README.TXT" and "/" are answered
// directly; other paths resolve via resolve_mapping (post url++) then
// fa_stat_ex(newpath, fs, flags, errbuf, errlen).
func (p *VFSProtocol) Stat(url string) (*FileStat, error) {
	// Strip vfs:// prefix — C: fa_resolve_proto removed it.
	if !strings.HasPrefix(url, VFSPrefix) {
		return nil, fmt.Errorf("not a VFS URL")
	}

	return p.FapStat(url[len(VFSPrefix):], 0)
}

// FapStat — C: vfs_stat (fa_vfs.c:230-261). Post-scheme path in; flags
// is fap_stat's flags arg (C: vfs_stat passes it to fa_stat_ex).
func (p *VFSProtocol) FapStat(vfsPath string, flags int) (*FileStat, error) {
	// C: !strcmp(url, "/README.TXT") → CONTENT_FILE + strlen(READMETXT)
	if vfsPath == "/README.TXT" {
		return &FileStat{
			Size:  int64(len(p.readmeText)),
			Type:  ContentFile,
			MTime: time.Time{},
		}, nil
	}

	// C: !strcmp(url, "/") → CONTENT_DIR
	if vfsPath == "/" {
		return &FileStat{
			Size:  0,
			Type:  ContentDir,
			MTime: time.Time{},
		}, nil
	}

	// C: url++; resolve_mapping → errbuf "No such file or directory"
	realURL, err := p.resolveMapping(vfsPath)
	if err != nil {
		return nil, err
	}

	// C: fa_stat_ex(newpath, fs, errbuf, errlen, flags)
	return StatEx(p.fileAccessMgr, realURL, flags)
}

// ScanDir scans a VFS directory
func (p *VFSProtocol) ScanDir(ctx context.Context, url string) (*Dir, error) {
	// Strip vfs:// prefix — C: fa_resolve_proto removed it.
	if !strings.HasPrefix(url, VFSPrefix) {
		return nil, fmt.Errorf("not a VFS URL")
	}

	return p.FapScandir(ctx, url[len(VFSPrefix):])
}

// FapScandir — C: vfs_scandir (fa_vfs.c:150-179). Post-scheme path in;
// flags ride the ctx (C: fap_scan's flags arg).
func (p *VFSProtocol) FapScandir(ctx context.Context, vfsPath string) (*Dir, error) {
	// Handle root directory — C: vfs_scandir's !strcmp(url, "/") branch.
	if vfsPath == "/" {
		p.mappingsMutex.Lock()
		defer p.mappingsMutex.Unlock()

		dir := DirAlloc()

		if len(p.mappings) == 0 {
			// C: fa_dir_add(fd, "vfs:///README.TXT", "README.TXT", ...)
			DirAdd(dir, "vfs:///README.TXT", "README.TXT", ContentFile)
		}

		for _, vm := range p.mappings {
			// C: fa_dir_add(fd, vm->vm_url, rstr_get(vm->vm_vdir), DIR)
			// — vm_vdir is the bare title; Go stores it "/"-prefixed.
			DirAdd(dir, vm.url, strings.TrimPrefix(vm.vdir, "/"), ContentDir)
		}

		return dir, nil
	}

	// C: url++; resolve_mapping → errbuf "No such file or directory",
	// then fa_scandir2(fd, newpath, errbuf, errlen, flags)
	realURL, err := p.resolveMapping(vfsPath)
	if err != nil {
		return nil, err
	}

	return FAScanDir(p.fileAccessMgr, realURL)
}

// CanHandle checks if the URL is a VFS URL
func (p *VFSProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, VFSPrefix)
}

// Name returns the protocol name
func (p *VFSProtocol) Name() string {
	return "vfs"
}

// Makedirs creates a directory in the VFS.
// C: vfs_makedir — resolve_mapping2 then fa_makedir (single-shot,
// NOT the recursive fa_makedirs).
func (p *VFSProtocol) Makedirs(url string) error {
	vfsPath := strings.TrimPrefix(url, VFSPrefix)
	if vfsPath == url {
		return fmt.Errorf("invalid VFS URL")
	}
	return p.FapMakedir(vfsPath)
}

// FapMakedir — C: vfs_makedir (fa_vfs.c:265-277). Post-scheme path in.
// Note C names this fap_makedir → fa_makedir (single-shot mkdir, NOT
// the recursive fa_makedirs).
func (p *VFSProtocol) FapMakedir(vfsPath string) error {
	realURL, err := p.resolveMapping2(vfsPath)
	if err != nil {
		return err
	}

	return Makedir(p.fileAccessMgr, realURL)
}

// Unlink deletes a file in the VFS.
// C: vfs_unlink — resolve_mapping2 then fa_unlink.
func (p *VFSProtocol) Unlink(url string) error {
	vfsPath := strings.TrimPrefix(url, VFSPrefix)
	if vfsPath == url {
		return fmt.Errorf("invalid VFS URL")
	}
	return p.FapUnlink(vfsPath)
}

// FapUnlink — C: vfs_unlink (fa_vfs.c:280-292). Post-scheme path in.
func (p *VFSProtocol) FapUnlink(vfsPath string) error {
	realURL, err := p.resolveMapping2(vfsPath)
	if err != nil {
		return err
	}

	return Unlink(p.fileAccessMgr, realURL)
}

// Rmdir removes a directory in the VFS.
// C: vfs_rmdir — resolve_mapping2 then fa_rmdir.
func (p *VFSProtocol) Rmdir(url string) error {
	vfsPath := strings.TrimPrefix(url, VFSPrefix)
	if vfsPath == url {
		return fmt.Errorf("invalid VFS URL")
	}
	return p.FapRmdir(vfsPath)
}

// FapRmdir — C: vfs_rmdir (fa_vfs.c:295-307). Post-scheme path in.
func (p *VFSProtocol) FapRmdir(vfsPath string) error {
	realURL, err := p.resolveMapping2(vfsPath)
	if err != nil {
		return err
	}

	return Rmdir(p.fileAccessMgr, realURL)
}

// Rename renames a file or directory in the VFS.
// C: vfs_rename — resolves the NEW path first, then the old, then
// fa_rename(oldpath, newpath).
func (p *VFSProtocol) Rename(oldURL, newURL string) error {
	newVFSPath := strings.TrimPrefix(newURL, VFSPrefix)
	if newVFSPath == newURL {
		return fmt.Errorf("invalid VFS URL")
	}

	oldVFSPath := strings.TrimPrefix(oldURL, VFSPrefix)
	if oldVFSPath == oldURL {
		return fmt.Errorf("invalid VFS URL")
	}
	return p.FapRename(oldVFSPath, newVFSPath)
}

// FapRename — C: vfs_rename (fa_vfs.c:310-327). Post-scheme paths in;
// resolves the NEW path first, then the old.
func (p *VFSProtocol) FapRename(oldVFSPath, newVFSPath string) error {
	realNewURL, err := p.resolveMapping2(newVFSPath)
	if err != nil {
		return err
	}

	realOldURL, err := p.resolveMapping2(oldVFSPath)
	if err != nil {
		return err
	}

	return Rename(p.fileAccessMgr, realOldURL, realNewURL)
}

// AddMapping adds a new VFS mapping (test/helper API — in C, mappings
// only come from the service-prop subscriptions).
func (p *VFSProtocol) AddMapping(vdir string, url string) {
	vm := &VFSMapping{
		vdir:    vdir,
		vdirlen: len(vdir),
		url:     url,
		isFS:    true,
	}
	p.mappingsMutex.Lock()
	p.addMappingLocked(vm)
	p.mappingsMutex.Unlock()
}

// RemoveMapping removes a VFS mapping (test/helper API).
func (p *VFSProtocol) RemoveMapping(vdir string) {
	p.mappingsMutex.Lock()
	defer p.mappingsMutex.Unlock()

	for i, vm := range p.mappings {
		if vm.vdir == vdir {
			p.mappings = slices.Delete(p.mappings, i, i+1)
			vm.exported = false
			break
		}
	}
}
