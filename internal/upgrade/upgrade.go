package upgrade

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/usage"
	"github.com/czz/movian-go/internal/version"
)

// Upgrade is the upgrade subsystem state.
// C: upgrade.c file-statics — upgrade_mutex, ctrlbase, artifact_type,
// archname, upgrade_root/status/error/progress/task, upgrade_track,
// app_download_*, notify_upgrades, inhibit_checks, news_ref
// (upgrade.c:55-75). In C they are globals; here they live on the
// instance.
type Upgrade struct {
	mutex       sync.Mutex // C: HTS_MUTEX_DECL(upgrade_mutex)
	settingsMgr *settings.SettingsManager
	pm          *propcore.PropManager
	ts          *trace.TraceSystem // C: trace() global — injected

	// Props — C: static prop_t *upgrade_root/status/error/progress/task
	upgradeRoot     *propcore.Prop
	upgradeStatus   *propcore.Prop
	upgradeError    *propcore.Prop
	upgradeProgress *propcore.Prop
	upgradeTask     *propcore.Prop

	ctrlBase     string                    // C: static const char *ctrlbase (upgrade.c:57)
	artifactType string                    // C: const char *artifact_type — platform seams only
	archName     string                    // C: const char *archname
	fam          *facore.FileAccessManager // C: implicit global fa context

	upgradeTrack   string // C: static char *upgrade_track
	notifyUpgrades int    // C: static int notify_upgrades — SettingTagWriteInt needs *int
	inhibitChecks  bool   // C: static int inhibit_checks = 1

	// C: static char *app_download_url/app_download_name,
	//     int app_download_size, uint8_t app_download_digest[20]
	appDownloadURL    string
	appDownloadName   string
	appDownloadSize   int
	appDownloadDigest [20]byte

	// newsRef — C: static prop_t *news_ref (upgrade.c:75)
	newsRef *propcore.Prop

	// mgos — C: `#if STOS` file-statics (upgrade.c:78-83). Empty
	// struct in non-mgos builds (mgos_off.go).
	mgos mgosState

	// deps — external seams (C globals: app_shutdown, usage_event,
	// gconf.upgrade_path/binary). Injected at construction.
	deps UpgradeDeps
}

// SetTraceSystem injects the trace system (C: trace() global).
func (u *Upgrade) SetTraceSystem(ts *trace.TraceSystem) { u.ts = ts }

// NewUpgrade allocates the upgrade subsystem.
// C: the static initializers in upgrade.c (ctrlbase, inhibit_checks=1);
// upgrade_init does the rest. deps may be nil (all seams no-op).
func NewUpgrade(sm *settings.SettingsManager, pm *propcore.PropManager, deps *UpgradeDeps) *Upgrade {
	u := &Upgrade{
		ctrlBase:      "http://upgrade.movian-go.czz78.com/upgrade/3",
		inhibitChecks: true,
		settingsMgr:   sm,
		pm:            pm,
	}
	if deps != nil {
		u.fam = deps.FAM
	}
	if deps != nil {
		u.deps = *deps
	}
	if u.deps.EnableBinReplace == nil {
		u.deps.EnableBinReplace = new(int)
	}
	if u.deps.EnableOmnigrade == nil {
		u.deps.EnableOmnigrade = new(int)
	}
	return u
}

// ==================== COMPLETE UPGRADE SYSTEM IMPLEMENTATION ====================

// UpgradeManifest represents the upgrade manifest from server
type UpgradeManifest struct {
	Version   string           `json:"version"`
	Artifacts []ArtifactInfo   `json:"artifacts"`
	Changelog []ChangelogEntry `json:"changelog"`
	Manifest  *STOSManifest    `json:"manifest,omitempty"`
}

// ArtifactInfo represents artifact info from manifest
type ArtifactInfo struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	SHA1      string `json:"sha1"`
	Size      int    `json:"size"`
	Name      string `json:"name"`
	Selectors string `json:"selectors,omitempty"`
}

// ChangelogEntry represents a changelog entry
type ChangelogEntry struct {
	Version string `json:"version"`
	Desc    string `json:"desc"`
}

// STOSManifest represents STOS-specific manifest
type STOSManifest struct {
	STOSVersion string `json:"stosVersion,omitempty"`
}

// ArtifactFull represents a complete upgrade artifact (C structure)
type ArtifactFull struct {
	Link             *ArtifactFull
	URL              string
	TempPath         string
	FinalPath        string
	Task             string
	Name             string
	Digest           [20]byte
	CheckPartial     bool
	ProgressOffset   float32
	ProgressScale    float32
	ProgressPart     int
	ProgressNumParts int
	Size             int64
}

// ArtifactQueue represents a queue of artifacts
type ArtifactQueue struct {
	Head *ArtifactFull
	Tail *ArtifactFull
}

// artifactsFree frees all artifacts in the queue
func artifactsFree(aq *ArtifactQueue) {
	for a := aq.Head; a != nil; {
		next := a.Link
		a.Link = nil
		a = next
	}
	aq.Head = nil
	aq.Tail = nil
}

// artifactsComputeProgressbarScale computes progress bar scale for artifacts
func artifactsComputeProgressbarScale(aq *ArtifactQueue) {
	totalSize := int64(0)
	for a := aq.Head; a != nil; a = a.Link {
		totalSize += a.Size
	}

	if totalSize == 0 {
		return
	}

	offset := float32(0)
	for a := aq.Head; a != nil; a = a.Link {
		a.ProgressScale = float32(a.Size) / float32(totalSize)
		a.ProgressOffset = offset
		offset += a.ProgressScale
	}
}

// artifactUpdateProgress updates progress for an artifact
func (u *Upgrade) artifactUpdateProgress(a *ArtifactFull, p float32) {
	p = p / float32(a.ProgressNumParts)
	p += float32(a.ProgressPart) / float32(a.ProgressNumParts)

	if u.upgradeProgress != nil {
		u.pm.SetFloatEx(u.upgradeProgress, nil, a.ProgressOffset+a.ProgressScale*p)
	}
}

// patchedConfigFile replaces the marked segment of the installed file
// with the marked segment of the download. newBegin/newEnd are the
// positions of the BEGIN/END markers inside data (found by caller).
// C: patched_config_file (upgrade.c:192-289) — seg1 = cur before
// BEGIN, seg2 = upd BEGIN→END slice, seg3 = cur END-marker→EOF
// (END marker preserved from the installed file).
func patchedConfigFile(data []byte, newBegin, newEnd int, fname string) ([]byte, error) {
	curData, err := os.ReadFile(fname)
	if err != nil {
		return data, nil
	}

	beginIdx := strings.Index(string(curData), "# BEGIN SHOWTIME CONFIG\n")
	if beginIdx == -1 {
		return data, nil
	}

	endIdx := strings.Index(string(curData), "# END SHOWTIME CONFIG\n")
	if endIdx == -1 || endIdx < beginIdx {
		return data, nil
	}

	seg1 := beginIdx
	seg2 := newEnd - newBegin
	seg3 := len(curData) - endIdx

	newSize := seg1 + seg2 + seg3
	newData := make([]byte, newSize)
	copy(newData, curData[:seg1])
	copy(newData[seg1:], data[newBegin:newEnd])
	copy(newData[seg1+seg2:], curData[endIdx:])

	return newData, nil
}

// installError — C: install_error (upgrade.c:294-303): sets the
// upgrade_error prop, status "upgradeError", and TRACE_ERRORs it.
func (u *Upgrade) installError(str string, url string) {
	if u.upgradeError != nil {
		u.pm.SetStringEx(u.upgradeError, nil, str, 0)
	}
	if u.upgradeStatus != nil {
		u.pm.SetStringEx(u.upgradeStatus, nil, "upgradeError", 0)
	}
	if url != "" {
		u.ts.Error("upgrade", "Download of %s failed -- %s", url, str)
	} else {
		u.ts.Error("upgrade", "Error occured: %s", str)
	}
}

// downloadCallback is called during download progress
func (u *Upgrade) downloadCallback(opaque any, loaded int, total int) {
	a, ok := opaque.(*ArtifactFull)
	if !ok {
		return
	}

	if total == 0 {
		total = int(a.Size)
	}

	u.artifactUpdateProgress(a, float32(loaded)/float32(total))
}

// downloadFile downloads an artifact
func (u *Upgrade) downloadFile(a *ArtifactFull, tryPatch bool) error {
	if a.URL == "" {
		return nil
	}

	a.ProgressNumParts = 2
	a.ProgressPart = 0
	u.artifactUpdateProgress(a, 0)

	if u.upgradeTask != nil {
		u.pm.SetStringEx(u.upgradeTask, nil, a.Task, 0)
	}

	// C: r = http_req(a->a_url, HTTP_RESULT_PTR(&b),
	//      HTTP_ERRBUF(errbuf, sizeof(errbuf)),
	//      HTTP_FLAGS(FA_COMPRESSION),
	//      HTTP_RESPONSE_HEADERS(&response_headers),
	//      HTTP_REQUEST_HEADERS(&req_headers),
	//      HTTP_PROGRESS_CALLBACK(download_callback, a), NULL)
	//    (upgrade.c:401) — r → install_error(errbuf, a->a_url)
	errbuf := make([]byte, 1024)
	var b *misc.Buf
	var responseHeaders, reqHeaders httpnet.HTTPHeaderList
	r := u.fam.HTTPReq(a.URL,
		facore.HTTPTagResultPtr, &b,
		facore.HTTPTagErrbuf, errbuf,
		facore.HTTPTagFlags, facore.FaCompression,
		facore.HTTPTagResponseHeaders, &responseHeaders,
		facore.HTTPTagRequestHeaders, &reqHeaders,
		facore.HTTPTagProgressCallback, facore.FALoadCB(u.downloadCallback), a)

	if r != 0 {
		responseHeaders.Free()
		u.installError(string(bytes.TrimRight(errbuf, "\x00")), a.URL)
		return fmt.Errorf("http_req: %d", r)
	}

	// C: http_headers_free(&response_headers) — #if CONFIG_BSPATCH off
	//    (no bspatch in this build; C's patch block is config-gated)
	responseHeaders.Free()

	// C: sha1 over b->b_ptr..b->b_size — data aliases the buf_t;
	//    every exit below mirrors the C buf_release(b) placement.
	data := b.C8()[:b.Len()]

	hash := sha1.Sum(data)
	digestStr := hex.EncodeToString(hash[:])
	expectedDigest := hex.EncodeToString(a.Digest[:])

	if digestStr != expectedDigest {
		u.installError("SHA-1 sum mismatch", a.URL)
		b.Release()
		return fmt.Errorf("SHA-1 mismatch")
	}

	if a.CheckPartial {
		newBegin := strings.Index(string(data), "# BEGIN SHOWTIME CONFIG\n")
		newEnd := strings.Index(string(data), "# END SHOWTIME CONFIG\n")

		if newBegin != -1 && newEnd > newBegin {
			var perr error
			data, perr = patchedConfigFile(data, newBegin, newEnd, a.FinalPath)
			if perr != nil {
				b.Release()
				return perr
			}
		}
	}

	dstPath := a.TempPath
	if dstPath == "" {
		dstPath = a.FinalPath
	}

	// C: a->a_progress_part++ before the write loop (upgrade.c:506)
	a.ProgressPart++

	// C: install_error is called by the write path itself on open,
	//    write or close failure (upgrade.c:497-543).
	if err := u.writeArtifactFile(a, dstPath, data); err != nil {
		b.Release()
		return err
	}

	b.Release()
	u.artifactUpdateProgress(a, 1.0)

	return nil
}

// writeArtifactFile — C: write loop in download_file (upgrade.c:485-544).
// Opens O_CREAT|O_RDWR|O_TRUNC (| O_SYNC under STOS → mgosOpenFlags),
// writes in 64KB chunks retrying EAGAIN/EINTR/EINPROGRESS, reports
// per-chunk progress, unlinks the partial file on write/close failure.
func (u *Upgrade) writeArtifactFile(a *ArtifactFull, dstPath string, data []byte) error {
	fd, err := os.OpenFile(dstPath,
		os.O_CREATE|os.O_RDWR|os.O_TRUNC|mgosOpenFlags(), 0777)
	if err != nil {
		u.installError("Unable to open file", dstPath)
		return err
	}

	length := len(data)
	off := 0
	for length > 0 {
		toWrite := min(length, 65536)
		r, werr := fd.Write(data[off : off+toWrite])
		if werr != nil {
			if errors.Is(werr, syscall.EAGAIN) || errors.Is(werr, syscall.EINTR) ||
				errors.Is(werr, syscall.EINPROGRESS) {
				continue
			}
			u.installError(fmt.Sprintf("Write(%d) failed: %s", toWrite, werr), dstPath)
			fd.Close()
			os.Remove(dstPath)
			return werr
		}
		length -= r
		off += r
		u.artifactUpdateProgress(a,
			float32(len(data)-length)/float32(len(data)))
	}

	if cerr := fd.Close(); cerr != nil {
		u.installError(fmt.Sprintf("Close failed: %s", cerr), dstPath)
		os.Remove(dstPath)
		return cerr
	}
	return nil
}

// checkUpgradeErr sets check upgrade error
func (u *Upgrade) checkUpgradeErr(msg string) {
	if u.upgradeError != nil {
		u.pm.SetStringEx(u.upgradeError, nil, msg, 0)
	}
	if u.upgradeStatus != nil {
		u.pm.SetStringEx(u.upgradeStatus, nil, "checkError", 0)
	}
}

// checkUpgrade checks for available upgrades
func (u *Upgrade) checkUpgrade(setNews bool) error {
	if u.inhibitChecks {
		return nil
	}

	if u.upgradeTrack == "" {
		u.checkUpgradeErr("No release track specified")
		return nil
	}

	if u.upgradeStatus != nil {
		u.pm.SetStringEx(u.upgradeStatus, nil, "checking", 0)
	}

	url := fmt.Sprintf("%s/%s-%s.json", u.ctrlBase, u.upgradeTrack, u.archName)

	// C: b = fa_load(url, FA_LOAD_ERRBUF(errbuf, sizeof(errbuf)),
	//      FA_LOAD_FLAGS(FA_DISABLE_AUTH | FA_COMPRESSION), NULL)
	//    (upgrade.c:671) — NULL → check_upgrade_err(errbuf)
	b, lerr := facore.FALoad2(u.fam, url,
		&facore.FALoadArgs{
			Flags: facore.FaDisableAuth | facore.FaCompression,
		})
	if lerr != nil {
		u.checkUpgradeErr(lerr.Error())
		return lerr
	}
	data := b.Data[:b.Size]

	var manifest UpgradeManifest
	err := json.Unmarshal(data, &manifest)
	if err != nil {
		u.checkUpgradeErr("Malformed JSON in repository")
		return nil
	}

	// C: #if STOS (upgrade.c:689-720) — stos_upgrade_needed starts from
	//    gconf.enable_omnigrade; manifest.stosVersion may force an OS
	//    upgrade; failure takes `goto err` → status "checkError".
	if u.mgosGate(&manifest) {
		if u.upgradeStatus != nil {
			u.pm.SetStringEx(u.upgradeStatus, nil, "checkError", 0)
		}
		return nil
	}

	var dlURL, sha1, name string
	var dlSize int

	for _, artifact := range manifest.Artifacts {
		if artifact.Type == u.artifactType {
			dlURL = artifact.URL
			sha1 = artifact.SHA1
			name = artifact.Name
			dlSize = artifact.Size
			break
		}
	}

	if dlURL == "" || dlSize == 0 || sha1 == "" || manifest.Version == "" {
		u.checkUpgradeErr("No URL or size present")
		return nil
	}

	// C: hex2bin(app_download_digest, sizeof, sha1) — return value is
	//    ignored upstream; a malformed digest just fails the SHA-1
	//    comparison at download time. hex.Decode decodes all complete
	//    pairs up to the first invalid nibble, like hex2binl.
	hex.Decode(u.appDownloadDigest[:], []byte(sha1))

	u.appDownloadURL = dlURL
	u.appDownloadName = name
	u.appDownloadSize = dlSize

	if u.upgradeRoot != nil {
		u.pm.SetVEx(nil, u.upgradeRoot, "track", u.upgradeTrack)
		u.pm.SetVEx(nil, u.upgradeRoot, "availableVersion", manifest.Version)
	}

	// C: int canUpgrade = gconf.enable_omnigrade;
	//    if(ver != NULL && parse_version_int(ver) > app_get_version_int())
	//      canUpgrade = 1;   (upgrade.c:771-776)
	canUpgrade := *u.deps.EnableOmnigrade != 0
	if manifest.Version != "" {
		if version.ParseVersionInt(manifest.Version) > version.AppGetVersionInt() {
			canUpgrade = true
		}
	}

	// C: prop_set_string(upgrade_status, canUpgrade ? "canUpgrade" : "upToDate")
	if u.upgradeStatus != nil {
		if canUpgrade {
			u.pm.SetStringEx(u.upgradeStatus, nil, "canUpgrade", 0)
		} else {
			u.pm.SetStringEx(u.upgradeStatus, nil, "upToDate", 0)
		}
	}

	// C: prop_destroy(news_ref); prop_ref_dec(news_ref); news_ref = NULL
	if u.newsRef != nil {
		u.pm.Destroy(u.newsRef)
		u.newsRef.Release()
		u.newsRef = nil
	}

	// C: if(set_news && canUpgrade)
	//    news_ref = add_news(buf, buf, "showtime:upgrade", "Open download page")
	if setNews && canUpgrade {
		if nm := u.deps.NM; nm != nil {
			title := fmt.Sprintf("%s version %s is available",
				app.AppNameUser, manifest.Version)
			u.newsRef = nm.AddNews(title, title, "showtime:upgrade",
				"Open download page")
		}
	}

	// C: changelog is updated LAST in check_upgrade (upgrade.c:800-830)
	//    — after status set and news_ref handling.
	if u.upgradeRoot != nil {
		changelog := u.pm.CreateEx(u.upgradeRoot, "changelog", nil, false, false)
		u.pm.DestroyChilds(changelog)

		for _, entry := range manifest.Changelog {
			q := u.pm.CreateRootEx("", false)
			u.pm.SetVEx(nil, q, "version", entry.Version)
			u.pm.SetVEx(nil, q, "text", entry.Desc)
			changelog.AddChild(q)
		}
	}

	return nil
}

// appAddArtifact adds app artifact to queue
func (u *Upgrade) appAddArtifact(aq *ArtifactQueue) {
	// C: a->a_name = strdup(app_download_name ?: APPNAME)
	name := u.appDownloadName
	if name == "" {
		name = app.AppName
	}
	a := &ArtifactFull{
		Name: name,
		// C: a->a_task = rstr_alloc(APPNAMEUSER)
		Task: app.AppNameUser,
		URL:  u.appDownloadURL,
		Size: int64(u.appDownloadSize),
		// C: a->a_final_path = strdup(gconf.upgrade_path ?: gconf.binary)
		// (upgrade.c:845)
		FinalPath: u.upgradeTarget(),
	}
	copy(a.Digest[:], u.appDownloadDigest[:])

	// C: #if STOS — a->a_temp_path = "<final>.tmp" (upgrade.c:847-851)
	mgosSetTempPath(a)

	if aq.Tail == nil {
		aq.Head = a
		aq.Tail = a
	} else {
		aq.Tail.Link = a
		aq.Tail = a
	}
}

// moveFilesIntoPlace moves downloaded files to final location
func (u *Upgrade) moveFilesIntoPlace(aq *ArtifactQueue) {
	// C: #if STOS — sync() before renaming (upgrade.c:1004-1008)
	u.mgosSyncFS()

	for a := aq.Head; a != nil; a = a.Link {
		if a.TempPath == "" || a.URL == "" {
			continue
		}

		err := os.Rename(a.TempPath, a.FinalPath)
		if err != nil {
			u.ts.Error("upgrade", "Rename failed: %v\n", err)
		}
	}

	// C: #if STOS — sync() after renaming (upgrade.c:1027-1031)
	u.mgosSyncFS()
}

// deleteUnusedFiles deletes files that are no longer needed
func (u *Upgrade) deleteUnusedFiles(aq *ArtifactQueue) {
	for a := aq.Head; a != nil; a = a.Link {
		if a.URL != "" {
			continue
		}

		err := os.Remove(a.FinalPath)
		if err != nil && !os.IsNotExist(err) {
			u.ts.Error("upgrade", "Delete failed: %v\n", err)
		}
	}
}

// printSummary prints summary of what will be done
func (u *Upgrade) printSummary(aq *ArtifactQueue) {
	for a := aq.Head; a != nil; a = a.Link {
		if a.URL == "" {
			u.ts.Debug("upgrade", "File %s will be deleted\n", a.FinalPath)
		} else {
			u.ts.Info("upgrade", "File %s will be downloaded from %s (%s)\n", a.FinalPath, a.URL, a.Name)
		}
	}
}

// installLocked performs the actual installation
func (u *Upgrade) installLocked(aq *ArtifactQueue) {
	if u.appDownloadURL == "" {
		return
	}

	// C: #if STOS (upgrade.c:1087-1108) — remount /boot rw, mkdir+clean
	//    /boot/dl, queue OS artifacts when stos_upgrade_needed.
	if u.mgosInstallPrepare(aq) {
		return
	}

	// C: usage_event("Upgrade", 1, USAGE_SEG("arch", archname,
	//    "track", upgrade_track)) (upgrade.c:1110-1112)
	u.deps.Usage.Event("Upgrade", 1, "arch", u.archName, "track", u.upgradeTrack)

	u.appAddArtifact(aq)
	u.printSummary(aq)
	u.deleteUnusedFiles(aq)
	artifactsComputeProgressbarScale(aq)

	if u.upgradeStatus != nil {
		u.pm.SetStringEx(u.upgradeStatus, nil, "download", 0)
	}

	for a := aq.Head; a != nil; a = a.Link {
		if u.downloadFile(a, false) != nil {
			return
		}
	}

	u.moveFilesIntoPlace(aq)

	u.ts.Info("upgrade", "All done, restarting")
	// C: sleep(1)
	time.Sleep(time.Second)

	// C: app_shutdown(stos_upgrade_needed ? APP_EXIT_REBOOT
	//    : APP_EXIT_RESTART) (upgrade.c:1146-1150)
	if u.deps.Shutdown != nil {
		u.deps.Shutdown(u.mgosExitCode())
	}
}

// installThread runs installation in background
func (u *Upgrade) installThread(aq *ArtifactQueue) {
	u.mutex.Lock()
	u.installLocked(aq)
	artifactsFree(aq)
	u.mutex.Unlock()
}

// install starts the installation
func (u *Upgrade) install() {
	var aq ArtifactQueue
	go u.installThread(&aq)
}

// upgradeCallback — C: upgrade_cb (upgrade.c:~1240). Called with
// upgrade_mutex held (C: PROP_TAG_MUTEX). Handles PROP_EXT_EVENT
// carrying EVENT_DYNAMIC_ACTION payloads on global.upgrade.eventSink.
func (u *Upgrade) upgradeCallback(opaque any, eventType propcore.EventType, args ...any) {
	if eventType != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	// C: event_is_type(e, EVENT_DYNAMIC_ACTION) →
	//    ((event_payload_t*)e)->payload
	var payload string
	if ep := event.BaseEvent(args[0]); ep != nil &&
		ep.Type == event.EVENT_DYNAMIC_ACTION {
		payload = ep.Payload
	}
	switch payload {
	case "checkUpdates":
		u.checkUpgrade(false)
	case "install":
		u.install()
	}
}

// setUpgradeTrack sets the upgrade track
func (u *Upgrade) setUpgradeTrack(opaque any, str string) {
	u.upgradeTrack = str
	u.checkUpgrade(false)
}

// UpgradeStart initializes the upgrade system with full prop integration.
// C: upgrade_init (upgrade.c:1243-1332) — takes no lock; the mutex only
// serializes check_upgrade/install via SETTING_MUTEX/PROP_TAG_MUTEX.
func (u *Upgrade) UpgradeStart() {
	// C: upgrade_init gate — fname = upgrade_path ?: binary;
	// if(fname == NULL) return (upgrade.c:1252-1254)
	if u.upgradeTarget() == "" {
		return
	}

	// C: #if STOS (upgrade.c:1257-1261) — stos_get_current_version(),
	//    artifact_type = "sqfs", archname = PLATFORM
	u.mgosStartUpgrade()

	// C: if(artifact_type == NULL || archname == NULL) return
	//    (upgrade.c:1275) — on plain Linux both stay unset upstream:
	//    the whole upgrade subsystem is inert there (no props,
	//    no settings node, no checks).
	if u.artifactType == "" || u.archName == "" {
		return
	}

	global := u.pm.GetGlobal()
	if global == nil {
		return
	}

	u.upgradeRoot = u.pm.CreateEx(global, "upgrade", nil, false, false)
	u.upgradeStatus = u.pm.CreateEx(u.upgradeRoot, "status", nil, false, false)
	u.upgradeProgress = u.pm.CreateEx(u.upgradeRoot, "progress", nil, false, false)
	u.upgradeError = u.pm.CreateEx(u.upgradeRoot, "error", nil, false, false)
	u.upgradeTask = u.pm.CreateEx(u.upgradeRoot, "task", nil, false, false)

	if u.upgradeStatus != nil {
		u.pm.SetStringEx(u.upgradeStatus, nil, "upToDate", 0)
	}

	upgradeDir := u.settingsMgr.SettingGetDir("general:upgrade")
	if upgradeDir == nil {
		return
	}

	// C: setting_create(SETTING_MULTIOPT, dir, SETTINGS_INITIAL_UPDATE,
	//   SETTING_TITLE(_p("Upgrade to releases from")),
	//   SETTING_STORE("upgrade", "track-5-0"),
	//   SETTING_OPTION("stable", ...), SETTING_OPTION("testing", ...),
	//   SETTING_OPTION_CSTR("master", ...),
	//   SETTING_CALLBACK(set_upgrade_track, NULL), ...)  (upgrade.c:1290)
	u.settingsMgr.SettingCreate(settings.SettingMultiOpt, upgradeDir, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, u.settingsMgr.P("Upgrade to releases from"),
		settings.SettingTagStore, "upgrade", "track-5-0",
		settings.SettingTagOption, "stable", u.settingsMgr.P("Stable"),
		settings.SettingTagOption, "testing", u.settingsMgr.P("Testing"),
		settings.SettingTagOptionCStr, "master", "Bleeding Edge (Very unstable)",
		settings.SettingTagCallback, u.setUpgradeTrack, nil,
		settings.SettingTagMutex, &u.mutex,
		0)

	u.settingsMgr.SettingCreate(settings.SettingInt, upgradeDir, settings.SettingsInitialUpdate,
		settings.SettingTagTitle, u.settingsMgr.P("Notify about upgrades"),
		settings.SettingTagValue, 1,
		settings.SettingTagStore, "upgrade", "check",
		settings.SettingTagWriteInt, &u.notifyUpgrades,
		settings.SettingTagMutex, &u.mutex,
		0)

	// C: prop_t *p = prop_create_root(NULL);
	//     prop_setv(p, "metadata", "title", NULL, PROP_SET_LINK,
	//               _p("Check for updates now"));
	//     prop_set(p, "type", PROP_SET_STRING, "movian");
	//     prop_set(p, "url",  PROP_SET_STRING, "showtime:upgrade");
	//     prop_set_parent(p, prop_create(dir, "nodes"))
	p := u.pm.CreateRootEx("", false)
	meta := u.pm.CreateEx(p, "metadata", nil, false, false)
	u.pm.SetStringEx(u.pm.CreateEx(meta, "title", nil, false, false), nil,
		"Check for updates now", propcore.StringUTF8)
	u.pm.SetStringEx(u.pm.CreateEx(p, "type", nil, false, false), nil,
		"movian", propcore.StringUTF8)
	u.pm.SetStringEx(u.pm.CreateEx(p, "url", nil, false, false), nil,
		"showtime:upgrade", propcore.StringUTF8)
	dirNodes := u.pm.CreateEx(upgradeDir, "nodes", nil, false, false)
	if u.pm.SetParentEx(p, dirNodes, nil, "") != 0 {
		// C: if(prop_set_parent(...)) abort()
		panic("upgrade: prop_set_parent on settings dir nodes failed")
	}

	u.inhibitChecks = false
}

// UpgradeSubscribe — C: prop_subscribe(0,
//
//	PROP_TAG_MUTEX, &upgrade_mutex,
//	PROP_TAG_CALLBACK, upgrade_cb, NULL,
//	PROP_TAG_NAME("global", "upgrade", "eventSink"), NULL)
//
// (upgrade.c:~1330). Runs after UpgradeStart's locked section because Go's
// sync.Mutex is not recursive like C's hts_mutex.
func (u *Upgrade) UpgradeSubscribe() {
	u.mutex.Lock()
	root := u.upgradeRoot
	u.mutex.Unlock()
	if root == nil {
		return
	}
	es := u.pm.CreateEx(root, "eventSink", nil, false, false)
	if es == nil {
		return
	}
	es.Subscribe(func(opaque any, eventType propcore.EventType, args ...any) {
		u.mutex.Lock()
		defer u.mutex.Unlock()
		u.upgradeCallback(opaque, eventType, args...)
	}, nil)
}

// UpgradeRefresh refreshes upgrade check
func (u *Upgrade) UpgradeRefresh() error {
	u.mutex.Lock()
	defer u.mutex.Unlock()
	return u.checkUpgrade(u.notifyUpgrades != 0)
}

// UpgradeGetTrack returns current upgrade track
func (u *Upgrade) UpgradeGetTrack() string {
	u.mutex.Lock()
	defer u.mutex.Unlock()
	return u.upgradeTrack
}

// Exit codes — C: main.h:193-195
const (
	AppExitRestart = 13 // C: APP_EXIT_RESTART
	AppExitReboot  = 15 // C: APP_EXIT_REBOOT
)

// UpgradeDeps — external seams the C code resolves through globals:
// app_shutdown (main.c), usage_event (usage.c), gconf.* (main.c).
// Explicit injection — no package-level mutable state.
type UpgradeDeps struct {
	Shutdown func(int)                          // C: app_shutdown(retcode)
	Usage    *usage.Reporter                    // C: usage_event() global (usage.c)
	NM       *notifications.NotificationManager // C: notify_add global
	FAM      *facore.FileAccessManager          // C: implicit global fa context
	TS       *trace.TraceSystem                 // C: trace() global

	// C: gconf.enable_bin_replace / gconf.enable_omnigrade
	// (main.h:234-235) — pointers into shared storage owned by the
	// composition root; the "binreplace"/"omnigrade" dev bools write
	// through the same pointers (settings.c init_dev_settings).
	EnableBinReplace *int
	EnableOmnigrade  *int

	UpgradePath string // C: gconf.upgrade_path (--upgrade-path)
	Binary      string // C: gconf.binary (argv[0], linux_main.c:145)
}

// upgradeTarget — C: gconf.upgrade_path ?: gconf.binary
// (upgrade.c:845,1252).
func (u *Upgrade) upgradeTarget() string {
	if u.deps.UpgradePath != "" {
		return u.deps.UpgradePath
	}
	return u.deps.Binary
}
