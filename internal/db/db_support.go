package db

// Canonical port of src/db/db_support.c (+ db_support.h macros).
// ENABLE_SQLITE_LOCKING is off in the reference build, so the custom
// sqlite3_mutex_methods block (db_support.c:587-674) is not compiled —
// the system libsqlite3 is threadsafe already.
//
// This file is variant-neutral: the raw sqlite3 seam lives in
// sqlite3_go.go (pure-Go modernc.org/sqlite, default) or sqlite3.go
// (cgo libsqlite3, legacy — opt back in with `-tags sqlitecgo`).

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/trace"
)

// dbLogTS — the trace system reached by the sqlite log hook. C: db_log
// runs on the process-global trace state (trace.c file statics); the
// hook has no per-connection context. Wired by init. Variant-neutral:
// used by goDBLog in both the cgo and modernc variants.
var dbLogTS struct{ ts *trace.TraceSystem }

// SetTraceSystem injects the trace system for the process-wide sqlite
// log hook (C: trace() global in db_support.c).
func SetTraceSystem(ts *trace.TraceSystem) { dbLogTS.ts = ts }

// unlockNotify — C: unlock_notify_t (db_support.c:31-35).
// Variant-neutral: waited on by waitForUnlockNotify in both the cgo
// (sqlite3.go) and pure-Go (sqlite3_go.go) bindings.
type unlockNotify struct {
	fired bool
	cond  *sync.Cond
	mu    sync.Mutex
}

// DBStep — C: db_step (db_support.c:75-87).
func DBStep(stmt *Stmt) int {
	var rc int
	for {
		rc = stmt.Step()
		if rc != SQLITE_LOCKED {
			break
		}
		rc = waitForUnlockNotify(stmt.DBHandle())
		if rc != SQLITE_OK {
			break
		}
		stmt.Reset()
	}
	if rc == SQLITE_LOCKED {
		stmt.db.ts.Trace(trace.TRACE_DEBUG, "DB", "Deadlock detected")
	}
	return rc
}

// DBPreparex — C: db_preparex (db_support.c:89-111).
func DBPreparex(db *DB, sql, file string, line int) (*Stmt, int) {
	var stmt *Stmt
	var rc int
	for {
		stmt, _, rc = db.PrepareV2(sql)
		if rc != SQLITE_LOCKED {
			break
		}
		rc = waitForUnlockNotify(db)
		if rc != SQLITE_OK {
			break
		}
	}

	if rc != SQLITE_OK {
		db.ts.Trace(trace.TRACE_ERROR, "SQLITE",
			"SQL Error %d at %s:%d", rc, file, line)
	} else {
		// C: if(0) db_explain(*ppStmt)
	}
	return stmt, rc
}

// DBPrepare — C: db_prepare macro (db_support.h). file/line come from
// the Go caller frame, mirroring __FILE__:__LINE__.
func DBPrepare(db *DB, sql string) (*Stmt, int) {
	_, file, line, _ := runtime.Caller(1)
	return DBPreparex(db, sql, file, line)
}

// DBOneStatement — C: db_one_statement (db_support.c:116-129).
func DBOneStatement(db *DB, sql, src string) int {
	var errmsg string
	rc := db.Exec(sql, &errmsg)
	if rc != 0 {
		if src == "" {
			src = sql
		}
		db.ts.Trace(trace.TRACE_ERROR, "SQLITE",
			"%s: %s failed -- %s", src, sql, errmsg)
	}
	return rc
}

// DBGetInt64FromQuery — C: db_get_int64_from_query
// (db_support.c:135-165).
func DBGetInt64FromQuery(db *DB, query string, v *int64) int {
	var rval int64 = -1

	for {
		stmt, rc := DBPrepare(db, query)
		if rc != 0 {
			if rc == SQLITE_LOCKED {
				continue // C: goto restart
			}
			return -1
		}

		rc = stmt.Step()
		if rc == SQLITE_LOCKED {
			stmt.Finalize()
			continue // C: goto restart
		}

		if rc == SQLITE_ROW {
			*v = stmt.ColumnInt64(0)
			rval = 0
		} else {
			rval = -1
		}
		stmt.Finalize()
		return int(rval)
	}
}

// DBGetIntFromQuery — C: db_get_int_from_query
// (db_support.c:171-179).
func DBGetIntFromQuery(db *DB, query string, v *int) int {
	var i64 int64
	r := DBGetInt64FromQuery(db, query, &i64)
	if r == 0 {
		*v = int(i64)
	}
	return r
}

// DBBegin0 — C: db_begin0 (db_support.c:183-187).
func DBBegin0(db *DB, src string) int {
	if db == nil {
		return 1 // C: db == NULL || ...
	}
	return DBOneStatement(db, "BEGIN;", src)
}

// DBCommit0 — C: db_commit0 (db_support.c:190-194).
func DBCommit0(db *DB, src string) int {
	if db == nil {
		return 1
	}
	return DBOneStatement(db, "COMMIT;", src)
}

// DBRollback0 — C: db_rollback0 (db_support.c:197-201).
func DBRollback0(db *DB, src string) int {
	if db == nil {
		return 1
	}
	return DBOneStatement(db, "ROLLBACK;", src)
}

// DBRollbackDeadlock0 — C: db_rollback_deadlock0
// (db_support.c:203-210).
func DBRollbackDeadlock0(db *DB, src string) int {
	r := DBRollback0(db, src)
	db.ts.Trace(trace.TRACE_DEBUG, "DB",
		"Rollback due to deadlock, and retrying")
	time.Sleep(100 * time.Millisecond) // C: usleep(100000)
	return r
}

// C: db_begin/db_commit/db_rollback macros — __FUNCTION__ equivalents.
func DBBegin(db *DB) int    { return DBBegin0(db, callerFunc()) }
func DBCommit(db *DB) int   { return DBCommit0(db, callerFunc()) }
func DBRollback(db *DB) int { return DBRollback0(db, callerFunc()) }
func DBRollbackDeadlock(db *DB) int {
	return DBRollbackDeadlock0(db, callerFunc())
}

func callerFunc() string {
	pc, _, _, _ := runtime.Caller(2)
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return ""
	}
	return fn.Name()
}

// DBUpgradeSchema — C: db_upgrade_schema (db_support.c:217-348).
func DBUpgradeSchema(db *DB, schemadir, dbname string,
	fam *fileaccesscore.FileAccessManager,
	extraDB, extraDBPath string) int {
	ver, tgtver := 0, 0
	var detach string

	if extraDB != "" {
		tmp := fmt.Sprintf("ATTACH DATABASE '%s' AS %s",
			extraDBPath, extraDB)
		detach = fmt.Sprintf("DETACH DATABASE %s", extraDB)
		if DBOneStatement(db, tmp, "") != 0 {
			db.ts.Trace(trace.TRACE_ERROR, "DB",
				"%s: Unable to %s", dbname, tmp)
			return -1
		}
	}

	DBOneStatement(db, "pragma journal_mode=wal;", "")

	if DBGetIntFromQuery(db, "pragma user_version", &ver) != 0 {
		db.ts.Trace(trace.TRACE_ERROR, "DB",
			"%s: Unable to query db version", dbname)
		if detach != "" {
			DBOneStatement(db, detach, "")
		}
		return -1
	}

	fd, serr := fileaccesscore.FAScandir(fam, schemadir, 0)

	if serr != nil {
		db.ts.Trace(trace.TRACE_ERROR, "DB",
			"%s: Unable to scan schema dir %s -- %s",
			dbname, schemadir, serr.Error())
		if detach != "" {
			DBOneStatement(db, detach, "")
		}
		return -1
	}

	// C: RB_FOREACH entries, CONTENT_FILE only, skip '~', atoi
	var names []string
	for _, fde := range fd.Entries {
		if fde.Type != fileaccesscore.ContentFile {
			continue
		}
		name := fde.Filename
		if strings.Contains(name, "~") {
			continue
		}
		names = append(names, name)
	}
	fd.Free()
	for _, n := range names {
		// C uses atoi(): parse leading digits only ("003.sql" -> 3)
		v := 0
		for _, c := range n {
			if c < '0' || c > '9' {
				break
			}
			v = v*10 + int(c-'0')
		}
		if v > tgtver {
			tgtver = v
		}
	}
	// keep ordering deterministic (RB_FOREACH was sorted)
	slices.Sort(names)

	if ver > tgtver {
		db.ts.Trace(trace.TRACE_ERROR, "DB",
			"%s: Installed version %d is too high for this "+
				"version of Movian", dbname, ver)
		if detach != "" {
			DBOneStatement(db, detach, "")
		}
		return -1
	}

	enableFK := false

	for {
		if ver == tgtver {
			db.ts.Trace(trace.TRACE_DEBUG, "DB",
				"%s: At current version %d", dbname, ver)
			if detach != "" {
				DBOneStatement(db, detach, "")
			}
			return 0
		}

		ver++
		path := fmt.Sprintf("%s/%03d.sql", schemadir, ver)

		sqlBuf, err := fileaccesscore.FALoad(fam, path, nil, nil, 0)
		if sqlBuf == nil || err != nil {
			msg := "load failed"
			if err != nil {
				msg = err.Error()
			}
			db.ts.Trace(trace.TRACE_ERROR, "DB",
				"%s: Unable to upgrade db schema to version "+
					"%d using %s -- %s",
				dbname, ver, path, msg)
			if detach != "" {
				DBOneStatement(db, detach, "")
			}
			return -1
		}

		sqlStr := string(sqlBuf.Data[:sqlBuf.Size])

		if strings.Contains(sqlStr, "-- schema-upgrade:disable-fk") {
			DBOneStatement(db, "PRAGMA foreign_keys=OFF;", "")
			enableFK = true
		}

		DBBegin(db)
		if DBOneStatement(db,
			fmt.Sprintf("PRAGMA user_version=%d", ver), "") != 0 {
			break
		}

		s := sqlStr
		failed := false
		for strings.Contains(s, ";") {
			stmt, tail, rc := db.PrepareV2(s)
			if rc != SQLITE_OK {
				db.ts.Trace(trace.TRACE_ERROR, "DB",
					"%s: Unable to prepare statement in "+
						"upgrade %d\n%s", dbname, ver, s)
				failed = true
				break
			}

			rc = stmt.Step()
			if rc != SQLITE_DONE {
				db.ts.Trace(trace.TRACE_ERROR, "DB",
					"%s: Unable to execute statement error "+
						"%d\n%s", dbname, rc, stmt.SQL())
				stmt.Finalize()
				failed = true
				break
			}
			stmt.Finalize()
			s = tail
		}
		if failed {
			goto fail
		}

		DBCommit(db)
		if enableFK {
			DBOneStatement(db, "PRAGMA foreign_keys=ON;", "")
			enableFK = false
		}
		db.ts.Trace(trace.TRACE_INFO, "DB",
			"%s: Upgraded to version %d", dbname, ver)
	}

fail:
	if detach != "" {
		DBOneStatement(db, detach, "")
	}
	DBRollback(db)

	if enableFK {
		DBOneStatement(db, "PRAGMA foreign_keys=ON;", "")
	}
	return -1
}

// DBPool — C: struct db_pool (db_support.c:354-360).
type DBPool struct {
	size   int
	closed bool
	path   string
	mutex  sync.Mutex
	pool   []*DB
	ts     *trace.TraceSystem // C: trace() global — injected at create
}

// DBPoolCreate — C: db_pool_create (db_support.c:365-375).
func DBPoolCreate(path string, size int, ts *trace.TraceSystem) *DBPool {
	return &DBPool{
		size: size,
		path: path,
		pool: make([]*DB, size),
		ts:   ts,
	}
}

// DBOpen — C: db_open (db_support.c:381-414).
func DBOpen(path string, flags int, ts *trace.TraceSystem) *DB {
	// dbOpenRaw is the per-variant seam: cgo libsqlite3
	// (sqlite3.go) or pure-Go modernc.org/sqlite (sqlite3_go.go).
	db, rc := dbOpenRaw(path, ts)
	if rc != 0 {
		ts.Trace(trace.TRACE_ERROR, "DB",
			"%s: Unable to open database: %s", path, db.Errmsg())
		db.Close()
		return nil
	}

	DBOneStatement(db, "PRAGMA synchronous = normal", path)
	if flags&DBOpenCaseSensitiveLike != 0 {
		DBOneStatement(db, "PRAGMA case_sensitive_like=1", path)
	}
	DBOneStatement(db, "PRAGMA foreign_keys=1", path)

	freelistCount := 0
	pageCount := 0

	DBGetIntFromQuery(db, "PRAGMA freelist_count", &freelistCount)
	DBGetIntFromQuery(db, "PRAGMA page_count", &pageCount)

	ts.Trace(trace.TRACE_DEBUG, "DB",
		"Opened database %s pages: free=%d total=%d",
		path, freelistCount, pageCount)

	return db
}

// DBClose — C: sqlite3_close used at call sites (metadb close paths).
func DBClose(db *DB) int {
	if db == nil {
		return SQLITE_OK
	}
	return db.Close()
}

// DBPoolGet — C: db_pool_get (db_support.c:420-448).
func DBPoolGet(dp *DBPool) *DB {
	if dp == nil {
		return nil
	}

	dp.mutex.Lock()

	if dp.closed {
		dp.mutex.Unlock()
		return nil
	}

	for i := range dp.size {
		if dp.pool[i] != nil {
			db := dp.pool[i]
			dp.pool[i] = nil
			dp.mutex.Unlock()
			return db
		}
	}

	dp.mutex.Unlock()

	return DBOpen(dp.path, DBOpenCaseSensitiveLike, dp.ts)
}

// DBPoolPut — C: db_pool_put (db_support.c:453-479).
func DBPoolPut(dp *DBPool, db *DB) {
	if db == nil {
		return
	}

	if db.GetAutocommit() == 0 {
		dp.ts.Trace(trace.TRACE_ERROR, "DB",
			"%s: db handle returned to pool while in "+
				"transaction, closing handle", dp.path)
		db.Close()
		return
	}

	dp.mutex.Lock()
	for i := range dp.size {
		if dp.pool[i] == nil {
			dp.pool[i] = db
			dp.mutex.Unlock()
			return
		}
	}

	dp.mutex.Unlock()
	db.Close()
}

// DBPoolClose — C: db_pool_close (db_support.c:485-498).
func DBPoolClose(dp *DBPool) {
	if dp == nil {
		return
	}

	dp.mutex.Lock()
	dp.closed = true
	for i := range dp.size {
		if dp.pool[i] != nil {
			dp.pool[i].Close()
		}
	}
	dp.mutex.Unlock()
}

// DBRstr — C: db_rstr (db_support.c:504-508). Returns a Go string —
// the rstr_t equivalent (callers consume it like rstr_get).
func DBRstr(stmt *Stmt, col int) string {
	return stmt.ColumnText(col)
}

// DBPosint — C: db_posint (db_support.c:514-520).
func DBPosint(stmt *Stmt, col int) int {
	if stmt.ColumnType(col) == SQLITE_INTEGER {
		return stmt.ColumnInt(col)
	}
	return -1
}

// DBEscapePathQuery — C: db_escape_path_query (db_support.c:526-546).
func DBEscapePathQuery(src string) string {
	if len(src) == 0 {
		return "%"
	}
	var dst strings.Builder
	dstlen := 256 // C: dstlen param; callers use PATH_MAX-ish sizes
	for i := 0; i < len(src) && dstlen > 4; dstlen-- {
		c := src[i]
		if c == '%' || c == '_' {
			dst.WriteByte('\\')
			dst.WriteByte(c)
			i++
			dstlen--
		} else {
			dst.WriteByte(c)
			i++
		}
	}
	dst.WriteByte('/')
	dst.WriteByte('%')
	return dst.String()
}

// DBExplain — C: db_explain (db_support.c:554-585).
func DBExplain(stmt *Stmt) int {
	zSql := stmt.SQL()
	if zSql == "" {
		return SQLITE_ERROR
	}

	stmt.db.ts.Trace(trace.TRACE_DEBUG, "EXPLAIN", "%s", zSql)

	zExplain := fmt.Sprintf("EXPLAIN QUERY PLAN %s", zSql)

	db := stmt.DBHandle()
	pExplain, _, rc := db.PrepareV2(zExplain)
	if rc != SQLITE_OK {
		return rc
	}

	for pExplain.Step() == SQLITE_ROW {
		iSelectid := pExplain.ColumnInt(0)
		iOrder := pExplain.ColumnInt(1)
		iFrom := pExplain.ColumnInt(2)
		zDetail := pExplain.ColumnText(3)

		stmt.db.ts.Trace(trace.TRACE_DEBUG, "EXPLAIN", "%d %d %d %s",
			iSelectid, iOrder, iFrom, zDetail)
	}

	return pExplain.Finalize()
}

// DBStart — C: db_init (db_support.c:719-734). ENABLE_SQLITE_LOCKING is
// off → no SQLITE_CONFIG_MUTEX; PS3 soft-heap-limit skipped;
// memlogger callout is behind if(0) upstream.
func DBStart(cachePath string) {
	mlSetTempDirectory(cachePath)
	mlConfigLog()
	sqlite3Initialize()
}
