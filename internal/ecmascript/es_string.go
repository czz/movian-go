// Canonical 1:1 port of src/ecmascript/es_string.c
//
// String/charset/URL helpers exposed as the "native/string" module.
package ecmascript

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/misc"
	http "github.com/czz/movian-go/internal/networking/http"
)

// ---------------------------------------------------------------------------
// es_is_utf8_duk — C: es_string.c:26-39
// ---------------------------------------------------------------------------

func esIsUtf8Gaf(gaf *gaftape.Context) int {
	if gaf.IsBuffer(0) {
		bytes := gaf.RequireBufferData(0)
		gaf.PushLstring(string(bytes), len(bytes))
	}

	str := gaf.RequireString(-1)
	v := misc.Utf8Verify(str)
	gaf.Pop()
	gaf.PushBoolean(v != 0)
	return 1
}

// ---------------------------------------------------------------------------
// es_utf8_from_bytes_auto — C: es_string.c:45-97
// Autodetect character encoding (HTML meta sniffing).
// ---------------------------------------------------------------------------

// esCharsetSrc — C: i18n_get_default_charset() reached from es_string.c.
// Resolved through the injected i18n instance (esEnv.i18n).
func esCharsetSrc() misc.CharsetDefaultSrc {
	if esEnv.i18n == nil {
		return nil
	}
	return esEnv.i18n
}

func esUtf8FromBytesAuto(gaf *gaftape.Context, bufstart []byte) *misc.Buf {
	var cs *misc.Charset

	bufsize := len(bufstart)
	bufend := bufsize

	if start := bytes.Index(bufstart,
		[]byte("<meta http-equiv=\"")); start >= 0 {
		start += len("<meta http-equiv=\"")
		if rel := bytes.IndexByte(bufstart[start+1:], '>'); rel >= 0 {
			end := start + 1 + rel
			_ = bufend
			length := end - start
			if length < 1024 {
				cpy := make([]byte, length+1)
				copy(cpy, bufstart[start:end])
				cpy[length] = 0
				if strings.EqualFold(string(cpy[:12]), "content-type") {
					_, after, ok := bytes.Cut(cpy, []byte("charset="))
					if ok {
						charset := string(after)
						if e := strings.IndexByte(charset, '"'); e >= 0 {
							charset = charset[:e]
							if strings.EqualFold(charset, "utf-8") ||
								strings.EqualFold(charset, "utf8") {
								x := make([]byte, bufsize+1)
								copy(x, bufstart)
								x[bufsize] = 0
								rbuf := misc.Utf8Cleanup(string(x[:bufsize]))

								return misc.BufCreateAndCopy(len(rbuf),
									[]byte(rbuf))
							} else {
								cs = misc.CharsetGet(charset)
							}
						}
					}
				}
			}
		}
	}

	b, _ := misc.Utf8FromBytes(bufstart, bufsize, cs, esCharsetSrc())
	return b
}

// ---------------------------------------------------------------------------
// es_utf8_from_bytes_duk — C: es_string.c:101-141
// ---------------------------------------------------------------------------

func esUtf8FromBytesGaf(gaf *gaftape.Context) int {
	bytes_ := gaf.RequireBufferData(0)
	size := len(bytes_)

	if !gaf.IsString(1) {
		b := esUtf8FromBytesAuto(gaf, bytes_)
		if b != nil {
			gaf.PushLstring(string(b.C8()[:b.Size()]), b.Size())
			b.Release()
		} else {
			gaf.PushUndefined()
		}
		return 1
	}

	csname := gaf.SafeToString(1)

	if strings.EqualFold(csname, "utf-8") ||
		strings.EqualFold(csname, "utf8") {

		gaf.PushLstring(string(bytes_), size)
		str := gaf.RequireString(-1)

		n := misc.Utf8Cleanup(str)
		if n != "" {
			gaf.Pop()
			gaf.PushString(n)
		}
		return 1
	}

	cs := misc.CharsetGet(csname)
	if cs == nil {
		gaf.Error(gaftape.GAF_ERR_ERROR,
			"Unknown character encoding %s", csname)
	}

	if bytes_ == nil {
		gaf.PushUndefined()
		return 1
	}

	b, _ := misc.Utf8FromBytes(bytes_, size, cs, esCharsetSrc())
	gaf.Pop()
	if b != nil {
		gaf.PushString(string(b.C8()[:b.Size()]))
		b.Release()
	} else {
		gaf.PushUndefined()
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_entitydecode — C: es_string.c:147-155
// ---------------------------------------------------------------------------

func esEntitydecode(ctx *gaftape.Context) int {
	out := []byte(ctx.SafeToString(0) + "\x00")
	misc.HtmlEntitiesDecode(out)
	ctx.PushString(strings.TrimRight(string(out), "\x00"))
	return 1
}

// ---------------------------------------------------------------------------
// es_queryStringSplit_internal — C: es_string.c:161-188
// ---------------------------------------------------------------------------

func esQueryStringSplitInternal(ctx *gaftape.Context, str string) {
	ctx.PushObject()

	s := str
	for s != "" {
		k := s
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		v := s[eq+1:]
		rest := ""
		if amp := strings.IndexByte(v, '&'); amp >= 0 {
			rest = v[amp+1:]
			v = v[:amp]
		}
		k = s[:eq]
		s = rest

		kb := append([]byte(k), 0)
		vb := append([]byte(v), 0)
		misc.UrlDeescape(kb)
		misc.UrlDeescape(vb)
		ks := strings.TrimRight(string(kb), "\x00")
		vs := strings.TrimRight(string(vb), "\x00")

		ctx.PushString(vs)
		ctx.PutPropString(-2, ks)
	}
}

// es_queryStringSplit — C: es_string.c:194-199
func esQueryStringSplit(ctx *gaftape.Context) int {
	esQueryStringSplitInternal(ctx, ctx.SafeToString(0))
	return 1
}

// ---------------------------------------------------------------------------
// es_escape / es_pathEscape / es_paramEscape — C: es_string.c:206-234
// ---------------------------------------------------------------------------

func esEscape(ctx *gaftape.Context, how int) int {
	str := ctx.SafeToString(0)

	length := misc.UrlEscape(nil, 0, []byte(str), how)
	r := make([]byte, length)
	misc.UrlEscape(r, length, []byte(str), how)

	ctx.PushLstring(string(r[:length-1]), length-1)
	return 1
}

func esPathEscape(ctx *gaftape.Context) int {
	return esEscape(ctx, misc.URLEscapePath)
}

func esParamEscape(ctx *gaftape.Context) int {
	return esEscape(ctx, misc.URLEscapeParam)
}

// ---------------------------------------------------------------------------
// es_durationtostring — C: es_string.c:239-251
// ---------------------------------------------------------------------------

func esDurationtostring(ctx *gaftape.Context) int {
	s := int(ctx.ToUint(0))
	m := s / 60
	h := s / 3600
	var tmp string
	if h > 0 {
		tmp = sprintfDuration1(h, m, s)
	} else {
		tmp = sprintfDuration2(m, s)
	}
	ctx.PushString(tmp)
	return 1
}

// ---------------------------------------------------------------------------
// es_parseTime — C: es_string.c:257-266
// ---------------------------------------------------------------------------

func esParseTime(ctx *gaftape.Context) int {
	str := ctx.RequireString(0)
	t, r := http.HTTPCtime(str)
	if r != 0 {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Invalid time: %s", str)
	}
	ctx.PushNumber(float64(t.Unix()) * 1000)
	return 1
}

// ---------------------------------------------------------------------------
// es_parseURL — C: es_string.c:272-343
// ---------------------------------------------------------------------------

func esParseURL(ctx *gaftape.Context) int {
	str := ctx.RequireString(0)
	parseq := ctx.GetBoolean(1)

	proto := make([]byte, 64)
	auth := make([]byte, 512)
	hostname := make([]byte, 512)
	port := -1
	path := make([]byte, 8192)
	misc.UrlSplit(proto, len(proto)-1,
		auth, len(auth),
		hostname, len(hostname),
		&port,
		path, len(path), str)

	protoS := misc.CStr(proto)
	authS := misc.CStr(auth)
	hostS := misc.CStr(hostname)
	pathS := misc.CStr(path)

	ctx.PushObject()

	ctx.PushString(protoS + ":")
	ctx.PutPropString(-2, "protocol")

	ctx.PushString(hostS)
	ctx.PutPropString(-2, "hostname")

	if authS != "" {
		ctx.PushString(authS)
		ctx.PutPropString(-2, "auth")
	}

	if port != -1 {
		ctx.PushInt(port)
		ctx.PutPropString(-2, "port")
	}

	if hi := strings.LastIndexByte(pathS, '#'); hi >= 0 {
		hash := pathS[hi+1:]
		pathS = pathS[:hi]
		ctx.PushString(hash)
		ctx.PutPropString(-2, "hash")
	}

	ctx.PushString(pathS)
	ctx.PutPropString(-2, "path")

	if qi := strings.IndexByte(pathS, '?'); qi >= 0 {
		ctx.PushString(pathS[qi:])
		ctx.PutPropString(-2, "search")
		query := pathS[qi+1:]
		pathS = pathS[:qi]

		if parseq {
			esQueryStringSplitInternal(ctx, query)
		} else {
			ctx.PushObject()
		}
		ctx.PutPropString(-2, "query")
	} else {
		if parseq {
			ctx.PushObject()
			ctx.PutPropString(-2, "query")
			ctx.PushString("")
			ctx.PutPropString(-2, "search")
		}
	}

	ctx.PushString(pathS)
	ctx.PutPropString(-2, "pathname")

	return 1
}

// ---------------------------------------------------------------------------
// es_resolveURL — C: es_string.c:361-370
// ---------------------------------------------------------------------------

func esResolveURL(ctx *gaftape.Context) int {
	base := ctx.RequireString(0)
	url := ctx.RequireString(1)
	newurl := misc.UrlResolveRelativeFromBase(base, url)
	ctx.PushString(newurl)
	return 1
}

// ---------------------------------------------------------------------------
// fnlist + module — C: es_string.c:375-389
// ---------------------------------------------------------------------------

var fnlistString = []gaftape.FunctionListEntry{
	{Key: "isUtf8", Value: esIsUtf8Gaf, Nargs: 1},
	{Key: "utf8FromBytes", Value: esUtf8FromBytesGaf, Nargs: 2},
	{Key: "entityDecode", Value: esEntitydecode, Nargs: 1},
	{Key: "queryStringSplit", Value: esQueryStringSplit, Nargs: 1},
	{Key: "pathEscape", Value: esPathEscape, Nargs: 1},
	{Key: "paramEscape", Value: esParamEscape, Nargs: 1},
	{Key: "durationToString", Value: esDurationtostring, Nargs: 1},
	{Key: "parseTime", Value: esParseTime, Nargs: 1},
	{Key: "parseURL", Value: esParseURL, Nargs: 2},
	{Key: "resolveURL", Value: esResolveURL, Nargs: 2},
}

// ES_MODULE("string", fnlist_string)
func registerEsString() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "string",
		Functions: fnlistString,
	})
}

func sprintfDuration1(h, m, s int) string {
	return fmt.Sprintf("%d:%02d:%02d", h, m%60, s%60)
}
func sprintfDuration2(m, s int) string {
	return fmt.Sprintf("%d:%02d", m%60, s%60)
}
