package metadata

// Port of src/metadata/metadb.c — 1:1 strict C-to-Go translation.
//
// C uses sqlite3*/sqlite3_stmt* + db_prepare/db_step; this port uses
// the raw cgo sqlite3 binding in pkg/db (db.DB = sqlite3*,
// db.Stmt = sqlite3_stmt*, db.DBStep = db_step).

import (
	"time"

	dbpkg "github.com/czz/movian-go/internal/db"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/notifications"
)

// rc2MetadataCode — C: rc2metadatacode (metadb.c:44-52).
func rc2MetadataCode(rc int) int {
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	if rc != dbpkg.SQLITE_OK && rc != dbpkg.SQLITE_DONE &&
		rc != dbpkg.SQLITE_ROW {
		return MetadataPermanentError
	}
	return 0
}

// dbBindRstr binds a string or NULL — C: db_bind_rstr /
// sqlite3_bind_text(..., NULL) binds a NULL value; an unbound
// parameter is also NULL, so empty string means "don't bind".
func dbBindStr(stmt *dbpkg.Stmt, i int, s string) {
	if s != "" {
		stmt.BindText(i, s)
	}
}

// dbExec runs a write statement: prepare, step, finalize.
// C pattern: rc = db_step(stmt); sqlite3_finalize(stmt).
func dbExec(db *dbpkg.DB, sql string, bind func(*dbpkg.Stmt)) int {
	stmt, rc := dbpkg.DBPrepare(db, sql)
	if rc != dbpkg.SQLITE_OK {
		return rc
	}
	if bind != nil {
		bind(stmt)
	}
	rc = dbpkg.DBStep(stmt)
	stmt.Finalize()
	return rc
}

// ItemsClear — C: items_clear (metadb.c:56-96), a settings action callback.
func (mm *MetadataManager) ItemsClear(opaque any, value any) {
	msg := "Clearing all metadata means losing <b>all</b> metadata cached from " +
		"external sources such as themoviedb.org, etc\n\n" +
		"Information about resume-points, playcount, etc will be saved.\n" +
		"Are you sure you proceed?"

	x := 0
	if mm.nm != nil {
		x = mm.nm.MessagePopup(msg,
			notifications.MessagePopupRichText|
				notifications.MessagePopupCancel|
				notifications.MessagePopupOK, nil)
	}
	if x != notifications.MessagePopupOK {
		return
	}

	db := mm.Get()
	if db == nil {
		return
	}
again:
	if dbpkg.DBBegin(db) != 0 {
		mm.Close(db)
		return
	}
	rc := dbExec(db, "DELETE FROM item", nil)
	if rc == dbpkg.SQLITE_LOCKED {
		dbpkg.DBRollbackDeadlock(db)
		goto again
	}
	deleted := db.Changes()
	dbpkg.DBCommit(db)
	mm.Close(db)

	if mm.nm != nil {
		mm.nm.NotifyAdd(nil, notifications.NotifyInfo, "", 3,
			"%d items deleted", deleted)
	}
}

// dbItemGet — C: db_item_get (metadb.c:170-198)
func dbItemGet(db *dbpkg.DB, url string, mtimep *int64) int64 {
	stmt, rc := dbpkg.DBPrepare(db,
		"SELECT id,mtime from item where url=?1")
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	stmt.BindText(1, url)

	rc = dbpkg.DBStep(stmt)
	if rc == dbpkg.SQLITE_ROW {
		id := stmt.ColumnInt64(0)
		if mtimep != nil && stmt.ColumnType(1) == dbpkg.SQLITE_INTEGER {
			*mtimep = stmt.ColumnInt64(1)
		}
		stmt.Finalize()
		return id
	}
	stmt.Finalize()
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	return MetadataPermanentError
}

// dbItemCreate — C: db_item_create (metadb.c:200-235)
func dbItemCreate(db *dbpkg.DB, url string, contenttype int, mtime int64,
	parentid int64, indexstatus MetadataIndexStatus) int64 {
	rc := dbExec(db, `INSERT INTO item
		(url, contenttype, mtime, parent, indexstatus)
		VALUES (?1, ?2, ?3, ?4, ?5)`, func(stmt *dbpkg.Stmt) {
		stmt.BindText(1, url)
		if contenttype != 0 {
			stmt.BindInt(2, contenttype)
		}
		if mtime != 0 {
			stmt.BindInt64(3, mtime)
		}
		if parentid > 0 {
			stmt.BindInt64(4, parentid)
		}
		stmt.BindInt(5, int(indexstatus))
	})
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	if rc != dbpkg.SQLITE_DONE {
		return MetadataPermanentError
	}
	return db.LastInsertRowid()
}

// getCache — C: get_cache_t (metadb.c:1309-1316)
type getCache struct {
	albumID     int64
	albumTitle  string
	artistID    int64
	artistTitle string
}

// metadataGet — C: metadata_get (metadb.c:2134-2176)
func (mm *MetadataManager) metadataGet(db *dbpkg.DB, itemID int64,
	contenttype int, gc *getCache) *Metadata {
	md := Create()
	md.ContentType = ContentType(contenttype)

	var r int
	switch md.ContentType {
	case ContentAudio:
		r = metadbMetadataGetAudio(db, md, itemID, gc)
	case ContentVideo:
		viID := metadbMetadataGetVideo(db, md, itemID, 1)
		if viID == -1 {
			r = 1
			break
		}
		r = metadbMetadataGetStreams(db, md, viID)
	case ContentImage:
		r = metadbMetadataGetImage(db, md, itemID)
	case ContentDir, ContentShare, ContentDVD:
		r = 0
	default:
		r = 1
	}
	if r != 0 {
		md.Destroy()
		return nil
	}
	return md
}

// MetadbMetadataGet — C: metadb_metadata_get (metadb.c:2182-2229)
func (mm *MetadataManager) MetadbMetadataGet(db *dbpkg.DB, url string,
	mtime int64) *Metadata {
	if db == nil {
		return nil
	}

	if dbpkg.DBBegin(db) != 0 {
		return nil
	}
	sel, rc := dbpkg.DBPrepare(db, `SELECT id,contenttype,parent from item
		where url=?1 AND mtime=?2`)
	if rc != dbpkg.SQLITE_OK {
		dbpkg.DBRollback(db)
		return nil
	}
	sel.BindText(1, url)
	sel.BindInt64(2, mtime)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		sel.Finalize()
		dbpkg.DBRollback(db)
		return nil
	}
	itemID := sel.ColumnInt64(0)
	contenttype := sel.ColumnInt(1)
	parent := sel.ColumnInt(2)
	sel.Finalize()

	gc := &getCache{}
	md := mm.metadataGet(db, itemID, contenttype, gc)

	if md != nil {
		if parent != 0 {
			md.CacheStatus = MetadataCacheStatusFull
		} else {
			md.CacheStatus = MetadataCacheStatusUnparented
		}
	}
	dbpkg.DBRollback(db)
	return md
}

// MetadbMetadataScandir — C: metadb_metadata_scandir (metadb.c:2235-2312)
func (mm *MetadataManager) MetadbMetadataScandir(db *dbpkg.DB, url string,
	mtime *int64) *fileaccesscore.Dir {
	if db == nil {
		return nil
	}

again:
	if dbpkg.DBBegin(db) != 0 {
		return nil
	}
	var pm int64
	parentID := dbItemGet(db, url, &pm)
	if parentID == MetadataDeadlock {
		dbpkg.DBRollbackDeadlock(db)
		goto again
	}
	if parentID < 0 {
		dbpkg.DBRollback(db)
		return nil
	}
	if mtime != nil {
		*mtime = pm
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT id, url, contenttype, mtime, indexstatus
		FROM item WHERE parent = ?1`)
	if rc != dbpkg.SQLITE_OK {
		dbpkg.DBRollback(db)
		return nil
	}
	sel.BindInt64(1, parentID)

	fd := fileaccesscore.DirAlloc()
	gc := &getCache{}

	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		if sel.ColumnType(2) != dbpkg.SQLITE_INTEGER {
			continue
		}
		itemID := sel.ColumnInt64(0)
		u := sel.ColumnText(1)
		contenttype := sel.ColumnInt(2)
		mtimeVal := sel.ColumnInt64(3)
		is := sel.ColumnInt(4)
		fname := mm.fam.URLGetLastComponent(u)

		fde := fileaccesscore.DirAdd(fd, u, fname, contenttype)
		if fde != nil {
			if sel.ColumnType(3) == dbpkg.SQLITE_INTEGER {
				fde.StatDone = true
				fde.Stat.MTime = time.Unix(mtimeVal, 0)
			}
			if mdm := mm.metadataGet(db, itemID, contenttype, gc); mdm != nil {
				mdm.CacheStatus = MetadataCacheStatusFull
				mdm.IndexStatus = MetadataIndexStatus(is)
				fde.Md = mdm
			}
		}
	}
	sel.Finalize()
	dbpkg.DBRollback(db)

	if fd.Count == 0 {
		fileaccesscore.DirFree(fd)
		return nil
	}
	return fd
}

// MetadbUnparentItem — C: metadb_unparent_item (metadb.c:2321-2349)
func (mm *MetadataManager) MetadbUnparentItem(db *dbpkg.DB, url string) {
	if db == nil {
		return
	}

again:
	if dbpkg.DBBegin(db) != 0 {
		return
	}
	rc := dbExec(db, "UPDATE item SET parent = NULL WHERE url=?1",
		func(stmt *dbpkg.Stmt) {
			stmt.BindText(1, url)
		})
	if rc == dbpkg.SQLITE_LOCKED {
		dbpkg.DBRollbackDeadlock(db)
		goto again
	}
	if rc != dbpkg.SQLITE_DONE {
		dbpkg.DBRollback(db)
		return
	}
	dbpkg.DBCommit(db)
}

// MetadbParentItem — C: metadb_parent_item (metadb.c:2355-2389)
func (mm *MetadataManager) MetadbParentItem(db *dbpkg.DB, url, parentURL string) {
	if db == nil {
		return
	}

again:
	if dbpkg.DBBegin(db) != 0 {
		return
	}
	parentID := dbItemGet(db, parentURL, nil)
	if parentID == MetadataDeadlock {
		dbpkg.DBRollbackDeadlock(db)
		goto again
	}
	rc := dbExec(db, "UPDATE item SET parent = ?2 WHERE url=?1",
		func(stmt *dbpkg.Stmt) {
			stmt.BindText(1, url)
			stmt.BindInt64(2, parentID)
		})
	if rc == dbpkg.SQLITE_LOCKED {
		dbpkg.DBRollbackDeadlock(db)
		goto again
	}
	if rc != dbpkg.SQLITE_DONE {
		dbpkg.DBRollback(db)
		return
	}
	dbpkg.DBCommit(db)
}

// MetadataGetVideoData — C: metadata_get_video_data (metadb.c:2396-2407)
func (mm *MetadataManager) MetadataGetVideoData(url string) *Metadata {
	db := mm.Get()
	if db == nil {
		return nil
	}
	var md *Metadata
	r := mm.MetadbGetVideoinfo(db, url, nil, nil, &md, 0)
	mm.Close(db)
	if r != 0 {
		return nil
	}
	return md
}
