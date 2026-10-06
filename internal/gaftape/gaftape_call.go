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
	"runtime"

	"github.com/dop251/goja"
)

// panicToValue converts a recovered panic into a pushable error value.
func (c *Context) panicToValue(e any) goja.Value {
	switch t := e.(type) {
	case *goja.Exception:
		return t.Value()
	case *goja.CompilerSyntaxError, *goja.CompilerReferenceError:
		return c.h.vm.ToValue(fmt.Sprint(e))
	case goja.Value:
		return t
	case error:
		obj, err := c.h.vm.New(c.h.vm.Get("Error"), c.h.vm.ToValue(t.Error()))
		if err == nil {
			return obj
		}
		return c.h.vm.ToValue(t.Error())
	default:
		return c.h.vm.ToValue(fmt.Sprintf("%v", e))
	}
}

// call invokes a JS callable, converting panics to errors.
func (c *Context) call(fn, this goja.Value, args []goja.Value) (res goja.Value, err error) {
	callable, ok := goja.AssertFunction(fn)
	if !ok {
		return nil, fmt.Errorf("not callable")
	}
	prev := c.h.activeCtx
	c.h.activeCtx = c
	defer func() {
		c.h.activeCtx = prev
		if e := recover(); e != nil {
			err = fmt.Errorf("%v", e)
			res = c.panicToValue(e)
		}
	}()
	res, err = callable(this, args...)
	if err != nil && res == nil {
		res = c.panicToValue(err)
	}
	return res, err
}

// duk_pcall(ctx, nargs) — [.. fn arg1..argN] → [.. result] | [.. err]
func (c *Context) PCall(nargs int) int {
	top := len(c.stack)
	if nargs+1 > top {
		c.PushString("invalid stack for pcall")
		return GAF_EXEC_ERROR
	}
	args := append([]goja.Value(nil), c.stack[top-nargs:]...)
	fn := c.stack[top-nargs-1]
	c.stack = c.stack[:top-nargs-1]
	res, err := c.call(fn, goja.Undefined(), args)
	if err != nil {
		c.push(res)
		return GAF_EXEC_ERROR
	}
	c.push(res)
	return GAF_EXEC_SUCCESS
}

// duk_pcall_method(ctx, nargs) — [.. obj fn arg1..argN] → [.. result]
func (c *Context) PCallMethod(nargs int) int {
	top := len(c.stack)
	if nargs+2 > top {
		c.PushString("invalid stack for pcall_method")
		return GAF_EXEC_ERROR
	}
	args := append([]goja.Value(nil), c.stack[top-nargs:]...)
	fn := c.stack[top-nargs-1]
	this := c.stack[top-nargs-2]
	c.stack = c.stack[:top-nargs-2]
	res, err := c.call(fn, this, args)
	if err != nil {
		c.push(res)
		return GAF_EXEC_ERROR
	}
	c.push(res)
	return GAF_EXEC_SUCCESS
}

// duk_error — throw a JS Error of the class matching code.
func (c *Context) Error(code int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	name := "Error"
	switch code {
	case GAF_ERR_TYPE_ERROR:
		name = "TypeError"
	case GAF_ERR_SYNTAX_ERROR:
		name = "SyntaxError"
	case GAF_ERR_RANGE_ERROR:
		name = "RangeError"
	case GAF_ERR_REFERENCE_ERROR:
		name = "ReferenceError"
	case GAF_ERR_EVAL_ERROR:
		name = "EvalError"
	case GAF_ERR_URI_ERROR:
		name = "URIError"
	}
	obj, err := c.h.vm.New(c.h.vm.Get(name), c.h.vm.ToValue(msg))
	if err != nil {
		panic(c.h.vm.ToValue(msg))
	}
	panic(obj)
}

// duk_put_function_list(ctx, obj_idx, list)
func (c *Context) PutFunctionList(idx int, list []FunctionListEntry) {
	i := c.NormalizeIndex(idx)
	for _, e := range list {
		c.PushCFunction(e.Value, e.Nargs)
		obj, _ := c.at(-1).(*goja.Object)
		if obj != nil {
			c.h.magicMu.Lock()
			if cell := c.h.magic[obj]; cell != nil {
				*cell = e.Magic
			}
			c.h.magicMu.Unlock()
		}
		c.PutPropString(i, e.Key)
	}
}

// duk_set_magic(ctx, fn_idx, magic)
func (c *Context) SetMagic(idx, magic int) {
	if obj, ok := c.unwrapProxy(c.at(idx)).(*goja.Object); ok {
		c.h.magicMu.Lock()
		if cell := c.h.magic[obj]; cell != nil {
			*cell = magic
		} else {
			cell = new(int)
			*cell = magic
			c.h.magic[obj] = cell
		}
		c.h.magicMu.Unlock()
	}
}

// duk_set_finalizer(ctx, obj_idx) — pops function on top, sets as finalizer.
func (c *Context) SetFinalizer(idx int) {
	fn := c.at(-1)
	c.Pop()
	obj, ok := c.unwrapProxy(c.at(idx)).(*goja.Object)
	fobj, _ := fn.(*goja.Object)
	if !ok || fobj == nil {
		return
	}
	c.h.magicMu.Lock()
	c.h.finalizers[obj] = fobj
	gfn := c.h.cfuncs[fobj]
	mcell := c.h.magic[fobj]
	c.h.magicMu.Unlock()
	h := c.h
	runtime.SetFinalizer(obj, func(o *goja.Object) {
		h.magicMu.Lock()
		delete(h.finalizers, o)
		h.magicMu.Unlock()
		if gfn == nil {
			return
		}
		sc := &Context{h: h, stack: []goja.Value{o}}
		if mcell != nil {
			sc.magic = *mcell
		}
		saved := sc.this
		sc.this = o
		gfn(sc)
		sc.this = saved
	})
}

// duk_pcompile(ctx, flags) — compiles string at top → function | error.
// duk_pcompile — compiles the string at index -2 using the filename at
// index -1; both are popped and replaced by the compiled function (or
// an error value on failure).
func (c *Context) PCompile(flags int) (ret int) {
	name := c.SafeToString(-1)
	src := c.SafeToString(-2)
	c.Pop()
	c.Pop()
	prog, err := goja.Compile(name, src, false)
	if err != nil {
		c.push(c.panicToValue(err))
		return GAF_EXEC_ERROR
	}
	vm := c.h.vm
	v := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		res, err := vm.RunProgram(prog)
		if err != nil {
			panic(err)
		}
		return res
	})
	c.push(v)
	return GAF_EXEC_SUCCESS
}

// duk_compile — throws on error.
func (c *Context) Compile(flags int) {
	if c.PCompile(flags) != GAF_EXEC_SUCCESS {
		panic(c.stack[len(c.stack)-1])
	}
}
