// plugin_ecmascript.go — ECMAScript plugin manager, thin wrapper over the
// canonical pkg/ecmascript layer (the strict 1:1 port of src/ecmascript/).
//
// The earlier loose goja rewrites (plugin_es_*.go) were removed; all JS
// execution now goes through ecmascript.EcmascriptPluginLoad / Unload and
// the canonical native/* modules. This file keeps the manager API surface
// used by the plugin loader/manager (Load/Unload/Get/GetAll/Reload/
// CallFunction/Set*) plus the generic plugin event bus.
package plugins

import (
	"fmt"
	"sync"

	"github.com/czz/movian-go/internal/ecmascript"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gaftape"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/usage"
	"github.com/dop251/goja"
)

// ECMAScriptPlugin represents a loaded ECMAScript plugin
type ECMAScriptPlugin struct {
	FQID    string
	Path    string
	Version int
	Flags   int
	Control string
	Loaded  bool
	Context *goja.Runtime
}

// ECMAScriptPluginManager manages ECMAScript plugins — bookkeeping over
// the canonical ecmascript contexts (one ESContext per plugin, keyed by
// fqid).
type ECMAScriptPluginManager struct {
	mu             sync.RWMutex
	plugins        map[string]*ECMAScriptPlugin
	propMgr        *propcore.PropManager
	routeRegistrar RouteRegistrar
	serviceSystem  *service.ServiceSystem
	faManager      *fileaccesscore.FileAccessManager
	usage          *usage.Reporter // C: usage_event global (usage.c)
}

// NewECMAScriptPluginManager creates a new ECMAScriptPluginManager
func NewECMAScriptPluginManager(u *usage.Reporter) *ECMAScriptPluginManager {
	return &ECMAScriptPluginManager{
		usage:   u,
		plugins: make(map[string]*ECMAScriptPlugin),
	}
}

// SetServiceSystem wires the service system seam into the canonical layer
// (C: service_create uses the global service system).
func (mgr *ECMAScriptPluginManager) SetServiceSystem(ss *service.ServiceSystem) {
	mgr.serviceSystem = ss
	ecmascript.EsSetServiceSystem(ss)
}

// SetPropManager wires the prop manager seam (native/prop module).
func (mgr *ECMAScriptPluginManager) SetPropManager(pm *propcore.PropManager) {
	mgr.propMgr = pm
	ecmascript.EsSetPropManager(pm)
}

// SetRouteRegistrar stores the registrar for API compatibility — the
// canonical es_route layer keeps a global route list consumed by
// EcmascriptOpenuri (registered as be_ecmascript on the backend system).
func (mgr *ECMAScriptPluginManager) SetRouteRegistrar(rr RouteRegistrar) {
	mgr.routeRegistrar = rr
}

// SetFileAccessManager wires the file access manager — the canonical es
// layer resolves all file I/O through the default fam.
func (mgr *ECMAScriptPluginManager) SetFileAccessManager(fam *fileaccesscore.FileAccessManager) {
	mgr.faManager = fam
}

// Load loads an ECMAScript plugin — C: plugin_load → ecmascript_plugin_load
// (plugins.c). Returns nil on success (C's ecmascript_plugin_load always
// returns 0; script errors are dumped via es_dump_err to trace).
func (mgr *ECMAScriptPluginManager) Load(fqid, path string, version int, control string, flags int) error {
	ecmascript.EcmascriptPluginLoad(mgr.usage, fqid, path, version, control, flags)

	mgr.mu.Lock()
	mgr.plugins[fqid] = &ECMAScriptPlugin{
		FQID:    fqid,
		Path:    path,
		Version: version,
		Flags:   flags,
		Control: control,
		Loaded:  true,
	}
	mgr.mu.Unlock()

	EmitESPluginEvent(&ESPluginEvent{
		PluginFQID: fqid,
		EventType:  "load",
	})
	return nil
}

// Unload unloads an ECMAScript plugin — C: ecmascript_plugin_unload.
func (mgr *ECMAScriptPluginManager) Unload(fqid string) {
	ecmascript.EcmascriptPluginUnload(fqid)

	mgr.mu.Lock()
	delete(mgr.plugins, fqid)
	mgr.mu.Unlock()

	EmitESPluginEvent(&ESPluginEvent{
		PluginFQID: fqid,
		EventType:  "unload",
	})
}

// Get returns a plugin by fqid
func (mgr *ECMAScriptPluginManager) Get(fqid string) *ECMAScriptPlugin {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.plugins[fqid]
}

// GetAll returns all loaded plugins
func (mgr *ECMAScriptPluginManager) GetAll() []*ECMAScriptPlugin {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	out := make([]*ECMAScriptPlugin, 0, len(mgr.plugins))
	for _, p := range mgr.plugins {
		out = append(out, p)
	}
	return out
}

// Reload reloads a plugin
func (mgr *ECMAScriptPluginManager) Reload(fqid string) error {
	mgr.mu.RLock()
	p, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()

	if !exists {
		return fmt.Errorf("plugin not found: %s", fqid)
	}

	mgr.Unload(fqid)
	return mgr.Load(fqid, p.Path, p.Version, p.Control, p.Flags)
}

// CallFunction calls a function in a plugin's context — the canonical
// equivalent runs the global function on the context's heap VM under
// ec_mutex (EsContextBegin/End).
func (mgr *ECMAScriptPluginManager) CallFunction(fqid, functionName string, args ...any) (any, error) {
	mgr.mu.RLock()
	_, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("plugin not found: %s", fqid)
	}

	var ec *ecmascript.ESContext
	for _, c := range ecmascript.EcmascriptGetAllContexts() {
		if c.ID() == fqid {
			ec = c
			break
		}
	}
	if ec == nil {
		return nil, fmt.Errorf("plugin context not found: %s", fqid)
	}
	defer ecmascript.EsContextRelease(ec)

	ctx := ecmascript.EsContextBegin(ec)
	defer ecmascript.EsContextEnd(ec, 1, ctx)

	fn := ec.VM().Get(functionName)
	callable, ok := goja.AssertFunction(fn)
	if !ok {
		return nil, fmt.Errorf("function %s not found in plugin %s",
			functionName, fqid)
	}
	jsArgs := make([]goja.Value, len(args))
	for i, a := range args {
		jsArgs[i] = ec.VM().ToValue(a)
	}
	ret, err := callable(goja.Undefined(), jsArgs...)
	if err != nil {
		return nil, err
	}
	return ret.Export(), nil
}

// InvokeHook invokes all hooks of the given type — C: es_hook_invoke.
// Go args are marshalled onto the gaf stack via the pushArgs closure.
func (mgr *ECMAScriptPluginManager) InvokeHook(name string, args ...any) bool {
	n := ecmascript.EsHookInvoke(name,
		func(ctx *gaftape.Context, opaque any) int {
			a := opaque.([]any)
			for _, v := range a {
				pushGafValue(ctx, v)
			}
			return len(a)
		}, args)
	return n > 0
}

// pushGafValue marshals a Go value onto the gaf stack for hook args.
func pushGafValue(ctx *gaftape.Context, v any) {
	switch t := v.(type) {
	case nil:
		ctx.PushUndefined()
	case string:
		ctx.PushString(t)
	case bool:
		ctx.PushBoolean(t)
	case int:
		ctx.PushInt(t)
	case int64:
		ctx.PushNumber(float64(t))
	case float64:
		ctx.PushNumber(t)
	case []byte:
		ctx.PushExternalBuffer(t)
	default:
		ctx.PushString(fmt.Sprintf("%v", t))
	}
}

// ESPluginAPI exposes plugin API functions
type ESPluginAPI struct {
	FQID      string
	Version   int
	Flags     int
	Log       func(string)
	Notify    func(string, string)
	GetConfig func(string) any
	SetConfig func(string, any)
}

// ESPluginEvent represents an event from an ECMAScript plugin
type ESPluginEvent struct {
	PluginFQID string
	EventType  string
	Data       any
}

// ESPluginEventHandler handles events from ECMAScript plugins
type ESPluginEventHandler func(event *ESPluginEvent)

var (
	esPluginHandlers     []ESPluginEventHandler
	esPluginHandlersLock sync.RWMutex
)

// RegisterESPluginHandler registers a handler for ECMAScript plugin events
func RegisterESPluginHandler(handler ESPluginEventHandler) {
	esPluginHandlersLock.Lock()
	defer esPluginHandlersLock.Unlock()
	esPluginHandlers = append(esPluginHandlers, handler)
}

// EmitESPluginEvent emits an event from an ECMAScript plugin
func EmitESPluginEvent(event *ESPluginEvent) {
	esPluginHandlersLock.RLock()
	defer esPluginHandlersLock.RUnlock()
	for _, handler := range esPluginHandlers {
		handler(event)
	}
}

// ESPluginCapability represents a plugin capability
type ESPluginCapability int

const (
	ESCapFileRead ESPluginCapability = 1 << iota
	ESCapFileWrite
	ESCapNetwork
	ESCapSettings
	ESCapNotifications
	ESCapMetadata
)

// HasCapability checks if a plugin has a specific capability
func (mgr *ECMAScriptPluginManager) HasCapability(fqid string, capability ESPluginCapability) bool {
	mgr.mu.RLock()
	plugin, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()

	if !exists {
		return false
	}

	switch capability {
	case ESCapFileRead:
		return plugin.Flags&ecmascript.ECMASCRIPT_FILE_BYPASS_ACL_READ != 0
	case ESCapFileWrite:
		return plugin.Flags&ecmascript.ECMASCRIPT_FILE_BYPASS_ACL_WRITE != 0
	default:
		return true
	}
}

// GetCapabilities returns all capabilities of a plugin
func (mgr *ECMAScriptPluginManager) GetCapabilities(fqid string) []ESPluginCapability {
	mgr.mu.RLock()
	plugin, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()

	if !exists {
		return []ESPluginCapability{}
	}

	capabilities := make([]ESPluginCapability, 0)

	if plugin.Flags&ecmascript.ECMASCRIPT_FILE_BYPASS_ACL_READ != 0 {
		capabilities = append(capabilities, ESCapFileRead)
	}
	if plugin.Flags&ecmascript.ECMASCRIPT_FILE_BYPASS_ACL_WRITE != 0 {
		capabilities = append(capabilities, ESCapFileWrite)
	}

	return capabilities
}
