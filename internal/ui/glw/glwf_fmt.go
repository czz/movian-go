package glw

// C: src/ui/glw/glw_view_eval.c — builtin function table (funcvec) and all
// glwf_* handlers. Ported 1:1 from the canonical C implementation.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	archpkg "github.com/czz/movian-go/internal/arch"
	miscpkg "github.com/czz/movian-go/internal/misc"
	nls "github.com/czz/movian-go/internal/nls"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// C: fmt helpers (glw_view_eval.c:4363-4559)
func fmtBuildFmt(zeropad, fl1, fl2 int, typ byte) string {
	s := "%"
	if zeropad != 0 {
		s += "0"
	}
	if fl1 != -1 {
		s += string(byte('0' + fl1))
	}
	if fl2 != -1 {
		s += "." + string(byte('0'+fl2))
	}
	s += string(typ)
	return s
}

func fmtAddInt(out *strings.Builder, v int, zeropad, fl1, fl2 int) {
	out.WriteString(fmt.Sprintf(fmtBuildFmt(zeropad, fl1, fl2, 'd'), v))
}

func fmtAddFloat(out *strings.Builder, v float32, zeropad, fl1, fl2 int) {
	out.WriteString(fmt.Sprintf(fmtBuildFmt(zeropad, fl1, fl2, 'f'), v))
}

func fmtAddString(out *strings.Builder, str string, outIsRich, strIsRich int) {
	if outIsRich != 0 && strIsRich == 0 {
		out.WriteString(htmlEscape(str))
	} else {
		out.WriteString(str)
	}
}

// C: dofmt (glw_view_eval.c:4416)
func dofmt(out *strings.Builder, fmts string, argv []*Token, rich int) {
	argptr := 0
	i := 0
	for i < len(fmts) {
		c := fmts[i]
		if c != '%' {
			out.WriteByte(c)
			i++
			continue
		}
		zeropad := 0
		off := i + 1
		fl1, fl2 := -1, -1
		if off < len(fmts) && fmts[off] == '0' {
			zeropad = 1
			off++
		}
		for off < len(fmts) && fmts[off] >= '0' && fmts[off] <= '9' {
			if fl1 == -1 {
				fl1 = 0
			}
			fl1 = fl1*10 + int(fmts[off]-'0')
			off++
		}
		if off < len(fmts) && fmts[off] == '.' {
			off++
			for off < len(fmts) && fmts[off] >= '0' && fmts[off] <= '9' {
				if fl2 == -1 {
					fl2 = 0
				}
				fl2 = fl2*10 + int(fmts[off]-'0')
				off++
			}
		}
		if off >= len(fmts) {
			break
		}
		typ := fmts[off]
		off++
		i = off

		if typ == '%' {
			out.WriteByte('%')
			continue
		}
		if argptr == len(argv) {
			continue
		}
		arg := argv[argptr]
		argptr++

		switch typ {
		case 'd':
			i32 := 0
			switch arg.typ {
			case tokenInt:
				i32 = arg.tInt
			case tokenFloat:
				i32 = int(arg.tFloat)
			}
			fmtAddInt(out, i32, zeropad, fl1, fl2)
		case 'f':
			flt := float32(0)
			switch arg.typ {
			case tokenInt:
				flt = float32(arg.tInt)
			case tokenFloat:
				flt = arg.tFloat
			}
			fmtAddFloat(out, flt, zeropad, fl1, fl2)
		default:
			switch arg.typ {
			case tokenInt:
				fmtAddInt(out, arg.tInt, zeropad, fl1, fl2)
			case tokenFloat:
				fmtAddFloat(out, arg.tFloat, zeropad, fl1, fl2)
			case tokenRstring:
				fmtAddString(out, miscpkg.RstrGet(arg.tRstring), rich, arg.tRstrType)
			case tokenCstring:
				fmtAddString(out, arg.tCstring, rich, 0)
			case tokenURI:
				fmtAddString(out, miscpkg.RstrGet(arg.tURITitle), rich, 0)
			}
		}
	}
}

// C: glwf_fmt (glw_view_eval.c:4559)
func glwfFmt(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	if argc < 1 {
		return glwViewSeterr(ec.ei, self, "fmt() requires at least one arguments")
	}
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	rich := 0
	var fmts string
	switch a.typ {
	case tokenRstring:
		fmts = miscpkg.RstrGet(a.tRstring)
		rich = a.tRstrType
	case tokenURI:
		fmts = miscpkg.RstrGet(a.tRstring)
	case tokenCstring:
		fmts = a.tCstring
	default:
		r := evalAlloc(self, ec, tokenRstring)
		r.tRstring = miscpkg.RstrAllocStr("")
		evalPush(ec, r)
		return 0
	}

	resArgs := make([]*Token, 0, argc-1)
	for i := uint(1); i < argc; i++ {
		t := tokenResolve(ec, argv[i])
		if t == nil {
			return -1
		}
		resArgs = append(resArgs, t)
	}

	var out strings.Builder
	dofmt(&out, fmts, resArgs, rich)
	r := evalAlloc(self, ec, tokenRstring)
	r.tRstring = miscpkg.RstrAllocStr(out.String())
	r.tRstrType = rich
	evalPush(ec, r)
	return 0
}

// C: token_cmp (glw_view_eval.c:4530)
func tokenCmp(a, b *Token) int {
	if a.typ == tokenInt && b.typ == tokenFloat {
		if float32(a.tInt) != b.tFloat {
			return 1
		}
		return 0
	}
	if a.typ == tokenFloat && b.typ == tokenInt {
		if a.tFloat != float32(b.tInt) {
			return 1
		}
		return 0
	}
	aa, aok := tokenAsString(a)
	bb, bok := tokenAsString(b)
	if aok && bok {
		return strings.Compare(aa, bb)
	}
	if a.typ != b.typ {
		return -1
	}
	switch a.typ {
	case tokenInt:
		return a.tInt - b.tInt
	case tokenFloat:
		if a.tFloat == b.tFloat {
			return 0
		}
		return 1
	default:
		return -1
	}
}

// C: glwf_translate (glw_view_eval.c:4564)
func glwfTranslate(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	if argc < 2 {
		return glwViewSeterr(ec.ei, self, "translate() requires at least two arguments")
	}
	if argc&1 != 0 {
		return glwViewSeterr(ec.ei, self, "translate() requires even number of arguments")
	}
	idx := tokenResolve(ec, argv[0])
	if idx == nil {
		return -1
	}
	def := tokenResolve(ec, argv[1])
	if def == nil {
		return -1
	}
	for i := uint(2); i < argc; i += 2 {
		k := tokenResolve(ec, argv[i])
		if k == nil {
			return -1
		}
		v := tokenResolve(ec, argv[i+1])
		if v == nil {
			return -1
		}
		if tokenCmp(idx, k) == 0 {
			evalPush(ec, v)
			return 0
		}
	}
	evalPush(ec, def)
	return 0
}

// C: glwf_strftime (glw_view_eval.c:4607)
func glwfStrftime(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	if b.typ != tokenRstring {
		return glwViewSeterr(ec.ei, self, "Invalid second operand to strftime()")
	}
	t := int64(token2int(ec, a))
	buf := ""
	if t != 0 {
		buf = glwStrftime(archpkg.Localtime(time.Unix(t, 0)), miscpkg.RstrGet(b.tRstring))
	}
	r := evalAlloc(self, ec, tokenRstring)
	r.tRstring = miscpkg.RstrAllocStr(buf)
	evalPush(ec, r)
	return 0
}

// C: glwf_isset (glw_view_eval.c:4644)
func glwfIsset(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	rv := 1
	switch a.typ {
	case tokenRstring, tokenURI:
		if miscpkg.RstrGet(a.tRstring) == "" {
			rv = 0
		}
	case tokenFloat:
		if a.tFloat == 0 {
			rv = 0
		}
	case tokenInt:
		if a.tInt == 0 {
			rv = 0
		}
	case tokenVoid:
		rv = 0
	}
	r := evalAlloc(self, ec, tokenInt)
	r.tInt = rv
	evalPush(ec, r)
	return 0
}

// C: glwf_isvoid (glw_view_eval.c:4686)
func glwfIsvoid(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	r := evalAlloc(self, ec, tokenInt)
	if a.typ == tokenVoid {
		r.tInt = 1
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_value2duration (glw_view_eval.c:4706)
func glwfValue2duration(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	if argc < 1 || argc > 2 {
		return glwViewSeterr(ec.ei, self, "value2duration(): Invalid number of arguments")
	}
	a := tokenResolve(ec, argv[0])
	var b *Token
	if argc > 1 {
		b = tokenResolve(ec, argv[1])
	}
	str := ""
	s := 0
	if a != nil {
		switch a.typ {
		case tokenRstring:
			str = miscpkg.RstrGet(a.tRstring)
		case tokenFloat:
			s = int(a.tFloat)
		case tokenInt:
			s = a.tInt
		default:
			str = ""
		}
		if a.typ == tokenFloat || a.typ == tokenInt {
			m := s / 60
			h := s / 3600
			if h > 0 || (b != nil && token2bool(b)) {
				str = fmt.Sprintf("%d:%02d:%02d", h, m%60, s%60)
			} else {
				str = fmt.Sprintf("%d:%02d", m%60, s%60)
			}
		}
	}
	r := evalAlloc(self, ec, tokenRstring)
	r.tRstring = miscpkg.RstrAllocStr(str)
	evalPush(ec, r)
	return 0
}

// C: glwf_value2size (glw_view_eval.c:4764)
func glwfValue2size(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	str := ""
	var s float64
	if a != nil {
		switch a.typ {
		case tokenRstring:
			str = miscpkg.RstrGet(a.tRstring)
		case tokenFloat:
			s = float64(a.tFloat)
		case tokenInt:
			s = float64(a.tInt)
		default:
			str = ""
		}
		if a.typ == tokenFloat || a.typ == tokenInt {
			if s > 1000*1000*1000 {
				str = fmt.Sprintf("%.1f GB", s/1000000000.0)
			} else if s > 1000*1000 {
				str = fmt.Sprintf("%.1f MB", s/1000000.0)
			} else if s > 1000 {
				str = fmt.Sprintf("%.1f kB", s/1000.0)
			} else {
				str = fmt.Sprintf("%d B", int(s))
			}
		}
	}
	r := evalAlloc(self, ec, tokenRstring)
	r.tRstring = miscpkg.RstrAllocStr(str)
	evalPush(ec, r)
	return 0
}

// C: glwf_value2quantity (glw_view_eval.c:4819)
func glwfValue2quantity(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	str := ""
	var s float64
	if a != nil {
		switch a.typ {
		case tokenRstring:
			str = miscpkg.RstrGet(a.tRstring)
		case tokenFloat:
			s = float64(a.tFloat)
		case tokenInt:
			s = float64(a.tInt)
		default:
			str = ""
		}
		if a.typ == tokenFloat || a.typ == tokenInt {
			if s > 1000*1000 {
				str = fmt.Sprintf("%.1fM", s/1000000.0)
			} else if s > 1000 {
				str = fmt.Sprintf("%.1fk", s/1000.0)
			} else {
				str = strconv.Itoa(int(s))
			}
		}
	}
	r := evalAlloc(self, ec, tokenRstring)
	r.tRstring = miscpkg.RstrAllocStr(str)
	evalPush(ec, r)
	return 0
}

// C: glwf_sin (glw_view_eval.c:5523)
func glwfSin(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	r := evalAlloc(self, ec, tokenFloat)
	if a.typ == tokenFloat {
		r.tFloat = float32(math.Sin(float64(a.tFloat)))
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_sinewave (glw_view_eval.c:5543)
func glwfSinewave(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gr := ec.w.glwRoot
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	p := token2float(ec, a)
	inc := float32(math.Pi*2) / (p * gr.grFramerate)
	self.tExtraFloat += inc
	if self.tExtraFloat >= math.Pi*2 {
		self.tExtraFloat -= math.Pi * 2
	}
	glwNeedRefresh(gr, 0)
	r := evalAlloc(self, ec, tokenFloat)
	r.tFloat = float32(math.Sin(float64(self.tExtraFloat)))
	evalPush(ec, r)
	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	return 0
}

// C: glwf_monotime (glw_view_eval.c:5572)
func glwfMonotime(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gr := ec.w.glwRoot
	r := evalAlloc(self, ec, tokenFloat)
	r.tFloat = float32(gr.grTimeSec)
	evalPush(ec, r)
	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	return 0
}

// C: glwf_rand (glw_view_eval.c:5590)
func glwfRand(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	gr := ec.w.glwRoot
	if self.tExtraInt == 0 {
		gr.grRandom = int((archpkg.GetTS()^int64(gr.grRandom))*1664525 + 1013904223)
		self.tExtraInt = 0x10000 | (gr.grRandom & 0xffff)
	}
	r := evalAlloc(self, ec, tokenFloat)
	r.tFloat = float32(self.tExtraInt&0xffff) / 65535.0
	evalPush(ec, r)
	return 0
}

// C: glwf_int / glwf_clamp (glw_view_eval.c:6164-6226)
func glwfInt(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	r := evalAlloc(self, ec, tokenInt)
	r.tInt = token2int(ec, a)
	evalPush(ec, r)
	return 0
}

func glwfClamp(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	c := tokenResolve(ec, argv[2])
	if c == nil {
		return -1
	}
	var r *Token
	switch a.typ {
	case tokenInt:
		r = evalAlloc(self, ec, tokenInt)
		r.tInt = int(glwClamp(float32(a.tInt), float32(token2int(ec, b)), float32(token2int(ec, c))))
	case tokenFloat:
		r = evalAlloc(self, ec, tokenFloat)
		r.tFloat = glwClamp(a.tFloat, token2float(ec, b), token2float(ec, c))
	default:
		r = evalAlloc(self, ec, tokenVoid)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_join (glw_view_eval.c:6231)
func glwfJoin(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	if argc < 2 {
		return glwViewSeterr(ec.ei, self, "join() requires at least two arguments")
	}
	sep := tokenResolve(ec, argv[0])
	if sep == nil {
		return -1
	}
	if sep.typ != tokenRstring {
		return glwViewSeterr(ec.ei, sep, "first arg (separator) must be a string")
	}
	parts := make([]string, 0, argc)
	rich := make([]int, 0, argc)
	nriches := 0
	for i := uint(1); i < argc; i++ {
		t := tokenResolve(ec, argv[i])
		if t == nil {
			continue
		}
		switch t.typ {
		case tokenRstring:
			parts = append(parts, miscpkg.RstrGet(t.tRstring))
			isRich := 0
			if t.tRstrType == int(propcore.PropStrRich) {
				isRich = 1
				nriches++
			}
			rich = append(rich, isRich)
		case tokenURI:
			parts = append(parts, miscpkg.RstrGet(t.tRstring))
			rich = append(rich, 0)
		case tokenCstring:
			parts = append(parts, t.tCstring)
			rich = append(rich, 0)
		}
	}
	sepRich := sep.tRstrType == int(propcore.PropStrRich)
	sepStr := miscpkg.RstrGet(sep.tRstring)

	r := evalAlloc(self, ec, tokenRstring)
	if nriches == 0 && !sepRich {
		// No rich texts
	} else if nriches == len(parts) && sepRich {
		r.tRstrType = int(propcore.PropStrRich)
	} else {
		r.tRstrType = int(propcore.PropStrRich)
		if !sepRich {
			sepStr = htmlEscape(sepStr)
		}
		for i := range parts {
			if rich[i] == 0 {
				parts[i] = htmlEscape(parts[i])
			}
		}
	}
	r.tRstring = miscpkg.RstrAllocStr(strings.Join(parts, sepStr))
	evalPush(ec, r)
	return 0
}

// C: glwf_pluralise (glw_view_eval.c:6331)
func glwfPluralise(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	b := tokenResolve(ec, argv[1])
	if b == nil {
		return -1
	}
	c := tokenResolve(ec, argv[2])
	if c == nil {
		return -1
	}
	if a.typ != tokenRstring {
		return glwViewSeterr(ec.ei, a, "first arg must be a string")
	}
	if b.typ != tokenRstring {
		return glwViewSeterr(ec.ei, b, "second arg must be a string")
	}
	r := evalAlloc(self, ec, tokenRstring)
	r.tRstring = miscpkg.RstrAllocStr(nls.GetRStringP(miscpkg.RstrGet(a.tRstring),
		miscpkg.RstrGet(b.tRstring), token2int(ec, c)))
	evalPush(ec, r)
	return 0
}

// C: glwf_abs (glw_view_eval.c:6917)
func glwfAbs(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	var r *Token
	switch a.typ {
	case tokenFloat:
		r = evalAlloc(self, ec, tokenFloat)
		r.tFloat = float32(math.Abs(float64(a.tFloat)))
	case tokenInt:
		r = evalAlloc(self, ec, tokenInt)
		if a.tInt < 0 {
			r.tInt = -a.tInt
		} else {
			r.tInt = a.tInt
		}
	default:
		r = evalAlloc(self, ec, tokenInt)
	}
	evalPush(ec, r)
	return 0
}

// C: glwf_timeAgo (glw_view_eval.c:7122)
func glwfTimeAgo(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	seconds := token2int(ec, a)
	var fmts string
	v := 0
	if seconds < 0 {
		r := evalAlloc(self, ec, tokenVoid)
		evalPush(ec, r)
		return 0
	}
	if seconds < 60 {
		fmts = nls.GetRString("Just now")
	} else if seconds < 120 {
		fmts = nls.GetRString("One minute ago")
	} else if seconds < 3600 {
		fmts = nls.GetRString("%d minutes ago")
		v = seconds / 60
	} else if seconds < 7200 {
		fmts = nls.GetRString("One hour ago")
	} else if seconds < 86400 {
		fmts = nls.GetRString("%d hours ago")
		v = seconds / 3600
	} else if seconds < 86400*2 {
		fmts = nls.GetRString("One day ago")
	} else if seconds < 86400*14 {
		fmts = nls.GetRString("%d days ago")
		v = seconds / 86400
	} else if seconds < 86400*50 {
		fmts = nls.GetRString("%d weeks ago")
		v = seconds / 604800
	} else if seconds < 86400*365 {
		v = seconds / 2592000
		if v == 1 {
			fmts = nls.GetRString("One month ago")
		} else {
			fmts = nls.GetRString("%d months ago")
		}
	} else {
		fmts = nls.GetRString("%d years ago")
		v = seconds / 31556736
	}
	str := fmt.Sprintf(fmts, v)

	ec.dynamicEval |= GLW_VIEW_EVAL_LAYOUT
	gr := ec.w.glwRoot
	glwScheduleRefresh(gr, gr.grFrameStart+30*1000000)
	r := evalAlloc(self, ec, tokenRstring)
	r.tRstring = miscpkg.RstrAllocStr(str)
	evalPush(ec, r)
	return 0
}

// C: glwf_rgb_to_string (glw_view_eval.c:7217)
func glwfRgbToString(ec *glwViewEvalContext, self *Token, argv []*Token, argc uint) int {
	a := tokenResolve(ec, argv[0])
	if a == nil {
		return -1
	}
	var r *Token
	if a.typ == tokenVectorFloat {
		r = evalAlloc(self, ec, tokenRstring)
		r.tRstring = miscpkg.RstrAllocStr(fmt.Sprintf("#%02x%02x%02x",
			int(a.tFloatVector[0]*255.0),
			int(a.tFloatVector[1]*255.0),
			int(a.tFloatVector[2]*255.0)))
	} else {
		r = evalAlloc(self, ec, tokenVoid)
	}
	evalPush(ec, r)
	return 0
}

// glwStrftime — C: strftime() via arch_localtime. Go has no strftime; map the
// common specifiers to Go reference-time layout (same mapping as pkg/glw).
func glwStrftime(t time.Time, format string) string {
	goFormat := format
	goFormat = strings.ReplaceAll(goFormat, "%Y", "2006")
	goFormat = strings.ReplaceAll(goFormat, "%m", "01")
	goFormat = strings.ReplaceAll(goFormat, "%d", "02")
	goFormat = strings.ReplaceAll(goFormat, "%H", "15")
	goFormat = strings.ReplaceAll(goFormat, "%M", "04")
	goFormat = strings.ReplaceAll(goFormat, "%S", "05")
	goFormat = strings.ReplaceAll(goFormat, "%F", "2006-01-02")
	goFormat = strings.ReplaceAll(goFormat, "%T", "15:04:05")
	return t.Format(goFormat)
}
