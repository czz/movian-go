// Canonical 1:1 port of src/ecmascript/es_stats.c — HTTP debug endpoints
// for ecmascript contexts (#if ENABLE_HTTPSERVER — httpserver is enabled
// on linux).
package ecmascript

import (
	"github.com/czz/movian-go/internal/asyncio"
	"github.com/czz/movian-go/internal/blobcache"
	"github.com/czz/movian-go/internal/db/kvstore"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/keyring"
	"github.com/czz/movian-go/internal/metadata"
	misc "github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/notifications"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/trace"
)

// EsSetHTTPServer wires the http server for the stats endpoints.
func EsSetHTTPServer(s *httpnet.HTTPServer) { esEnv.httpServer = s }

// EsSetAsyncIO wires gconf.asyncio (websocket connect/DNS/tasks).
func EsSetAsyncIO(aio *asyncio.AsyncIO) { esEnv.asyncIO = aio }

// EsSetBlobCache wires the blobcache (C: implicit global read by
// es_cache_put/get).
func EsSetBlobCache(bc *blobcache.BlobCache) { esEnv.blobCache = bc }

// EsSetSystem injects the ecmascript subsystem (C: file statics).
func EsSetSystem(sys *System) {
	if sys != nil {
		esEnv.sys = sys
	}
}

// EsSetFAM wires the file access manager (C: implicit global fa context).
func EsSetFAM(fam *facore.FileAccessManager) {
	esEnv.fam = fam
	if fam != nil {
		// C: REGISTER_HTTP_REQUEST_INSPECTOR link-time init
		fam.HTTPRequestInspectorRegister(esHTTPInspectorEntry)
	}
}

// EsSetKeyring wires the keyring (C: implicit global read by
// keyring_lookup callers).
func EsSetKeyring(kr *keyring.Keyring) { esEnv.keyring = kr }

// EsSetNotifMgr wires the notification manager (C: notify_add /
// message_popup globals read by es_popup/es_textDialog).
func EsSetNotifMgr(nm *notifications.NotificationManager) { esEnv.notifMgr = nm }

// EsSetKVStore wires the kvstore (C: global kvstore_* funcs read by
// es_kvstore builtins).
func EsSetKVStore(kvs *kvstore.KVStore) { esEnv.kvstore = kvs }

// EsSetTaskSystem wires the task system (C: task_* globals used by
// es_websocket/es_io/es_faprovider/es_scrobble/es_service).
func EsSetTaskSystem(ts *task.TaskSystem) { esEnv.tasks = ts }

// EsSetTraceSystem injects the trace system (C: trace() global).
func EsSetTraceSystem(ts *trace.TraceSystem) { esEnv.tracer = ts }

// EsSetMetadataManager wires the metadata manager (C: implicit default).
func EsSetMetadataManager(mm *metadata.MetadataManager) { esEnv.metadata = mm }

// dumpResourceList — C: dump_resource_list (es_stats.c:34-48)
func dumpResourceList(out *misc.HtsbufQueue, list []*ESResource) {
	for _, er := range list {
		if er.erClass.ErcInfo != nil {
			out.QPrintf("\t%s: %s\n", er.erClass.ErcName,
				er.erClass.ErcInfo(er))
		} else {
			out.QPrintf("\t%s\n", er.erClass.ErcName)
		}
	}
}

// dumpContext — C: dump_context (es_stats.c:53-86)
func dumpContext(out *misc.HtsbufQueue, ec *ESContext) {
	ec.ecMutex.Lock()

	out.QPrintf("\n--- %s ------------------------\n", ec.ecID)

	out.QPrintf("  Loaded from %s\n", ec.ecPath)

	out.QPrintf("  Memory usage, current: %d bytes, peak: %d\n",
		ec.ecMemActive, ec.ecMemPeak)
	out.QPrintf("  Rooted Ecmascript objects: %d\n",
		ec.ecRootedObjects)

	out.QPrintf("  Native objects referenced:\n")
	for i := range ECMASCRIPT_MAX_NATIVE_CLASSES {
		if ec.ecNativeInstances[i] != 0 {
			name := EcmascriptNativeClassName(i)
			if name != "" {
				out.QPrintf("    %s: %d active\n",
					name, ec.ecNativeInstances[i])
			}
		}
	}

	out.QPrintf("  Attached permanent resources:\n")
	dumpResourceList(out, ec.ecResourcesPermanent)

	ec.ecMutex.Unlock()
}

// dumpstats — C: dumpstats (es_stats.c:93-112)
func dumpstats(hc *httpnet.HTTPConnection, remain string, opaque any,
	method httpnet.HTTPCmd) int {

	out := &misc.HtsbufQueue{}
	out.HtsbufQueueSetup(0)

	vec := EcmascriptGetAllContexts()

	for _, ec := range vec {
		dumpContext(out, ec)
	}

	EcmascriptReleaseContextVector(vec)

	out.QPrintf("\n")

	return hc.HTTPSendReply(0,
		"text/plain; charset=utf-8", "", "", 0, out.Bytes())
}

// dogc — C: dogc (es_stats.c:118-140)
func dogc(hc *httpnet.HTTPConnection, remain string, opaque any,
	method httpnet.HTTPCmd) int {

	out := &misc.HtsbufQueue{}
	out.HtsbufQueueSetup(0)

	vec := EcmascriptGetAllContexts()

	for _, ec := range vec {
		ec.ecMutex.Lock()
		ec.ecGaf.Gc(0)
		ec.ecMutex.Unlock()
	}

	EcmascriptReleaseContextVector(vec)

	out.QPrintf("OK\n")

	return hc.HTTPSendReply(0,
		"text/plain; charset=utf-8", "", "", 0, out.Bytes())
}

// EcmascriptStatsStart — C: ecmascript_stats_init (es_stats.c:146-151)
// + INITME(INIT_GROUP_API, ...). Called from the api init group.
func EcmascriptStatsStart() {
	if esEnv.httpServer == nil {
		return
	}
	esEnv.httpServer.HTTPPathAdd("/api/ecmascript/stats", nil, dumpstats, true)
	esEnv.httpServer.HTTPPathAdd("/api/ecmascript/gc", nil, dogc, true)
}
