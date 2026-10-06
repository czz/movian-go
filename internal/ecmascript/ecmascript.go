// Package ecmascript is a canonical 1:1 port of src/ecmascript/ecmascript.c
// and ecmascript.h — the Duktape-based JavaScript context infrastructure,
// with the Duktape engine replaced by goja behind pkg/gaftape's stack API.
//
// C: src/ecmascript/ecmascript.c, src/ecmascript/ecmascript.h
package ecmascript

import (
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/db/kvstore"

	"github.com/czz/movian-go/internal/blobcache"
	"github.com/czz/movian-go/internal/keyring"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/notifications"

	"github.com/czz/movian-go/internal/asyncio"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/i18n"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
	"github.com/czz/movian-go/internal/version"
	"github.com/dop251/goja"
)

// C: ecmascript.h flags
const (
	ECMASCRIPT_DEBUG                 = 0x1
	ECMASCRIPT_FILE_BYPASS_ACL_READ  = 0x2
	ECMASCRIPT_FILE_BYPASS_ACL_WRITE = 0x4
	ECMASCRIPT_PLUGIN                = 0x8
)

// C: ecmascript.h:31
const ECMASCRIPT_MAX_NATIVE_CLASSES = 16

// C: ecmascript.h:29-30 — error prop markers
const (
	ST_ERROR_PROP_ZOMBIE = 0x8000
	ST_ERROR_SQLITE_BASE = 0x10000
)

// ---------------------------------------------------------------------------
// gconf seam — globals the C code reads via gconf.
// Wired by the app init path (cmd/movian-go/init.go).
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// C: static struct es_context_list es_contexts; static int es_num_contexts;
// static HTS_MUTEX_DECL(es_context_mutex);
// ---------------------------------------------------------------------------

// C: es_contexts + es_context_mutex + modules — grouped process state.
// es_num_contexts is derived from len(sys.contexts).
// esEnv — dependency seams wired at init (C: gconf.* + implicit
// globals). Grouped; set via EsSet* / direct fields from cmd/movian-go.
var esEnv = struct {
	asyncIO          *asyncio.AsyncIO    // C: gconf.asyncio
	httpServer       *httpnet.HTTPServer // C: gconf.http_server
	backendSystem    *backendcore.BackendSystem
	serviceSystem    *service.ServiceSystem
	i18n             *i18n.I18N
	blobCache        *blobcache.BlobCache               // C: implicit global blobcache
	fam              *facore.FileAccessManager          // C: implicit global fa context
	keyring          *keyring.Keyring                   // C: implicit global keyring
	notifMgr         *notifications.NotificationManager // C: notify_add global
	kvstore          *kvstore.KVStore                   // C: kvstore_* globals
	tasks            *task.TaskSystem                   // C: task_* globals
	tracer           *trace.TraceSystem                 // C: trace() global
	pluginSelectView func(pluginID, filename string)    // C: plugin_select_view
	gconf            *gconf.T                           // C: gconf_t — injected
	metadata         *metadata.MetadataManager          // C: implicit default manager
	propPM           *propcore.PropManager              // C: implicit global prop ctx
	eventManager     *event.EventManager                // C: implicit event_mgr
	propPageManager  interface {
		BackendPropMake(pm *propcore.PropManager, model *propcore.Prop, suggest string) string
	}
	subSys          *subtitles.System // C: subtitles.c statics
	pluginUninstall func(id string)   // C: plugin_uninstall under ENABLE_PLUGINS
	sys             *System           // C: ecmascript.c/es_hook.c/es_route.c file statics
}{
	// C's file statics exist unconditionally — unwired callers get a
	// default instance (overridden by EsSetSystem at init).
	sys: NewSystem(),
}

// System — C: ecmascript.c / es_hook.c / es_route.c file statics
// (es_context_mutex+es_contexts, hook_mutex+es_hooks+num_hooks,
// route_mutex+es_routes). The module list and native_classes stay
// package-level link-time registries (init() populates them before
// any System exists — C: ES_MODULE/REGISTER_NATIVE_CLASS ctors).
type System struct {
	mu       sync.Mutex   // C: es_context_mutex
	contexts []*ESContext // C: LIST_HEAD(es_context)

	hookMu   sync.Mutex // C: hook_mutex
	hookList []*esHook  // C: es_hooks (LIST_INSERT_HEAD)
	hookNum  int        // C: num_hooks

	routeMu   sync.Mutex // C: route_mutex
	routeList []*esRoute // C: es_routes (LIST_INSERT_SORTED)

	timer     esTimerStateT     // C: es_timer.c statics
	fa        esFaStateT        // C: es_faprovider.c mutex+cond
	inspector esInspectorStateT // C: es_io.c inspector statics
}

// esTimerStateT — C: timer thread_running + list + mutex + kick
// (es_timer.c file statics).
type esTimerStateT struct {
	running bool
	list    []*esTimer // LIST_HEAD, sorted by etExpire
	mu      sync.Mutex
	kick    chan struct{}
}

// esFaStateT — C: fa mutex + cond (es_faprovider.c).
type esFaStateT struct {
	mu   sync.Mutex
	cond *sync.Cond
}

// esInspectorStateT — C: es_http_inspectors + mutex + cond (es_io.c).
type esInspectorStateT struct {
	list []*esHTTPInspector
	mu   sync.Mutex
	cond *sync.Cond
}

// NewSystem — allocates the subsystem (conds must be built).
func NewSystem() *System {
	sys := &System{}
	sys.timer.kick = make(chan struct{}, 1)
	sys.fa.cond = sync.NewCond(&sys.fa.mu)
	sys.inspector.cond = sync.NewCond(&sys.inspector.mu)
	return sys
}

// esModules — C: LIST_HEAD(, ecmascript_module) modules — link-time
// registration registry (init() only; read-only at runtime).
var esModules []*EcmascriptModule

// ---------------------------------------------------------------------------
// es_resource_class_t / es_resource_t
// ---------------------------------------------------------------------------

// C: es_resource_class_t (ecmascript.h:110-119)
type ESResourceClass struct {
	ErcName      string
	ErcSize      int
	ErcDestroy   func(er *ESResource)
	ErcInfo      func(er *ESResource) string
	ErcFinalizer func(er *ESResource)
}

// C: es_resource_t (ecmascript.h:124-132)
// Concrete resources embed this and are carried as *ESResource in er.Data.
type ESResource struct {
	erClass    *ESResourceClass
	erCtx      *ESContext
	erZombie   bool
	erRefcount atomic.Int32
	// Data holds the concrete resource object (the C code embeds
	// es_resource_t as the first member of the concrete struct).
	Data any
}

// C: ecmascript_module_t (ecmascript.h:294-298)
type EcmascriptModule struct {
	Name      string
	Functions []gaftape.FunctionListEntry
}

// C: ecmascript_native_class_t (ecmascript.h:37-41)
type EcmascriptNativeClass struct {
	Name    string
	ID      int
	Release func(ptr any)
}

// C: static ecmascript_native_class_t *native_classes[16];
var nativeClasses [ECMASCRIPT_MAX_NATIVE_CLASSES]*EcmascriptNativeClass

// ---------------------------------------------------------------------------
// es_context_t
// ---------------------------------------------------------------------------

// C: es_context_t (ecmascript.h:60-101)
type ESContext struct {
	sys       *System         // C: implicit global es subsystem
	usage     *usage.Reporter // C: usage_event global (usage.c)
	ecID      string          // rstr_t
	ecPath    string
	ecStorage string

	ecDebug              bool
	ecBypassFileACLWrite bool
	ecBypassFileACLRead  bool

	ecFlags  int
	ecLinked int

	ecRefcount atomic.Int32

	ecMutex sync.Mutex
	ecGaf   *gaftape.Context // main context (heap entry point)
	ecHeap  *gaftape.Heap

	ecThread *gaftape.Context // parked thread for reuse

	ecResourcesPermanent []*ESResource // LIST: permanent resources
	ecResourcesVolatile  []*ESResource // LIST: volatile resources

	ecMemActive int64
	ecMemPeak   int64

	ecManifest any // *htsmsg — plugin.json

	ecPropUnloadDestroy *propcore.PropVec

	ecPropDispatchGroup *propcore.DispatchGroup

	ecNativeInstances [ECMASCRIPT_MAX_NATIVE_CLASSES]int

	ecRootedObjects int
}

// ID returns the context id (C: rstr_get(ec->ec_id)).
func (ec *ESContext) ID() string { return ec.ecID }

// Path returns the script url the context was created for (C: ec_path).
func (ec *ESContext) Path() string { return ec.ecPath }

// VM exposes the underlying goja runtime — the main heap's VM.
// Callers must hold ec_mutex (EsContextBegin) before touching it.
func (ec *ESContext) VM() *goja.Runtime { return ec.ecGaf.VM() }

// ---------------------------------------------------------------------------
// LIST_* helpers — intrusive list semantics on Go slices.
// ---------------------------------------------------------------------------

func listInsertHead(l *[]*ESResource, e *ESResource) {
	*l = slices.Insert(*l, 0, e)
}

func listRemoveResource(l *[]*ESResource, e *ESResource) {
	for i, x := range *l {
		if x == e {
			*l = slices.Delete((*l), i, i+1)
			return
		}
	}
}

func listFirstResource(l []*ESResource) *ESResource {
	if len(l) == 0 {
		return nil
	}
	return l[0]
}

// ---------------------------------------------------------------------------
// ecmascript_context_lockmgr — C: ecmascript.c:39-61
// ---------------------------------------------------------------------------

// C lockmgr_op_t values (misc/lockmgr.h)
const (
	LOCKMGR_UNLOCK  = 0
	LOCKMGR_LOCK    = 1
	LOCKMGR_TRY     = 2
	LOCKMGR_RETAIN  = 3
	LOCKMGR_RELEASE = 4
)

// EcmascriptContextLockmgr — C: ecmascript_context_lockmgr
func EcmascriptContextLockmgr(ptr *ESContext, op int) int {
	ec := ptr

	switch op {
	case LOCKMGR_UNLOCK:
		ec.ecMutex.Unlock()
		return 0
	case LOCKMGR_LOCK:
		ec.ecMutex.Lock()
		return 0
	case LOCKMGR_TRY:
		if ec.ecMutex.TryLock() {
			return 0
		}
		return 1
	case LOCKMGR_RETAIN:
		ec.ecRefcount.Add(1)
		return 0
	case LOCKMGR_RELEASE:
		EsContextRelease(ec)
		return 0
	}
	panic("unreachable")
}

// ---------------------------------------------------------------------------
// ecmascript_register_module — C: ecmascript.c:66-70
// ---------------------------------------------------------------------------

// EcmascriptRegisterModule — C: ecmascript_register_module
func EcmascriptRegisterModule(m *EcmascriptModule) {
	esModules = slices.Insert(esModules, 0, m)
}

// ---------------------------------------------------------------------------
// es_prop_is_true / es_prop_to_int / es_prop_to_rstr
// ---------------------------------------------------------------------------

// EsPropIsTrue — C: es_prop_is_true (ecmascript.c:75-84)
func EsPropIsTrue(ctx *gaftape.Context, objIdx int, id string) int {
	if !ctx.IsObject(objIdx) {
		return 0
	}
	ctx.GetPropString(objIdx, id)
	r := 0
	if ctx.ToBoolean(-1) {
		r = 1
	}
	ctx.Pop()
	return r
}

// EsPropToInt — C: es_prop_to_int (ecmascript.c:89-100)
func EsPropToInt(ctx *gaftape.Context, objIdx int, id string, def int) int {
	if !ctx.IsObject(objIdx) {
		return def
	}
	ctx.GetPropString(objIdx, id)
	if ctx.IsNumber(-1) {
		def = ctx.ToInt(-1)
	}
	ctx.Pop()
	return def
}

// EsPropToRstr — C: es_prop_to_rstr (ecmascript.c:105-118)
// Returns nil for absent (C returns NULL rstr); *"" differs from NULL.
func EsPropToRstr(ctx *gaftape.Context, objIdx int, id string) *string {
	if !ctx.IsObject(objIdx) {
		return nil
	}
	var r *string
	ctx.GetPropString(objIdx, id)
	if ctx.IsString(-1) {
		str := ctx.GetString(-1)
		r = &str
	}
	ctx.Pop()
	return r
}

// ---------------------------------------------------------------------------
// es_dumpstack — C: ecmascript.c:124-135
// ---------------------------------------------------------------------------

// EsDumpstack — C: es_dumpstack
func EsDumpstack(ctx *gaftape.Context) {
	size := ctx.GetTop()
	fmt.Println("STACKDUMP")
	for i := -1; i > -1-size; i-- {
		ctx.Dup(i)
		fmt.Printf("  [%5d]: %s\n", i, ctx.SafeToString(-1))
		ctx.Pop()
	}
}

// ---------------------------------------------------------------------------
// ecmascript_push_buf + buf finalizer — C: ecmascript.c:138-187
// ---------------------------------------------------------------------------

// C: #define BUFNAME "\xff""ptr"
const bufname = "\xffptr"

// ecmascriptBufFinalizer — C: ecmascript_buf_finalizer (ecmascript.c:144-153)
func ecmascriptBufFinalizer(ctx *gaftape.Context) int {
	ctx.GetPropString(0, bufname)
	if ctx.IsPointer(-1) {
		// C: buf_release — Go Buffer is GC'd; release is a no-op.
	}
	return 0
}

// EcmascriptPushBuf — C: ecmascript_push_buf (ecmascript.c:159-187)
func EcmascriptPushBuf(ctx *gaftape.Context, b *facore.Buffer) {
	ctx.PushExternalBuffer(b.Data[:b.Size])
	ctx.ConfigBuffer(-1, b.Data[:b.Size])

	ctx.PushBufferObject(-1, 0, b.Size, gaftape.GAF_BUFOBJ_UINT8ARRAY)

	ctx.PushPointer(b) // C: buf_retain(b) — retained by pointer prop
	ctx.PutPropString(-2, bufname)

	ctx.PushCFunction(ecmascriptBufFinalizer, 1)
	ctx.SetFinalizer(-2)

	ctx.SwapTop(-2)
	ctx.Pop()
}

// ---------------------------------------------------------------------------
// es_get — C: ecmascript.c:193-199
// ---------------------------------------------------------------------------

// EsGet — C: es_get — the es_context owning this gaf context.
func EsGet(ctx *gaftape.Context) *ESContext {
	var fns gaftape.MemoryFunctions
	ctx.GetMemoryFunctions(&fns)
	if ec, ok := fns.Udata.(*ESContext); ok {
		return ec
	}
	if ec, ok := ctx.Ec.(*ESContext); ok {
		return ec
	}
	return nil
}

// ---------------------------------------------------------------------------
// es_resource_* — C: ecmascript.c:204-305
// ---------------------------------------------------------------------------

// EsResourceAlloc — C: es_resource_alloc (ecmascript.c:204-210)
// C calloc's erc_size bytes and the caller embeds es_resource_t first.
// In Go the concrete resource object is supplied by the caller and
// stored in er.Data.
func EsResourceAlloc(erc *ESResourceClass, data any) *ESResource {
	er := &ESResource{}
	er.erClass = erc
	er.Data = data
	return er
}

// es_resource_retain — C: static inline (ecmascript.h:149-152)
func esResourceRetain(er *ESResource) {
	er.erRefcount.Add(1)
}

// EsResourceRetain — exported variant of es_resource_retain.
func EsResourceRetain(er *ESResource) { esResourceRetain(er) }

// EsResourceLink — C: es_resource_link (ecmascript.c:215-223)
func EsResourceLink(er *ESResource, ec *ESContext, permanent int) {
	er.erCtx = esContextRetain(ec)
	er.erRefcount.Add(1)
	if permanent != 0 {
		listInsertHead(&ec.ecResourcesPermanent, er)
	} else {
		listInsertHead(&ec.ecResourcesVolatile, er)
	}
}

// EsResourceCreate — C: es_resource_create (ecmascript.c:229-234)
func EsResourceCreate(ec *ESContext, erc *ESResourceClass, permanent int,
	data any) any {
	r := EsResourceAlloc(erc, data)
	EsResourceLink(r, ec, permanent)
	return r
}

// EsResourceRelease — C: es_resource_release (ecmascript.c:241-250)
func EsResourceRelease(er *ESResource) {
	if er.erRefcount.Add(-1) != 0 {
		return
	}
	EsContextRelease(er.erCtx)
	if er.erClass.ErcFinalizer != nil {
		er.erClass.ErcFinalizer(er)
	}
	// C: free(er) — GC'd in Go
}

// EsResourceDestroy — C: es_resource_destroy (ecmascript.c:256-262)
func EsResourceDestroy(er *ESResource) {
	if er.erZombie {
		return
	}
	er.erZombie = true
	er.erClass.ErcDestroy(er)
}

// EsResourceUnlink — C: es_resource_unlink (ecmascript.c:268-272)
func EsResourceUnlink(er *ESResource) {
	ec := er.erCtx
	// C: LIST_REMOVE(er, er_link) — membership is in ctx lists.
	found := false
	if ec != nil {
		if slices.Contains(ec.ecResourcesPermanent, er) {
			found = true
		}
		if found {
			listRemoveResource(&ec.ecResourcesPermanent, er)
		} else {
			listRemoveResource(&ec.ecResourcesVolatile, er)
		}
	}
	EsResourceRelease(er)
}

// EsResourcePush — C: es_resource_push (ecmascript.c:278-282)
func EsResourcePush(ctx *gaftape.Context, er *ESResource) int {
	esResourceRetain(er)
	return esPushNativeObj(ctx, esNativeResource, er)
}

// EsResourceGet — C: es_resource_get (ecmascript.c:288-302)
func EsResourceGet(ctx *gaftape.Context, objIdx int,
	erc *ESResourceClass) any {
	er := esGetNativeObj(ctx, objIdx, esNativeResource).(*ESResource)
	if er.erClass != erc {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Invalid resource class %s expected %s",
			er.erClass.ErcName, erc.ErcName)
	}
	return er
}

// es_resource_destroy_duk — C: ecmascript.c:309-314
func esResourceDestroyGaf(ctx *gaftape.Context) int {
	er := esGetNativeObj(ctx, 0, esNativeResource).(*ESResource)
	EsResourceDestroy(er)
	return 0
}

// ---------------------------------------------------------------------------
// Core module functions — C: ecmascript.c:318-381
// ---------------------------------------------------------------------------

// es_compile — C: ecmascript.c:319-337
func esCompile(ctx *gaftape.Context) int {
	path := ctx.RequireString(0)
	buf, lerr := faLoad(path)

	if buf == nil {
		msg := "load failed"
		if lerr != nil {
			msg = lerr.Error()
		}
		ctx.Error(gaftape.GAF_ERR_ERROR, "Unable to load %s -- %s",
			path, msg)
	}

	s := string(buf.Data[:buf.Size])
	ctx.PushLstring(s, len(s))

	ctx.PushString(path)

	ctx.Compile(0)
	return 1
}

// es_sleep — C: ecmascript.c:342-347
func esSleep(ctx *gaftape.Context) int {
	t := ctx.GetNumber(0) * 1000000.0
	time.Sleep(time.Duration(t) * time.Nanosecond)
	return 0
}

// es_timestamp — C: ecmascript.c:352-356
func esTimestamp(ctx *gaftape.Context) int {
	ctx.PushNumber(float64(archGetTS()))
	return 1
}

// es_random_bytes — C: ecmascript.c:361-370
func esRandomBytes(ctx *gaftape.Context) int {
	l := ctx.GetInt(0)
	if l > 65536 {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Too many bytes requested")
	}
	ptr := ctx.PushFixedBuffer(l)
	rand.Read(ptr)
	return 1
}

// C: static const duk_function_list_entry fnlist_core[] (ecmascript.c:374-381)
var fnlistCore = []gaftape.FunctionListEntry{
	{Key: "compile", Value: esCompile, Nargs: 1},
	{Key: "resourceDestroy", Value: esResourceDestroyGaf, Nargs: 1},
	{Key: "sleep", Value: esSleep, Nargs: 1},
	{Key: "timestamp", Value: esTimestamp, Nargs: 0},
	{Key: "randomBytes", Value: esRandomBytes, Nargs: 1},
}

// ---------------------------------------------------------------------------
// Module loading — C: ecmascript.c:385-465
// ---------------------------------------------------------------------------

// tryload — C: ecmascript.c:389-406
func tryload(ctx *gaftape.Context, path, id string, ec *ESContext) int {
	buf, _ := faLoad(path)

	if buf != nil {
		esDebug(ec, "Module %s loaded from %s", id, path)
		s := string(buf.Data[:buf.Size])
		ctx.PushLstring(s, len(s))
		return 1
	}
	return 0
}

// es_modsearch — C: es_modsearch (ecmascript.c:410-465)
func esModsearch(ctx *gaftape.Context) int {
	var path string

	ec := EsGet(ctx)
	id := ctx.RequireString(0)

	esDebug(ec, "Searching for module %s", id)

	if nativemod := mystrbegins(id, "native/"); nativemod != "" {
		var m *EcmascriptModule
		for _, x := range esModules {
			if x.Name == nativemod {
				m = x
				break
			}
		}
		if m == nil {
			ctx.Error(gaftape.GAF_ERR_ERROR, "Can't find native module %s", id)
		}
		for i := range len(m.Functions) {
			f := m.Functions[i]
			ctx.PushCLightFunc(f.Value, f.Nargs, 0, 0)
			ctx.PutPropString(2, f.Key)
		}
		return 0
	}
	if compatname := mystrbegins(id, "showtime/"); compatname != "" {
		id = "movian/" + compatname
	}

	if ec.ecPath != "" {
		path = facore.PathJoin(ec.ecPath, id) + ".js"
		if tryload(ctx, path, id, ec) != 0 {
			return 1
		}
	}

	path = fmt.Sprintf("dataroot://res/ecmascript/modules/%s.js", id)
	if tryload(ctx, path, id, ec) != 0 {
		return 1
	}

	ctx.Error(gaftape.GAF_ERR_ERROR, "Can't find module %s", id)
	return 0
}

// ---------------------------------------------------------------------------
// es_create_env — C: ecmascript.c:470-517
// ---------------------------------------------------------------------------

func esCreateEnv(ec *ESContext, loaddir, storage string) {
	ctx := ec.ecGaf

	ctx.PushGlobalStash()

	ctx.PushObject()
	ctx.PutPropString(-2, "roots")

	ctx.Pop() // global_stash

	ctx.PushGlobalObject()

	objIdx := ctx.PushObject()

	ctx.PushInt(int(version.AppGetVersionInt()))
	ctx.PutPropString(objIdx, "currentVersionInt")

	ctx.PushString(version.AppVersion())
	ctx.PutPropString(objIdx, "currentVersionString")

	ctx.PushString(esGconf().DeviceID)
	ctx.PutPropString(objIdx, "deviceId")

	if loaddir != "" {
		ctx.PushString(loaddir)
		ctx.PutPropString(objIdx, "loadPath")
	}

	if storage != "" {
		ctx.PushString(storage)
		ctx.PutPropString(objIdx, "storagePath")
	}

	ctx.PutFunctionList(objIdx, fnlistCore)
	ctx.PutPropString(-2, "Core")

	// Initialize modSearch helper
	ctx.GetPropString(-1, "Duktape")
	ctx.PushCFunction(esModsearch, 4)
	ctx.PutPropString(-2, "modSearch")
	ctx.Pop()

	ctx.PutFunctionList(-1, esFnlistTimer)

	ctx.PushObject()
	ctx.PutFunctionList(-1, esFnlistConsole)
	ctx.PutPropString(-2, "console")

	// Pop global object
	ctx.Pop()
}

// ---------------------------------------------------------------------------
// Memory accounting — C: ecmascript.c:522-566
// Go can't hook goja allocations; these track allocations made through the
// heap's memory functions (buffers, es_resource allocs routed via mfs).
// ---------------------------------------------------------------------------

func esMemAlloc(udata any, size int) any {
	ec := udata.(*ESContext)
	p := make([]byte, size)
	ec.ecMemActive += int64(size)
	if ec.ecMemActive > ec.ecMemPeak {
		ec.ecMemPeak = ec.ecMemActive
	}
	return p
}

func esMemRealloc(udata any, ptr any, size int) any {
	ec := udata.(*ESContext)
	prev := 0
	if udata != nil && ptr != nil {
		prev = len(ptr.([]byte))
	}
	var np []byte
	if ptr != nil {
		old := ptr.([]byte)
		np = make([]byte, size)
		copy(np, old)
	} else {
		np = make([]byte, size)
	}
	ec.ecMemActive -= int64(prev)
	ec.ecMemActive += int64(size)
	if ec.ecMemActive > ec.ecMemPeak {
		ec.ecMemPeak = ec.ecMemActive
	}
	return np
}

func esMemFree(udata any, ptr any) {
	ec := udata.(*ESContext)
	if ptr == nil {
		return
	}
	ec.ecMemActive -= int64(len(ptr.([]byte)))
}

// ---------------------------------------------------------------------------
// es_context_create / release / begin / end / suspend / resume
// C: ecmascript.c:571-756
// ---------------------------------------------------------------------------

// esContextCreate — C: es_context_create (ecmascript.c:571-634)
func esContextCreate(u *usage.Reporter, id string, flags int, url, storage string) *ESContext {
	ec := &ESContext{usage: u}

	if normalized, err := esEnv.fam.FANormalize(url); err == nil {
		url = normalized
	}

	// C: fa_parent + fa_stat — only set ec_path if parent exists
	if path, err := facore.FAParent(url); err == nil {
		if _, serr := facore.StatEx(esEnv.fam, path, 0); serr == nil {
			ec.ecPath = path
		}
	}

	if ec.ecPath == "" {
		esEnv.tracer.Trace(trace.TRACE_ERROR, id,
			"Unable to get parent directory for %s -- No loadPath set", url)
	}

	if storage != "" {
		ec.ecStorage = storage
	}

	ec.ecFlags = flags
	ec.ecDebug = flags&ECMASCRIPT_DEBUG != 0 || esGconf().EnableEcmascriptDebug.Load()
	ec.ecBypassFileACLRead = flags&ECMASCRIPT_FILE_BYPASS_ACL_READ != 0
	ec.ecBypassFileACLWrite = flags&ECMASCRIPT_FILE_BYPASS_ACL_WRITE != 0

	ec.ecRefcount.Store(1)

	ec.ecPropUnloadDestroy = propcore.PropVecCreate(16)

	// C: prop_dispatch_group_create() — shared psd for this context's
	// subscriptions. Group-mode notifications are drained by the prop
	// global dispatch workers, which apply hps_lockmgr
	// (= ecmascript_context_lockmgr on ec->ec_mutex) around each callback.
	ec.ecPropDispatchGroup = propcore.NewDispatchGroup()

	ec.ecHeap = gaftape.CreateHeap(esMemAlloc, esMemRealloc, esMemFree,
		ec, nil)
	ec.ecGaf = gaftape.NewMainContext(ec.ecHeap)
	ec.ecGaf.Ec = ec

	esCreateEnv(ec, ec.ecPath, ec.ecStorage)

	ec.ecID = id
	ec.sys = esEnv.sys

	ec.sys.mu.Lock()
	ec.ecLinked = 1
	ec.sys.contexts = slices.Insert(ec.sys.contexts, 0, ec)
	ec.sys.mu.Unlock()

	return ec
}

// EsContextRelease — C: es_context_release (ecmascript.c:641-661)
func EsContextRelease(ec *ESContext) {
	if ec.ecRefcount.Add(-1) != 0 {
		return
	}
	// hts_mutex_destroy — no-op in Go
	ec.ecID = ""
	ec.ecPath = ""
	ec.ecStorage = ""

	if ec.ecLinked != 0 {
		ec.sys.mu.Lock()
		for i, x := range ec.sys.contexts {
			if x == ec {
				ec.sys.contexts = slices.Delete(ec.sys.contexts, i, i+1)
				break
			}
		}
		ec.sys.mu.Unlock()
	}
	ec.ecManifest = nil
	// C: free(ec)
}

// esContextRetain — C: static inline es_context_retain (ecmascript.h:163-167)
func esContextRetain(ec *ESContext) *ESContext {
	ec.ecRefcount.Add(1)
	return ec
}

// EsContextBegin — C: es_context_begin (ecmascript.c:667-682)
func EsContextBegin(ec *ESContext) *gaftape.Context {
	ec.ecRefcount.Add(1)
	ec.ecMutex.Lock()

	if ec.ecThread != nil {
		ctx := ec.ecThread
		ec.ecThread = nil
		return ctx
	}

	idx := ec.ecGaf.PushThread()

	ctx := ec.ecGaf.GetContext(idx)
	EsRootRegister(ec.ecGaf, idx, ctx)

	ec.ecGaf.Pop()

	return ctx
}

// EsContextSuspend — C: es_context_suspend (ecmascript.c:688-693)
func EsContextSuspend(ec *ESContext, ctx *gaftape.Context,
	state *gaftape.ThreadState) {
	ctx.Suspend(state)
	ec.ecMutex.Unlock()
}

// EsContextResume — C: es_context_resume (ecmascript.c:699-704)
func EsContextResume(ec *ESContext, ctx *gaftape.Context,
	state *gaftape.ThreadState) {
	ec.ecMutex.Lock()
	ctx.Resume(state)
}

// EsContextEnd — C: es_context_end (ecmascript.c:710-756)
func EsContextEnd(ec *ESContext, doGC int, ctx *gaftape.Context) {
	if ec.ecGaf != nil {

		ctx.SetTop(0)

		if ec.ecThread == nil {
			ec.ecThread = ctx
		} else {
			EsRootUnregister(ec.ecGaf, ctx)
		}

		if doGC != 0 {
			ec.ecGaf.Gc(0)
		}

		if listFirstResource(ec.ecResourcesPermanent) == nil {
			// No more permanent resources, attached. Terminate context

			if ec.ecThread != nil {
				EsRootUnregister(ec.ecGaf, ec.ecThread)
				ec.ecThread = nil
			}

			var er *ESResource
			for er = listFirstResource(ec.ecResourcesVolatile); er != nil; er = listFirstResource(ec.ecResourcesVolatile) {
				// assert(er->er_zombie == 0)
				EsResourceDestroy(er)
			}

			ec.ecHeap.Destroy()
			ec.ecGaf = nil

			propcore.PropVecDestroyEntries(ec.ecPropUnloadDestroy)
			propcore.PropVecRelease(ec.ecPropUnloadDestroy)

			if ec.ecPropDispatchGroup != nil {
				// C: prop_dispatch_group_destroy — releases the
				// creator's reference on the shared psd.
				propcore.DispatchGroupDestroy(ec.ecPropDispatchGroup)
				ec.ecPropDispatchGroup = nil
			}

			esEnv.tracer.Trace(trace.TRACE_DEBUG, ec.ecID, "Unloaded")
		}
	}
	ec.ecMutex.Unlock()
	EsContextRelease(ec)
}

// ---------------------------------------------------------------------------
// Error helpers — C: ecmascript.c:762-816
// ---------------------------------------------------------------------------

// EsDumpErrEx — C: es_dump_err_ex (ecmascript.c:762-805)
func EsDumpErrEx(ctx *gaftape.Context, nativeFunc, nativeFile string,
	nativeLine int) {
	ec := EsGet(ctx)

	if ctx.IsString(-1) {
		// Not a real exception
		esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "%s", ctx.ToString(-1))
		return
	}

	ctx.GetPropString(-1, "name")
	name := ctx.GetString(-1)

	ctx.GetPropString(-2, "message")
	message := ctx.ToString(-1)

	ctx.GetPropString(-3, "fileName")
	filename := ctx.GetString(-1)

	ctx.GetPropString(-4, "lineNumber")
	lineNo := ctx.GetInt(-1)

	ctx.GetPropString(-5, "stack")
	stack := ctx.GetString(-1)

	if filename != "" {
		esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "%s (%s) at %s:%d",
			name, message, filename, lineNo)
	} else {
		esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "%s (%s)",
			name, message)
	}
	esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "STACK DUMP: %s", stack)

	esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "Native callsite %s() at %s:%d",
		nativeFunc, nativeFile, nativeLine)

	ctx.PopN(5)
}

// EsDumpErr — C: es_dump_err macro (with callsite info).
func EsDumpErr(ctx *gaftape.Context) {
	EsDumpErrEx(ctx, "?", "?", 0)
}

// EsGetErrCode — C: es_get_err_code (ecmascript.c:811-816)
func EsGetErrCode(ctx *gaftape.Context) int {
	ctx.GetPropString(-1, "message")
	r := ctx.ToInt(-1)
	ctx.Pop()
	return r
}

// ---------------------------------------------------------------------------
// Script load/exec — C: ecmascript.c:822-877
// ---------------------------------------------------------------------------

// esLoadAndCompile — C: es_load_and_compile (ecmascript.c:822-848)
func esLoadAndCompile(ec *ESContext, path string, ctx *gaftape.Context) int {
	buf, _ := faLoad(path)

	if buf == nil {
		esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "Unable to load %s", path)
		return -1
	}

	s := string(buf.Data[:buf.Size])
	ctx.PushLstring(s, len(s))
	ctx.PushString(path)

	if ctx.PCompile(0) != 0 {
		esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "Unable to compile %s -- %s",
			path, ctx.SafeToString(-1))
		ctx.Pop()
		return -1
	}
	return 0
}

// esExec — C: es_exec (ecmascript.c:854-873)
func esExec(ec *ESContext, path string, ctx *gaftape.Context) int {
	if esLoadAndCompile(ec, path, ctx) != 0 {
		return -1
	}

	rc := ctx.PCall(0)
	if rc != 0 {
		EsDumpErr(ctx)
	}
	ctx.Pop()
	return 0
}

// ---------------------------------------------------------------------------
// ecmascript_plugin_load — C: ecmascript.c:880-953
// ---------------------------------------------------------------------------

// EcmascriptPluginLoad — C: ecmascript_plugin_load
func EcmascriptPluginLoad(u *usage.Reporter, id, url string,
	version2 int, manifest string, flags int) int {

	storage := fmt.Sprintf("%s/plugins/%s", esGconf().PersistentPath, id)

	ec := esContextCreate(u, id, flags|ECMASCRIPT_PLUGIN, url, storage)

	ctx := EsContextBegin(ec)

	ctx.PushGlobalObject()

	pluginObjIdx := ctx.PushObject()

	ctx.PushString(id)
	ctx.PutPropString(pluginObjIdx, "id")

	ctx.PushString(url)
	ctx.PutPropString(pluginObjIdx, "url")

	ctx.PushString(manifest)
	ctx.PutPropString(pluginObjIdx, "manifest")

	ctx.PushInt(version2)
	ctx.PutPropString(pluginObjIdx, "apiversion")

	if ec.ecPath != "" {
		ctx.PushString(ec.ecPath)
		ctx.PutPropString(pluginObjIdx, "path")
	}

	ctx.PutPropString(-2, "Plugin")
	ctx.Pop()

	var ts0, ts1, ts2, ts3, ts4 int64

	if version2 == 1 {

		ts0 = archGetTS()

		if esLoadAndCompile(ec, "dataroot://res/ecmascript/legacy/api-v1.js",
			ctx) != 0 {
			goto bad
		}

		ts1 = archGetTS()

		if ctx.PCall(0) != 0 {
			EsDumpErr(ctx)
			goto bad
		}

		ts2 = archGetTS()

		if esLoadAndCompile(ec, url, ctx) != 0 {
			ctx.Pop()
			goto bad
		}

		ts3 = archGetTS()

		ctx.SwapTop(0)
		if ctx.PCallMethod(0) != 0 {
			EsDumpErr(ctx)
		}

		ts4 = archGetTS()

		esDebug(ec, "API v1 emulation: Compile:%dms Exec:%dms",
			(int(ts1-ts0))/1000,
			(int(ts2-ts1))/1000)

		esDebug(ec, "Plugin main:      Compile:%dms Exec:%dms",
			(int(ts3-ts2))/1000,
			(int(ts4-ts3))/1000)
	} else {
		esExec(ec, url, ctx)
	}

bad:
	EsContextEnd(ec, 1, ctx)
	EsContextRelease(ec)
	return 0
}

// ---------------------------------------------------------------------------
// Context vector — C: ecmascript.c:959-980
// ---------------------------------------------------------------------------

// EcmascriptGetAllContexts — C: ecmascript_get_all_contexts
// C returns NULL-terminated vector; Go returns slice.
func EcmascriptGetAllContexts() []*ESContext {
	sys := esEnv.sys
	sys.mu.Lock()
	var v []*ESContext
	for _, ec := range sys.contexts {
		v = append(v, esContextRetain(ec))
	}
	sys.mu.Unlock()
	return v
}

// EcmascriptReleaseContextVector — C: ecmascript_release_context_vector
func EcmascriptReleaseContextVector(v []*ESContext) {
	for _, ec := range v {
		EsContextRelease(ec)
	}
}

// ---------------------------------------------------------------------------
// ecmascript_plugin_unload — C: ecmascript.c:986-1010
// ---------------------------------------------------------------------------

// EcmascriptPluginUnload — C: ecmascript_plugin_unload
func EcmascriptPluginUnload(id string) {
	var ec *ESContext
	var er *ESResource

	sys := esEnv.sys
	sys.mu.Lock()
	for _, x := range sys.contexts {
		if id == x.ecID {
			// assert(ec->ec_linked)
			for i, y := range sys.contexts {
				if y == x {
					sys.contexts = slices.Delete(sys.contexts, i, i+1)
					break
				}
			}
			x.ecLinked = 0
			ec = x
			break
		}
	}
	sys.mu.Unlock()

	if ec == nil {
		return
	}

	ctx := EsContextBegin(ec)

	for er = listFirstResource(ec.ecResourcesPermanent); er != nil; er = listFirstResource(ec.ecResourcesPermanent) {
		EsResourceDestroy(er)
	}

	EsContextEnd(ec, 1, ctx)
}

// ---------------------------------------------------------------------------
// init/fini — C: ecmascript.c:1016-1067
// ---------------------------------------------------------------------------

// EcmascriptStart — C: ecmascript_init (INIT_GROUP_API)
func EcmascriptStart(u *usage.Reporter) {
	if esGconf().LoadEcmascript == "" {
		return
	}

	flags := ECMASCRIPT_DEBUG |
		ECMASCRIPT_FILE_BYPASS_ACL_READ |
		ECMASCRIPT_FILE_BYPASS_ACL_WRITE

	ec := esContextCreate(u, "cmdline", flags, esGconf().LoadEcmascript, "/tmp")

	ctx := EsContextBegin(ec)

	esExec(ec, esGconf().LoadEcmascript, ctx)

	EsContextEnd(ec, 1, ctx)
	EsContextRelease(ec)
}

// EcmascriptFini — C: ecmascript_fini
func EcmascriptFini() {
	sys := esEnv.sys
	sys.mu.Lock()
	for len(sys.contexts) > 0 {
		ec := sys.contexts[0]

		sys.contexts = sys.contexts[1:]
		ec.ecLinked = 0
		sys.mu.Unlock()

		ctx := EsContextBegin(ec)

		for er := listFirstResource(ec.ecResourcesPermanent); er != nil; er = listFirstResource(ec.ecResourcesPermanent) {
			EsResourceDestroy(er)
		}

		EsContextEnd(ec, 1, ctx)

		sys.mu.Lock()
	}
	sys.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Backend registration — C: ecmascript.c:1090-1097
// be_ecmascript is registered by the ecmascript package's init wiring
// (see es_route.go / es_searcher.go for ecmascript_openuri/search).
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// es_debug macro — C: ecmascript.h:255-261
func esDebug(ec *ESContext, format string, args ...any) {
	if ec.ecDebug {
		esEnv.tracer.Trace(trace.TRACE_DEBUG, ec.ecID, format, args...)
	}
}

// mystrbegins — C: misc/str.h mystrbegins — returns s+len(prefix) or NULL.
func mystrbegins(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return ""
}

// faLoad — C: fa_load(url, FA_LOAD_ERRBUF, NULL) against the default fam.
func faLoad(path string) (*facore.Buffer, error) {
	fam := esEnv.fam
	if fam == nil {
		return nil, errors.New("no fileaccess manager")
	}
	buf, err := facore.FALoad(fam, path, nil, nil, 0)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// archGetTS — C: arch_get_ts — microseconds since some epoch.
func archGetTS() int64 {
	return time.Now().UnixNano() / 1000
}

// esGconf().EnableEcmascriptDebug — C: gconf field (see devsettings binding).

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func SetGconf(g *gconf.T) { esEnv.gconf = g }

// EsSetSubtitleSystem injects the subtitles subsystem (C: subtitles.c
// statics — provider register/unregister from JS plugins).
func EsSetSubtitleSystem(s *subtitles.System) { esEnv.subSys = s }

// esGconf — C: gconf_t reads; nil-safe for unwired/test paths.
func esGconf() *gconf.T {
	if esEnv.gconf == nil {
		esEnv.gconf = gconf.New()
	}
	return esEnv.gconf
}
