//go:build !linux || !mgos

package upgrade

// mgos_off.go — no-op counterparts of the `#if STOS` seams in
// src/upgrade.c for builds without -tags "linux mgos".

// mgosState — empty in non-mgos builds (see mgos.go for the real
// fields; C: the `#if STOS` file-statics simply don't exist).
type mgosState struct{}

// mgosGate — C: `#if STOS` block in check_upgrade. Never fails.
func (u *Upgrade) mgosGate(m *UpgradeManifest) bool { return false }

// mgosInstallPrepare — C: `#if STOS` head of install_locked.
func (u *Upgrade) mgosInstallPrepare(aq *ArtifactQueue) bool { return false }

// mgosSyncFS — C: sync() under `#if STOS` in move_files_into_place.
func (u *Upgrade) mgosSyncFS() {}

// mgosExitCode — C: upgrade.c:1146-1150 — plain builds always restart.
func (u *Upgrade) mgosExitCode() int { return AppExitRestart }

// mgosSetTempPath — C: `#if STOS` in app_add_artifact.
func mgosSetTempPath(a *ArtifactFull) {}

// mgosStartUpgrade — C: `#if STOS` in upgrade_init.
func (u *Upgrade) mgosStartUpgrade() {}

// mgosOpenFlags — C: `#if STOS` in download_file adds O_SYNC; plain
// builds add nothing.
func mgosOpenFlags() int { return 0 }
