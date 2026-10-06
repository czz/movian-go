package subtitles

// vobsub.go — canonical port of src/subtitles/vobsub.c's probing half.
// The loader/demuxer (vobsub_load, worker thread, picker) lives in
// pkg/subtitles/ext/vobsub.go because it needs text/fileaccess deps
// that sit above pkg/subtitles in the import DAG. The palette/size
// decoders live in pkg/media/core/dvdspu.go for the same reason.

import (
	"github.com/czz/movian-go/internal/misc"
	"strings"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// vobsubProbe — C: vobsub_probe (vobsub.c:75-148).
//
// Stats the .sub sibling, loads the .idx, and for every "id:" line
// emits a "vobsub:" JSON subtitle track via mp_add_track.
func vobsubProbe(fam *fileaccesscore.FileAccessManager, url, filename string, score int, prop *propcore.Prop,
	subfile string, autosel int) {

	if subfile == "" {
		// C: sf = strrchr(mystrdupa(url), '.');
		//    if(sf == NULL || strlen(sf) != 4) return;
		//    strcpy(sf, ".sub");
		i := strings.LastIndex(url, ".")
		if i < 0 || len(url)-i != 4 {
			return
		}
		subfile = url[:i] + ".sub"
	}

	if _, err := fileaccesscore.Stat(fam, subfile); err != nil {
		fam.TraceSystem().Trace(trace.TRACE_ERROR, "VOBSUB",
			"Unable to stat sub file: %s -- %v", subfile, err)
		return
	}

	// C: b = fa_load(url, FA_LOAD_ERRBUF, FA_LOAD_CACHE_CONTROL(
	//     DISABLE_CACHE), NULL)
	fh, err := fileaccesscore.FAOpenEx(fam, url, 0, nil)
	if err != nil || fh == nil {
		fam.TraceSystem().Trace(trace.TRACE_ERROR, "VOBSUB",
			"Unable to load %s -- %v", url, err)
		return
	}
	fab := fileaccesscore.LoadAndClose(fh)
	if fab == nil {
		fam.TraceSystem().Trace(trace.TRACE_ERROR, "VOBSUB",
			"Unable to load %s", url)
		return
	}

	data := string(fab.Data[:fab.Size])

	// C: for(; l = strcspn(s, "\r\n"), *s; s += l+1+strspn(s+l+1, "\r\n"))
	s := 0
	for s < len(data) && data[s] != 0 {
		l := strings.IndexAny(data[s:], "\r\n")
		if l < 0 {
			l = len(data) - s
		}
		line := data[s : s+l]

		// C: s += l+1+strspn(s+l+1, "\r\n") — skip the terminator plus
		// any further CR/LF.
		next := s + l + 1
		for next < len(data) && (data[next] == '\r' || data[next] == '\n') {
			next++
		}
		s = next

		p, ok := myStrBegins(line, "id:")
		if !ok {
			continue
		}
		for len(p) > 0 && p[0] == ' ' {
			p = p[1:]
		}
		if len(p) < 2 {
			continue
		}
		lang := p

		m := htsmsg.NewMap()
		m.AddStr("idx", url)
		m.AddStr("sub", subfile)

		if x := strings.Index(lang, "index:"); x >= 0 {
			m.AddU32("index", uint32(misc.Atoi(lang[x+len("index:"):])))
			lang = lang[:x]
		}

		if x := strings.IndexByte(lang, ','); x >= 0 {
			lang = lang[:x]
		}

		u, serr := htsmsg.SerializeJSONToRstr(m, "vobsub:")
		if serr != nil {
			continue
		}

		mpAddTrack(prop, filename, u, "VobSub", "", lang, "",
			pstr("External file"), score, autosel)

		// C: if(p == NULL) break; // No indexing found, can only add
		// one — dead code: p is non-NULL inside the mystrbegins
		// branch. Kept for parity (never fires).
	}
}

// myStrBegins — C: mystrbegins (str.h). Returns the remainder if s
// starts with prefix.
func myStrBegins(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}
