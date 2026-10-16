package ecmascript

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dop251/goja"
)

type proxyTestProp struct {
	name     string
	value    any
	children []*proxyTestProp
}

func (p *proxyTestProp) child(name string) *proxyTestProp {
	for _, child := range p.children {
		if child.name == name {
			return child
		}
	}
	child := &proxyTestProp{name: name}
	p.children = append(p.children, child)
	return child
}

type proxyTestPropRegistry struct {
	nextID int
	props  map[int]*proxyTestProp
}

func (r *proxyTestPropRegistry) object(vm *goja.Runtime, p *proxyTestProp) *goja.Object {
	id := r.nextID
	r.nextID++
	r.props[id] = p
	o := vm.NewObject()
	if err := o.Set("__propID", id); err != nil {
		panic(err)
	}
	return o
}

func (r *proxyTestPropRegistry) get(vm *goja.Runtime, v goja.Value) *proxyTestProp {
	id := int(v.ToObject(vm).Get("__propID").ToInteger())
	return r.props[id]
}

// TestPropProxyAssignsObjectsToNumericChildren exercises movian/prop's public
// proxy behavior with Goja and the native/prop getChild contract: string keys
// create/find named children; integer keys index existing children.
func TestPropProxyAssignsObjectsToNumericChildren(t *testing.T) {
	vm := goja.New()
	nativeProp := vm.NewObject()
	registry := &proxyTestPropRegistry{props: make(map[int]*proxyTestProp)}
	rootProp := &proxyTestProp{name: "global"}

	setNativeFunc := func(name string, fn func(goja.FunctionCall) goja.Value) {
		t.Helper()
		if err := nativeProp.Set(name, fn); err != nil {
			t.Fatal(err)
		}
	}
	getProp := func(v goja.Value) *proxyTestProp {
		p := registry.get(vm, v)
		if p == nil {
			panic("native/prop received a non-property object")
		}
		return p
	}

	setNativeFunc("global", func(goja.FunctionCall) goja.Value {
		return registry.object(vm, rootProp)
	})
	setNativeFunc("create", func(call goja.FunctionCall) goja.Value {
		return registry.object(vm, &proxyTestProp{name: call.Argument(0).String()})
	})
	setNativeFunc("getChild", func(call goja.FunctionCall) goja.Value {
		p := getProp(call.Argument(0))
		key := call.Argument(1)
		if _, ok := key.(*goja.Symbol); ok {
			return vm.ToValue(func(call goja.FunctionCall) goja.Value {
				raw := call.This.ToObject(vm).Get("__rawprop__")
				return vm.ToValue(getProp(raw).value)
			})
		}
		if goja.IsNumber(key) {
			index := key.ToInteger()
			if index < 0 || index >= int64(len(p.children)) {
				return goja.Undefined()
			}
			return registry.object(vm, p.children[index])
		}
		return registry.object(vm, p.child(key.String()))
	})
	setNativeFunc("set", func(call goja.FunctionCall) goja.Value {
		p := getProp(call.Argument(0)).child(call.Argument(1).String())
		p.value = call.Argument(2).Export()
		return goja.Undefined()
	})
	setNativeFunc("getValue", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(getProp(call.Argument(0)).value)
	})
	setNativeFunc("isValue", func(goja.FunctionCall) goja.Value { return vm.ToValue(false) })
	setNativeFunc("setRichStr", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	setNativeFunc("enumerate", func(goja.FunctionCall) goja.Value { return vm.NewArray() })
	setNativeFunc("has", func(goja.FunctionCall) goja.Value { return vm.ToValue(false) })
	setNativeFunc("deleteChild", func(goja.FunctionCall) goja.Value { return vm.ToValue(true) })

	if err := vm.Set("require", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).String() != "native/prop" {
			panic(fmt.Sprintf("unexpected module %q", call.Argument(0).String()))
		}
		return nativeProp
	}); err != nil {
		t.Fatal(err)
	}
	exports := vm.NewObject()
	if err := vm.Set("exports", exports); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join("..", "..", "res", "ecmascript", "modules", "movian", "prop.js")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vm.RunString(string(source)); err != nil {
		t.Fatalf("evaluate movian/prop module: %v", err)
	}
	createRoot, ok := goja.AssertFunction(exports.Get("createRoot"))
	if !ok {
		t.Fatal("movian/prop does not export createRoot")
	}
	root, err := createRoot(goja.Undefined(), vm.ToValue("test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := vm.Set("root", root); err != nil {
		t.Fatal(err)
	}

	got, err := vm.RunString(`
		root.metadata = { backdrops: [{ url: "https://example.invalid/cover.jpg" }] };
		root.metadata.backdrops[0].url;
	`)
	if err != nil {
		t.Fatalf("assign and read nested numeric child: %v", err)
	}
	if got.String() != "https://example.invalid/cover.jpg" {
		t.Fatalf("nested numeric child URL = %q, want %q", got.String(), "https://example.invalid/cover.jpg")
	}
}
