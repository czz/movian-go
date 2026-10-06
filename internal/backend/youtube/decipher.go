// decipher.go — YouTube signature/n-parameter transforms, evaluated
// with the goja runtime already in-tree (the same engine powering
// ecmascript plugins) instead of a hand-rolled interpreter.
//
// base.js layout (minified): the sig cipher is a small function whose
// body is a chain of helper calls on a single var object —
//
//	XX=function(a){a=a.split("");YY.rev(a);YY.swp(a,42);return a.join("")}
//	var YY={rev:function(a){a.reverse()},...}
//
// The n-transform is a self-contained function discovered via the
// .get("n") call site. Both are extracted by balanced-brace scan and
// evaluated verbatim.
package youtube

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dop251/goja"
)

// fetchBaseJS downloads and caches a base.js player script.
func (s *System) fetchBaseJS(jsURL string) (string, error) {
	s.jsCacheMu.Lock()
	if cached, ok := s.jsCache[jsURL]; ok {
		s.jsCacheMu.Unlock()
		return cached, nil
	}
	s.jsCacheMu.Unlock()

	if strings.HasPrefix(jsURL, "/") {
		jsURL = "https://www.youtube.com" + jsURL
	}
	b, code, err := s.httpGet(jsURL)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("base.js: HTTP %d", code)
	}
	js := string(b)
	s.jsCacheMu.Lock()
	s.jsCache[jsURL] = js
	s.jsCacheMu.Unlock()
	return js, nil
}

// extractBalanced returns the brace-balanced source segment starting
// at the '{' at src[pos], honoring "…" '…' `…` strings and backslash
// escapes. Empty on unbalanced input.
func extractBalanced(src string, pos int) string {
	if pos < 0 || pos >= len(src) || src[pos] != '{' {
		return ""
	}
	depth := 0
	var quote byte
	for i := pos; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return src[pos : i+1]
			}
		}
	}
	return ""
}

// extractFuncBody returns the body of `NAME=function(a){…}` or
// `function NAME(a){…}` starting the search after idx.
func extractFuncBody(js, name string) string {
	for _, pat := range []string{
		name + "=function(a){",
		"function " + name + "(a){",
	} {
		i := strings.Index(js, pat)
		if i < 0 {
			continue
		}
		return extractBalanced(js, i+len(pat)-1)
	}
	return ""
}

var (
	cipherNameRe = regexp.MustCompile(`([a-zA-Z0-9$]{2,})=function\(a\)\{a=a\.split`)
	helperRefRe  = regexp.MustCompile(`\b([a-zA-Z0-9$]{2})\.[a-zA-Z0-9$]+\(a`)
	nNameRe      = regexp.MustCompile(`\.get\("n"\)\)&&\(b=([a-zA-Z0-9$]{2,})\(`)
)

// extractCipher pulls the sig-decipher function and the helper var
// object it calls into one evaluatable snippet: "var YY={…};XX=function…"
func extractCipher(js string) (name, src string, err error) {
	m := cipherNameRe.FindStringSubmatchIndex(js)
	if m == nil {
		return "", "", fmt.Errorf("decipher: cipher function not found")
	}
	name = js[m[2]:m[3]]
	body := extractFuncBody(js, name)
	if body == "" {
		return "", "", fmt.Errorf("decipher: empty body for %s", name)
	}
	// Helper object name: first `YY.` call inside the body.
	var helper string
	if hm := helperRefRe.FindStringSubmatch(body); hm != nil {
		helper = hm[1]
	}
	src = ""
	if helper != "" {
		hi := strings.Index(js, "var "+helper+"=")
		if hi < 0 {
			hi = strings.Index(js, helper+"={")
			if hi < 0 {
				return "", "", fmt.Errorf("decipher: helper %s not found", helper)
			}
			src = "var " + helper + "=" + extractBalanced(js, hi+len(helper)+1) + ";"
		} else {
			src = "var " + helper + "=" + extractBalanced(js, hi+len("var "+helper+"=")) + ";"
		}
		if src == "var "+helper+"=;" {
			return "", "", fmt.Errorf("decipher: empty helper %s", helper)
		}
	}
	src += name + "=function(a)" + body
	return name, src, nil
}

// extractN pulls the n-transform function discovered via get("n").
func extractN(js string) (name, src string, err error) {
	m := nNameRe.FindStringSubmatch(js)
	if m == nil {
		return "", "", fmt.Errorf("decipher: n function not found")
	}
	name = m[1]
	body := extractFuncBody(js, name)
	if body == "" {
		return "", "", fmt.Errorf("decipher: empty n body for %s", name)
	}
	return name, name + "=function(a)" + body, nil
}

// evalJS evaluates a small self-contained snippet and calls its named
// function with one string argument.
func evalJS(src, funcName, arg string) (string, error) {
	vm := goja.New()
	if _, err := vm.RunString(src); err != nil {
		return "", fmt.Errorf("decipher: eval: %w", err)
	}
	v := vm.Get(funcName)
	fn, ok := goja.AssertFunction(v)
	if !ok {
		return "", fmt.Errorf("decipher: %s is not a function", funcName)
	}
	res, err := fn(goja.Undefined(), vm.ToValue(arg))
	if err != nil {
		return "", fmt.Errorf("decipher: call %s: %w", funcName, err)
	}
	return res.String(), nil
}

// decipherJSURL was a package global — now threaded as jsURL parameter
// from pickStream through streamURL/decipherURL/applyNThrottling.

var (
	playerJSRe = regexp.MustCompile(`"(/s/player/[a-f0-9]+/[^"?]+\.js)`)
	stsRe      = regexp.MustCompile(`signatureTimestamp:\s*(\d+)`)
)

// playerScript resolves the current base.js URL and its
// signatureTimestamp for a video: the embed page (reachable even when
// Innertube is bot-walled) names the player script, which carries
// signatureTimestamp — required by TVHTML5/WEB /player calls.
func (s *System) playerScript(videoID string) (jsURL, sts string) {
	s.embedJSMu.Lock()
	if v, ok := s.embedJSOnce[videoID]; ok {
		s.embedJSMu.Unlock()
		return v[0], v[1]
	}
	s.embedJSMu.Unlock()

	body, _, err := s.httpGet("https://www.youtube.com/embed/" + videoID)
	if err == nil {
		if m := playerJSRe.FindSubmatch(body); m != nil {
			jsURL = "https://www.youtube.com" + string(m[1])
		}
	}
	if jsURL != "" {
		if js, err := s.fetchBaseJS(jsURL); err == nil {
			if m := stsRe.FindStringSubmatch(js); m != nil {
				sts = m[1]
			}
		}
	}
	s.embedJSMu.Lock()
	s.embedJSOnce[videoID] = [2]string{jsURL, sts}
	s.embedJSMu.Unlock()
	return jsURL, sts
}

// signatureTimestamp returns the sts value of the current player —
// "" when unavailable (the embed page is unreachable).
func (s *System) signatureTimestamp(videoID string) string {
	_, sts := s.playerScript(videoID)
	return sts
}

// decipherSig applies the base.js signature transform.
func (s *System) decipherSig(sig, jsURL string) (string, error) {
	js, err := s.fetchBaseJS(jsURL)
	if err != nil {
		return "", err
	}
	name, src, err := extractCipher(js)
	if err != nil {
		return "", err
	}
	return evalJS(src, name, sig)
}

// nTransform applies the base.js n-parameter transform.
func (s *System) nTransform(n, jsURL string) (string, error) {
	js, err := s.fetchBaseJS(jsURL)
	if err != nil {
		return "", err
	}
	name, src, err := extractN(js)
	if err != nil {
		return "", err
	}
	return evalJS(src, name, n)
}
