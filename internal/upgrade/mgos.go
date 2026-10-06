//go:build linux && mgos

package upgrade

// mgos.go — canonical port of the `#if STOS` blocks in src/upgrade.c,
// renamed MGOS (Movian-Go OS). Active only under -tags mgos.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/czz/movian-go/internal/arch/mgos"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/version"
)

// mgosState — C: upgrade.c:78-83 — STOS upgrade state file-statics,
// as instance state on Upgrade.mgos (Go dep-injection; the type is
// empty in non-mgos builds — see mgos_off.go).
type mgosState struct {
	upgradeNeeded  bool           // C: stos_upgrade_needed
	currentVersion uint32         // C: stos_current_version
	reqVersion     uint32         // C: stos_req_version
	availVersion   uint32         // C: stos_avail_version
	artifacts      []ArtifactInfo // C: stos_artifacts (detached list)
}

// mgosArch — C: archname = PLATFORM (upgrade.c:1260). mgos.ArchName is
// "rpi" under -tags rpi, GOARCH otherwise.
func mgosArch() string {
	return mgos.ArchName
}

// mgosGetCurrentVersion — C: stos_get_current_version (upgrade.c:1220-1238).
// Reads the running OS version from /mgosversion (C: /stosversion).
func (u *Upgrade) mgosGetCurrentVersion() {
	data, err := os.ReadFile(mgos.VersionFile)
	if err != nil {
		return
	}
	buf := string(data)
	if i := strings.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i]
	}
	if buf != "" {
		u.mgos.currentVersion = version.ParseVersionInt(buf)
		u.deps.TS.Trace(trace.TRACE_DEBUG, "STOS", "Current version: %s (%d)",
			buf, u.mgos.currentVersion)
	}
}

// mgosCheckUpgrade — C: stos_check_upgrade (upgrade.c:549-631).
// Fetches the OS manifest <ctrlbase>/master-<arch>.json, extracts the
// version and the artifacts list, validates each sqfs/bin artifact.
func (u *Upgrade) mgosCheckUpgrade() int {
	url := fmt.Sprintf("%s/master-%s.json", mgos.CtrlBase, mgosArch())

	// C: b = fa_load(url, FA_LOAD_ERRBUF(errbuf, sizeof(errbuf)),
	//      FA_LOAD_FLAGS(FA_DISABLE_AUTH | FA_COMPRESSION), NULL)
	//    (upgrade.c:558) — NULL → TRACE errbuf, return -1
	b, lerr := facore.FALoad2(u.fam, url,
		&facore.FALoadArgs{
			Flags: facore.FaDisableAuth | facore.FaCompression,
		})
	if lerr != nil {
		u.deps.TS.Trace(trace.TRACE_ERROR, "STOS",
			"Unable to query for STOS manifest -- %s",
			lerr.Error())
		return -1
	}
	data := b.Data[:b.Size]

	var doc struct {
		Version   string         `json:"version"`
		Artifacts []ArtifactInfo `json:"artifacts"`
	}
	if json.Unmarshal(data, &doc) != nil {
		u.deps.TS.Trace(trace.TRACE_ERROR, "STOS", "Malformed JSON")
		return -1
	}

	if doc.Version == "" {
		return -1
	}

	u.mgos.availVersion = version.ParseVersionInt(doc.Version)
	u.deps.TS.Trace(trace.TRACE_DEBUG, "STOS", "Available version: %s (%d)",
		doc.Version, u.mgos.availVersion)

	// C: htsmsg_release(stos_artifacts); stos_artifacts = NULL;
	//    stos_artifacts = htsmsg_detach_submsg(artifacts field)
	u.mgos.artifacts = nil
	if doc.Artifacts == nil {
		return -1
	}
	u.mgos.artifacts = doc.Artifacts

	// C: validate — each artifact with type sqfs or bin must have
	//    url, sha1, size and name (upgrade.c:602-619)
	for _, a := range u.mgos.artifacts {
		if a.Type != "sqfs" && a.Type != "bin" {
			continue
		}
		if a.URL == "" || a.SHA1 == "" || a.Size == 0 || a.Name == "" {
			u.mgos.artifacts = nil // C: bad_artifacts
			return 1
		}
	}
	return 0
}

// cleanDlDir — C: clean_dl_dir (upgrade.c:858-885). Removes all files
// from the temporary download directory /boot/dl.
func (u *Upgrade) cleanDlDir() {
	namelist, err := os.ReadDir(mgos.DlDir)
	if err != nil {
		u.deps.TS.Trace(trace.TRACE_ERROR, "Upgrade",
			"Unable to scan directory %s -- %s (%d)",
			mgos.DlDir, err, 0)
		return
	}
	for _, e := range namelist {
		f := e.Name()
		if f == "." || f == ".." {
			continue
		}
		fullpath := filepath.Join(mgos.DlDir, f)
		if err := os.Remove(fullpath); err != nil {
			u.deps.TS.Trace(trace.TRACE_ERROR, "Upgrade",
				"Unable to delete %s -- %s (%d)", fullpath, err, 0)
		}
	}
}

// mgosAddArtifacts — C: stos_add_artifacts (upgrade.c:888-973).
// Queues each sqfs/bin/txt artifact for download into /boot/dl then
// rename into /boot. Selectors (e.g. machine=armv6l) that don't match
// uname turn the artifact into a delete (a_url = NULL).
func (u *Upgrade) mgosAddArtifacts(aq *ArtifactQueue) {
	// C: uname(&uts) — machine selector
	var uts syscall.Utsname
	syscall.Uname(&uts)
	machine := charsToString(int8ToBytes(uts.Machine[:]))

	for _, msg := range u.mgos.artifacts {
		typ := msg.Type
		if typ != "sqfs" && typ != "bin" && typ != "txt" {
			continue
		}
		dlurl := msg.URL
		sha1 := msg.SHA1
		dlsize := msg.Size
		name := msg.Name
		selectors := msg.Selectors
		if dlurl == "" || sha1 == "" || name == "" {
			continue
		}

		// C: foobar-1.2.3.img → foobar.img — strip the last "-version"
		//    component, keep the extension.
		postfix := ""
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			postfix = name[i:]
		} else {
			continue
		}
		n := name[:len(name)-len(postfix)]
		dash := strings.LastIndexByte(n, '-')
		if dash < 0 {
			continue
		}
		n = n[:dash]

		dlpath := fmt.Sprintf("%s/%s%s", mgos.DlDir, n, postfix)
		finalpath := fmt.Sprintf("%s/%s%s", mgos.BootDir, n, postfix)

		a := &ArtifactFull{}

		if selectors != "" {
			// C: key=value comma list; "machine" must match uname,
			//    otherwise the artifact is deleted locally (dlurl=NULL)
			for _, kv := range strings.Split(selectors, ",") {
				eq := strings.IndexByte(kv, '=')
				if eq < 0 {
					break
				}
				key, value := kv[:eq], kv[eq+1:]
				if key == "machine" && value != machine {
					u.deps.TS.Trace(trace.TRACE_DEBUG, "Upgrade",
						"%s [%s] skipped (not for this machine [%s])",
						name, value, machine)
					dlurl = ""
				}
			}
		}

		a.Task = "System" // C: a->a_task = _("System")
		a.Name = name
		a.URL = dlurl
		a.TempPath = dlpath
		a.FinalPath = finalpath
		// C: hex2bin(a->a_digest, sizeof(a->a_digest), sha1)
		if digest, derr := hex.DecodeString(sha1); derr == nil {
			copy(a.Digest[:], digest)
		}
		a.Size = int64(dlsize)
		// C: a->a_check_partial_update = !strcmp(type, "txt")
		a.CheckPartial = typ == "txt"
		// C: TAILQ_INSERT_TAIL(aq, a, a_link)
		if aq.Tail == nil {
			aq.Head = a
			aq.Tail = a
		} else {
			aq.Tail.Link = a
			aq.Tail = a
		}
	}
}

// mgosGate — C: the `#if STOS` block inside check_upgrade
// (upgrade.c:689-720). Sets mgosUpgradeNeeded from omnigrade and the
// manifest's stosVersion requirement; fetches the OS manifest and
// verifies availability. Returns true when the C code took `goto err`
// (upgrade_error already set — caller sets status "checkError").
func (u *Upgrade) mgosGate(m *UpgradeManifest) bool {
	u.mgos.upgradeNeeded = *u.deps.EnableOmnigrade != 0

	if m.Manifest != nil && m.Manifest.STOSVersion != "" {
		u.mgos.reqVersion = version.ParseVersionInt(m.Manifest.STOSVersion)

		if u.mgos.currentVersion < u.mgos.reqVersion {
			u.mgos.upgradeNeeded = true
			u.deps.TS.Trace(trace.TRACE_DEBUG, "STOS",
				"Required version for upgrade: %s (%d)",
				m.Manifest.STOSVersion, u.mgos.reqVersion)
			u.deps.TS.Trace(trace.TRACE_DEBUG, "STOS",
				"Need to perform STOS upgrade, checking what is available")
		}

		if u.mgos.upgradeNeeded {
			if u.mgosCheckUpgrade() != 0 {
				if u.upgradeError != nil {
					u.pm.SetStringEx(u.upgradeError, nil,
						"Failed to find any STOS updates", 0)
				}
				return true
			}
			if u.mgos.availVersion < u.mgos.reqVersion {
				if u.upgradeError != nil {
					u.pm.SetStringEx(u.upgradeError, nil,
						"Required STOS version not available", 0)
				}
				return true
			}
		}
	}
	return false
}

// mgosInstallPrepare — C: `#if STOS` head of install_locked
// (upgrade.c:1087-1108). Remounts /boot read-write, creates and cleans
// the download dir, queues the OS artifacts. Returns true to abort.
func (u *Upgrade) mgosInstallPrepare(aq *ArtifactQueue) bool {
	// First, remount /boot as readwrite
	if err := syscall.Mount(mgos.BootDev, mgos.BootDir, "vfat",
		syscall.MS_REMOUNT, ""); err != nil {
		u.installError("Unable to remount /boot to read-write", "")
		u.deps.Usage.Event("Upgrade error", 1, "reason", "Remount")
		return true
	}

	if err := os.Mkdir(mgos.DlDir, 0770); err != nil && !os.IsExist(err) {
		u.installError("Unable to create temp directory /boot/dl", "")
		u.deps.Usage.Event("Upgrade error", 1, "reason", "mkdir")
		return true
	}

	// Clean the temporary download directory
	u.cleanDlDir()

	// Add MGOS artifacts if we need to upgrade mgos
	if u.mgos.upgradeNeeded && u.mgos.artifacts != nil {
		u.mgosAddArtifacts(aq)
	}
	return false
}

// mgosSyncFS — C: sync() under #if STOS in move_files_into_place.
func (u *Upgrade) mgosSyncFS() {
	u.deps.TS.Trace(trace.TRACE_DEBUG, "Upgrade", "Syncing filesystems")
	syscall.Sync()
	u.deps.TS.Trace(trace.TRACE_DEBUG, "Upgrade", "Syncing filesystems done")
}

// mgosExitCode — C: upgrade.c:1146-1150 — after a successful install the
// OS reboots when an mgos upgrade was staged, otherwise just restarts.
func (u *Upgrade) mgosExitCode() int {
	if u.mgos.upgradeNeeded {
		return AppExitReboot
	}
	return AppExitRestart
}

// mgosSetTempPath — C: `#if STOS` in app_add_artifact (upgrade.c:847-851):
// the app binary downloads to "<final>.tmp" then renames into place.
func mgosSetTempPath(a *ArtifactFull) {
	a.TempPath = a.FinalPath + ".tmp"
}

// mgosStartUpgrade — C: `#if STOS` in upgrade_init (upgrade.c:1257-1261):
// read the running OS version and select the sqfs artifact channel.
func (u *Upgrade) mgosStartUpgrade() {
	u.mgosGetCurrentVersion()
	u.artifactType = "sqfs"
	u.archName = mgosArch()
}

// mgosOpenFlags — C: `#if STOS` in download_file (upgrade.c:491-493):
// the artifact file is opened with O_SYNC on top of O_CREAT|O_RDWR|O_TRUNC.
func mgosOpenFlags() int { return os.O_SYNC }

// int8ToBytes — syscall.Utsname fields are []int8 on amd64 but
// []uint8 on arm (C char signedness is arch-dependent).
func int8ToBytes[T int8 | uint8](b []T) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = byte(v)
	}
	return out
}

// charsToString — utsname byte-array to Go string (NUL-terminated).
func charsToString(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return s
}
