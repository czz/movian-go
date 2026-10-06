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
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

// ---------------------------------------------------------------------------
// Constants — Duktape public API defines used by the es code.
// ---------------------------------------------------------------------------

// duk_enum(ctx, obj_idx, flags) — pushes enumerator.
func (c *Context) Enum(idx int, flags int) {
	obj := c.toObject(c.at(idx))
	var keys []string
	if obj != nil {
		keys = obj.Keys()
	}
	c.push(c.h.vm.ToValue(&enumVal{keys: keys, obj: obj}))
}

// duk_next(ctx, enum_idx, get_value) — pushes key (+value); returns 1/0.
func (c *Context) Next(enumIdx int, getValue bool) bool {
	v := c.at(enumIdx)
	ev, ok := v.Export().(*enumVal)
	if !ok || ev == nil || ev.i >= len(ev.keys) {
		return false
	}
	k := ev.keys[ev.i]
	ev.i++
	c.PushString(k)
	if getValue {
		if ev.obj != nil {
			c.push(ev.obj.Get(k))
		} else {
			c.PushUndefined()
		}
	}
	return true
}

// duk_json_encode(ctx, idx) — replaces value with its JSON string.
func (c *Context) JsonEncode(idx int) {
	v := c.at(idx)
	var sb strings.Builder
	jsonEncodeValue(&sb, v)
	s := sb.String()
	i := c.NormalizeIndex(idx)
	if i >= 0 && i < len(c.stack) {
		c.stack[i] = c.h.vm.ToValue(s)
	} else {
		c.PushString(s)
	}
}

// duk_concat(ctx, n) — concatenate top n values into a string.
func (c *Context) Concat(n int) {
	top := len(c.stack)
	if n > top {
		n = top
	}
	var sb strings.Builder
	for _, v := range c.stack[top-n:] {
		sb.WriteString(v.String())
	}
	c.stack = c.stack[:top-n]
	c.PushString(sb.String())
}

// duk_join(ctx, n) — join top n values with a single space.
func (c *Context) Join(n int) {
	top := len(c.stack)
	if n > top {
		n = top
	}
	parts := make([]string, 0, n)
	for _, v := range c.stack[top-n:] {
		parts = append(parts, v.String())
	}
	c.stack = c.stack[:top-n]
	c.PushString(strings.Join(parts, " "))
}

// duk_suspend — bookkeeping only (es_context_suspend drops ec_mutex).
func (c *Context) Suspend(state *ThreadState) {}

// duk_resume — bookkeeping only (es_context_resume takes ec_mutex).
func (c *Context) Resume(state *ThreadState) {}

// duk_gc — Go GC; no-op for correctness.
func (c *Context) Gc(flags int) { runtime.GC() }

func jsonEncodeValue(sb *strings.Builder, v goja.Value) {
	if v == nil || goja.IsUndefined(v) {
		sb.WriteString("undefined")
		return
	}
	if goja.IsNull(v) {
		sb.WriteString("null")
		return
	}
	switch e := v.Export().(type) {
	case string:
		writeJSONString(sb, e)
	case bool:
		if e {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case float64:
		sb.WriteString(strconv.FormatFloat(e, 'g', -1, 64))
	case int64:
		sb.WriteString(strconv.FormatInt(e, 10))
	case int:
		sb.WriteString(strconv.Itoa(e))
	case int32:
		sb.WriteString(strconv.FormatInt(int64(e), 10))
	case uint32:
		sb.WriteString(strconv.FormatUint(uint64(e), 10))
	case uint64:
		sb.WriteString(strconv.FormatUint(e, 10))
	case goja.ArrayBuffer:
		sb.WriteString("null")
	case ptrVal, *ptrVal:
		sb.WriteString("null")
	default:
		obj, ok := v.(*goja.Object)
		if !ok {
			sb.WriteString("null")
			return
		}
		className := obj.ClassName()
		if className == "Array" {
			sb.WriteByte('[')
			length := 0
			if lv := obj.Get("length"); lv != nil {
				length = int(lv.ToInteger())
			}
			for i := range length {
				if i > 0 {
					sb.WriteByte(',')
				}
				jsonEncodeValue(sb, obj.Get(strconv.Itoa(i)))
			}
			sb.WriteByte(']')
			return
		}
		sb.WriteByte('{')
		first := true
		for _, k := range obj.Keys() {
			kv := obj.Get(k)
			if kv == nil || goja.IsUndefined(kv) {
				continue
			}
			if _, isfn := goja.AssertFunction(kv); isfn {
				continue
			}
			if !first {
				sb.WriteByte(',')
			}
			first = false
			writeJSONString(sb, k)
			sb.WriteByte(':')
			jsonEncodeValue(sb, kv)
		}
		sb.WriteByte('}')
	}
}

func writeJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString("\\\"")
		case '\\':
			sb.WriteString("\\\\")
		case '\n':
			sb.WriteString("\\n")
		case '\r':
			sb.WriteString("\\r")
		case '\t':
			sb.WriteString("\\t")
		case '\b':
			sb.WriteString("\\b")
		case '\f':
			sb.WriteString("\\f")
		default:
			if r < 0x20 {
				fmt.Fprintf(sb, "\\u%04x", r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}
