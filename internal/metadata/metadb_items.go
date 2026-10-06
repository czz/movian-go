package metadata

// Port of src/metadata/metadb.c — 1:1 strict C-to-Go translation.
//
// C uses sqlite3*/sqlite3_stmt* + db_prepare/db_step; this port uses
// the raw cgo sqlite3 binding in pkg/db (db.DB = sqlite3*,
// db.Stmt = sqlite3_stmt*, db.DBStep = db_step).

import (
	"fmt"
	"strings"
	"time"

	dbpkg "github.com/czz/movian-go/internal/db"
	mediacore "github.com/czz/movian-go/internal/media/core"
)

// MetadbArtistGetByTitle — C: metadb_artist_get_by_title (metadb.c:241-297)
func (mm *MetadataManager) MetadbArtistGetByTitle(db *dbpkg.DB, title string,
	dsID int, extID string) int64 {
	if db == nil {
		return MetadataPermanentError
	}

	stmt, rc := dbpkg.DBPrepare(db, `SELECT id FROM artist
		WHERE title=?1 AND ds_id=?2 AND (?3 OR ext_id = ?4)`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	stmt.BindText(1, title)
	stmt.BindInt(2, dsID)
	if extID == "" {
		stmt.BindInt(3, 1)
	} else {
		stmt.BindInt(3, 0)
		stmt.BindText(4, extID)
	}

	var rval int64 = MetadataPermanentError
	rc = dbpkg.DBStep(stmt)
	if rc == dbpkg.SQLITE_ROW {
		rval = stmt.ColumnInt64(0)
		stmt.Finalize()
		return rval
	}
	stmt.Finalize()
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	// SQLITE_DONE — insert
	rc = dbExec(db, `INSERT INTO artist (title, ds_id, ext_id)
		VALUES (?1, ?2, ?3)`, func(stmt *dbpkg.Stmt) {
		stmt.BindText(1, title)
		stmt.BindInt(2, dsID)
		dbBindStr(stmt, 3, extID)
	})
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	if rc == dbpkg.SQLITE_DONE {
		rval = db.LastInsertRowid()
	}
	return rval
}

// MetadbAlbumGetByTitle — C: metadb_album_get_by_title (metadb.c:303-364)
func (mm *MetadataManager) MetadbAlbumGetByTitle(db *dbpkg.DB, album string,
	artistID int64, dsID int, extID string) int64 {
	if db == nil {
		return MetadataPermanentError
	}

	stmt, rc := dbpkg.DBPrepare(db, `SELECT id FROM album
		WHERE title=?1 AND artist_id IS ?2 AND ds_id = ?3
		AND (?4 OR ext_id = ?5)`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	stmt.BindText(1, album)
	if artistID != -1 {
		stmt.BindInt64(2, artistID)
	}
	stmt.BindInt(3, dsID)
	if extID == "" {
		stmt.BindInt(4, 1)
	} else {
		stmt.BindInt(4, 0)
		stmt.BindText(5, extID)
	}

	var rval int64 = MetadataPermanentError
	rc = dbpkg.DBStep(stmt)
	if rc == dbpkg.SQLITE_ROW {
		rval = stmt.ColumnInt64(0)
		stmt.Finalize()
		return rval
	}
	stmt.Finalize()
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	rc = dbExec(db, `INSERT INTO album (title, ds_id, artist_id, ext_id)
		VALUES (?1, ?3, ?2, ?4)`, func(stmt *dbpkg.Stmt) {
		stmt.BindText(1, album)
		if artistID != -1 {
			stmt.BindInt64(2, artistID)
		}
		stmt.BindInt(3, dsID)
		dbBindStr(stmt, 4, extID)
	})
	if rc == dbpkg.SQLITE_DONE {
		rval = db.LastInsertRowid()
	}
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	return rval
}

// MetadbInsertAlbumart — C: metadb_insert_albumart (metadb.c:371-393)
func (mm *MetadataManager) MetadbInsertAlbumart(db *dbpkg.DB, albumID int64,
	url string, width, height int) {
	if db == nil {
		return
	}

	dbExec(db, `INSERT INTO albumart (album_id, url, width, height)
		VALUES (?1, ?2, ?3, ?4)`, func(stmt *dbpkg.Stmt) {
		stmt.BindInt64(1, albumID)
		stmt.BindText(2, url)
		if width != 0 {
			stmt.BindInt(3, width)
		}
		if height != 0 {
			stmt.BindInt(4, height)
		}
	})
}

// MetadbInsertArtistpic — C: metadb_insert_artistpic (metadb.c:399-421)
func (mm *MetadataManager) MetadbInsertArtistpic(db *dbpkg.DB, artistID int64,
	url string, width, height int) {
	if db == nil {
		return
	}

	dbExec(db, `INSERT INTO artistpic (artist_id, url, width, height)
		VALUES (?1, ?2, ?3, ?4)`, func(stmt *dbpkg.Stmt) {
		stmt.BindInt64(1, artistID)
		stmt.BindText(2, url)
		if width != 0 {
			stmt.BindInt(3, width)
		}
		if height != 0 {
			stmt.BindInt(4, height)
		}
	})
}

// metadbInsertAudioitem — C: metadb_insert_audioitem (metadb.c:573-641)
func (mm *MetadataManager) metadbInsertAudioitem(db *dbpkg.DB, itemID int64,
	md *Metadata, dsID int) int {
	if db == nil {
		return MetadataPermanentError
	}

	var artistID int64 = -1
	var albumID int64 = -1

	if md.Artist != "" {
		artistID = mm.MetadbArtistGetByTitle(db, md.Artist, dsID, "")
		if artistID < 0 {
			return int(artistID)
		}
	}
	if md.Album != "" {
		albumID = mm.MetadbAlbumGetByTitle(db, md.Album, artistID, dsID, "")
		if albumID < 0 {
			return int(albumID)
		}
	}

	var rc int
	for i := range 2 {
		var q string
		if i == 0 {
			q = `INSERT OR FAIL INTO audioitem
				(item_id, title, album_id, artist_id, duration, ds_id, track)
				VALUES (?1, ?2, ?3, ?4, ?5, 1, ?6)`
		} else {
			q = `UPDATE audioitem SET title = ?2, album_id = ?3,
				artist_id = ?4, duration = ?5
				WHERE item_id = ?1 AND ds_id = 1`
		}
		rc = dbExec(db, q, func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, itemID)
			dbBindStr(stmt, 2, md.Title)
			if albumID != -1 {
				stmt.BindInt64(3, albumID)
			}
			if artistID != -1 {
				stmt.BindInt64(4, artistID)
			}
			stmt.BindInt(5, int(md.Duration*1000))
			stmt.BindInt(6, int(md.Track))
		})
		if rc == dbpkg.SQLITE_CONSTRAINT && i == 0 {
			continue
		}
		break
	}
	return rc2MetadataCode(rc)
}

// metadbConstructImageset — C: metadb_construct_imageset (metadb.c:647-670)
// Serializes rows (url, width, height) into "imageset:" JSON.
func metadbConstructImageset(sel *dbpkg.Stmt, urlcol, wcol, hcol int) string {
	var b strings.Builder
	b.WriteString("imageset:[")
	first := true
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		url := sel.ColumnText(urlcol)
		w := sel.ColumnInt(wcol)
		h := sel.ColumnInt(hcol)
		if !first {
			b.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&b, `{"url":%q`, url)
		if w > 0 {
			fmt.Fprintf(&b, `,"width":%d`, w)
		}
		if h > 0 {
			fmt.Fprintf(&b, `,"height":%d`, h)
		}
		b.WriteByte('}')
	}
	b.WriteByte(']')
	return b.String()
}

// MetadbGetAlbumArt — C: metadb_get_album_art (metadb.c:677-699)
func (mm *MetadataManager) MetadbGetAlbumArt(db *dbpkg.DB, album, artist string) string {
	if db == nil {
		return ""
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT aa.url, aa.width, aa.height
		FROM artist,album,albumart AS aa
		WHERE artist.title=?1 AND album.title=?2
		AND album.artist_id = artist.id AND aa.album_id = album.id`)
	if rc != dbpkg.SQLITE_OK {
		return ""
	}
	sel.BindText(1, artist)
	sel.BindText(2, album)
	r := metadbConstructImageset(sel, 0, 1, 2)
	sel.Finalize()
	return r
}

// MetadbGetArtistPics — C: metadb_get_artist_pics (metadb.c:824-852)
func (mm *MetadataManager) MetadbGetArtistPics(db *dbpkg.DB, artist string,
	cb func(opaque any, url string, width, height int),
	opaque any) int {
	if db == nil {
		return MetadataPermanentError
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT ap.url, ap.width, ap.height
		FROM artist,artistpic AS ap
		WHERE artist.title=?1 AND ap.artist_id = artist.id`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindText(1, artist)
	rval := MetadataPermanentError
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		cb(opaque, sel.ColumnText(0), sel.ColumnInt(1), sel.ColumnInt(2))
		rval = 0
	}
	sel.Finalize()
	return rval
}

// metadbInsertStream — C: metadb_insert_stream (metadb.c:861-903)
func (mm *MetadataManager) metadbInsertStream(db *dbpkg.DB, videoitemID int64,
	ms *MetadataStream) int {
	if db == nil {
		return MetadataPermanentError
	}

	var media string
	switch ms.Type {
	case int(mediacore.MediaTypeVideo):
		media = "video"
	case int(mediacore.MediaTypeAudio):
		media = "audio"
	case int(mediacore.MediaTypeSubtitle):
		media = "subtitle"
	default:
		return 0
	}
	rc := dbExec(db, `INSERT INTO videostream
		(videoitem_id, streamindex, info, isolang,
		codec, mediatype, disposition, title)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, videoitemID)
			stmt.BindInt(2, ms.StreamIndex)
			dbBindStr(stmt, 3, ms.Info)
			dbBindStr(stmt, 4, ms.ISOLang)
			dbBindStr(stmt, 5, ms.Codec)
			stmt.BindText(6, media)
			stmt.BindInt(7, ms.Disposition)
			dbBindStr(stmt, 8, ms.Title)
		})
	return rc2MetadataCode(rc)
}

// metadbSetStreams — C: metadb_set_streams (metadb.c:909-936)
func (mm *MetadataManager) metadbSetStreams(db *dbpkg.DB, videoitemID int64,
	md *Metadata) int {
	if db == nil {
		return MetadataPermanentError
	}

	rc := dbExec(db, "DELETE FROM videostream WHERE videoitem_id = ?1",
		func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, videoitemID)
		})
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	if rc != dbpkg.SQLITE_DONE {
		return MetadataPermanentError
	}
	for i := range md.Streams {
		if r := mm.metadbInsertStream(db, videoitemID, &md.Streams[i]); r < 0 {
			return r
		}
	}
	return 0
}

// metadbMetadataWritex — C: metadb_metadata_writex (metadb.c:1168-1264)
func (mm *MetadataManager) metadbMetadataWritex(db *dbpkg.DB, url string,
	mtime int64, md *Metadata, parent string, parentMtime int64,
	indexstatus MetadataIndexStatus) int {
	if db == nil {
		return MetadataPermanentError
	}

	var parentID int64
	if parent != "" {
		parentID = dbItemGet(db, parent, nil)
		if parentID == MetadataDeadlock {
			return MetadataDeadlock
		}
		if parentID == MetadataPermanentError {
			parentID = dbItemCreate(db, parent, int(ContentDir),
				parentMtime, 0, 0)
		}
		if parentID == MetadataDeadlock {
			return MetadataDeadlock
		}
	}

	itemID := dbItemGet(db, url, nil)
	if itemID == MetadataDeadlock {
		return MetadataDeadlock
	}

	if itemID < 0 {
		itemID = dbItemCreate(db, url, int(md.ContentType), mtime,
			parentID, indexstatus)
		if itemID == MetadataDeadlock {
			return MetadataDeadlock
		}
		if itemID == -1 {
			return MetadataTemporaryError
		}
	} else {
		// C builds UPDATE with only non-empty fields (metadb.c:1208-1241)
		sets := ""
		argn := 1
		if md.ContentType != 0 {
			argn++
			sets += fmt.Sprintf("contenttype=?%d,", argn)
		}
		if mtime != 0 {
			argn++
			sets += fmt.Sprintf("mtime=?%d,", argn)
		}
		if parentID != 0 {
			argn++
			sets += fmt.Sprintf("parent=?%d,", argn)
		}
		if indexstatus != IndexStatusNoChange {
			argn++
			sets += fmt.Sprintf("indexstatus=?%d,", argn)
		}
		if strings.HasSuffix(sets, ",") {
			sets = sets[:len(sets)-1] + " "
			n := 1
			rc := dbExec(db, "UPDATE item SET "+sets+" WHERE id=?1",
				func(stmt *dbpkg.Stmt) {
					stmt.BindInt64(1, itemID)
					if md.ContentType != 0 {
						n++
						stmt.BindInt(n, int(md.ContentType))
					}
					if mtime != 0 {
						n++
						stmt.BindInt64(n, mtime)
					}
					if parentID != 0 {
						n++
						stmt.BindInt64(n, parentID)
					}
					if indexstatus != IndexStatusNoChange {
						n++
						stmt.BindInt(n, int(indexstatus))
					}
				})
			if rc == dbpkg.SQLITE_LOCKED {
				return MetadataDeadlock
			}
		}
	}

	var r int
	switch md.ContentType {
	case ContentAudio:
		r = mm.metadbInsertAudioitem(db, itemID, md, 1)
	case ContentVideo:
		if mm.metadbInsertVideoitem0(db, itemID, 1, "", md, 3, 0, 0, 0) < 0 {
			r = 1
		}
	case ContentImage:
		r = mm.metadbInsertImageitem(db, itemID, md)
	default:
		return 0
	}
	return r
}

// MetadbMetadataWrite — C: metadb_metadata_write (metadb.c:1271-1306)
func (mm *MetadataManager) MetadbMetadataWrite(db *dbpkg.DB, url string,
	mtime int64, md *Metadata, parent string, parentMtime int64,
	indexstatus MetadataIndexStatus) {
	if db == nil {
		return
	}

	switch md.ContentType {
	case ContentAudio, ContentVideo, ContentImage,
		ContentDir, ContentDVD, ContentShare:
	default:
		return
	}
	for {
		if dbpkg.DBBegin(db) != 0 {
			return
		}
		r := mm.metadbMetadataWritex(db, url, mtime, md, parent,
			parentMtime, indexstatus)
		if r == MetadataDeadlock {
			dbpkg.DBRollbackDeadlock(db)
			continue
		}
		if r != 0 {
			dbpkg.DBRollback(db)
		} else {
			dbpkg.DBCommit(db)
		}
		return
	}
}

// metadbMetadataGetArtist — C: metadb_metadata_get_artist (metadb.c:1333-1369)
func metadbMetadataGetArtist(db *dbpkg.DB, gc *getCache, id int64) int {
	if id < 1 {
		return MetadataPermanentError
	}
	if id == gc.artistID {
		return 0
	}
	sel, rc := dbpkg.DBPrepare(db,
		`SELECT title FROM artist WHERE id = ?1 AND ds_id=1`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, id)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		sel.Finalize()
		return MetadataPermanentError
	}
	gc.artistID = id
	gc.artistTitle = sel.ColumnText(0)
	sel.Finalize()
	return 0
}

// metadbMetadataGetAlbum — C: metadb_metadata_get_album (metadb.c:1375-1409)
func metadbMetadataGetAlbum(db *dbpkg.DB, gc *getCache, id int64) int {
	if id < 1 {
		return MetadataPermanentError
	}
	if id == gc.albumID {
		return 0
	}
	sel, rc := dbpkg.DBPrepare(db,
		`SELECT title FROM album WHERE id = ?1 AND ds_id=1`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, id)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		sel.Finalize()
		return MetadataPermanentError
	}
	gc.albumID = id
	gc.albumTitle = sel.ColumnText(0)
	sel.Finalize()
	return 0
}

// metadbMetadataGetAudio — C: metadb_metadata_get_audio (metadb.c:1415-1453)
func metadbMetadataGetAudio(db *dbpkg.DB, md *Metadata, itemID int64,
	gc *getCache) int {
	sel, rc := dbpkg.DBPrepare(db, `SELECT title, album_id, artist_id, duration, track
		FROM audioitem WHERE item_id = ?1 AND ds_id = 1`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, itemID)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		sel.Finalize()
		return MetadataPermanentError
	}
	title := sel.ColumnText(0)
	var albumID, artistID, duration, track int64
	if sel.ColumnType(1) == dbpkg.SQLITE_INTEGER {
		albumID = sel.ColumnInt64(1)
	}
	if sel.ColumnType(2) == dbpkg.SQLITE_INTEGER {
		artistID = sel.ColumnInt64(2)
	}
	duration = sel.ColumnInt64(3)
	track = sel.ColumnInt64(4)
	sel.Finalize()

	md.Title = title
	if metadbMetadataGetAlbum(db, gc, albumID) == 0 {
		md.Album = gc.albumTitle
	}
	if metadbMetadataGetArtist(db, gc, artistID) == 0 {
		md.Artist = gc.artistTitle
	}
	md.Duration = float32(duration) / 1000.0
	md.Track = int16(track)
	return 0
}

// metadbMetadataGetStreams — C: metadb_metadata_get_streams (metadb.c:2043-2093)
func metadbMetadataGetStreams(db *dbpkg.DB, md *Metadata, videoitemID int64) int {
	sel, rc := dbpkg.DBPrepare(db, `SELECT streamindex, info, isolang, codec,
		mediatype, disposition, title
		FROM videostream WHERE videoitem_id = ?1 ORDER BY streamindex`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, videoitemID)
	atrack, strack, vtrack := 0, 0, 0
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		var typ, tn int
		switch sel.ColumnText(4) {
		case "audio":
			typ = int(mediacore.MediaTypeAudio)
			atrack++
			tn = atrack
		case "video":
			typ = int(mediacore.MediaTypeVideo)
			vtrack++
			tn = vtrack
		case "subtitle":
			typ = int(mediacore.MediaTypeSubtitle)
			strack++
			tn = strack
		default:
			continue
		}
		md.AddStream(sel.ColumnText(3), typ, sel.ColumnInt(0),
			sel.ColumnText(6), sel.ColumnText(1), sel.ColumnText(2),
			sel.ColumnInt(5), tn, -1)
	}
	sel.Finalize()
	return 0
}

// metadbMetadataGetImage — C: metadb_metadata_get_image (metadb.c:2099-2128)
func metadbMetadataGetImage(db *dbpkg.DB, md *Metadata, itemID int64) int {
	sel, rc := dbpkg.DBPrepare(db, `SELECT original_time, manufacturer, equipment
		FROM imageitem WHERE item_id = ?1`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, itemID)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		sel.Finalize()
		return MetadataPermanentError
	}
	if sel.ColumnType(0) == dbpkg.SQLITE_INTEGER {
		md.Time = time.Unix(sel.ColumnInt64(0), 0)
	}
	md.Manufacturer = sel.ColumnText(1)
	md.Equipment = sel.ColumnText(2)
	sel.Finalize()
	return 0
}
