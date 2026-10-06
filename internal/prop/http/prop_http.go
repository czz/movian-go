// Package http — C: src/prop/prop_http.c
// /api/prop debug/inspection endpoint over the prop tree.
package http

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	eventcore "github.com/czz/movian-go/internal/event"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
)

var (
	propPM      *propcore.PropManager
	propHTTP    *httpnet.HTTPServer
	propEventMg *eventcore.EventManager
)

// SetDeps wires the prop manager / http server / event manager for the
// /api/prop endpoint. C: INITME(INIT_GROUP_API, prop_http_init).
func SetDeps(pm *propcore.PropManager, s *httpnet.HTTPServer, em *eventcore.EventManager) {
	propPM = pm
	propHTTP = s
	propEventMg = em
}

// propFromPath — C: prop_from_path (prop_http.c:28)
// strvec_split(path,'/') + prop_get_by_name(n, follow_links=1)
func propFromPath(path string) *propcore.Prop {
	n := strings.Split(path, "/")
	// C: prop_get_by_name((const char **)n, 1, NULL) — names[0] is a
	// named root ("global" → prop_global), the rest is Subfind.
	return propPM.GetByName(n, 1, nil, nil)
}

// emitStr — C: emit_str (prop_http.c:38)
func emitStr(out *strings.Builder, htmlOut bool, str string) {
	if htmlOut {
		out.WriteString(html.EscapeString(str)) // C: htsbuf_append_and_escape_xml
	} else {
		out.WriteString(str)
	}
}

// emitValue — C: emit_value (prop_http.c:48)
func emitValue(out *strings.Builder, htmlOut bool, p *propcore.Prop) {
	switch p.GetPropType() {
	case propcore.PropTypeString:
		v := p.GetString()
		if htmlOut && p.GetRstrType() == propcore.PropStrRich {
			out.WriteString(v) // C: rich text emitted unescaped
		} else {
			emitStr(out, htmlOut, v)
		}
	case propcore.PropTypeURI:
		if uv, ok := p.GetRawValue().(propcore.URIValue); ok {
			emitStr(out, htmlOut, uv.Title)
			out.WriteString(" ")
			emitStr(out, htmlOut, uv.URL)
		}
	case propcore.PropTypeFloat:
		fmt.Fprintf(out, "%f", p.GetFloat())
	case propcore.PropTypeInt:
		fmt.Fprintf(out, "%d", p.GetInt())
	case propcore.PropTypeVoid:
		out.WriteString("(void)")
	case propcore.PropTypeZombie:
		out.WriteString("(zombie)")
	case propcore.PropTypeProxy:
		out.WriteString("(proxy)")
	case propcore.PropTypeProp:
		out.WriteString("(prop)")
	case propcore.PropTypeDir:
	}
}

// hcProp — C: hc_prop (prop_http.c:94)
func hcProp(hc *httpnet.HTTPConnection, remain string, opaque any, method httpnet.HTTPCmd) int {
	var out strings.Builder
	var rval int

	htmlOut := false
	if s := hc.HTTPArgGetHdr("accept"); s != "" {
		htmlOut = strings.Contains(s, "text/html")
	}

	if remain == "" {
		return 404
	}
	p := propFromPath(remain)
	if p == nil {
		return 404
	}
	propPM.RefInc(p) // C: prop_get_by_name returns a referenced prop

	name := p.GetName()
	if name == "" {
		name = "<unnamed>"
	}

	switch method {
	case httpnet.HTTPCmdPost:
		if s := hc.HTTPArgGetReq("action"); s != "" {
			e := propEventMg.CreateActionStr(s)
			propPM.SendExtEvent(p, e)
			e.Release()
			rval = httpnet.HTTPStatusOK
			break
		}
		if s := hc.HTTPArgGetReq("debug"); s != "" {
			// C: p->hp_flags |= PROP_DEBUG_THIS / &= ~PROP_DEBUG_THIS
			if s == "on" {
				p.SetDebug(true)
			} else {
				p.SetDebug(false)
			}
			rval = httpnet.HTTPStatusOK
			break
		}
		rval = 400

	case httpnet.HTTPCmdGet:
		if htmlOut {
			out.WriteString("<html><body>")
		}
		fmt.Fprintf(&out, "%s (ref:%d xref:%d) is a ", name,
			p.GetRefCount(), propPM.XrefCount(p))

		if p.GetPropType() == propcore.PropTypeDir {
			out.WriteString("directory\n")
			if htmlOut {
				out.WriteString("<table border=1>\n")
			}
			cnt := 0
			for _, c := range p.GetChildren() {
				var tmp string
				cname := c.GetName()
				ref := cname
				if cname == "" {
					tmp = "*" + strconv.Itoa(cnt)
					ref = tmp
					cname = "<unnamed>"
				}
				if htmlOut {
					out.WriteString("<tr>\n")
					fmt.Fprintf(&out, "<td><a href=\"/api/prop/%s/%s\">", remain, ref)
					out.WriteString(html.EscapeString(cname))
					out.WriteString("</a>\n<td>")
					if c.GetPropType() == propcore.PropTypeDir {
						out.WriteString("dir")
					} else {
						emitValue(&out, htmlOut, c)
					}
					out.WriteString("</tr>\n")
				} else {
					fmt.Fprintf(&out, "  %s\n", cname)
				}
				cnt++
			}
			if htmlOut {
				out.WriteString("</table>\n")
			}
		} else {
			emitValue(&out, htmlOut, p)
			out.WriteString("\n")
		}

		content := "text/plain; charset=utf-8"
		if htmlOut {
			content = "text/html; charset=utf-8"
		}
		rval = hc.HTTPSendReply(0, content, "", "", 0, []byte(out.String()))

	default:
		rval = httpnet.HTTPStatusMethodNotAllowed
	}

	propPM.RefDec(p) // C: prop_ref_dec(p)
	return rval
}

// PropHTTPStart — C: prop_http_init (prop_http.c:271)
// INITME(INIT_GROUP_API). /subtrack is PROP_DEBUG-only in C (not ported).
func PropHTTPStart() {
	if propHTTP == nil || propPM == nil {
		return
	}
	propHTTP.HTTPPathAdd("/api/prop", nil, hcProp, false)
}
