package ext

// Canonical port of src/subtitles/ext_subtitles.c.

import (
	"bytes"
	"fmt"
	"slices"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/subtitles"
	"github.com/czz/movian-go/internal/text"
)

// RegisterMediaHooks — Go import-DAG seam: media_track.c calls
// subtitles_load / subtitles_destroy but mediacore cannot import this
// package (it sits below pkg/text). C links these directly; Go
// registers hooks on the MediaSystem instance.
func RegisterMediaHooks(ms *mediacore.MediaSystem) {
	ms.SubtitlesLoad = func(mp *mediacore.MediaPipe, url string, dcs misc.CharsetDefaultSrc) any {
		return SubtitlesLoad(mp, url, dcs)
	}
	ms.SubtitlesDestroy = func(es any) {
		if e, ok := es.(*subtitles.ExtSubtitles); ok {
			subtitles.SubtitlesDestroy(e)
		}
	}
	ms.VideoOverlayDecode = VideoOverlayDecode
	ms.SubAssRender = SubAssRender
}

// esInsertText — C: es_insert_text
func esInsertText(es *subtitles.ExtSubtitles, txt string, start, stop int64, tags int) {
	vo := VideoOverlayRenderCleartext(es.Sys, txt, start, stop, tags, 0)
	if vo != nil {
		// C: TAILQ_INSERT_TAIL(&es->es_entries, vo, vo_link)
		es.Entries = append(es.Entries, vo)
	}
}

// linereader — C: linereader_t
type linereader struct {
	buf []byte
	ll  int // Length of current line (-1 = none yet / EOF)
}

// linereaderSetup — C: linereader_init
func linereaderSetup(lr *linereader, buf []byte) {
	lr.buf = buf
	lr.ll = -1
}

// linereaderNext — C: linereader_next. Returns current line length or -1.
func linereaderNext(lr *linereader) int {
	if lr.ll != -1 {
		// Skip over previous line
		lr.buf = lr.buf[lr.ll:]

		// Skip over EOL
		if len(lr.buf) > 0 && lr.buf[0] == 13 {
			lr.buf = lr.buf[1:]
		}
		if len(lr.buf) > 0 && lr.buf[0] == 10 {
			lr.buf = lr.buf[1:]
		}
	}

	if len(lr.buf) == 0 {
		// At EOF
		lr.ll = -1
		return -1
	}

	i := 0
	for i < len(lr.buf) {
		if lr.buf[i] == 10 || lr.buf[i] == 13 {
			break
		}
		i++
	}
	lr.ll = i
	return i
}

// line — current line contents (C: lr->buf[0:lr->ll])
func (lr *linereader) line() []byte {
	if lr.ll < 0 {
		return nil
	}
	return lr.buf[:lr.ll]
}

// getInt — C: get_int
func getInt(lr *linereader, vp *int) int {
	r := 0
	for i := range lr.ll {
		if lr.buf[i] < '0' || lr.buf[i] > '9' {
			return -1
		}
		r = r*10 + int(lr.buf[i]-'0')
	}
	*vp = r
	return 0
}

// getSrtTimestamp2 — C: get_srt_timestamp2
func getSrtTimestamp2(buf []byte) int64 {
	return 1000 * (int64(buf[0]-'0')*36000000 +
		int64(buf[1]-'0')*3600000 +
		int64(buf[3]-'0')*600000 +
		int64(buf[4]-'0')*60000 +
		int64(buf[6]-'0')*10000 +
		int64(buf[7]-'0')*1000 +
		int64(buf[9]-'0')*100 +
		int64(buf[10]-'0')*10 +
		int64(buf[11]-'0'))
}

// getSrtTimestamp — C: get_srt_timestamp
func getSrtTimestamp(lr *linereader, start, stop *int64) int {
	if lr.ll < 29 || !bytes.Equal(lr.buf[12:17], []byte(" --> ")) {
		return -1
	}
	*start = getSrtTimestamp2(lr.buf)
	*stop = getSrtTimestamp2(lr.buf[17:])
	return 0
}

// srtSkipPreamble — C: srt_skip_preamble
func srtSkipPreamble(buf []byte) []byte {
	// WEBVTT is srt (see #2752)
	if len(buf) > 6 && bytes.Equal(buf[:6], []byte("WEBVTT")) {
		buf = buf[6:]
	}
	// Skip over any initial control characters (Issue #1885)
	for len(buf) > 0 && buf[0] != 0 && buf[0] <= 32 {
		buf = buf[1:]
	}
	return buf
}

// isSrt — C: is_srt
func isSrt(buf []byte) bool {
	var lr linereader
	var n int
	var start, stop int64

	buf = srtSkipPreamble(buf)

	linereaderSetup(&lr, buf)

	if linereaderNext(&lr) < 0 {
		return false
	}
	if getSrtTimestamp(&lr, &start, &stop) != 0 {
		if getInt(&lr, &n) != 0 {
			return false
		}
		if linereaderNext(&lr) < 0 {
			return false
		}
		if getSrtTimestamp(&lr, &start, &stop) != 0 {
			return false
		}
	}

	if stop < start {
		return false
	}
	return true
}

// isAss — C: is_ass
func isAss(buf []byte) bool {
	if !bytes.Contains(buf, []byte("[Script Info]")) {
		return false
	}
	if !bytes.Contains(buf, []byte("[Events]")) {
		return false
	}
	return true
}

// loadSrt — C: load_srt
func loadSrt(sys *subtitles.System, url string, buf []byte) *subtitles.ExtSubtitles {
	var lr linereader
	es := &subtitles.ExtSubtitles{Sys: sys}
	var txt []byte
	txtoff := 0
	var start, stop, pstart, pstop int64
	pstart, pstop = -1, -1

	const tagFlags = text.TEXT_PARSE_HTML_TAGS | text.TEXT_PARSE_HTML_ENTITIES |
		text.TEXT_PARSE_SLOPPY_TAGS | text.TEXT_PARSE_SUB_TAGS

	buf = srtSkipPreamble(buf)

	linereaderSetup(&lr, buf)
	for {
		if linereaderNext(&lr) < 0 {
			break
		}

		if getSrtTimestamp(&lr, &start, &stop) == 0 {
			if txt != nil && pstart != -1 && pstop != -1 {
				esInsertText(es, string(txt[:txtoff]), pstart, pstop, tagFlags)
				txt = nil
				txtoff = 0
			}
			pstart = start
			pstop = stop
			continue
		}
		if pstart == -1 {
			continue
		}

		tlen := len(txt)
		txt = append(txt, lr.line()...)
		txt = append(txt, 0x0a)
		if lr.ll == 0 && tlen > 0 {
			txtoff = tlen - 1
		}
	}

	if txt != nil && pstart != -1 && pstop != -1 {
		esInsertText(es, string(txt[:txtoff]), pstart, pstop, tagFlags)
	}
	return es
}

// htsmsgGetStr — C: htsmsg_get_str (nil-detecting variant)
func htsmsgGetStr(m *htsmsg.HTSMsg, name string) (string, bool) {
	for _, f := range m.GetFields() {
		if f.GetName() == name && f.GetType() == htsmsg.HmfStr {
			return f.GetStrValue(), true
		}
	}
	return "", false
}

// isTimedtext — C: is_timedtext
func isTimedtext(buf *misc.Buf) bool {
	if buf.Len() < 30 {
		return false
	}
	if !bytes.Equal(buf.C8()[:5], []byte("<?xml")) {
		return false
	}
	if !bytes.Contains(buf.C8(), []byte("<transcript>")) {
		return false
	}
	return true
}

// loadTimedtext — C: load_timedtext
func loadTimedtext(sys *subtitles.System, url string, buf *misc.Buf) *subtitles.ExtSubtitles {
	xml, err := htsmsg.DeserializeXMLBuf(buf.C8())
	if err != nil {
		return nil
	}

	transcript := xml.GetMapMulti("transcript")
	if transcript == nil {
		return nil
	}

	es := &subtitles.ExtSubtitles{Sys: sys}

	for _, f := range transcript.GetFields() {
		if f.GetType() == htsmsg.HmfStr && f.GetChilds() != nil {
			n := f.GetChilds()

			str, ok := htsmsgGetStr(n, "start")
			if !ok {
				continue
			}
			startF, _ := misc.MyStr2double(str)
			start := int64(startF * 1000000.0)

			str, ok = htsmsgGetStr(n, "dur")
			if !ok {
				continue
			}
			endF, _ := misc.MyStr2double(str)
			end := start + int64(endF*1000000.0)

			txt := []byte(f.GetStrValue())
			misc.HtmlEntitiesDecode(txt)
			esInsertText(es, string(txt), start, end, 0)
		}
	}
	return es
}

// isTtml — C: is_ttml
func isTtml(buf *misc.Buf) bool {
	if buf.Len() < 30 {
		return false
	}
	if !bytes.Equal(buf.C8()[:5], []byte("<?xml")) {
		return false
	}
	if !bytes.Contains(buf.C8(), []byte("http://www.w3.org/2006/10/ttaf1")) {
		return false
	}
	return true
}

// ttmlTimeExpression — C: ttml_time_expression
func ttmlTimeExpression(str string) int64 {
	t, ep := misc.MyStr2double(str)
	endp := str[ep:]

	switch endp {
	case "h":
		return int64(t * 3600 * 1000000)
	case "m":
		return int64(t * 60 * 1000000)
	case "ms":
		return int64(t * 1000)
	case "s":
		return int64(t * 1000000)
	}
	return -1
}

// loadTtml — C: load_ttml (TTML docs: http://www.w3.org/TR/ttaf1-dfxp/)
func loadTtml(sys *subtitles.System, url string, buf *misc.Buf) *subtitles.ExtSubtitles {
	xml, err := htsmsg.DeserializeXMLBuf(buf.C8())
	if err != nil {
		return nil
	}

	subs := xml.GetMapMulti("tt", "body", "div")
	if subs == nil {
		return nil
	}

	es := &subtitles.ExtSubtitles{Sys: sys}

	for _, f := range subs.GetFields() {
		if f.GetType() == htsmsg.HmfStr && f.GetChilds() != nil {
			n := f.GetChilds()
			txt := f.GetStrValue()

			str, ok := htsmsgGetStr(n, "begin")
			if !ok {
				continue
			}
			start := ttmlTimeExpression(str)
			if start == -1 {
				continue
			}

			str, ok = htsmsgGetStr(n, "end")
			if !ok {
				continue
			}
			end := ttmlTimeExpression(str)
			if end == -1 {
				continue
			}

			esInsertText(es, txt, start, end, 0)
		}
	}
	return es
}

// getSubMplTimestamp — C: get_sub_mpl_timestamp
// Returns bytes consumed past the closing bracket or -1.
func getSubMplTimestamp(buf []byte, start, stop *int, left, right byte) int {
	b := buf
	if len(b) == 0 || b[0] != left {
		return -1
	}
	v, n := strtolB(b[1:])
	if n == 0 {
		return -1
	}
	*start = v
	b = b[1+n:]
	if len(b) < 2 || b[0] != right || b[1] != left {
		return -1
	}
	v, n = strtolB(b[2:])
	if n == 0 {
		return -1
	}
	*stop = v
	b = b[2+n:]
	if len(b) == 0 || b[0] != right {
		return -1
	}
	// C: return b + 1 - buf
	return len(buf) - len(b) + 1
}

// strtolB — C: strtol(b, &end, 10) over bytes; returns value + consumed.
func strtolB(b []byte) (int, int) {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' ||
		b[i] == '\r' || b[i] == '\v' || b[i] == '\f') {
		i++
	}
	neg := false
	if i < len(b) && (b[i] == '+' || b[i] == '-') {
		neg = b[i] == '-'
		i++
	}
	v := 0
	digits := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		v = v*10 + int(b[i]-'0')
		i++
		digits++
	}
	if digits == 0 {
		return 0, 0
	}
	if neg {
		v = -v
	}
	return v, i
}

// isSub — C: is_sub
func isSub(buf []byte) bool {
	var start, stop int
	return getSubMplTimestamp(buf, &start, &stop, '{', '}') != -1
}

// isMpl — C: is_mpl
func isMpl(buf []byte) bool {
	var start, stop int
	return getSubMplTimestamp(buf, &start, &stop, '[', ']') != -1
}

// loadSubVariant — C: load_sub_variant
func loadSubVariant(sys *subtitles.System, url string, buf []byte, fr *mediacore.AVRational,
	mpl bool) *subtitles.ExtSubtitles {
	es := &subtitles.ExtSubtitles{Sys: sys}
	subDefault := mediacore.AVRational{Num: 25, Den: 1}
	mplDefault := mediacore.AVRational{Num: 10, Den: 1}
	var fr0 mediacore.AVRational

	var left, right byte
	if mpl {
		left, right = '[', ']'
	} else {
		left, right = '{', '}'
	}

	if fr == nil || fr.Num == 0 || fr.Den == 0 {
		if mpl {
			fr = &mplDefault
		} else {
			fr = &subDefault
		}
	}

	tagflags := text.TEXT_PARSE_SUB_TAGS
	if mpl {
		tagflags |= text.TEXT_PARSE_SLASH_PREFIX
	}

	// C: LINEPARSE(s, buf) — lp_get over mutable buffer
	lp := buf
	for {
		s := misc.LpGet(&lp)
		if s == nil {
			break
		}
		var start, stop int
		x := getSubMplTimestamp(s, &start, &stop, left, right)
		if x <= 0 {
			continue
		}
		s = s[x:]

		if !mpl && start == 1 && stop == 1 {
			// Set framerate
			frF, _ := misc.MyStr2double(misc.CStr(s))
			fr0.Num = int(frF * 1000000.0)
			fr0.Den = 1000000
			fr = &fr0
			continue
		}

		// C: for(i = 0, len = strlen(s); i < len; i++) — bound at first NUL
		n := len(s)
		if j := bytes.IndexByte(s, 0); j != -1 {
			n = j
		}
		for i := range n {
			if s[i] == '|' {
				s[i] = '\n'
			}
		}

		esInsertText(es, misc.CStr(s),
			1000000*int64(start)*int64(fr.Den)/int64(fr.Num),
			1000000*int64(stop)*int64(fr.Den)/int64(fr.Num),
			tagflags)
	}
	return es
}

// scanfDec — C scanf %wd: skips whitespace, optional sign, up to w digits.
// Returns (value, consumed, ok).
func scanfDec(b []byte, w int) (int, int, bool) {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' ||
		b[i] == '\r' || b[i] == '\v' || b[i] == '\f') {
		i++
	}
	neg := false
	if i < len(b) && (b[i] == '+' || b[i] == '-') {
		neg = b[i] == '-'
		i++
	}
	v := 0
	n := 0
	for i < len(b) && n < w && b[i] >= '0' && b[i] <= '9' {
		v = v*10 + int(b[i]-'0')
		i++
		n++
	}
	if n == 0 {
		return 0, 0, false
	}
	if neg {
		v = -v
	}
	return v, i, true
}

// scanTimestamp8 — C: sscanf(buf, "%02d:%2d:%02d:%02d %02d:%02d:%02d:%02d ",...)
// Returns the 8 ints and whether all matched (literals ':' and ' ' = ws skip).
func scanTimestamp8(b []byte) ([8]int, bool) {
	var s [8]int
	pos := 0
	for i := range 8 {
		v, n, ok := scanfDec(b[pos:], 2)
		if !ok {
			return s, false
		}
		s[i] = v
		pos += n
		if i == 3 || i == 7 {
			// ' ' in scanf format: skip optional whitespace (always succeeds)
			for pos < len(b) && (b[pos] == ' ' || b[pos] == '\t' ||
				b[pos] == '\n' || b[pos] == '\r' || b[pos] == '\v' ||
				b[pos] == '\f') {
				pos++
			}
		} else {
			if pos >= len(b) || b[pos] != ':' {
				return s, false
			}
			pos++
		}
	}
	return s, true
}

// isTxt — C: is_txt
func isTxt(buf []byte) bool {
	_, ok := scanTimestamp8(buf)
	return ok
}

// loadTxtLine — C: load_txt_line
func loadTxtLine(es *subtitles.ExtSubtitles, src []byte, start, stop uint) {
	if len(src) < 24 {
		return
	}
	src = src[24:]

	var dst []byte
	for len(src) > 0 {
		if src[0] < 32 {
			break
		}
		if len(src) > 1 && src[0] == '/' && src[1] == '/' {
			dst = append(dst, '\n')
			src = src[2:]
		} else {
			dst = append(dst, src[0])
			src = src[1:]
		}
	}
	esInsertText(es, string(dst), int64(start)*10000, int64(stop)*10000, 0)
}

// loadTxt — C: load_txt
func loadTxt(sys *subtitles.System, url string, buf []byte) *subtitles.ExtSubtitles {
	es := &subtitles.ExtSubtitles{Sys: sys}
	var lr linereader

	linereaderSetup(&lr, buf)
	for {
		if linereaderNext(&lr) < 0 {
			break
		}

		s, ok := scanTimestamp8(lr.line())
		if !ok {
			continue
		}

		start := uint(s[0]*360000 + s[1]*6000 + s[2]*100 + s[3])
		stop := uint(s[4]*360000 + s[5]*6000 + s[6]*100 + s[7])
		loadTxtLine(es, lr.line(), start, stop)
	}
	return es
}

// scanTimestamp3 — C: sscanf(buf, "%02d:%2d:%02d:", &x, &x, &x)
func scanTimestamp3(b []byte) ([3]int, bool) {
	var s [3]int
	pos := 0
	for i := range 3 {
		v, n, ok := scanfDec(b[pos:], 2)
		if !ok {
			return s, false
		}
		s[i] = v
		pos += n
		if pos >= len(b) || b[pos] != ':' {
			return s, false
		}
		pos++
	}
	return s, true
}

// isTmp — C: is_tmp
func isTmp(buf []byte) bool {
	_, ok := scanTimestamp3(buf)
	return ok
}

// loadTmpLine — C: load_tmp_line
func loadTmpLine(es *subtitles.ExtSubtitles, src []byte, start uint) {
	if len(src) < 9 {
		return
	}
	src = src[9:]

	var dst []byte
	l := len(src)

	delay := int(float64(l) / 14.7)

	for l > 0 {
		if src[0] < 32 {
			break
		}
		if src[0] == '|' {
			dst = append(dst, '\n')
			src = src[1:]
		} else {
			dst = append(dst, src[0])
			src = src[1:]
		}
		l--
	}
	if delay < 2 {
		delay = 2
	}
	esInsertText(es, string(dst), int64(start)*1000000,
		(int64(start)+int64(delay))*1000000, text.TEXT_PARSE_SLASH_PREFIX)
}

// loadTmp — C: load_tmp
func loadTmp(sys *subtitles.System, url string, buf []byte) *subtitles.ExtSubtitles {
	es := &subtitles.ExtSubtitles{Sys: sys}
	var lr linereader

	linereaderSetup(&lr, buf)
	for {
		if linereaderNext(&lr) < 0 {
			break
		}

		s, ok := scanTimestamp3(lr.line())
		if !ok {
			continue
		}

		start := uint(s[0]*3600 + s[1]*60 + s[2])
		loadTmpLine(es, lr.line(), start)
	}
	return es
}

// vocmp — C: vocmp
func vocmp(a, b *mediacore.VideoOverlay) int {
	if a.Start < b.Start {
		return -1
	}
	if a.Start > b.Start {
		return 1
	}
	if a.Stop < b.Stop {
		return -1
	}
	if a.Stop > b.Stop {
		return 1
	}
	return 0
}

// esSort — C: es_sort
func esSort(es *subtitles.ExtSubtitles, trimStop bool) {
	vec := es.Entries

	slices.SortFunc(vec, vocmp)

	if trimStop {
		// Trim so no stop time is higher than next items start time
		for i := range len(vec) - 1 {
			if vec[i].Stop > vec[i+1].Start {
				vec[i].Stop = vec[i+1].Start
			}
		}
	}
}

// convertToUtf8 — C: convert_to_utf8
func convertToUtf8(src *misc.Buf, url string, dcs misc.CharsetDefaultSrc) *misc.Buf {
	b, _how := misc.Utf8FromBytes(src.C8(), src.Len(), nil, dcs)
	_ = _how // C: TRACE(TRACE_INFO, "Subtitles", "%s is not valid UTF-8. %s", url, how)
	src.Release()
	return b
}

// subtitlesCreate — C: subtitles_create
func subtitlesCreate(sys *subtitles.System, path string, buf *misc.Buf,
	fr *mediacore.AVRational, dcs misc.CharsetDefaultSrc) *subtitles.ExtSubtitles {
	trimStop := false
	var s *subtitles.ExtSubtitles

	if isTtml(buf) {
		s = loadTtml(sys, path, buf)
	} else if isTimedtext(buf) {
		s = loadTimedtext(sys, path, buf)
	} else {
		u8 := buf.C8()
		off := 0

		if buf.Len() > 2 && ((u8[0] == 0xff && u8[1] == 0xfe) ||
			(u8[0] == 0xfe && u8[1] == 0xff)) {
			// UTF-16 BOM
			buf = misc.Utf16ToUtf8(buf)
		} else if buf.Len() > 3 &&
			u8[0] == 0xef && u8[1] == 0xbb && u8[2] == 0xbf {
			// UTF-8 BOM
			off = 3
		} else if misc.Utf8Verify(misc.CStr(buf.C8())) != 0 {
			// It's UTF-8 clean (C: utf8_verify on NUL-terminated buf_cstr)
		} else {
			buf = convertToUtf8(buf, path, dcs)
		}

		buf = misc.BufMakeWritable(buf)
		// C: char *b0 = buf_str(buf) + off; int len = buf_len(buf) - off
		b0 := buf.C8()[off:]

		if isSrt(b0) {
			s = loadSrt(sys, path, b0)
		} else if isAss(b0) {
			s = LoadSSA(sys, path, string(b0))
		} else if isSub(b0) {
			s = loadSubVariant(sys, path, b0, fr, false)
		} else if isMpl(b0) {
			s = loadSubVariant(sys, path, b0, nil, true)
		} else if isTxt(b0) {
			s = loadTxt(sys, path, b0)
		} else if isTmp(b0) {
			s = loadTmp(sys, path, b0)
			trimStop = true
		}
		buf.Release()
	}

	if s != nil {
		esSort(s, trimStop)
	}
	return s
}

// subtitlesFromZipfile — C: subtitles_from_zipfile
func subtitlesFromZipfile(mp *mediacore.MediaPipe, b *misc.Buf, dcs misc.CharsetDefaultSrc) *subtitles.ExtSubtitles {
	var ret *subtitles.ExtSubtitles

	fam := mp.FAM
	bm := fam.GetBundleManager()
	id := bm.MemFileRegister(b.C8())
	url := fmt.Sprintf("zip://memfile://%d", id)
	fd, err := fileaccesscore.FAScanDir(fam, url)
	if fd != nil {
		for _, fde := range fd.Entries {
			ret = SubtitlesLoad(mp, fde.URL, dcs)
			if ret != nil {
				break
			}
		}
		fd.Free()
	} else {
		_ = err // C: TRACE(TRACE_ERROR, ..., "Unable to open ZIP -- %s", errbuf)
	}

	bm.MemFileUnregister(id)
	b.Release()
	return ret
}

// faLoad — C: fa_load(url, FA_LOAD_ERRBUF, NULL)
func faLoad(fam *fileaccesscore.FileAccessManager, url string) *fileaccesscore.Buffer {
	fh, err := fileaccesscore.FAOpenEx(fam, url, 0, nil)
	if err != nil {
		return nil
	}
	return fileaccesscore.LoadAndClose(fh)
}

// SubtitlesLoad — C: subtitles_load
func SubtitlesLoad(mp *mediacore.MediaPipe, url string, dcs misc.CharsetDefaultSrc) *subtitles.ExtSubtitles {
	if s, ok := myStrBegins(url, "vobsub:"); ok {
		sub := VobsubLoad(s, mp)
		if sub != nil {
			sub.Sys = subtitles.SystemOf(mp)
		}
		// C: if(sub == NULL) TRACE(..., "Unable to load %s", s)
		return sub
	}

	// C: TRACE(TRACE_DEBUG, "Subtitles", "Trying to load %s", url)
	fab := faLoad(mp.FAM, url)
	if fab == nil {
		return nil
	}

	b := misc.BufCreateAndCopy(fab.Size, fab.Data)

	if b.Len() > 4 && bytes.Equal(b.C8()[:4], []byte{'P', 'K', 3, 4}) {
		// C: TRACE — "%s is a ZIP archive, scanning..."
		return subtitlesFromZipfile(mp, b, dcs)
	}

	if misc.GzCheck(b) != 0 {
		// is .gz compressed, inflate it
		nb, err := misc.GzInflate(b)
		if err != nil {
			return nil
		}
		b = nb
	}

	sub := subtitlesCreate(subtitles.SystemOf(mp), url, b, &mp.Framerate, dcs)
	if sub != nil {
		sub.Sys = subtitles.SystemOf(mp)
	}
	// C: hexdump of first 64 bytes on unknown format — omitted (diagnostic only)
	return sub
}

// SubtitlesProbe — C: subtitles_probe
func SubtitlesProbe(fam *fileaccesscore.FileAccessManager, url string) string {
	fab := faLoad(fam, url)
	if fab == nil {
		return ""
	}
	b := misc.BufCreateAndCopy(fab.Size, fab.Data)
	defer b.Release()

	var ret string
	c8 := b.C8()
	if isTxt(c8) {
		ret = "TXT"
	} else if isMpl(c8) {
		ret = "MPL"
	} else if isSub(c8) {
		ret = "SUB"
	} else if isTmp(c8) {
		ret = "TMP"
	} else if isTimedtext(b) {
		ret = "TimedText"
	}
	return ret
}
