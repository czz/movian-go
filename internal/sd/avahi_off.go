//go:build !(linux && !android) || (avahicgo && !cgo)

package sd

// avahiDaemon — C: avahi.c statics. Empty without avahi support.
type avahiDaemon struct{}

// C: #ifdef CONFIG_AVAHI avahi_init() #endif — CONFIG_AVAHI is only enabled
// on Linux builds with avahi-client; elsewhere sd_init() makes no call.
func (s *System) avahiStart() {}

// AvahiUpdateHostname — C: avahi_update_hostname (avahi.c, #if STOS).
// No-op without avahi.
func (s *System) AvahiUpdateHostname() {}
