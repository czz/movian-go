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
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"

	"github.com/dop251/goja"
)

// duk_create_heap(alloc, realloc, free, udata, fatal)
func CreateHeap(alloc func(any, int) any,
	realloc func(any, any, int) any,
	freeFn func(any, any),
	udata any, fatal func(*Context, string)) *Heap {
	h := &Heap{
		vm:         goja.New(),
		magic:      make(map[*goja.Object]*int),
		cfuncs:     make(map[*goja.Object]Function),
		finalizers: make(map[*goja.Object]*goja.Object),
		bufData:    make(map[*goja.Object][]byte),
	}
	h.stash = h.vm.NewObject()
	if alloc != nil {
		h.mem = &MemoryFunctions{
			AllocFunc:   alloc,
			ReallocFunc: realloc,
			FreeFunc:    freeFn,
			Udata:       udata,
		}
	}
	return h
}

// duk_destroy_heap — C runs pending finalizers during heap teardown.
func (h *Heap) Destroy() {
	if h.vm != nil {
		h.magicMu.Lock()
		fins := h.finalizers
		h.finalizers = make(map[*goja.Object]*goja.Object)
		h.magicMu.Unlock()
		for obj, fobj := range fins {
			// Only pure-JS finalizers (Duktape.fin) are invoked here;
			// cfunc finalizers already run via runtime.SetFinalizer.
			if h.cfuncs[fobj] != nil {
				continue
			}
			if fn, ok := goja.AssertFunction(fobj); ok {
				func() {
					defer func() { recover() }()
					fn(goja.Undefined(), obj)
				}()
			}
		}
	}
	h.magic = nil
	h.cfuncs = nil
	h.finalizers = nil
	h.bufData = nil
	h.vm = nil
	h.stash = nil
}

// NewMainContext creates the main Context for a heap — the equivalent of
// the duk_context returned alongside duk_create_heap.
func NewMainContext(h *Heap) *Context {
	c := &Context{h: h}
	// C: Duktape provides a built-in `Duktape` global object that the es
	// glue attaches modSearch to (ecmascript.c:502). goja has none —
	// provide an empty object + modLoaded cache.
	var gaftapeObj *goja.Object
	if v := h.vm.Get("Duktape"); v == nil || goja.IsUndefined(v) {
		gaftapeObj = h.vm.NewObject()
		h.vm.Set("Duktape", gaftapeObj)
	} else {
		gaftapeObj = v.ToObject(h.vm)
	}
	// "Duktape" stays for backwards compatibility with scripts; the same
	// object is also exposed under the "Gaftape" name.
	if v := h.vm.Get("Gaftape"); v == nil || goja.IsUndefined(v) {
		h.vm.Set("Gaftape", gaftapeObj)
	}
	if v := gaftapeObj.Get("modLoaded"); v == nil || goja.IsUndefined(v) {
		gaftapeObj.Set("modLoaded", h.vm.NewObject())
	}
	// C: Duktape.version — bundled duktape is 1.x (e.g. 1.5.99 → 10599).
	// goja has no TextDecoder, so reporting <19999 keeps res/ecmascript
	// code on the bytes.toString() path (modules/movian/http.js:63).
	if v := gaftapeObj.Get("version"); v == nil || goja.IsUndefined(v) {
		gaftapeObj.Set("version", 10599)
	}
	// C: Duktape.enc('hex'|'base64', data) — duktape.c encoder builtin.
	if v := gaftapeObj.Get("enc"); v == nil || goja.IsUndefined(v) {
		vm := h.vm
		gaftapeObj.Set("enc", func(call goja.FunctionCall) goja.Value {
			how := call.Argument(0).String()
			var data []byte
			switch e := call.Argument(1).Export().(type) {
			case []byte:
				data = e
			case string:
				data = []byte(e)
			case goja.ArrayBuffer:
				data = e.Bytes()
			default:
				data = []byte(call.Argument(1).String())
			}
			switch how {
			case "hex":
				return vm.ToValue(hex.EncodeToString(data))
			case "base64":
				return vm.ToValue(base64.StdEncoding.EncodeToString(data))
			}
			panic(vm.NewTypeError("Duktape.enc: unsupported encoding " + how))
		})
	}
	// C: Duktape.dec('hex'|'base64', str) — duk_bi_duktape_object_dec
	// (duktape.c): duk_hex_decode / duk_base64_decode push a plain buffer.
	// The decoder skips ASCII whitespace, treats '=' as padding and throws
	// TypeError ("decode failed") on any other invalid char.
	if v := gaftapeObj.Get("dec"); v == nil || goja.IsUndefined(v) {
		vm := h.vm
		gaftapeObj.Set("dec", func(call goja.FunctionCall) goja.Value {
			how := call.Argument(0).String()
			s := call.Argument(1).String()
			var data []byte
			var err error
			switch how {
			case "hex":
				data, err = hex.DecodeString(s)
			case "base64":
				clean := strings.Map(func(r rune) rune {
					switch r {
					case ' ', '\t', '\n', '\v', '\f', '\r':
						return -1
					}
					return r
				}, s)
				if data, err = base64.StdEncoding.DecodeString(clean); err != nil {
					data, err = base64.RawStdEncoding.DecodeString(clean)
				}
			default:
				panic(vm.NewTypeError("Duktape.dec: unsupported encoding " + how))
			}
			if err != nil {
				panic(vm.NewTypeError("decode failed"))
			}
			return vm.ToValue(vm.NewArrayBuffer(data))
		})
	}
	// C: Duktape.fin(obj, fn) — registers a JS finalizer. Duktape runs
	// finalizers during GC and heap destruction; here JS finalizers are
	// stored and invoked by Heap.Destroy (heap teardown), which is the
	// only deterministic point where the VM can still execute them.
	if v := gaftapeObj.Get("fin"); v == nil || goja.IsUndefined(v) {
		vm := h.vm
		gaftapeObj.Set("fin", func(call goja.FunctionCall) goja.Value {
			obj, _ := call.Argument(0).(*goja.Object)
			fobj, _ := call.Argument(1).(*goja.Object)
			if obj == nil || fobj == nil {
				return goja.Undefined()
			}
			h.magicMu.Lock()
			h.finalizers[obj] = fobj
			h.magicMu.Unlock()
			_ = vm
			return goja.Undefined()
		})
	}
	// C: duk_to_string/duk_safe_to_string on a plain buffer coerce to the
	// raw byte content. Our gaf buffers map to goja ArrayBuffer, whose
	// default toString yields "[object ArrayBuffer]" — patch the
	// prototype so String(buf), buf.toString() and concatenation produce
	// the byte content (e.g. String(Duktape.dec('base64', x)) or
	// modules/movian/http.js bytes.toString()).
	if abv := h.vm.Get("ArrayBuffer"); abv != nil && !goja.IsUndefined(abv) {
		if pv := abv.ToObject(h.vm).Get("prototype"); pv != nil && !goja.IsUndefined(pv) {
			pv.ToObject(h.vm).Set("toString", func(call goja.FunctionCall) goja.Value {
				if ab, ok := call.This.Export().(goja.ArrayBuffer); ok {
					return h.vm.ToValue(string(ab.Bytes()))
				}
				return h.vm.ToValue("[object ArrayBuffer]")
			})
		}
	}
	// C: print()/alert() global builtins — duk_bi_global_object_print_helper
	// (duktape.c, enabled by DUK_USE_BROWSER_LIKE + DUK_USE_FILE_IO, both
	// on by default in the vendored duk_config.h). ToString-coerces the
	// arguments, joins them with a single space, appends a newline and
	// writes to stdout; alert() does the same to stderr. A single buffer
	// argument is written raw with no newline.
	if v := h.vm.Get("print"); v == nil || goja.IsUndefined(v) {
		printAlert := func(stderr bool) func(goja.FunctionCall) goja.Value {
			out := os.Stdout
			if stderr {
				out = os.Stderr
			}
			return func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 {
					out.WriteString("\n")
					return goja.Undefined()
				}
				if len(call.Arguments) == 1 {
					if ab, ok := call.Argument(0).Export().(goja.ArrayBuffer); ok {
						out.Write(ab.Bytes())
						return goja.Undefined()
					}
				}
				parts := make([]string, len(call.Arguments))
				for i, a := range call.Arguments {
					parts[i] = a.String()
				}
				out.WriteString(strings.Join(parts, " ") + "\n")
				return goja.Undefined()
			}
		}
		h.vm.Set("print", printAlert(false))
		h.vm.Set("alert", printAlert(true))
	}
	// C: require() global — duk_bi_global_object_require (duktape.c)
	h.vm.Set("require", h.requireFunc())
	return c
}

// duk__bi_global_resolve_module_id — bundled duktape.c module-ID resolver.
// Resolves reqID against modID (current module path) using the CommonJS
// term algorithm. Returns (resolvedID, lastComponent). Panics with a
// goja error on resolve failure (C: resolve_error → duk_throw).
func (h *Heap) resolveModuleID(reqID, modID string) (string, string) {
	var buf []byte
	if modID != "" && len(reqID) > 0 && reqID[0] == '.' {
		buf = []byte(modID + "/../" + reqID)
	} else {
		buf = []byte(reqID)
	}
	buf = append(buf, 0) // NUL terminator, matching C's string walk

	out := make([]byte, 0, len(buf))
	p, qLast := 0, 0

	eatDupSlashes := func() {
		for buf[p] == '/' {
			p++
		}
	}

loop:
	for {
		// p at start of a term; qLast reset each iteration
		qLast = len(out)
		c := buf[p]
		p++
		switch {
		case c == 0:
			panic(h.vm.NewTypeError("resolve error: requested ID must end with a non-empty term"))
		case c == '.':
			c = buf[p]
			p++
			if c == '/' {
				eatDupSlashes()
				continue
			}
			if c == '.' && buf[p] == '/' {
				p++ // eat first input slash
				if len(out) == 0 {
					panic(h.vm.NewTypeError("resolve error: term was '..' but nothing to backtrack"))
				}
				// backtrack to last output slash, then to previous component
				out = out[:len(out)-1] // drop trailing '/'
				for len(out) > 0 && out[len(out)-1] != '/' {
					out = out[:len(out)-1]
				}
				eatDupSlashes()
				continue
			}
			panic(h.vm.NewTypeError("resolve error: term begins with '.' but is not '.' or '..'"))
		case c == '/':
			panic(h.vm.NewTypeError("resolve error: empty term"))
		default:
			for {
				out = append(out, c)
				c = buf[p]
				p++
				if c == 0 {
					break loop
				}
				if c == '/' {
					out = append(out, '/')
					eatDupSlashes()
					continue loop
				}
			}
		}
	}
	resolved := string(out)
	return resolved, resolved[qLast:]
}

// requireFunc — C: duk_bi_global_object_require (bundled duktape.c).
// CommonJS require: resolve id, modLoaded cache, fresh require with .id,
// exports/module tables, Duktape.modSearch call, wrapped-source eval.
// modID carries the calling module's resolved id — C reads it off the
// calling require function's .id; in Go each module's require is a
// closure bound to its own resolved id (fresh_require.id).
func (h *Heap) requireFunc() func(goja.FunctionCall) goja.Value {
	return h.requireFuncWithBase("")
}

func (h *Heap) requireFuncWithBase(modID string) func(goja.FunctionCall) goja.Value {
	vm := h.vm
	return func(fc goja.FunctionCall) goja.Value {
		reqID := fc.Argument(0).String()
		resolvedID, lastComp := h.resolveModuleID(reqID, modID)

		gafObj := vm.Get("Duktape").ToObject(vm)
		modLoaded := gafObj.Get("modLoaded").ToObject(vm)

		// Cached module check
		if m := modLoaded.Get(resolvedID); m != nil && !goja.IsUndefined(m) {
			return m.ToObject(vm).Get("exports")
		}

		// Fresh require with .id = resolved target module id
		// (C: fresh_require with require.id = resolved_id — the resolution
		// base for the module's own relative requires)
		freshRequire := vm.ToValue(h.requireFuncWithBase(resolvedID)).ToObject(vm)
		freshRequire.Set("name", "require")
		freshRequire.Set("id", resolvedID)

		exports := vm.NewObject()
		module := vm.NewObject()
		module.Set("exports", exports)
		module.Set("id", resolvedID)

		// Register early for circular references
		modLoaded.Set(resolvedID, module)

		// Duktape.modSearch(resolved_id, fresh_require, exports, module)
		modSearch, ok := goja.AssertFunction(gafObj.Get("modSearch"))
		if !ok {
			modLoaded.Delete(resolvedID)
			panic(vm.NewTypeError("no modSearch"))
		}
		res, err := modSearch(goja.Undefined(),
			vm.ToValue(resolvedID), freshRequire, exports, module)
		if err != nil {
			modLoaded.Delete(resolvedID)
			panic(err)
		}

		// Non-string return → pure native module (exports already set)
		if _, isStr := res.(goja.String); !isStr {
			return module.Get("exports")
		}

		// Wrap source: (function(require,exports,module){src})
		src := "(function(require,exports,module){" + res.String() + "})"
		filename := resolvedID
		if fn := module.Get("filename"); fn != nil && !goja.IsUndefined(fn) {
			filename = fn.String()
		}
		// C: duk_eval_raw(NULL, 0, DUK_COMPILE_EVAL) with .fileName
		compiled, err := goja.Compile(filename, src, false)
		if err != nil {
			modLoaded.Delete(resolvedID)
			panic(err)
		}
		modName := lastComp
		if mn := module.Get("name"); mn != nil && !goja.IsUndefined(mn) {
			modName = mn.String()
		}
		v, err := vm.RunProgram(compiled)
		if err != nil {
			modLoaded.Delete(resolvedID)
			panic(err)
		}
		fn, ok := goja.AssertFunction(v)
		if !ok {
			modLoaded.Delete(resolvedID)
			panic(vm.NewTypeError("module source did not evaluate to a function"))
		}
		if fo, ok := v.(*goja.Object); ok {
			fo.Set("name", modName)
		}

		// Call wrapped module fn: this=exports, args=(freshRequire,
		// module.exports relookup, module)
		_, err = fn(exports, freshRequire,
			module.ToObject(vm).Get("exports"), module)
		if err != nil {
			modLoaded.Delete(resolvedID)
			panic(err)
		}

		return module.Get("exports")
	}
}
