//go:build android_cf_firstparty

package ui

// androidFirstPartyVariant is true in the restricted first-party build: a
// Partitioned cookie is accepted for the exact top-level origin only. This
// models the origin the WebView exposed, not CHIPS.
const androidFirstPartyVariant = true
