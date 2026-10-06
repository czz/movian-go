// Package gaftape provides the Duktape C API surface used by Movian's
// ecmascript layer, implemented over goja (pure Go JS engine).
//
// C: ext/duktape/duktape.h + ext/duktape/duktape.c
//
// This package exists so that src/ecmascript/*.c can be translated
// function-by-function to Go without restructuring. Each duk_* call maps to
// a method or function here with the same name and semantics:
//
//	duk_push_string(ctx, s)        -> ctx.PushString(s)
//	duk_get_prop_string(ctx, i, n) -> ctx.GetPropString(i, n)
//	duk_pcall(ctx, n)              -> ctx.PCall(n)
//
// duk_context maps to *Context: an explicit value stack plus the call
// frame state Duktape threads carry. All Contexts of one heap share a
// single *goja.Runtime; mutual exclusion is provided by the owning
// es_context mutex (es_context_begin/end/suspend/resume), exactly like
// Duktape's single-threaded heap.
package gaftape

import (
	"sync"

	"github.com/dop251/goja"
)

const (
	GAF_EXEC_SUCCESS = 0
	GAF_EXEC_ERROR   = 1
)

// duk_errcode_t
const (
	GAF_ERR_NONE            = 0
	GAF_ERR_ERROR           = 1
	GAF_ERR_EVAL_ERROR      = 6
	GAF_ERR_RANGE_ERROR     = 2
	GAF_ERR_REFERENCE_ERROR = 3
	GAF_ERR_SYNTAX_ERROR    = 4
	GAF_ERR_TYPE_ERROR      = 5
	GAF_ERR_URI_ERROR       = 7
)

// duk_compile() flags
const (
	GAF_COMPILE_EVAL     = 1 << 3
	GAF_COMPILE_FUNCTION = 1 << 4
)

// duk_enum() flags
const (
	GAF_ENUM_INCLUDE_NONENUMERABLE = 1 << 0
	GAF_ENUM_INCLUDE_INTERNAL      = 1 << 2
	GAF_ENUM_OWN_PROPERTIES_ONLY   = 1 << 3
	GAF_ENUM_ARRAY_INDICES_ONLY    = 1 << 4
	GAF_ENUM_SORT_ARRAY_INDICES    = 1 << 5
)

// duk_push_buffer_object() flags
const (
	GAF_BUFOBJ_ARRAYBUFFER       = 0
	GAF_BUFOBJ_NODEJS_BUFFER     = 1
	GAF_BUFOBJ_DATAVIEW          = 2
	GAF_BUFOBJ_INT8ARRAY         = 3
	GAF_BUFOBJ_UINT8ARRAY        = 4
	GAF_BUFOBJ_UINT8CLAMPEDARRAY = 5
	GAF_BUFOBJ_INT16ARRAY        = 6
	GAF_BUFOBJ_UINT16ARRAY       = 7
	GAF_BUFOBJ_INT32ARRAY        = 8
	GAF_BUFOBJ_UINT32ARRAY       = 9
	GAF_BUFOBJ_FLOAT32ARRAY      = 10
	GAF_BUFOBJ_FLOAT64ARRAY      = 11
)

// duk_push_thread() flags
const (
	GAF_THREAD_NEW_GLOBAL_ENV = 1 << 0
)

// duk_ret_t
type Ret = int

// duk_c_function — signature of a native function.
type Function func(ctx *Context) Ret

// duk_function_list_entry
type FunctionListEntry struct {
	Key   string
	Value Function
	Nargs int
	Magic int
}

// duk_memory_functions
type MemoryFunctions struct {
	AllocFunc   func(udata any, size int) any
	ReallocFunc func(udata any, ptr any, size int) any
	FreeFunc    func(udata any, ptr any)
	Udata       any
}

// duk_thread_state — bookkeeping for suspend/resume. In this implementation
// suspending is a no-op: the native function keeps its goroutine alive while
// the owning context drops ec_mutex (es_context_suspend).
type ThreadState struct{}

// opaque value wrappers — gaf pointer / thread / enumerator values are not
// real JS objects but are carried on the stack as wrapped Go values.
type ptrVal struct{ p any }

type threadVal struct{ c *Context }

type enumVal struct {
	keys []string
	i    int
	obj  *goja.Object
}

type Heap struct {
	vm    *goja.Runtime
	stash *goja.Object
	mem   *MemoryFunctions

	magicMu    sync.Mutex
	magic      map[*goja.Object]*int
	cfuncs     map[*goja.Object]Function     // pushed c-function → Go fn
	finalizers map[*goja.Object]*goja.Object // object → finalizer fn object
	bufData    map[*goja.Object][]byte       // duk_config_buffer overrides

	activeCtx *Context // ctx currently executing JS inside the heap
}

type Context struct {
	h     *Heap
	stack []goja.Value
	this  goja.Value // duk_push_this() of the in-flight c-function call
	magic int        // duk_get_current_magic() of the in-flight c-function

	// backlink used by es_get(); set by the ecmascript package
	Ec any
}

// VM exposes the goja runtime (analog of dereferencing the gaf heap).
func (c *Context) VM() *goja.Runtime { return c.h.vm }

// Heap returns the owning heap.
func (c *Context) Heap() *Heap { return c.h }
