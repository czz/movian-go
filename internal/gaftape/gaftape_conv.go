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
	"strconv"

	"github.com/dop251/goja"
)

func (c *Context) IsUndefined(idx int) bool {
	return goja.IsUndefined(c.at(idx))
}

func (c *Context) IsNull(idx int) bool {
	return goja.IsNull(c.at(idx))
}

func (c *Context) IsNullOrUndefined(idx int) bool {
	v := c.at(idx)
	return goja.IsNull(v) || goja.IsUndefined(v)
}

func (c *Context) IsBoolean(idx int) bool {
	_, ok := c.at(idx).Export().(bool)
	return ok
}

func (c *Context) IsNumber(idx int) bool {
	switch c.at(idx).Export().(type) {
	case int64, float64, int, int32, uint32, uint64:
		return true
	}
	return false
}

func (c *Context) IsString(idx int) bool {
	// Check the JS type, not Export(): a *goja.Symbol exports its
	// description as a Go string, but duk_is_string(symbol) is false
	// and duk_require_string(symbol) throws TypeError.
	return goja.IsString(c.at(idx))
}

// duk_is_symbol (duktape 2.x predicate; vendored duktape 1.5 has no JS
// Symbol type, this exists only so callers can detect goja-only values
// that can never reach the C code paths).
func (c *Context) IsSymbol(idx int) bool {
	_, ok := c.at(idx).(*goja.Symbol)
	return ok
}

func (c *Context) IsObject(idx int) bool {
	v := c.at(idx)
	_, ok := v.(*goja.Object)
	return ok && !isOpaque(v)
}

func (c *Context) IsObjectCoercible(idx int) bool {
	return !c.IsNullOrUndefined(idx)
}

func (c *Context) IsFunction(idx int) bool {
	_, ok := goja.AssertFunction(c.at(idx))
	return ok
}

func (c *Context) IsBuffer(idx int) bool {
	return c.bufferData(c.at(idx)) != nil
}

func (c *Context) IsPointer(idx int) bool {
	v := c.at(idx)
	if v == nil {
		return false
	}
	switch v.Export().(type) {
	case ptrVal, *ptrVal:
		return true
	}
	return false
}

func (c *Context) GetBoolean(idx int) bool {
	return c.at(idx).ToBoolean()
}

func (c *Context) GetInt(idx int) int {
	return int(c.at(idx).ToInteger())
}

func (c *Context) GetUint(idx int) uint32 {
	v := c.at(idx).ToInteger()
	return uint32(uint32(v) & 0xffffffff)
}

func (c *Context) GetNumber(idx int) float64 {
	return c.at(idx).ToFloat()
}

// duk_get_string — NULL for non-string.
func (c *Context) GetString(idx int) string {
	if !c.IsString(idx) {
		return ""
	}
	return c.at(idx).String()
}

// duk_get_lstring — binary safe string.
func (c *Context) GetLstring(idx int) string {
	return c.GetString(idx)
}

// duk_get_pointer
func (c *Context) GetPointer(idx int) any {
	v := c.at(idx)
	if v == nil {
		return nil
	}
	switch p := v.Export().(type) {
	case ptrVal:
		return p.p
	case *ptrVal:
		return p.p
	}
	return nil
}

// duk_to_boolean — coerce in place, return result.
func (c *Context) ToBoolean(idx int) bool {
	b := c.at(idx).ToBoolean()
	i := c.NormalizeIndex(idx)
	if i >= 0 && i < len(c.stack) {
		c.stack[i] = c.h.vm.ToValue(b)
	}
	return b
}

func (c *Context) ToInt(idx int) int {
	n := int(c.at(idx).ToInteger())
	i := c.NormalizeIndex(idx)
	if i >= 0 && i < len(c.stack) {
		c.stack[i] = c.h.vm.ToValue(n)
	}
	return n
}

func (c *Context) ToInt32(idx int) int32 { return int32(c.ToInt(idx)) }

func (c *Context) ToUint(idx int) uint32 {
	n := c.at(idx).ToInteger()
	i := c.NormalizeIndex(idx)
	if i >= 0 && i < len(c.stack) {
		c.stack[i] = c.h.vm.ToValue(uint32(uint32(n)))
	}
	return uint32(uint32(n))
}

func (c *Context) ToNumber(idx int) float64 {
	f := c.at(idx).ToFloat()
	i := c.NormalizeIndex(idx)
	if i >= 0 && i < len(c.stack) {
		c.stack[i] = c.h.vm.ToValue(f)
	}
	return f
}

// duk_to_string — coerce in place; returns "" for undefined.
func (c *Context) ToString(idx int) string {
	v := c.at(idx)
	if v == nil || goja.IsUndefined(v) {
		return ""
	}
	// duk_to_string follows the ToString abstract op: a Symbol throws
	// TypeError. goja's (*Symbol).String() silently returns the desc.
	if _, ok := v.(*goja.Symbol); ok {
		c.requireTypeErr("string")
	}
	s := v.String()
	i := c.NormalizeIndex(idx)
	if i >= 0 && i < len(c.stack) {
		c.stack[i] = c.h.vm.ToValue(s)
	}
	return s
}

func (c *Context) ToLstring(idx int) string { return c.ToString(idx) }

// duk_safe_to_string — never throws; returns representation string.
func (c *Context) SafeToString(idx int) (s string) {
	defer func() {
		if r := recover(); r != nil {
			s = fmt.Sprintf("%v", c.at(idx))
		}
	}()
	return c.ToString(idx)
}

func (c *Context) requireTypeErr(what string) {
	c.Error(GAF_ERR_TYPE_ERROR, "%s required", what)
}

func (c *Context) RequireString(idx int) string {
	if !c.IsString(idx) {
		c.requireTypeErr("string")
	}
	return c.GetString(idx)
}

func (c *Context) RequireBoolean(idx int) bool {
	if !c.IsBoolean(idx) {
		c.requireTypeErr("boolean")
	}
	return c.GetBoolean(idx)
}

func (c *Context) RequireInt(idx int) int {
	if !c.IsNumber(idx) {
		c.requireTypeErr("number")
	}
	return c.ToInt(idx)
}

func (c *Context) RequireNumber(idx int) float64 {
	if !c.IsNumber(idx) {
		c.requireTypeErr("number")
	}
	return c.ToNumber(idx)
}

func (c *Context) RequirePointer(idx int) any {
	if !c.IsPointer(idx) {
		c.requireTypeErr("pointer")
	}
	return c.GetPointer(idx)
}

func (c *Context) toObject(v goja.Value) *goja.Object {
	if v == nil {
		return nil
	}
	if obj, ok := v.(*goja.Object); ok {
		return obj
	}
	return v.ToObject(c.h.vm)
}

// unwrapProxy resolves an ECMAScript Proxy to its target — Duktape internal
// slots (finalizers, \xff hidden props, magic) always address the target.
func (c *Context) unwrapProxy(v goja.Value) goja.Value {
	for v != nil {
		px, ok := v.Export().(goja.Proxy)
		if !ok {
			return v
		}
		v = px.Target()
	}
	return nil
}

// duk_get_prop_string(ctx, obj_idx, key) — pushes value, returns 1/0.
func (c *Context) GetPropString(idx int, key string) bool {
	obj := c.toObject(c.at(idx))
	if len(key) > 0 && key[0] == '\xff' {
		obj = c.toObject(c.unwrapProxy(c.at(idx)))
	}
	if obj == nil {
		c.PushUndefined()
		return false
	}
	v := obj.Get(key)
	if v == nil || goja.IsUndefined(v) {
		c.PushUndefined()
		return false
	}
	c.push(v)
	return true
}

// duk_put_prop_string(ctx, obj_idx, key) — value on top.
func (c *Context) PutPropString(idx int, key string) {
	obj := c.toObject(c.at(idx))
	if len(key) > 0 && key[0] == '\xff' {
		obj = c.toObject(c.unwrapProxy(c.at(idx)))
	}
	v := c.at(-1)
	if obj != nil {
		obj.Set(key, v)
	}
	c.Pop()
}

// duk_put_prop_index(ctx, obj_idx, arr_index)
func (c *Context) PutPropIndex(idx int, arrIdx int) {
	obj := c.toObject(c.at(idx))
	v := c.at(-1)
	if obj != nil {
		obj.Set(strconv.Itoa(arrIdx), v)
	}
	c.Pop()
}

// duk_del_prop_string
func (c *Context) DelPropString(idx int, key string) bool {
	obj := c.toObject(c.at(idx))
	if len(key) > 0 && key[0] == '\xff' {
		obj = c.toObject(c.unwrapProxy(c.at(idx)))
	}
	if obj == nil {
		return false
	}
	return obj.Delete(key) == nil
}

// duk_safe_call(ctx, fn, nargs, nrets) — Duktape 1.x signature.
// fn runs on a sub-frame containing the nargs; on success the top nrets
// values remain on the caller stack; on error the stack is restored and
// the error value is pushed.
func (c *Context) SafeCall(fn func(*Context) Ret, nargs, nrets int) int {
	top := len(c.stack)
	if nargs > top {
		c.PushString("invalid stack for safe_call")
		return GAF_EXEC_ERROR
	}
	args := append([]goja.Value(nil), c.stack[top-nargs:]...)
	saved := c.stack[:top-nargs]
	savedThis, savedMagic := c.this, c.magic
	c.stack = args
	var r Ret
	var errv goja.Value
	func() {
		defer func() {
			if e := recover(); e != nil {
				errv = c.panicToValue(e)
			}
		}()
		r = fn(c)
	}()
	c.this, c.magic = savedThis, savedMagic
	frame := c.stack
	if errv != nil {
		c.stack = saved
		c.push(errv)
		return GAF_EXEC_ERROR
	}
	_ = r
	res := slices.Clone(frame)
	if len(res) > nrets {
		res = res[len(res)-nrets:]
	}
	c.stack = saved
	c.stack = append(c.stack, res...)
	return GAF_EXEC_SUCCESS
}

// duk_get_magic(ctx, fn_idx)
func (c *Context) GetMagic(idx int) int {
	if obj, ok := c.unwrapProxy(c.at(idx)).(*goja.Object); ok {
		c.h.magicMu.Lock()
		defer c.h.magicMu.Unlock()
		if cell := c.h.magic[obj]; cell != nil {
			return *cell
		}
	}
	return 0
}

// duk_get_current_magic — magic of the in-flight c-function.
func (c *Context) GetCurrentMagic() int { return c.magic }

// duk_get_finalizer(ctx, obj_idx) — pushes the finalizer fn, returns 1/0.
func (c *Context) GetFinalizer(idx int) bool {
	obj, ok := c.unwrapProxy(c.at(idx)).(*goja.Object)
	if !ok {
		c.PushUndefined()
		return false
	}
	c.h.magicMu.Lock()
	f, has := c.h.finalizers[obj]
	c.h.magicMu.Unlock()
	if !has {
		c.PushUndefined()
		return false
	}
	c.push(f)
	return true
}

// duk_get_context(ctx, idx) — the *Context behind a thread value.
func (c *Context) GetContext(idx int) *Context {
	v := c.at(idx)
	if v == nil {
		return nil
	}
	switch t := v.Export().(type) {
	case *threadVal:
		return t.c
	case threadVal:
		return t.c
	}
	return nil
}

// duk_get_memory_functions
func (c *Context) GetMemoryFunctions(mfs *MemoryFunctions) {
	if c.h.mem != nil {
		*mfs = *c.h.mem
	} else {
		*mfs = MemoryFunctions{
			AllocFunc: func(u any, size int) any {
				return make([]byte, size)
			},
			FreeFunc: func(u any, p any) {},
			Udata:    nil,
		}
	}
}
