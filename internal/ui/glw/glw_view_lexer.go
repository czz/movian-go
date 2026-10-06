package glw

// C: src/ui/glw/glw_view_lexer.c — canonical 1:1 port.

import (
	"math"
	"strings"

	facore "github.com/czz/movian-go/internal/fileaccess"
	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// C: static void lexer_link_token(token_t *prev, rstr_t *f, int line,
// token_t *t, token_type_t type) (glw_view_lexer.c:26-34)
func lexerLinkToken(prev *Token, f *miscpkg.Rstr, line int, t *Token,
	typ tokenType) {
	t.typ = typ
	prev.next = t

	t.file = miscpkg.RstrDup(f)
	t.line = line
}

// C: static token_t *lexer_add_token_simple(glw_root_t *gr, token_t *prev,
// rstr_t *f, int line, token_type_t type) (glw_view_lexer.c:42-49)
func lexerAddTokenSimple(gr *glwRoot, prev *Token, f *miscpkg.Rstr,
	line int, typ tokenType) *Token {
	t := glwViewTokenAlloc(gr)
	lexerLinkToken(prev, f, line, t, typ)
	return t
}

// C: static token_t *lexer_add_token_string(glw_root_t *gr, token_t *prev,
// rstr_t *f, int line, const char *start, const char *end, token_type_t type)
// (glw_view_lexer.c:55-66)
func lexerAddTokenString(gr *glwRoot, prev *Token, f *miscpkg.Rstr, line int,
	start, end int, src string, typ tokenType) *Token {
	t := glwViewTokenAlloc(gr)
	// C: rstr_allocl(start, end-start) then deescape_cstyle in-place —
	// the rstr buffer has room for the NUL terminator deescape writes.
	b := make([]byte, end-start+1)
	copy(b, src[start:end])
	miscpkg.DeescapeCstyle(b)
	t.tRstring = miscpkg.RstrAllocStr(strings.TrimRight(string(b), "\x00"))

	lexerLinkToken(prev, f, line, t, typ)
	return t
}

// C: static token_t *lexer_add_token_float(glw_root_t *gr, token_t *prev,
// rstr_t *f, int line, const char *start, const char *end)
// (glw_view_lexer.c:72-115)
func lexerAddTokenFloat(gr *glwRoot, prev *Token, f *miscpkg.Rstr, line int,
	start, end int, src string) *Token {
	t := lexerAddTokenSimple(gr, prev, f, line, tokenFloat)
	var sign float32 = 1.0
	var n, s, m int

	if src[start] == '-' {
		start++
		sign = -1.0
	}

	if start == end {
		// A bit strange
		t.tFloat = -1.0
		return t
	}

	n = 0
	for start < end {
		s = int(src[start])
		start++
		if s < '0' || s > '9' {
			break
		}
		n = n*10 + s - '0'
	}

	t.tFloat = float32(n)
	if start == end || s != '.' {
		t.tFloat *= sign
		return t
	}

	n = 0
	for start < end {
		s = int(src[start])
		start++
		if s < '0' || s > '9' {
			break
		}
		n = n*10 + s - '0'
		m++
	}

	t.tFloat += float32(math.Pow(10, float64(-m))) * float32(n)
	t.tFloat *= sign
	return t
}

// C: static token_t *lexer_single_char(glw_root_t *gr, token_t *next,
// rstr_t *f, int line, char s) (glw_view_lexer.c:121-152)
func lexerSingleChar(gr *glwRoot, next *Token, f *miscpkg.Rstr, line int,
	s byte) *Token {
	var ty tokenType
	switch s {
	case '#':
		ty = tokenHash
	case '=':
		ty = tokenAssignment
	case '(':
		ty = tokenLeftParenthesis
	case ')':
		ty = tokenRightParenthesis
	case '[':
		ty = tokenLeftBracket
	case ']':
		ty = tokenRightBracket
	case '{':
		ty = tokenBlockOpen
	case '}':
		ty = tokenBlockClose
	case ';':
		ty = tokenEndOfExpr
	case ',':
		ty = tokenSeparator
	case '.':
		ty = tokenDot
	case '+':
		ty = tokenAdd
	case '-':
		ty = tokenSub
	case '*':
		ty = tokenMultiply
	case '/':
		ty = tokenDivide
	case '%':
		ty = tokenModulo
	case '$':
		ty = tokenDollar
	case '!':
		ty = tokenBooleanNot
	case '&':
		ty = tokenAmpersand
	case '>':
		ty = tokenGt
	case '<':
		ty = tokenLt
	case ':':
		ty = tokenColon
	case '?':
		ty = tokenQuestionmark
	default:
		return nil
	}
	return lexerAddTokenSimple(gr, next, f, line, ty)
}

// C: #define lex_isalpha(v) (glw_view_lexer.c:155-156)
func lexIsalpha(v byte) bool {
	return (v >= 'a' && v <= 'z') || (v >= 'A' && v <= 'Z') || v == '_'
}

// C: #define lex_isdigit(v) (glw_view_lexer.c:158-159)
func lexIsdigit(v byte) bool {
	return (v >= '0' && v <= '9') || v == '-'
}

// C: #define lex_isalnum(v) (glw_view_lexer.c:161)
func lexIsalnum(v byte) bool { return lexIsalpha(v) || lexIsdigit(v) }

// glw_view_lexer.c:170-339 — Do lexical analysis of buffer in 'src'.
// Returns pointer to last token, or NULL if an error occured.
func glwViewLexer(gr *glwRoot, src string, ei *errorinfoT,
	f *miscpkg.Rstr, prev *Token) *Token {
	var start int
	line := 1
	i := 0
	n := len(src)

	c := func(j int) byte { // bounds-safe src[j]
		if j < n {
			return src[j]
		}
		return 0
	}

	for i < n {
		if src[i] == '\n' {
			i++
			line++
			continue
		}

		if src[i] <= 32 {
			i++
			continue
		}

		if c(i) == 'v' && c(i+1) == 'o' && c(i+2) == 'i' && c(i+3) == 'd' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenVoid)
			i += 4
			continue
		}

		if c(i) == 't' && c(i+1) == 'r' && c(i+2) == 'u' && c(i+3) == 'e' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenInt)
			i += 4
			prev.tInt = 1
			continue
		}

		if c(i) == 'f' && c(i+1) == 'a' && c(i+2) == 'l' && c(i+3) == 's' &&
			c(i+4) == 'e' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenInt)
			i += 5
			prev.tInt = 0
			continue
		}

		if c(i) == '/' && c(i+1) == '/' {
			// C++ style comment
			i += 2
			for c(i) != '\n' && i < n {
				i++
			}
			i++
			line++
			continue
		}

		if c(i) == '/' && c(i+1) == '*' {
			// A normal C-comment
			i += 2
			for i < n && !(c(i) == '/' && c(i-1) == '*') {
				if c(i) == '\n' {
					line++
				}
				i++
			}
			i++
			continue
		}

		if c(i) == '&' && c(i+1) == '&' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenBooleanAnd)
			i += 2
			continue
		}
		if c(i) == '?' && c(i+1) == '=' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenCondAssignment)
			i += 2
			continue
		}
		if c(i) == '<' && c(i+1) == '-' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenLinkAssignment)
			i += 2
			continue
		}
		if c(i) == ':' && c(i+1) == '=' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenRefAssignment)
			i += 2
			continue
		}
		if c(i) == '_' && c(i+1) == '=' && c(i+2) == '_' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenDebugAssignment)
			i += 3
			continue
		}
		if c(i) == '|' && c(i+1) == '|' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenBooleanOr)
			i += 2
			continue
		}
		if c(i) == '^' && c(i+1) == '^' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenBooleanXor)
			i += 2
			continue
		}
		if c(i) == '=' && c(i+1) == '=' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenEq)
			i += 2
			continue
		}
		if c(i) == '!' && c(i+1) == '=' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenNeq)
			i += 2
			continue
		}
		if c(i) == '?' && c(i+1) == '?' {
			prev = lexerAddTokenSimple(gr, prev, f, line, tokenNullCoalesce)
			i += 2
			continue
		}

		if !(c(i) == '-' && lexIsdigit(c(i+1))) {
			if t := lexerSingleChar(gr, prev, f, line, c(i)); t != nil {
				i++
				prev = t
				continue
			}
		}

		start = i

		if src[i] == '"' || src[i] == '\'' {
			// A quoted string " ... "
			stop := src[i]
			i++
			start++

			for i < n && (c(i) != stop || (c(i-1) == '\\' && c(i-2) != '\\')) {
				if c(i) == '\n' {
					line++
				}
				i++
			}
			if c(i) != stop {
				ei.error = "Unterminated quote"
				ei.file = miscpkg.RstrGet(f)
				ei.line = line
				return nil
			}

			prev = lexerAddTokenString(gr, prev, f, line, start, i, src,
				tokenRstring)
			if stop == '\'' {
				prev.tRstrType = propcore.PropStrRich
			}
			i++
			continue
		}

		if lexIsalpha(src[i]) {
			// Alphanumeric string
			for i < n && lexIsalnum(src[i]) {
				i++
			}
			prev = lexerAddTokenString(gr, prev, f, line, start, i, src,
				tokenIdentifier)
			continue
		}

		if lexIsdigit(src[i]) {
			// Integer
			for i < n && lexIsdigit(src[i]) {
				i++
			}
			if i < n && src[i] == '.' {
				i++
				for i < n && lexIsdigit(src[i]) {
					i++
				}
			}
			if i < n && src[i] == 'f' {
				i++
			}
			prev = lexerAddTokenFloat(gr, prev, f, line, start, i, src)
			continue
		}

		var ch = ' '
		if src[i] > 31 {
			ch = rune(src[i])
		}
		ei.error = "Invalid char '" + string(ch) + "'"
		ei.file = miscpkg.RstrGet(f)
		ei.line = line
		return nil
	}
	return prev
}

// C: token_t *glw_view_load1(glw_root_t *gr, rstr_t *url, errorinfo_t *ei,
// token_t *prev, int may_unlock) (glw_view_lexer.c:349-385)
func glwViewLoad1(gr *glwRoot, url *miscpkg.Rstr, ei *errorinfoT,
	prev *Token, mayUnlock int) *Token {

	p := glwResolvePath(url, prev.file, gr, nil)

	if mayUnlock != 0 {
		glwUnlock(gr)
	}

	b, lerr := facore.FALoad2(glwDeps.fam, miscpkg.RstrGet(p), nil)

	if mayUnlock != 0 {
		glwLock(gr)
	}

	if b == nil {
		msg := "load failed"
		if lerr != nil {
			msg = lerr.Error()
		}
		ei.error = "Unable to open \"" + miscpkg.RstrGet(p) + "\" -- " + msg
		ei.file = miscpkg.RstrGet(prev.file)
		ei.line = prev.line
		miscpkg.RstrRelease(p)
		return nil
	}

	last := glwViewLexer(gr, string(b.Data), ei, p, prev)
	miscpkg.RstrRelease(p)
	return last
}
