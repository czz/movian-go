// Package ui — canonical port of src/ui/.
//
// clipboard.go — C: src/ui/clipboard.c (1:1).
//
// In-memory clipboard (a file/URL string) with three prop event
// subscriptions under $global.clipboard:
//
//	setFromItem  — copy an item's URL into the clipboard
//	copyItem     — copy an item to a user-picked destination directory
//	pasteToModel — copy the clipboard contents into a target model dir
//
// A "contents" status prop ("string"|"path"|null) is maintained
// asynchronously via task_run, and copy progress is reported through
// $global.clipboard.copyprogress.
package ui

import (
	"cmp"
	"fmt"
	"slices"
	"sync"

	"github.com/czz/movian-go/internal/nls"

	"github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/trace"
)

// Clipboard — C: static clipboard_mutex + clipboard content + the
// dependencies C resolves via globals (prop_get_global, task_run,
// fa_* defaults, filepicker_pick, notify_add, _(), gconf.clipboard_*).
// Owned by the app context; the glw text widget reaches it via glwDeps.
type Clipboard struct {
	mu      sync.Mutex
	content *string // rstr_t — nil models NULL

	depsMu sync.RWMutex
	pm     *propcore.PropManager
	em     *event.EventManager
	fam    *facore.FileAccessManager
	nm     *notifications.NotificationManager // C: notify_add global
	tasks  *task.TaskSystem                   // C: task_run globals
	ts     *trace.TraceSystem                 // C: trace() global

	// gettext — C: _() (nls_gettext) for popup strings.
	gettext func(string) string

	gconfSet func(string)
	gconfGet func() *string
}

// clipGettext — C: _() (nls_gettext); identity until ClipboardStart.
func (clip *Clipboard) clipGettext(s string) string {
	clip.depsMu.RLock()
	defer clip.depsMu.RUnlock()
	if clip.gettext != nil {
		return clip.gettext(s)
	}
	return s
}

// SetGconfHooks installs the platform clipboard hooks
// (C: gconf.clipboard_set / gconf.clipboard_get; the GLFW layer is not
// ported — nil selects the internal buffer).
func (clip *Clipboard) SetGconfHooks(set func(string), get func() *string) {
	clip.depsMu.Lock()
	clip.gconfSet = set
	clip.gconfGet = get
	clip.depsMu.Unlock()
}

// clipPropGetString — C: prop_get_string(p, "url") → rstr_t*.
// Returns nil when the child is absent or holds a non-string value,
// matching rstr_t NULL semantics.
func (clip *Clipboard) clipPropGetString(p *propcore.Prop, name string) *string {
	clip.depsMu.RLock()
	pm := clip.pm
	clip.depsMu.RUnlock()
	if p == nil || pm == nil {
		return nil
	}
	c := p.GetChild(name)
	if c == nil {
		return nil
	}
	if s, ok := pm.GetValue(c).(string); ok {
		return &s
	}
	return nil
}

// clipboardSetFromItem — C: clipboard_setFromItem
// (PROP_TAG_CALLBACK_EVENT on $global.clipboard.setFromItem).
func (clip *Clipboard) clipboardSetFromItem(_ any, eventType propcore.EventType, args ...any) {
	e := clip.clipExtEventPropRef(eventType, args)
	if e == nil {
		return
	}
	p, _ := e.P.(*propcore.Prop)
	url := clip.clipPropGetString(p, "url")
	clip.depsMu.RLock()
	gset := clip.gconfSet
	clip.depsMu.RUnlock()
	if gset != nil {
		s := ""
		if url != nil {
			s = *url
		}
		gset(s)
	} else {
		clip.mu.Lock()
		clip.content = url
		clip.mu.Unlock()
	}
	// C: rstr_release(url) — Go GC
	clip.ValidateContents()
}

// clipboardCopyJob — C: clipboard_copy_job_t
type clipboardCopyJob struct {
	totalBytes int64 // C: total_bytes
	completed  int64 // C: completed
	totalFiles int   // C: total_files
	prop       *propcore.Prop

	totalProp     *propcore.Prop // C: total_prop
	completedProp *propcore.Prop // C: completed_prop
	filesProp     *propcore.Prop // C: files_prop

	errbuf string // C: errbuf[256]
}

// fail sets the job errbuf, mirroring snprintf(j->errbuf, sizeof(j->errbuf), ...).
func (j *clipboardCopyJob) fail(format string, args ...any) {
	j.errbuf = fmt.Sprintf(format, args...)
	if len(j.errbuf) > 255 {
		j.errbuf = j.errbuf[:255]
	}
}

// clipboardCopyFile — C: clipboard_copy_file
func (clip *Clipboard) clipboardCopyFile(src, dst string, j *clipboardCopyJob) int {
	clip.depsMu.RLock()
	fam := clip.fam
	clip.depsMu.RUnlock()
	sfh, err := facore.OpenEx(fam, src, nil, 0)
	if sfh == nil || err != nil {
		if err != nil {
			j.errbuf = err.Error()
		}
		return 1
	}

	filename := clip.fam.FAURLGetLastComponent(src)
	dstpath := facore.FAPathjoin(dst, filename)

	dfh, err := facore.OpenEx(fam, dstpath, &facore.OpenExtra{Flags: facore.FaWrite}, 0)
	if dfh == nil || err != nil {
		if err != nil {
			j.errbuf = err.Error()
		}
		facore.FAClose(sfh)
		return 1
	}

	const bufsize = 32768
	buf := make([]byte, bufsize)
	rcode := 0
	for {
		r, rerr := facore.FARead(sfh, buf)
		if r <= 0 {
			if rerr != nil {
				// C: fa_read <= 0 ends the loop; error detail is not
				// propagated into errbuf by the C code either.
				break
			}
			break
		}
		w, werr := facore.FAWrite(dfh, buf[:r])
		if werr != nil || w != r {
			rcode = 1
			j.fail("Write failure")
			break
		}
		j.completed += int64(r)
		j.completedProp.SetFloat(float32(j.completed))
	}

	facore.FAClose(dfh)
	facore.FAClose(sfh)
	if rcode != 0 {
		_ = facore.FAUnlink(fam, dstpath)
	}
	return rcode
}

// clipboardCopyFiles0 — C: clipboard_copy_files0
//
// Two-phase recursive copy: phase 1 (dst == "") counts total bytes/files
// into the progress props; phase 2 performs the copy.
func (clip *Clipboard) clipboardCopyFiles0(src, dst string, j *clipboardCopyJob) int {
	clip.depsMu.RLock()
	fam := clip.fam
	clip.depsMu.RUnlock()

	st, err := facore.Stat(fam, src)
	if err != nil {
		j.errbuf = err.Error()
		return 1
	}

	if st.Type == facore.ContentFile {
		if dst == "" {
			j.totalBytes += st.Size
			j.totalFiles++
			j.totalProp.SetFloat(float32(j.totalBytes))
			j.filesProp.SetInt(j.totalFiles)
			return 0
		}
		return clip.clipboardCopyFile(src, dst, j)
	}

	if dst != "" {
		filename := clip.fam.FAURLGetLastComponent(src)
		dstpath := facore.FAPathjoin(dst, filename)
		dst = dstpath
		if err := facore.FAMakedirs(fam, dst); err != nil {
			return 1
		}
	}

	fd, serr := facore.FAScandir(fam, src, 0)
	if serr != nil {
		j.errbuf = serr.Error()
		return 1
	}
	rcode := 0
	// C: RB_FOREACH over fd->fd_entries — an ordered tree sorted by fde_url
	// (fa_dir_cmp1); Go's Dir.Entries is a map, so sort by entry URL.
	fdes := make([]*facore.DirEntry, 0, len(fd.Entries))
	for _, fde := range fd.Entries {
		fdes = append(fdes, fde)
	}
	slices.SortFunc(fdes, func(a, b *facore.DirEntry) int { return cmp.Compare(a.URL, b.URL) })
	for _, fde := range fdes {
		if err := facore.DirEntryStat(fam, fde); err != nil {
			continue
		}
		if fde.Stat.Type == facore.ContentFile {
			if dst == "" {
				j.totalBytes += fde.Stat.Size
				j.totalProp.SetFloat(float32(j.totalBytes))
				j.totalFiles++
				j.filesProp.SetInt(j.totalFiles)
			} else {
				if clip.clipboardCopyFiles0(fde.URL, dst, j) != 0 {
					rcode = 1
					break
				}
			}
		} else {
			if clip.clipboardCopyFiles0(fde.URL, dst, j) != 0 {
				rcode = 1
				break
			}
		}
	}

	facore.DirFree(fd)
	return rcode
}

// clipboardCopyFiles — C: clipboard_copy_files
func (clip *Clipboard) clipboardCopyFiles(src, dst string) {
	j := &clipboardCopyJob{}
	clip.ts.Trace(trace.TRACE_DEBUG, "COPY", "Copy from %s to directory %s", src, dst)

	// C: prop_create(prop_create(prop_get_global(), "clipboard"),
	//   "copyprogress") — prop_create does NOT deduplicate; each copy
	// creates a fresh "clipboard"/"copyprogress" node pair.
	clip.depsMu.RLock()
	pm := clip.pm
	clip.depsMu.RUnlock()
	global := pm.GetGlobal()
	clipboardNode := pm.CreateEx(global, "clipboard", nil, false, false)
	p := pm.CreateEx(clipboardNode, "copyprogress", nil, false, false)

	n := pm.CreateRoot("")
	if pm.SetParentEx(n, p, nil, "") != 0 {
		// C: if(prop_set_parent(n, p)) abort();
		panic("clipboard: prop_set_parent failed")
	}
	j.totalProp = pm.CreateEx(n, "total", nil, false, false)
	j.completedProp = pm.CreateEx(n, "completed", nil, false, false)
	j.filesProp = pm.CreateEx(n, "files", nil, false, false)

	if clip.clipboardCopyFiles0(src, "", j) != 0 {
		if clip.nm != nil {
			clip.nm.NotifyAdd(nil, notifications.NotifyError, "", 5,
				clip.clipGettext("Copy failed: %s"), j.errbuf)
		}
		return
	}
	clip.ts.Trace(trace.TRACE_DEBUG, "COPY", "Copy from %s total %d bytes",
		src, j.totalBytes)

	if clip.clipboardCopyFiles0(src, dst, j) != 0 {
		if clip.nm != nil {
			clip.nm.NotifyAdd(nil, notifications.NotifyError, "", 5,
				clip.clipGettext("Copy failed: %s"), j.errbuf)
		}
	}
	pm.Destroy(n)
}

// clipboardPasteToModel — C: clipboard_pasteToModel
// (PROP_TAG_CALLBACK_EVENT on $global.clipboard.pasteToModel).
func (clip *Clipboard) clipboardPasteToModel(_ any, eventType propcore.EventType, args ...any) {
	e := clip.clipExtEventPropRef(eventType, args)
	if e == nil {
		return
	}
	clip.depsMu.RLock()
	pm, em := clip.pm, clip.em
	clip.depsMu.RUnlock()
	p, _ := e.P.(*propcore.Prop)
	dst := clip.clipPropGetString(p, "url")
	src := clip.Get()
	var srcS, dstS string
	if src != nil {
		srcS = *src
	}
	if dst != nil {
		dstS = *dst
	}
	clip.clipboardCopyFiles(srcS, dstS)
	// C: rstr_release(src); rstr_release(dst)

	r := em.CreateAction(event.ACTION_RELOAD_DATA)
	es := pm.CreateEx(p, "eventSink", nil, false, false)
	pm.RefInc(es) // C: prop_create_r incref
	pm.SendExtEvent(es, r.AsEvent())
	r.AsEvent().Release()
	pm.RefDec(es)
}

// clip.clipboardValidateTask — C: clipboard_validate_task (task_run worker).
func (clip *Clipboard) clipboardValidateTask(_ any) {
	clip.depsMu.RLock()
	pm, fam := clip.pm, clip.fam
	clip.depsMu.RUnlock()
	path := clip.Get()
	var contents *string
	if path != nil {
		s := "string"
		contents = &s
		if _, err := facore.Stat(fam, *path); err == nil {
			p := "path"
			contents = &p
		}
	}
	// C: prop_setv(prop_get_global(), "clipboard", "contents", NULL,
	//   PROP_SET_STRING, contents) — walk-create the path, then set.
	global := pm.GetGlobal()
	clipboardNode := global.GetChild("clipboard")
	if clipboardNode == nil {
		clipboardNode = pm.CreateEx(global, "clipboard", nil, false, false)
	}
	if contents != nil {
		pm.SetVEx(nil, clipboardNode, "contents", *contents)
	} else {
		c := clipboardNode.GetChild("contents")
		if c == nil {
			c = pm.CreateEx(clipboardNode, "contents", nil, false, false)
		}
		pm.SetVoidEx(c, nil)
	}
	// C: rstr_release(path)
}

// ClipboardValidateContents — C: clipboard_validate_contents
func (clip *Clipboard) ValidateContents() {
	clip.depsMu.RLock()
	ts := clip.tasks
	clip.depsMu.RUnlock()
	if ts != nil {
		ts.Run(clip.clipboardValidateTask, nil)
	}
}

// ClipboardGet — C: clipboard_get → rstr_t* (nil models NULL).
func (clip *Clipboard) Get() *string {
	clip.depsMu.RLock()
	gget := clip.gconfGet
	clip.depsMu.RUnlock()
	if gget != nil {
		return gget()
	}
	clip.mu.Lock()
	defer clip.mu.Unlock()
	return clip.content // C: rstr_dup(clipboard_content)
}

// clipboardCopyItem — C: clipboard_copyItem
// (PROP_TAG_CALLBACK_EVENT on $global.clipboard.copyItem).
func (clip *Clipboard) clipboardCopyItem(_ any, eventType propcore.EventType, args ...any) {
	e := clip.clipExtEventPropRef(eventType, args)
	if e == nil {
		return
	}
	p, _ := e.P.(*propcore.Prop)
	src := clip.clipPropGetString(p, "url")

	title := clip.clipGettext("Target folder")
	clip.depsMu.RLock()
	fam := clip.fam
	clip.depsMu.RUnlock()
	dst := facore.FilepickerPick(fam, title, facore.FilepickerDirectories)
	// C: rstr_release(title)
	if dst != "" {
		s := ""
		if src != nil {
			s = *src
		}
		clip.clipboardCopyFiles(s, dst)
	}
	// C: rstr_release(src)
}

// clipExtEventPropRef extracts the EVENT_PROPREF event from a
// PROP_TAG_CALLBACK_EVENT delivery — C: event_is_type(e, EVENT_PROPREF).
// The ext event arrives as args[0] (see notifySub EventExtEvent packing).
func (clip *Clipboard) clipExtEventPropRef(eventType propcore.EventType, args []any) *event.EventProp {
	if eventType != propcore.EventExtEvent || len(args) == 0 {
		return nil
	}
	ep, ok := event.ConcreteOf(args[0]).(*event.EventProp)
	if !ok || ep.Type != event.EVENT_PROPREF {
		return nil
	}
	return ep
}

// ClipboardStart — C: clipboard_init (INITME INIT_GROUP_IPC, prio 10).
//
// Registers the three PROP_TAG_NAME path subscriptions under
// $global.clipboard and validates the initial contents.
// NewClipboard — C: clipboard_init (INIT_GROUP_IPC). Wires the dynamic
// path subscriptions and returns the clipboard instance.
func NewClipboard(pm *propcore.PropManager, em *event.EventManager, fam *facore.FileAccessManager, nm *notifications.NotificationManager, ts *task.TaskSystem, tr *trace.TraceSystem) *Clipboard {
	clip := &Clipboard{}
	clip.depsMu.Lock()
	clip.gettext = nls.GetRString
	clip.depsMu.Unlock()

	clip.depsMu.Lock()
	clip.pm = pm
	clip.em = em
	clip.fam = fam
	clip.nm = nm
	clip.tasks = ts
	clip.ts = tr
	clip.depsMu.Unlock()

	global := pm.GetGlobal()
	// C: prop_subscribe(0, PROP_TAG_CALLBACK_EVENT, fn, NULL,
	//   PROP_TAG_NAME("global", "clipboard", ...), NULL) — dynamic path
	// subscriptions that attach when the leaf prop is created.
	clip.clipWatch(global, []string{"clipboard", "setFromItem"}, clip.clipboardSetFromItem)
	clip.clipWatch(global, []string{"clipboard", "copyItem"}, clip.clipboardCopyItem)
	clip.clipWatch(global, []string{"clipboard", "pasteToModel"}, clip.clipboardPasteToModel)

	clip.ValidateContents()
	return clip
}

// clipWatch installs a dynamic path subscription whose leaf receives the
// PROP_TAG_CALLBACK_EVENT-style callback. The watcher lives for the
// process lifetime, matching C's named subscriptions (never unsubscribed).
func (clip *Clipboard) clipWatch(root *propcore.Prop, path []string,
	cb func(any, propcore.EventType, ...any)) {
	facore.WatchChildPath(root, path, func(leaf *propcore.Prop) *propcore.Subscription {
		return leaf.Subscribe(cb, nil)
	})
}
