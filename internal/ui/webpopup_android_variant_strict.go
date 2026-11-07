//go:build !android_cf_firstparty

package ui

// androidFirstPartyVariant is false in the strict build: a Partitioned
// cookie is rejected.
const androidFirstPartyVariant = false
