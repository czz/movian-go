// Canonical 1:1 port of src/ecmascript/es_io.c — HTTP request, inspectors,
// probe, xmlrpc.
package ecmascript

import (
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/czz/movian-go/internal/api/xmlrpc"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gaftape"
	htsmsg "github.com/czz/movian-go/internal/htsmsg"
	misc "github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// esEnv.backendSystem is the backend_probe seam (C: global backend_probe()).

// EsSetBackendSystem wires the backend system for probe().
func EsSetBackendSystem(bs *backendcore.BackendSystem) { esEnv.backendSystem = bs }

// ---------------------------------------------------------------------------
// es_http_request_t — C: es_io.c:35-58
// ---------------------------------------------------------------------------

// C: es_http_request_t
type esHTTPRequest struct {
	super *ESResource

	ehrURL             string
	ehrRequestHeaders  httpnet.HTTPHeaderList
	ehrResponseHeaders httpnet.HTTPHeaderList
	ehrPostdata        *misc.HtsbufQueue
	ehrPostContentType string
	ehrMethod          string
	ehrHTTPArgs        []string // C: char **ehr_httpargs (strvec)

	ehrFlags     int
	ehrHeadreq   int
	ehrMinExpire int
	ehrCache     int

	ehrResult *misc.Buf

	ehrErrbuf [512]byte

	ehrError      int
	ehrHTTPStatus int
}

// ehrCleanup — C: ehr_cleanup (es_io.c:65-75)
func ehrCleanup(ehr *esHTTPRequest) {
	ehr.ehrResponseHeaders.Free()
	if ehr.ehrPostdata != nil {
		// C: htsbuf_queue_flush — drop all queued data
		ehr.ehrPostdata.Flush()
		ehr.ehrPostdata = nil
	}
	if ehr.ehrResult != nil {
		ehr.ehrResult.Release()
		ehr.ehrResult = nil
	}
}

// esHTTPRequestDestroy — C: es_http_request_destroy (es_io.c:82-87)
func esHTTPRequestDestroy(eres *ESResource) {
	ehr := eres.Data.(*esHTTPRequest)
	ehrCleanup(ehr)
	EsRootUnregister(eres.erCtx.ecGaf, eres)
	EsResourceUnlink(ehr.super)
}

// esHTTPRequestInfo — C: es_http_request_info (es_io.c:93-96)
func esHTTPRequestInfo(eres *ESResource) string {
	ehr := eres.Data.(*esHTTPRequest)
	return ehr.ehrURL
}

// C: es_resource_http_request (es_io.c:102-107)
var esResourceHTTPRequest = &ESResourceClass{
	ErcName:    "http_request",
	ErcDestroy: esHTTPRequestDestroy,
	ErcInfo:    esHTTPRequestInfo,
}

// httpAddArgs — C: http_add_args (es_io.c:113-123)
func httpAddArgs(ctx *gaftape.Context, httpargs *[]string) {
	ctx.Enum(-1, 0)

	for ctx.Next(-1, true) {
		*httpargs = append(*httpargs, ctx.SafeToString(-2))
		*httpargs = append(*httpargs, ctx.SafeToString(-1))
		ctx.Pop2()
	}
	ctx.Pop()
}

// disableCacheOnHTTPHeaders — C: disable_cache_on_http_headers (es_io.c:129-138)
func disableCacheOnHTTPHeaders(list *httpnet.HTTPHeaderList) int {
	for _, hh := range list.List {
		if strings.EqualFold(hh.Key, "user-agent") {
			continue
		}
		return 1
	}
	return 0
}

// esHTTPDoRequest — C: es_http_do_request (es_io.c:144-195)
func esHTTPDoRequest(ehr *esHTTPRequest) {
	fam := esEnv.fam

	if ehr.ehrCache != 0 &&
		(ehr.ehrMethod == "" || ehr.ehrMethod == "GET") &&
		ehr.ehrHeadreq == 0 &&
		ehr.ehrPostdata == nil {

		// GET + cache → fa_load path
		var qargs [][2]string
		for i := 0; i+1 < len(ehr.ehrHTTPArgs); i += 2 {
			qargs = append(qargs, [2]string{ehr.ehrHTTPArgs[i],
				ehr.ehrHTTPArgs[i+1]})
		}
		fb, err := facore.FALoad2(fam, ehr.ehrURL, &facore.FALoadArgs{
			QueryArgs:    qargs,
			Flags:        ehr.ehrFlags,
			MinExpire:    ehr.ehrMinExpire,
			ReqHeaders:   &ehr.ehrRequestHeaders,
			RespHeaders:  &ehr.ehrResponseHeaders,
			ProtocolCode: &ehr.ehrHTTPStatus,
		})
		if err != nil {
			copy(ehr.ehrErrbuf[:], err.Error())
		}
		if fb != nil {
			ehr.ehrResult = misc.BufCreateAndCopy(fb.Size, fb.Data)
		}
		if ehr.ehrResult == nil || err != nil {
			ehr.ehrError = 1
		}

	} else {
		var postdata *misc.HtsbufQueue
		var postCT string
		if ehr.ehrPostdata != nil {
			postdata = ehr.ehrPostdata
			postCT = ehr.ehrPostContentType
		}
		ehr.ehrError = esEnv.fam.HTTPReqv(ehr.ehrURL, []any{
			facore.HTTPTagArgList, ehr.ehrHTTPArgs,
			facore.HTTPTagResultPtr, esResultPtr(ehr),
			facore.HTTPTagErrbuf, ehr.ehrErrbuf[:],
			facore.HTTPTagPostData, postdata, postCT,
			facore.HTTPTagFlags, ehr.ehrFlags,
			facore.HTTPTagResponseHeaders, &ehr.ehrResponseHeaders,
			facore.HTTPTagRequestHeaders, &ehr.ehrRequestHeaders,
			facore.HTTPTagMethod, ehr.ehrMethod,
			facore.HTTPTagResponseCode, &ehr.ehrHTTPStatus,
		}, nil, nil)

		if ehr.ehrError != 0 {
			ehr.ehrResult = nil
		}
	}

	// C: strvec_free + http_headers_free(request_headers)
	ehr.ehrHTTPArgs = nil
	ehr.ehrRequestHeaders.Free()
}

// esResultPtr — C: ehr->ehr_headreq ? NULL : &ehr->ehr_result
func esResultPtr(ehr *esHTTPRequest) **misc.Buf {
	if ehr.ehrHeadreq != 0 {
		return nil
	}
	return &ehr.ehrResult
}

// esHTTPPushResult — C: es_http_push_result (es_io.c:201-242)
func esHTTPPushResult(ctx *gaftape.Context, ehr *esHTTPRequest) {
	resIdx := ctx.PushObject()

	if ehr.ehrResult != nil {
		buf := ctx.PushFixedBuffer(ehr.ehrResult.Len())
		copy(buf, ehr.ehrResult.C8())
		ctx.PutPropString(resIdx, "buffer")
	}

	arrIdx := ctx.PushArray()

	idx := 0
	for _, hh := range ehr.ehrResponseHeaders.List {
		ctx.PushString(hh.Key)
		ctx.PutPropIndex(arrIdx, idx)
		idx++
		ctx.PushString(hh.Value)
		ctx.PutPropIndex(arrIdx, idx)
		idx++
	}

	ehr.ehrResponseHeaders.Free()
	ctx.PutPropString(resIdx, "responseheaders")

	ctx.PushInt(ehr.ehrHTTPStatus)
	ctx.PutPropString(resIdx, "statuscode")
}

// ehrTask — C: ehr_task (es_io.c:250-279)
func ehrTask(aux any) {
	ehr := aux.(*esHTTPRequest)
	esHTTPDoRequest(ehr)

	ec := ehr.super.erCtx
	ctx := EsContextBegin(ec)

	EsPushRoot(ctx, ehr)

	if ehr.ehrError == 0 ||
		(ehr.ehrFlags&facore.FaContentOnError != 0 && ehr.ehrHTTPStatus != 0) {
		ctx.PushBoolean(false)
		esHTTPPushResult(ctx, ehr)
	} else {
		ctx.PushString(misc.CStr(ehr.ehrErrbuf[:]))
		ctx.PushUndefined()
	}

	rc := ctx.PCall(2)
	if rc != 0 {
		EsDumpErr(ctx)
	}
	ctx.Pop()

	EsResourceDestroy(ehr.super)

	EsContextEnd(ec, 1, ctx)
}

// esHTTPReq — C: es_http_req (es_io.c:285-406)
func esHTTPReq(ctx *gaftape.Context) int {
	url := ctx.ToString(0)

	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Invalid URL for HTTP request, "+
			"begins with: %.10s", url)
	}

	ec := EsGet(ctx)
	ehr := &esHTTPRequest{}
	ehr.super = EsResourceAlloc(esResourceHTTPRequest, ehr)

	ehr.ehrURL = url

	if EsPropIsTrue(ctx, 1, "debug") != 0 {
		ehr.ehrFlags |= facore.FaDebug
	}
	if EsPropIsTrue(ctx, 1, "noFollow") != 0 {
		ehr.ehrFlags |= facore.FaNofollow
	}
	if EsPropIsTrue(ctx, 1, "compression") != 0 {
		ehr.ehrFlags |= facore.FaCompression
	}
	if EsPropIsTrue(ctx, 1, "noAuth") != 0 {
		ehr.ehrFlags |= facore.FaDisableAuth
	}
	if EsPropIsTrue(ctx, 1, "noFail") != 0 {
		ehr.ehrFlags |= facore.FaContentOnError
	}
	if EsPropIsTrue(ctx, 1, "verifySSL") != 0 {
		ehr.ehrFlags |= facore.FaSSLVerify
	}

	ehr.ehrHeadreq = EsPropIsTrue(ctx, 1, "headRequest")
	ehr.ehrMinExpire = EsPropToInt(ctx, 1, "cacheTime", 0)
	if EsPropIsTrue(ctx, 1, "caching") != 0 || ehr.ehrMinExpire != 0 {
		ehr.ehrCache = 1
	}

	// -- 'headers' argument
	ctx.GetPropString(1, "headers")

	if ctx.IsObject(-1) {
		ctx.Enum(-1, 0)
		for ctx.Next(-1, true) {
			if ctx.IsObjectCoercible(-1) {
				k := ctx.SafeToString(-2)
				v := ctx.SafeToString(-1)
				ehr.ehrRequestHeaders.Add(k, v, false)
			}
			ctx.Pop2()
		}
		ctx.Pop()
	}
	ctx.Pop()

	// -- 'postdata' argument
	ctx.GetPropString(1, "postdata")

	if ctx.IsBuffer(-1) {
		buf := ctx.GetBuffer(-1)
		ehr.ehrPostdata = &misc.HtsbufQueue{}
		ehr.ehrPostdata.HtsbufQueueSetup(0)
		ehr.ehrPostdata.Append(buf)
		ehr.ehrPostContentType = "application/octet-stream"
	} else if ctx.IsObject(-1) {
		prefix := ""

		ehr.ehrPostdata = &misc.HtsbufQueue{}
		ehr.ehrPostdata.HtsbufQueueSetup(0)

		ctx.Enum(-1, 0)
		for ctx.Next(-1, true) {
			if ctx.IsObjectCoercible(-1) {
				k := ctx.SafeToString(-2)
				v := ctx.SafeToString(-1)

				ehr.ehrPostdata.Append([]byte(prefix))
				ehr.ehrPostdata.AppendAndEscapeURL(k)
				ehr.ehrPostdata.Append([]byte("="))
				ehr.ehrPostdata.AppendAndEscapeURL(v)
				prefix = "&"
			}
			ctx.Pop2()
		}
		ctx.Pop()

		ehr.ehrPostContentType = "application/x-www-form-urlencoded"

	} else if ctx.IsString(-1) {
		str := ctx.GetLstring(-1)
		ehr.ehrPostdata = &misc.HtsbufQueue{}
		ehr.ehrPostdata.HtsbufQueueSetup(0)
		ehr.ehrPostdata.Append([]byte(str))
		ehr.ehrPostContentType = "text/plain"
	}

	ctx.Pop()

	// -- 'args'
	ctx.GetPropString(1, "args")
	if ctx.IsObject(-1) {
		httpAddArgs(ctx, &ehr.ehrHTTPArgs)
	}
	ctx.Pop()

	// -- 'method'
	ctx.GetPropString(1, "method")
	ehr.ehrMethod = ctx.GetString(-1)
	ctx.Pop()

	// Disable caching when specific headers are present
	if ehr.ehrCache != 0 && ehr.ehrMinExpire == 0 {
		if disableCacheOnHTTPHeaders(&ehr.ehrRequestHeaders) != 0 {
			ehr.ehrCache = 0
		} else {
			ehr.ehrCache = 1
		}
	}

	if ctx.IsFunction(2) {
		// Async mode
		EsResourceLink(ehr.super, ec, 1)
		EsRootRegister(ctx, 2, ehr)
		esEnv.tasks.Run(ehrTask, ehr)
		return 0
	}

	var state gaftape.ThreadState
	EsContextSuspend(ec, ctx, &state)

	esHTTPDoRequest(ehr)

	EsContextResume(ec, ctx, &state)

	if ehr.ehrError != 0 {
		errURL := ehr.ehrURL
		errBuf := misc.CStr(ehr.ehrErrbuf[:])
		ehrCleanup(ehr)
		ctx.Error(gaftape.GAF_ERR_ERROR, "HTTP request failed %s -- %s",
			errURL, errBuf)
	}

	esHTTPPushResult(ctx, ehr)
	ehrCleanup(ehr)
	return 1
}

// ---------------------------------------------------------------------------
// HTTP inspectors — C: es_io.c:412-770
// ---------------------------------------------------------------------------

// C: es_http_inspector_t
type esHTTPInspector struct {
	super      *ESResource
	ehiPattern string
	ehiRegex   misc.HtsRegex
	ehiPrio    int
	ehiAsync   int
}

// C: es_http_inspection_t
type esHTTPInspection struct {
	refcount atomic.Int32
	hri      *facore.HTTPRequestInspection
	ehi      *esHTTPInspector
	url      string
	done     bool
	rval     int
}

// C: http_inspectors list + mutex + cond (es_io.c:448-450)
// C: es_http_inspectors + mutex + cond (es_io.c). Sorted by prio desc —
// live on System.inspector (see ecmascript.go).

// esHTTPInspectionRelease — C: es_http_inspection_release (es_io.c:458-467)
func esHTTPInspectionRelease(insp *esHTTPInspection) {
	if insp.refcount.Add(-1) != 0 {
		return
	}
	EsResourceRelease(insp.ehi.super)
	insp.url = ""
}

// C: ES_NATIVE_CLASS(http_inspection, es_http_inspection_release)
var esNativeHTTPInspection = &EcmascriptNativeClass{
	Name:    "http_inspection",
	Release: func(p any) { esHTTPInspectionRelease(p.(*esHTTPInspection)) },
}

func registerEsIoa() {
	ecmascriptRegisterNativeClass(esNativeHTTPInspection)
}

// esHTTPInspectorDestroy — C: es_http_inspector_destroy (es_io.c:481-494)
func esHTTPInspectorDestroy(eres *ESResource) {
	ehi := eres.Data.(*esHTTPInspector)

	EsRootUnregister(eres.erCtx.ecGaf, eres)

	eres.erCtx.sys.inspector.mu.Lock()
	for i, x := range eres.erCtx.sys.inspector.list {
		if x == ehi {
			eres.erCtx.sys.inspector.list = slices.Delete(eres.erCtx.sys.inspector.list, i, i+1)
			break
		}
	}
	eres.erCtx.sys.inspector.mu.Unlock()

	misc.HtsRegfree(&ehi.ehiRegex)

	EsResourceUnlink(ehi.super)
}

// esHTTPInspectorInfo — C: es_http_inspector_info (es_io.c:500-503)
func esHTTPInspectorInfo(eres *ESResource) string {
	ehi := eres.Data.(*esHTTPInspector)
	return ehi.ehiPattern + " (prio:" + strconv.Itoa(ehi.ehiPrio) + ")"
}

// C: es_resource_http_inspector (es_io.c:508-513)
var esResourceHTTPInspector = &ESResourceClass{
	ErcName:    "http-inspector",
	ErcDestroy: esHTTPInspectorDestroy,
	ErcInfo:    esHTTPInspectorInfo,
}

// ehiCmp — C: ehi_cmp (es_io.c:519-522) — sort by prio descending.
func ehiCmp(a, b *esHTTPInspector) int {
	return b.ehiPrio - a.ehiPrio
}

// esHTTPInspectorCreate — C: es_http_inspector_create (es_io.c:560-604)
func esHTTPInspectorCreate(ctx *gaftape.Context) int {
	str := ctx.SafeToString(0)
	async := 0
	if ctx.GetBoolean(2) {
		async = 1
	}

	var s string
	if !strings.HasPrefix(str, "^") {
		s = "^" + str
	} else {
		s = str
	}
	if strings.HasSuffix(s, ".*") {
		s = s[:len(s)-2]
	}
	str = s

	ec := EsGet(ctx)

	EsGet(ctx).sys.inspector.mu.Lock()

	ehi := &esHTTPInspector{}
	ehi.super = EsResourceAlloc(esResourceHTTPInspector, ehi)
	var errmsg string
	if misc.HtsRegcomp(&ehi.ehiRegex, str, &errmsg) != 0 {
		EsGet(ctx).sys.inspector.mu.Unlock()
		ctx.Error(gaftape.GAF_ERR_ERROR,
			"Invalid regular expression for http_inspector %s -- %s",
			str, errmsg)
	}

	ehi.ehiPattern = str
	ehi.ehiPrio = len(str)
	ehi.ehiAsync = async

	esDebug(ec, "Adding HTTP inspection for pattern %s", str)

	// LIST_INSERT_SORTED — descending prio
	inserted := false
	for i, x := range EsGet(ctx).sys.inspector.list {
		if ehiCmp(ehi, x) < 0 {
			EsGet(ctx).sys.inspector.list = slices.Insert(EsGet(ctx).sys.inspector.list, i, ehi)
			inserted = true
			break
		}
	}
	if !inserted {
		EsGet(ctx).sys.inspector.list = append(EsGet(ctx).sys.inspector.list, ehi)
	}

	EsResourceLink(ehi.super, ec, 1)

	EsGet(ctx).sys.inspector.mu.Unlock()

	EsRootRegister(ctx, 1, ehi)

	EsResourcePush(ctx, ehi.super)
	return 1
}

// getInsp — C: get_insp (es_io.c:609-618) — reads "hri" prop off `this`.
func getInsp(ctx *gaftape.Context) *esHTTPInspection {
	ctx.PushThis()
	ctx.GetPropString(-1, "hri")
	v := esGetNativeObj(ctx, -1, esNativeHTTPInspection)
	ctx.Pop2()
	if insp, ok := v.(*esHTTPInspection); ok {
		return insp
	}
	return nil
}

// esHTTPInspectorDone — C: es_http_inspector_done (es_io.c:622-632)
func esHTTPInspectorDone(insp *esHTTPInspection, rval int) {
	insp.ehi.super.erCtx.sys.inspector.mu.Lock()
	insp.rval = rval
	if insp.ehi.ehiAsync != 0 {
		insp.ehi.super.erCtx.sys.inspector.cond.Broadcast()
		insp.done = true
	}
	insp.ehi.super.erCtx.sys.inspector.mu.Unlock()
}

// esHTTPInspectorFail — C: es_http_inspector_fail (es_io.c:639-651)
func esHTTPInspectorFail(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	insp := getInsp(ctx)
	hri := insp.hri
	if hri == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "HTTP request no longer valid")
	}

	reason := ctx.SafeToString(0)
	esDebug(ec, "Inspector failing request -- %s", reason)
	facore.HTTPClientFailReq(hri, ctx.SafeToString(0))
	esHTTPInspectorDone(insp, 0)
	return 0
}

// esHTTPInspectorProceed — C: es_http_inspector_proceed (es_io.c:658-667)
func esHTTPInspectorProceed(ctx *gaftape.Context) int {
	insp := getInsp(ctx)
	hri := insp.hri
	if hri == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "HTTP request no longer valid")
	}
	esHTTPInspectorDone(insp, 0)
	return 0
}

// esHTTPInspectorIgnore — C: es_http_inspector_ignore (es_io.c:673-682)
func esHTTPInspectorIgnore(ctx *gaftape.Context) int {
	insp := getInsp(ctx)
	hri := insp.hri
	if hri == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "HTTP request no longer valid")
	}
	esHTTPInspectorDone(insp, 1)
	return 0
}

// esHTTPInspectorSetHeader — C: es_http_inspector_set_header (es_io.c:688-703)
func esHTTPInspectorSetHeader(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	insp := getInsp(ctx)
	hri := insp.hri
	if hri == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "HTTP request no longer valid")
	}

	key := ctx.SafeToString(0)
	value := ctx.SafeToString(1)
	esDebug(ec, "Inspector adding header %s = %s", key, value)
	facore.HTTPClientSetHeader(hri, key, value)
	return 0
}

// esHTTPInspectorSetCookie — C: es_http_inspector_set_cookie (es_io.c:709-726)
func esHTTPInspectorSetCookie(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	insp := getInsp(ctx)
	hri := insp.hri
	if hri == nil {
		ctx.Error(gaftape.GAF_ERR_ERROR, "HTTP request no longer valid")
	}

	key := ctx.SafeToString(0)
	value := ctx.GetString(1)
	if value != "" {
		esDebug(ec, "Inspector setting cookie %s = %s", key, value)
	} else {
		esDebug(ec, "Inspector clearing cookie %s", key)
	}
	facore.HTTPClientSetCookie(hri, key, value)
	return 0
}

// C: fnlist_inspector (es_io.c:732-739)
var fnlistInspector = []gaftape.FunctionListEntry{
	{Key: "fail", Value: esHTTPInspectorFail, Nargs: 1},
	{Key: "proceed", Value: esHTTPInspectorProceed, Nargs: 0},
	{Key: "ignore", Value: esHTTPInspectorIgnore, Nargs: 0},
	{Key: "setHeader", Value: esHTTPInspectorSetHeader, Nargs: 2},
	{Key: "setCookie", Value: esHTTPInspectorSetCookie, Nargs: 2},
}

// esHTTPInspectorRun — C: es_http_inspector_run (es_io.c:745-792)
func esHTTPInspectorRun(aux any) {
	insp := aux.(*esHTTPInspection)
	ehi := insp.ehi
	hri := insp.hri

	ec := ehi.super.erCtx

	esDebug(ec, "Inspecting %s using %s", insp.url, ehi.ehiPattern)

	ctx := EsContextBegin(ec)

	objIdx := ctx.PushObject()

	ctx.PutFunctionList(objIdx, fnlistInspector)

	insp.refcount.Add(1)
	esPushNativeObj(ctx, esNativeHTTPInspection, insp)
	ctx.PutPropString(objIdx, "hri")

	ctx.PushString(insp.url)
	ctx.PutPropString(objIdx, "url")

	ctx.PushBoolean(hri.AuthHasFailed != 0)
	ctx.PutPropString(objIdx, "authFailed")

	EsPushRoot(ctx, ehi)
	ctx.Dup(objIdx)

	var rval int
	rc := ctx.PCall(1)
	if rc != 0 {
		EsDumpErr(ctx)
		rval = 1
	} else {
		if ctx.GetBoolean(-1) {
			rval = 1
		}
	}

	ctx.Pop2()
	if ehi.ehiAsync == 0 {
		insp.rval = rval
	}

	EsContextEnd(ec, 1, ctx)

	esHTTPInspectionRelease(insp)
}

// esHTTPInspect — C: es_http_inspect (es_io.c:798-843)
// Returns 1 if no processing happened.
func esHTTPInspect(url string, hri *facore.HTTPRequestInspection) int {
	esEnv.sys.inspector.mu.Lock()

	var ehi *esHTTPInspector
	for _, x := range esEnv.sys.inspector.list {
		if misc.HtsRegexec(&x.ehiRegex, url, 0, nil) == 0 {
			ehi = x
			break
		}
	}

	if ehi == nil {
		esEnv.sys.inspector.mu.Unlock()
		return 1
	}

	esResourceRetain(ehi.super)

	insp := &esHTTPInspection{
		ehi: ehi,
		hri: hri,
		url: url,
	}
	insp.refcount.Store(2)

	if ehi.ehiAsync != 0 {
		esEnv.tasks.Run(esHTTPInspectorRun, insp)
		for !insp.done {
			esEnv.sys.inspector.cond.Wait()
		}
		insp.hri = nil

		esEnv.sys.inspector.mu.Unlock()
	} else {
		esEnv.sys.inspector.mu.Unlock()
		esHTTPInspectorRun(insp)
	}
	rval := insp.rval
	esHTTPInspectionRelease(insp)
	return rval
}

// C: REGISTER_HTTP_REQUEST_INSPECTOR(es_http_inspect) (es_io.c:846) —
// link-time registration; Go wires it onto the fam when fam arrives
// (EsSetFAM in es_stats.go).
var esHTTPInspectorEntry = &facore.HTTPRequestInspector{
	Check: func(url string, hri *facore.HTTPRequestInspection) bool {
		return esHTTPInspect(url, hri) != 0
	},
}

// ---------------------------------------------------------------------------
// es_probe — C: es_io.c:852-874
// ---------------------------------------------------------------------------

func esProbe(ctx *gaftape.Context) int {
	url := ctx.RequireString(0)
	timeout := ctx.ToInt32(1)

	var res backendcore.ProbeResult
	var perr error
	if esEnv.backendSystem != nil {
		res, perr = esEnv.backendSystem.Probe(url, int(timeout))
	} else {
		res = backendcore.ProbeFail
	}

	ctx.PushObject()

	if res != backendcore.ProbeOK {
		errmsg := ""
		if perr != nil {
			errmsg = perr.Error()
		}
		ctx.PushString(errmsg)
		ctx.PutPropString(-2, "errmsg")
	}

	ctx.PushInt(int(res))
	ctx.PutPropString(-2, "result")
	return 1
}

// ---------------------------------------------------------------------------
// es_xmlrpc — C: es_io.c:881-906
// ---------------------------------------------------------------------------

func esXMLRPC(ctx *gaftape.Context) int {
	argc := ctx.GetTop()
	if argc < 2 {
		return -5
	}

	url := ctx.ToString(0)
	method := ctx.ToString(1)
	json := ctx.ToString(2)

	args, err := htsmsg.DeserializeJSON(json)
	if args == nil {
		errmsg := "parse error"
		if err != nil {
			errmsg = err.Error()
		}
		ctx.Error(gaftape.GAF_ERR_ERROR, "Bad interim JSON -- %s", errmsg)
	}

	reply, rerr := xmlrpc.Request(esEnv.fam, url, method, args)

	if reply == nil {
		errmsg := ""
		if rerr != nil {
			errmsg = rerr.Error()
		}
		ctx.Error(gaftape.GAF_ERR_ERROR,
			"XMLRPC request %s to %s failed -- %s",
			method, url, errmsg)
	}

	esPushNativeObj(ctx, esNativeHtsmsg, reply)
	return 1
}

// ---------------------------------------------------------------------------
// fnlist_io — C: es_io.c:909-915
// ---------------------------------------------------------------------------

var esFnlistIO = []gaftape.FunctionListEntry{
	{Key: "httpReq", Value: esHTTPReq, Nargs: 3},
	{Key: "httpInspectorCreate", Value: esHTTPInspectorCreate, Nargs: 3},
	{Key: "probe", Value: esProbe, Nargs: 2},
	{Key: "xmlrpc", Value: esXMLRPC, Nargs: -1},
}

// C: ES_MODULE("io", fnlist_io)
func registerEsIob() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "io",
		Functions: esFnlistIO,
	})
}
