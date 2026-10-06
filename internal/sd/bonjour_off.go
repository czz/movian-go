//go:build !darwin

package sd

// bonjourDaemon — C: bonjour.c statics. Empty without bonjour support.
type bonjourDaemon struct{}

// C: #ifdef CONFIG_BONJOUR bonjour_init() #endif — CONFIG_BONJOUR is only
// enabled on macOS builds (configure.osx); elsewhere sd_init() makes no call.
func (s *System) bonjourStart() {}
