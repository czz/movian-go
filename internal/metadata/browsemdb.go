package metadata

// Canonical port of src/metadata/browsemdb.c — the "library:" backend.
// Queries the metadb for albums/artists/videos and populates the
// $page.model.nodes prop tree for the browser UI.

import (
	"fmt"
	"strconv"
	"time"

	"github.com/czz/movian-go/internal/db"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// library_query_t (metadata.h:465-471)
const (
	LibraryQueryAlbums  = iota // LIBRARY_QUERY_ALBUMS
	LibraryQueryAlbum          // LIBRARY_QUERY_ALBUM
	LibraryQueryArtists        // LIBRARY_QUERY_ARTISTS
	LibraryQueryArtist         // LIBRARY_QUERY_ARTIST
	LibraryQueryVideos         // LIBRARY_QUERY_VIDEOS
)

// bmdb — C: typedef struct bmdb (browsemdb.c:37-44)
type bmdb struct {
	mm       *MetadataManager // owning manager — C resolves via singleton
	query    string           // char *b_query
	btype    int              // library_query_t b_type
	nodes    *propcore.Prop   // prop_t *b_nodes
	metadata *propcore.Prop   // prop_t *b_metadata
}

// countItems — C: count_items (browsemdb.c:52-71). Runs a COUNT query
// with the url escaped via db_escape_path_query bound at ?1.
func countItems(dbc *db.DB, query string, url string) int {
	rval := 0
	stmt, rc := db.DBPrepare(dbc, query)
	if rc != db.SQLITE_OK {
		return 0
	}
	pfx := db.DBEscapePathQuery(url)
	stmt.BindText(1, pfx)
	rc = db.DBStep(stmt)
	if rc == db.SQLITE_ROW {
		rval = stmt.ColumnInt(0)
	}
	stmt.Finalize()
	return rval
}

// getPercentage — C: get_percentage (browsemdb.c:77-102). Indexing
// progress for url: done/(done+remain)*100, or 100 when nothing remains.
func (b *bmdb) getPercentage(url string) int {
	dbc := b.mm.Get()
	if dbc == nil {
		return 0
	}
	remain := countItems(dbc,
		"SELECT count(*) "+
			"FROM item "+
			"WHERE url LIKE ?1 "+
			"AND contenttype = 1 "+
			"AND indexstatus == 0",
		url)
	var rval int
	if remain != 0 {
		done := countItems(dbc,
			"SELECT count(*) "+
				"FROM item "+
				"WHERE url LIKE ?1 "+
				"AND contenttype = 1 "+
				"AND indexstatus > 1",
			url)
		rval = min(done*100/(done+remain), 100)
	} else {
		rval = 100
	}
	b.mm.Close(dbc)
	return rval
}

// bmdbDestroy — C: bmdb_destroy (browsemdb.c:113-120).
func (b *bmdb) destroy(pm *propcore.PropManager) {
	pm.RefDec(b.nodes)
	pm.RefDec(b.metadata)
	// C: free(b->b_query); free(b); — Go GC
}

// addItem — C: add_item (browsemdb.c:126-167). Creates a node child
// named by url, sets type/url/metadata props.
func (b *bmdb) addItem(pm *propcore.PropManager, url, parent string,
	contenttype string, title string, track int, artist string, duration int) {
	// C: prop_t *c = prop_create_r(b->b_nodes, url);
	c := pm.CreateEx(b.nodes, url, nil, false, false)
	pm.RefInc(c)

	// C: prop_unmark(c);
	pm.Unmark(c)

	// C: prop_set(c, "type", PROP_SET_RSTRING, contenttype);
	if p := pm.CreateEx(c, "type", nil, false, false); p != nil {
		pm.SetStringEx(p, nil, contenttype, propcore.StringUTF8)
	}
	// C: prop_set(c, "url", PROP_SET_STRING, url);
	if p := pm.CreateEx(c, "url", nil, false, false); p != nil {
		pm.SetStringEx(p, nil, url, propcore.StringUTF8)
	}

	// C: prop_t *metadata = prop_create_r(c, "metadata");
	metadataProp := pm.CreateEx(c, "metadata", nil, false, false)
	pm.RefInc(metadataProp)

	if track != 0 {
		if p := pm.CreateEx(metadataProp, "track", nil, false, false); p != nil {
			pm.SetIntEx(p, nil, track)
		}
	}
	if artist != "" {
		if p := pm.CreateEx(metadataProp, "artist", nil, false, false); p != nil {
			pm.SetStringEx(p, nil, artist, propcore.StringUTF8)
		}
	}
	if duration > 0 {
		if p := pm.CreateEx(metadataProp, "duration", nil, false, false); p != nil {
			pm.SetIntEx(p, nil, duration/1000)
		}
	}

	if title == "" {
		// C: fa_url_get_last_component + metadata_remove_postfix
		fname := b.mm.fam.URLGetLastComponent(url)
		// C: rstr_t *ft = metadata_remove_postfix(fname)
		ft := misc.RstrGet(MetadataRemovePostfix(fname))
		if p := pm.CreateEx(metadataProp, "title", nil, false, false); p != nil {
			pm.SetStringEx(p, nil, ft, propcore.StringUTF8)
		}
	} else {
		if p := pm.CreateEx(metadataProp, "title", nil, false, false); p != nil {
			pm.SetStringEx(p, nil, title, propcore.StringUTF8)
		}
	}

	// C: prop_ref_dec(metadata); prop_ref_dec(c);
	pm.RefDec(metadataProp)
	pm.RefDec(c)
}

// videoQuery — C: video_query (browsemdb.c:173-197).
func (b *bmdb) videoQuery(pm *propcore.PropManager, dbc *db.DB) {
	stmt, rc := db.DBPrepare(dbc,
		"SELECT i.url, p.url, i.contenttype "+
			"FROM item AS i, item AS p "+
			"WHERE i.url LIKE ?1 "+
			"AND i.parent IS NOT NULL "+
			"AND (i.contenttype == 5 OR i.contenttype == 7) "+
			"AND i.parent = p.id")
	if rc != db.SQLITE_OK {
		return
	}
	q := db.DBEscapePathQuery(b.query)
	stmt.BindText(1, q)
	for db.DBStep(stmt) == db.SQLITE_ROW {
		url := stmt.ColumnText(0)
		parent := stmt.ColumnText(1)
		ct := Content2Type(ContentType(stmt.ColumnInt(2)))
		b.addItem(pm, url, parent, ct, "", 0, "", 0)
	}
	stmt.Finalize()
}

// albumsQuery — C: albums_query (browsemdb.c:203-239).
func (b *bmdb) albumsQuery(pm *propcore.PropManager, dbc *db.DB) {
	stmt, rc := db.DBPrepare(dbc,
		"SELECT album.id, album.title, artist.title "+
			"FROM album, item, audioitem, artist "+
			"WHERE audioitem.item_id = item.id "+
			"AND audioitem.album_id = album.id "+
			"AND item.url LIKE ?1 "+
			"AND audioitem.ds_id = 1 "+
			"AND item.parent IS NOT NULL "+
			"AND audioitem.artist_id = artist.id "+
			"GROUP BY album_id")
	if rc != db.SQLITE_OK {
		return
	}
	q := db.DBEscapePathQuery(b.query)
	stmt.BindText(1, q)
	ct := "album"
	for db.DBStep(stmt) == db.SQLITE_ROW {
		url := fmt.Sprintf("library:album:%d", stmt.ColumnInt(0))
		b.addItem(pm, url, "", ct,
			stmt.ColumnText(1), 0, stmt.ColumnText(2), 0)
	}
	stmt.Finalize()
}

// albumQuery — C: album_query (browsemdb.c:245-303).
func (b *bmdb) albumQuery(pm *propcore.PropManager, dbc *db.DB) {
	// C: int album_id = misc.Atoi(b->b_query);
	albumID, _ := strconv.Atoi(b.query)

	stmt, rc := db.DBPrepare(dbc,
		"SELECT album.title, artist.title "+
			"FROM album, artist "+
			"WHERE album.id = ?1 "+
			"AND artist.id = album.artist_id "+
			"AND album.ds_id = 1")
	if rc == db.SQLITE_OK {
		stmt.BindInt(1, albumID)
		if db.DBStep(stmt) == db.SQLITE_ROW {
			album := db.DBRstr(stmt, 0)
			artist := db.DBRstr(stmt, 1)

			if p := pm.CreateEx(b.metadata, "title", nil, false, false); p != nil {
				pm.SetStringEx(p, nil, album, propcore.StringUTF8)
			}
			if p := pm.CreateEx(b.metadata, "artist_name", nil, false, false); p != nil {
				pm.SetStringEx(p, nil, artist, propcore.StringUTF8)
			}

			// C: prop_t *p = prop_create_r(b->b_metadata, "album_art");
			//    metadata_bind_albumart(p, artist, album); prop_ref_dec(p);
			if mm := b.mm; mm != nil {
				p := pm.CreateEx(b.metadata, "album_art", nil, false, false)
				pm.RefInc(p)
				mm.MetadataBindAlbumart(p, artist, album)
				pm.RefDec(p)
			}
		}
		stmt.Finalize()
	}

	stmt, rc = db.DBPrepare(dbc,
		"SELECT url, audioitem.title, track, duration, "+
			"artist.title "+
			"FROM audioitem,item,artist "+
			"WHERE audioitem.item_id = item.id "+
			"AND audioitem.artist_id = artist.id "+
			"AND album_id = ?1 "+
			"AND parent IS NOT NULL "+
			"AND audioitem.ds_id = 1")
	if rc != db.SQLITE_OK {
		return
	}
	stmt.BindInt(1, albumID)
	ct := "audio"
	for db.DBStep(stmt) == db.SQLITE_ROW {
		b.addItem(pm, stmt.ColumnText(0), "", ct,
			stmt.ColumnText(1),
			stmt.ColumnInt(2),
			stmt.ColumnText(4),
			stmt.ColumnInt(3))
	}
	stmt.Finalize()
}

// artistQuery — C: artist_query (browsemdb.c:309-375).
func (b *bmdb) artistQuery(pm *propcore.PropManager, dbc *db.DB) {
	// C: int artist_id = misc.Atoi(b->b_query);
	artistID, _ := strconv.Atoi(b.query)

	stmt, rc := db.DBPrepare(dbc,
		"SELECT title "+
			"FROM artist "+
			"WHERE ds_id = 1 "+
			"AND id = ?1")
	if rc == db.SQLITE_OK {
		stmt.BindInt(1, artistID)
		if db.DBStep(stmt) == db.SQLITE_ROW {
			artist := db.DBRstr(stmt, 0)
			if p := pm.CreateEx(b.metadata, "title", nil, false, false); p != nil {
				pm.SetStringEx(p, nil, artist, propcore.StringUTF8)
			}

			// C: prop_t *p = prop_create_r(b->b_metadata, "artist_images");
			//    metadata_bind_artistpics(p, artist); prop_ref_dec(p);
			if mm := b.mm; mm != nil {
				p := pm.CreateEx(b.metadata, "artist_images", nil, false, false)
				pm.RefInc(p)
				mm.MetadataBindArtistpics(p, artist)
				pm.RefDec(p)
			}
		}
		stmt.Finalize()
	}

	stmt, rc = db.DBPrepare(dbc,
		"SELECT id,title "+
			"FROM album "+
			"WHERE ds_id = 1 "+
			"AND artist_id = ?1")
	if rc != db.SQLITE_OK {
		return
	}
	stmt.BindInt(1, artistID)
	ct := "album"
	for db.DBStep(stmt) == db.SQLITE_ROW {
		url := fmt.Sprintf("library:album:%d", stmt.ColumnInt(0))
		b.addItem(pm, url, "", ct, stmt.ColumnText(1), 0, "", 0)
	}
	stmt.Finalize()
}

// artistsQuery — C: artists_query (browsemdb.c:381-415).
func (b *bmdb) artistsQuery(pm *propcore.PropManager, dbc *db.DB) {
	stmt, rc := db.DBPrepare(dbc,
		"SELECT artist.id, artist.title "+
			"FROM artist,item,audioitem "+
			"WHERE audioitem.item_id = item.id "+
			"AND audioitem.artist_id = artist.id "+
			"AND item.url like ?1 "+
			"AND parent IS NOT NULL "+
			"AND audioitem.ds_id = 1 "+
			"GROUP by artist_id")
	if rc != db.SQLITE_OK {
		return
	}
	q := db.DBEscapePathQuery(b.query)
	stmt.BindText(1, q)
	ct := "artist"
	for db.DBStep(stmt) == db.SQLITE_ROW {
		url := fmt.Sprintf("library:artist:%d", stmt.ColumnInt(0))
		b.addItem(pm, url, "", ct, stmt.ColumnText(1), 0, "", 0)
	}
	stmt.Finalize()
}

// bmdbQueryExec — C: bmdb_query_exec (browsemdb.c:418-448). Marks all
// node children, runs the query, destroys what wasn't refreshed.
func (b *bmdb) queryExec(pm *propcore.PropManager, dbc *db.DB) int {
	pm.MarkChilds(b.nodes)

	switch b.btype {
	case LibraryQueryAlbums:
		b.albumsQuery(pm, dbc)
	case LibraryQueryAlbum:
		b.albumQuery(pm, dbc)
	case LibraryQueryVideos:
		b.videoQuery(pm, dbc)
	case LibraryQueryArtists:
		b.artistsQuery(pm, dbc)
	case LibraryQueryArtist:
		b.artistQuery(pm, dbc)
	}

	pm.DestroyMarkedChilds(b.nodes)
	return 0
}

// MetadataBrowse — C: metadata_browse (browsemdb.c:451-488). Runs the
// query loop, updating model.percentage/progressmeter/status until
// checkstop fires.
func (mm *MetadataManager) MetadataBrowse(pm *propcore.PropManager, dbc *db.DB, url string,
	nodes, model *propcore.Prop, btype int,
	checkstop func(opaque any) bool, opaque any) {
	// C: prop_t *status = prop_create_r(model, "status");
	status := pm.CreateEx(model, "status", nil, false, false)
	pm.RefInc(status)

	b := &bmdb{
		mm:    mm,
		query: url,
		btype: btype,
		nodes: nodes,
	}
	// C: b.b_metadata = prop_create_r(model, "metadata");
	b.metadata = pm.CreateEx(model, "metadata", nil, false, false)
	pm.RefInc(b.metadata)

	for !checkstop(opaque) {
		p := b.getPercentage(url)

		if p != 100 {
			if pp := pm.CreateEx(model, "percentage", nil, false, false); pp != nil {
				pm.SetIntEx(pp, nil, p)
			}
			if pp := pm.CreateEx(model, "progressmeter", nil, false, false); pp != nil {
				pm.SetIntEx(pp, nil, 1)
			}
			// C: prop_link(_p("Indexing"), status);
			idx := pm.CreateRoot("")
			pm.SetStringEx(idx, nil, "Indexing", propcore.StringUTF8)
			idx.Link(status)
		} else {
			if pp := pm.CreateEx(model, "progressmeter", nil, false, false); pp != nil {
				pm.SetIntEx(pp, nil, 0)
			}
			status.Unlink()
		}

		b.queryExec(pm, dbc)
		if pp := pm.CreateEx(model, "loading", nil, false, false); pp != nil {
			pm.SetIntEx(pp, nil, 0)
		}
		// C: sleep(1);
		time.Sleep(time.Second)
	}

	if pp := pm.CreateEx(model, "progressmeter", nil, false, false); pp != nil {
		pm.SetIntEx(pp, nil, 0)
	}
	status.Unlink()

	// C: free(b.b_query); prop_ref_dec(status); prop_ref_dec(b.b_metadata);
	pm.RefDec(status)
	pm.RefDec(b.metadata)
}

// bmdbThread — C: bmdb_thread (browsemdb.c:497-508). Opens a metadb
// connection, runs the query once, destroys b.
func bmdbThread(pm *propcore.PropManager, b *bmdb) {
	dbc := b.mm.Get()
	b.queryExec(pm, dbc)
	b.mm.Close(dbc)
	b.destroy(pm)
}

// bmdbQueryCreate — C: bmdb_query_create (browsemdb.c:512-525).
func bmdbQueryCreate(mm *MetadataManager, pm *propcore.PropManager, query string, btype int,
	model *propcore.Prop) *bmdb {
	// C: prop_set(model, "type", PROP_SET_STRING, "directory");
	if p := pm.CreateEx(model, "type", nil, false, false); p != nil {
		pm.SetStringEx(p, nil, "directory", propcore.StringUTF8)
	}
	b := &bmdb{mm: mm, btype: btype, query: query}
	// C: b->b_nodes = prop_create_r(model, "nodes");
	b.nodes = pm.CreateEx(model, "nodes", nil, false, false)
	pm.RefInc(b.nodes)
	// C: b->b_metadata = prop_create_r(model, "metadata");
	b.metadata = pm.CreateEx(model, "metadata", nil, false, false)
	pm.RefInc(b.metadata)
	return b
}

// LibraryOpen — C: library_open (browsemdb.c:529-562). Parses the
// "library:<query>" URL and spawns bmdb_thread.
func (mm *MetadataManager) LibraryOpen(pm *propcore.PropManager, page *propcore.Prop, url string, sync bool, openError func(pm *propcore.PropManager, page *propcore.Prop, msg string)) int {
	// C: prop_t *model = prop_create(page, "model");
	model := pm.CreateEx(page, "model", nil, false, false)

	// C: url += strlen("library:");
	url = url[len("library:"):]

	var b *bmdb
	var q string
	ok := false

	if q = strBegins(url, "albums:"); q != "" || url == "albums:" {
		b = bmdbQueryCreate(mm, pm, q, LibraryQueryAlbums, model)
		ok = true
	} else if q = strBegins(url, "album:"); q != "" {
		b = bmdbQueryCreate(mm, pm, q, LibraryQueryAlbum, model)
		// C: prop_set(model, "contents", PROP_SET_STRING, "album");
		if p := pm.CreateEx(model, "contents", nil, false, false); p != nil {
			pm.SetStringEx(p, nil, "album", propcore.StringUTF8)
		}
		ok = true
	} else if q = strBegins(url, "artists:"); q != "" || url == "artists:" {
		b = bmdbQueryCreate(mm, pm, q, LibraryQueryArtists, model)
		ok = true
	} else if q = strBegins(url, "artist:"); q != "" {
		b = bmdbQueryCreate(mm, pm, q, LibraryQueryArtist, model)
		if p := pm.CreateEx(model, "contents", nil, false, false); p != nil {
			pm.SetStringEx(p, nil, "artist", propcore.StringUTF8)
		}
		ok = true
	} else if q = strBegins(url, "videos:"); q != "" || url == "videos:" {
		b = bmdbQueryCreate(mm, pm, q, LibraryQueryVideos, model)
		ok = true
	}

	if !ok {
		// C: nav_open_error(page, "Invalid browse URL"); return 0;
		if openError != nil {
			openError(pm, page, "Invalid browse URL")
		}
		return 0
	}

	// C: hts_thread_create_detached("bmdbquery", bmdb_thread, b, THREAD_PRIO_METADATA);
	go bmdbThread(pm, b)
	return 0
}

// strBegins — C: mystrbegins (misc/str.h) — returns s+len(prefix) or "".
func strBegins(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return ""
}

// LibraryCanHandle — C: library_canhandle (browsemdb.c:565-569).
// Returns non-zero when the url starts with "library:".
func LibraryCanHandle(url string) int {
	if len(url) >= len("library:") && url[:len("library:")] == "library:" {
		return 1
	}
	return 0
}
