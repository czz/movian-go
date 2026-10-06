package nls

import (
	"sync"

	propcore "github.com/czz/movian-go/internal/prop"
)

// nlsString represents a translatable string with plural forms.
// Matches C's nls_string_t.
type nlsString struct {
	key    string
	values []string // index 0 = plural, index 1 = singular (or more forms for some languages)

	prop *propcore.Prop // C: ns_prop (i18n.c:367)
}

var (
	// C: static LIST_HEAD(, nls_string) nls_hash[61] — the hash buckets
	// with move-to-front are an internal lookup optimization; a Go map
	// preserves the observable behavior.
	nlsStrings = map[string]*nlsString{}
	nlsMu      sync.RWMutex

	// nlsPM creates the per-string prop roots (C uses the global prop
	// manager via prop_create_root).
	nlsPM = propcore.NewPropManager()
)

// nlsStringFind — C: nls_string_find (i18n.c:354-378). Creates the entry
// with ns_prop = prop_create_root(NULL) + prop_set_rstring(key).
func nlsStringFind(key string) *nlsString {
	if ns, ok := nlsStrings[key]; ok {
		return ns
	}
	ns := &nlsString{key: key}
	ns.prop = nlsPM.CreateRoot("")
	nlsPM.SetStringEx(ns.prop, nil, key, propcore.StringUTF8)
	nlsStrings[key] = ns
	return ns
}

func nsValGet(ns *nlsString, idx int) string {
	if idx < 0 || idx >= len(ns.values) {
		return ""
	}
	return ns.values[idx]
}

// nsValSet sets the translation at the given index.
func nsValSet(ns *nlsString, idx int, value string) {
	for len(ns.values) <= idx {
		ns.values = append(ns.values, "")
	}
	ns.values[idx] = value
}

// nsValClr — C: ns_val_clr (i18n.c) — clears all translations.
func nsValClr(ns *nlsString) {
	ns.values = nil
}

// GetRStringP returns the plural or singular form of a translated string.
// Matches C's nls_get_rstringp(string, singularis, val).
// If val == 1: returns singular translation (or singularis as fallback).
// If val != 1: returns plural translation (or string as fallback).
func GetRStringP(str, singularis string, val int) string {
	nlsMu.Lock()
	defer nlsMu.Unlock()

	ns := nlsStringFind(str)
	if val == 1 {
		if s := nsValGet(ns, 1); s != "" {
			return s
		}
		return singularis
	}
	if s := nsValGet(ns, 0); s != "" {
		return s
	}
	return str
}

// GetRString returns the translation of a string.
// Matches C's nls_get_rstring(string).
func GetRString(str string) string {
	nlsMu.Lock()
	defer nlsMu.Unlock()

	ns := nlsStringFind(str)
	if s := nsValGet(ns, 0); s != "" {
		return s
	}
	return str
}

// GetProp — C: nls_get_prop (i18n.c:383-387). Returns the prop that
// tracks this string's current translation.
func GetProp(str string) *propcore.Prop {
	nlsMu.Lock()
	defer nlsMu.Unlock()

	return nlsStringFind(str).prop
}

// LoadTranslation sets a translation for a key at the given index.
// C: ns_val_set + (idx==0 → prop_set_rstring(ns_prop, ns_val_get(ns,0)))
// as in nls_load_from_data (i18n.c:475-476, 500-502).
func LoadTranslation(key string, idx int, value string) {
	nlsMu.Lock()
	defer nlsMu.Unlock()

	ns := nlsStringFind(key)
	nsValSet(ns, idx, value)
	if idx == 0 {
		nlsPM.SetStringEx(ns.prop, nil, nsValGet(ns, 0), propcore.StringUTF8)
	}
}

// Clear — C: nls_clear (i18n.c:423-436). Clears all translation values
// and resets each ns_prop to its key; entries are NOT removed.
func Clear() {
	nlsMu.Lock()
	defer nlsMu.Unlock()

	for _, ns := range nlsStrings {
		nsValClr(ns)
		nlsPM.SetStringEx(ns.prop, nil, ns.key, propcore.StringUTF8)
	}
}
