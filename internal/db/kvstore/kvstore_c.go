package kvstore

// Canonical port of src/db/kvstore.c — global kvstore pool, deferred writes,
// url/url_kv schema, kv_prop_bind_create prop tracking and kv_url_opt_*
// accessors.
//
// C: kvstore.c (CONFIG_KVSTORE build path — enabled by default).

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/db"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: kvstore.h — domain constants
const (
	DomainSys     = 1 // C: KVSTORE_DOMAIN_SYS
	DomainProp    = 2 // C: KVSTORE_DOMAIN_PROP
	DomainPlugin  = 3 // C: KVSTORE_DOMAIN_PLUGIN
	DomainSetting = 4 // C: KVSTORE_DOMAIN_SETTING
)

// C: kvstore.h — set types
const (
	SetString   = 1     // C: KVSTORE_SET_STRING
	SetInt      = 2     // C: KVSTORE_SET_INT
	SetVoid     = 3     // C: KVSTORE_SET_VOID
	SetInt64    = 4     // C: KVSTORE_SET_INT64
	Unimportant = 0x100 // C: KVSTORE_UNIMPORTANT
)

// kvstoreWrite — C: kvstore_write_t (kvstore.c:42-56).
type kvstoreWrite struct {
	kvs         *KVStore // Go-only backref — C reads globals
	url         string   // C: kw_url
	key         string   // C: kw_key
	domain      int      // C: kw_domain
	typ         int      // C: kw_type
	unimportant int      // C: kw_unimportant
	strVal      string   // C: kw_string
	hasStr      bool     // C: kw_string != NULL
	intVal      int      // C: kw_int
	int64Val    int64    // C: kw_int64
}

// KVStore — C: static db_pool_t *kvstore_pool + deferred_writes +
// deferred_mutex + deferred_callout. Grouped store state; instances are
// created by NewKVStore and injected into consumers (kvstore_init).
type KVStore struct {
	pool          *db.DBPool
	calloutSystem *callout.CalloutSystem
	mu            sync.Mutex      // C: deferred_mutex
	writes        []*kvstoreWrite // C: kvstore_write_list deferred_writes
	callout       *callout.Callout
	fam           *fileaccesscore.FileAccessManager // C: implicit global fa context
	ts            *trace.TraceSystem                // C: trace() global — injected
	gconf         *gconf.T                          // C: gconf_t — injected
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (kvs *KVStore) SetGconf(g *gconf.T) { kvs.gconf = g }

// Gconf — C: gconf_t as read by kvstore consumers.
func (kvs *KVStore) Gconf() *gconf.T { return kvs.gcfg() }

// gcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (kvs *KVStore) gcfg() *gconf.T {
	if kvs != nil && kvs.gconf != nil {
		return kvs.gconf
	}
	return gconf.New()
}

// C: domain_to_name[] — used only by the xattr paths.
var domainToName = map[int]string{
	DomainSys:     "sys",
	DomainProp:    "prop",
	DomainPlugin:  "plugin",
	DomainSetting: "setting",
}

// EnableKvstoreDebug — C: gconf.enable_kvstore_debug — default 0.

// Fini — C: kvstore_fini (kvstore.c:77-80).
func (kvs *KVStore) Fini() {
	if kvs == nil {
		return
	}
	db.DBPoolClose(kvs.pool)
	kvs.pool = nil
}

// get — C: kvstore_get (kvstore.c:87-90).
func (kvs *KVStore) get() *db.DB {
	if kvs == nil {
		return nil
	}
	return db.DBPoolGet(kvs.pool)
}

// close — C: kvstore_close (kvstore.c:97-100).
func (kvs *KVStore) close(dbc *db.DB) {
	db.DBPoolPut(kvs.pool, dbc)
}

// NewKVStore — C: kvstore_init (kvstore.c:106-133).
// persistentPath = gconf.persistent_path, dataRoot = app_dataroot().
func NewKVStore(persistentPath, dataRoot string,
	cs *callout.CalloutSystem, fam *fileaccesscore.FileAccessManager) *KVStore {
	kvs := &KVStore{calloutSystem: cs, callout: &callout.Callout{}, fam: fam, ts: fam.TraceSystem()}

	fileaccesscore.FAMakedir(fam, persistentPath+"/kvstore")

	kvs.pool = db.DBPoolCreate(persistentPath+"/kvstore/kvstore.db", 4, fam.TraceSystem())
	dbc := kvs.get()
	if dbc == nil {
		return kvs
	}

	r := db.DBUpgradeSchema(dbc, dataRoot+"/res/kvstore", "kvstore", fam, "", "")

	kvs.close(dbc)

	if r != 0 {
		kvs.pool = nil // Disable
	}
	return kvs
}

// kvPropBind — C: kv_prop_bind_t (kvstore.c:137-140).
type kvPropBind struct {
	url string // C: kpb_url (rstr_t *)
	id  int64  // C: kpb_id — -1 until get_url resolves it
	sub *propcore.Subscription
	kvs *KVStore // injected store — C reaches the kvstore_* globals
}

// kvPropBindValue — C: kv_prop_bind_value_t (kvstore.c:143-147).
type kvPropBindValue struct {
	name string // C: kpbv_name (rstr_t *)
	url  string // C: kpbv_url (rstr_t *)
	id   int64  // C: kpbv_id — ID of row in URL table
	kpb  *kvPropBind
	sub  *propcore.Subscription
}

// getURL — C: get_url (kvstore.c:154-201). Returns SQLITE_* code;
// id is written on SQLITE_OK.
func getURL(dbc *db.DB, url string, id *int64, g *gconf.T) int {
	stmt, rc := db.DBPrepare(dbc, "SELECT id FROM url WHERE url=?1")
	if rc != db.SQLITE_OK {
		return rc
	}

	stmt.BindText(1, url)

	rc = stmt.Step()
	if rc == db.SQLITE_LOCKED {
		stmt.Finalize()
		return db.SQLITE_LOCKED
	}
	if rc == db.SQLITE_ROW {
		*id = stmt.ColumnInt64(0)
		stmt.Finalize()
		return db.SQLITE_OK
	} else if rc == db.SQLITE_DONE {
		stmt.Finalize()

		stmt, rc = db.DBPrepare(dbc,
			"INSERT INTO url ('url') VALUES (?1)")
		if rc != db.SQLITE_OK {
			return rc
		}

		stmt.BindText(1, url)

		rc = db.DBStep(stmt)
		if rc == db.SQLITE_DONE {
			*id = dbc.LastInsertRowid()
			rc = db.SQLITE_OK

			if g.EnableKvstoreDebug.Load() {
				dbc.TraceSystem().Trace(trace.TRACE_DEBUG, "kvstore",
					"Created row %d for URL %s", int(*id), url)
			}
		}
	}
	stmt.Finalize()
	return rc
}

// kpbvDestroy — C: kpbv_destroy (kvstore.c:209-214).
func kpbvDestroy(kpbv *kvPropBindValue) {
	// C: rstr_release(name) + rstr_release(url) + free — GC handles.
}

// kvValueCb — C: kv_value_cb (kvstore.c:220-332). Per-child value
// subscription: persists value changes into url_kv (domain PROP).
func kvValueCb(opaque any, event propcore.EventType,
	args ...any) {
	kpbv := opaque.(*kvPropBindValue)
	kvs := kpbv.kpb.kvs

	switch event {

	case propcore.EventDestroyed:
		// C: prop_unsubscribe(va_arg(ap, prop_sub_t *)); kpbv_destroy
		for _, a := range args {
			if s, ok := a.(*propcore.Subscription); ok {
				s.Unsubscribe()
			}
		}
		kpbvDestroy(kpbv)

	case propcore.EventSetVoid, propcore.EventSetRString,
		propcore.EventSetCString, propcore.EventSetInt,
		propcore.EventSetFloat:

		if kpbv.name == "" {
			break
		}

		dbc := kvs.get()
		if dbc == nil {
			break
		}

	again:
		if db.DBBegin(dbc) != 0 {
			break
		}

		if kpbv.id == -1 {
			rc := getURL(dbc, kpbv.url, &kpbv.id, kvs.gcfg())
			if rc == db.SQLITE_LOCKED {
				db.DBRollbackDeadlock(dbc)
				goto again
			}
			if rc != db.SQLITE_OK {
				db.DBRollback(dbc)
				kvs.close(dbc)
				return
			}
		}

		var stmt *db.Stmt
		var rc int
		if event == propcore.EventSetVoid {
			stmt, rc = db.DBPrepare(dbc, "DELETE FROM url_kv "+
				"WHERE url_id = ?1 "+
				"AND domain = ?4 "+
				"AND key = ?2")
		} else {
			stmt, rc = db.DBPrepare(dbc,
				"INSERT OR REPLACE INTO url_kv "+
					"(url_id, domain, key, value) "+
					"VALUES "+
					"(?1, ?4, ?2, ?3)")
		}

		if rc != db.SQLITE_OK {
			db.DBRollback(dbc)
			kvs.close(dbc)
			return
		}

		stmt.BindInt64(1, kpbv.id)
		stmt.BindInt(4, DomainProp)

		switch event {
		case propcore.EventSetRString, propcore.EventSetCString:
			if len(args) > 0 {
				if v, ok := args[0].(string); ok {
					stmt.BindText(3, v)
				}
			}
		case propcore.EventSetInt:
			if len(args) > 0 {
				if v, ok := args[0].(int); ok {
					stmt.BindInt(3, v)
				}
			}
		case propcore.EventSetFloat:
			if len(args) > 0 {
				switch v := args[0].(type) {
				case float32:
					stmt.BindDouble(3, float64(v))
				case float64:
					stmt.BindDouble(3, v)
				}
			}
		}

		stmt.BindText(2, kpbv.name)

		rc = stmt.Step()
		stmt.Finalize()

		if rc == db.SQLITE_LOCKED {
			db.DBRollbackDeadlock(dbc)
			goto again
		}
		db.DBCommit(dbc)
		kvs.close(dbc)
	}
}

// kpbDestroy — C: kpb_destroy (kvstore.c:340-344).
func kpbDestroy(kpb *kvPropBind) {
	// C: rstr_release(url) + free — GC handles.
}

// kvCb — C: kv_cb (kvstore.c:351-414). Parent subscription on the
// bound prop: creates per-child value subscriptions.
func kvCb(opaque any, event propcore.EventType, args ...any) {
	kpb := opaque.(*kvPropBind)

	switch event {
	case propcore.EventAddChild, propcore.EventAddChildBefore:
		if len(args) == 0 {
			return
		}
		p, ok := args[0].(*propcore.Prop)
		if !ok {
			return
		}
		kpbv := &kvPropBindValue{
			id:  kpb.id,
			url: kpb.url,
			kpb: kpb,
		}
		kpbv.name = p.GetName()
		kpbv.sub = p.Subscribe(kvValueCb, kpbv,
			propcore.SubFlagTrackDestroy)

	case propcore.EventAddChildVectorDirect:
		if len(args) == 0 {
			return
		}
		var children []*propcore.Prop
		switch pv := args[0].(type) {
		case *propcore.PropVec:
			for i := range pv.Len() {
				children = append(children, pv.Get(i))
			}
		case []*propcore.Prop:
			children = pv
		}
		for _, p := range children {
			kpbv := &kvPropBindValue{
				id:  kpb.id,
				url: kpb.url,
				kpb: kpb,
			}
			kpbv.name = p.GetName()
			kpbv.sub = p.Subscribe(kvValueCb, kpbv,
				propcore.SubFlagTrackDestroy|
					propcore.SubNoInitialUpdate|
					propcore.SubFlagDontLock)
		}

	case propcore.EventDestroyed:
		for _, a := range args {
			if s, ok := a.(*propcore.Subscription); ok {
				s.Unsubscribe()
			}
		}
		kpbDestroy(kpb)

	case propcore.EventDelChild, propcore.EventSetVoid,
		propcore.EventMoveChild, propcore.EventSetDir,
		propcore.EventReqDeleteVector, propcore.EventReqDelete,
		propcore.EventHaveMoreChildsYes, propcore.EventHaveMoreChildsNo,
		propcore.EventWantMoreChilds:
		// C: break;

	default:
		// C: printf("Cant handle event %d\n", event); abort();
		panic(fmt.Sprintf("kvstore: cant handle event %d", event))
	}
}

// KVPropBindCreate — C: kv_prop_bind_create (kvstore.c:421-481).
func (kvs *KVStore) PropBindCreate(p *propcore.Prop, url string) {
	if kvs == nil {
		return
	}
	var id int64 = -1

	dbc := kvs.get()
	if dbc == nil {
		return
	}

	stmt, rc := db.DBPrepare(dbc,
		"SELECT id,key,value "+
			"FROM url "+
			"LEFT OUTER JOIN url_kv ON id = url_id "+
			"WHERE url=?1 "+
			"AND domain=?2")

	if rc != db.SQLITE_OK {
		kvs.close(dbc)
		return
	}
	stmt.BindText(1, url)
	stmt.BindInt(2, DomainProp)

	for db.DBStep(stmt) == db.SQLITE_ROW {
		if id == -1 {
			id = stmt.ColumnInt64(0)
		}
		if stmt.ColumnType(1) != db.SQLITE_TEXT {
			continue
		}
		key := stmt.ColumnText(1)
		c := p.Manager().CreateEx(p, key, nil, false, false)
		switch stmt.ColumnType(2) {
		case db.SQLITE_TEXT:
			c.SetString(stmt.ColumnText(2))
		case db.SQLITE_INTEGER:
			c.SetInt(stmt.ColumnInt(2))
		case db.SQLITE_FLOAT:
			c.SetFloat(float32(stmt.ColumnDouble(2)))
		default:
			c.SetVoid()
		}
	}

	stmt.Finalize()
	kvs.close(dbc)

	kpb := &kvPropBind{id: id, url: url, kvs: kvs}
	kpb.sub = p.Subscribe(kvCb, kpb,
		propcore.SubFlagTrackDestroy|propcore.SubFlagDirectUpdate)
}

// kvUrlOptGet — C: kv_url_opt_get (kvstore.c:488-516). Returns the
// stmt positioned on the row, or nil.
func kvUrlOptGet(dbc *db.DB, url string, domain int, key string) *db.Stmt {
	if dbc == nil {
		return nil
	}

	stmt, rc := db.DBPrepare(dbc,
		"SELECT value "+
			"FROM url, url_kv "+
			"WHERE url=?1 "+
			"AND key = ?2 "+
			"AND domain = ?3 "+
			"AND url.id = url_id")

	if rc != db.SQLITE_OK {
		return nil
	}
	stmt.BindText(1, url)
	stmt.BindText(2, key)
	stmt.BindInt(3, domain)

	if db.DBStep(stmt) == db.SQLITE_ROW {
		return stmt
	}
	stmt.Finalize()
	return nil
}

// deferredGet — C: deferred_get (kvstore.c:525-535). Caller holds
// deferred_mutex.
func (kvs *KVStore) deferredGet(url string, domain int, key string) *kvstoreWrite {
	for _, kw := range kvs.writes {
		if kw.url == url && kw.key == key && kw.domain == domain {
			return kw
		}
	}
	return nil
}

// optGetEA — C: opt_get_ea (kvstore.c:542-553).
func optGetEA(url string, domain int, key string, g *gconf.T, fam *fileaccesscore.FileAccessManager) ([]byte, int) {
	if !fileaccesscore.FAKVStoreAsXattr(g) {
		return nil, fileaccesscore.FAP_NOT_SUPPORTED
	}

	ea := fmt.Sprintf("showtime.default.%s.%s",
		domainToName[domain], key)

	return fam.FAGetXattr(url, ea)
}

// KVUrlOptGetStringOK — C: kv_url_opt_get_rstr (kvstore.c:560-623).
// ok=false mirrors the C NULL rstr return.
func (kvs *KVStore) UrlOptGetStringOK(url string, domain int, key string) (string, bool) {
	if kvs == nil {
		return "", false
	}
	if url == "" {
		return "", false
	}

	kvs.mu.Lock()
	kw := kvs.deferredGet(url, domain, key)
	if kw != nil {
		var r string
		ok := true
		switch kw.typ {
		case SetInt:
			r = strconv.Itoa(kw.intVal)
		case SetInt64:
			r = strconv.FormatInt(kw.int64Val, 10)
		case SetString:
			r = kw.strVal
		default:
			ok = false
		}
		kvs.mu.Unlock()
		return r, ok
	}
	kvs.mu.Unlock()

	data, err := optGetEA(url, domain, key, kvs.gcfg(), kvs.fam)

	if err == 0 && len(data) > 0 {
		rval := string(data)
		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET XA url=%s key=%s domain=%d value=%s",
				url, key, domain, rval)
		}
		return rval, true
	}

	dbc := kvs.get()
	stmt := kvUrlOptGet(dbc, url, domain, key)
	var r string
	ok := false
	if stmt != nil {
		r = stmt.ColumnText(0)
		ok = true
		stmt.Finalize()
		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET DB url=%s key=%s domain=%d value=%s",
				url, key, domain, r)
		}
	} else {
		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET DB url=%s key=%s domain=%d value=UNSET",
				url, key, domain)
		}
	}
	kvs.close(dbc)
	return r, ok
}

// KVUrlOptGetString — C: kv_url_opt_get_rstr; "" when unset.
func (kvs *KVStore) UrlOptGetString(url string, domain int, key string) string {
	if kvs == nil {
		return ""
	}
	r, _ := kvs.UrlOptGetStringOK(url, domain, key)
	return r
}

// KVUrlOptGetInt — C: kv_url_opt_get_int (kvstore.c:630-693).
func (kvs *KVStore) UrlOptGetInt(url string, domain int, key string, def int) int {
	if kvs == nil {
		return def
	}
	if url == "" {
		return def
	}

	kvs.mu.Lock()
	kw := kvs.deferredGet(url, domain, key)
	if kw != nil {
		var r int
		switch kw.typ {
		case SetInt:
			r = kw.intVal
		case SetInt64:
			r = int(kw.int64Val)
		case SetString:
			r, _ = strconv.Atoi(kw.strVal)
		default:
			r = def
		}
		kvs.mu.Unlock()
		return r
	}
	kvs.mu.Unlock()

	data, err := optGetEA(url, domain, key, kvs.gcfg(), kvs.fam)

	if err == 0 && len(data) == 4 {
		rval := int(binary.BigEndian.Uint32(data))

		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET XA url=%s key=%s domain=%d value=%d",
				url, key, domain, rval)
		}
		return rval
	}

	dbc := kvs.get()
	stmt := kvUrlOptGet(dbc, url, domain, key)
	v := def
	if stmt != nil {
		v = stmt.ColumnInt(0)
		stmt.Finalize()
		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET DB url=%s key=%s domain=%d value=%d",
				url, key, domain, v)
		}
	} else {
		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET DB url=%s key=%s domain=%d value=UNSET",
				url, key, domain)
		}
	}
	kvs.close(dbc)
	return v
}

// KVUrlOptGetInt64 — C: kv_url_opt_get_int64 (kvstore.c:700-777).
func (kvs *KVStore) UrlOptGetInt64(url string, domain int, key string, def int64) int64 {
	if kvs == nil {
		return def
	}
	if url == "" {
		return def
	}

	kvs.mu.Lock()
	kw := kvs.deferredGet(url, domain, key)
	if kw != nil {
		var r int64
		switch kw.typ {
		case SetInt:
			r = int64(kw.intVal)
		case SetInt64:
			r = kw.int64Val
		case SetString:
			// C: strtoull(kw->kw_string, NULL, 10)
			u, _ := strconv.ParseUint(kw.strVal, 10, 64)
			r = int64(u)
		default:
			r = def
		}
		kvs.mu.Unlock()
		return r
	}
	kvs.mu.Unlock()

	data, err := optGetEA(url, domain, key, kvs.gcfg(), kvs.fam)

	if err == 0 {
		var rval int64
		ok := false

		if len(data) == 8 {
			rval = int64(binary.BigEndian.Uint64(data))
			ok = true
		} else if len(data) == 4 {
			rval = int64(binary.BigEndian.Uint32(data))
			ok = true
		}

		if ok {
			if kvs.gcfg().EnableKvstoreDebug.Load() {
				kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
					"GET XA url=%s key=%s domain=%d value=%d",
					url, key, domain, rval)
			}
			return rval
		}
	}

	dbc := kvs.get()
	stmt := kvUrlOptGet(dbc, url, domain, key)
	v := def
	if stmt != nil {
		v = stmt.ColumnInt64(0)
		stmt.Finalize()
		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET DB url=%s key=%s domain=%d value=%d",
				url, key, domain, v)
		}
	} else {
		if kvs.gcfg().EnableKvstoreDebug.Load() {
			kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
				"GET DB url=%s key=%s domain=%d value=UNSET",
				url, key, domain)
		}
	}
	kvs.close(dbc)
	return v
}

// kvWriteDB — C: kv_write_db (kvstore.c:784-852). Runs inside the
// flush transaction. Returns SQLITE_* code.
func kvWriteDB(dbc *db.DB, kw *kvstoreWrite, id int64, g *gconf.T) int {
	var stmt *db.Stmt
	var rc int
	var value string

	if kw.typ == SetVoid {
		stmt, rc = db.DBPrepare(dbc,
			"DELETE FROM url_kv "+
				"WHERE url_id = ?1 "+
				"AND key = ?2 "+
				"AND domain = ?3")

		if rc != db.SQLITE_OK {
			return rc
		}

		value = "[DELETED]"

	} else {

		stmt, rc = db.DBPrepare(dbc,
			"INSERT OR REPLACE INTO url_kv "+
				"(url_id, key, domain, value) "+
				"VALUES "+
				"(?1, ?2, ?3, ?4)")

		if rc != db.SQLITE_OK {
			return rc
		}

		switch kw.typ {
		case SetInt:
			stmt.BindInt(4, kw.intVal)
			value = strconv.Itoa(kw.intVal)
		case SetInt64:
			stmt.BindInt(4, int(kw.int64Val))
			value = strconv.FormatInt(kw.int64Val, 10)
		case SetString:
			stmt.BindText(4, kw.strVal)
			value = kw.strVal
		}
	}

	stmt.BindInt64(1, id)
	stmt.BindText(2, kw.key)
	stmt.BindInt(3, kw.domain)

	rc = stmt.Step()
	stmt.Finalize()

	if rc == db.SQLITE_DONE {
		rc = db.SQLITE_OK
	}

	if g.EnableKvstoreDebug.Load() {
		dbc.TraceSystem().Trace(trace.TRACE_DEBUG, "kvstore",
			"SET DB url=%s key=%s domain=%d value=%s rc=%d",
			kw.url, kw.key, kw.domain, value, rc)
	}
	return rc
}

// kvWriteXattr — C: kv_write_xattr (kvstore.c:859-909).
func kvWriteXattr(kw *kvstoreWrite, g *gconf.T, fam *fileaccesscore.FileAccessManager) int {
	var data []byte
	var value string

	ea := fmt.Sprintf("showtime.default.%s.%s",
		domainToName[kw.domain], kw.key)

	switch kw.typ {
	case SetInt:
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, uint32(kw.intVal))
		data = buf
		value = strconv.Itoa(kw.intVal)

	case SetInt64:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(kw.int64Val))
		data = buf
		value = strconv.FormatInt(kw.int64Val, 10)

	case SetString:
		data = []byte(kw.strVal)
		value = kw.strVal

	case SetVoid:
		data = nil
		value = "[DELETED]"
	default:
		panic("kvstore: kv_write_xattr bad type")
	}
	rc := fam.FASetXattr(kw.url, ea, data)

	if g.EnableKvstoreDebug.Load() {
		kw.kvs.ts.Trace(trace.TRACE_DEBUG, "kvstore",
			"SET XA url=%s key=%s domain=%d value=%s -- %s",
			kw.url, kw.key, kw.domain, value,
			fileaccesscore.FAErrCodeStr(rc))
	}

	if rc != 0 {
		return 1
	}
	return 0
}

// KVStoreDeferredFlush — C: kvstore_deferred_flush (kvstore.c:917-992).
func (kvs *KVStore) DeferredFlush() {
	if kvs == nil {
		return
	}
	var id uint64
	var currentURL string

	dbc := kvs.get()
	if dbc == nil {
		return
	}

	kvs.mu.Lock()

again:
	if db.DBBegin(dbc) != 0 {
		goto err
	}

	currentURL = ""

	for _, kw := range kvs.writes {

		if fileaccesscore.FAKVStoreAsXattr(kvs.gcfg()) {
			if kvWriteXattr(kw, kvs.gcfg(), kvs.fam) == 0 {
				continue
			}
		}

		// C: #ifdef STOS — if(kw->kw_unimportant) continue;
		//    (kvstore.c:944-947): unimportant writes never hit the db
		//    on the OS image (xattr-only or dropped).
		if mgosSkipUnimportant && kw.unimportant != 0 {
			continue
		}

		if currentURL == "" || kw.url != currentURL {
			var id64 int64
			rc := getURL(dbc, kw.url, &id64, kvs.gcfg())
			if rc == db.SQLITE_LOCKED {
				db.DBRollbackDeadlock(dbc)
				goto again
			}
			if rc != db.SQLITE_OK {
				db.DBRollback(dbc)
				goto err
			}
			id = uint64(id64)
			currentURL = kw.url
		}

		rc := kvWriteDB(dbc, kw, int64(id), kvs.gcfg())
		if rc == db.SQLITE_LOCKED {
			db.DBRollbackDeadlock(dbc)
			goto again
		}
		if rc != db.SQLITE_OK {
			db.DBRollback(dbc)
			goto err
		}
	}

	db.DBCommit(dbc)

err:
	// C: drain deferred_writes regardless of outcome
	kvs.writes = nil

	kvs.mu.Unlock()
	kvs.close(dbc)
}

// deferredCalloutFire — C: deferred_callout_fire (kvstore.c:999-1002).
func deferredCalloutFire(c *callout.Callout, opaque any) {
	if kvs, ok := opaque.(*KVStore); ok {
		kvs.DeferredFlush()
	}
}

// KVUrlOptSet — C: kv_url_opt_set (kvstore.c:1009-1061).
// value semantics by typ: SetInt → int, SetInt64 → int64,
// SetString → string (nil/non-string → SetVoid).
func (kvs *KVStore) UrlOptSet(url string, domain int, key string, typ int,
	value any) {
	if kvs == nil {
		return
	}

	kvs.mu.Lock()

	kw := kvs.deferredGet(url, domain, key)

	if kw == nil {
		kw = &kvstoreWrite{
			kvs:    kvs,
			url:    url,
			key:    key,
			domain: domain,
		}
		// C: LIST_INSERT_HEAD
		kvs.writes = slices.Insert(kvs.writes, 0, kw)
	}

	kw.typ = typ & 0xff
	kw.unimportant = typ & Unimportant

	switch kw.typ {
	case SetInt:
		kw.intVal, _ = value.(int)
	case SetInt64:
		kw.int64Val, _ = value.(int64)
	case SetString:
		// C: str == NULL → kw_type = KVSTORE_SET_VOID
		if s, ok := value.(string); ok {
			kw.strVal = s
			kw.hasStr = true
		} else {
			kw.typ = SetVoid
		}
	}

	kvs.mu.Unlock()

	// C: callout_arm(&deferred_callout, deferred_callout_fire, NULL, 1)
	if kvs.calloutSystem != nil {
		kvs.calloutSystem.Arm(kvs.callout, deferredCalloutFire, kvs, 1)
	}
}

// kvs.gcfg().EnableKvstoreDebug — C: gconf field (see devsettings binding).
