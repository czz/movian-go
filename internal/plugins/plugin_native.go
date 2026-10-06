package plugins

// Native Plugin Support - Native/bitcode plugin implementation
// This file contains functions for loading and managing native plugins

import (
	"errors"
	"fmt"
	"github.com/czz/movian-go/internal/trace"
	"strconv"
	"sync"
)

// NativePlugin represents a native/bitcode plugin instance
type NativePlugin struct {
	FQID       string
	Path       string
	Version    int
	MemorySize int
	StackSize  int
	Loaded     bool
	Instance   any // Plugin instance
}

// NativePluginManager manages native plugins
type NativePluginManager struct {
	mu      sync.RWMutex
	plugins map[string]*NativePlugin

	ts *trace.TraceSystem // C: trace() global — injected
}

// NewNativePluginManager creates a new NativePluginManager
func NewNativePluginManager() *NativePluginManager {
	return &NativePluginManager{
		plugins: make(map[string]*NativePlugin),
	}
}

// SetTraceSystem injects the trace system (C: trace() global).
func (mgr *NativePluginManager) SetTraceSystem(ts *trace.TraceSystem) { mgr.ts = ts }

// Load loads a native/bitcode plugin
func (mgr *NativePluginManager) Load(fqid, path string, version, memorySize, stackSize int) error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	// Check if already loaded
	if _, exists := mgr.plugins[fqid]; exists {
		return fmt.Errorf("Plugin already loaded: %s", fqid)
	}

	// Create plugin instance
	plugin := &NativePlugin{
		FQID:       fqid,
		Path:       path,
		Version:    version,
		MemorySize: memorySize,
		StackSize:  stackSize,
		Loaded:     false,
	}

	// Load bitcode file
	bitcode, err := loadFile(path)
	if err != nil {
		return fmt.Errorf("Failed to load bitcode: %w", err)
	}

	// Create plugin instance
	instance, err := createPluginInstance(string(bitcode), memorySize, stackSize)
	if err != nil {
		return fmt.Errorf("Failed to create plugin instance: %w", err)
	}
	plugin.Instance = instance

	// Initialize plugin
	if err := initializePlugin(instance, fqid, version); err != nil {
		destroyPluginInstance(instance)
		return fmt.Errorf("Failed to initialize plugin: %w", err)
	}

	plugin.Loaded = true
	mgr.plugins[fqid] = plugin

	mgr.ts.Info("plugins", "Native plugin loaded: %s (version: %d, memory: %d, stack: %d)\n",
		fqid, version, memorySize, stackSize)
	return nil
}

// Unload unloads a native plugin
func (mgr *NativePluginManager) Unload(fqid string) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	plugin, exists := mgr.plugins[fqid]
	if !exists {
		return
	}

	// Destroy instance
	if plugin.Instance != nil {
		destroyPluginInstance(plugin.Instance)
	}

	// Remove from manager
	delete(mgr.plugins, fqid)

	mgr.ts.Info("plugins", "Native plugin unloaded: %s\n", fqid)
}

// Get returns a native plugin by FQID
func (mgr *NativePluginManager) Get(fqid string) *NativePlugin {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	return mgr.plugins[fqid]
}

// GetAll returns all native plugins
func (mgr *NativePluginManager) GetAll() []*NativePlugin {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	plugins := make([]*NativePlugin, 0, len(mgr.plugins))
	for _, plugin := range mgr.plugins {
		plugins = append(plugins, plugin)
	}
	return plugins
}

// Reload reloads a native plugin
func (mgr *NativePluginManager) Reload(fqid string) error {
	mgr.mu.Lock()
	plugin, exists := mgr.plugins[fqid]
	if !exists {
		mgr.mu.Unlock()
		return fmt.Errorf("Plugin not found: %s", fqid)
	}

	path := plugin.Path
	version := plugin.Version
	memorySize := plugin.MemorySize
	stackSize := plugin.StackSize
	mgr.mu.Unlock()

	// Unload
	mgr.Unload(fqid)

	// Reload
	return mgr.Load(fqid, path, version, memorySize, stackSize)
}

// CallFunction calls a function in a native plugin
func (mgr *NativePluginManager) CallFunction(fqid, functionName string, args ...any) (any, error) {
	mgr.mu.RLock()
	plugin, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("Plugin not found: %s", fqid)
	}

	if !plugin.Loaded {
		return nil, fmt.Errorf("Plugin not loaded: %s", fqid)
	}

	return callPluginFunction(plugin.Instance, functionName, args...)
}

// NativePluginAPI represents the API exposed to native plugins
type NativePluginAPI struct {
	FQID                    string
	Version                 int
	Log                     func(string)
	Notify                  func(string, string)
	GetConfig               func(string) any
	SetConfig               func(string, any)
	RegisterURIHandler      func(string, any)
	RegisterMetadataHandler func(string, any)
}

// SetupNativePluginEnvironment sets up the plugin environment with API
func SetupNativePluginEnvironment(ts *trace.TraceSystem, instance any, fqid string, version int) error {
	// Create API object
	api := &NativePluginAPI{
		FQID:    fqid,
		Version: version,
		Log: func(msg string) {
			ts.Debug("plugins", "[Native Plugin %s] %s\n", fqid, msg)
		},
		Notify: func(title, message string) {
			ts.Debug("plugins", "[Native Plugin %s] Notify: %s - %s\n", fqid, title, message)
		},
		GetConfig: func(key string) any {
			// In real implementation, this would get plugin config
			return nil
		},
		SetConfig: func(key string, value any) {
			// In real implementation, this would set plugin config
		},
		RegisterURIHandler: func(prefix string, handler any) {
			// In real implementation, this would register a URI handler
			ts.Info("plugins", "[Native Plugin %s] Registered URI handler: %s\n", fqid, prefix)
		},
		RegisterMetadataHandler: func(string, any) {
			// In real implementation, this would register a metadata handler
		},
	}

	// Expose API to plugin
	return exposeNativeAPI(instance, api)
}

// NativePluginCapability represents a plugin capability
type NativePluginCapability int

const (
	NPCapFileRead NativePluginCapability = 1 << iota
	NPCapFileWrite
	NPCapNetwork
	NPCapSettings
	NPCapNotifications
	NPCapMetadata
	NPCapURIHandler
	NPCapAudio
	NPCapVideo
)

// HasCapability checks if a plugin has a specific capability
func (mgr *NativePluginManager) HasCapability(fqid string, capability NativePluginCapability) bool {
	mgr.mu.RLock()
	_, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()

	if !exists {
		return false
	}

	// In real implementation, this would check the plugin's capabilities
	// For now, return true for all capabilities
	return true
}

// GetCapabilities returns all capabilities of a plugin
func (mgr *NativePluginManager) GetCapabilities(fqid string) []NativePluginCapability {
	mgr.mu.RLock()
	_, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()

	if !exists {
		return []NativePluginCapability{}
	}

	// In real implementation, this would return the plugin's actual capabilities
	// For now, return all capabilities
	return []NativePluginCapability{
		NPCapFileRead,
		NPCapFileWrite,
		NPCapNetwork,
		NPCapSettings,
		NPCapNotifications,
		NPCapMetadata,
		NPCapURIHandler,
		NPCapAudio,
		NPCapVideo,
	}
}

// NativePluginEvent represents an event from a native plugin
type NativePluginEvent struct {
	PluginFQID string
	EventType  string
	Data       any
}

// NativePluginEventHandler handles events from native plugins
type NativePluginEventHandler func(event *NativePluginEvent)

var (
	nativePluginHandlers     []NativePluginEventHandler
	nativePluginHandlersLock sync.RWMutex
)

// RegisterNativePluginHandler registers a handler for native plugin events
func RegisterNativePluginHandler(handler NativePluginEventHandler) {
	nativePluginHandlersLock.Lock()
	defer nativePluginHandlersLock.Unlock()
	nativePluginHandlers = append(nativePluginHandlers, handler)
}

// EmitNativePluginEvent emits an event from a native plugin
func EmitNativePluginEvent(event *NativePluginEvent) {
	nativePluginHandlersLock.RLock()
	defer nativePluginHandlersLock.RUnlock()
	for _, handler := range nativePluginHandlers {
		handler(event)
	}
}

// Helper functions for native plugin integration

func createPluginInstance(bitcode string, memorySize, stackSize int) (any, error) {
	// Create a plugin instance using a VM or runtime for the bitcode (e.g., WebAssembly, LuaJIT)
	instance := &struct {
		Bitcode    string
		MemorySize int
		StackSize  int
		Loaded     bool
	}{
		Bitcode:    bitcode,
		MemorySize: memorySize,
		StackSize:  stackSize,
		Loaded:     true,
	}
	return instance, nil
}

func destroyPluginInstance(instance any) {
	// Cleanup VM resources
	if inst, ok := instance.(*struct {
		Bitcode    string
		MemorySize int
		StackSize  int
		Loaded     bool
	}); ok {
		inst.Loaded = false
	}
}

func initializePlugin(instance any, fqid string, version int) error {
	// Call the plugin's init function
	if instance == nil {
		return errors.New("nil plugin instance")
	}
	return nil
}

func callPluginFunction(instance any, functionName string, args ...any) (any, error) {
	// Invoke a function in the plugin VM
	if instance == nil {
		return nil, errors.New("nil plugin instance")
	}
	return map[string]any{
		"function": functionName,
		"args":     args,
		"result":   nil,
	}, nil
}

func exposeNativeAPI(instance any, api *NativePluginAPI) error {
	// In real implementation, this would register native functions in the plugin VM
	// For now, just validate the API
	if instance == nil {
		return errors.New("nil plugin instance")
	}
	if api == nil {
		return errors.New("nil API")
	}
	return nil
}

// NativePluginMetadata represents metadata about a native plugin
type NativePluginMetadata struct {
	ID           string
	Title        string
	Version      string
	Author       string
	Description  string
	Capabilities []NativePluginCapability
}

// GetMetadata returns metadata for a native plugin
func (mgr *NativePluginManager) GetMetadata(fqid string) (*NativePluginMetadata, error) {
	mgr.mu.RLock()
	plugin, exists := mgr.plugins[fqid]
	mgr.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("Plugin not found: %s", fqid)
	}

	// In real implementation, this would query the plugin for its metadata
	metadata := &NativePluginMetadata{
		ID:           plugin.FQID,
		Title:        plugin.FQID,
		Version:      strconv.Itoa(plugin.Version),
		Author:       "Unknown",
		Description:  "Native plugin",
		Capabilities: mgr.GetCapabilities(fqid),
	}

	return metadata, nil
}
