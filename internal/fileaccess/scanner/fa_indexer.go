// Package scanner — canonical port of src/fileaccess/fa_indexer.c.
//
// The file indexer: a background thread that rescans enabled root
// directories, diffs the live fa_scandir listing against the metadb,
// and updates item rows / index statuses accordingly.
package scanner

import (
	"github.com/czz/movian-go/internal/gconf"
	"slices"
	"sync"
	"sync/atomic"

	dbpkg "github.com/czz/movian-go/internal/db"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/trace"
)

// gcfg.EnableIndexerDebug — C: gconf.enable_indexer_debug.

func (indexer *Indexer) indexerTrace(format string, args ...any) {
	if g := indexer.gconf.Load(); g != nil && g.EnableIndexerDebug.Load() {
		indexer.ts.Load().Trace(trace.TRACE_DEBUG, "Indexer", format, args...)
	}
}

// updateItem — C: update_item (fa_indexer.c:40-67)
func (indexer *Indexer) updateItem(db *dbpkg.DB, fsentry *fileaccesscore.DirEntry,
	parent string, parentMtime int64) {

	var md *metadata.Metadata
	indexStatus := metadata.IndexStatusAnalyzed

	if metadata.ContentDirish(metadata.ContentType(fsentry.Type)) {
		md = FAProbeDir(indexer.fam.Load(), fsentry.URL)

		if md != nil && md.ContentType == metadata.ContentDir {
			// Regular dirs need further scanning
			indexStatus = metadata.IndexStatusUnset
		}
	} else {
		// C: fa_probe_metadata(url, NULL, 0, rstr_get(fde_filename), NULL)
		md, _ = FAProbeMetadata(indexer.fam.Load(), fsentry.URL,
			fsentry.Filename, nil)
	}
	if md == nil {
		return
	}

	indexer.mm.Load().MetadbMetadataWrite(db, fsentry.URL,
		fsentry.Stat.MTime.Unix(), md, parent, parentMtime, indexStatus)
	// C: metadata_destroy(md) — GC'd
}

// rescanDirectory — C: rescan_directory (fa_indexer.c:74-133)
// Diffs the live directory listing against the metadb contents.
func (indexer *Indexer) rescanDirectory(url string, db *dbpkg.DB,
	fsMtime int64) int {

	// C: fa_scandir(url, errbuf, errlen)
	fsdir, err := fileaccesscore.ScanDir(indexer.fam.Load(), url)
	if err != nil {
		return -1
	}

	for _, fsentry := range fsdir.Entries {
		fileaccesscore.DirEntryStat(indexer.fam.Load(), fsentry)
		if fsentry.Type == ContentFile {
			fsentry.Type = ContentTypeFromFilename(fsentry.Filename)
		}
	}

	// C: metadb_metadata_scandir(db, url, NULL)
	dbdir := indexer.mm.Load().MetadbMetadataScandir(db, url, nil)
	if dbdir == nil {
		dbdir = fileaccesscore.DirAlloc()
	}

	for _, dbentry := range dbdir.Entries {
		fsentry := fileaccesscore.DirFind(fsdir, dbentry.URL)
		if fsentry != nil && fsentry.Type == ContentUnknown {
			fsentry = nil
		}

		if fsentry != nil {

			// Exist in fs and in db, check modification time and index status
			dbmd, _ := dbentry.Md.(*metadata.Metadata)
			if fsentry.Stat.MTime.Unix() == dbentry.Stat.MTime.Unix() &&
				dbmd != nil &&
				dbmd.IndexStatus >= metadata.IndexStatusAnalyzed {
				// Ok, don't do anything
			} else {
				indexer.indexerTrace("Updating item %s", fsentry.URL)
				indexer.updateItem(db, fsentry, url, fsMtime)
			}
			fileaccesscore.DirEntryFree(fsdir, fsentry)
		} else {
			// Exist in DB but not in filesystem
			indexer.indexerTrace("Removing item %s", dbentry.URL)
			indexer.mm.Load().MetadbUnparentItem(db, dbentry.URL)
		}
	}

	for _, fsentry := range fsdir.Entries {
		if fsentry.Type == ContentUnknown {
			continue
		}
		indexer.indexerTrace("New item %s", fsentry.URL)
		indexer.updateItem(db, fsentry, url, fsMtime)
	}

	fileaccesscore.DirFree(fsdir)
	fileaccesscore.DirFree(dbdir)
	return 0
}

// indexDirectory — C: index_directory (fa_indexer.c:139-172)
// Rescans url and stamps its item row with the resulting index status.
func (indexer *Indexer) indexDirectory(url string) {
	db := indexer.mm.Load().Get()

	err := 0
	fs, serr := fileaccesscore.StatEx(indexer.fam.Load(), url,
		fileaccesscore.FaNonInteractive)
	if serr == nil {
		indexer.indexerTrace("Scanning path %s", url)
		err = indexer.rescanDirectory(url, db, fs.MTime.Unix())
	} else {
		indexer.indexerTrace("Scanning %s failed -- %s", url, serr)
		err = 1
	}

	// Update the index status for the scanned directory
	// C: db_prepare + UPDATE item SET indexstatus=?2 WHERE url=?1
	status := metadata.IndexStatusAnalyzed
	if err != 0 {
		status = metadata.IndexStatusError
	}
	if db != nil {
		// C: db_prepare + UPDATE item SET indexstatus=?2 WHERE url=?1
		stmt, rc := dbpkg.DBPrepare(db,
			"UPDATE item SET indexstatus = ?2 WHERE url = ?1")
		if rc == dbpkg.SQLITE_OK {
			stmt.BindText(1, url)
			stmt.BindInt(2, int(status))
			dbpkg.DBStep(stmt)
			stmt.Finalize()
		}
	}
	indexer.mm.Load().Close(db)
}

// ---------------------------------------------------------------------------
// item_queue — C: TAILQ_HEAD(item_queue, item) (fa_indexer.c:176-217)
// ---------------------------------------------------------------------------

// getItems — C: get_items (fa_indexer.c:196-218)
// Runs query with ?1=pfx, appending result URLs to q.
func getItems(db *dbpkg.DB, q *[]string, pfx, query string) int {
	if db == nil {
		return metadata.MetadataPermanentError
	}
	sel, rc := dbpkg.DBPrepare(db, query)
	if rc != dbpkg.SQLITE_OK {
		return metadata.MetadataPermanentError
	}
	defer sel.Finalize()
	sel.BindText(1, pfx)
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		*q = append(*q, sel.ColumnText(0))
	}
	return 0
}

// findUnprocessedDirectory — C: find_unprocessed_directory (fa_indexer.c:224-254)
func (indexer *Indexer) findUnprocessedDirectory(prefix string) int {
	db := indexer.mm.Load().Get()
	pfx := dbpkg.DBEscapePathQuery(prefix)

	var q []string
	r := getItems(db, &q, pfx,
		"SELECT url "+
			"FROM item "+
			"WHERE url LIKE ? "+
			"AND contenttype = 1 "+
			"AND indexstatus = 0 "+
			"LIMIT 1")

	indexer.mm.Load().Close(db)
	if r != 0 {
		return 0
	}

	r = 0
	for _, u := range q {
		indexer.indexDirectory(u)
		r = 1
	}
	return r
}

// ---------------------------------------------------------------------------
// indexer roots — C: struct indexer_root_queue roots (fa_indexer.c:257-385)
// ---------------------------------------------------------------------------

// indexerRoot — C: indexer_root_t (fa_indexer.c:263-268)
type indexerRoot struct {
	url         string // ir_url
	refcount    int    // ir_refcount
	rootScanned bool   // ir_root_scanned
}

// Indexer — C: fa_indexer.c static indexer state (mutex+cond+roots+init)
// and indexer.fam, the FileAccessManager the indexer works on
// (C: gconf.fa / fa_indexer's implicit fileaccess context).
// Owned by main (ctx.indexer); the receiver is named `indexer` so the
// bodies read exactly like the C file-static accesses.
type Indexer struct {
	mu    sync.Mutex
	cond  *sync.Cond
	roots []*indexerRoot // C: TAILQ roots
	init  bool
	fam   atomic.Pointer[fileaccesscore.FileAccessManager]
	store atomic.Pointer[htsmsg.Store]             // C: global htsmsg_store
	mm    atomic.Pointer[metadata.MetadataManager] // C: implicit default manager
	ts    atomic.Pointer[trace.TraceSystem]        // C: trace() global
	gconf atomic.Pointer[gconf.T]                  // C: gconf_t — injected
}

// NewIndexer creates the indexer instance — C: fa_indexer.c statics,
// owned by main.
func NewIndexer() *Indexer { return &Indexer{} }

// addroot — C: addroot (fa_indexer.c:286-295). Caller holds indexer.mu.
func (indexer *Indexer) addroot(url string) {
	ir := &indexerRoot{url: url, refcount: 1}
	indexer.roots = append(indexer.roots, ir)
}

// irRelease — C: ir_release (fa_indexer.c:272-279). Caller holds indexer.mu.
func irRelease(ir *indexerRoot) {
	ir.refcount--
}

// clearIndexStatus — C: clear_index_status (fa_indexer.c:301-323)
func (indexer *Indexer) clearIndexStatus(url string) {
	pfx := dbpkg.DBEscapePathQuery(url)
	db := indexer.mm.Load().Get()
	if db != nil {
		// C: UPDATE item SET indexstatus=0 WHERE url LIKE ?1 OR url = ?2
		stmt, rc := dbpkg.DBPrepare(db,
			"UPDATE item SET indexstatus = 0 WHERE url LIKE ?1 OR url = ?2")
		if rc == dbpkg.SQLITE_OK {
			stmt.BindText(1, pfx)
			stmt.BindText(2, url)
			dbpkg.DBStep(stmt)
			stmt.Finalize()
		}
	}
	indexer.mm.Load().Close(db)
}

// saveState — C: save_state (fa_indexer.c:329-346). Caller holds indexer.mu.
func (indexer *Indexer) saveState() {
	m := htsmsg.NewMap()
	r := htsmsg.NewList()

	for _, ir := range indexer.roots {
		u := htsmsg.NewMap()
		u.AddStr("url", ir.url)
		r.AddMsg("", u)
	}

	m.AddMsg("roots", r)
	if gs := indexer.store.Load(); gs != nil {
		gs.Save(m, "indexer")
	}
}

// FAIndexerEnabled — C: fa_indexer_enabled (fa_indexer.c:352-365)
func (indexer *Indexer) FAIndexerEnabled(url string) bool {
	if indexer == nil {
		return false
	}
	indexer.mu.Lock()
	defer indexer.mu.Unlock()
	rval := false
	for _, ir := range indexer.roots {
		if ir.url == url {
			rval = true
		}
	}
	return rval
}

// FAIndexerEnable — C: fa_indexer_enable (fa_indexer.c:371-398)
func (indexer *Indexer) FAIndexerEnable(url string, on bool) {
	if indexer == nil {
		return
	}
	indexer.mu.Lock()
	defer indexer.mu.Unlock()

	var ir *indexerRoot
	idx := -1
	for i, e := range indexer.roots {
		if e.url == url {
			ir = e
			idx = i
			break
		}
	}

	if on {
		if ir == nil {
			indexer.addroot(url)
			indexer.ts.Load().Trace(trace.TRACE_INFO, "Indexer",
				"Creating indexed root at %s", url)
			if indexer.cond != nil {
				indexer.cond.Signal()
			}
			indexer.saveState()
		}
	} else {
		if ir != nil {
			indexer.roots = slices.Delete(indexer.roots, idx, idx+1)
			irRelease(ir)
			indexer.ts.Load().Trace(trace.TRACE_INFO, "Indexer",
				"Removing indexed root at %s", url)
			indexer.clearIndexStatus(url)
			indexer.saveState()
		}
	}
}

// ---------------------------------------------------------------------------
// indexer_thread — C: indexer_thread (fa_indexer.c:404-448)
// ---------------------------------------------------------------------------

func (indexer *Indexer) indexerThread() {
	indexer.mu.Lock()
	for {
	restart:
		didSomething := 0
		for i := 0; i < len(indexer.roots); i++ {
			ir := indexer.roots[i]
			ir.refcount++

			doroot := false
			if !ir.rootScanned {
				ir.rootScanned = true
				doroot = true
			}

			indexer.mu.Unlock()

			if doroot {
				indexer.indexDirectory(ir.url)
				didSomething = 1
			} else {
				didSomething |= indexer.findUnprocessedDirectory(ir.url)
			}

			indexer.mu.Lock()
			rf := ir.refcount
			irRelease(ir)
			if rf == 1 {
				// C: ir was freed — the TAILQ_FOREACH next pointer is
				// invalid; restart the scan like C's break does
				break
			}
		}

		for _, ir := range indexer.roots {
			if !ir.rootScanned {
				goto restart
			}
		}
		if didSomething == 0 {
			indexer.cond.Wait()
		}
	}
}

// SetIndexerMetadataManager injects the metadata manager (C: the
// implicit default manager read lazily by metadb_* at index time).
func (indexer *Indexer) SetIndexerMetadataManager(mm *metadata.MetadataManager) {
	indexer.mm.Store(mm)
}

// SetIndexerTraceSystem injects the trace system (C: trace() global
// reached by TRACE/INDEXER_TRACE in fa_indexer.c).
func (indexer *Indexer) SetIndexerTraceSystem(ts *trace.TraceSystem) {
	indexer.ts.Store(ts)
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (indexer *Indexer) SetGconf(g *gconf.T) { indexer.gconf.Store(g) }

// FAIndexerStart — C: fa_indexer_init (fa_indexer.c:454-475)
// Loads persisted roots and spawns the indexer thread.
func (indexer *Indexer) FAIndexerStart(fam *fileaccesscore.FileAccessManager,
	storeDep any) {
	indexer.mu.Lock()
	if indexer.init {
		indexer.mu.Unlock()
		return
	}
	indexer.init = true
	indexer.cond = sync.NewCond(&indexer.mu)
	indexer.fam.Store(fam)
	if st, _ := storeDep.(*htsmsg.Store); st != nil {
		indexer.store.Store(st)
	}
	indexer.mu.Unlock()

	// C: htsmsg_store_load("indexer")
	if gs := indexer.store.Load(); gs != nil {
		if m, err := gs.Load("indexer"); err == nil && m != nil {
			if r := m.GetList("roots"); r != nil {
				for _, f := range r.GetFields() {
					// C: htsmsg_get_map_by_field(f)
					o := f.GetMap()
					if o == nil {
						continue
					}
					// C: htsmsg_get_str(o, "url") — non-NULL iff the
					// field exists and is HMF_STR or HMF_S64 (S64 is
					// stringified in place by htsmsg_field_get_string).
					uf := o.FieldFind("url")
					if uf == nil ||
						(uf.GetType() != htsmsg.HmfStr &&
							uf.GetType() != htsmsg.HmfS64) {
						continue
					}
					indexer.mu.Lock()
					indexer.addroot(o.GetStr("url"))
					indexer.mu.Unlock()
				}
			}
		}
	}

	// C: hts_thread_create_detached("indexer", indexer_thread, NULL,
	// THREAD_PRIO_METADATA_BG)
	go indexer.indexerThread()
}
