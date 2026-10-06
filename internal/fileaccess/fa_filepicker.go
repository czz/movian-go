package fileaccess

// Canonical port of src/fileaccess/fa_filepicker.c — a modal directory
// picker driven by a prop tree ("pages" stack of directory models) and a
// waitable courier event loop. UI frontends drive it by sending
// EVENT_OPENURL / ACTION_NAV_BACK ext events to the picker's eventSink.

import (
	"slices"

	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
)

const (
	FilepickerFiles       = 0x1 // C: FILEPICKER_FILES
	FilepickerDirectories = 0x2 // C: FILEPICKER_DIRECTORIES
)

// filepickerDir — C: filepicker_dir_t
type filepickerDir struct {
	root *propcore.Prop // C: fd_root
}

// Filepicker — C: filepicker_t
type Filepicker struct {
	path     string // C: fp_path (hasPath == fp_path != NULL)
	hasPath  bool
	pages    *propcore.Prop   // C: fp_pages
	dirstack []*filepickerDir // C: fp_dirstack (LIST head = index 0)
	run      bool             // C: fp_run
	flags    int              // C: fp_flags
	fam      *FileAccessManager
	pm       *propcore.PropManager
}

// fpIsDir — C: isdir (CONTENT_DIR | CONTENT_SHARE | CONTENT_ARCHIVE)
func fpIsDir(t int) bool {
	return t == ContentDir || t == ContentShare || t == ContentArchive
}

// popdir — C: popdir
func (fp *Filepicker) popdir() {
	fd := fp.dirstack[0]
	fp.dirstack = fp.dirstack[1:]
	fp.pm.Destroy(fd.root)

	if len(fp.dirstack) > 0 {
		fp.pm.SelectChildProp(fp.dirstack[0].root, nil)
	} else {
		fp.run = false
	}
}

// fpScandir — C: filepicker_scandir
func (fp *Filepicker) fpScandir(path string, model *propcore.Prop, flags int) {
	dir, err := FAScanDir(fp.fam, path)
	if err != nil {
		// C: prop_set(model, "error", PROP_SET_STRING, errbuf)
		fp.pm.SetStringEx(model, nil, err.Error(), propcore.PropStrUTF8)
		return
	}
	nodes := fp.pm.CreateEx(model, "nodes", nil, false, true)

	// C: RB_FOREACH over fd_entries — sorted by filename
	names := make([]string, 0, dir.Count)
	for name := range dir.Entries {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		fde := dir.Entries[name]

		if !fde.StatDone {
			// C: fa_stat_ex(url, &fde_stat, NULL, 0, FA_NON_INTERACTIVE)
			if st, serr := Stat(fp.fam, fde.URL); serr == nil && st != nil {
				fde.Stat = *st
			}
		}

		if !fpIsDir(fde.Type) && flags&FilepickerFiles == 0 {
			continue
		}

		node := fp.pm.CreateRootEx("", false)
		// C: prop_set(node, "url", PROP_SET_RSTRING, fde->fde_url)
		fp.pm.SetStringEx(fp.pm.CreateEx(node, "url", nil, false, true), nil,
			fde.URL, propcore.PropStrUTF8)
		fp.pm.SetStringEx(fp.pm.CreateEx(node, "name", nil, false, true), nil,
			fde.Filename, propcore.PropStrUTF8)
		fp.pm.SetStringEx(fp.pm.CreateEx(node, "type", nil, false, true), nil,
			contentTypeString(fde.Type), propcore.PropStrUTF8)
		dirFlag := 0
		if fpIsDir(fde.Type) {
			dirFlag = 1
		}
		fp.pm.SetIntEx(fp.pm.CreateEx(node, "dir", nil, false, true), nil, dirFlag)
		if fp.pm.SetParentEx(node, nodes, nil, "") != 0 {
			fp.pm.Destroy(node)
		}
	}
	// C: prop_set(model, "loading", PROP_SET_INT, 0)
	fp.pm.SetIntEx(fp.pm.CreateEx(model, "loading", nil, false, false), nil, 0)
	// C: prop_ref_dec(nodes) — Go children are parent-owned; no dec needed.
	dir.Free() // C: fa_dir_free(fd)
}

// filepickerScandirOp — C: filepicker_scandir_op_t
type filepickerScandirOp struct {
	fp    *Filepicker
	path  string
	model *propcore.Prop
	flags int
}

// filepickerScandirTask — C: filepicker_scandir_task
func filepickerScandirTask(aux any) {
	op := aux.(*filepickerScandirOp)
	op.fp.fpScandir(op.path, op.model, op.flags)
	// C: free(path); prop_ref_dec(model); free(op)
	op.model.Release()
}

// loadDir — C: filepicker_load_dir
func (fp *Filepicker) loadDir(path, title string) {
	fd := &filepickerDir{}
	fd.root = fp.pm.CreateRootEx("", false)
	fd.root.Retain() // C: prop_ref_inc(prop_create_root(NULL))

	if fp.pm.SetParentEx(fd.root, fp.pages, nil, "") != 0 {
		// C: popup destroyed, bail out
		fp.pm.Destroy(fd.root)
		fd.root.Release()
		fp.run = false
		return
	}
	fp.pm.SelectChildProp(fd.root, nil)

	// C: prop_set(fd->fd_root, "canGoBack", PROP_SET_INT, LIST_FIRST != NULL)
	canGoBack := 0
	if len(fp.dirstack) > 0 {
		canGoBack = 1
	}
	fp.pm.SetIntEx(fp.pm.CreateEx(fd.root, "canGoBack", nil, false, true), nil, canGoBack)

	// C: LIST_INSERT_HEAD(&fp->fp_dirstack, fd, fd_link)
	fp.dirstack = slices.Insert(fp.dirstack, 0, fd)

	if title == "" {
		// C: fa_url_get_last_component(tmp, sizeof(tmp), path)
		title = fp.fam.URLGetLastComponent(path)
	}

	model := fp.pm.CreateEx(fd.root, "model", nil, false, true) // C: prop_create_r
	fp.pm.SetIntEx(fp.pm.CreateEx(model, "loading", nil, false, true), nil, 1)
	fp.pm.SetStringEx(fp.pm.CreateEx(model, "title", nil, false, true), nil,
		title, propcore.PropStrUTF8)

	// C: task_run(filepicker_scandir_task, op) — model ref transfers to op
	model.Retain()
	fp.fam.tasks.Run(filepickerScandirTask, &filepickerScandirOp{
		fp:    fp,
		path:  path,
		model: model,
		flags: fp.flags,
	})
}

// pickURL — C: pick_url
func (fp *Filepicker) pickURL(url, how string) {
	var doDescend bool
	if fp.flags&FilepickerDirectories != 0 {
		doDescend = how == "descend"
	} else {
		fs, err := Stat(fp.fam, url)
		if err != nil {
			// C: TRACE(TRACE_ERROR, "filepicker", "%s", errbuf)
			return
		}
		doDescend = fpIsDir(fs.Type)
	}

	if doDescend {
		fp.loadDir(url, "")
	} else {
		fp.path = url
		fp.hasPath = true
		fp.run = false
	}
}

// filepickerEvent — C: filepicker_event
func (fp *Filepicker) filepickerEvent(e *event.Event) {
	if e.Type == event.EVENT_OPENURL {
		// C: event_openurl_t *eo — url/how fields
		var url, how string
		if e.OpenURL != nil {
			url = e.OpenURL.URL
			how = e.OpenURL.How
		}
		fp.pickURL(url, how)
	} else if e.IsAction(event.ACTION_NAV_BACK) {
		fp.popdir()
	}
	// C: else printf("filepicker not handling event %s\n", ...)
}

// eventsink — C: eventsink (PROP_DESTROYED → run=0; PROP_EXT_EVENT →
// filepicker_event)
func (fp *Filepicker) eventsink(opaque any, ev propcore.EventType, args ...any) {
	switch ev {
	case propcore.EventDestroyed:
		fp.run = false
	case propcore.EventExtEvent:
		if len(args) > 0 {
			if e, ok := args[0].(*event.Event); ok {
				fp.filepickerEvent(e)
			}
		}
	}
}

// FilepickerPick — C: filepicker_pick. Opens the picker rooted at
// vfs:/// and blocks until a pick is made or the popup is destroyed.
// Returns "" when cancelled (C: NULL fp_path).
func FilepickerPick(fam *FileAccessManager, title string, flags int) string {
	fp := &Filepicker{
		run:   true,
		flags: flags,
		fam:   fam,
		pm:    fam.pm,
	}

	p := fp.pm.CreateRootEx("", false)
	fp.pm.SetStringEx(fp.pm.CreateEx(p, "type", nil, false, true), nil,
		"filepicker", propcore.PropStrUTF8)

	if title == "" {
		// C: prop_set(p, "title", PROP_ADOPT_RSTRING, _("Select a file"))
		fp.pm.SetStringEx(fp.pm.CreateEx(p, "title", nil, false, true), nil,
			"Select a file", propcore.PropStrUTF8)
	} else {
		fp.pm.SetStringEx(fp.pm.CreateEx(p, "title", nil, false, true), nil,
			title, propcore.PropStrUTF8)
	}

	if flags&FilepickerDirectories != 0 {
		fp.pm.SetIntEx(fp.pm.CreateEx(p, "dirmode", nil, false, true), nil, 1)
	}

	fp.pages = fp.pm.CreateEx(p, "pages", nil, false, true) // C: prop_create_r

	pc := propcore.NewCourier("filepicker")

	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY, PROP_TAG_CALLBACK, eventsink,
	//   PROP_TAG_NAMED_ROOT p "node", PROP_TAG_COURIER pc,
	//   PROP_TAG_NAME("node","eventSink")) — resolves p.eventSink dynamically.
	sinkWatch := watchChildPath(p, []string{"eventSink"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			return fp.pm.SubscribeWithCourier(leaf, pc, fp.eventsink, fp,
				propcore.SubFlagTrackDestroy)
		})

	// C: prop_set_parent(p, prop_create(prop_get_global(), "popups"))
	popups := fp.pm.CreateEx(fp.pm.GetGlobal(), "popups", nil, false, true)
	if fp.pm.SetParentEx(p, popups, nil, "") != 0 {
		// C: abort() — popuproot zombie; keep a soft error instead.
		sinkWatch.destroy()
		pc.Destroy()
		fp.pm.Destroy(p)
		return ""
	}

	fp.loadDir("vfs:///", "")

	// C: while(fp.fp_run) prop_courier_wait_and_dispatch(pc)
	for fp.run {
		pc.Wait()
	}

	sinkWatch.destroy()
	pc.Destroy()

	for len(fp.dirstack) > 0 {
		fd := fp.dirstack[0]
		fp.dirstack = fp.dirstack[1:]
		fp.pm.Destroy(fd.root)
	}
	fp.pm.Destroy(p)

	if !fp.hasPath {
		return ""
	}
	return fp.path
}

// fpaux — C: fpaux_t
type fpaux struct {
	fam    *FileAccessManager
	title  string
	target *propcore.Prop
	url    string
	flags  int
}

// filepickerPickToPropTask — C: filepicker_pick_to_prop_task
func filepickerPickToPropTask(aux any) {
	a := aux.(*fpaux)
	res := FilepickerPick(a.fam, a.title, a.flags)

	if res != "" {
		// C: prop_set_rstring(a->target, res)
		a.target.SetString(res)
	}
	// C: free(title); prop_ref_dec(target); free(url); free(a)
	a.target.Release()
}

// FilepickerPickToProp — C: filepicker_pick_to_prop. Runs the picker on
// a task thread and writes the result into target.
func FilepickerPickToProp(fam *FileAccessManager, title string, target *propcore.Prop, current string, flags int) {
	if target == nil {
		return
	}
	target.Retain() // C: prop_ref_inc(target)
	fam.tasks.Run(filepickerPickToPropTask, &fpaux{
		fam:    fam,
		title:  title,
		target: target,
		url:    current,
		flags:  flags,
	})
}

// contentTypeString — C: content2type (metadata.c:177) — index table,
// out-of-range → "" (C returns NULL).
func contentTypeString(ctype int) string {
	switch ctype {
	case ContentUnknown:
		return "unknown"
	case ContentDir:
		return "directory"
	case ContentFile:
		return "file"
	case ContentAudio:
		return "audio"
	case ContentArchive:
		return "archive"
	case ContentVideo:
		return "video"
	case ContentPlaylist:
		return "playlist"
	case ContentDVD:
		return "dvd"
	case ContentImage:
		return "image"
	case ContentAlbum:
		return "album"
	case ContentPlugin:
		return "plugin"
	case ContentFont:
		return "font"
	case ContentShare:
		return "share"
	case ContentDocument:
		return "document"
	default:
		return ""
	}
}
