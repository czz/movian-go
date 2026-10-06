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
	"fmt"
	"slices"

	"github.com/dop251/goja"
)

// duk_get_top
func (c *Context) GetTop() int { return len(c.stack) }

// duk_set_top
func (c *Context) SetTop(top int) {
	if top < 0 {
		top = len(c.stack) + top
	}
	for len(c.stack) < top {
		c.stack = append(c.stack, goja.Undefined())
	}
	c.stack = c.stack[:top]
}

// duk_get_top_index — topmost valid index or -1.
func (c *Context) GetTopIndex() int { return len(c.stack) - 1 }

// duk_normalize_index
func (c *Context) NormalizeIndex(idx int) int {
	if idx < 0 {
		return len(c.stack) + idx
	}
	return idx
}

func (c *Context) at(idx int) goja.Value {
	i := c.NormalizeIndex(idx)
	if i < 0 || i >= len(c.stack) {
		return goja.Undefined()
	}
	return c.stack[i]
}

// duk_dup
func (c *Context) Dup(idx int) { c.push(c.at(idx)) }

// duk_swap_top
func (c *Context) SwapTop(idx int) {
	i := c.NormalizeIndex(idx)
	if i < 0 || i >= len(c.stack) || len(c.stack) == 0 {
		return
	}
	c.stack[i], c.stack[len(c.stack)-1] = c.stack[len(c.stack)-1], c.stack[i]
}

// duk_pop
func (c *Context) Pop() {
	if n := len(c.stack); n > 0 {
		c.stack = c.stack[:n-1]
	}
}

// duk_pop_2
func (c *Context) Pop2() { c.PopN(2) }

// duk_pop_n
func (c *Context) PopN(n int) {
	if n > len(c.stack) {
		n = len(c.stack)
	}
	c.stack = c.stack[:len(c.stack)-n]
}

func (c *Context) push(v goja.Value) {
	c.stack = append(c.stack, v)
}

func (c *Context) PushUndefined() { c.push(goja.Undefined()) }

func (c *Context) PushNull() { c.push(goja.Null()) }

func (c *Context) PushBoolean(b bool) { c.push(c.h.vm.ToValue(b)) }

func (c *Context) PushTrue() { c.PushBoolean(true) }

func (c *Context) PushFalse() { c.PushBoolean(false) }

func (c *Context) PushInt(v int) { c.push(c.h.vm.ToValue(v)) }

func (c *Context) PushUint(v uint32) { c.push(c.h.vm.ToValue(v)) }

func (c *Context) PushNumber(v float64) { c.push(c.h.vm.ToValue(v)) }

func (c *Context) PushString(s string) { c.push(c.h.vm.ToValue(s)) }

func (c *Context) PushLstring(s string, n int) {
	if n > len(s) {
		n = len(s)
	}
	c.PushString(s[:n])
}

// duk_push_sprintf
func (c *Context) PushSprintf(format string, args ...any) {
	c.PushString(fmt.Sprintf(format, args...))
}

// duk_push_object
func (c *Context) PushObject() int {
	c.push(c.h.vm.NewObject())
	return len(c.stack) - 1
}

// duk_push_array
func (c *Context) PushArray() int {
	c.push(c.h.vm.NewArray())
	return len(c.stack) - 1
}

// duk_push_this
func (c *Context) PushThis() {
	if c.this == nil {
		c.push(goja.Undefined())
		return
	}
	c.push(c.this)
}

// duk_push_global_object
func (c *Context) PushGlobalObject() { c.push(c.h.vm.GlobalObject()) }

// duk_push_global_stash — hidden per-heap object unreachable from JS.
func (c *Context) PushGlobalStash() { c.push(c.h.stash) }

// duk_push_pointer — opaque C pointer. Carried as a wrapped Go value;
// not an object from the gaf API perspective.
func (c *Context) PushPointer(p any) {
	if p == nil {
		c.push(goja.Null())
		return
	}
	c.push(c.h.vm.ToValue(ptrVal{p: p}))
}

// duk_push_buffer(ctx, size, dynamic) — returns the data slice.
func (c *Context) PushBuffer(size int, dynamic bool) []byte {
	b := make([]byte, size)
	c.push(c.h.vm.ToValue(c.h.vm.NewArrayBuffer(b)))
	return b
}

// duk_push_fixed_buffer — returns the data slice.
func (c *Context) PushFixedBuffer(size int) []byte {
	return c.PushBuffer(size, false)
}

// duk_push_external_buffer — buffer over an existing Go slice.
func (c *Context) PushExternalBuffer(b []byte) {
	c.push(c.h.vm.ToValue(c.h.vm.NewArrayBuffer(b)))
}

// duk_push_buffer_object(ctx, buf_idx, offset, length, flags)
func (c *Context) PushBufferObject(bufIdx, offset, length, flags int) {
	buf := c.at(bufIdx)
	switch flags {
	case GAF_BUFOBJ_ARRAYBUFFER:
		// Our buffers already are ArrayBuffers.
		c.push(buf)
	case GAF_BUFOBJ_UINT8ARRAY, GAF_BUFOBJ_UINT8CLAMPEDARRAY:
		v, err := c.h.vm.New(c.h.vm.Get("Uint8Array"), buf,
			c.h.vm.ToValue(offset), c.h.vm.ToValue(length))
		if err != nil {
			panic(err)
		}
		c.push(v)
	case GAF_BUFOBJ_DATAVIEW:
		v, err := c.h.vm.New(c.h.vm.Get("DataView"), buf,
			c.h.vm.ToValue(offset), c.h.vm.ToValue(length))
		if err != nil {
			panic(err)
		}
		c.push(v)
	default:
		v, err := c.h.vm.New(c.h.vm.Get("Uint8Array"), buf,
			c.h.vm.ToValue(offset), c.h.vm.ToValue(length))
		if err != nil {
			panic(err)
		}
		c.push(v)
	}
}

// bufferData resolves the []byte behind a buffer/buffer-object value.
func (c *Context) bufferData(v goja.Value) []byte {
	if v == nil {
		return nil
	}
	if obj, ok := v.(*goja.Object); ok {
		c.h.magicMu.Lock()
		b := c.h.bufData[obj]
		c.h.magicMu.Unlock()
		if b != nil {
			return b
		}
		if ab := obj.Export(); ab != nil {
			switch t := ab.(type) {
			case goja.ArrayBuffer:
				return t.Bytes()
			case []byte:
				return t
			}
		}
		// TypedArray / DataView / Node Buffer: read "buffer" prop
		if b := obj.Get("buffer"); b != nil {
			if bo, ok := b.(*goja.Object); ok {
				if ab, ok := bo.Export().(goja.ArrayBuffer); ok {
					return ab.Bytes()
				}
			}
		}
	}
	return nil
}

// duk_get_buffer_data(ctx, idx, &size) — returns slice (nil if not buffer).
func (c *Context) GetBufferData(idx int) []byte {
	return c.bufferData(c.at(idx))
}

// duk_get_buffer — same; in Duktape also accepts buffer objects.
func (c *Context) GetBuffer(idx int) []byte {
	return c.bufferData(c.at(idx))
}

// duk_require_buffer_data — throws TypeError if not a buffer.
func (c *Context) RequireBufferData(idx int) []byte {
	b := c.bufferData(c.at(idx))
	if b == nil {
		c.Error(GAF_ERR_TYPE_ERROR, "buffer required")
	}
	return b
}

// duk_require_buffer — pushes nothing, returns data; C returns void*.
func (c *Context) RequireBuffer(idx int) []byte {
	return c.RequireBufferData(idx)
}

// duk_to_buffer — coerce value at idx to a buffer in place.
func (c *Context) ToBuffer(idx int) []byte {
	v := c.at(idx)
	if b := c.bufferData(v); b != nil {
		return b
	}
	s := v.String()
	b := []byte(s)
	c.h.magicMu.Lock()
	if obj, ok := v.(*goja.Object); ok {
		c.h.bufData[obj] = b
		c.h.magicMu.Unlock()
		return b
	}
	c.h.magicMu.Unlock()
	i := c.NormalizeIndex(idx)
	if i >= 0 && i < len(c.stack) {
		c.stack[i] = c.h.vm.ToValue(c.h.vm.NewArrayBuffer(b))
	}
	return b
}

// duk_config_buffer — reconfigure buffer data pointer.
func (c *Context) ConfigBuffer(idx int, data []byte) {
	if obj, ok := c.at(idx).(*goja.Object); ok {
		c.h.magicMu.Lock()
		c.h.bufData[obj] = data
		c.h.magicMu.Unlock()
	}
}

func isOpaque(v goja.Value) bool {
	if v == nil {
		return false
	}
	switch v.Export().(type) {
	case ptrVal, *ptrVal, threadVal, *threadVal, enumVal, *enumVal:
		return true
	}
	return false
}

// duk_push_c_function(ctx, fn, nargs) — pushes a callable that runs fn
// on a fresh stack frame (args at indices 0..).
func (c *Context) PushCFunction(fn Function, nargs int) {
	cell := new(int)
	h := c.h
	gfn := func(call goja.FunctionCall) (ret goja.Value) {
		ctx := h.activeCtx
		if ctx == nil {
			ctx = &Context{h: h}
		}
		saved := ctx.stack
		savedThis, savedMagic := ctx.this, ctx.magic
		ctx.stack = slices.Clone(call.Arguments)
		ctx.this = call.This
		ctx.magic = *cell
		defer func() {
			ctx.stack = saved
			ctx.this, ctx.magic = savedThis, savedMagic
		}()
		r := fn(ctx)
		if r < 0 {
			// Duktape: negative return throws the matching error type
			// (DUK_RET_TYPE_ERROR etc).
			panic(h.vm.NewTypeError("invalid arguments"))
		}
		if r == 0 || len(ctx.stack) == 0 {
			return goja.Undefined()
		}
		return ctx.stack[len(ctx.stack)-1]
	}
	v := c.h.vm.ToValue(gfn)
	if obj, ok := v.(*goja.Object); ok {
		c.h.magicMu.Lock()
		c.h.magic[obj] = cell
		c.h.cfuncs[obj] = fn
		c.h.magicMu.Unlock()
	}
	c.push(v)
}

// duk_push_c_lightfunc(ctx, fn, nargs, length, magic)
func (c *Context) PushCLightFunc(fn Function, nargs, length, magic int) {
	c.PushCFunction(fn, nargs)
	obj, _ := c.at(-1).(*goja.Object)
	if obj != nil {
		c.h.magicMu.Lock()
		if cell := c.h.magic[obj]; cell != nil {
			*cell = magic
		}
		c.h.magicMu.Unlock()
	}
}

// duk_push_thread(ctx) — returns stack index of the new thread value.
func (c *Context) PushThread(flags ...int) int {
	t := &Context{h: c.h, Ec: c.Ec}
	c.push(c.h.vm.ToValue(&threadVal{c: t}))
	return len(c.stack) - 1
}

// Top returns the top value (internal helper).
func (c *Context) Top() goja.Value { return c.at(-1) }
