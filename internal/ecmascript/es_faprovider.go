package ecmascript

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"time"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gaftape"
)

// es_faprovider.go — canonical port of src/ecmascript/es_faprovider.c
//
// Implements the JS file-access provider layer: native/faprovider's
// register() installs a dynamic FA protocol whose open/read/stat/close/
// redirect ops are dispatched into the plugin's gaf context via task_run.
// The fap ops block on the global es_fa_cond (guarded by es_fa_mutex)
// until the JS side calls the matching *Respond function.

// C: static hts_mutex_t es_fa_mutex + hts_cond_t es_fa_cond
// (es_faprovider.c:30-37) — live on System.fa (see ecmascript.go).

// ---------------------------------------------------------------------------
// C: typedef struct es_fap (es_faprovider.c:41-48)
// ---------------------------------------------------------------------------

type esFap struct {
	super            ESResource // C: es_resource_t super (first member)
	name             string     // C: char *name
	fap              facore.FAProtocol
	internalSeekMode int               // C: int internal_seek_mode
	famProto         *esFapFamProtocol // fam-level Protocol adapter
}

// ---------------------------------------------------------------------------
// C: es_fap_destroy (es_faprovider.c:57-64)
// ---------------------------------------------------------------------------

func esFapDestroy(eres *ESResource) {
	ef := eres.Data.(*esFap)
	esEnv.fam.UnregisterDynamicFAProtocol(&ef.fap)
	if fam := esEnv.fam; fam != nil && ef.famProto != nil {
		fam.UnregisterProtocol(ef.famProto)
	}
	ef.name = ""
	EsRootUnregister(eres.erCtx.ecGaf, eres)
	EsResourceUnlink(&ef.super)
}

// ---------------------------------------------------------------------------
// C: es_fap_info (es_faprovider.c:71-75)
// ---------------------------------------------------------------------------

func esFapInfo(eres *ESResource) string {
	ef := eres.Data.(*esFap)
	return ef.name
}

// C: static const es_resource_class_t es_resource_fap (es_faprovider.c:81-86)
var esResourceFap = &ESResourceClass{
	ErcName:    "faprovider",
	ErcSize:    0,
	ErcDestroy: esFapDestroy,
	ErcInfo:    esFapInfo,
}

// ---------------------------------------------------------------------------
// C: es_fap_fini (es_faprovider.c:91-95)
// ---------------------------------------------------------------------------

func esFapFini(fap *facore.FAProtocol) {
	ef := fap.Opaque.(*esFap)
	EsResourceRelease(&ef.super)
}

// ---------------------------------------------------------------------------
// C: typedef struct es_fa_handle (es_faprovider.c:100-135)
// ---------------------------------------------------------------------------

const (
	esFAWorking   = 0 // C: ES_FA_WORKING
	esFACancelled = 1 // C: ES_FA_CANCELLED
	esFAOK        = 2 // C: ES_FA_OK
	esFAError     = 3 // C: ES_FA_ERROR
)

type esFah struct {
	fh *facore.Handle // C: fa_handle_t fh (first member)

	fahRefcount atomic.Int32 // C: atomic_t fah_refcount

	fahURL      string  // C: char *fah_url
	fahRedirect *string // C: rstr_t *fah_redirect

	fahErr error // C: char *fah_errbuf + fah_errsize (set under esFaState.mu)

	fahFlags int // C: int fah_flags

	fahStatus      int // C: int fah_status (ES_FA_*)
	fahReturnValue int // C: int fah_return_value

	fahEf *esFap // C: es_fap_t *fah_ef

	fahReadbuf    []byte // C: void *fah_readbuf — caller's dest buffer
	fahReadbufObj any    // Go: stable es_root key for readbuf (C uses raw ptr)
	fahReadlen    int    // C: size_t fah_readlen

	fahSize int64 // C: int64_t fah_size
	fahFpos int64 // C: int64_t fah_fpos

	fahType  int   // C: int fah_type (CONTENT_*)
	fahMtime int64 // C: int fah_mtime
}

// ---------------------------------------------------------------------------
// C: fah_release / fah_retain (es_faprovider.c:139-155)
// ---------------------------------------------------------------------------

func fahRelease(fah *esFah) {
	if fah.fahRefcount.Add(-1) != 0 {
		return
	}
	EsResourceRelease(&fah.fahEf.super)
	fah.fahURL = ""
	fah.fahRedirect = nil
	// C: free(fah) — GC'd
}

func fahRetain(fah *esFah) *esFah {
	fah.fahRefcount.Add(1)
	return fah
}

// C: ES_NATIVE_CLASS(fah, &fah_release) (es_faprovider.c:157)
var esNativeFah = &EcmascriptNativeClass{
	Name:    "fah",
	Release: func(ptr any) { fahRelease(ptr.(*esFah)) },
}

func registerEsFaprovidera() {
	ecmascriptRegisterNativeClass(esNativeFah)
}

// ---------------------------------------------------------------------------
// C: fah_fail / fah_exception / fah_ok / fah_ok_val (es_faprovider.c:161-204)
// ---------------------------------------------------------------------------

func fahFail(fah *esFah, ctx *gaftape.Context) {
	msg := ctx.GetString(-1)
	EsGet(ctx).sys.fa.mu.Lock()
	if fah.fahStatus == esFAWorking {
		if msg == "" {
			msg = "No message in JS exception"
		}
		fah.fahErr = errors.New(msg)
		fah.fahStatus = esFAError
		EsGet(ctx).sys.fa.cond.Broadcast()
	}
	EsGet(ctx).sys.fa.mu.Unlock()
}

func fahException(fah *esFah, ctx *gaftape.Context) {
	ctx.GetPropString(-1, "message")
	fahFail(fah, ctx)
	ctx.Pop()
}

func fahOk(fah *esFah) {
	fah.fahEf.super.erCtx.sys.fa.mu.Lock()
	if fah.fahStatus == esFAWorking {
		fah.fahStatus = esFAOK
		fah.fahEf.super.erCtx.sys.fa.cond.Broadcast()
	}
	fah.fahEf.super.erCtx.sys.fa.mu.Unlock()
}

func fahOkVal(fah *esFah, val int) {
	fah.fahEf.super.erCtx.sys.fa.mu.Lock()
	if fah.fahStatus == esFAWorking {
		fah.fahStatus = esFAOK
		fah.fahReturnValue = val
		fah.fahEf.super.erCtx.sys.fa.cond.Broadcast()
	}
	fah.fahEf.super.erCtx.sys.fa.mu.Unlock()
}

// ---------------------------------------------------------------------------
// C: es_fap_open_task / es_fap_open (es_faprovider.c:211-283)
// ---------------------------------------------------------------------------

func esFapOpenTask(aux any) {
	fah := aux.(*esFah)
	ef := fah.fahEf
	ec := ef.super.erCtx

	ctx := EsContextBegin(ec)

	EsPushRoot(ctx, ef)
	ctx.GetPropString(-1, "open")
	esPushNativeObj(ctx, esNativeFah, fahRetain(fah))
	ctx.PushString(fah.fahURL)

	rc := ctx.PCall(2)
	if rc != 0 {
		fahException(fah, ctx)
		EsDumpErr(ctx)
	}

	EsContextEnd(ec, 0, ctx)
	fahRelease(fah)
}

// esFapOpen — C: es_fap_open (fap_open impl, es_faprovider.c:237-283).
func esFapOpen(fap *facore.FAProtocol, url string,
	flags int, foe *facore.OpenExtra) (*facore.Handle, error) {
	fah := &esFah{fahSize: -1}
	ef := fap.Opaque.(*esFap)

	fah.fahEf = ef
	EsResourceRetain(&ef.super)

	fah.fahURL = url
	fah.fahFlags = flags

	fah.fahRefcount.Store(2) // One to return, one for task

	ef.super.erCtx.sys.fa.mu.Lock()
	fah.fahStatus = esFAWorking

	esEnv.tasks.Run(esFapOpenTask, fah)

	for fah.fahStatus == esFAWorking {
		ef.super.erCtx.sys.fa.cond.Wait()
	}

	err := fah.fahErr

	ef.super.erCtx.sys.fa.mu.Unlock()

	switch fah.fahStatus {
	case esFAOK:
		// C: return &fah->fh — the embedded fa_handle_t. Go: build the
		// fam Handle once, bound to fah's read/seek/close/fsize ops.
		fah.fh = facore.NewHandle(esEnv.fam, ef.famProto, url, fah, nil, fah,
			func() int64 { return fah.fahSize })
		return fah.fh, nil

	case esFACancelled:
		err = errors.New("Cancelled")
		fallthrough
	case esFAError:
		fahRelease(fah)
	}
	return nil, err
}

// ---------------------------------------------------------------------------
// C: es_faprovider_openRespond (es_faprovider.c:287-298)
// ---------------------------------------------------------------------------

func esFaproviderOpenRespond(ctx *gaftape.Context) int {
	fah := esGetNativeObj(ctx, 0, esNativeFah).(*esFah)

	if ctx.GetBoolean(1) {
		EsRootRegister(ctx, 2, fah)
		fahOk(fah)
	} else {
		fahFail(fah, ctx)
	}
	return 0
}

// ---------------------------------------------------------------------------
// C: es_fap_read_task / es_fap_read (es_faprovider.c:306-381)
// ---------------------------------------------------------------------------

func esFapReadTask(aux any) {
	fah := aux.(*esFah)
	ef := fah.fahEf
	ec := ef.super.erCtx

	ctx := EsContextBegin(ec)

	ctx.SetTop(0)

	ctx.PushExternalBuffer(fah.fahReadbuf)
	EsRootRegister(ctx, -1, fah.readbufKey())

	// C: duk_config_buffer(ctx, 0, fah->fah_readbuf, fah->fah_readlen)
	ctx.ConfigBuffer(0, fah.fahReadbuf)

	EsPushRoot(ctx, ef)
	ctx.GetPropString(-1, "read")
	esPushNativeObj(ctx, esNativeFah, fahRetain(fah))
	EsPushRoot(ctx, fah)

	ctx.PushBufferObject(0, 0, fah.fahReadlen, gaftape.GAF_BUFOBJ_UINT8ARRAY)
	ctx.PushInt(fah.fahReadlen)
	ctx.PushNumber(float64(fah.fahFpos))

	rc := ctx.PCall(5)
	if rc != 0 {
		fahException(fah, ctx)
		EsDumpErr(ctx)
	}

	EsContextEnd(ec, 0, ctx)
	fahRelease(fah)
}

// esFapRead — C: es_fap_read (fap_read impl, es_faprovider.c:343-381).
// The JS read callback writes directly into fah.fahReadbuf (the caller's
// destination buffer) via the rooted external buffer.
func esFapRead(fah *esFah, buf []byte, size int) int {
	fah.fahReadbuf = buf[:size]
	fah.fahReadlen = size

	fah.fahEf.super.erCtx.sys.fa.mu.Lock()
	fah.fahStatus = esFAWorking

	esEnv.tasks.Run(esFapReadTask, fahRetain(fah))

	for fah.fahStatus == esFAWorking {
		fah.fahEf.super.erCtx.sys.fa.cond.Wait()
	}

	fah.fahEf.super.erCtx.sys.fa.mu.Unlock()

	ef := fah.fahEf
	ec := ef.super.erCtx

	ctx := EsContextBegin(ec)
	EsPushRoot(ctx, fah.readbufKey())
	ctx.ConfigBuffer(-1, nil)
	EsRootUnregister(ctx, fah.readbufKey())
	EsContextEnd(ec, 0, ctx)

	switch fah.fahStatus {
	case esFAOK:
		if fah.fahReturnValue < size {
			size = fah.fahReturnValue
		}
		fah.fahFpos += int64(size)
		return size

	case esFACancelled, esFAError:
		return -1
	}
	return 0
}

// readbufKey — C uses the raw fah_readbuf pointer as the es_root key;
// in Go we need a stable identity object per fah (the key outlives a
// single read since the same key is registered/unregistered each call).
func (fah *esFah) readbufKey() any {
	if fah.fahReadbufObj == nil {
		fah.fahReadbufObj = &struct{}{}
	}
	return fah.fahReadbufObj
}

// ---------------------------------------------------------------------------
// C: es_faprovider_readRespond (es_faprovider.c:385-400)
// ---------------------------------------------------------------------------

func esFaproviderReadRespond(ctx *gaftape.Context) int {
	fah := esGetNativeObj(ctx, 0, esNativeFah).(*esFah)
	size := ctx.GetInt(1)
	if size < 0 {
		EsGet(ctx).sys.fa.mu.Lock()
		if fah.fahStatus == esFAWorking {
			fah.fahStatus = esFAError
			EsGet(ctx).sys.fa.cond.Broadcast()
		}
		EsGet(ctx).sys.fa.mu.Unlock()
	} else {
		fahOkVal(fah, ctx.GetInt(1))
	}
	return 0
}

// ---------------------------------------------------------------------------
// C: es_fap_close_task / es_fap_close (es_faprovider.c:404-448)
// ---------------------------------------------------------------------------

func esFapCloseTask(aux any) {
	fah := aux.(*esFah)
	ef := fah.fahEf
	ec := ef.super.erCtx
	ctx := EsContextBegin(ec)

	EsPushRoot(ctx, ef)
	ctx.GetPropString(-1, "close")
	esPushNativeObj(ctx, esNativeFah, fahRetain(fah))
	EsPushRoot(ctx, fah)

	rc := ctx.PCall(2)

	if rc != 0 {
		fahException(fah, ctx)
		EsDumpErr(ctx)
	}

	EsRootUnregister(ctx, fah)
	EsContextEnd(ec, 1, ctx)
	fahRelease(fah)
}

// esFapClose — C: es_fap_close (fap_close impl, es_faprovider.c:431-448).
func esFapClose(fah *esFah) {

	fah.fahEf.super.erCtx.sys.fa.mu.Lock()
	fah.fahStatus = esFAWorking

	esEnv.tasks.Run(esFapCloseTask, fahRetain(fah))

	for fah.fahStatus == esFAWorking {
		fah.fahEf.super.erCtx.sys.fa.cond.Wait()
	}

	fah.fahEf.super.erCtx.sys.fa.mu.Unlock()

	fahRelease(fah)
}

// ---------------------------------------------------------------------------
// C: es_faprovider_closeRespond (es_faprovider.c:451-456)
// ---------------------------------------------------------------------------

func esFaproviderCloseRespond(ctx *gaftape.Context) int {
	fah := esGetNativeObj(ctx, 0, esNativeFah).(*esFah)
	fahOkVal(fah, 0)
	return 0
}

// ---------------------------------------------------------------------------
// C: es_faprovider_setSize (es_faprovider.c:463-469)
// ---------------------------------------------------------------------------

func esFaproviderSetSize(ctx *gaftape.Context) int {
	fah := esGetNativeObj(ctx, 0, esNativeFah).(*esFah)
	fah.fahSize = int64(ctx.GetNumber(1))
	return 0
}

// ---------------------------------------------------------------------------
// C: es_fap_seek / es_fap_fsize (es_faprovider.c:472-510)
// ---------------------------------------------------------------------------

// esFapSeek — C: es_fap_seek (fap_seek impl; `lazy` param dropped — C
// ignores it in this implementation).
func esFapSeek(fah *esFah, pos int64, whence int) int64 {
	var np int64

	switch whence {
	case io.SeekStart:
		np = pos

	case io.SeekCurrent:
		np = fah.fahFpos + pos

	case io.SeekEnd:
		if fah.fahSize == -1 {
			return -1
		}
		np = fah.fahSize + pos

	default:
		return -1
	}

	if np < 0 {
		return -1
	}

	fah.fahFpos = np
	return np
}

// esFapFsize — C: es_fap_fsize (fap_fsize impl).
func esFapFsize(fah *esFah) int64 {
	return fah.fahSize
}

// ---------------------------------------------------------------------------
// C: es_fap_stat_task / es_fap_stat (es_faprovider.c:519-585)
// ---------------------------------------------------------------------------

func esFapStatTask(aux any) {
	fah := aux.(*esFah)
	ef := fah.fahEf
	ec := ef.super.erCtx
	ctx := EsContextBegin(ec)

	ctx.SetTop(0)

	EsPushRoot(ctx, ef)
	ctx.GetPropString(-1, "stat")
	esPushNativeObj(ctx, esNativeFah, fahRetain(fah))
	ctx.PushString(fah.fahURL)

	rc := ctx.PCall(2)
	if rc != 0 {
		fahException(fah, ctx)
		EsDumpErr(ctx)
	}

	EsContextEnd(ec, 0, ctx)
	fahRelease(fah)
}

// esFapStat — C: es_fap_stat (fap_stat impl, es_faprovider.c:545-585).
func esFapStat(fap *facore.FAProtocol, url string, buf *facore.FileStat,
	flags int) (int, error) {
	fah := &esFah{}
	ef := fap.Opaque.(*esFap)

	fah.fahURL = url

	fah.fahEf = ef
	EsResourceRetain(&ef.super)

	fah.fahRefcount.Store(2)

	fah.fahStatus = esFAWorking
	esEnv.tasks.Run(esFapStatTask, fah)

	ef.super.erCtx.sys.fa.mu.Lock()

	for fah.fahStatus == esFAWorking {
		ef.super.erCtx.sys.fa.cond.Wait()
	}

	err := fah.fahErr

	ef.super.erCtx.sys.fa.mu.Unlock()

	r := fah.fahReturnValue
	if r == 0 {
		buf.Size = fah.fahSize
		buf.Type = fah.fahType
		buf.MTime = fahMtimeToTime(fah.fahMtime)
	}

	fahRelease(fah)
	return r, err
}

// ---------------------------------------------------------------------------
// C: es_faprovider_statRespond (es_faprovider.c:589-616)
// ---------------------------------------------------------------------------

func esFaproviderStatRespond(ctx *gaftape.Context) int {
	fah := esGetNativeObj(ctx, 0, esNativeFah).(*esFah)

	if ctx.GetBoolean(1) {
		EsGet(ctx).sys.fa.mu.Lock()
		if fah.fahStatus == esFAWorking {
			fah.fahStatus = esFAOK
			fah.fahReturnValue = 0

			fah.fahSize = int64(ctx.GetNumber(2))
			fah.fahType = facore.ContentFile

			typ := ctx.GetString(3)
			if typ == "dir" {
				fah.fahType = facore.ContentDir
			}
			fah.fahMtime = int64(ctx.GetNumber(4))
			EsGet(ctx).sys.fa.cond.Broadcast()
		}
		EsGet(ctx).sys.fa.mu.Unlock()
	} else {
		fahFail(fah, ctx)
	}
	return 0
}

// ---------------------------------------------------------------------------
// C: es_fap_redirect_task / es_fap_redirect (es_faprovider.c:627-681)
// ---------------------------------------------------------------------------

func esFapRedirectTask(aux any) {
	fah := aux.(*esFah)
	ef := fah.fahEf
	ec := ef.super.erCtx
	ctx := EsContextBegin(ec)

	ctx.SetTop(0)

	EsPushRoot(ctx, ef)
	ctx.GetPropString(-1, "redirect")
	esPushNativeObj(ctx, esNativeFah, fahRetain(fah))
	ctx.PushString(fah.fahURL)

	rc := ctx.PCall(2)
	if rc != 0 {
		fahException(fah, ctx)
		EsDumpErr(ctx)
	}

	EsContextEnd(ec, 0, ctx)
	fahRelease(fah)
}

// esFapRedirect — C: es_fap_redirect (fap_redirect impl, es_faprovider.c:654-681).
func esFapRedirect(fap *facore.FAProtocol, url string) string {
	fah := &esFah{}
	ef := fap.Opaque.(*esFap)

	fah.fahURL = url

	fah.fahEf = ef
	EsResourceRetain(&ef.super)

	fah.fahRefcount.Store(2)

	fah.fahStatus = esFAWorking
	esEnv.tasks.Run(esFapRedirectTask, fah)

	ef.super.erCtx.sys.fa.mu.Lock()

	for fah.fahStatus == esFAWorking {
		ef.super.erCtx.sys.fa.cond.Wait()
	}

	ef.super.erCtx.sys.fa.mu.Unlock()
	r := fah.fahRedirect
	fah.fahRedirect = nil
	fahRelease(fah)
	if r == nil {
		return ""
	}
	return *r
}

// ---------------------------------------------------------------------------
// C: es_faprovider_redirectRespond (es_faprovider.c:685-698)
// ---------------------------------------------------------------------------

func esFaproviderRedirectRespond(ctx *gaftape.Context) int {
	fah := esGetNativeObj(ctx, 0, esNativeFah).(*esFah)

	if ctx.GetBoolean(1) {
		newurl := ctx.GetString(2)
		fah.fahRedirect = &newurl
		fahOk(fah)
	} else {
		fahFail(fah, ctx)
	}
	return 0
}

// ---------------------------------------------------------------------------
// C: es_faprovider_register (es_faprovider.c:704-737)
// ---------------------------------------------------------------------------

func esFaproviderRegister(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	name := ctx.RequireString(0)

	ef := &esFap{name: name}
	ef.super.erClass = esResourceFap
	ef.super.Data = ef

	ef.fap.Opaque = ef
	if EsPropIsTrue(ctx, 1, "cacheable") != 0 {
		ef.fap.Flags |= facore.FAPAllowCache
	}

	ef.fap.Fini = esFapFini

	if EsPropIsTrue(ctx, 1, "redirect") != 0 {
		ef.fap.Redirect = esFapRedirect
	}

	ef.fap.Name = ef.name

	// fam-level Protocol adapter carrying the C fap_{open,seek,read,
	// close,fsize,stat} entry points.
	ef.famProto = &esFapFamProtocol{ef: ef}

	EsResourceRetain(&ef.super) // Refcount owned by FAP
	esEnv.fam.RegisterDynamicFAProtocol(&ef.fap)
	if fam := esEnv.fam; fam != nil {
		fam.RegisterProtocol(ef.famProto)
	}

	EsResourceLink(&ef.super, ec, 1)
	EsRootRegister(ctx, 1, ef)
	EsResourcePush(ctx, &ef.super)
	return 1
}

// ---------------------------------------------------------------------------
// fam-level Protocol adapter — C dispatches fap_{open,stat} via
// fa_resolve_proto + the fa_protocol_t vtable; in Go the fam-level
// Protocol owns Open/Stat, resolving through FAResolveProto so the
// JS redirect callback fires exactly where C's fap_redirect does.
// ---------------------------------------------------------------------------

type esFapFamProtocol struct {
	ef *esFap
}

func (p *esFapFamProtocol) Name() string { return p.ef.name }

func (p *esFapFamProtocol) CanHandle(url string) bool {
	return strings.HasPrefix(url, p.ef.name+":")
}

func (p *esFapFamProtocol) Open(url string, extra *facore.OpenExtra) (*facore.Handle, error) {
	fap, fname, err := esEnv.fam.FAResolveProto(url)
	if err != nil {
		return nil, err
	}
	defer facore.FAProtocolRelease(fap)
	flags := 0
	if extra != nil {
		flags = extra.Flags
	}
	return esFapOpen(fap, fname, flags, extra)
}

func (p *esFapFamProtocol) Stat(url string) (*facore.FileStat, error) {
	fap, fname, err := esEnv.fam.FAResolveProto(url)
	if err != nil {
		return nil, err
	}
	defer facore.FAProtocolRelease(fap)
	st := &facore.FileStat{}
	if _, serr := esFapStat(fap, fname, st, 0); serr != nil {
		return nil, serr
	}
	return st, nil
}

func (p *esFapFamProtocol) ScanDir(ctx context.Context, url string) (*facore.Dir, error) {
	// C: no fap_scandir on es_fap — fa_scandir reports unsupported
	return nil, errors.New("Protocol does not support directory listing")
}

// ---------------------------------------------------------------------------
// esFah implements io.ReadCloser + io.Seeker over the C fap ops —
// Read → esFapRead, Seek → esFapSeek, Close → esFapClose.
// ---------------------------------------------------------------------------

func (fah *esFah) Read(buf []byte) (int, error) {
	n := esFapRead(fah, buf, len(buf))
	if n < 0 {
		if fah.fahStatus == esFACancelled {
			return 0, errors.New("Cancelled")
		}
		return 0, errors.New("fap read error")
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (fah *esFah) Seek(offset int64, whence int) (int64, error) {
	np := esFapSeek(fah, offset, whence)
	if np < 0 {
		return fah.fahFpos, errors.New("Invalid argument")
	}
	return np, nil
}

func (fah *esFah) Close() error {
	esFapClose(fah)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fahMtimeToTime — C: buf->fs_mtime = fah->fah_mtime (time_t epoch).
func fahMtimeToTime(m int64) time.Time {
	return time.Unix(m, 0)
}

// C: ES_MODULE("faprovider", fnlist_faprovider) (es_faprovider.c:741-752)
var esFnlistFaprovider = []gaftape.FunctionListEntry{
	{Key: "register", Value: esFaproviderRegister, Nargs: 2},
	{Key: "openRespond", Value: esFaproviderOpenRespond, Nargs: 3},
	{Key: "readRespond", Value: esFaproviderReadRespond, Nargs: 2},
	{Key: "closeRespond", Value: esFaproviderCloseRespond, Nargs: 1},
	{Key: "statRespond", Value: esFaproviderStatRespond, Nargs: 5},
	{Key: "redirectRespond", Value: esFaproviderRedirectRespond, Nargs: 3},
	{Key: "setSize", Value: esFaproviderSetSize, Nargs: 2},
}

// C: ES_MODULE("faprovider", fnlist_faprovider)
func registerEsFaproviderb() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "faprovider",
		Functions: esFnlistFaprovider,
	})
}
