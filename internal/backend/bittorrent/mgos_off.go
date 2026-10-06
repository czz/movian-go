//go:build !mgos

package bittorrent

// mgosTorrent — C: #ifdef STOS in torrent_settings.c.
const mgosTorrent = false
