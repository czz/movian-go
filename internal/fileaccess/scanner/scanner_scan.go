package scanner

import (
	"context"
	"errors"
	"time"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/backend/playlist/playqueue"
	"github.com/czz/movian-go/internal/db/kvstore"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// rescan rescans the directory
// findEntryProp finds the property node for directory entry url under
// s.nodes — the Go equivalent of fde->fde_prop (the prop created by
// make_prop() carries a "url" child holding the entry's URL).
func (s *Scanner) findEntryProp(url string) *propcore.Prop {
	if s.nodes == nil {
		return nil
	}
	for _, c := range s.nodes.GetChildren() {
		if u := c.GetChild("url"); u != nil && u.GetString() == url {
			return c
		}
	}
	return nil
}

// findDirEntryByURL — C: fa_dir_find (fileaccess.c:753) — looks up an
// entry by its full URL (Go keys Entries by filename, so compare URLs).
func findDirEntryByURL(fd *Dir, url string) (string, *DirEntry) {
	if fd == nil {
		return "", nil
	}
	for k, e := range fd.Entries {
		if e.URL == url {
			return k, e
		}
	}
	return "", nil
}

// ScanDir scans a directory at the given URL and returns its contents
// Parameters:
//   - ctx: Context for cancellation and timeout
//   - url: The URL of the directory to scan (e.g., "file:///path/to/dir"
//     or a raw local path like "/path/to/dir")
//
// Returns:
//   - *Dir: The directory structure with entries
//   - error: Error if scanning failed
//
// C: fa_scandir (fileaccess.c:440) dispatches through the registered
// protocol list — the FS backend claims both "file://" URLs and raw
// paths without a "scheme:" prefix (fa_backend.c backend_can_handle).
// The Go equivalent is fileaccesscore.ScanDir on the default fam.
func ScanDir(fam *fileaccesscore.FileAccessManager, ctx context.Context, url string) (*Dir, error) {
	// Check context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	fd, err := fileaccesscore.ScanDir(fam, url)
	if err != nil {
		return nil, err
	}
	if fd == nil {
		return nil, errors.New("ScanDir: unable to scan " + url)
	}

	dir := DirAlloc()
	for _, e := range fd.Entries {
		de := &DirEntry{
			Name:     e.Filename,
			Filename: e.Filename,
			URL:      e.URL,
			Type:     e.Type,
			MTime:    e.Stat.MTime,
			StatDone: e.StatDone,
			Probed:   ScannerProbeStatus(e.Probed),
			Marked:   e.Marked,
			Stat: FileInfo{
				Name:     e.Filename,
				Size:     e.Stat.Size,
				Type:     e.Stat.Type,
				MTime:    e.Stat.MTime,
				Modified: e.Stat.MTime,
				URL:      e.URL,
			},
		}
		if md, ok := e.Md.(*metadata.Metadata); ok {
			de.md = md
		}
		dir.Entries[e.Filename] = de
		dir.Count++
	}
	return dir, nil
}

// rescan rescans the directory
// C: rescan (fa_scanner.c:460-525)
func (s *Scanner) rescan() int {
	fd, err := ScanDir(s.fam, s.ctx, s.url)
	if err != nil {
		return -1
	}

	if s.fd != nil && s.fd.Count != fd.Count {
		s.ts.Debug("scanner", "%s: Rescanning found %d items, previously %d",
			s.url, fd.Count, s.fd.Count)
	}

	changed := 0

	// C: for(a = RB_FIRST(&s->s_fd->fd_entries); a; a = n) — walk old set,
	// match against the fresh listing by URL.
	if s.fd != nil {
		for name, a := range s.fd.Entries {
			bkey, b := findDirEntryByURL(fd, a.URL)
			if b != nil {
				// Exists in old and new set
				if b.Type == ContentShare {
					a.Type = b.Type
					delete(fd.Entries, bkey) // C: fa_dir_entry_free(fd, b)
					if p := s.findEntryProp(a.URL); p != nil {
						set_type(s.pm, p, a.Type)
					}
					continue
				}

				// C: if(!fa_dir_entry_stat(b) &&
				//       a->fde_stat.fs_mtime != b->fde_stat.fs_mtime)
				if !b.StatDone {
					if st, serr := fileaccesscore.Stat(
						s.fam,
						b.URL); serr == nil && st != nil {
						b.Stat.MTime = st.MTime
						b.StatDone = true
					}
				}
				if b.StatDone && !a.Stat.MTime.Equal(b.Stat.MTime) {
					// Modification time changed — trig deep probe
					a.Type = b.Type
					a.Probed = FDEProbedNone
					a.Stat = b.Stat
					a.IgnoreCache = true
					changed = 1
				}
				delete(fd.Entries, bkey) // C: fa_dir_entry_free(fd, b)
			} else {
				changed = 1
				// C: scanner_entry_destroy(s, a, "rescan")
				se := &ScannerEntry{url: a.URL, prop: s.findEntryProp(a.URL)}
				s.scannerEntryDestroy(se, "rescan")
				delete(s.fd.Entries, name)
				s.fd.Count--
			}
		}
	}

	// C: while((b = fd->fd_entries.root) != NULL) { fa_dir_remove(fd, b);
	//      fa_dir_insert(s->s_fd, b); changed |= scanner_entry_setup(s, b, ...) }
	for nk, b := range fd.Entries {
		delete(fd.Entries, nk)
		if s.fd != nil {
			s.fd.Entries[nk] = b
			s.fd.Count++
		}
		se := &ScannerEntry{
			url:         b.URL,
			filename:    b.Filename,
			typ:         b.Type,
			statdone:    b.StatDone,
			stat:        b.Stat,
			probestatus: b.Probed,
		}
		changed |= s.scannerEntrySetup(se, "rescan")
	}

	if changed != 0 {
		s.analyzer(true)
	}
	return 0
}

// doscan performs the initial scan
func (s *Scanner) doscan() int {
	var pendingRescan int = 0

	s.fd = s.metadb_metadata_scandir(s.url, nil)

	if s.fd == nil {
		s.fd, _ = ScanDir(s.fam, s.ctx, s.url)
		if s.fd != nil {
			s.ts.Debug("scanner", "%s: Found %d by directory scanning", s.url, s.fd.Count)
		}
	} else {
		s.ts.Debug("scanner", "%s: Found %d items in cache", s.url, s.fd.Count)
		pendingRescan = 1
	}

	if s.loading != nil {
		s.loading.SetInt(0)
	}

	if s.fd != nil {
		s.analyzer(false)

		if s.nodes != nil {
			pv := propcore.CreatePropVec(s.fd.Count)
			for _, entry := range s.fd.Entries {
				// Convert DirEntry to ScannerEntry for make_prop
				scannerEntry := &ScannerEntry{
					url: entry.URL,
					typ: entry.Type,
					stat: FileInfo{
						Size:     entry.Stat.Size,
						Type:     entry.Stat.Type,
						Modified: entry.Stat.MTime,
						URL:      entry.URL,
					},
					filename:    entry.Filename,
					probestatus: FDEProbedFilename,
					statdone:    entry.StatDone,
				}
				make_prop(s.pm, scannerEntry, s.fam.Gconf())
				pv.Append(scannerEntry.prop)
			}
			s.pm.SetParentVector(pv, s.nodes, nil, "")
			s.pm.Destroy(pv)
		}
		s.analyzer(true)
	} else {
		// TRACE(TRACE_INFO, "scanner", "Unable to scan %s -- %s", s.url, err)
		s.fd = &Dir{}
	}

	if pendingRescan == 1 {
		s.ts.Debug("scanner", "%s: Starting rescan", s.url)
		rescanErr := s.rescan()
		s.ts.Debug("scanner", "%s: Rescan completed: %d", s.url, rescanErr)
	}

	s.closedb()

	// C: while(s->s_running)
	//      prop_courier_wait_and_dispatch(s->s_pc);
	for pc := s.pc; s.running.Load() == 1 && pc != nil && pc == s.pc; {
		pc.Wait()
	}

	return 0
}

// browseAsDir — C: browse_as_dir (fa_scanner.c:1013-1020)
func (s *Scanner) browseAsDir() {
	s.db = metadata.DecoratedBrowseCreate(s.model, s.pnf, s.nodes, s.title,
		metadata.DecoFlagsRawFilenames|metadata.DecoFlagsNoAutoDestroy,
		s.url, "Directory", s.kvstore, s.metadataMgr)
}

// cleanupModel cleans up the model
// C: cleanup_model (fa_scanner.c:615-623)
func (s *Scanner) cleanupModel() {
	if s.db != nil {
		metadata.DecoratedBrowseDestroy(s.db)
		s.db = nil
	}
	// prop_destroy_childs(s.nodes)
	if s.nodes != nil {
		s.pm.DestroyChilds(s.nodes)
	}
}

// scannerThread is the main scanner thread
func (s *Scanner) scannerThread() {
	if s.model != nil {
		contentsProp := s.pm.CreateMulti(s.model, "contents")
		if contentsProp != nil {
			contentsProp.SetVoid()
		}
		canPasteProp := s.pm.CreateMulti(s.model, "canPaste")
		if canPasteProp != nil {
			canPasteProp.SetInt(1)
		}
	}
	s.browseAsDir()
	s.doscan()

	s.cleanupModel()
	s.closedb()

	s.playme = ""

	ScannerRelease(s)
}

// deleteItems deletes selected items
func (s *Scanner) deleteItems(pv any) {
	if pv == nil {
		return
	}

	propVec, ok := pv.(*propcore.Prop)
	if !ok {
		return
	}

	for i := range propVec.Len() {
		p := propVec.Get(i)
		if p == nil {
			continue
		}

		// Get URL from property
		urlProp := p.FindChild("url")
		if urlProp == nil {
			continue
		}
		url := urlProp.GetString()
		if url == "" {
			continue
		}

		// Delete the file/directory
		if err := FAUnlink(s.ctx, url); err != nil {
			if s.notifMgr != nil {
				s.notifMgr.NotifyAdd(nil, notifications.NotifyError, "", 5,
					"Unable to delete %s: %s", url, err.Error())
			}
		} else {
			if s.notifMgr != nil {
				s.notifMgr.NotifyAdd(nil, notifications.NotifyInfo, "", 5,
					"Deleted %s", url)
			}
		}
	}
}

// addSortOptionType adds sort option by type.
// Reproduces C's add_sort_option_type (fa_scanner.c:762):
// Creates a multiopt widget under model.options with "Filename"/"Newest first"/"Oldest first"
// choices, subscribes to selection changes, and updates PropNF sort key.
func (s *Scanner) addSortOptionType(model *propcore.Prop) {
	if s.pnf == nil || model == nil {
		return
	}

	// C: add_sort_option_type (fa_scanner.c:757-819)
	parent := s.pm.CreateMulti(model, "options")
	n := s.pm.CreateRootEx("", false)
	m := s.pm.CreateMulti(n, "metadata")
	options := s.pm.CreateMulti(n, "options")

	if t := s.pm.CreateMulti(n, "type"); t != nil {
		t.SetString("multiopt")
	}
	if e := s.pm.CreateMulti(n, "enabled"); e != nil {
		e.SetInt(1)
	}
	// C: prop_link(_p("Sort on"), prop_create(m, "title"))
	if tp := s.pm.CreateMulti(m, "title"); tp != nil {
		tp.SetString("Sort on")
	}

	onTitle := s.pm.CreateRootEx("title", false)
	if tp := s.pm.CreateMulti(onTitle, "title"); tp != nil {
		tp.SetString("Filename") // C: prop_link(_p("Filename"), ...)
	}
	s.pm.SetParentEx(onTitle, options, nil, "")

	onDate := s.pm.CreateRootEx("date", false)
	if tp := s.pm.CreateMulti(onDate, "title"); tp != nil {
		tp.SetString("Newest first")
	}
	s.pm.SetParentEx(onDate, options, nil, "")

	onDateOld := s.pm.CreateRootEx("dateold", false)
	if tp := s.pm.CreateMulti(onDateOld, "title"); tp != nil {
		tp.SetString("Oldest first")
	}
	s.pm.SetParentEx(onDateOld, options, nil, "")

	// C: rstr_t *cur = kv_url_opt_get_rstr(s->s_url, KVSTORE_DOMAIN_SYS, "sortorder")
	cur := s.kvstore.UrlOptGetString(s.url, kvstore.DomainSys, "sortorder")

	if cur == "date" {
		propcore.ProxySelect(onDate)
		propcore.PropNFSort(s.pnf.PropNF, "node.metadata.timestamp", true, 3, nil, false)
	} else if cur == "dateold" {
		propcore.ProxySelect(onDate) // C selects on_date here (fa_scanner.c:785)
		propcore.PropNFSort(s.pnf.PropNF, "node.metadata.timestamp", false, 3, nil, false)
	} else {
		propcore.ProxySelect(onTitle)
		propcore.PropNFSort(s.pnf.PropNF, "node.metadata.title", false, 3, nil, true)
	}

	// C: prop_linkselected_create(options, n, "current", "value")
	s.pm.LinkselectedCreate(options, n, "current", "value")

	// C: prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE | PROP_SUB_TRACK_DESTROY,
	//                   PROP_TAG_CALLBACK, set_sort_order, s,
	//                   PROP_TAG_ROOT, options, NULL)  (fa_scanner.c:794-798)
	ScannerRetain(s)
	options.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
		if event == propcore.EventDestroyed {
			ScannerRelease(s)
			return
		}
		if event == propcore.EventSelectChild && len(args) > 0 {
			if p, ok := args[0].(*propcore.Prop); ok {
				val := p.GetName()
				// C: set_sort_order (fa_scanner.c:719-752)
				switch val {
				case "title":
					propcore.PropNFSort(s.pnf.PropNF, "node.metadata.title", false, 3, nil, true)
				case "date":
					propcore.PropNFSort(s.pnf.PropNF, "node.metadata.timestamp", true, 3, nil, false)
				case "dateold":
					propcore.PropNFSort(s.pnf.PropNF, "node.metadata.timestamp", false, 3, nil, false)
				}
				s.kvstore.UrlOptSet(s.url, kvstore.DomainSys, "sortorder", kvstore.SetString, val)
			}
		}
	}, s, propcore.SubNoInitialUpdate, propcore.SubFlagTrackDestroy)

	s.pm.SetParentEx(n, parent, nil, "")
}

// addSortOptionDirfirst adds sort option for directories first.
// Reproduces C's add_sort_option_dirfirst (fa_scanner.c:861):
// Creates a bool widget under model.options, subscribes to value changes.
func (s *Scanner) addSortOptionDirfirst(model *propcore.Prop) {
	if s.pnf == nil || model == nil {
		return
	}

	// C: add_sort_option_dirfirst (fa_scanner.c:849-880)
	parent := s.pm.CreateMulti(model, "options")
	n := s.pm.CreateRootEx("", false)
	m := s.pm.CreateMulti(n, "metadata")
	value := s.pm.CreateMulti(n, "value")
	v := s.kvstore.UrlOptGetInt(s.url, kvstore.DomainSys, "dirsfirst", 1)

	if t := s.pm.CreateMulti(n, "type"); t != nil {
		t.SetString("bool")
	}
	if e := s.pm.CreateMulti(n, "enabled"); e != nil {
		e.SetInt(1)
	}
	if value != nil {
		value.SetInt(v)
	}
	if tp := s.pm.CreateMulti(m, "title"); tp != nil {
		tp.SetString("Sort folders first") // C: prop_link(_p("Sort folders first"), ...)
	}

	// C: prop_nf_sort(s->s_pnf, v ? "node.type" : NULL, 0, 0, typemap, 1)
	path := ""
	if v != 0 {
		path = "node.type"
	}
	propcore.PropNFSort(s.pnf.PropNF, path, false, 0, scannerTypeMap, true)

	// C: prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE | PROP_SUB_TRACK_DESTROY,
	//                   PROP_TAG_CALLBACK, set_sort_dirs, s,
	//                   PROP_TAG_ROOT, value, NULL)  (fa_scanner.c:875-878)
	if value != nil {
		ScannerRetain(s)
		value.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
			if event == propcore.EventDestroyed {
				ScannerRelease(s)
				return
			}
			if event == propcore.EventSetInt && len(args) > 0 {
				if val, ok := args[0].(int); ok {
					// C: set_sort_dirs (fa_scanner.c:832-846)
					p := ""
					if val != 0 {
						p = "node.type"
					}
					propcore.PropNFSort(s.pnf.PropNF, p, false, 0, scannerTypeMap, true)
					s.kvstore.UrlOptSet(s.url, kvstore.DomainSys, "dirsfirst", kvstore.SetInt, val)
				}
			}
		}, s, propcore.SubNoInitialUpdate, propcore.SubFlagTrackDestroy)
	}

	s.pm.SetParentEx(n, parent, nil, "")
}

// addIncludeInLibrary adds library inclusion option
// C: add_include_in_library (fa_scanner.c:899-925)
func (s *Scanner) addIncludeInLibrary(model any) {
	if model == nil {
		return
	}

	propModel, ok := model.(*propcore.Prop)
	if !ok {
		return
	}

	parent := s.pm.CreateMulti(propModel, "options")
	n := s.pm.CreateRootEx("", false)
	m := s.pm.CreateMulti(n, "metadata")
	value := s.pm.CreateMulti(n, "value")
	v := s.ix.FAIndexerEnabled(s.url)

	if t := s.pm.CreateMulti(n, "type"); t != nil {
		t.SetString("bool")
	}
	if e := s.pm.CreateMulti(n, "enabled"); e != nil {
		e.SetInt(1)
	}
	if value != nil {
		if v {
			value.SetInt(1)
		} else {
			value.SetInt(0)
		}
	}
	if tp := s.pm.CreateMulti(m, "title"); tp != nil {
		tp.SetString("Include in library")
	}

	// C: prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE | PROP_SUB_TRACK_DESTROY,
	//                   PROP_TAG_CALLBACK, set_include_in_library, s,
	//                   PROP_TAG_ROOT, value, NULL)  (fa_scanner.c:919-922)
	if value != nil {
		ScannerRetain(s)
		value.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
			if event == propcore.EventDestroyed {
				ScannerRelease(s)
				return
			}
			if event == propcore.EventSetInt && len(args) > 0 {
				if val, ok := args[0].(int); ok {
					// C: set_include_in_library → fa_indexer_enable(s->s_url, val)
					s.ix.FAIndexerEnable(s.url, val != 0)
				}
			}
		}, s, propcore.SubNoInitialUpdate, propcore.SubFlagTrackDestroy)
	}

	s.pm.SetParentEx(n, parent, nil, "")
}

// addOnlySupportedFiles adds supported files filter option.
// Returns the "value" prop (C: *valp out-parameter), used as the enable
// prop for the node.type predicates. C: add_only_supported_files
// (fa_scanner.c:975-1006)
func (s *Scanner) addOnlySupportedFiles(model any) *propcore.Prop {
	if model == nil {
		return nil
	}

	propModel, ok := model.(*propcore.Prop)
	if !ok {
		return nil
	}

	parent := s.pm.CreateMulti(propModel, "options")
	n := s.pm.CreateRootEx("", false)
	m := s.pm.CreateMulti(n, "metadata")
	value := s.pm.CreateMulti(n, "value")
	v := s.kvstore.UrlOptGetInt(s.url, kvstore.DomainSys, "supportedfiles", 1)

	if t := s.pm.CreateMulti(n, "type"); t != nil {
		t.SetString("bool")
	}
	if e := s.pm.CreateMulti(n, "enabled"); e != nil {
		e.SetInt(1)
	}
	if value != nil {
		value.SetInt(v)
	}
	if tp := s.pm.CreateMulti(m, "title"); tp != nil {
		tp.SetString("Show only supported files")
	}

	// C: prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE | PROP_SUB_TRACK_DESTROY,
	//                   PROP_TAG_CALLBACK, set_only_supported_files, s,
	//                   PROP_TAG_ROOT, value, NULL)  (fa_scanner.c:998-1001)
	if value != nil {
		ScannerRetain(s)
		value.Subscribe(func(opaque any, event propcore.EventType, args ...any) {
			if event == propcore.EventDestroyed {
				ScannerRelease(s)
				return
			}
			if event == propcore.EventSetInt && len(args) > 0 {
				if val, ok := args[0].(int); ok {
					// C: set_only_supported_files → kv_url_opt_set(..., "supportedfiles", KVSTORE_SET_INT, val)
					s.kvstore.UrlOptSet(s.url, kvstore.DomainSys, "supportedfiles", kvstore.SetInt, val)
				}
			}
		}, s, propcore.SubNoInitialUpdate, propcore.SubFlagTrackDestroy)
	}

	// C: if(prop_set_parent(n, parent)) { prop_destroy(n); *valp = NULL; }
	s.pm.SetParentEx(n, parent, nil, "")
	return value
}

// FAScannerPage creates a scanner page (public API)
// Reproduces C's fa_scanner_page (fa_scanner.c:1026):
//  1. Create scanner with refcount=2 (scannerThread + nodes subscription)
//  2. Create model.source, model.loading
//  3. Create PropNF linking model.nodes ← model.source with AUTODESTROY
//  4. Set model.canFilter = 1
//  5. Add PropNF predicates: node.hidden, node.type=="unknown", node.type=="file"
//  6. Add sort options (type + dirfirst) with subscriptions
//  7. Add only-supported-files option
//  8. Subscribe to model.nodes for PROP_DESTROYED (page destruction → s.running=0)
//  9. Run scannerThread
func FAScannerPage(url string, urlMtime time.Time, model *propcore.Prop, playme string, directClose *propcore.Prop, title string, pm *propcore.PropManager, ts *trace.TraceSystem, fam *fileaccesscore.FileAccessManager, ms *mediacore.MediaSystem, ix *Indexer, pq *playqueue.PlayQueue, kvsDep any, mmDep any, g *gconf.T) {
	if g == nil {
		g = gconf.New()
	}
	// C: playqueue is a global; injected (process-global singleton in C).
	kvs, _ := kvsDep.(*kvstore.KVStore)
	mm, _ := mmDep.(*metadata.MetadataManager)
	// C: scanner_create(url, url_mtime, gconf.enable_fa_scanner_debug)
	// (fa_scanner.c:1030)
	dbg := 0
	if g.EnableFaScannerDebug.Load() {
		dbg = 1
	}
	s := ScannerCreate(context.Background(), url, urlMtime, dbg, pm, ts, fam, ms, ix)
	s.pq = pq
	s.kvstore = kvs
	s.metadataMgr = mm
	if mm != nil {
		s.metadb = mm.Get() // C: s->s_metadb = metadb_get() in scanner_create
	}

	s.playme = playme

	// C: s->s_nodes = prop_create_r(model, "source")
	s.nodes = pm.CreateMulti(model, "source")

	// C: s->s_loading = prop_create_r(model, "loading")
	s.loading = pm.CreateMulti(model, "loading")

	s.model = model
	s.directClose = directClose
	s.title = title

	// C: s->s_pnf = prop_nf_create(prop_create(s->s_model, "nodes"),
	//                               s->s_nodes,
	//                               prop_create(s->s_model, "filter"),
	//                               PROP_NF_AUTODESTROY)
	nodesProp := pm.CreateMulti(s.model, "nodes")
	filterProp := pm.CreateMulti(s.model, "filter")
	if nodesProp != nil && s.nodes != nil {
		// C: prop_nf_create(dst, src, filter, PROP_NF_AUTODESTROY)
		inner := propcore.PropNFCreate(nodesProp, s.nodes, filterProp, propcore.PropNFAutoDestroy)
		if inner != nil {
			s.pnf = &PropNF{PropNF: inner}
		}
	}

	// C: prop_set(s->s_model, "canFilter", PROP_SET_INT, 1)
	canFilterProp := pm.CreateMulti(s.model, "canFilter")
	if canFilterProp != nil {
		canFilterProp.SetInt(1)
	}

	// C: prop_nf_pred_int_add(s->s_pnf, "node.hidden", PROP_NF_CMP_EQ, 1, NULL, PROP_NF_MODE_EXCLUDE)
	if s.pnf != nil {
		propcore.PropNFPredIntAdd(s.pnf.PropNF, "node.hidden",
			propcore.PropNFCmpEq, 1, nil, propcore.PropNFModeExclude)
	}

	// C: if(gconf.enable_indexer)
	//     add_include_in_library(s, s->s_model)  (fa_scanner.c:1057-1058)
	if g.EnableIndexer.Load() {
		s.addIncludeInLibrary(model)
	}

	// C: add_sort_option_type(s, s->s_model)
	s.addSortOptionType(model)

	// C: add_sort_option_dirfirst(s, s->s_model)
	s.addSortOptionDirfirst(model)

	// C: add_only_supported_files(s, s->s_model, &onlysupported)
	//     prop_nf_pred_str_add(s->s_pnf, "node.type", PROP_NF_CMP_EQ, "unknown", onlysupported, PROP_NF_MODE_EXCLUDE)
	//     prop_nf_pred_str_add(s->s_pnf, "node.type", PROP_NF_CMP_EQ, "file", onlysupported, PROP_NF_MODE_EXCLUDE)
	onlysupported := s.addOnlySupportedFiles(model)
	if s.pnf != nil {
		propcore.PropNFPredStrAdd(s.pnf.PropNF, "node.type",
			propcore.PropNFCmpEq, "unknown", onlysupported,
			propcore.PropNFModeExclude)
		propcore.PropNFPredStrAdd(s.pnf.PropNF, "node.type",
			propcore.PropNFCmpEq, "file", onlysupported,
			propcore.PropNFModeExclude)
	}

	// C: s->s_ref = fa_reference(s->s_url) (fa_scanner.c:1074)
	s.ref = s.fam.FAReference(s.url)

	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY, ..., scanner_nodes_callback, s, PROP_TAG_ROOT, s->s_nodes, ...)
	// Subscribe to model.nodes for page destruction detection.
	// When the page is destroyed, model.nodes is destroyed, firing PROP_DESTROYED,
	// which sets s.running=0 and releases the scanner reference.
	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY,
	//                   PROP_TAG_CALLBACK, scanner_nodes_callback, s,
	//                   PROP_TAG_ROOT, s->s_nodes,
	//                   PROP_TAG_COURIER, s->s_pc, NULL);
	if s.nodes != nil {
		ScannerRetain(s) // refcount: 2 → 3 (scannerThread + nodes sub + sort sub already retained)
		s.pm.SubscribeWithCourier(s.nodes, s.pc, func(opaque any, event propcore.EventType, args ...any) {
			if event == propcore.EventDestroyed {
				s.running.Store(0)
				ScannerRelease(s)
			} else if event == propcore.EventReqDeleteVector {
				if len(args) > 0 {
					s.deleteItems(args[0])
				}
			}
		}, s, propcore.SubFlagTrackDestroy)
	}

	s.scannerThread()
}

// metadb_metadata_scandir scans a directory and caches the results in the metadata database
// metadb_metadata_scandir scans a directory using the metadata DB cache
// (C: metadb_metadata_scandir, metadb.c:2235-2312)
func (s *Scanner) metadb_metadata_scandir(url string, mtime *int64) *Dir {
	db := s.getdb()
	if db == nil {
		return nil
	}
	fd := s.metadataMgr.MetadbMetadataScandir(db, url, mtime)
	if fd == nil {
		return nil
	}
	defer fileaccesscore.DirFree(fd)

	dir := &Dir{Entries: make(map[string]*DirEntry)}
	for _, e := range fd.Entries {
		de := &DirEntry{
			Filename: e.Filename,
			URL:      e.URL,
			Type:     e.Type,
			StatDone: e.StatDone,
			Probed:   ScannerProbeStatus(e.Probed),
			Marked:   e.Marked,
			Stat: FileInfo{
				Name:     e.Filename,
				Size:     e.Stat.Size,
				Type:     e.Stat.Type,
				Modified: e.Stat.MTime,
				URL:      e.URL,
			},
		}
		if e.Md != nil {
			de.md = e.Md.(*metadata.Metadata)
		}
		dir.Entries[e.Filename] = de
		dir.Count++
	}
	return dir
}
