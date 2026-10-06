package misc

import "strings"

/*
 * Port of ext/minilibs/regexp.c — re1-style NFA regex engine.
 *
 * C pointer arithmetic mapping:
 *   const char *sp          -> byte offset into the input []byte
 *   Reinst *pc              -> index into prog.insts
 *   Reinst *x / *y          -> inst indices (x, y int)
 *   Renode *g.pend++        -> index into preallocated node pool
 *   Reclass *end            -> end index into spans[64]
 *   jmp_buf kaboom          -> panic / recover
 *   Resub sp/ep (char*)     -> byte offsets, -1 = NULL
 */

type reRune = uint // C: typedef unsigned int Rune

const (
	regIcase       = 1         // C: REG_ICASE
	regNewline     = 2         // C: REG_NEWLINE
	regNotbol      = 4         // C: REG_NOTBOL
	regMaxsub      = 8         // C: REG_MAXSUB
	repinf         = 255       // C: REPINF
	maxThreadStack = 64        // C: MAXTHREAD_STACK
	maxsub         = regMaxsub // C: MAXSUB
)

// C: struct Resub — sub[i].sp/ep are byte offsets (-1 = NULL)
type Resub struct {
	Nsub uint
	Sub  [regMaxsub]struct {
		Sp, Ep int
	}
}

// C: struct Reclass { Rune *end; Rune spans[64]; }
type reclass struct {
	end   int
	spans [64]reRune
}

// C: struct Reinst — x,y are inst indices into prog.insts
type reinst struct {
	opcode uint8
	n      uint8
	c      reRune
	cc     *reclass
	x, y   int
}

// C: struct Reprog
type Reprog struct {
	insts  []reinst // C: start..end array; start=0, end=len
	end    int      // C: Reinst *end (emit cursor)
	flags  int
	nsub   uint
	cclass [16]reclass
}

// C: struct Renode
type renode struct {
	typ      uint8
	ng, m, n uint8
	c        reRune
	cc       *reclass
	x, y     *renode
}

// C: static struct { ... } g — parser/compiler global state
type regexG struct {
	prog   *Reprog
	pstart []renode // C: Renode pool
	pend   int      // C: g.pend cursor

	source    string
	sourceIdx int
	ncclass   uint
	nsub      uint
	sub       [maxsub]*renode

	lookahead    int
	yychar       reRune
	yycc         *reclass
	yymin, yymax int

	err any // C: g.error + setjmp marker
}

// C: static int isalpharune(Rune c)
func isalpharune(c reRune) int {
	if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		return 1
	}
	return 0
}

// C: static Rune toupperrune(Rune c)
func toupperrune(c reRune) reRune {
	if c >= 'a' && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// C: static int chartorune(Rune *r, const char *s)
// Reads one byte at position p (0 past end). Returns rune + width.
func chartorune(s string, p int) (reRune, int) {
	if p >= len(s) {
		return 0, 1
	}
	return reRune(s[p]), 1
}

// C: static void die(const char *message) — longjmp → panic
func die(message string) {
	panic(reError(message))
}

type reError string

// C: static Rune canon(Rune c)
func canon(c reRune) reRune {
	u := toupperrune(c)
	if c >= 128 && u < 128 {
		return c
	}
	return u
}

/* Scan */

const (
	lChar    = 256 + iota // C: L_CHAR = 256
	lCclass               // L_CCLASS
	lNcclass              // L_NCCLASS
	lNc                   // L_NC "(?:"
	lPla                  // L_PLA "(?="
	lNla                  // L_NLA "(?!"
	lWord                 // L_WORD "\b"
	lNword                // L_NWORD "\B"
	lRef                  // L_REF "\1"
	lCount                // L_COUNT "{M,N}"
)

// C: static int hex(int c)
func reHex(c int) int {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	if c >= 'a' && c <= 'f' {
		return c - 'a' + 0xA
	}
	if c >= 'A' && c <= 'F' {
		return c - 'A' + 0xA
	}
	die("invalid escape sequence")
	return 0
}

// C: static int dec(int c)
func reDec(c int) int {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	die("invalid quantifier")
	return 0
}

const reEscapes = "BbDdSsWw^$\\.*+?()[]{}|0123456789" // C: ESCAPES

// byteAt returns s[i] or 0 past end (C reads the NUL terminator).
func byteAt(s string, i int) byte {
	if i >= len(s) || i < 0 {
		return 0
	}
	return s[i]
}

// C: static int g.nextrune(void)
func (g *regexG) nextrune() int {
	var w int
	g.yychar, w = chartorune(g.source, g.sourceIdx)
	g.sourceIdx += w
	if g.yychar == '\\' {
		g.yychar, w = chartorune(g.source, g.sourceIdx)
		g.sourceIdx += w
		switch g.yychar {
		case 0:
			die("unterminated escape sequence")
		case 'f':
			g.yychar = '\f'
			return 0
		case 'n':
			g.yychar = '\n'
			return 0
		case 'r':
			g.yychar = '\r'
			return 0
		case 't':
			g.yychar = '\t'
			return 0
		case 'v':
			g.yychar = '\v'
			return 0
		case 'c':
			g.yychar = reRune(byteAt(g.source, g.sourceIdx)) & 31
			g.sourceIdx++
			return 0
		case 'x':
			g.yychar = reRune(reHex(int(byteAt(g.source, g.sourceIdx)))) << 4
			g.sourceIdx++
			g.yychar += reRune(reHex(int(byteAt(g.source, g.sourceIdx))))
			g.sourceIdx++
			if g.yychar == 0 {
				g.yychar = '0'
				return 1
			}
			return 0
		case 'u':
			g.yychar = reRune(reHex(int(byteAt(g.source, g.sourceIdx)))) << 12
			g.sourceIdx++
			g.yychar += reRune(reHex(int(byteAt(g.source, g.sourceIdx)))) << 8
			g.sourceIdx++
			g.yychar += reRune(reHex(int(byteAt(g.source, g.sourceIdx)))) << 4
			g.sourceIdx++
			g.yychar += reRune(reHex(int(byteAt(g.source, g.sourceIdx))))
			g.sourceIdx++
			if g.yychar == 0 {
				g.yychar = '0'
				return 1
			}
			return 0
		}
		if strings.IndexByte(reEscapes, byte(g.yychar)) >= 0 {
			return 1
		}
		if isalpharune(g.yychar) != 0 || g.yychar == '_' {
			die("invalid escape character")
		}
		return 0
	}
	return 0
}

// C: static int g.lexcount(void)
func (g *regexG) lexcount() int {
	g.yychar = reRune(byteAt(g.source, g.sourceIdx))
	g.sourceIdx++

	g.yymin = reDec(int(g.yychar))
	g.yychar = reRune(byteAt(g.source, g.sourceIdx))
	g.sourceIdx++
	for g.yychar != ',' && g.yychar != '}' {
		g.yymin = g.yymin*10 + reDec(int(g.yychar))
		g.yychar = reRune(byteAt(g.source, g.sourceIdx))
		g.sourceIdx++
	}
	if g.yymin >= repinf {
		die("numeric overflow")
	}

	if g.yychar == ',' {
		g.yychar = reRune(byteAt(g.source, g.sourceIdx))
		g.sourceIdx++
		if g.yychar == '}' {
			g.yymax = repinf
		} else {
			g.yymax = reDec(int(g.yychar))
			g.yychar = reRune(byteAt(g.source, g.sourceIdx))
			g.sourceIdx++
			for g.yychar != '}' {
				g.yymax = g.yymax*10 + reDec(int(g.yychar))
				g.yychar = reRune(byteAt(g.source, g.sourceIdx))
				g.sourceIdx++
			}
			if g.yymax >= repinf {
				die("numeric overflow")
			}
		}
	} else {
		g.yymax = g.yymin
	}

	return lCount
}

// C: static void g.newcclass(void)
func (g *regexG) newcclass() {
	if g.ncclass >= uint(len(g.prog.cclass)) {
		die("too many character classes")
	}
	g.yycc = &g.prog.cclass[g.ncclass]
	g.ncclass++
	g.yycc.end = 0
}

// C: static void g.addrange(Rune a, Rune b)
func (g *regexG) addrange(a, b reRune) {
	if a > b {
		die("invalid character class range")
	}
	if g.yycc.end+2 == len(g.yycc.spans) {
		die("too many character class ranges")
	}
	g.yycc.spans[g.yycc.end] = a
	g.yycc.end++
	g.yycc.spans[g.yycc.end] = b
	g.yycc.end++
}

// C: static void addranges_d(void)
func (g *regexG) addrangesD() { g.addrange('0', '9') }

// C: static void addranges_D(void)
func (g *regexG) addrangesND() {
	g.addrange(0, '0'-1)
	g.addrange('9'+1, 0xFFFF)
}

// C: static void addranges_s(void)
func (g *regexG) addrangesS() {
	g.addrange(0x9, 0x9)
	g.addrange(0xA, 0xD)
	g.addrange(0x20, 0x20)
	g.addrange(0xA0, 0xA0)
	g.addrange(0x2028, 0x2029)
	g.addrange(0xFEFF, 0xFEFF)
}

// C: static void addranges_S(void)
func (g *regexG) addrangesNS() {
	g.addrange(0, 0x9-1)
	g.addrange(0x9+1, 0xA-1)
	g.addrange(0xD+1, 0x20-1)
	g.addrange(0x20+1, 0xA0-1)
	g.addrange(0xA0+1, 0x2028-1)
	g.addrange(0x2029+1, 0xFEFF-1)
	g.addrange(0xFEFF+1, 0xFFFF)
}

// C: static void addranges_w(void)
func (g *regexG) addrangesW() {
	g.addrange('0', '9')
	g.addrange('A', 'Z')
	g.addrange('_', '_')
	g.addrange('a', 'z')
}

// C: static void addranges_W(void)
func (g *regexG) addrangesNW() {
	g.addrange(0, '0'-1)
	g.addrange('9'+1, 'A'-1)
	g.addrange('Z'+1, '_'-1)
	g.addrange('_'+1, 'a'-1)
	g.addrange('z'+1, 0xFFFF)
}

// C: static int g.lexclass(void)
func (g *regexG) lexclass() int {
	typ := lCclass
	var save reRune = 0

	g.newcclass()

	quoted := g.nextrune()
	if quoted == 0 && g.yychar == '^' {
		typ = lNcclass
		quoted = g.nextrune()
	}

	havesave, havedash := 0, 0
	for {
		if g.yychar == 0 {
			die("unterminated character class")
		}
		if quoted == 0 && g.yychar == ']' {
			break
		}

		if quoted == 0 && g.yychar == '-' {
			if havesave != 0 {
				if havedash != 0 {
					g.addrange(save, '-')
					havesave, havedash = 0, 0
				} else {
					havedash = 1
				}
			} else {
				save = '-'
				havesave = 1
			}
		} else if quoted != 0 && strings.IndexByte("DSWdsw", byte(g.yychar)) >= 0 {
			if havesave != 0 {
				g.addrange(save, save)
				if havedash != 0 {
					g.addrange('-', '-')
				}
			}
			switch g.yychar {
			case 'd':
				g.addrangesD()
			case 's':
				g.addrangesS()
			case 'w':
				g.addrangesW()
			case 'D':
				g.addrangesND()
			case 'S':
				g.addrangesNS()
			case 'W':
				g.addrangesNW()
			}
			havesave, havedash = 0, 0
		} else {
			if quoted != 0 {
				if g.yychar == 'b' {
					g.yychar = '\b'
				} else if g.yychar == '0' {
					g.yychar = 0
				}
			}
			if havesave != 0 {
				if havedash != 0 {
					g.addrange(save, g.yychar)
					havesave, havedash = 0, 0
				} else {
					g.addrange(save, save)
					save = g.yychar
				}
			} else {
				save = g.yychar
				havesave = 1
			}
		}

		quoted = g.nextrune()
	}

	if havesave != 0 {
		g.addrange(save, save)
		if havedash != 0 {
			g.addrange('-', '-')
		}
	}

	return typ
}

// C: static int g.lex(void)
func (g *regexG) lex() int {
	quoted := g.nextrune()
	if quoted != 0 {
		switch g.yychar {
		case 'b':
			return lWord
		case 'B':
			return lNword
		case 'd':
			g.newcclass()
			g.addrangesD()
			return lCclass
		case 's':
			g.newcclass()
			g.addrangesS()
			return lCclass
		case 'w':
			g.newcclass()
			g.addrangesW()
			return lCclass
		case 'D':
			g.newcclass()
			g.addrangesD()
			return lNcclass
		case 'S':
			g.newcclass()
			g.addrangesS()
			return lNcclass
		case 'W':
			g.newcclass()
			g.addrangesW()
			return lNcclass
		case '0':
			g.yychar = 0
			return lChar
		}
		if g.yychar >= '0' && g.yychar <= '9' {
			g.yychar -= '0'
			if b := byteAt(g.source, g.sourceIdx); b >= '0' && b <= '9' {
				g.yychar = g.yychar*10 + reRune(b) - '0'
				g.sourceIdx++
			}
			return lRef
		}
		return lChar
	}

	switch g.yychar {
	case 0, '$', ')', '*', '+', '.', '?', '^', '|':
		return int(g.yychar)
	}

	if g.yychar == '{' {
		return g.lexcount()
	}
	if g.yychar == '[' {
		return g.lexclass()
	}
	if g.yychar == '(' {
		if byteAt(g.source, g.sourceIdx) == '?' {
			switch byteAt(g.source, g.sourceIdx+1) {
			case ':':
				g.sourceIdx += 2
				return lNc
			case '=':
				g.sourceIdx += 2
				return lPla
			case '!':
				g.sourceIdx += 2
				return lNla
			}
		}
		return '('
	}

	return lChar
}

/* Parse */

const (
	pCat     = iota // C: P_CAT
	pAlt            // P_ALT
	pRep            // P_REP
	pBol            // P_BOL
	pEol            // P_EOL
	pWord           // P_WORD
	pNword          // P_NWORD
	pPar            // P_PAR
	pPla            // P_PLA
	pNla            // P_NLA
	pAny            // P_ANY
	pChar           // P_CHAR
	pCclass         // P_CCLASS
	pNcclass        // P_NCCLASS
	pRef            // P_REF
)

// C: static Renode *g.newnode(int type)
func (g *regexG) newnode(typ int) *renode {
	node := &g.pstart[g.pend]
	g.pend++
	node.typ = uint8(typ)
	node.cc = nil
	node.c = 0
	node.ng = 0
	node.m = 0
	node.n = 0
	node.x = nil
	node.y = nil
	return node
}

// C: static int empty(Renode *node)
func empty(node *renode) int {
	if node == nil {
		return 1
	}
	switch node.typ {
	case pCat:
		if empty(node.x) != 0 && empty(node.y) != 0 {
			return 1
		}
		return 0
	case pAlt:
		if empty(node.x) != 0 || empty(node.y) != 0 {
			return 1
		}
		return 0
	case pRep:
		if empty(node.x) != 0 || node.m == 0 {
			return 1
		}
		return 0
	case pPar:
		return empty(node.x)
	case pRef:
		return empty(node.x)
	case pAny, pChar, pCclass, pNcclass:
		return 0
	default:
		return 1
	}
}

// C: static Renode *g.newrep(Renode *atom, int ng, int min, int max)
func (g *regexG) newrep(atom *renode, ng, min, max int) *renode {
	rep := g.newnode(pRep)
	if max == repinf && empty(atom) != 0 {
		die("infinite loop matching the empty string")
	}
	rep.ng = uint8(ng)
	rep.m = uint8(min)
	rep.n = uint8(max)
	rep.x = atom
	return rep
}

// C: static void next(void)
func (g *regexG) reNext() {
	g.lookahead = g.lex()
}

// C: static int g.accept(int t)
func (g *regexG) accept(t int) int {
	if g.lookahead == t {
		g.reNext()
		return 1
	}
	return 0
}

// C: static Renode *g.parseatom(void)
func (g *regexG) parseatom() *renode {
	var atom *renode
	if g.lookahead == lChar {
		atom = g.newnode(pChar)
		atom.c = g.yychar
		g.reNext()
		return atom
	}
	if g.lookahead == lCclass {
		atom = g.newnode(pCclass)
		atom.cc = g.yycc
		g.reNext()
		return atom
	}
	if g.lookahead == lNcclass {
		atom = g.newnode(pNcclass)
		atom.cc = g.yycc
		g.reNext()
		return atom
	}
	if g.lookahead == lRef {
		atom = g.newnode(pRef)
		if g.yychar == 0 || g.yychar >= reRune(g.nsub) || g.sub[g.yychar] == nil {
			die("invalid back-reference")
		}
		atom.n = uint8(g.yychar)
		atom.x = g.sub[g.yychar]
		g.reNext()
		return atom
	}
	if g.accept('.') != 0 {
		return g.newnode(pAny)
	}
	if g.accept('(') != 0 {
		atom = g.newnode(pPar)
		if g.nsub == maxsub {
			die("too many captures")
		}
		atom.n = uint8(g.nsub)
		g.nsub++
		atom.x = g.parsealt()
		g.sub[atom.n] = atom
		if g.accept(')') == 0 {
			die("unmatched '('")
		}
		return atom
	}
	if g.accept(lNc) != 0 {
		atom = g.parsealt()
		if g.accept(')') == 0 {
			die("unmatched '('")
		}
		return atom
	}
	if g.accept(lPla) != 0 {
		atom = g.newnode(pPla)
		atom.x = g.parsealt()
		if g.accept(')') == 0 {
			die("unmatched '('")
		}
		return atom
	}
	if g.accept(lNla) != 0 {
		atom = g.newnode(pNla)
		atom.x = g.parsealt()
		if g.accept(')') == 0 {
			die("unmatched '('")
		}
		return atom
	}
	die("syntax error")
	return nil
}

// C: static Renode *g.parserep(void)
func (g *regexG) parserep() *renode {
	if g.accept('^') != 0 {
		return g.newnode(pBol)
	}
	if g.accept('$') != 0 {
		return g.newnode(pEol)
	}
	if g.accept(lWord) != 0 {
		return g.newnode(pWord)
	}
	if g.accept(lNword) != 0 {
		return g.newnode(pNword)
	}

	atom := g.parseatom()
	if g.lookahead == lCount {
		min, max := g.yymin, g.yymax
		g.reNext()
		if max < min {
			die("invalid quantifier")
		}
		return g.newrep(atom, g.accept('?'), min, max)
	}
	if g.accept('*') != 0 {
		return g.newrep(atom, g.accept('?'), 0, repinf)
	}
	if g.accept('+') != 0 {
		return g.newrep(atom, g.accept('?'), 1, repinf)
	}
	if g.accept('?') != 0 {
		return g.newrep(atom, g.accept('?'), 0, 1)
	}
	return atom
}

// C: static Renode *g.parsecat(void)
func (g *regexG) parsecat() *renode {
	var cat *renode
	if g.lookahead != 0 && g.lookahead != '|' && g.lookahead != ')' {
		cat = g.parserep()
		for g.lookahead != 0 && g.lookahead != '|' && g.lookahead != ')' {
			x := cat
			cat = g.newnode(pCat)
			cat.x = x
			cat.y = g.parserep()
		}
		return cat
	}
	return nil
}

// C: static Renode *g.parsealt(void)
func (g *regexG) parsealt() *renode {
	alt := g.parsecat()
	for g.accept('|') != 0 {
		x := alt
		alt = g.newnode(pAlt)
		alt.x = x
		alt.y = g.parsecat()
	}
	return alt
}

/* Compile */

const (
	iEnd     = iota // C: I_END
	iJump           // I_JUMP
	iSplit          // I_SPLIT
	iPla            // I_PLA
	iNla            // I_NLA
	iAnynl          // I_ANYNL
	iAny            // I_ANY
	iChar           // I_CHAR
	iCclass         // I_CCLASS
	iNcclass        // I_NCCLASS
	iRef            // I_REF
	iBol            // I_BOL
	iEol            // I_EOL
	iWord           // I_WORD
	iNword          // I_NWORD
	iLpar           // I_LPAR
	iRpar           // I_RPAR
)

// C: static unsigned int count(Renode *node)
func count(node *renode) uint {
	if node == nil {
		return 0
	}
	switch node.typ {
	case pCat:
		return count(node.x) + count(node.y)
	case pAlt:
		return count(node.x) + count(node.y) + 2
	case pRep:
		min := uint(node.m)
		max := uint(node.n)
		if min == max {
			return count(node.x) * min
		}
		if max < repinf {
			return count(node.x)*max + (max - min)
		}
		return count(node.x)*(min+1) + 2
	case pPar:
		return count(node.x) + 2
	case pPla:
		return count(node.x) + 2
	case pNla:
		return count(node.x) + 2
	default:
		return 1
	}
}

// C: static Reinst *emit(Reprog *prog, int opcode) — returns inst index
func emit(prog *Reprog, opcode int) int {
	idx := prog.end
	prog.end++
	inst := &prog.insts[idx]
	inst.opcode = uint8(opcode)
	inst.n = 0
	inst.c = 0
	inst.cc = nil
	inst.x = 0
	inst.y = 0
	return idx
}

// C: static void compile(Reprog *prog, Renode *node)
func compile(prog *Reprog, node *renode) {
	var inst, split, jump int

	if node == nil {
		return
	}

	switch node.typ {
	case pCat:
		compile(prog, node.x)
		compile(prog, node.y)

	case pAlt:
		split = emit(prog, iSplit)
		compile(prog, node.x)
		jump = emit(prog, iJump)
		compile(prog, node.y)
		prog.insts[split].x = split + 1
		prog.insts[split].y = jump + 1
		prog.insts[jump].x = prog.end

	case pRep:
		for i := uint8(0); i < node.m; i++ {
			inst = prog.end
			compile(prog, node.x)
		}
		if node.m == node.n {
			break
		}
		if node.n < repinf {
			for i := node.m; i < node.n; i++ {
				split = emit(prog, iSplit)
				compile(prog, node.x)
				if node.ng != 0 {
					prog.insts[split].y = split + 1
					prog.insts[split].x = prog.end
				} else {
					prog.insts[split].x = split + 1
					prog.insts[split].y = prog.end
				}
			}
		} else if node.m == 0 {
			split = emit(prog, iSplit)
			compile(prog, node.x)
			jump = emit(prog, iJump)
			if node.ng != 0 {
				prog.insts[split].y = split + 1
				prog.insts[split].x = prog.end
			} else {
				prog.insts[split].x = split + 1
				prog.insts[split].y = prog.end
			}
			prog.insts[jump].x = split
		} else {
			split = emit(prog, iSplit)
			if node.ng != 0 {
				prog.insts[split].y = inst
				prog.insts[split].x = prog.end
			} else {
				prog.insts[split].x = inst
				prog.insts[split].y = prog.end
			}
		}

	case pBol:
		emit(prog, iBol)
	case pEol:
		emit(prog, iEol)
	case pWord:
		emit(prog, iWord)
	case pNword:
		emit(prog, iNword)

	case pPar:
		inst = emit(prog, iLpar)
		prog.insts[inst].n = node.n
		compile(prog, node.x)
		inst = emit(prog, iRpar)
		prog.insts[inst].n = node.n
	case pPla:
		split = emit(prog, iPla)
		compile(prog, node.x)
		emit(prog, iEnd)
		prog.insts[split].x = split + 1
		prog.insts[split].y = prog.end
	case pNla:
		split = emit(prog, iNla)
		compile(prog, node.x)
		emit(prog, iEnd)
		prog.insts[split].x = split + 1
		prog.insts[split].y = prog.end

	case pAny:
		emit(prog, iAny)
	case pChar:
		inst = emit(prog, iChar)
		if prog.flags&regIcase != 0 {
			prog.insts[inst].c = canon(node.c)
		} else {
			prog.insts[inst].c = node.c
		}
	case pCclass:
		inst = emit(prog, iCclass)
		prog.insts[inst].cc = node.cc
	case pNcclass:
		inst = emit(prog, iNcclass)
		prog.insts[inst].cc = node.cc
	case pRef:
		inst = emit(prog, iRef)
		prog.insts[inst].n = node.n
	}
}

// C: Reprog *myregcomp(const char *pattern, int cflags, const char **errorp)
// Returns (nil, errmsg) on error (C: NULL + *errorp via setjmp/longjmp).
func Myregcomp(pattern string, cflags int) (prog *Reprog, errmsg string) {
	g := &regexG{}
	g.prog = &Reprog{}
	// C: g.pstart = g.pend = malloc(sizeof(Renode) * strlen(pattern) * 2)
	g.pstart = make([]renode, len(pattern)*2+2)
	g.pend = 0
	g.err = nil

	// C: if(setjmp(g.kaboom)) { *errorp = g.error; return NULL; }
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(reError); ok {
				prog = nil
				errmsg = string(e)
				return
			}
			panic(r)
		}
	}()

	g.source = pattern
	g.sourceIdx = 0
	g.ncclass = 0
	g.nsub = 1
	for i := range maxsub {
		g.sub[i] = nil
	}

	g.prog.flags = cflags

	g.reNext()
	node := g.parsealt()
	if g.lookahead == ')' {
		die("unmatched ')'")
	}
	if g.lookahead != 0 {
		die("syntax error")
	}

	g.prog.nsub = g.nsub
	// C: g.prog->start = g.prog->end = malloc((count(node) + 6) * sizeof(Reinst))
	g.prog.insts = make([]reinst, count(node)+6)
	g.prog.end = 0

	split := emit(g.prog, iSplit)
	g.prog.insts[split].x = split + 3
	g.prog.insts[split].y = split + 1
	emit(g.prog, iAnynl)
	jump := emit(g.prog, iJump)
	g.prog.insts[jump].x = split
	emit(g.prog, iLpar)
	compile(g.prog, node)
	emit(g.prog, iRpar)
	emit(g.prog, iEnd)

	return g.prog, ""
}

// C: void myregfree(Reprog *prog)
func Myregfree(prog *Reprog) {
	// C: free(prog->start); free(prog); — Go GC
}

/* Match */

// C: static int isnewline(int c)
func isnewline(c reRune) int {
	if c == 0xA || c == 0xD || c == 0x2028 || c == 0x2029 {
		return 1
	}
	return 0
}

// C: static int iswordchar(int c)
func iswordchar(c reRune) int {
	if c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') {
		return 1
	}
	return 0
}

// C: static int incclass(Reclass *cc, Rune c)
func incclass(cc *reclass, c reRune) int {
	for p := 0; p < cc.end; p += 2 {
		if cc.spans[p] <= c && c <= cc.spans[p+1] {
			return 1
		}
	}
	return 0
}

// C: static int incclasscanon(Reclass *cc, Rune c)
func incclasscanon(cc *reclass, c reRune) int {
	for p := 0; p < cc.end; p += 2 {
		for r := cc.spans[p]; r <= cc.spans[p+1]; r++ {
			if c == canon(r) {
				return 1
			}
		}
	}
	return 0
}

// C: static int strncmpcanon(const char *a, const char *b, unsigned int n)
func strncmpcanon(s string, a, b int, n uint) int {
	for n > 0 {
		n--
		if byteAt(s, a) == 0 {
			return -1
		}
		if byteAt(s, b) == 0 {
			return 1
		}
		ra, wa := chartorune(s, a)
		rb, wb := chartorune(s, b)
		a += wa
		b += wb
		c := int(canon(ra)) - int(canon(rb))
		if c != 0 {
			return c
		}
	}
	return 0
}

// C: strncmp on byte positions (stops at NUL like C strncmp)
func strncmppos(s string, a, b int, n int) int {
	for k := range n {
		ba := byteAt(s, a+k)
		bb := byteAt(s, b+k)
		if ba != bb {
			return int(ba) - int(bb)
		}
		if ba == 0 {
			return 0
		}
	}
	return 0
}

// C: struct Rethread — pc = inst index, sp = byte offset, sub = captures
type rethread struct {
	pc  int
	sp  int
	sub Resub
}

// C: static void spawn(Rethread *t, Reinst *pc, const char *sp, Resub *sub)
func spawn(t *rethread, pc int, sp int, sub *Resub) {
	t.pc = pc
	t.sp = sp
	t.sub = *sub
}

// C: static int match(Reinst *pc, const char *sp, const char *bol, int flags, Resub *out)
// pc = inst index, sp/bol = byte offsets into s.
func match(prog *Reprog, pci, spi, bol int, flags int, out *Resub, s string) int {
	ready := make([]rethread, maxThreadStack) // C: readystack[64]
	var scratch, sub Resub
	var c reRune
	var nready uint
	var i int
	var w int

	// C: spawn(ready, pc, sp, out); nready = 1
	spawn(&ready[0], pci, spi, out)
	nready = 1

	for nready > 0 {
		nready--
		pc := ready[nready].pc
		sp := ready[nready].sp
		sub = ready[nready].sub
		for {
			inst := &prog.insts[pc]
			switch inst.opcode {
			case iEnd:
				for i = range maxsub {
					out.Sub[i].Sp = sub.Sub[i].Sp
					out.Sub[i].Ep = sub.Sub[i].Ep
				}
				return 1
			case iJump:
				pc = inst.x
				continue
			case iSplit:
				if nready >= 2000 {
					// C: fprintf(stderr, "regexec: backtrack overflow!\n")
					return 0
				}

				if int(nready) == len(ready) {
					// C: readystacksize *= 2; realloc
					nr := make([]rethread, len(ready)*2)
					copy(nr, ready)
					ready = nr
				}

				spawn(&ready[nready], inst.y, sp, &sub)
				nready++
				pc = inst.x
				continue

			case iPla:
				if match(prog, inst.x, sp, bol, flags, &sub, s) == 0 {
					goto dead
				}
				pc = inst.y
				continue
			case iNla:
				scratch = sub
				if match(prog, inst.x, sp, bol, flags, &scratch, s) != 0 {
					goto dead
				}
				pc = inst.y
				continue

			case iAnynl:
				c, w = chartorune(s, sp)
				sp += w
				if c == 0 {
					goto dead
				}
			case iAny:
				c, w = chartorune(s, sp)
				sp += w
				if c == 0 {
					goto dead
				}
				if isnewline(c) != 0 {
					goto dead
				}
			case iChar:
				c, w = chartorune(s, sp)
				sp += w
				if c == 0 {
					goto dead
				}
				if flags&regIcase != 0 {
					c = canon(c)
				}
				if c != inst.c {
					goto dead
				}
			case iCclass:
				c, w = chartorune(s, sp)
				sp += w
				if c == 0 {
					goto dead
				}
				if flags&regIcase != 0 {
					if incclasscanon(inst.cc, canon(c)) == 0 {
						goto dead
					}
				} else {
					if incclass(inst.cc, c) == 0 {
						goto dead
					}
				}
			case iNcclass:
				c, w = chartorune(s, sp)
				sp += w
				if c == 0 {
					goto dead
				}
				if flags&regIcase != 0 {
					if incclasscanon(inst.cc, canon(c)) != 0 {
						goto dead
					}
				} else {
					if incclass(inst.cc, c) != 0 {
						goto dead
					}
				}
			case iRef:
				i = sub.Sub[inst.n].Ep - sub.Sub[inst.n].Sp
				if flags&regIcase != 0 {
					if strncmpcanon(s, sp, sub.Sub[inst.n].Sp, uint(i)) != 0 {
						goto dead
					}
				} else {
					if strncmppos(s, sp, sub.Sub[inst.n].Sp, i) != 0 {
						goto dead
					}
				}
				if i > 0 {
					sp += i
				}

			case iBol:
				if sp == bol && flags&regNotbol == 0 {
					break
				}
				if flags&regNewline != 0 {
					if sp > bol && isnewline(reRune(byteAt(s, sp-1))) != 0 {
						break
					}
				}
				goto dead
			case iEol:
				if byteAt(s, sp) == 0 {
					break
				}
				if flags&regNewline != 0 {
					if isnewline(reRune(byteAt(s, sp))) != 0 {
						break
					}
				}
				goto dead
			case iWord:
				if sp > bol && iswordchar(reRune(byteAt(s, sp-1))) != 0 {
					i = 1
				} else {
					i = 0
				}
				i ^= iswordchar(reRune(byteAt(s, sp)))
				if i != 0 {
					break
				}
				goto dead
			case iNword:
				if sp > bol && iswordchar(reRune(byteAt(s, sp-1))) != 0 {
					i = 1
				} else {
					i = 0
				}
				i ^= iswordchar(reRune(byteAt(s, sp)))
				if i == 0 {
					break
				}
				goto dead

			case iLpar:
				sub.Sub[inst.n].Sp = sp
			case iRpar:
				sub.Sub[inst.n].Ep = sp
			default:
				goto dead
			}
			pc = pc + 1
		}
	dead:
	}
	return 0
}

// C: int myregexec(Reprog *prog, const char *sp, Resub *sub, int eflags)
// Returns 0 on match (C convention).
func Myregexec(prog *Reprog, sp string, sub *Resub, eflags int) int {
	var scratch Resub

	if sub == nil {
		sub = &scratch
	}

	sub.Nsub = prog.nsub
	for i := range maxsub {
		sub.Sub[i].Sp = -1 // C: NULL
		sub.Sub[i].Ep = -1
	}

	if match(prog, 0, 0, 0, prog.flags|eflags, sub, sp) != 0 {
		return 0
	}
	return 1
}
