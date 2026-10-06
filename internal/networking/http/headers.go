package http

// Canonical port of src/networking/http.c — the http_header_list API,
// http_ctime/http_asctime, http_parse_uri_args and http_read_line.
//
// NOTE: This is the C-faithful list type — distinct from the HTTPHeaders
// slice in http_client.go (which has different append semantics).
// http_header_list is a LIST_HEAD: add with append=0 replaces the value
// of a same-named (case-insensitive) entry or inserts a NEW entry at the
// head; append=1 always inserts a new head entry (duplicates allowed —
// e.g. multiple Set-Cookie lines).

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// HTTPHeaderEntry — C: http_header_t (networking/http.h:48-52).
type HTTPHeaderEntry struct {
	Key   string // C: hh_key
	Value string // C: hh_value — "" models NULL
}

// HTTPHeaderList — C: struct http_header_list (LIST_HEAD of
// http_header). Entry order is significant: newest entries are at the
// head unless append-mode inserts further duplicates.
type HTTPHeaderList struct {
	List []*HTTPHeaderEntry
}

// HTTPHeadersFree — C: http_headers_free (http.c:33-46).
func (l *HTTPHeaderList) Free() {
	if l == nil {
		return
	}
	l.List = nil
}

// find returns the first entry matching key case-insensitively.
func (l *HTTPHeaderList) find(key string) *HTTPHeaderEntry {
	for _, hh := range l.List {
		if strings.EqualFold(hh.Key, key) {
			return hh
		}
	}
	return nil
}

// AddAlloced — C: http_header_add_alloced (http.c:53-78). The value is
// adopted (Go strings are immutable — no ownership semantics needed).
func (l *HTTPHeaderList) AddAlloced(key, value string, appendMode bool) {
	var hh *HTTPHeaderEntry
	if !appendMode {
		hh = l.find(key)
	}
	if hh == nil {
		hh = &HTTPHeaderEntry{Key: key}
		l.List = slices.Insert(l.List, 0, hh)
	}
	hh.Value = value
}

// Add — C: http_header_add (http.c:81-87). A "" value models NULL.
func (l *HTTPHeaderList) Add(key, value string, appendMode bool) {
	l.AddAlloced(key, value, appendMode)
}

// AddLWS — C: http_header_add_lws (http.c:91-107). Appends a
// line-continuation word to the FIRST entry's value.
func (l *HTTPHeaderList) AddLWS(data string) {
	if len(l.List) == 0 || l.List[0] == nil || l.List[0].Value == "" {
		return
	}
	l.List[0].Value += " " + data
}

// AddInt — C: http_header_add_int (http.c:110-117).
func (l *HTTPHeaderList) AddInt(key string, value int) {
	l.Add(key, strconv.Itoa(value), false)
}

// Get — C: http_header_get (http.c:123-134). Returns (value, found) —
// Go needs the bool to distinguish NULL from an empty value.
func (l *HTTPHeaderList) Get(key string) (string, bool) {
	if l == nil {
		return "", false
	}
	if hh := l.find(key); hh != nil {
		return hh.Value, true
	}
	return "", false
}

// Merge — C: http_header_merge (http.c:138-156). For each src entry:
// replace dst's same-named entry value, or add a new head entry.
func (l *HTTPHeaderList) Merge(src *HTTPHeaderList) {
	if src == nil {
		return
	}
	for _, hhs := range src.List {
		if hhd := l.find(hhs.Key); hhd != nil {
			hhd.Value = hhs.Value
		} else {
			l.Add(hhs.Key, hhs.Value, false)
		}
	}
}

var httpMonths = [12]string{
	"Jan", "Feb", "Mar", "Apr", "May", "Jun",
	"Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

var httpWeekdays = [7]string{
	"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// HTTPCtime — C: http_ctime (http.c:170-194). Parses
// "Www, DD Mon YYYY HH:MM:SS" via sscanf("%3s, %d%c%3s%c%d %d:%d:%d")
// — the %c verbs swallow any single separator char (so "21-Oct-2015"
// works too). Returns the UTC time; r != 0 on failure.
func HTTPCtime(d string) (time.Time, int) {
	// %3s — weekday (up to 3 non-space chars)
	i := 0
	for i < len(d) && i < 3 && d[i] != ' ' {
		i++
	}
	if i == 0 || i >= len(d) {
		return time.Time{}, -1
	}
	// ", " literal — sscanf requires ',' then space
	if d[i] != ',' {
		return time.Time{}, -1
	}
	i++
	for i < len(d) && d[i] == ' ' {
		i++
	}
	// %d%c — mday + separator char (scanf %d skips whitespace)
	for i < len(d) && d[i] == ' ' {
		i++
	}
	mday, n := atoiPrefix(d[i:])
	if n == 0 || i+n >= len(d) {
		return time.Time{}, -1
	}
	i += n + 1
	// %3s%c — month + separator char
	if i+3 > len(d) {
		return time.Time{}, -1
	}
	month := d[i : i+3]
	i += 3
	if i >= len(d) {
		return time.Time{}, -1
	}
	i++
	// %d — year (%d skips whitespace)
	for i < len(d) && d[i] == ' ' {
		i++
	}
	year, n := atoiPrefix(d[i:])
	if n == 0 {
		return time.Time{}, -1
	}
	// ' ' in format skips whitespace, then %d:%d:%d
	for i < len(d) && d[i] == ' ' {
		i++
	}
	hour, n := atoiPrefix(d[i:])
	if n == 0 {
		return time.Time{}, -1
	}
	i += n
	if i >= len(d) || d[i] != ':' {
		return time.Time{}, -1
	}
	i++
	min, n := atoiPrefix(d[i:])
	if n == 0 {
		return time.Time{}, -1
	}
	i += n
	if i >= len(d) || d[i] != ':' {
		return time.Time{}, -1
	}
	i++
	sec, n := atoiPrefix(d[i:])
	if n == 0 {
		return time.Time{}, -1
	}

	mi := -1
	for j := range 12 {
		if strings.EqualFold(httpMonths[j], month) {
			mi = j
			break
		}
	}
	// C: mktime_utc(tp, year, i, mday, hour, min, sec) — i stays 12 when
	// the month is unrecognized (mktime normalizes mon=12 → next year);
	// Go's time.Date normalizes month 13 the same way.
	return time.Date(year, time.Month(mi+1), mday, hour, min, sec, 0,
		time.UTC), 0
}

// atoiPrefix parses leading decimal digits; returns (value, bytes).
func atoiPrefix(s string) (int, int) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 {
		return 0, 0
	}
	v, _ := strconv.Atoi(s[:n])
	return v, n
}

// ParseURIArgs — C: http_parse_uri_args (http.c:218-243). Parses
// "k=v&k=v" (URL-escaped) into the list.
func (l *HTTPHeaderList) ParseURIArgs(args string, appendMode bool) {
	for args != "" {
		eq := strings.IndexByte(args, '=')
		if eq == -1 {
			break
		}
		k := args[:eq]
		args = args[eq+1:]
		v := args
		if amp := strings.IndexByte(args, '&'); amp != -1 {
			v = args[:amp]
			args = args[amp+1:]
		} else {
			args = ""
		}
		kb := urlDeescapeInPlace([]byte(k))
		vb := urlDeescapeInPlace([]byte(v))
		l.Add(string(kb), string(vb), appendMode)
	}
}

// urlDeescapeInPlace — C: url_deescape — decodes %XX, returning the
// decoded prefix.
func urlDeescapeInPlace(b []byte) []byte {
	r := 0
	for w := 0; w < len(b); w++ {
		if b[w] == '%' && w+2 < len(b) {
			hi := hexval(b[w+1])
			lo := hexval(b[w+2])
			if hi >= 0 && lo >= 0 {
				b[r] = byte(hi<<4 | lo)
				w += 2
				r++
				continue
			}
		}
		b[r] = b[w]
		r++
	}
	return b[:r]
}

func hexval(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
