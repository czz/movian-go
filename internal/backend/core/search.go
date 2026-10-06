package core

// Split from backend.go — Fase 4 pure-move refactor.

import (
	"bufio"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/czz/movian-go/internal/app"
	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/fileaccess/scanner"
	"github.com/czz/movian-go/internal/metadata"
	"github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	"github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

func (bs *BackendSystem) Search(model any, query string, loading any) {
	bs.backendsMutex.RLock()
	defer bs.backendsMutex.RUnlock()

	for _, backend := range bs.backends {
		if backend.Search != nil {
			backend.Search(model, query, loading)
		}
	}
}

// PageOpen opens a page
func pluginCatTitle(cat string) string {
	switch cat {
	case "tv":
		return "Online TV"
	case "video":
		return "Video streaming"
	case "music":
		return "Music streaming"
	case "cloud":
		return "Cloud services"
	case "glwview":
		return "User interface extensions"
	case "glwosk":
		return "On Screen Keyboards"
	case "subtitles":
		return "Subtitles"
	case "audioengine":
		return "Audio decoders"
	default:
		return "Uncategorized"
	}
}

// openPluginCategoryPage — C: open_category_page (plugins.c:1643-1653).
// type=directory, contents=grid, title=cat title, nodes links the filtered
// category child of plugin_repo_model.
func (bs *BackendSystem) openPluginCategoryPage(model *propcore.Prop, category string) {
	pm := bs.propManager
	pm.SetStringEx(pm.CreateEx(model, "type", nil, false, false),
		nil, "directory", propcore.StringUTF8)
	pm.SetStringEx(pm.CreateEx(model, "contents", nil, false, false),
		nil, "grid", propcore.StringUTF8)

	meta := pm.CreateEx(model, "metadata", nil, false, false)
	pm.Link(nls.GetProp(pluginCatTitle(category)),
		pm.CreateEx(meta, "title", nil, false, false), nil, false, false)

	repoModel := bs.getOrCreatePluginRepoModel()
	if repoModel != nil {
		// C: prop_link(prop_create(plugin_repo_model, category), n) —
		// prop_create creates the (empty) category node if absent.
		catProp := pm.CreateEx(repoModel, category, nil, false, false)
		pm.Link(catProp, pm.CreateEx(model, "nodes", nil, false, false),
			nil, false, false)
	}
}

// openPluginCategories — C: open_categories (plugins.c:1672-1694).
// type=directory, contents=grid, title="Plugin categories", nodes gets one
// directory item per browsable category (VMIR audioengine excluded —
// deliberately not supported; !PS3 → glwosk included).
func (bs *BackendSystem) openPluginCategories(model *propcore.Prop) {
	pm := bs.propManager
	pm.SetStringEx(pm.CreateEx(model, "type", nil, false, false),
		nil, "directory", propcore.StringUTF8)
	pm.SetStringEx(pm.CreateEx(model, "contents", nil, false, false),
		nil, "grid", propcore.StringUTF8)

	meta := pm.CreateEx(model, "metadata", nil, false, false)
	pm.Link(nls.GetProp("Plugin categories"),
		pm.CreateEx(meta, "title", nil, false, false), nil, false, false)

	nodes := pm.CreateEx(model, "nodes", nil, false, false)
	// C: add_category calls in order — (category, subtype)
	for _, c := range []struct{ cat, subtype string }{
		{"tv", "tv"},
		{"video", "movie"},
		{"music", "audiotrack"},
		{"subtitles", "subtitles"},
		{"other", "other"},
		{"glwosk", "keyboard"},
	} {
		item := pm.CreateEx(nodes, "", nil, false, false)
		pm.SetStringEx(pm.CreateEx(item, "type", nil, false, false),
			nil, "directory", propcore.StringUTF8)
		pm.SetStringEx(pm.CreateEx(item, "url", nil, false, false),
			nil, "plugin:repo:"+c.cat, propcore.StringUTF8)
		pm.SetStringEx(pm.CreateEx(item, "subtype", nil, false, false),
			nil, c.subtype, propcore.StringUTF8)
		itemMeta := pm.CreateEx(item, "metadata", nil, false, false)
		pm.Link(nls.GetProp(pluginCatTitle(c.cat)),
			pm.CreateEx(itemMeta, "title", nil, false, false), nil, false, false)
	}
}

// pluginSearch reproduces C's plugin_search (src/plugins.c:1733).
// It creates a "Plugins" search class under model.nodes and adds
// plugins whose title contains the query string.
func (bs *BackendSystem) pluginSearch(model *propcore.Prop, query string) {
	if bs.pluginManager == nil {
		return
	}

	// C: search_class_create(classnodes, &nodes, &entries, "Plugins", NULL)
	classNodes := bs.propManager.CreateEx(model, "nodes", nil, false, false)
	if classNodes == nil {
		return
	}

	// Collect matching plugins first, then create the class directory
	// with results already populated. This ensures the PropNF predicate
	// (node.entries == 0 → exclude) doesn't filter out the class directory
	// before results are added.
	queryLower := strings.ToLower(query)
	type matchEntry struct {
		repoProp *propcore.Prop
	}
	var matches []matchEntry
	for _, pl := range bs.pluginManager.GetPlugins() {
		if pl.RepoModel == nil {
			continue
		}
		if !strings.Contains(strings.ToLower(pl.Title), queryLower) {
			continue
		}
		matches = append(matches, matchEntry{repoProp: pl.RepoModel})
	}

	if len(matches) == 0 {
		return
	}

	// Create the search class directory with results already populated.
	// C: search_class_create() creates the class with all children BEFORE
	// attaching to parent. We replicate this by creating as root first,
	// populating, then attaching (triggers EventAddChild with full structure).
	classDir := bs.propManager.CreateRootEx("", false)
	if classDir == nil {
		return
	}
	typeProp := bs.propManager.CreateEx(classDir, "type", nil, false, false)
	bs.propManager.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)
	metaProp := bs.propManager.CreateEx(classDir, "metadata", nil, false, false)
	titleProp := bs.propManager.CreateEx(metaProp, "title", nil, false, false)
	bs.propManager.SetStringEx(titleProp, nil, "Plugins", propcore.StringUTF8)
	nodesProp := bs.propManager.CreateEx(classDir, "nodes", nil, false, false)
	entriesProp := bs.propManager.CreateEx(classDir, "entries", nil, false, false)

	// Add all matching plugins as nodes
	for _, m := range matches {
		childNode := bs.propManager.CreateEx(nodesProp, "", nil, false, false)
		if childNode != nil {
			bs.propManager.Link(m.repoProp, childNode, nil, false, false)
		}
	}
	bs.propManager.SetIntEx(entriesProp, nil, len(matches))

	// Now attach to parent (triggers EventAddChild with full structure)
	bs.propManager.SetParentEx(classDir, classNodes, nil, "")
}

// getOrCreatePluginStartModel returns the global plugin start model,
// creating it if it doesn't exist yet. Matches C's plugin_start_model
// (src/plugins.c:1047-1080): type="directory", metadata.title="Plugins",
// nodes with a "store" item + installed plugins.
func (bs *BackendSystem) getOrCreatePluginStartModel() *propcore.Prop {
	if bs.pluginStartModel != nil {
		return bs.pluginStartModel
	}

	global := bs.propManager.GetGlobal()
	if global == nil {
		return nil
	}

	// Check if it already exists under global
	pluginProp := global.FindChild("plugin")
	if pluginProp == nil {
		pluginProp = bs.propManager.CreateEx(global, "plugin", nil, false, false)
	}
	startModel := pluginProp.FindChild("start")
	if startModel == nil {
		startModel = bs.propManager.CreateEx(pluginProp, "start", nil, false, false)
	}
	bs.pluginStartModel = startModel

	// Only populate if it doesn't have children yet
	if len(startModel.GetChildren()) > 0 {
		return startModel
	}

	// C: prop_set(plugin_start_model, "type", PROP_SET_STRING, "directory")
	typeProp := bs.propManager.CreateEx(startModel, "type", nil, false, false)
	bs.propManager.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

	// C: prop_set(plugin_start_model, "contents", PROP_SET_STRING, "plugins")
	contentsProp := bs.propManager.CreateEx(startModel, "contents", nil, false, false)
	bs.propManager.SetStringEx(contentsProp, nil, "plugins", propcore.StringUTF8)

	// C: prop_set(plugin_start_model, "safeui", PROP_SET_INT, 1)
	safeuiProp := bs.propManager.CreateEx(startModel, "safeui", nil, false, false)
	bs.propManager.SetIntEx(safeuiProp, nil, 1)

	// C: metadata.title = "Plugins"
	metaProp := bs.propManager.CreateEx(startModel, "metadata", nil, false, false)
	titleProp := bs.propManager.CreateEx(metaProp, "title", nil, false, false)
	bs.propManager.SetStringEx(titleProp, nil, "Plugins", propcore.StringUTF8)

	// C: plugin_setup_start_model (plugins.c:1041-1082) —
	// nodes = prop_concat[ store-source, nf(installed)+separator ]
	nodesProp := bs.propManager.CreateEx(startModel, "nodes", nil, false, false)
	pc := propcore.PropConcatCreate(bs.propManager, nodesProp)

	// Top items — C: sta = prop_create_root(NULL) with a "store" child
	// linking _p("Browse available plugins") and url plugin:repo:categories.
	sta := bs.propManager.CreateRootEx("", false)
	staItem := bs.propManager.CreateEx(sta, "", nil, false, false)
	bs.propManager.SetStringEx(bs.propManager.CreateEx(staItem, "type", nil, false, false),
		nil, "store", propcore.StringUTF8)
	staMeta := bs.propManager.CreateEx(staItem, "metadata", nil, false, false)
	bs.propManager.Link(nls.GetProp("Browse available plugins"),
		bs.propManager.CreateEx(staMeta, "title", nil, false, false), nil, false, false)
	bs.propManager.SetStringEx(bs.propManager.CreateEx(staItem, "url", nil, false, false),
		nil, "plugin:repo:categories", propcore.StringUTF8)
	pc.AddSource(sta, nil)

	// Installed plugins — C: inst = prop_create_root(NULL) +
	// prop_nf(inst, plugin_root_list, EXCLUDE node.status.installed != 1)
	// with a trailing "Installed plugins" separator node.
	inst := bs.propManager.CreateRootEx("", false)
	pluginRootList := global.FindChild("plugins")
	if pluginRootList != nil {
		pluginRootList = pluginRootList.FindChild("nodes")
	}
	if pluginRootList != nil {
		pnf := propcore.PropNFCreate(inst, pluginRootList, nil, propcore.PropNFAutoDestroy)
		propcore.PropNFPredIntAdd(pnf, "node.status.installed",
			propcore.PropNFCmpNeq, 1, nil, propcore.PropNFModeExclude)
	}

	d := bs.propManager.CreateRootEx("", false)
	dMeta := bs.propManager.CreateEx(d, "metadata", nil, false, false)
	bs.propManager.Link(nls.GetProp("Installed plugins"),
		bs.propManager.CreateEx(dMeta, "title", nil, false, false), nil, false, false)
	bs.propManager.SetStringEx(bs.propManager.CreateEx(d, "type", nil, false, false),
		nil, "separator", propcore.StringUTF8)
	pc.AddSource(inst, d)

	return startModel
}

// getOrCreatePluginRepoModel returns the global plugin repo model.
// Matches C's plugin_repo_model (src/plugins.c:1147).
func (bs *BackendSystem) getOrCreatePluginRepoModel() *propcore.Prop {
	if bs.pluginRepoModel != nil {
		return bs.pluginRepoModel
	}

	global := bs.propManager.GetGlobal()
	if global == nil {
		return nil
	}

	// Check if it already exists under global.plugin.repo
	pluginProp := global.FindChild("plugin")
	if pluginProp == nil {
		pluginProp = bs.propManager.CreateEx(global, "plugin", nil, false, false)
	}
	repoModel := pluginProp.FindChild("repo")
	if repoModel == nil {
		repoModel = bs.propManager.CreateEx(pluginProp, "repo", nil, false, false)
	}
	bs.pluginRepoModel = repoModel

	// Only populate if it doesn't have children yet
	if len(repoModel.GetChildren()) > 0 {
		return repoModel
	}

	// C: plugin_setup_repo_model (plugins.c:1140-1180)
	typeProp := bs.propManager.CreateEx(repoModel, "type", nil, false, false)
	bs.propManager.SetStringEx(typeProp, nil, "directory", propcore.StringUTF8)

	safeuiProp := bs.propManager.CreateEx(repoModel, "safeui", nil, false, false)
	bs.propManager.SetIntEx(safeuiProp, nil, 1)

	contentsProp := bs.propManager.CreateEx(repoModel, "contents", nil, false, false)
	bs.propManager.SetStringEx(contentsProp, nil, "plugins", propcore.StringUTF8)

	metaProp := bs.propManager.CreateEx(repoModel, "metadata", nil, false, false)
	bs.propManager.Link(nls.GetProp("Available plugins"),
		bs.propManager.CreateEx(metaProp, "title", nil, false, false), nil, false, false)

	nodesProp := bs.propManager.CreateEx(repoModel, "nodes", nil, false, false)
	pc := propcore.PropConcatCreate(bs.propManager, nodesProp)

	pluginRootList := global.FindChild("plugins")
	if pluginRootList != nil {
		pluginRootList = pluginRootList.FindChild("nodes")
	}

	// C: catnames strtab order defines the category indices
	// (plugins.c:61-71). PLUGIN_CAT_num = 9.
	pluginCats := []struct {
		name  string
		title string
	}{
		{"tv", "Online TV"},
		{"video", "Video streaming"},
		{"music", "Music streaming"},
		{"cloud", "Cloud services"},
		{"other", "Uncategorized"},
		{"glwview", "User interface extensions"},
		{"glwosk", "On Screen Keyboards"},
		{"audioengine", "Audio decoders"},
		{"subtitles", "Subtitles"},
	}

	for _, cat := range pluginCats {
		catProp := bs.propManager.CreateEx(repoModel, cat.name, nil, false, false)
		if pluginRootList != nil {
			pnf := propcore.PropNFCreate(catProp, pluginRootList, nil,
				propcore.PropNFAutoDestroy)
			propcore.PropNFPredStrAdd(pnf, "node.metadata.category",
				propcore.PropNFCmpNeq, cat.name, nil, propcore.PropNFModeExclude)
			propcore.PropNFPredIntAdd(pnf, "node.status.inRepo",
				propcore.PropNFCmpNeq, 1, nil, propcore.PropNFModeExclude)
			propcore.PropNFSort(pnf, "node.metadata.title", false, 0, nil, true)
			propcore.PropNFRelease(pnf)
		}

		header := bs.propManager.CreateRootEx("", false)
		bs.propManager.SetStringEx(bs.propManager.CreateEx(header, "type", nil, false, false),
			nil, "separator", propcore.StringUTF8)
		headerMeta := bs.propManager.CreateEx(header, "metadata", nil, false, false)
		bs.propManager.Link(nls.GetProp(cat.title),
			bs.propManager.CreateEx(headerMeta, "title", nil, false, false), nil, false, false)

		pc.AddSource(catProp, header)
	}

	return repoModel
}

// registerDiscoveredBackend registers the discovered backend for handling
// discovered: URLs. Reproduces C's discovered_open_url (src/service.c:544):
//   - metadata.title = "Local network"
//   - type = "directory"
//   - nodes = prop_link(discovered_nodes, model.nodes)
type faSearchT struct {
	query   string                 // fas_query
	nodes   *propcore.Prop         // fas_nodes
	sub     *propcore.Subscription // fas_sub
	stdout  io.ReadCloser          // fas_fp (popen read pipe)
	cmd     *exec.Cmd              // fas_fp (process behind the pipe)
	pc      *propcore.Courier      // fas_pc
	run     bool                   // fas_run
	loading *propcore.Prop         // not in C struct; Go needs it to clear loading
}

// faSearchDestroy — C: fa_search_destroy (fa_locatedb.c:56-76)
func (fas *faSearchT) destroy() {
	// C: free(fas->fas_query) — Go GC
	if fas.sub != nil {
		fas.sub.Unsubscribe() // C: prop_unsubscribe(fas->fas_sub)
	}
	if fas.pc != nil {
		fas.pc.Destroy() // C: prop_courier_destroy(fas->fas_pc)
	}
	if fas.nodes != nil {
		fas.nodes.Release() // C: prop_ref_dec(fas->fas_nodes)
	}
	// C: pclose(fas->fas_fp) — waits for the child to finish.
	if fas.stdout != nil {
		fas.stdout.Close()
	}
	if fas.cmd != nil {
		fas.cmd.Wait()
	}
	if fas.loading != nil {
		// Not in C (loading cleared by the page lifecycle); Go clears it
		// here so the spinner stops when the search finishes/aborts.
		fas.loading.SetInt(0)
	}
}

// faSearchNodesub — C: fa_search_nodesub (fa_locatedb.c:80-99)
func faSearchNodesub(opaque any, ev propcore.EventType, args ...any) {
	fas := opaque.(*faSearchT)
	if ev == propcore.EventDestroyed {
		fas.run = false
	}
}

// deregex — C: deregex (fa_locatedb.c:107-135). Replaces regex tokens
// with '.' in the string.
func deregex(str string) string {
	b := []byte(str)
	for i, c := range b {
		switch c {
		case '|', '\\', '(', ')', '^', '$', '+', '?', '*', '[', ']', '{', '}':
			b[i] = '.'
		}
	}
	return string(b)
}

// faCreatePathsRegex — C: fa_create_paths_regex (fa_locatedb.c:143-191).
// Compiles a regexp matching all paths of existing file:// services,
// e.g. "^(path1/|path2/)". Returns nil on compile failure (C: -1).
func (bs *BackendSystem) faCreatePathsRegex() *regexp.Regexp {
	var str string

	// C: hts_mutex_lock(&service_mutex); LIST_FOREACH(s, &services, s_link)
	var paths []string
	if bs.serviceSystem != nil {
		for _, svc := range bs.serviceSystem.GetAllServices() {
			url := service.ServiceGetURL(svc)
			if strings.HasPrefix(url, "file://") {
				p := url[len("file://"):]
				if !strings.HasSuffix(p, "/") {
					p += "/" // C: (s->s_url[...]=='/' ? "" : "/")
				}
				paths = append(paths, deregex(p))
			}
		}
	}

	if len(paths) == 0 {
		// C: No file:// services found → str = strdup(".*")
		str = ".*"
	} else {
		// C: "^(" p1/ "|" p2/ "|" ... ")" — the trailing '|' is
		// overwritten by ')'.
		str = "^(" + strings.Join(paths, "|") + ")"
	}

	// C: regcomp(preg, str, REG_EXTENDED|REG_ICASE|REG_NOSUB)
	re, err := regexp.Compile("(?i)" + str)
	if err != nil {
		bs.traceSystem.Trace(trace.TRACE_ERROR, "FA",
			"Search regex compilation of %q failed: %s", str, err)
		return nil
	}
	return re
}

// searchClassCreate — C: search_class_create (src/backend/search.c:39-66)
func (bs *BackendSystem) searchClassCreate(parent *propcore.Prop,
	title, icon string) (nodes, entries *propcore.Prop, err error) {
	pm := bs.propManager
	p := pm.CreateRootEx("", false)
	m := pm.CreateEx(p, "metadata", nil, false, false)

	// C: prop_set(p, "url", PROP_ADOPT_RSTRING, backend_prop_make(p, NULL))
	u := pm.CreateEx(p, "url", nil, false, false)
	pm.SetStringEx(u, nil,
		bs.propPageManager.BackendPropMake(pm, p, ""), propcore.StringUTF8)

	pm.SetStringEx(pm.CreateEx(m, "title", nil, false, false), nil,
		title, propcore.StringUTF8)
	if icon != "" {
		pm.SetStringEx(pm.CreateEx(m, "icon", nil, false, false), nil,
			icon, propcore.StringUTF8)
	}
	pm.SetStringEx(pm.CreateEx(p, "type", nil, false, false), nil,
		"directory", propcore.StringUTF8)

	n := pm.CreateEx(p, "nodes", nil, false, false)
	e := pm.CreateEx(p, "entries", nil, false, false)
	pm.SetIntEx(e, nil, 0)

	n.Retain() // C: *nodesp = prop_ref_inc(n)
	e.Retain() // C: *entriesp = prop_ref_inc(e)

	if pm.SetParentEx(p, parent, nil, "") != 0 {
		pm.Destroy(p) // C: prop_destroy(p); return 1
		return nil, nil, errors.New("set parent failed")
	}
	return n, e, nil
}

// faLocateSearcher — C: fa_locate_searcher (fa_locatedb.c:198-346).
// Consume 'locate' (updatedb) output results and feed into search results.
func (bs *BackendSystem) faLocateSearcher(fas *faSearchT) {
	pm := bs.propManager

	preg := bs.faCreatePathsRegex()
	if preg == nil {
		fas.destroy()
		return
	}

	// C: snprintf(iconpath, ..., "%s/res/fileaccess/fs_icon.png", app_dataroot())
	iconpath := app.AppDataRoot() + "/res/fileaccess/fs_icon.png"

	var entries [2]*propcore.Prop
	var nodes [2]*propcore.Prop

	rd := bufio.NewReader(fas.stdout)

	// Consume 'locate' results.
	for {
		// C: prop_courier_poll(fas->fas_pc)
		fas.pc.Poll()

		if !fas.run {
			break
		}

		// C: fgets(buf, sizeof(buf), fas->fas_fp) — ReadString keeps the
		// trailing '\n' like fgets; a final line without '\n' is still
		// returned (err == io.EOF), matching fgets at EOF.
		buf, err := rd.ReadString('\n')
		if buf == "" {
			break
		}
		// C: if (!*buf || *buf == '\n') continue;
		if buf == "\n" {
			continue
		}

		// C: buf[strlen(buf)-1] = '\0' — strips '\n', or the last byte of
		// a line with no trailing newline (C quirk preserved).
		buf = buf[:len(buf)-1]

		// C: Ignore dot-files/dirs — strstr(buf, "/.")
		if strings.Contains(buf, "/.") {
			continue
		}

		// C: regexec(&preg, buf, 0, NULL, 0)
		if !preg.MatchString(buf) {
			continue
		}

		url := "file://" + buf

		// C: fa_stat(url, &fs, NULL, 0)
		fs, err := fileaccesscore.Stat(bs.fileAccessManager, url)
		if err != nil {
			continue
		}

		// C: metadata = prop_create_root("metadata")
		meta := pm.CreateRootEx("metadata", false)
		var ctype metadata.ContentType

		if fs.Type == fileaccesscore.ContentDir {
			ctype = metadata.ContentDir
			// C: prop_set_string(prop_create(metadata,"title"), basename(buf))
			pm.SetStringEx(pm.CreateEx(meta, "title", nil, false, false),
				nil, filepath.Base(buf), propcore.StringUTF8)
		} else {
			// C: md = fa_probe_metadata(url, NULL, 0, NULL, NULL)
			md, _ := scanner.FAProbeMetadata(bs.fileAccessManager, url,
				"", nil)
			if md != nil {
				ctype = md.ContentType
				md.Destroy()
			} else {
				ctype = metadata.ContentUnknown
			}
		}

		if ctype == metadata.ContentUnknown {
			continue
		}

		var t int
		switch ctype {
		case metadata.ContentAudio:
			t = 0
		case metadata.ContentVideo, metadata.ContentDVD:
			t = 1
		default:
			continue
		}

		if nodes[t] == nil {
			// C: search_class_create(fas->fas_nodes, &nodes[t],
			//   &entries[t], t ? "Local video files" :
			//   "Local audio files", iconpath)
			classTitle := "Local audio files"
			if t == 1 {
				classTitle = "Local video files"
			}
			var err error
			nodes[t], entries[t], err = bs.searchClassCreate(fas.nodes,
				classTitle, iconpath)
			if err != nil {
				break
			}
		}

		// C: prop_add_int(entries[t], 1)
		pm.SetIntEx(entries[t], nil, entries[t].GetInt()+1)

		// C: if((type = content2type(ctype)) == NULL) continue;
		typ := metadata.Content2Type(ctype)

		p := pm.CreateRootEx("", false)

		// C: if (prop_set_parent(metadata, p)) prop_destroy(metadata)
		if pm.SetParentEx(meta, p, nil, "") != 0 {
			pm.Destroy(meta)
		}

		pm.SetStringEx(pm.CreateEx(p, "url", nil, false, false), nil,
			url, propcore.StringUTF8)
		pm.SetStringEx(pm.CreateEx(p, "type", nil, false, false), nil,
			typ, propcore.StringUTF8)

		// C: if(prop_set_parent(p, nodes[t])) { prop_destroy(p); break; }
		if pm.SetParentEx(p, nodes[t], nil, "") != 0 {
			pm.Destroy(p)
			break
		}
	}

	// C: for(i...) { prop_ref_dec(nodes[i]); prop_ref_dec(entries[i]); }
	for i := range 2 {
		if nodes[i] != nil {
			nodes[i].Release()
		}
		if entries[i] != nil {
			entries[i].Release()
		}
	}

	bs.traceSystem.Trace(trace.TRACE_DEBUG, "FA", "Searcher: %s: Done", fas.query)
	fas.destroy()
}

// faSearcher — C: fa_searcher (fa_locatedb.c:350-385). Runs on a
// detached thread in C; a goroutine here.
func (bs *BackendSystem) faSearcher(fas *faSearchT) {
	// C: snprintf(cmd, ..., "locate -i -L -q -b '%s'", fas->fas_query)
	// -i: case insensitive, -L: follow symlinks, -q: quiet, -b: basename only.
	// GNU findutils locate lacks -q; it only suppresses stderr warnings so
	// omitting it is output-equivalent.
	fas.cmd = exec.Command("locate", "-i", "-L", "-b", fas.query)
	stdout, err := fas.cmd.StdoutPipe()
	if err == nil {
		fas.stdout = stdout
		err = fas.cmd.Start()
	}
	if err != nil {
		// C: popen() == NULL → destroy + return
		bs.traceSystem.Trace(trace.TRACE_ERROR, "FA",
			"Searcher: %s: Unable to execute locate: %s", fas.query, err)
		fas.destroy()
		return
	}

	// C: fas->fas_pc = prop_courier_create_passive();
	fas.pc = propcore.NewCourier("locatedb")
	// C: prop_subscribe(PROP_SUB_TRACK_DESTROY, PROP_TAG_CALLBACK, ...,
	//   PROP_TAG_ROOT, fas->fas_nodes, PROP_TAG_COURIER, fas->fas_pc, NULL)
	fas.sub = bs.propManager.SubscribeWithCourier(fas.nodes, fas.pc,
		faSearchNodesub, fas, propcore.SubFlagTrackDestroy)

	bs.faLocateSearcher(fas)
}

// locatedbSearch — C: locatedb_search (fa_locatedb.c:389-408)
func (bs *BackendSystem) locatedbSearch(model *propcore.Prop, query string, loading *propcore.Prop) {
	// C: if (!locatedb_enabled) return;
	if bs.locatedbEnabled == 0 {
		return
	}

	fas := &faSearchT{}
	// C: tolower() the query for case-insensitive search
	fas.query = strings.ToLower(query)
	fas.run = true
	// C: fas->fas_nodes = prop_ref_inc(prop_create(model, "nodes"))
	// CreateEx returns nil on a destroyed parent (C: PROP_ZOMBIE → NULL);
	// the page was torn down mid-search — nothing to do.
	fas.nodes = bs.propManager.CreateEx(model, "nodes", nil, false, false)
	if fas.nodes == nil {
		if loading != nil {
			loading.SetInt(0)
		}
		return
	}
	fas.nodes.Retain()
	fas.loading = loading

	// C: hts_thread_create_detached("fa search", fa_searcher, fas, ...)
	go bs.faSearcher(fas)
}

// locatedbSetup — C: locatedb_init (fa_locatedb.c:415-427).
// Registers the "Search using Unix locatedb" bool setting under
// search_get_settings(), stored as "locatedb"/"enable", default on.
func (bs *BackendSystem) locatedbSetup() error {
	if bs.settingsMgr == nil {
		return nil
	}
	// C: search_get_settings() — settings_add_dir(NULL, _p("Search"),
	//   "search", NULL, NULL, "settings:search") with static caching.
	if bs.searchSettings == nil {
		bs.searchSettings = bs.settingsMgr.AddDir(nil, bs.settingsMgr.P("Search"),
			"search", "", nil, "settings:search")
	}
	// C: setting_create(SETTING_BOOL, s, SETTINGS_INITIAL_UPDATE,
	//   SETTING_TITLE(_p("Search using Unix locatedb")), SETTING_VALUE(1),
	//   SETTING_WRITE_BOOL(&locatedb_enabled),
	//   SETTING_STORE("locatedb", "enable"), NULL)
	bs.settingsMgr.SettingCreate(settings.SettingBool, bs.searchSettings,
		settings.SettingsInitialUpdate,
		settings.SettingTagTitle, bs.settingsMgr.P("Search using Unix locatedb"),
		settings.SettingTagValue, 1,
		settings.SettingTagWriteInt, &bs.locatedbEnabled,
		settings.SettingTagStore, "locatedb", "enable",
		0)
	return nil
}

// registerSlideshowBackend — C: BE_REGISTER(slideshow)
// (src/backend/slideshow/slideshow.c:677-682). The backend is a plain
// canhandle/open pair; be_slideshow sets no be_flags (no
// BACKEND_OPEN_CHECKS_URI — the canhandle/open path handles it).
