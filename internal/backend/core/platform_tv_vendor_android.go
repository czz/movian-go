//go:build android

package core

// Vendor-adapter: vendor TV behaviour lives in a profile, not in
// core rules. A new box means a new registry entry — never a core
// patch. Detection keys on the TvInput package names Java reports
// (e.g. "com.technicolor.android.dtvinput" → technicolor); unknown
// vendors get the generic profile, which is today's behaviour.

// tvVendorProfile — one vendor family's TV behaviour contract.
type tvVendorProfile struct {
	// Name — human/debug label ("generic", "technicolor").
	Name string

	// FeederURIPrefixes — extra URI prefixes the vendor treats as
	// playable tuner feeds beyond the generic list in tvIsMediaURI
	// (e.g. a proprietary "dvbfs:/" scheme). Empty for known vendors.
	FeederURIPrefixes []string

	// TunePrefersRowID — a positive channel id is a real TvProvider
	// row that must tune through TvView (proven on Technicolor/TIM;
	// true also for generic Android TV where the provider is sane).
	TunePrefersRowID bool

	// SetupNeedsActivity — whether the input list's Setup flag marks
	// the vendor setup activity (channel scan) worth offering.
	SetupNeedsActivity bool
}

// tvVendorRegistry — detection is by TvInput package prefix. Add a
// new vendor here; core code never branches on vendor names.
var tvVendorRegistry = []struct {
	InputPrefix string
	Profile     tvVendorProfile
}{
	{"com.technicolor.", tvVendorProfile{
		Name:               "technicolor",
		TunePrefersRowID:   true,
		SetupNeedsActivity: true,
	}},
}

// tvVendorGeneric — fallback: same behaviour the code had before the
// adapter existed.
var tvVendorGeneric = tvVendorProfile{
	Name:               "generic",
	TunePrefersRowID:   true,
	SetupNeedsActivity: true,
}

// tvVendorDetected — cached detection (inputs don't change at runtime).
var tvVendorDetected *tvVendorProfile

// tvVendor — the active vendor profile. Detection inspects the
// platform's TvInput package ids once and caches the result.
func (bs *BackendSystem) tvVendor() *tvVendorProfile {
	if tvVendorDetected != nil {
		return tvVendorDetected
	}
	p := tvVendorGeneric
	for _, in := range bs.tvLoadInputs() {
		for _, ent := range tvVendorRegistry {
			if len(in.ID) >= len(ent.InputPrefix) &&
				in.ID[:len(ent.InputPrefix)] == ent.InputPrefix {
				p = ent.Profile
				break
			}
		}
	}
	tvVendorDetected = &p
	return tvVendorDetected
}

// tvIsFeederURI — vendor-aware media-URI check: the generic rules in
// tvIsMediaURI plus any vendor-specific feeder prefixes.
func (bs *BackendSystem) tvIsFeederURI(uri string) bool {
	if tvIsMediaURI(uri) {
		return true
	}
	for _, pre := range bs.tvVendor().FeederURIPrefixes {
		if len(uri) >= len(pre) && uri[:len(pre)] == pre {
			return true
		}
	}
	return false
}
