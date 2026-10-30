// Package hostname holds the rule that decides whether a computer name a
// source reported can name a host on Local network.
package hostname

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Valid reports whether a reported computer name is fit to name an
// entry. The name becomes part of the service id, which is also a
// settings store path, so only letters, digits and the punctuation
// NetBIOS allows in a computer name pass; anything else leaves the host
// listed by address.
func Valid(name string) bool {
	if name == "" || len(name) > 63 || name[0] == '.' || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("!@#$%^&()-_'{}.~", r) {
			return false
		}
	}
	return true
}
