package metadata

// Port of src/metadata/metadb.c — 1:1 strict C-to-Go translation.
//
// C uses sqlite3*/sqlite3_stmt* + db_prepare/db_step; this port uses
// the raw cgo sqlite3 binding in pkg/db (db.DB = sqlite3*,
// db.Stmt = sqlite3_stmt*, db.DBStep = db_step).

import (
	"fmt"
	"strconv"
	"strings"

	dbpkg "github.com/czz/movian-go/internal/db"
	propcore "github.com/czz/movian-go/internal/prop"
)

// MetadbInsertVideoart — C: metadb_insert_videoart (metadb.c:426-455)
func (mm *MetadataManager) MetadbInsertVideoart(db *dbpkg.DB, videoitemID int64,
	url string, imageType MetadataImageType, width, height int,
	weight int, group string, titled int) {
	if db == nil {
		return
	}

	dbExec(db, `INSERT OR REPLACE INTO videoart
		(videoitem_id, url, width, height, type, weight, grp, titled)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
		func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, videoitemID)
			stmt.BindText(2, url)
			if width != 0 {
				stmt.BindInt(3, width)
			}
			if height != 0 {
				stmt.BindInt(4, height)
			}
			stmt.BindInt(5, int(imageType))
			if weight != 0 {
				stmt.BindInt(6, weight)
			}
			dbBindStr(stmt, 7, group)
			stmt.BindInt(8, titled)
		})
}

// MetadbDeleteVideoart — C: metadb_delete_videoart (metadb.c:461-476)
func (mm *MetadataManager) MetadbDeleteVideoart(db *dbpkg.DB, videoitemID int64) {
	if db == nil {
		return
	}

	dbExec(db, "DELETE FROM videoart WHERE videoitem_id = ?1",
		func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, videoitemID)
		})
}

// MetadbInsertVideocast — C: metadb_insert_videocast (metadb.c:483-520)
func (mm *MetadataManager) MetadbInsertVideocast(db *dbpkg.DB, videoitemID int64,
	name, character, department, job string, order int,
	image string, width, height int, extID string) {
	if db == nil {
		return
	}

	dbExec(db, `INSERT OR REPLACE INTO videocast
		(videoitem_id, name, character, department, job,
		"order", image, width, height, ext_id)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)`,
		func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, videoitemID)
			dbBindStr(stmt, 2, name)
			dbBindStr(stmt, 3, character)
			dbBindStr(stmt, 4, department)
			dbBindStr(stmt, 5, job)
			stmt.BindInt(6, order)
			dbBindStr(stmt, 7, image)
			if width != 0 {
				stmt.BindInt(8, width)
			}
			if height != 0 {
				stmt.BindInt(9, height)
			}
			dbBindStr(stmt, 10, extID)
		})
}

// MetadbDeleteVideocast — C: metadb_delete_videocast (metadb.c:526-541)
func (mm *MetadataManager) MetadbDeleteVideocast(db *dbpkg.DB, videoitemID int64) {
	if db == nil {
		return
	}

	dbExec(db, "DELETE FROM videocast WHERE videoitem_id = ?1",
		func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, videoitemID)
		})
}

// MetadbInsertVideogenre — C: metadb_insert_videogenre (metadb.c:547-566)
func (mm *MetadataManager) MetadbInsertVideogenre(db *dbpkg.DB, videoitemID int64,
	title string) {
	if db == nil {
		return
	}

	dbExec(db, `INSERT OR REPLACE INTO videogenre (videoitem_id, title)
		VALUES (?1, ?2)`, func(stmt *dbpkg.Stmt) {
		stmt.BindInt64(1, videoitemID)
		dbBindStr(stmt, 2, title)
	})
}

// metadbGetVideoArt — C: metadb_get_video_art (metadb.c:705-733)
func (mm *MetadataManager) metadbGetVideoArt(db *dbpkg.DB, videoitemID int64,
	imageType int) []string {
	if db == nil {
		return nil
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT url FROM videoart
		WHERE videoitem_id=?1 AND type=?2 ORDER BY weight DESC`)
	if rc != dbpkg.SQLITE_OK {
		return nil
	}
	sel.BindInt64(1, videoitemID)
	sel.BindInt(2, imageType)

	var rv []string
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		rv = append(rv, sel.ColumnText(0))
	}
	sel.Finalize()
	return rv
}

// metadbConstructList — C: metadb_construct_list (metadb.c:739-754)
// buf is 512 bytes; snprintf truncates — replicate the cap.
func metadbConstructList(sel *dbpkg.Stmt, col int) string {
	var b strings.Builder
	cnt := 0
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		if sel.ColumnType(col) != dbpkg.SQLITE_TEXT {
			continue
		}
		s := sel.ColumnText(col)
		sep := ""
		if cnt > 0 {
			sep = ", "
		}
		// C: snprintf(buf+cnt, 512-cnt, ...) truncates at 511 bytes
		n := len(sep) + len(s)
		if cnt+n > 511 {
			n = 511 - cnt
			if n <= 0 {
				break
			}
			b.WriteString((sep + s)[:n])
			cnt += n
			break
		}
		b.WriteString(sep)
		b.WriteString(s)
		cnt += n
	}
	return b.String()
}

// metadbGetVideoGenre — C: metadb_get_video_genre (metadb.c:760-778)
func (mm *MetadataManager) metadbGetVideoGenre(db *dbpkg.DB, videoitemID int64) string {
	if db == nil {
		return ""
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT title FROM videogenre
		WHERE videoitem_id = ?1`)
	if rc != dbpkg.SQLITE_OK {
		return ""
	}
	sel.BindInt64(1, videoitemID)
	r := metadbConstructList(sel, 0)
	sel.Finalize()
	return r
}

// metadbGetVideoCast — C: metadb_get_video_cast (metadb.c:784-816)
func (mm *MetadataManager) metadbGetVideoCast(db *dbpkg.DB, videoitemID int64,
	md *Metadata) int {
	if db == nil {
		return MetadataPermanentError
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT name,character,department,job,image
		FROM videocast WHERE videoitem_id = ?1 ORDER BY "order"`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, videoitemID)
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		mp := MetadataPerson{
			Name:       sel.ColumnText(0),
			Character:  sel.ColumnText(1),
			Department: sel.ColumnText(2),
			Job:        sel.ColumnText(3),
			Portrait:   sel.ColumnText(4),
		}
		if sel.ColumnType(2) == dbpkg.SQLITE_TEXT &&
			sel.ColumnText(2) == "Cast" {
			md.Cast = append(md.Cast, mp)
		} else {
			md.Crew = append(md.Crew, mp)
		}
	}
	sel.Finalize()
	return 0
}

// metadbInsertVideoitem0 — C: metadb_insert_videoitem0 (metadb.c:943-1092)
func (mm *MetadataManager) metadbInsertVideoitem0(db *dbpkg.DB, itemID int64,
	dsID int, extID string, md *Metadata, status int, weight int64,
	qtype int, cfgid int64) int64 {
	if db == nil {
		return MetadataPermanentError
	}

	var id int64 = MetadataPermanentError
	var rc int

	for i := range 2 {
		if i == 1 {
			sel, qrc := dbpkg.DBPrepare(db, `SELECT id FROM videoitem
				WHERE (?5 OR item_id = ?1) AND ds_id = ?2
				AND (?3 OR ext_id = ?4)`)
			if qrc != dbpkg.SQLITE_OK {
				return MetadataPermanentError
			}
			sel.BindInt64(1, itemID)
			sel.BindInt(2, dsID)
			if extID == "" {
				sel.BindInt(3, 1)
			} else {
				sel.BindInt(3, 0)
				sel.BindText(4, extID)
			}
			if itemID == 0 {
				sel.BindInt(5, 1)
			} else {
				sel.BindInt(5, 0)
			}
			qrc = dbpkg.DBStep(sel)
			if qrc != dbpkg.SQLITE_ROW {
				sel.Finalize()
				if qrc == dbpkg.SQLITE_LOCKED {
					return MetadataDeadlock
				}
				return MetadataPermanentError
			}
			id = sel.ColumnInt64(0)
			sel.Finalize()
		}

		var q string
		if i == 0 {
			q = `INSERT OR FAIL INTO videoitem
				(item_id, ds_id, ext_id,
				title, duration, format, type, tagline, description,
				year, rating, rate_count, imdb_id, status, weight,
				querytype, cfgid, parent_id, idx)
				VALUES (?1, ?2, ?4,
				?5, ?6, ?7, ?8, ?9, ?10,
				?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?20)`
		} else {
			q = `UPDATE videoitem SET
				title = ?5, duration = ?6, format = ?7, type = ?8,
				tagline = ?9, description = ?10, year = ?11,
				rating = ?12, rate_count = ?13, imdb_id = ?14,
				status = ?15, cfgid = ?18, parent_id = ?19, idx = ?20
				WHERE id = ?3`
		}

		rc = dbExec(db, q, func(stmt *dbpkg.Stmt) {
			// Keys
			if itemID != 0 {
				stmt.BindInt64(1, itemID)
			}
			stmt.BindInt(2, dsID)
			if i == 1 {
				stmt.BindInt64(3, id)
			}
			dbBindStr(stmt, 4, extID)

			// Data
			mtype := 0
			if md != nil {
				dbBindStr(stmt, 5, md.Title)
				if md.Duration != 0 {
					stmt.BindInt(6, int(md.Duration*1000))
				}
				dbBindStr(stmt, 7, md.Format)
				mtype = int(md.Type)
				dbBindStr(stmt, 9, md.Tagline)
				dbBindStr(stmt, 10, md.Description)
				if md.Year > 1900 {
					stmt.BindInt(11, int(md.Year))
				}
				if md.Rating >= 0 {
					stmt.BindInt(12, int(md.Rating))
				}
				if md.RatingCount >= 0 {
					stmt.BindInt(13, md.RatingCount)
				}
				dbBindStr(stmt, 14, md.IMDBID)
				if md.Idx >= 0 {
					stmt.BindInt(20, int(md.Idx))
				}
				if md.ParentID != 0 {
					stmt.BindInt64(19, md.ParentID)
				}
			}
			stmt.BindInt(8, mtype)
			stmt.BindInt(15, status)
			stmt.BindInt64(16, weight)
			stmt.BindInt(17, qtype)
			stmt.BindInt64(18, cfgid)
		})
		if rc == dbpkg.SQLITE_CONSTRAINT && i == 0 {
			continue
		}
		if i == 0 && rc == dbpkg.SQLITE_DONE {
			id = db.LastInsertRowid()
		}
		break
	}

	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	if rc != dbpkg.SQLITE_DONE {
		return MetadataPermanentError
	}

	if md != nil {
		if mm.metadbSetStreams(db, id, md) != 0 {
			return MetadataPermanentError
		}
	}
	return id
}

// MetadbInsertVideoitem — C: metadb_insert_videoitem (metadb.c:1095-1114)
func (mm *MetadataManager) MetadbInsertVideoitem(db *dbpkg.DB, url string,
	dsID int, extID string, md *Metadata, status int, weight int64,
	qtype int, cfgid int64) int64 {
	if db == nil {
		return MetadataPermanentError
	}

	itemID := dbItemGet(db, url, nil)
	if itemID == MetadataDeadlock {
		return itemID
	}
	if itemID == MetadataPermanentError {
		itemID = dbItemCreate(db, url, int(ContentVideo), 0, 0, 0)
		if itemID < 0 {
			return itemID
		}
	}
	return mm.metadbInsertVideoitem0(db, itemID, dsID, extID, md, status,
		weight, qtype, cfgid)
}

// metadbInsertImageitem — C: metadb_insert_imageitem (metadb.c:1119-1162)
func (mm *MetadataManager) metadbInsertImageitem(db *dbpkg.DB, itemID int64,
	md *Metadata) int {
	if db == nil {
		return MetadataPermanentError
	}

	var rc int
	for i := range 2 {
		var q string
		if i == 0 {
			q = `INSERT OR FAIL INTO imageitem
				(item_id, original_time, manufacturer, equipment)
				VALUES (?1, ?2, ?3, ?4)`
		} else {
			q = `UPDATE imageitem SET
				original_time = ?2, manufacturer = ?3, equipment = ?4
				WHERE item_id = ?1`
		}
		rc = dbExec(db, q, func(stmt *dbpkg.Stmt) {
			stmt.BindInt64(1, itemID)
			if !md.Time.IsZero() {
				stmt.BindInt64(2, md.Time.Unix())
			}
			dbBindStr(stmt, 3, md.Manufacturer)
			dbBindStr(stmt, 4, md.Equipment)
		})
		if rc == dbpkg.SQLITE_CONSTRAINT && i == 0 {
			continue
		}
		break
	}
	return rc2MetadataCode(rc)
}

// metadbMetadataGetVideo — C: metadb_metadata_get_video (metadb.c:1459-1495)
func metadbMetadataGetVideo(db *dbpkg.DB, md *Metadata, itemID int64,
	dsID int) int64 {
	sel, rc := dbpkg.DBPrepare(db, `SELECT id, title, duration, format, year
		FROM videoitem WHERE item_id = ?1 AND ds_id = ?2`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, itemID)
	sel.BindInt(2, dsID)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		sel.Finalize()
		return MetadataPermanentError
	}
	id := sel.ColumnInt64(0)
	md.Title = sel.ColumnText(1)
	md.Duration = float32(sel.ColumnInt64(2)) / 1000.0
	md.Format = sel.ColumnText(3)
	md.Year = int16(sel.ColumnInt64(4))
	sel.Finalize()
	return id
}

// MetadbVideoitemSetPreferred — C: metadb_videoitem_set_preferred (metadb.c:1502-1525)
func (mm *MetadataManager) MetadbVideoitemSetPreferred(db *dbpkg.DB, url string,
	vid int64) int {
	if db == nil {
		return MetadataPermanentError
	}

	rc := dbExec(db, `UPDATE videoitem
		SET preferred = (CASE WHEN id=?2 THEN 1 ELSE 0 END)
		WHERE item_id = (SELECT id FROM item WHERE url = ?1)`,
		func(stmt *dbpkg.Stmt) {
			stmt.BindText(1, url)
			stmt.BindInt64(2, vid)
		})
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	return 0
}

// MetadbVideoitemDeleteFromDs — C: metadb_videoitem_delete_from_ds (metadb.c:1531-1554)
func (mm *MetadataManager) MetadbVideoitemDeleteFromDs(db *dbpkg.DB, url string,
	ds int) int {
	if db == nil {
		return MetadataPermanentError
	}

	rc := dbExec(db, `DELETE FROM videoitem
		WHERE item_id = (SELECT id FROM item WHERE url = ?1) AND ds_id = ?2`,
		func(stmt *dbpkg.Stmt) {
			stmt.BindText(1, url)
			stmt.BindInt(2, ds)
		})
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	return 0
}

// metadbVideoitemAlternatives0 — C: metadb_videoitem_alternatives0
// (metadb.c:1560-1624)
func (mm *MetadataManager) metadbVideoitemAlternatives0(db *dbpkg.DB,
	p *propcore.Prop, url string, dsid int, skipme *propcore.Subscription) int {
	if db == nil {
		return MetadataPermanentError
	}

	pm := p.Manager()
	var active *propcore.Prop

	sel, rc := dbpkg.DBPrepare(db, `SELECT v.id, v.title, v.year, v.preferred, v.status
		FROM videoitem as v, item
		WHERE item.url = ?1 AND v.item_id = item.id AND v.ds_id = ?2
		ORDER BY v.weight DESC`)
	if rc != dbpkg.SQLITE_OK {
		return 0
	}
	sel.BindText(1, url)
	sel.BindInt(2, dsid)

	// C: pv = prop_vec_create(10) — Go uses a container prop as the vector
	vec := pm.CreateRootEx("", true)

	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		id := sel.ColumnInt64(0)
		title := sel.ColumnText(1)
		year := sel.ColumnInt64(2)
		preferred := sel.ColumnInt(3)
		status := sel.ColumnInt(4)
		if status == MetaItemStatusAbsent {
			continue
		}
		c := pm.CreateRootEx(strconv.FormatInt(id, 10), true)
		if preferred != 0 && active == nil {
			active = pm.RefInc(c)
		}
		var str string
		if year != 0 {
			str = fmt.Sprintf("%s (%d)", title, year)
		} else {
			str = title
		}
		pm.CreateEx(c, "title", nil, false, false).SetString(str)
		pm.SetParentEx(c, vec, nil, "")
	}
	sel.Finalize()

	pm.DestroyChilds(p)
	pm.SetParentVector(vec, p, nil, "")

	if active != nil {
		pm.SelectChildProp(active, nil)
	} else if len(vec.GetChildren()) > 0 {
		pm.SelectChildProp(vec.GetChildren()[0], nil)
	}
	if active != nil {
		pm.RefDec(active)
	}
	pm.Destroy(vec)
	return 0
}

// MetadbVideoitemAlternatives — C: metadb_videoitem_alternatives
// (metadb.c:1629-1649)
func (mm *MetadataManager) MetadbVideoitemAlternatives(p *propcore.Prop,
	url string, dsid int, skipme *propcore.Subscription) {
	db := mm.Get()
	if db == nil {
		return
	}
again:
	if dbpkg.DBBegin(db) != 0 {
		mm.Close(db)
		return
	}
	if mm.metadbVideoitemAlternatives0(db, p, url, dsid, skipme) != 0 {
		goto again
	}
	dbpkg.DBRollback(db)
	mm.Close(db)
}

// MetadbItemSetPreferredDs — C: metadb_item_set_preferred_ds (metadb.c:1655-1680)
func (mm *MetadataManager) MetadbItemSetPreferredDs(db *dbpkg.DB, url string,
	dsID int) int {
	if db == nil {
		return MetadataPermanentError
	}

	rc := dbExec(db, "UPDATE item SET ds_id = ?2 WHERE url=?1",
		func(stmt *dbpkg.Stmt) {
			stmt.BindText(1, url)
			if dsID != 0 {
				stmt.BindInt(2, dsID)
			}
		})
	if rc == dbpkg.SQLITE_LOCKED {
		return MetadataDeadlock
	}
	return 0
}

// MetadbItemGetPreferredDs — C: metadb_item_get_preferred_ds (metadb.c:1686-1715)
func (mm *MetadataManager) MetadbItemGetPreferredDs(url string) int {
	db := mm.Get()
	if db == nil {
		return MetadataPermanentError
	}
	defer mm.Close(db)
	sel, rc := dbpkg.DBPrepare(db, "SELECT ds_id FROM item WHERE url=?1")
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	defer sel.Finalize()
	sel.BindText(1, url)
	if dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		return sel.ColumnInt(0)
	}
	return 0
}

// MetadbItemGetUserTitle — C: metadb_item_get_user_title (metadb.c:1721-1752)
func (mm *MetadataManager) MetadbItemGetUserTitle(url string) string {
	db := mm.Get()
	if db == nil {
		return ""
	}
	defer mm.Close(db)
	sel, rc := dbpkg.DBPrepare(db,
		"SELECT usertitle FROM item WHERE url=?1")
	if rc != dbpkg.SQLITE_OK {
		return ""
	}
	defer sel.Finalize()
	sel.BindText(1, url)
	if dbpkg.DBStep(sel) != dbpkg.SQLITE_ROW {
		return ""
	}
	return sel.ColumnText(0)
}

// MetadbItemSetUserTitle — C: metadb_item_set_user_title (metadb.c:1759-1788)
func (mm *MetadataManager) MetadbItemSetUserTitle(url, str string) {
	db := mm.Get()
	if db == nil {
		return
	}
	defer mm.Close(db)
	// C: if(str && !*str) str = NULL;
	dbExec(db, "UPDATE item SET usertitle=?2 WHERE url=?1",
		func(stmt *dbpkg.Stmt) {
			stmt.BindText(1, url)
			dbBindStr(stmt, 2, str)
		})
}

// metadbGetVideoinfo2 — C: metadb_get_videoinfo2 (metadb.c:1794-1854)
func (mm *MetadataManager) metadbGetVideoinfo2(db *dbpkg.DB, id int64,
	mdp **Metadata) int {
	if db == nil {
		return MetadataPermanentError
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT v.parent_id, v.title, v.tagline, v.description,
		v.year, v.rating, v.rate_count, v.imdb_id, v.idx, v.type, v.id
		FROM videoitem AS v WHERE v.id = ?1`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, id)

	rc = dbpkg.DBStep(sel)
	if rc == dbpkg.SQLITE_LOCKED {
		sel.Finalize()
		return MetadataDeadlock
	}
	if rc != dbpkg.SQLITE_ROW {
		sel.Finalize()
		return 0
	}

	md := Create()
	md.ParentID = sel.ColumnInt64(0)
	md.Title = sel.ColumnText(1)
	md.Tagline = sel.ColumnText(2)
	md.Description = sel.ColumnText(3)
	md.Year = int16(sel.ColumnInt(4))
	md.Rating = int16(dbpkg.DBPosint(sel, 5))
	md.RatingCount = dbpkg.DBPosint(sel, 6)
	md.IMDBID = sel.ColumnText(7)
	md.Idx = int16(dbpkg.DBPosint(sel, 8))
	md.Type = MetadataType(sel.ColumnInt(9))
	md.ID = sel.ColumnInt64(10)
	sel.Finalize()

	md.Icons = mm.metadbGetVideoArt(db, id, int(MetadataImagePoster))
	md.Backdrops = mm.metadbGetVideoArt(db, id, int(MetadataImageBackdrop))
	md.Thumbs = mm.metadbGetVideoArt(db, id, int(MetadataImageThumb))
	md.WideBanners = mm.metadbGetVideoArt(db, id, int(MetadataImageBannerWide))
	md.Genre = mm.metadbGetVideoGenre(db, id)
	mm.metadbGetVideoCast(db, id, md)

	if md.ParentID != 0 {
		mm.metadbGetVideoinfo2(db, md.ParentID, &md.Parent)
	}
	*mdp = md
	return 0
}

// MetadbGetVideoitem — C: metadb_get_videoitem (metadb.c:1860-1885)
func (mm *MetadataManager) MetadbGetVideoitem(db *dbpkg.DB, url string) int64 {
	if db == nil {
		return MetadataPermanentError
	}

	sel, rc := dbpkg.DBPrepare(db, `SELECT videoitem.id FROM videoitem,item
		WHERE videoitem.item_id = item.id AND item.url = ?1`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	defer sel.Finalize()
	sel.BindText(1, url)
	if dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		return sel.ColumnInt64(0)
	}
	return MetadataPermanentError
}

// MetadataSourceQueryInfo — C: metadata_source_query_info_t (metadata.h:313)
type MetadataSourceQueryInfo struct {
	MS     *MetadataSource
	ID     int64
	Mark   bool
	QType  int
	Status int
}

// MetadbGetVideoinfo — C: metadb_get_videoinfo (metadb.c:1891-2037)
func (mm *MetadataManager) MetadbGetVideoinfo(db *dbpkg.DB, url string,
	msqi []MetadataSourceQueryInfo, fixedDs *int, mdp **Metadata,
	onlyPreferred int) int {
	if db == nil {
		return MetadataPermanentError
	}

	if fixedDs != nil {
		*fixedDs = 0
	}
	*mdp = nil

	sel, rc := dbpkg.DBPrepare(db,
		"SELECT id, ds_id FROM item WHERE url = ?1")
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindText(1, url)
	var itemID int64
	var dsID int64
	rc = dbpkg.DBStep(sel)
	if rc != dbpkg.SQLITE_ROW {
		sel.Finalize()
		if rc == dbpkg.SQLITE_LOCKED {
			return MetadataDeadlock
		}
		return 0
	}
	itemID = sel.ColumnInt64(0)
	dsID = sel.ColumnInt64(1)
	sel.Finalize()

	if fixedDs != nil {
		*fixedDs = int(dsID)
	}
	if onlyPreferred != 0 && dsID == 0 {
		return 0
	}

	sel, rc = dbpkg.DBPrepare(db, `SELECT v.id, v.title, v.tagline, v.description, v.year,
		v.rating, v.rate_count, v.imdb_id, v.ds_id, v.status,
		v.preferred, v.ext_id, ds.id, ds.enabled, v.querytype,
		v.cfgid, v.idx, v.type, v.parent_id
		FROM datasource AS ds, videoitem AS v
		WHERE v.item_id = ?1 AND ds.id = v.ds_id
		AND (?2 == 0 OR ?2 = v.ds_id)
		ORDER BY ds.prio ASC, v.weight DESC`)
	if rc != dbpkg.SQLITE_OK {
		return MetadataPermanentError
	}
	sel.BindInt64(1, itemID)
	sel.BindInt64(2, dsID)

	var md *Metadata
	for dbpkg.DBStep(sel) == dbpkg.SQLITE_ROW {
		vid := sel.ColumnInt64(0)
		status := sel.ColumnInt(9)
		dsid := sel.ColumnInt(12)
		dsenabled := sel.ColumnInt(13)
		preferred := sel.ColumnInt(10)
		qtype := sel.ColumnInt(14)
		cfgid := sel.ColumnInt64(15)
		if dsenabled == 0 {
			continue
		}

		if len(msqi) > 0 {
			found := -1
			for i := range msqi {
				ms := msqi[i].MS
				if ms != nil && ms.ID == dsid && ms.CfgID == cfgid {
					found = i
					break
				}
			}
			if found >= 0 {
				msqi[found].Mark = true
				msqi[found].QType = qtype
				msqi[found].Status = status
			} else {
				continue
			}
		}

		if status == MetaItemStatusAbsent {
			continue
		}
		if preferred != 0 && md != nil {
			md.Destroy()
			md = nil
		}
		if md != nil {
			continue
		}
		md = Create()
		md.Preferred = preferred != 0
		md.Title = sel.ColumnText(1)
		md.Tagline = sel.ColumnText(2)
		md.Description = sel.ColumnText(3)
		md.Year = int16(sel.ColumnInt(4))
		md.Rating = int16(dbpkg.DBPosint(sel, 5))
		md.RatingCount = dbpkg.DBPosint(sel, 6)
		md.Type = MetadataType(sel.ColumnInt(17))
		md.IMDBID = sel.ColumnText(7)
		md.DSID = int16(sel.ColumnInt(8))
		md.MetaItemStatus = status
		md.ExtID = sel.ColumnText(11)
		md.QType = qtype
		md.Idx = int16(dbpkg.DBPosint(sel, 16))
		md.ParentID = sel.ColumnInt64(18)
		md.ID = vid

		// C: nested statements on the same handle while iterating —
		// buffer the scalars, then run the sub-queries after the row
		// columns are consumed (sqlite allows this on one handle).
		icons := mm.metadbGetVideoArt(db, vid, int(MetadataImagePoster))
		backdrops := mm.metadbGetVideoArt(db, vid, int(MetadataImageBackdrop))
		wide := mm.metadbGetVideoArt(db, vid, int(MetadataImageBannerWide))
		thumbs := mm.metadbGetVideoArt(db, vid, int(MetadataImageThumb))
		mm.metadbGetVideoCast(db, vid, md)
		genre := mm.metadbGetVideoGenre(db, vid)
		md.Icons = icons
		md.Backdrops = backdrops
		md.WideBanners = wide
		md.Thumbs = thumbs
		md.Genre = genre

		if md.ParentID != 0 {
			mm.metadbGetVideoinfo2(db, md.ParentID, &md.Parent)
		}
	}
	sel.Finalize()
	*mdp = md
	return 0
}
