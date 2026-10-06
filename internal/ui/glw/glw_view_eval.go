package glw

// C: src/ui/glw/glw_view_eval.c — canonical 1:1 port (core engine).

import (
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// C: eval stack (glw_view_eval.c:281-320)

// C: eval_push
func evalPush(ec *glwViewEvalContext, t *Token) {
	t.tmp = unsafe.Pointer(ec.stack)
	ec.stack = t
}

// C: eval_pop
func evalPop(ec *glwViewEvalContext) *Token {
	r := ec.stack
	if r != nil {
		ec.stack = (*Token)(r.tmp)
	}
	return r
}

// C: eval_alloc
func evalAlloc(src *Token, ec *glwViewEvalContext, typ tokenType) *Token {
	r := glwViewTokenAlloc(ec.gr)
	if src.file != nil {
		r.file = miscpkg.RstrDup(src.file)
	}
	r.line = src.line
	r.typ = typ
	r.next = ec.alloc
	ec.alloc = r
	return r
}

// ---------------------------------------------------------------------------
// C: token_resolve_ex (glw_view_eval.c:323-351)
func tokenResolveEx(ec *glwViewEvalContext, t *Token, typ int) *Token {
	if t == nil {
		glwViewSeterr(ec.ei, t, "Missing operand")
		return nil
	}

	if (t.typ == tokenPropertyName || t.typ == tokenPropertyRef) &&
		subscribeProp(ec, t, typ) != 0 {
		return nil
	}

	if t.typ != tokenPropertySubscription {
		return t
	}

	gps := t.tPropsubr
	var r *Token
	ec.dynamicEval |= GLW_VIEW_EVAL_PROP
	if gps.gpsType == gpsValueSlave {
		r = gps.gpsMaster.gpsToken
	} else {
		r = gps.gpsToken
	}
	if r == nil {
		r = evalAlloc(t, ec, tokenVoid)
	}
	return r
}

// C: token_resolve (glw_view_eval.c:354-358)
func tokenResolve(ec *glwViewEvalContext, t *Token) *Token {
	return tokenResolveEx(ec, t, gpsValue)
}

// ---------------------------------------------------------------------------
// C: eval_op_* (glw_view_eval.c:361-370)
func evalOpFadd(a, b float32) float32 { return a + b }

func evalOpFsub(a, b float32) float32 { return a - b }

func evalOpFmul(a, b float32) float32 { return a * b }

func evalOpFdiv(a, b float32) float32 { return a / b }

func evalOpFmod(a, b float32) float32 { return float32(int(a) % int(b)) }

func evalOpIadd(a, b int) int { return a + b }

func evalOpIsub(a, b int) int { return a - b }

func evalOpImul(a, b int) int { return a * b }

func evalOpImod(a, b int) int { return a % b }

// C: token_as_string (glw_view_eval.c:376-384)
func tokenAsString(t *Token) (string, bool) {
	if t.typ == tokenRstring || t.typ == tokenURI {
		return miscpkg.RstrGet(t.tRstring), true
	}
	if t.typ == tokenCstring {
		return t.tCstring, true
	}
	return "", false
}

// C: token2int (glw_view_eval.c:392-409)
func token2int(ec *glwViewEvalContext, t *Token) int {
	switch t.typ {
	case tokenInt:
		return t.tInt
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		return int(float32(ec.w.glwRoot.grCurrentSize) * t.tFloat)
	case tokenFloat:
		return int(t.tFloat)
	default:
		return 0
	}
}

// C: token2float (glw_view_eval.c:412-429)
func token2float(ec *glwViewEvalContext, t *Token) float32 {
	switch t.typ {
	case tokenInt:
		return float32(t.tInt)
	case tokenEm:
		ec.dynamicEval |= GLW_VIEW_EVAL_EM
		return float32(ec.w.glwRoot.grCurrentSize) * t.tFloat
	case tokenFloat:
		return t.tFloat
	default:
		return 0
	}
}

// C: token_floatish (glw_view_eval.c:432-439)
func tokenFloatish(t *Token) bool {
	return t.typ == tokenFloat || t.typ == tokenInt || t.typ == tokenEm
}

// C: token2bool (glw_view_eval.c:445-467)
func token2bool(t *Token) bool {
	switch t.typ {
	case tokenVoid:
		return false
	case tokenRstring:
		return len(miscpkg.RstrGet(t.tRstring)) > 0
	case tokenCstring:
		return len(t.tCstring) > 0
	case tokenInt:
		return t.tInt != 0
	case tokenFloat:
		return t.tFloat != 0
	case tokenIdentifier:
		return miscpkg.RstrGet(t.tRstring) == "true"
	default:
		return true
	}
}

// C: token2rstr (glw_view_eval.c:470-482)
func token2rstr(t *Token) *miscpkg.Rstr {
	if t.typ == tokenRstring || t.typ == tokenURI || t.typ == tokenIdentifier {
		return miscpkg.RstrDup(t.tRstring)
	}
	if t.typ == tokenCstring {
		return miscpkg.RstrAllocStr(t.tCstring)
	}
	return nil
}

// C: token_rgbstr_to_vec (glw_view_eval.c:488-515)
func tokenRgbstrToVec(t *Token, ec *glwViewEvalContext) *Token {
	if t.typ != tokenRstring {
		return t
	}
	s := miscpkg.RstrGet(t.tRstring)
	if len(s) == 0 || s[0] != '#' {
		return t
	}
	s0 := s[1:]
	n := 0
	for i := range len(s0) {
		if miscpkg.Hexnibble(s0[i]) == -1 {
			return t
		}
		n++
	}
	if n == 3 || n == 6 {
		t = evalAlloc(t, ec, tokenVectorFloat)
		t.tElements = 3
		var fv [3]float32
		miscpkg.RgbstrToFloatvec(s0, &fv)
		t.tFloatVector[0], t.tFloatVector[1], t.tFloatVector[2] = fv[0], fv[1], fv[2]
	}
	return t
}

// C: t_zero — static zero token (glw_view_eval.c:533)
var tZero = &Token{typ: tokenInt, tInt: 0}

// C: eval_op (glw_view_eval.c:518-659)
func evalOp(ec *glwViewEvalContext, self *Token) int {
	b := evalPop(ec)
	a := evalPop(ec)
	var r *Token
	var fFn func(float32, float32) float32
	var iFn func(int, int) int

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	if b = tokenResolve(ec, b); b == nil {
		return -1
	}

	if a.typ == tokenVoid {
		a = tZero
	}
	if b.typ == tokenVoid {
		b = tZero
	}

	a = tokenRgbstrToVec(a, ec)
	b = tokenRgbstrToVec(b, ec)

	switch self.typ {
	case tokenAdd:
		if aa, ok := tokenAsString(a); ok {
			if bb, ok2 := tokenAsString(b); ok2 {
				// Concatenation of strings
				rich := (a.typ == tokenRstring && a.tRstrType == propcore.PropStrRich) ||
					(b.typ == tokenRstring && b.tRstrType == propcore.PropStrRich)

				var astr, bstr string
				if rich && a.typ == tokenRstring && a.tRstrType == propcore.PropStrUTF8 {
					astr = htmlEscape(aa)
				} else {
					astr = aa
				}
				if rich && b.typ == tokenRstring && b.tRstrType == propcore.PropStrUTF8 {
					bstr = htmlEscape(bb)
				} else {
					bstr = bb
				}

				r = evalAlloc(self, ec, tokenRstring)
				r.tRstring = miscpkg.RstrAllocStr(astr + bstr)
				if rich {
					r.tRstrType = propcore.PropStrRich
				} else {
					r.tRstrType = propcore.PropStrUTF8
				}
				evalPush(ec, r)
				return 0
			}
		}
		fFn = evalOpFadd
		iFn = evalOpIadd
	case tokenSub:
		fFn = evalOpFsub
		iFn = evalOpIsub
	case tokenMultiply:
		fFn = evalOpFmul
		iFn = evalOpImul
	case tokenDivide:
		fFn = evalOpFdiv
		iFn = nil
	case tokenModulo:
		fFn = evalOpFmod
		iFn = evalOpImod
	default:
		panic("eval_op: bad token")
	}

	if a.typ == tokenInt && b.typ == tokenInt {
		if iFn == nil {
			r = evalAlloc(self, ec, tokenFloat)
			r.tFloat = fFn(float32(a.tInt), float32(b.tInt))
		} else {
			r = evalAlloc(self, ec, tokenInt)
			r.tInt = iFn(a.tInt, b.tInt)
		}
	} else if tokenFloatish(a) && tokenFloatish(b) {
		r = evalAlloc(self, ec, tokenFloat)
		r.tFloat = fFn(token2float(ec, a), token2float(ec, b))
	} else if a.typ == tokenVectorFloat && b.typ == tokenVectorFloat {
		if a.tElements != b.tElements {
			return glwViewSeterr(ec.ei, self,
				"Arithmetic op is invalid for non-equal sized vectors")
		}
		r = evalAlloc(self, ec, tokenVectorFloat)
		r.tElements = a.tElements
		for i := range a.tElements {
			r.tFloatVector[i] = fFn(a.tFloatVector[i], b.tFloatVector[i])
		}
	} else if a.typ == tokenVectorFloat && tokenFloatish(b) {
		v := token2float(ec, b)
		r = evalAlloc(self, ec, tokenVectorFloat)
		r.tElements = a.tElements
		for i := range a.tElements {
			r.tFloatVector[i] = fFn(a.tFloatVector[i], v)
		}
	} else if tokenFloatish(a) && b.typ == tokenVectorFloat {
		v := token2float(ec, a)
		r = evalAlloc(self, ec, tokenVectorFloat)
		r.tElements = b.tElements
		for i := range b.tElements {
			r.tFloatVector[i] = fFn(v, b.tFloatVector[i])
		}
	} else {
		r = evalAlloc(self, ec, tokenVoid)
	}

	evalPush(ec, r)
	return 0
}

// htmlEscape — C: html_enteties_escape(src, NULL) + copy. Escapes &, <, >.
func htmlEscape(s string) string {
	n := miscpkg.HtmlEntetiesEscape([]byte(s), nil)
	if n <= 1 {
		return s
	}
	dst := make([]byte, n)
	miscpkg.HtmlEntetiesEscape([]byte(s), dst)
	// C's return includes the NUL; strip it
	if n > 0 && dst[n-1] == 0 {
		dst = dst[:n-1]
	}
	return string(dst)
}

// ---------------------------------------------------------------------------
// C: eval_op_xor/or/and + eval_bool_op (glw_view_eval.c:662-700)
func evalOpXor(a, b int) int { return a ^ b }

func evalOpOr(a, b int) int { return a | b }

func evalOpAnd(a, b int) int { return a & b }

func evalBoolOp(ec *glwViewEvalContext, self *Token) int {
	b := evalPop(ec)
	a := evalPop(ec)
	var fn func(int, int) int

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	if b = tokenResolve(ec, b); b == nil {
		return -1
	}

	aa := miscpkg.BoolToInt(token2bool(a))
	bb := miscpkg.BoolToInt(token2bool(b))

	switch self.typ {
	case tokenBooleanXor:
		fn = evalOpXor
	case tokenBooleanOr:
		fn = evalOpOr
	case tokenBooleanAnd:
		fn = evalOpAnd
	default:
		panic("eval_bool_op: bad token")
	}

	r := evalAlloc(self, ec, tokenInt)
	r.tInt = fn(aa, bb)
	evalPush(ec, r)
	return 0
}

// C: eval_bool_not (glw_view_eval.c:703-718)
func evalBoolNot(ec *glwViewEvalContext, self *Token) int {
	a := evalPop(ec)
	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	r := evalAlloc(self, ec, tokenInt)
	r.tInt = miscpkg.BoolToInt(!token2bool(a))
	evalPush(ec, r)
	return 0
}

// C: eval_eq (glw_view_eval.c:721-765)
func evalEq(ec *glwViewEvalContext, self *Token, neq bool) int {
	b := evalPop(ec)
	a := evalPop(ec)
	var rr bool

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	if b = tokenResolve(ec, b); b == nil {
		return -1
	}

	aa, aok := tokenAsString(a)
	bb, bok := tokenAsString(b)
	if aok && bok {
		rr = aa == bb
	} else if a.typ == tokenInt && b.typ == tokenFloat {
		rr = float32(a.tInt) == b.tFloat
	} else if a.typ == tokenFloat && b.typ == tokenInt {
		rr = a.tFloat == float32(b.tInt)
	} else if a.typ != b.typ {
		rr = false
	} else {
		switch a.typ {
		case tokenInt:
			rr = a.tInt == b.tInt
		case tokenFloat:
			rr = a.tFloat == b.tFloat
		case tokenVoid:
			rr = true
		default:
			rr = false
		}
	}

	r := evalAlloc(self, ec, tokenInt)
	r.tInt = miscpkg.BoolToInt(rr) ^ miscpkg.BoolToInt(neq)
	evalPush(ec, r)
	return 0
}

// C: eval_lt (glw_view_eval.c:768-792)
func evalLt(ec *glwViewEvalContext, self *Token, gt bool) int {
	b := evalPop(ec)
	a := evalPop(ec)
	var rr bool

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	if b = tokenResolve(ec, b); b == nil {
		return -1
	}

	if gt {
		rr = token2float(ec, a) > token2float(ec, b)
	} else {
		rr = token2float(ec, a) < token2float(ec, b)
	}

	r := evalAlloc(self, ec, tokenInt)
	r.tInt = miscpkg.BoolToInt(rr)
	evalPush(ec, r)
	return 0
}

// C: eval_null_coalesce (glw_view_eval.c:795-816)
func evalNullCoalesce(ec *glwViewEvalContext, self *Token) int {
	b := evalPop(ec)
	a := evalPop(ec)

	if a = tokenResolve(ec, a); a == nil {
		return -1
	}
	if b = tokenResolve(ec, b); b == nil {
		return -1
	}

	if a.typ == tokenVoid {
		evalPush(ec, b)
	} else {
		evalPush(ec, a)
	}
	return 0
}

// C: signal_to_eval_mask[GLW_SIGNAL_num] (glw_view_eval.c:1131-1144)
var signalToEvalMask = [glwSignalNum]uint8{
	glwSignalInactive:                    GLW_VIEW_EVAL_ACTIVE,
	glwSignalFhpPathChanged:              GLW_VIEW_EVAL_FHP_CHANGE,
	glwSignalFocusChildInteractive:       GLW_VIEW_EVAL_OTHER,
	glwSignalFocusChildAutomatic:         GLW_VIEW_EVAL_OTHER,
	glwSignalCanScrollChanged:            GLW_VIEW_EVAL_OTHER,
	glwSignalFullwindowConstraintChanged: GLW_VIEW_EVAL_OTHER,
	glwSignalStatusChanged:               GLW_VIEW_EVAL_OTHER,
	glwSignalReselectChanged:             GLW_VIEW_EVAL_OTHER,
}

// C: run_dynamics (glw_view_eval.c:1148-1174)
func runDynamics(w *Glw, ec *glwViewEvalContext, mask int) {
	ec.mask = int8(mask)
	ec.w = w
	ec.gr = w.glwRoot
	ec.sublist = &w.glwPropSubscriptions
	ec.scope = w.glwScope

	t := w.glwDynamicExpressions
	var allFlags uint8

	for t != nil {
		if int(t.tDynamicEval)&mask != 0 {
			glwViewEvalRpn0(t, ec)
			t.tDynamicEval = uint8(ec.dynamicEval)
		}
		allFlags |= t.tDynamicEval
		t = t.next
	}
	w.glwDynamicEval = allFlags
	glwViewFreeChain(ec.gr, ec.alloc)
}

// C: glw_view_eval_signal (glw_view_eval.c:1177-1195)
func glwViewEvalSignal(w *Glw, sig glwSignal) {
	mask := int(signalToEvalMask[sig])
	if mask == 0 {
		return
	}
	if w.glwDynamicEval&uint8(mask) == 0 {
		return
	}
	var ec glwViewEvalContext
	runDynamics(w, &ec, mask)
}

// C: glw_view_eval_layout (glw_view_eval.c:1198-1209)
func glwViewEvalLayout(w *Glw, rc *glwRctx, mask int) {
	var ec glwViewEvalContext
	ec.rc = rc
	runDynamics(w, &ec, mask)
}

// C: glw_view_eval_dynamics (glw_view_eval.c:1212-1220)
func glwViewEvalDynamics(w *Glw, flags int) {
	var ec glwViewEvalContext
	runDynamics(w, &ec, flags)
}
