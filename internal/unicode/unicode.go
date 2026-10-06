// Package unicode provides Unicode case folding.
//
// C parity: src/misc/str.c builds casefoldtable[] from
// unicode_casefolding[] and exposes it via unicode_casefold(). The
// Go stdlib unicode package implements the same simple (1:1) case
// folding from the same UnicodeData source, so we delegate to it
// instead of carrying a hand-maintained subset table.
package unicode

import (
	"strings"
	"unicode"

	"github.com/czz/movian-go/internal/misc"
)

// UnicodeSystem manages Unicode support without global state
type UnicodeSystem struct {
	initialized bool
}

// NewUnicodeSystem creates a new Unicode system
func NewUnicodeSystem() *UnicodeSystem {
	return &UnicodeSystem{}
}

// Start initializes Unicode support.
// C: unicode_init() (misc/str.c) — delegated to misc.UnicodeStart,
// which builds the canonical casefoldtable used by misc.Dictcmp etc.
func (us *UnicodeSystem) Start() error {
	if us.initialized {
		return nil
	}
	misc.UnicodeStart()
	us.initialized = true
	return nil
}

// CaseFold returns the case-folded version of a rune
func (us *UnicodeSystem) CaseFold(r rune) rune {
	if !us.initialized {
		us.Start()
	}
	// C's casefoldtable maps MICRO SIGN → GREEK SMALL LETTER MU;
	// Go's simple ToLower leaves it unchanged.
	if r == 'µ' {
		return 'μ'
	}
	return unicode.ToLower(r)
}

// CaseFoldString returns the case-folded version of a string
func (us *UnicodeSystem) CaseFoldString(s string) string {
	if !us.initialized {
		us.Start()
	}
	return strings.Map(unicode.ToLower, s)
}
