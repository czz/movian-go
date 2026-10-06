package glw

// C: src/ui/glw/glw_view_parser.c — canonical 1:1 port.

import (
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/nls"
)

// C: typedef struct tokenqueue (glw_view_parser.c:32-34)
type tokenqueue struct {
	head, tail *Token
}

// C: static token_t *tokenqueue_enqueue(tokenqueue_t *q, token_t *t,
// token_t *f) (glw_view_parser.c:37-53)
func tokenqueueEnqueue(q *tokenqueue, t *Token, f *Token) *Token {
	r := t.next
	t.next = nil

	if q.head == nil {
		q.head = t
		q.tail = t
	} else {
		q.tail.next = t
		q.tail = t
	}

	if f != nil && f.tNumArgs&1 == 0 {
		f.tNumArgs++
	}
	return r
}

// C: static token_t *tokenstack_push(token_t **s, token_t *t)
// (glw_view_parser.c:59-67)
func tokenstackPush(s **Token, t *Token) *Token {
	r := t.next
	t.next = *s
	*s = t
	return r
}

// C: static token_t *tokenstack_pop(token_t **s) (glw_view_parser.c:73-79)
func tokenstackPop(s **Token) *Token {
	r := *s
	*s = r.next
	return r
}

// C: static const int tokenprecedence[TOKEN_num] (glw_view_parser.c:85-112)
var tokenprecedence = [tokenNum]int{
	tokenAssignment:      1,
	tokenCondAssignment:  1,
	tokenRefAssignment:   1,
	tokenDebugAssignment: 1,
	tokenLinkAssignment:  1,
	tokenColon:           2,
	tokenQuestionmark:    3,
	tokenTenary:          3,
	tokenNullCoalesce:    4,
	tokenBooleanOr:       5,
	tokenBooleanAnd:      6,
	tokenBooleanXor:      7,
	tokenEq:              8,
	tokenNeq:             8,
	tokenLt:              8,
	tokenGt:              8,
	tokenAdd:             9,
	tokenSub:             9,
	tokenMultiply:        10,
	tokenDivide:          10,
	tokenModulo:          11,
	tokenBlock:           12,
	tokenBooleanNot:      13,
}

// C: static int parse_shunting_yard(token_t *expr, errorinfo_t *ei,
// glw_root_t *gr) — Shunting-yard to RPN (glw_view_parser.c:126-317)
func parseShuntingYard(expr *Token, ei *errorinfoT, gr *glwRoot) int {
	var outq tokenqueue
	var stack *Token
	t := expr.child
	var curfunc *Token

	typ := tokenPureRpn

	expr.child = nil // Avoid duplicate free if we bail out

	for t != nil {
		switch t.typ {

		case tokenBlock, tokenPropertyRef, tokenPropertyName:
			typ = tokenRpn
			fallthrough
		case tokenResolvedAttribute, tokenUnresolvedAttribute,
			tokenRstring, tokenCstring, tokenFloat, tokenEm,
			tokenInt, tokenIdentifier, tokenVoid:
			t = tokenqueueEnqueue(&outq, t, curfunc)
			continue

		case tokenSeparator:
			for stack != nil && (stack.typ != tokenLeftParenthesis &&
				stack.typ != tokenLeftBracket) {
				tokenqueueEnqueue(&outq, tokenstackPop(&stack), curfunc)
			}
			if curfunc != nil {
				if curfunc.tNumArgs&1 == 0 {
					glwViewSeterr(ei, t, "Unexpected separator '',''")
					goto err
				}
				curfunc.tNumArgs++
			}

		case tokenAdd, tokenSub, tokenMultiply, tokenDivide,
			tokenModulo, tokenBooleanOr, tokenBooleanAnd,
			tokenBooleanXor, tokenAssignment, tokenCondAssignment,
			tokenRefAssignment, tokenDebugAssignment,
			tokenLinkAssignment, tokenEq, tokenNullCoalesce,
			tokenNeq, tokenLt, tokenGt, tokenBooleanNot:
			for stack != nil &&
				tokenprecedence[t.typ] <= tokenprecedence[stack.typ] {
				tokenqueueEnqueue(&outq, tokenstackPop(&stack), nil)
			}
			fallthrough

		case tokenLeftParenthesis:
			t = tokenstackPush(&stack, t)
			continue

		case tokenQuestionmark:
			for stack != nil &&
				tokenprecedence[t.typ] < tokenprecedence[stack.typ] {
				tokenqueueEnqueue(&outq, tokenstackPop(&stack), nil)
			}
			t = tokenstackPush(&stack, t)
			continue

		case tokenColon:
			for stack != nil && stack.typ != tokenQuestionmark {
				tokenqueueEnqueue(&outq, tokenstackPop(&stack), curfunc)
			}
			if stack == nil {
				glwViewSeterr(ei, t, "Unbalanced ?: operator")
				goto err
			}
			glwViewTokenFree(gr, tokenstackPop(&stack))
			t.typ = tokenTenary
			t = tokenstackPush(&stack, t)
			continue

		case tokenFunction:
			typ = tokenRpn
			fallthrough
		case tokenLeftBracket:
			t.tmp = unsafe.Pointer(curfunc)
			curfunc = t
			t = tokenstackPush(&stack, t)
			continue

		case tokenRightParenthesis:
			for stack != nil && stack.typ != tokenLeftParenthesis {
				tokenqueueEnqueue(&outq, tokenstackPop(&stack), curfunc)
			}
			if stack == nil {
				glwViewSeterr(ei, t, "Unbalanced parentheses")
				goto err
			}
			glwViewTokenFree(gr, tokenstackPop(&stack))

			if stack != nil && stack.typ == tokenFunction {
				if stack.tNumArgs != 0 && stack.tNumArgs&1 == 0 {
					glwViewSeterr(ei, t, "Unexpected separator '',''")
					goto err
				}
				stack.tNumArgs = (1 + stack.tNumArgs) / 2
				curfunc = (*Token)(stack.tmp)
				tokenqueueEnqueue(&outq, tokenstackPop(&stack), curfunc)
			}

		case tokenRightBracket:
			for stack != nil && stack.typ != tokenLeftBracket {
				tokenqueueEnqueue(&outq, tokenstackPop(&stack), curfunc)
			}
			if stack == nil {
				glwViewSeterr(ei, t, "Unbalanced brackets")
				goto err
			}
			// C: assert(stack == curfunc)
			if stack.tNumArgs != 0 && stack.tNumArgs&1 == 0 {
				glwViewSeterr(ei, t, "Unexpected separator '',''")
				goto err
			}
			stack.tNumArgs = (1 + stack.tNumArgs) / 2
			curfunc = (*Token)(stack.tmp)
			tokenqueueEnqueue(&outq, tokenstackPop(&stack), curfunc)

		default:
			glwViewSeterr(ei, t, "Unexpected symbol in RPN processor")
			goto err
		}
		x := t
		t = t.next
		glwViewTokenFree(gr, x)
	}

	for stack != nil {
		if stack.typ == tokenLeftParenthesis ||
			stack.typ == tokenRightParenthesis {
			glwViewSeterr(ei, stack, "Unbalanced parentheses")
			goto err
		}
		tokenqueueEnqueue(&outq, tokenstackPop(&stack), curfunc)
	}

	expr.child = outq.head
	// Assignments to the 'style' property are always pure because
	// delegating that to the target of the style will result in a cycle.
	if expr.child.typ == tokenResolvedAttribute &&
		expr.child.tAttrib.name == "style" {
		typ = tokenPureRpn
	}

	expr.typ = typ
	return 0

err:
	for stack != nil {
		glwViewTokenFree(gr, tokenstackPop(&stack))
	}
	for outq.head != nil {
		glwViewTokenFree(gr, tokenstackPop(&outq.head))
	}
	return -1
}

// C: static void optimize_attribute_assignment(token_t *t, token_t *prev,
// glw_root_t *gr) (glw_view_parser.c:339-380)
func optimizeAttributeAssignment(t *Token, prev *Token, gr *glwRoot) {
	if t.typ == tokenPureRpn {
		att := t.child
		if att.typ == tokenResolvedAttribute &&
			att.next != nil && att.next.next != nil &&
			att.next.next.next == nil &&
			att.next.next.typ == tokenAssignment {

			val := att.next
			ass := att.next.next
			if val.typ == tokenFloat || val.typ == tokenInt ||
				val.typ == tokenVectorFloat || val.typ == tokenVoid ||
				val.typ == tokenCstring || val.typ == tokenRstring ||
				val.typ == tokenIdentifier {

				val.tAttrib = att.tAttrib

				val.next = t.next
				prev.next = val

				glwViewTokenFree(gr, ass)
				glwViewTokenFree(gr, att)
				glwViewTokenFree(gr, t)
			}
		}
	}
}

// C: static int propnamecmp(const char *a[16], const char *b[16])
// (glw_view_parser.c:387-401)
func propnamecmp(a, b *[16]string) int {
	for i := range 16 {
		if a[i] == "" && b[i] == "" {
			return 1
		}
		if a[i] == "" || b[i] == "" {
			return 0
		}
		if a[i] != b[i] {
			return 0
		}
	}
	panic("propnamecmp") // C: abort()
}

// C: static void scan_prop_names(token_t *rpn, token_t *prev,
// glw_root_t *gr) (glw_view_parser.c:410-451)
func scanPropNames(rpn *Token, prev *Token, gr *glwRoot) {
	idcnt := 0
	if rpn.typ != tokenRpn {
		return
	}

	for t := rpn.child; t != nil; t = t.next {
		if t.typ == tokenPropertyName {
			var tname [16]string
			tnameIsSet := false // Call propname_to_array lazy

			var u *Token
			for u = rpn.child; u != t; u = u.next {
				if u.typ == tokenPropertyName {
					if !tnameIsSet {
						glwPropnameToArray(&tname, t)
						tnameIsSet = true
					}
					var uname [16]string
					glwPropnameToArray(&uname, u)
					if propnamecmp(&tname, &uname) != 0 {
						break
					}
				}
			}
			if u == t {
				idcnt++
				t.tPropNameID = idcnt
			} else {
				t.tPropNameID = u.tPropNameID
			}
		}
	}
}

// C: static int parse_prep_expression(token_t *expr, errorinfo_t *ei,
// glw_root_t *gr) (glw_view_parser.c:457-579)
func parsePrepExpression(expr *Token, ei *errorinfoT, gr *glwRoot) int {
	t := expr.child

	for t != nil {
		t1 := t.next

		// Transform [$&]foo.bar.etc into a property chain
		if (t.typ == tokenDollar || t.typ == tokenAmpersand) &&
			t1 != nil && t1.typ == tokenIdentifier {
			t0 := t
			t2 := t
			if t.typ == tokenAmpersand {
				t0.tFlags |= tokenFCanonicalPath
			}
			t0.typ = tokenPropertyName

			t0.next = t1.next
			t0.tElements = 1
			t0.tPnvec[0] = t1.tRstring
			t1.tRstring = nil

			glwViewTokenFree(gr, t1)

			t = t0.next
			for t != nil && t.typ == tokenDot {
				t1 = t.next
				if t1 == nil || t1.typ != tokenIdentifier {
					glwViewSeterr(ei, t, "Invalid object dereference")
					return -1
				}

				t0.next = t1.next

				if t2.tElements < tokenPropertyNameVecSize {
					// Can still fit stuff in previous token
					t2.tPnvec[t2.tElements] = t1.tRstring
					t2.tElements++
					t1.tRstring = nil
					glwViewTokenFree(gr, t)
					glwViewTokenFree(gr, t1)
				} else {
					t1.next = nil
					t1.typ = tokenPropertyName
					t1.tElements = 1
					// C: t_pnvec[] aliases t_rstring through the token union —
					// the identifier's t_rstring becomes t_pnvec[0] by setting
					// t_elements. Go fields are flattened: copy it explicitly.
					t1.tPnvec[0] = t1.tRstring
					t1.tRstring = nil
					glwViewTokenFree(gr, t)
					t2.child = t1
					t2 = t1
				}
				t = t0.next
			}
			continue
		}

		// Transform int/float "em" into TOKEN_EM
		if (t.typ == tokenFloat || t.typ == tokenInt) &&
			t1 != nil && t1.typ == tokenIdentifier &&
			miscpkg.RstrGet(t1.tRstring) == "em" {

			if t.typ == tokenInt {
				t.tFloat = float32(t.tInt)
			}
			t.typ = tokenEm

			t.next = t1.next
			t = t1.next
			glwViewTokenFree(gr, t1)
			continue
		}

		// Transform '.name' into just 'name' and set its type to
		// object attribute
		if t.typ == tokenDot {
			if t1 == nil || t1.typ != tokenIdentifier {
				glwViewSeterr(ei, t, "Invalid object attribute reference")
				return -1
			}

			t.tRstring = t1.tRstring
			t1.tRstring = nil

			glwViewAttribResolve(t)
			t.next = t1.next
			t = t1.next
			glwViewTokenFree(gr, t1)
			continue
		}

		// Transform 'name: ' into a attribute assignment
		if t.typ == tokenIdentifier &&
			t1 != nil && t1.typ == tokenColon {
			glwViewAttribResolve(t)
			t1.typ = tokenAssignment
			continue
		}

		if t.typ == tokenIdentifier && t1 != nil {
			// Check if identifier is a function (i.e, it is followed
			// by a parenthesis)
			if t1.typ == tokenLeftParenthesis {
				// Yep, try to resolve the identifier into a function

				if miscpkg.RstrGet(t.tRstring) == "_" &&
					// The _() function is special as it refers to
					// i18n translations
					t1.next.typ == tokenRstring &&
					t1.next.next.typ == tokenRightParenthesis {

					glwViewNlsString(t, miscpkg.RstrGet(t1.next.tRstring))

					t.next = t1.next.next.next
					glwViewTokenFree(gr, t1.next.next)
					glwViewTokenFree(gr, t1.next)
					glwViewTokenFree(gr, t1)

					t = t.next
				} else {
					// Resolve as ordinary function
					n := glwViewFunctionResolve(gr, ei, t)
					if n == nil {
						return -1
					}
					t = n
				}
				continue
			}
		}
		t = t1
	}
	return parseShuntingYard(expr, ei, gr)
}

// C: static int parse_one_expression(token_t *prev, token_t *first,
// errorinfo_t *ei, glw_root_t *gr) (glw_view_parser.c:586-644)
func parseOneExpression(prev *Token, first *Token, ei *errorinfoT,
	gr *glwRoot) int {
	t := first
	var l *Token
	balance := 0

	for t != nil {
		switch t.typ {
		case tokenEnd:
			glwViewSeterr(ei, first, "Unexpected end of file")
			return -1

		case tokenBlockOpen:
			if parseBlock(t, ei, tokenBlockClose, gr) != 0 {
				return -1
			}

		case tokenEndOfExpr:
			t.typ = tokenExpr
			if l == nil {
				t.typ = tokenNop
				// Empty expression
				return 0
			}

			l.next = nil
			prev.next = t
			t.child = first
			if parsePrepExpression(t, ei, gr) != 0 {
				return -1
			}

			scanPropNames(t, prev, gr)
			optimizeAttributeAssignment(t, prev, gr)
			return 0

		case tokenBlockClose:
			glwViewSeterr(ei, t, "Unexpected '}'")
			return -1

		case tokenLeftParenthesis:
			balance++

		case tokenRightParenthesis:
			balance--
		}

		l = t
		t = t.next
	}
	return -1
}

// C: static int parse_block(token_t *first, errorinfo_t *ei,
// token_type_t term, glw_root_t *gr) (glw_view_parser.c:651-686)
func parseBlock(first *Token, ei *errorinfoT, term tokenType,
	gr *glwRoot) int {
	var p *Token

	first.typ = tokenBlock
	if first.next.typ == term {
		// Empty block
		p = first.next
		first.next = p.next
		glwViewTokenFree(gr, p)
		return 0
	}

	p = first

	for p != nil && p.next != nil {
		if p.next.typ == term {
			first.child = first.next
			first.next = p.next.next
			glwViewTokenFree(gr, p.next)
			p.next = nil
			glwViewAttribOptimize(first.child, gr)
			return 0
		}

		if parseOneExpression(p, p.next, ei, gr) != 0 {
			return -1
		}
		p = p.next
	}

	glwViewSeterr(ei, first, "Unbalanced block")
	return -1
}

// C: int glw_view_parse(token_t *sof, errorinfo_t *ei, glw_root_t *gr)
// (glw_view_parser.c:692-696)
func glwViewParse(sof *Token, ei *errorinfoT, gr *glwRoot) int {
	return parseBlock(sof, ei, tokenEnd, gr)
}

// C: static void glw_view_nls_string(token_t *t, const char *str)
// (glw_view_parser.c:702-709) — Transform a string token into a
// translated property.
func glwViewNlsString(t *Token, str string) {
	p := nls.GetProp(str)
	miscpkg.RstrRelease(t.tRstring)
	t.typ = tokenPropertyRef
	t.tProp = glwDeps.pm.RefInc(p)
}
