//go:build linux && rpi && connman && connmancgo

package connman

// prop_gvariant.go — C: src/prop/prop_gvariant.c. GVariant → prop tree
// conversion used by connman (the only CONFIG_CONNMAN consumer). Lives
// in this package rather than pkg/prop because cgo GVariant types are
// per-package and prop/core has no glib dependency.

/*
#include <gio/gio.h>
#include "csrc/connman_glue.h"
*/
import "C"

import (
	"fmt"
	"math"
	"strings"

	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// fixupTitle — C: fixup_title (prop_gvariant.c:20-28). Lowercases
// A-Z and maps '.' → '_' in place.
func fixupTitle(k string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		if r == '.' {
			return '_'
		}
		return r
	}, k)
}

// propSetFromGvariant — C: prop_set_from_gvariant (prop_gvariant.c:36-94).
func propSetFromGvariant(v *C.GVariant, p *propcore.Prop) {
	ts := C.GoString(C.g_variant_get_type_string(v))

	switch ts {
	case "b": // C: G_VARIANT_TYPE_BOOLEAN
		pm.SetIntEx(p, nil, misc.BoolToInt(C.g_variant_get_boolean(v) != 0))
	case "y": // C: G_VARIANT_TYPE_BYTE
		pm.SetIntEx(p, nil, int(C.g_variant_get_byte(v)))
	case "n": // C: G_VARIANT_TYPE_INT16
		pm.SetIntEx(p, nil, int(C.g_variant_get_int16(v)))
	case "q": // C: G_VARIANT_TYPE_UINT16
		pm.SetIntEx(p, nil, int(C.g_variant_get_uint16(v)))
	case "i": // C: G_VARIANT_TYPE_INT32
		pm.SetIntEx(p, nil, int(C.g_variant_get_int32(v)))
	case "u": // C: G_VARIANT_TYPE_UINT32
		pm.SetIntEx(p, nil, int(C.g_variant_get_uint32(v)))
	case "x": // C: G_VARIANT_TYPE_INT64
		val := int64(C.g_variant_get_int64(v))
		if val <= math.MaxInt32 {
			pm.SetIntEx(p, nil, int(val))
		} else {
			pm.SetFloatEx(p, nil, float32(val))
		}
	case "t": // C: G_VARIANT_TYPE_UINT64
		val := uint64(C.g_variant_get_uint64(v))
		if val <= math.MaxInt32 {
			pm.SetIntEx(p, nil, int(val))
		} else {
			pm.SetFloatEx(p, nil, float32(val))
		}
	case "s", "o", "g": // C: STRING || OBJECT_PATH || SIGNATURE
		pm.SetStringEx(p, nil, C.GoString(C.g_variant_get_string(v, nil)),
			propcore.StringUTF8)
	case "a{sv}": // C: G_VARIANT_TYPE_VARDICT
		pm.VoidChilds(p)
		propSetFromVardict(v, p)
	default:
		if C.movian_variant_type_is_array(v) != 0 {
			num := int(C.g_variant_n_children(v))
			pm.DestroyChilds(p)
			for i := range num {
				propSetFromGvariant(
					C.g_variant_get_child_value(v, C.gsize(i)),
					pm.CreateEx(p, "", nil, true, false))
			}
		} else {
			// C: fprintf(stderr, "%s(): can't deal with type %s\n", ...)
			fmt.Printf("propSetFromGvariant(): can't deal with type %s\n", ts)
		}
	}
}

// propSetFromVardict — C: prop_set_from_vardict (prop_gvariant.c:102-115).
func propSetFromVardict(v *C.GVariant, parent *propcore.Prop) {
	var iter C.GVariantIter
	var value *C.GVariant
	var key *C.char

	C.g_variant_iter_init(&iter, v)
	for C.movian_iter_loop_sv(&iter, &key, &value) != 0 {
		k := fixupTitle(C.GoString(key))
		propSetFromGvariant(value, pm.CreateEx(parent, k, nil, false, false))
	}
}

// propSetFromTuple — C: prop_set_from_tuple (prop_gvariant.c:121-131).
func propSetFromTuple(v *C.GVariant, parent *propcore.Prop) {
	key := C.GoString(C.g_variant_get_string(
		C.g_variant_get_child_value(v, 0), nil))
	value := C.g_variant_get_variant(C.g_variant_get_child_value(v, 1))

	k := fixupTitle(key)
	propSetFromGvariant(value, pm.CreateEx(parent, k, nil, false, false))
}
