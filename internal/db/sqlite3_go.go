// The sqlite3 cgo binding was removed entirely — modernc.org/sqlite is
// the only implementation (real log/unlock_notify callbacks verified,
// WAL concurrency tested, zero libsqlite3 dependency).

// Pure-Go DB/Stmt implementation over modernc.org/sqlite — the
// transpiled-to-Go SQLite, which is the upstream C code compiled to Go
// (ccgo). Semantics are therefore identical to libsqlite3; only the
// binding layer differs. This is the only implementation on all
// architectures — the cgo libsqlite3 variant and the vendored
// amalgamation (third_party/sqlite, scripts/build_sqlite_android.sh)
// were removed after identical-behavior verification.
//
// C-callback parity note: modernc transpiled code dereferences a "C
// function pointer" as a Go func value stored in a struct's first
// field. Passing the func-value bits of a package-level (non-capturing
// — static closure, immortal) function therefore installs a real
// callback: SQLITE_CONFIG_LOG and sqlite3_unlock_notify both work
// exactly as in the cgo variant.

package db

import (
	"runtime/cgo"
	"sync"
	"unsafe"

	"github.com/czz/movian-go/internal/trace"
	"modernc.org/libc"
	lib "modernc.org/sqlite/lib"
)

// sqliteTransient — SQLITE_TRANSIENT destructor sentinel ((void*)-1).
const sqliteTransient = ^uintptr(0)

// tlsPool — modernc TLS objects are not goroutine-shareable but are
// safely reusable after the call completes (each call leaves no
// dangling C-space state). Pooling avoids a libc.NewTLS/Close pair on
// every sqlite operation.
var tlsPool = sync.Pool{New: func() any { return libc.NewTLS() }}

// withTLS runs f under a pooled modernc TLS (tls is not
// goroutine-shareable). Mirrors the cgo call boundary.
func withTLS(f func(tls *libc.TLS) int) int {
	tls := tlsPool.Get().(*libc.TLS)
	defer tlsPool.Put(tls)
	return int(f(tls))
}

// cLoadUintptr reads a uintptr out-param from C-space memory.
// AssignAddPtrUintptr(p, 0) dereferences inside the transpiled heap —
// the vet-clean equivalent of dereferencing a raw C out-pointer.
func cLoadUintptr(p uintptr) uintptr { return libc.AssignAddPtrUintptr(p, 0) }

// mlSetTempDirectory / mlConfigLog / sqlite3Initialize — memlogger
// callouts used by db_init (db_support.c:719-734).
func mlSetTempDirectory(path string) {
	// intentionally leaked — mirrors C's static global assignment
	cpath, _ := libc.CString(path)
	lib.Xsqlite3_temp_directory = cpath
}

// goDBLog — C: db_log (db_support.c:678-690), reached through
// sqlite3_config(SQLITE_CONFIG_LOG). Callback ABI: the transpiled
// sqlite3_log invokes xLog as func(tls, pArg, iErrCode, zMsg).
func goDBLog(t *libc.TLS, pArg uintptr, code int32, zMsg uintptr) {
	nonExtendedCode := int(code) & 0xff
	// Some codes are nothing to worry about as we or sqlite
	// retries internally
	if nonExtendedCode == SQLITE_CONSTRAINT ||
		int(code) == SQLITE_LOCKED_SHAREDCACHE ||
		nonExtendedCode == SQLITE_SCHEMA {
		return
	}
	dbLogTrace(int(code) == 0, "SQLITE", "%s (code: 0x%x)",
		libc.GoString(zMsg), int(code))
}

// goDBLogFn — the func-value bits of goDBLog = a C-callable pointer per
// the modernc convention (non-capturing func → static closure).
var goDBLogFn = *(*uintptr)(unsafe.Pointer(&struct {
	f func(*libc.TLS, uintptr, int32, uintptr)
}{goDBLog}))

// mlConfigLog — C: sqlite3_config(SQLITE_CONFIG_LOG, db_log, NULL)
// (db_support.c:719-734).
func mlConfigLog() {
	withTLS(func(t *libc.TLS) int {
		bp := libc.Xmalloc(t, 16)
		defer libc.Xfree(t, bp)
		return int(lib.Xsqlite3_config(t, lib.SQLITE_CONFIG_LOG,
			libc.VaList(bp, goDBLogFn, uintptr(0))))
	})
}

func sqlite3Initialize() { withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_initialize(t)) }) }

// dbOpenRaw — the sqlite3_open_v2 seam used by DBOpen.
func dbOpenRaw(path string, ts *trace.TraceSystem) (*DB, int) {
	db := &DB{ts: ts}
	fspath := dbFSPath(path)
	rc := withTLS(func(t *libc.TLS) int {
		cpath, _ := libc.CString(fspath)
		defer libc.Xfree(t, cpath)
		pp := libc.Xmalloc(t, 8)
		defer libc.Xfree(t, pp)
		rc := int(lib.Xsqlite3_open_v2(t, cpath, pp,
			lib.SQLITE_OPEN_READWRITE|lib.SQLITE_OPEN_CREATE|
				lib.SQLITE_OPEN_NOMUTEX|lib.SQLITE_OPEN_SHAREDCACHE, 0))
		db.p = cLoadUintptr(pp)
		return rc
	})
	return db, rc
}

// dbLogTrace — TRACE() from C's db_log (kept for API parity; unused —
// the SQLITE_CONFIG_LOG hook cannot be registered under modernc).
func dbLogTrace(info bool, subsys, format string, args ...any) {
	level := trace.TRACE_ERROR
	if info {
		level = trace.TRACE_INFO
	}
	dbLogTS.ts.Trace(level, subsys, format, args...)
}

// goUnlockNotify — C: unlock_notify_cb (db_support.c:37-47).
// ppArg is the C-space array of pArgs we registered; each element is a
// cgo.Handle value pointing at an *unlockNotify (same scheme as the
// cgo variant).
func goUnlockNotify(t *libc.TLS, ppArg uintptr, nArg int32) {
	for i := int32(0); i < nArg; i++ {
		h := cgo.Handle(cLoadUintptr(ppArg))
		ppArg += 8
		un := h.Value().(*unlockNotify)
		un.mu.Lock()
		un.fired = true
		un.cond.Signal()
		un.mu.Unlock()
	}
}

var goUnlockNotifyFn = *(*uintptr)(unsafe.Pointer(&struct {
	f func(*libc.TLS, uintptr, int32)
}{goUnlockNotify}))

// waitForUnlockNotify — C: wait_for_unlock_notify (db_support.c:49-73).
func waitForUnlockNotify(db *DB) int {
	un := &unlockNotify{}
	un.cond = sync.NewCond(&un.mu)

	h := cgo.NewHandle(un)
	rc := withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_unlock_notify(t, db.p,
			goUnlockNotifyFn, uintptr(h)))
	})

	if rc == SQLITE_OK {
		un.mu.Lock()
		for !un.fired {
			un.cond.Wait()
		}
		un.mu.Unlock()
	}
	h.Delete()
	return rc
}

// DB — C: sqlite3 *.
type DB struct {
	p  uintptr
	ts *trace.TraceSystem // C: trace() global — injected at open
}

// TraceSystem returns the injected trace system (nil until wired).
// Nil-receiver safe.
func (db *DB) TraceSystem() *trace.TraceSystem {
	if db == nil {
		return nil
	}
	return db.ts
}

// Stmt — C: sqlite3_stmt *.
type Stmt struct {
	p  uintptr
	db *DB // Go-only backref — C reads the implicit trace/db context
}

// sqlite3 result codes
const (
	SQLITE_OK       = 0
	SQLITE_ERROR    = 1
	SQLITE_INTERNAL = 2
	SQLITE_PERM     = 3
	SQLITE_ABORT    = 4
	SQLITE_BUSY     = 5
	SQLITE_LOCKED   = 6
	SQLITE_NOMEM    = 7
	SQLITE_READONLY = 8
	SQLITE_ROW      = 100
	SQLITE_DONE     = 101

	// extended
	SQLITE_LOCKED_SHAREDCACHE = 262
	SQLITE_CONSTRAINT         = 19
	SQLITE_SCHEMA             = 17

	// column types
	SQLITE_INTEGER = 1
	SQLITE_FLOAT   = 2
	SQLITE_TEXT    = 3
	SQLITE_BLOB    = 4
	SQLITE_NULL    = 5

	// db_open flags (db_support.h)
	DBOpenCaseSensitiveLike = 0x1 // DB_OPEN_CASE_SENSITIVE_LIKE
)

// --- raw sqlite3 wrappers used by db_support.go and consumers ---

func (db *DB) Close() int {
	return withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_close(t, db.p)) })
}

func (db *DB) Exec(sql string, errmsg *string) int {
	return withTLS(func(t *libc.TLS) int {
		csql, _ := libc.CString(sql)
		defer libc.Xfree(t, csql)
		var cerr uintptr
		pErr := libc.Xmalloc(t, 8)
		defer libc.Xfree(t, pErr)
		rc := int(lib.Xsqlite3_exec(t, db.p, csql, 0, 0, pErr))
		if cerr = cLoadUintptr(pErr); cerr != 0 {
			if errmsg != nil {
				*errmsg = libc.GoString(cerr)
			}
			lib.Xsqlite3_free(t, cerr)
		}
		return rc
	})
}

func (db *DB) Errmsg() string {
	var s string
	withTLS(func(t *libc.TLS) int {
		s = libc.GoString(lib.Xsqlite3_errmsg(t, db.p))
		return 0
	})
	return s
}

func (db *DB) PrepareV2(sql string) (*Stmt, string, int) {
	var stmt uintptr
	var tailStr string
	rc := withTLS(func(t *libc.TLS) int {
		csql, _ := libc.CString(sql)
		defer libc.Xfree(t, csql)
		pp := libc.Xmalloc(t, 8)
		defer libc.Xfree(t, pp)
		pTail := libc.Xmalloc(t, 8)
		defer libc.Xfree(t, pTail)
		rc := int(lib.Xsqlite3_prepare_v2(t, db.p, csql, -1, pp, pTail))
		stmt = cLoadUintptr(pp)
		if tail := cLoadUintptr(pTail); tail != 0 {
			tailStr = libc.GoString(tail)
		}
		return rc
	})
	return &Stmt{p: stmt, db: db}, tailStr, rc
}

func (db *DB) LastInsertRowid() int64 {
	var r int64
	withTLS(func(t *libc.TLS) int {
		r = int64(lib.Xsqlite3_last_insert_rowid(t, db.p))
		return 0
	})
	return r
}

func (db *DB) Changes() int {
	return withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_changes(t, db.p)) })
}

func (db *DB) GetAutocommit() int {
	return withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_get_autocommit(t, db.p)) })
}

func (db *DB) Errcode() int {
	return withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_errcode(t, db.p)) })
}

func (s *Stmt) Step() int {
	return withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_step(t, s.p)) })
}

func (s *Stmt) Reset() int {
	return withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_reset(t, s.p)) })
}

func (s *Stmt) Finalize() int {
	return withTLS(func(t *libc.TLS) int { return int(lib.Xsqlite3_finalize(t, s.p)) })
}

func (s *Stmt) DBHandle() *DB {
	return s.db // C: sqlite3_db_handle — same underlying connection
}

func (s *Stmt) SQL() string {
	var out string
	withTLS(func(t *libc.TLS) int {
		out = libc.GoString(lib.Xsqlite3_sql(t, s.p))
		return 0
	})
	return out
}

func (s *Stmt) BindText(i int, v string) int {
	return withTLS(func(t *libc.TLS) int {
		cs, _ := libc.CString(v)
		defer libc.Xfree(t, cs)
		return int(lib.Xsqlite3_bind_text(t, s.p, int32(i), cs, -1,
			sqliteTransient))
	})
}

func (s *Stmt) BindInt(i, v int) int {
	return withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_bind_int(t, s.p, int32(i), int32(v)))
	})
}

func (s *Stmt) BindInt64(i int, v int64) int {
	return withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_bind_int64(t, s.p, int32(i), v))
	})
}

func (s *Stmt) BindDouble(i int, v float64) int {
	return withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_bind_double(t, s.p, int32(i), v))
	})
}

func (s *Stmt) BindNull(i int) int {
	return withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_bind_null(t, s.p, int32(i)))
	})
}

func (s *Stmt) ColumnInt(col int) int {
	return withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_column_int(t, s.p, int32(col)))
	})
}

func (s *Stmt) ColumnInt64(col int) int64 {
	var r int64
	withTLS(func(t *libc.TLS) int {
		r = int64(lib.Xsqlite3_column_int64(t, s.p, int32(col)))
		return 0
	})
	return r
}

func (s *Stmt) ColumnDouble(col int) float64 {
	var r float64
	withTLS(func(t *libc.TLS) int {
		r = float64(lib.Xsqlite3_column_double(t, s.p, int32(col)))
		return 0
	})
	return r
}

func (s *Stmt) ColumnText(col int) string {
	var out string
	withTLS(func(t *libc.TLS) int {
		if p := lib.Xsqlite3_column_text(t, s.p, int32(col)); p != 0 {
			out = libc.GoString(p)
		}
		return 0
	})
	return out
}

func (s *Stmt) ColumnType(col int) int {
	return withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_column_type(t, s.p, int32(col)))
	})
}

func (s *Stmt) ColumnName(col int) string {
	var out string
	withTLS(func(t *libc.TLS) int {
		out = libc.GoString(lib.Xsqlite3_column_name(t, s.p, int32(col)))
		return 0
	})
	return out
}

func (s *Stmt) DataCount() int {
	return withTLS(func(t *libc.TLS) int {
		return int(lib.Xsqlite3_data_count(t, s.p))
	})
}
