package glw

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

// C: src/ui/glw/glw_view_support.c — token alloc/free, free_chain, seterr.

// glwViewTokenAlloc — C: glw_view_token_alloc (glw_view_support.c:34)
func glwViewTokenAlloc(gr *glwRoot) *Token {
	return (*Token)(gr.grTokenPool.PoolGet())
}

// glwViewTokenFree — C: glw_view_token_free (glw_view_support.c:44)
// It must be delinked for all lists before
func glwViewTokenFree(gr *glwRoot, t *Token) {
	miscpkg.RstrRelease(t.file)

	switch t.typ {
	case tokenFunction:
		if t.tFunc.ctor != nil {
			t.tFunc.dtor(gr, t)
		}

	case tokenPropertyRef:
		glwDeps.pm.RefDec(t.tProp)

	case tokenPropertyOwner:
		glwDeps.pm.Destroy(t.tProp)

	case tokenRstring, tokenIdentifier, tokenUnresolvedAttribute:
		miscpkg.RstrRelease(t.tRstring)

	case tokenPropertyName:
		for i := range t.tElements {
			miscpkg.RstrRelease(t.tPnvec[i])
		}

	case tokenGem:
		glwEventMapDestroy(gr, t.tGem)

	case tokenEvent:
		if t.tEvent != nil {
			t.tEvent.Release() // C: event_release(t->t_event)
			t.tEvent = nil
		}

	case tokenURI:
		miscpkg.RstrRelease(t.tURITitle)
		miscpkg.RstrRelease(t.tURI)

	default:
		// C: all other types (operators, punctuation, literals)
		// fall through to break — TOKEN_FLOAT..TOKEN_CSTRING plus
		// TOKEN_EXPR/RPN/BLOCK/NOP/COLON/?/TENARY/VECTOR/MOD_FLAGS.
		break
	}

	gr.grTokenPool.PoolPut(unsafe.Pointer(t))
}

// glwViewFreeChain2 — C: glw_view_free_chain2 (glw_view_support.c:287)
func glwViewFreeChain2(gr *glwRoot, t *Token, indent int) {
	var n *Token
	for ; t != nil; t = n {
		n = t.next
		if t.child != nil {
			glwViewFreeChain2(gr, t.child, indent+2)
		}
		glwViewTokenFree(gr, t)
	}
}

// glwViewFreeChain — C: glw_view_free_chain (glw_view_support.c:308)
func glwViewFreeChain(gr *glwRoot, t *Token) {
	glwViewFreeChain2(gr, t, 0)
}

// glwViewSeterr — C: glw_view_seterr (glw_view_support.c:485)
// Records an error and returns -1.
func glwViewSeterr(ei *errorinfoT, b *Token, format string,
	args ...any) int {
	if ei == nil {
		ei = &errorinfoT{}
	}
	ei.error = fmt.Sprintf(format, args...)
	ei.file = miscpkg.RstrGet(b.file)
	ei.line = b.line
	glwDeps.ts.Trace(trace.TRACE_ERROR, "GLW", "Error %s:%d: %s",
		miscpkg.RstrGet(b.file), b.line, ei.error)
	return -1
}

// glwPropnameToArray — C: glw_propname_to_array (glw_view_support.c)
// Walks t->child filling pname[i] = rstr_get(t_pnvec[j]).
func glwPropnameToArray(pname *[16]string, a *Token) {
	i := 0
	for t := a; t != nil && i < 15; t = t.child {
		for j := 0; j < t.tElements && i < 15; j++ {
			pname[i] = miscpkg.RstrGet(t.tPnvec[j])
			i++
		}
	}
}

// glwViewTokenCopy — C: glw_view_token_copy (glw_view_support.c:155)
// Clone a token.
func glwViewTokenCopy(gr *glwRoot, src *Token) *Token {
	dst := (*Token)(gr.grTokenPool.PoolGet())

	dst.file = miscpkg.RstrDup(src.file)
	dst.line = src.line

	dst.typ = src.typ
	// C: dst->t_propsubr = src->t_propsubr copies the union word —
	// t_attrib and t_prop_name_id alias the same storage.
	dst.tPropsubr = src.tPropsubr
	dst.tAttrib = src.tAttrib
	dst.tPropNameID = src.tPropNameID
	dst.tFlags = src.tFlags

	switch src.typ {
	case tokenFloat, tokenEm:
		dst.tFloat = src.tFloat

	case tokenModFlags:
		dst.tClr = src.tClr
		// C: u.ival aliases u.modflags.set — the TOKEN_INT fallthrough
		// copies 'set'; flattened fields need it copied explicitly.
		dst.tSet = src.tSet
		fallthrough
	case tokenInt:
		dst.tInt = src.tInt

	case tokenPropertyRef:
		dst.tProp = glwDeps.pm.RefInc(src.tProp)

	case tokenPropertyOwner:
		dst.tProp = glwDeps.pm.XrefAddref(src.tProp)

	case tokenPropertySubscription, tokenDirectory:

	case tokenFunction:
		dst.tFunc = src.tFunc
		dst.tFuncArg = src.tFuncArg
		if dst.tFunc.ctor != nil {
			dst.tFunc.ctor(dst)
		}
		fallthrough
	case tokenLeftBracket:
		dst.tNumArgs = src.tNumArgs

	case tokenResolvedAttribute:
		dst.tAttrib = src.tAttrib

	case tokenCstring:
		dst.tCstring = src.tCstring

	case tokenRstring:
		dst.tRstrType = src.tRstrType
		fallthrough
	case tokenIdentifier, tokenUnresolvedAttribute:
		dst.tRstring = miscpkg.RstrDup(src.tRstring)

	case tokenPropertyName:
		for i := range src.tElements {
			dst.tPnvec[i] = miscpkg.RstrDup(src.tPnvec[i])
		}
		dst.tElements = src.tElements

	case tokenRpn:
		dst.setRpnOrigin(src.getRpnOrigin())

	case tokenURI:
		dst.tURITitle = miscpkg.RstrDup(src.tURITitle)
		dst.tURI = miscpkg.RstrDup(src.tURI)

	case tokenVectorFloat, tokenGem, tokenEvent, tokenVector, tokenNum:
		panic("glw_view_token_copy: uncopyable token type")

	default:
		// operators/punctuation carry no payload
	}
	return dst
}

// glwViewCloneChain — C: glw_view_clone_chain (glw_view_support.c:318)
func glwViewCloneChain(gr *glwRoot, src *Token, lp **Token) *Token {
	var r *Token
	pp := &r

	for ; src != nil; src = src.next {
		d := glwViewTokenCopy(gr, src)
		*pp = d
		pp = &d.next
		if lp != nil {
			*lp = d
		}
		d.child = glwViewCloneChain(gr, src.child, nil)
	}
	return r
}

// token2name — C: token2name (glw_view_support.c:340)
func token2name(t *Token) string {
	if t == nil {
		return "(null)"
	}
	switch t.typ {
	case tokenStart:
		return "<start>"
	case tokenEnd:
		return "<end>"
	case tokenEndOfExpr:
		return ";"
	case tokenBlockOpen:
		return "{"
	case tokenBlockClose:
		return "}"
	case tokenSeparator:
		return ","
	case tokenQuestionmark:
		return "?"
	case tokenColon:
		return ":"
	case tokenTenary:
		return "tenary"
	case tokenLeftParenthesis:
		return "("
	case tokenRightParenthesis:
		return ")"
	case tokenRstring:
		return miscpkg.RstrGet(t.tRstring)
	case tokenCstring:
		return t.tCstring
	case tokenDot:
		return "."
	case tokenHash:
		return "#"
	case tokenAdd:
		return "+"
	case tokenSub:
		return "-"
	case tokenMultiply:
		return "*"
	case tokenDivide:
		return "/"
	case tokenBooleanNot:
		return "NOT"
	case tokenBooleanAnd:
		return "AND"
	case tokenBooleanOr:
		return "OR"
	case tokenBooleanXor:
		return "XOR"
	case tokenBlock:
		return "<block>"
	case tokenExpr:
		return "<infix expr>"
	case tokenRpn:
		return "<rpn>"
	case tokenPureRpn:
		return "<pure-rpn>"
	case tokenNop:
		return "<nop>"
	case tokenGt:
		return ">"
	case tokenLt:
		return "<"
	case tokenFunction:
		return fmt.Sprintf("%s()", t.tFunc.name)
	case tokenPropertySubscription:
		return "property subscription"
	case tokenPropertyRef:
		return "property ref"
	case tokenPropertyName:
		var buf strings.Builder
		buf.WriteString("<property> ")
		for i := range t.tElements {
			buf.WriteString(fmt.Sprintf("%s ", miscpkg.RstrGet(t.tPnvec[i])))
		}
		return buf.String()
	case tokenResolvedAttribute:
		return fmt.Sprintf("%s:", t.tAttrib.name)
	case tokenUnresolvedAttribute:
		return fmt.Sprintf("%s:", miscpkg.RstrGet(t.tRstring))
	case tokenIdentifier:
		return miscpkg.RstrGet(t.tRstring)
	case tokenAssignment:
		return "="
	case tokenFloat:
		return fmt.Sprintf("%ff", t.tFloat)
	case tokenEm:
		return fmt.Sprintf("%fem", t.tFloat)
	case tokenInt:
		return strconv.Itoa(t.tInt)
	case tokenVoid:
		return "(void)"
	case tokenVectorFloat:
		var buf strings.Builder
		buf.WriteString("[")
		for i := range t.tElements {
			buf.WriteString(fmt.Sprintf("%f ", t.tFloatVector[i]))
		}
		return buf.String() + "]"
	case tokenLeftBracket:
		return "["
	case tokenRightBracket:
		return "]"
	case tokenDirectory:
		return "<directory>"
	case tokenURI:
		return fmt.Sprintf("Link<%s, %s>",
			miscpkg.RstrGet(t.tURITitle), miscpkg.RstrGet(t.tURI))
	case tokenVector:
		return "[]"
	default:
		return fmt.Sprintf("Tokentype<%d>", t.typ)
	}
}

// glwViewPrintTree — C: glw_view_print_tree (glw_view_support.c:465)
func glwViewPrintTree(f *Token, indent int) {
	for c := f; c != nil; c = c.next {
		glwDeps.ts.Trace(trace.TRACE_DEBUG, "GLW",
			"%*.s%s %p (%s:%d)\n", indent, "", token2name(c), c,
			miscpkg.RstrGet(c.file), c.line)

		if c.child != nil {
			glwViewPrintTree(c.child, indent+4)
		}
	}
}
