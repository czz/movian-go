package misc

// Port of src/misc/regex.c + regex.h
// Wraps the minilib regexp engine (ext/minilibs/regexp.c).

// C: static HTS_MUTEX_DECL(regcompmutex) — unneeded: the parser state
// (C's static regex_g) is now a per-call regexG allocated in Myregcomp.

// C: typedef struct { void *r; } hts_regex_t
type HtsRegex struct {
	r *Reprog
}

// C: typedef struct { int rm_so; int rm_eo; } hts_regmatch_t
type HtsRegmatch struct {
	RmSo int
	RmEo int
}

// C: int hts_regcomp(hts_regex_t *r, const char *pat, const char **errmsg)
// Returns 0 on success; errmsg returned when non-nil on failure.
func HtsRegcomp(r *HtsRegex, pat string, errmsg *string) int {
	prog, e := Myregcomp(pat, 0)
	r.r = prog
	if prog == nil {
		if errmsg != nil {
			*errmsg = e
		}
		return 1
	}
	if errmsg != nil {
		*errmsg = ""
	}
	return 0
}

// C: int hts_regexec(hts_regex_t *r, const char *text,
//
//	int nmatches, hts_regmatch_t *matches)
//
// Returns 0 on match.
func HtsRegexec(r *HtsRegex, text string, nmatches int, matches []HtsRegmatch) int {
	var m Resub
	if r.r == nil {
		return 1
	}

	if Myregexec(r.r, text, &m, 0) != 0 {
		return 1
	}

	i := 0
	for ; i < int(m.Nsub) && i < nmatches; i++ {
		// C: matches[i].rm_so = m.sub[i].sp - text
		matches[i].RmSo = m.Sub[i].Sp
		matches[i].RmEo = m.Sub[i].Ep
	}
	for ; i < nmatches; i++ {
		matches[i].RmSo = -1
		matches[i].RmEo = -1
	}
	return 0
}

// C: void hts_regfree(hts_regex_t *r)
func HtsRegfree(r *HtsRegex) {
	Myregfree(r.r)
}
