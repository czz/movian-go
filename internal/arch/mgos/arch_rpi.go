//go:build mgos && rpi

package mgos

// ArchName — C: archname = PLATFORM (upgrade.c:1260). The upgrade
// manifest channel is per-board: "rpi" for the Raspberry Pi build.
const ArchName = "rpi"
