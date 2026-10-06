// Package mgos — canonical port of the C STOS ("Showtime OS") platform
// layer, renamed MGOS (Movian-Go OS). Enabled by the `mgos` build tag,
// which corresponds to the C `-DSTOS` compile-time define.
//
// C source: src/arch/stos/ + `#if STOS` blocks scattered in posix.c,
// upgrade.c, settings.c, avahi.c, plugins.c, http_server.c, i18n.c,
// persistent_file.c, kvstore.c, torrent_settings.c.
//
// Paths are renamed s/stos/mgos/ and the app component "showtime" is
// renamed "movian-go" (the Go binary's name).
package mgos

const (
	// C: /stos/cache/showtime (posix.c:138)
	CachePath = "/mgos/cache/movian-go"
	// C: /stos/persistent/showtime (posix.c:139)
	PersistentPath = "/mgos/persistent/movian-go"
	// C: /stos/fsinfo (stos_automount.c:373) — directory where the OS
	// writes one blkid-style info file per block device.
	FsinfoDir = "/mgos/fsinfo"
	// C: /stos/media (stos_automount.c:88) — automount root.
	MediaDir = "/mgos/media"
	// C: /stosversion (posix.c:92, upgrade.c:1225) — OS version file.
	VersionFile = "/mgosversion"
	// C: /var/run/stos-splash.pid (rpi_main.c:751) — bootsplash pidfile.
	SplashPidFile = "/var/run/mgos-splash.pid"
	// C: ctrlbase_stos "http://upgrade.movian.tv/stos/2" (upgrade.c:78)
	CtrlBase = "http://upgrade.movian-go.czz78.com/mgos/2"
	// C: upgrade.c install_locked — boot partition device/mountpoint.
	BootDev = "/dev/mmcblk0p1"
	BootDir = "/boot"
	DlDir   = "/boot/dl"
)
