// Canonical 1:1 port of src/ecmascript/es_sqlite.c — per-plugin sqlite
// database resource (databases/<name>), query/step model, upgradeSchema.
//
// C operates on a raw sqlite3*/sqlite3_stmt* — this port uses the raw
// cgo sqlite3 binding in pkg/db (db.DB = sqlite3*, db.Stmt = sqlite3_stmt*).
package ecmascript

import (
	"math"

	"github.com/czz/movian-go/internal/db"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/trace"
)

// ---------------------------------------------------------------------------
// es_sqlite_t — C: es_sqlite.c:27-37
// ---------------------------------------------------------------------------

// C: es_sqlite_t
type esSqlite struct {
	super  *ESResource
	esName string

	esDB          *db.DB   // C: sqlite3 *es_db
	esStmt        *db.Stmt // C: sqlite3_stmt *es_stmt
	esStepRC      int      // C: es_step_rc
	esTransaction int      // C: es_transaction
	esDebug       int      // C: es_debug
}

// esSqliteDestroy — C: es_sqlite_destroy (es_sqlite.c:44-59)
func esSqliteDestroy(eres *ESResource) {
	es := eres.Data.(*esSqlite)

	if es.esStmt != nil {
		es.esStmt.Finalize()
	}

	if es.esDB != nil {
		es.esDB.Close()
	}

	if es.esDebug != 0 {
		esEnv.tracer.Trace(trace.TRACE_DEBUG, "JS", "Database %s finalized",
			es.esName)
	}

	EsResourceUnlink(es.super)
}

// esSqliteInfo — C: es_sqlite_info (es_sqlite.c:66-71)
func esSqliteInfo(eres *ESResource) string {
	es := eres.Data.(*esSqlite)
	s := es.esName
	if es.esTransaction != 0 {
		s += ", in transaction"
	}
	return s
}

// C: es_resource_sqlite (es_sqlite.c:77-82)
var esResourceSqlite = &ESResourceClass{
	ErcName:    "sqlite",
	ErcDestroy: esSqliteDestroy,
	ErcInfo:    esSqliteInfo,
}

// esSqliteCreate — C: es_sqlite_create (es_sqlite.c:89-118)
func esSqliteCreate(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	name := ctx.SafeToString(0)

	// Create the db-dir for this plugin
	path := ec.ecStorage + "/databases"

	if err := facore.Makedirs(esEnv.fam,
		path); err != nil {
		ctx.Error(gaftape.GAF_ERR_ERROR,
			"Unable to create directory %s -- %s", path, err.Error())
	}

	path = ec.ecStorage + "/databases/" + name

	dbh := db.DBOpen(path, 0, esEnv.tracer)
	if dbh == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR,
			"Unable to open database -- check logs")
	}

	es := &esSqlite{}
	es.super = EsResourceCreate(ec, esResourceSqlite, 0, es).(*ESResource)

	es.esDB = dbh
	es.esName = name

	EsResourcePush(ctx, es.super)
	return 1
}

// esSqliteStmtStep — C: es_sqlite_stmt_step (es_sqlite.c:125-144)
func esSqliteStmtStep(ctx *gaftape.Context, es *esSqlite) {
	es.esStepRC = db.DBStep(es.esStmt)

	if es.esTransaction != 0 && es.esStepRC == db.SQLITE_LOCKED {
		ctx.Error(ST_ERROR_SQLITE_BASE|es.esStepRC, "Deadlock")
	}

	if es.esStepRC == db.SQLITE_ROW || es.esStepRC == db.SQLITE_DONE {
		if es.esStmt.DataCount() == 0 {
			// No data to be returned, close stmt
			es.esStmt.Finalize()
			es.esStmt = nil
		}
	} else {
		ctx.Error(ST_ERROR_SQLITE_BASE|es.esStepRC,
			"Sqlite error 0x%x -- %s",
			es.esStepRC, es.esDB.Errmsg())
	}
}

// esSqliteQuery — C: es_sqlite_query (es_sqlite.c:151-196)
func esSqliteQuery(ctx *gaftape.Context) int {
	argc := ctx.GetTop()
	if argc < 2 {
		return -5 // DUK_RET_TYPE_ERROR
	}

	r := EsResourceGet(ctx, 0, esResourceSqlite)
	es := r.(*ESResource).Data.(*esSqlite)
	query := ctx.SafeToString(1)

	if es.esStmt != nil {
		es.esStmt.Finalize()
		es.esStmt = nil
	}

	var rc int
	es.esStmt, rc = db.DBPrepare(es.esDB, query)

	if rc != db.SQLITE_OK {
		if es.esTransaction != 0 && rc == db.SQLITE_LOCKED {
			ctx.Error(ST_ERROR_SQLITE_BASE|rc, "Deadlock")
		}
		ctx.Error(ST_ERROR_SQLITE_BASE|rc,
			"Sqlite error 0x%x -- %s", rc, es.esDB.Errmsg())
	}

	stmt := es.esStmt

	for i := 2; i < argc; i++ {
		sqliteArg := i - 1

		if ctx.IsNullOrUndefined(i) {
			stmt.BindNull(sqliteArg)
		} else if ctx.IsNumber(i) {
			stmt.BindDouble(sqliteArg, ctx.GetNumber(i))
		} else if ctx.IsBoolean(i) {
			b := 0
			if ctx.GetBoolean(i) {
				b = 1
			}
			stmt.BindInt(sqliteArg, b)
		} else {
			stmt.BindText(sqliteArg, ctx.SafeToString(i))
		}
	}

	esSqliteStmtStep(ctx, es)
	return 0
}

// esSqliteStep — C: es_sqlite_step (es_sqlite.c:204-245)
func esSqliteStep(ctx *gaftape.Context) int {
	r := EsResourceGet(ctx, 0, esResourceSqlite)
	es := r.(*ESResource).Data.(*esSqlite)

	if es.esStmt == nil {
		ctx.PushNull()
		return 1
	}

	cols := es.esStmt.DataCount()

	ctx.PushObject()

	for i := range cols {
		switch es.esStmt.ColumnType(i) {

		case db.SQLITE_INTEGER:
			i64 := es.esStmt.ColumnInt64(i)
			if i64 >= math.MinInt32 && i64 <= math.MaxInt32 {
				ctx.PushInt(int(i64))
			} else if i64 >= 0 && i64 <= math.MaxUint32 {
				ctx.PushUint(uint32(i64))
			} else {
				ctx.PushNumber(float64(i64))
			}
		case db.SQLITE_TEXT:
			ctx.PushString(es.esStmt.ColumnText(i))
		case db.SQLITE_FLOAT:
			ctx.PushNumber(es.esStmt.ColumnDouble(i))
		default:
			continue
		}
		ctx.PutPropString(-2, es.esStmt.ColumnName(i))
	}
	esSqliteStmtStep(ctx, es)
	return 1
}

// esDBChanges — C: es_db_changes (es_sqlite.c:253-257)
func esDBChanges(ctx *gaftape.Context) int {
	r := EsResourceGet(ctx, 0, esResourceSqlite)
	es := r.(*ESResource).Data.(*esSqlite)
	ctx.PushInt(es.esDB.Changes())
	return 1
}

// esDBLastErrorCode — C: es_db_last_error_code (es_sqlite.c:265-269)
func esDBLastErrorCode(ctx *gaftape.Context) int {
	r := EsResourceGet(ctx, 0, esResourceSqlite)
	es := r.(*ESResource).Data.(*esSqlite)
	ctx.PushInt(es.esDB.Errcode())
	return 1
}

// esDBLastErrorStr — C: es_db_last_error_str (es_sqlite.c:277-281)
func esDBLastErrorStr(ctx *gaftape.Context) int {
	r := EsResourceGet(ctx, 0, esResourceSqlite)
	es := r.(*ESResource).Data.(*esSqlite)
	ctx.PushString(es.esDB.Errmsg())
	return 1
}

// esDBLastInsertRowID — C: es_db_last_insert_row_id
// (es_sqlite.c:289-294)
func esDBLastInsertRowID(ctx *gaftape.Context) int {
	r := EsResourceGet(ctx, 0, esResourceSqlite)
	es := r.(*ESResource).Data.(*esSqlite)
	ctx.PushUint(uint32(es.esDB.LastInsertRowid()))
	return 1
}

// esDBUpgradeSchema — C: es_db_upgrade_schema (es_sqlite.c:301-311)
func esDBUpgradeSchema(ctx *gaftape.Context) int {
	r := EsResourceGet(ctx, 0, esResourceSqlite)
	es := r.(*ESResource).Data.(*esSqlite)

	rc := db.DBUpgradeSchema(es.esDB, ctx.SafeToString(1),
		es.esName, esEnv.fam, "", "")

	if rc != 0 {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Unable to upgrade schema")
	}
	return 0
}

// ---------------------------------------------------------------------------
// fnlist_sqlite — C: es_sqlite.c:318-328
// ---------------------------------------------------------------------------

var esFnlistSqlite = []gaftape.FunctionListEntry{
	{Key: "create", Value: esSqliteCreate, Nargs: 1},
	{Key: "query", Value: esSqliteQuery, Nargs: -1},
	{Key: "changes", Value: esDBChanges, Nargs: 1},
	{Key: "step", Value: esSqliteStep, Nargs: 1},
	{Key: "lastErrorCode", Value: esDBLastErrorCode, Nargs: 1},
	{Key: "lastErrorString", Value: esDBLastErrorStr, Nargs: 1},
	{Key: "lastRowId", Value: esDBLastInsertRowID, Nargs: 1},
	{Key: "upgradeSchema", Value: esDBUpgradeSchema, Nargs: 2},
}

// C: ES_MODULE("sqlite", fnlist_sqlite)
func registerEsSqlite() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "sqlite",
		Functions: esFnlistSqlite,
	})
}
