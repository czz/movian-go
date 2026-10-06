package glw

// C: src/ui/glw/glw_view_preproc.c — canonical 1:1 port.

import (
	"unsafe"

	miscpkg "github.com/czz/movian-go/internal/misc"
)

// C: typedef struct macro_arg (glw_view_preproc.c:27-32)
type macroArg struct {
	first, last *Token        // C: first, last
	rname       *miscpkg.Rstr // C: rname
	maDef       *Token        // C: ma_def
	linkNext    *macroArg     // C: TAILQ_ENTRY link
	linkPrev    **macroArg    //
}

// C: typedef struct macro (glw_view_preproc.c:35-40)
type macroT struct {
	body  *Token
	rname *miscpkg.Rstr
	args  macroArgQueue // C: TAILQ_HEAD args

	linkNext *macroT // C: LIST_ENTRY link
	linkPrev **macroT
}

type macroList struct{ lhFirst *macroT }
type macroArgQueue struct {
	tqhFirst *macroArg
	tqhLast  **macroArg
}

// C: typedef struct import (glw_view_preproc.c:46-49)
type importT struct {
	rname    *miscpkg.Rstr
	linkNext *importT
	linkPrev **importT
}
type importList struct{ lhFirst *importT }

// C: static void macro_destroy(glw_root_t *gr, macro_t *m)
// (glw_view_preproc.c:54-70)
func macroDestroy(gr *glwRoot, m *macroT, ml *macroList) {
	for ma := m.args.tqhFirst; ma != nil; {
		nxt := ma.linkNext
		if ma.linkNext != nil {
			ma.linkNext.linkPrev = ma.linkPrev
		} else {
			m.args.tqhLast = ma.linkPrev
		}
		*ma.linkPrev = ma.linkNext
		miscpkg.RstrRelease(ma.rname)
		if ma.maDef != nil {
			glwViewFreeChain(gr, ma.maDef)
		}
		ma = nxt
	}

	// C: LIST_REMOVE(m, link)
	if m.linkNext != nil {
		m.linkNext.linkPrev = m.linkPrev
	}
	*m.linkPrev = m.linkNext
	glwViewFreeChain(gr, m.body)
	miscpkg.RstrRelease(m.rname)
}

// C: static macro_arg_t *macro_add_arg(macro_t *m, rstr_t *name)
// (glw_view_preproc.c:75-82)
func macroAddArg(m *macroT, name *miscpkg.Rstr) *macroArg {
	ma := &macroArg{rname: miscpkg.RstrDup(name)}
	ma.linkPrev = m.args.tqhLast
	ma.linkNext = nil
	*ma.linkPrev = ma
	m.args.tqhLast = &ma.linkNext
	return ma
}

// C: #define consumetoken() — assert(p != t); p->next = t->next;
// glw_view_token_free(gr, t); t = p->next
func consumetoken(gr *glwRoot, p *Token, t **Token) {
	p.next = (*t).next
	glwViewTokenFree(gr, *t)
	*t = p.next
}

// C: static int glw_view_preproc0(glw_root_t *gr, token_t *p,
// errorinfo_t *ei, struct macro_list *ml, struct import_list *il,
// int may_unlock) (glw_view_preproc.c:89-457)
func glwViewPreproc0(gr *glwRoot, p *Token, ei *errorinfoT,
	ml *macroList, il *importList, mayUnlock int) int {
	var t, n, x, a, b, c, d, e *Token
	var m *macroT
	var ma *macroArg
	balance := 0

	for {
		t = p.next
		if t.typ == tokenEnd {
			return 0
		}

		if t.typ == tokenIdentifier {
			n = t.next
			if n.typ == tokenLeftParenthesis {
				m = nil
				for mm := ml.lhFirst; mm != nil; mm = mm.linkNext {
					if miscpkg.RstrGet(mm.rname) == miscpkg.RstrGet(t.tRstring) {
						m = mm
						break
					}
				}

				if m != nil {
					// Macro invokation
					consumetoken(gr, p, &t) // identifier
					consumetoken(gr, p, &t) // left parenthesis

					x = p
					c = p.next // Pointer to argument list, used for freeing later

					if p.next.typ == tokenIdentifier &&
						p.next.next.typ == tokenAssignment {

						for mma := m.args.tqhFirst; mma != nil; mma = mma.linkNext {
							mma.first = nil
						}
						p = p.next

						for {
							ma = nil
							for mma := m.args.tqhFirst; mma != nil; mma = mma.linkNext {
								if miscpkg.RstrGet(mma.rname) == miscpkg.RstrGet(p.tRstring) {
									ma = mma
									break
								}
							}

							p = p.next
							if ma != nil {
								ma.first = p.next
							}
							for {
								t = p.next
								if t.typ == tokenEnd {
									return glwViewSeterr(ei, p, "Unexpected end of input in "+
										"macro invokation")
								}

								if t.typ == tokenRightParenthesis && balance == 0 {
									break
								}

								if t.typ == tokenBlockClose ||
									t.typ == tokenRightParenthesis ||
									t.typ == tokenRightBracket {
									balance--
								} else if t.typ == tokenBlockOpen ||
									t.typ == tokenLeftParenthesis ||
									t.typ == tokenLeftBracket {
									balance++
								} else if t.typ == tokenSeparator && balance == 0 {
									break
								}
								p = p.next
							}
							if ma != nil {
								ma.last = p
							}
							if t.typ == tokenRightParenthesis {
								break
							}
							p = t.next

							if p.typ != tokenIdentifier ||
								p.next.typ != tokenAssignment {
								return glwViewSeterr(ei, p,
									"Mixing named and unnamed arguments "+
										"is not supported")
							}
						}
					} else {
						for mma := m.args.tqhFirst; mma != nil; mma = mma.linkNext {
							mma.first = p.next

							if mma.first.typ == tokenRightParenthesis {
								mma.first = nil
								break
							}

							for {
								t = p.next
								if t.typ == tokenEnd {
									return glwViewSeterr(ei, p, "Unexpected end of input in "+
										"macro invokation")
								}

								if t.typ == tokenRightParenthesis && balance == 0 {
									// Clear remaining arguments
									for xx := mma.linkNext; xx != nil; xx = xx.linkNext {
										xx.first = nil
									}
									break
								}

								if t.typ == tokenBlockClose ||
									t.typ == tokenRightParenthesis ||
									t.typ == tokenRightBracket {
									balance--
								} else if t.typ == tokenBlockOpen ||
									t.typ == tokenLeftParenthesis ||
									t.typ == tokenLeftBracket {
									balance++
								} else if t.typ == tokenSeparator && balance == 0 {
									break
								}
								p = p.next
							}

							mma.last = p

							if t.typ == tokenRightParenthesis {
								break
							}
							p = p.next
						}

						if t.typ != tokenRightParenthesis {
							return glwViewSeterr(ei, t,
								"Too many arguments to macro %s",
								miscpkg.RstrGet(m.rname))
						}
					}
					d = t.next
					t.next = nil

					p = x
					b = nil
					p.next = d

					for a = m.body; a != nil; a = a.next {
						if a.tmp != nil && a.next.typ != tokenAssignment {
							ma = (*macroArg)(a.tmp)
							e = ma.first

							if e == nil && ma.maDef != nil {
								var last *Token
								b = glwViewCloneChain(gr, ma.maDef, &last)
								p.next = b
								p = last
							} else {
								for {
									if e == nil {
										return glwViewSeterr(ei, t,
											"Too few arguments to macro %s",
											miscpkg.RstrGet(m.rname))
									}

									b = glwViewTokenCopy(gr, e)
									p.next = b
									p = b

									if e == ma.last {
										break
									}
									e = e.next
								}
							}
						} else {
							b = glwViewTokenCopy(gr, a)
							p.next = b
							p = b
						}
					}

					if b != nil {
						b.next = d
					}

					p = x
					glwViewFreeChain(gr, c)
					continue
				}
			}
		}

		if t.typ != tokenHash {
			p = p.next
			continue
		}

		consumetoken(gr, p, &t)

		if t.typ == tokenIdentifier {
			name := miscpkg.RstrGet(t.tRstring)

			// Include another file
			if name == "include" {
				consumetoken(gr, p, &t)

				if t.typ != tokenRstring {
					return glwViewSeterr(ei, t, "Invalid filename after include")
				}

				x = t.next
				n = glwViewLoad1(gr, t.tRstring, ei, t, mayUnlock)
				if n == nil {
					return -1
				}
				n.next = x
				consumetoken(gr, p, &t)
				continue
			}

			// Import another file
			if name == "import" {
				consumetoken(gr, p, &t)

				if t.typ != tokenRstring {
					return glwViewSeterr(ei, t, "Invalid filename after import")
				}

				var imp *importT
				for i2 := il.lhFirst; i2 != nil; i2 = i2.linkNext {
					if miscpkg.RstrGet(i2.rname) == miscpkg.RstrGet(t.tRstring) {
						imp = i2
						break
					}
				}

				if imp == nil {
					imp = &importT{rname: miscpkg.RstrDup(t.tRstring)}
					imp.linkNext = il.lhFirst
					if imp.linkNext != nil {
						imp.linkNext.linkPrev = &imp.linkNext
					}
					il.lhFirst = imp
					imp.linkPrev = &il.lhFirst

					x = t.next
					n = glwViewLoad1(gr, t.tRstring, ei, t, mayUnlock)
					if n == nil {
						return -1
					}
					n.next = x
					consumetoken(gr, p, &t)
				}
				continue
			}

			// Define a macro
			if name == "define" {
				consumetoken(gr, p, &t)

				if t.typ != tokenIdentifier {
					return glwViewSeterr(ei, t, "Invalid macro name")
				}

				m = &macroT{}
				m.args.tqhLast = &m.args.tqhFirst
				// C: LIST_INSERT_HEAD(ml, m, link)
				m.linkNext = ml.lhFirst
				if m.linkNext != nil {
					m.linkNext.linkPrev = &m.linkNext
				}
				ml.lhFirst = m
				m.linkPrev = &ml.lhFirst

				m.rname = miscpkg.RstrDup(t.tRstring)
				consumetoken(gr, p, &t)

				if t.typ != tokenLeftParenthesis {
					return glwViewSeterr(ei, t, "Expected '(' after macro name")
				}
				consumetoken(gr, p, &t)

				if t.typ != tokenRightParenthesis {
					defaultargs := 0
					for {
						if t.typ != tokenIdentifier {
							return glwViewSeterr(ei, t, "Expected macro argument")
						}

						ma2 := macroAddArg(m, t.tRstring)
						consumetoken(gr, p, &t)

						if t.typ == tokenAssignment {
							// Default argument
							consumetoken(gr, p, &t)

							ma2.maDef = t
							depth := 0
							for t.next.typ != tokenEnd {
								if t.next.typ == tokenLeftParenthesis {
									depth++
								}
								if t.next.typ == tokenRightParenthesis {
									if depth == 0 {
										break
									}
									depth--
								}
								if t.next.typ == tokenSeparator {
									break
								}
								t = t.next
							}

							xx := t.next
							t.next = nil
							t = xx

							defaultargs = 1
						} else if defaultargs != 0 {
							return glwViewSeterr(ei, t,
								"Non default arg after default arg")
						}

						if t.typ == tokenRightParenthesis {
							consumetoken(gr, p, &t)
							break
						}

						if t.typ != tokenSeparator {
							return glwViewSeterr(ei, t,
								"Expected ',' or ')' "+
									"after macro argument")
						}
						consumetoken(gr, p, &t)
					}
				} else {
					consumetoken(gr, p, &t)
				}

				if t.typ != tokenBlockOpen {
					return glwViewSeterr(ei, t, "Expected '{' after macro header")
				}
				consumetoken(gr, p, &t)

				x = p

				for {
					t = p.next
					if t.typ == tokenEnd {
						return glwViewSeterr(ei, x.next, "Unexpected end of input in "+
							"macro definition")
					}

					if t.typ == tokenBlockClose {
						if balance == 0 {
							consumetoken(gr, p, &t)
							break
						}
						balance--
					} else if t.typ == tokenBlockOpen {
						balance++
					} else if t.typ == tokenIdentifier {
						for mma := m.args.tqhFirst; mma != nil; mma = mma.linkNext {
							if miscpkg.RstrGet(mma.rname) == miscpkg.RstrGet(t.tRstring) {
								t.tmp = unsafe.Pointer(mma)
								break
							}
						}
					}
					p = p.next
				}

				p.next = nil
				m.body = x.next

				x.next = t
				p = x
				continue
			}
		}
		return glwViewSeterr(ei, t, "Invalid preprocessor directive")
	}
}

// C: int glw_view_preproc(glw_root_t *gr, token_t *p, errorinfo_t *ei,
// int may_unlock) (glw_view_preproc.c:463-489)
func glwViewPreproc(gr *glwRoot, p *Token, ei *errorinfoT, mayUnlock int) int {
	var ml macroList
	var il importList

	r := glwViewPreproc0(gr, p, ei, &ml, &il, mayUnlock)

	for {
		m := ml.lhFirst
		if m == nil {
			break
		}
		macroDestroy(gr, m, &ml)
	}

	for {
		i := il.lhFirst
		if i == nil {
			break
		}
		if i.linkNext != nil {
			i.linkNext.linkPrev = i.linkPrev
		}
		*i.linkPrev = i.linkNext
		miscpkg.RstrRelease(i.rname)
	}

	return r
}
