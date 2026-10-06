package text

// C: src/text/fontconfig.c — font family resolution via libfontconfig.
//
// fc state lives on ft (C: static FcConfig *fc_config; static int
// fc_started — fontconfig.c). sys.ft.fcConfig is an unsafe.Pointer so the
// backend-specific FcConfig handle stays confined to the fc* seam.

// C: FC_* object names (fontconfig.h) — passed through the seam as a
// selector so neither backend needs to know the C header strings.
const (
	fcObjFamily   = iota // FC_FAMILY
	fcObjOutline         // FC_OUTLINE
	fcObjWeight          // FC_WEIGHT
	fcObjSlant           // FC_SLANT
	fcObjScalable        // FC_SCALABLE
	fcObjCharset         // FC_CHARSET
	fcObjFile            // FC_FILE
)

// C: FcMatchPattern (fontconfig.h), FcTrue/FcResultMatch.
const (
	fcMatchPattern = 0
	fcTrue         = 1
	fcResultMatch  = 0
)

// C: static void fontconfig_init(void)
func (sys *System) fontconfigStart() {
	sys.ft.fcConfig = fcInit()
	sys.ft.fcInitialized = true
}

// C: int fontconfig_resolve(int uc, uint8_t style, const char *family,
//
//	char *urlbuf, size_t urllen)
//
// Returns 0 on success (urlbuf holds "file://<path>"), 1 on failure.
func (sys *System) FontconfigResolve(uc int, style uint8, family string, urlbuf []byte) int {
	rval := 1

	if !sys.ft.fcInitialized {
		sys.fontconfigStart()
	}

	if sys.ft.fcConfig == nil {
		return 1
	}

	if family == "" {
		family = "Arial"
	}

	pat := fcPatternCreate()
	var sorted fcFontSet

	fcPatternAddString(pat, fcObjFamily, family)
	fcPatternAddBool(pat, fcObjOutline, fcTrue)
	weight := 80
	if style&TR_STYLE_BOLD != 0 {
		weight = 200
	}
	fcPatternAddInteger(pat, fcObjWeight, weight)
	slant := 0
	if style&TR_STYLE_ITALIC != 0 {
		slant = 110
	}
	fcPatternAddInteger(pat, fcObjSlant, slant)

	fcDefaultSubstitute(pat)

	// C: goto done pattern
	done := func() int {
		if pat != nil {
			fcPatternDestroy(pat)
		}
		if sorted != nil {
			fcFontSetDestroy(sorted)
		}
		return rval
	}

	if fcConfigSubstitute(sys.ft.fcConfig, pat, fcMatchPattern) == 0 {
		return done()
	}

	sorted, _ = fcFontSort(sys.ft.fcConfig, pat, fcTrue)

	var fp fcPattern
	n := fcFontSetNFont(sorted)
	i := 0
	for ; i < n; i++ {
		fp = fcFontSetFont(sorted, i)

		outline := fcPatternGetBoolVal(fp, fcObjOutline, 0)
		scalable := fcPatternGetBoolVal(fp, fcObjScalable, 0)

		if outline == 0 || scalable == 0 {
			continue
		}

		rCharset, res := fcPatternGetCharSet(fp, fcObjCharset, 0)
		if res != fcResultMatch {
			continue
		}
		if fcCharSetHasChar(rCharset, uint32(uc)) != 0 {
			break
		}
	}

	if i == n {
		return done()
	}

	fname, result := fcPatternGetString(fp, fcObjFile, 0)
	if result == fcResultMatch {
		url := "file://" + fname
		copy(urlbuf, url)
		if len(urlbuf) > len(url) {
			urlbuf[len(url)] = 0
		}
		rval = 0
	}

	return done()
}
