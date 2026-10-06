package metadata

// Canonical 1:1 port of src/metadata/metadata_str.c
//
// Filename/folder heuristics used by the metadata lazy loader (mlp.c) to
// derive query titles, years, seasons and episodes.

import (
	"github.com/czz/movian-go/internal/gconf"
	"regexp"
	"strconv"
	"strings"

	fileaccesscore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
)

// isnum — C: #define isnum(a) ((a) >= '0' && (a) <= '9')
func isnum(a byte) bool {
	return a >= '0' && a <= '9'
}

// atoiPrefix — C: atoi on a string position; parses leading digits
// (empty prefix → 0, like atoi on a non-digit).
func atoiPrefix(s string) int {
	n := 0
	for n < len(s) && isnum(s[n]) {
		n++
	}
	v, _ := strconv.Atoi(s[:n])
	return v
}

// stopstrings — C: stopstrings[] (metadata_str.c:33-147)
var stopstrings = []string{
	"1080",
	"1080P",
	"3D",
	"720",
	"720P",
	"AC3",
	"AE",
	"AHDTV",
	"ANALOG",
	"AUDIO",
	"BDRIP",
	"CAM",
	"CD",
	"CD1",
	"CD2",
	"CD3",
	"CHRONO",
	"COLORIZED",
	"COMPLETE",
	"CONVERT",
	"CUSTOM",
	"DC",
	"DDC",
	"DIRFIX",
	"DISC",
	"DISC1",
	"DISC2",
	"DISC3",
	"DIVX",
	"DOLBY",
	"DSR",
	"DTS",
	"DTV",
	"DUAL",
	"DUBBED",
	"DVBRIP",
	"DVDRIP",
	"DVDSCR",
	"DVDSCREENER",
	"EXTENDED",
	"FINAL",
	"FS",
	"HARDCODED",
	"HARDSUB",
	"HARDSUBBED",
	"HD",
	"HDDVDRIP",
	"HDRIP",
	"HDTV",
	"HR",
	"INT",
	"INTERNAL",
	"LASERDISC",
	"LIMITED",
	"LINE",
	"LIVE.AUDIO",
	"MP3",
	"MULTI",
	"NATIVE",
	"NFOFIX",
	"NTSC",
	"OAR",
	"P2P",
	"PAL",
	"PDTV",
	"PPV",
	"PREAIR",
	"PROOFFIX",
	"PROPER",
	"PT",
	"R1",
	"R2",
	"R3",
	"R4",
	"R5",
	"R6",
	"RATED",
	"RC",
	"READ.NFO",
	"READNFO",
	"REMASTERED",
	"REPACK",
	"RERIP",
	"RETAIL",
	"SAMPLEFIX",
	"SATRIP",
	"SCR",
	"SCREENER",
	"SE",
	"STV",
	"SUBBED",
	"SUBFORCED",
	"SUBS",
	"SVCD",
	"SYNCFIX",
	"TC",
	"TELECINE",
	"TELESYNC",
	"THEATRICAL",
	"TS",
	"TVRIP",
	"UNCUT",
	"UNRATED",
	"UNSUBBED",
	"VCDRIP",
	"VHSRIP",
	"WATERMARKED",
	"WORKPRINT",
	"WP",
	"WS",
	"X264",
	"XVID",
}

// urlDeescape — C: url_deescape (misc/str.c:60-105). In-place: decodes
// %XX, '+' → ' '. Returns the decoded prefix (malformed % truncates,
// matching C's `*d = 0; return`).
func urlDeescape(s []byte) []byte {
	d := 0
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '+' {
			s[d] = ' '
			d++
			i++
		} else if c == '%' {
			var v byte
			i++
			if i >= len(s) {
				return s[:d]
			}
			switch {
			case s[i] >= '0' && s[i] <= '9':
				v = (s[i] - '0') << 4
			case s[i] >= 'a' && s[i] <= 'f':
				v = (s[i] - 'a' + 10) << 4
			case s[i] >= 'A' && s[i] <= 'F':
				v = (s[i] - 'A' + 10) << 4
			default:
				return s[:d]
			}
			i++
			if i >= len(s) {
				return s[:d]
			}
			switch {
			case s[i] >= '0' && s[i] <= '9':
				v |= s[i] - '0'
			case s[i] >= 'a' && s[i] <= 'f':
				v |= s[i] - 'a' + 10
			case s[i] >= 'A' && s[i] <= 'F':
				v |= s[i] - 'A' + 10
			default:
				return s[:d]
			}
			i++
			s[d] = v
			d++
		} else {
			s[d] = c
			d++
			i++
		}
	}
	return s[:d]
}

// MetadataFilenameToTitle — C: metadata_filename_to_title
// (metadata_str.c:152-223)
func MetadataFilenameToTitle(filename string, yearp *int,
	titlep **misc.Rstr) {
	year := 0

	// C: char *s = mystrdupa(filename); url_deescape(s)
	s := urlDeescape([]byte(filename))

	// i is the logical end of the string; s[i] is the char that follows
	// the active prefix (NUL semantics in C). Truncations write s[i]=0,
	// which is observable by later iterations — keep the full slice.
	i := len(s)

	for i > 0 {
		if i > 5 && s[i-5] == '.' &&
			isnum(s[i-4]) && isnum(s[i-3]) && isnum(s[i-2]) && isnum(s[i-1]) {
			// C: year = misc.Atoi(s + i - 4) — consumes all leading digits
			year = atoiPrefix(string(s[i-4:]))
			i -= 5
			s[i] = 0
			continue
		}

		if i > 7 && s[i-7] == ' ' && s[i-6] == '(' &&
			isnum(s[i-5]) && isnum(s[i-4]) && isnum(s[i-3]) && isnum(s[i-2]) &&
			s[i-1] == ')' {
			// C: year = misc.Atoi(s + i - 5)
			year = atoiPrefix(string(s[i-5:]))
			i -= 7
			s[i] = 0
			continue
		}

		matched := -1
		for j, ss := range stopstrings {
			sl := len(ss)
			var tail byte
			if i < len(s) {
				tail = s[i]
			}
			if i > sl+1 && (s[i-sl-1] == '.' || s[i-sl-1] == ' ') &&
				strings.EqualFold(string(s[i-sl:i]), ss) &&
				(tail == '.' || tail == ' ' || tail == '-' || tail == 0) {
				i -= sl + 1
				s[i] = 0
				matched = j
				break
			}
		}

		if matched >= 0 {
			continue
		}

		i--
	}

	// C: char *lastword = strrchr(s, ' ') — searches s[:i]
	lastword := -1
	for j := i - 1; j >= 0; j-- {
		if s[j] == ' ' {
			lastword = j
			break
		}
	}
	if lastword >= 0 && lastword > 0 {
		// C: y = misc.Atoi(lastword + 1) — reads to end of (truncated) string
		y := atoiPrefix(string(s[lastword+1:]))
		if y > 1900 && y < 2040 {
			year = y
			s[lastword] = 0
			i = lastword
		}
	}

	for j := range i {
		if s[j] == '.' {
			s[j] = ' '
		}
	}

	if yearp != nil {
		*yearp = year
	}

	if titlep != nil {
		str := string(s[:i])
		*titlep = misc.RstrAlloc(&str)
	}
}

// MetadataFilenameToEpisode — C: metadata_filename_to_episode
// (metadata_str.c:229-300). Returns 0 on success, -1 if no episode
// pattern was found.
func MetadataFilenameToEpisode(s string, seasonp, episodep *int,
	titlep **misc.Rstr) int {
	l := len(s)
	season := -1
	episode := -1
	i := 0

	// Parse S##E## format
	for i = 0; i < l; i++ {
		if (s[i] == 's' || s[i] == 'S') && i+2 < l &&
			isnum(s[i+1]) && isnum(s[i+2]) {
			o := 3 + i
			if o < l && s[o] == '.' {
				o++
			}
			if o+2 < l && (s[o] == 'e' || s[o] == 'E') &&
				isnum(s[o+1]) && isnum(s[o+2]) {
				season = atoiPrefix(s[i+1:])
				episode = atoiPrefix(s[o+1:])
				break
			}
		}
	}

	if season == -1 && episode == -1 {
		// Parse ' (#)#x## - ' format
		for i = 3; i < l-2; i++ {
			if s[i] == 'x' && isnum(s[i+1]) && isnum(s[i+2]) &&
				i+3 < l && (s[i+3] == ' ' || s[i+3] == '.') {
				episode = atoiPrefix(s[i+1:])
				if isnum(s[i-1]) &&
					(s[i-2] == ' ' || s[i-2] == '.') {
					season = atoiPrefix(s[i-1:])
					i--
					break
				} else if isnum(s[i-1]) && isnum(s[i-2]) &&
					(s[i-3] == ' ' || s[i-3] == '.') {
					season = atoiPrefix(s[i-2:])
					i -= 2
					break
				}
			}
		}
	}

	if season == -1 || episode == -1 {
		return -1
	}

	*seasonp = season
	*episodep = episode

	// C: char *t = mystrdupa(s); url_deescape(t);
	//    for(j = 0; j < i; j++) if(t[j] == '.') t[j] = ' ';
	//    t[j] = 0;
	// The deescaped buffer can be shorter than i (i indexes the
	// original string); rstr_alloc stops at the NUL, so the effective
	// title is t[:min(i, len(t))].
	t := urlDeescape([]byte(s))
	end := min(i, len(t))
	for j := range end {
		if t[j] == '.' {
			t[j] = ' '
		}
	}

	if titlep != nil {
		if i > 0 {
			str := string(t[:end])
			*titlep = misc.RstrAlloc(&str)
		} else {
			*titlep = nil
		}
	}
	return 0
}

// folderToSeason — C: folder_to_season[] (metadata_str.c:306-310)
var folderToSeason = []string{
	`(.*)[ .]S([0-9][0-9])`,
	`(.*)[ .]Season[ .]([0-9]+)`,
}

var folderToSeasonRe = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(folderToSeason))
	for i, p := range folderToSeason {
		out[i] = regexp.MustCompile(p)
	}
	return out
}()

// MetadataFolderToSeason — C: metadata_folder_to_season
// (metadata_str.c:317-342). Returns 0 on match, -1 otherwise.
func MetadataFolderToSeason(s string, seasonp *int,
	titlep **misc.Rstr) int {
	for i, re := range folderToSeasonRe {
		m := re.FindStringSubmatchIndex(s)
		if m == nil {
			continue
		}
		if seasonp != nil {
			// C: *seasonp = misc.Atoi(s + matches[2].rm_so)
			*seasonp = atoiPrefix(s[m[4]:])
		}
		if titlep != nil {
			l := m[3] - m[2]
			if l > 0 {
				// C: rstr_allocl(s + matches[i].rm_so, l) — upstream
				// quirk: indexes `matches` with the loop counter i
				// (the pattern index), not group 1.
				sub := s[m[2*i]:]
				*titlep = misc.RstrAllocl(&sub, l)
			} else {
				*titlep = nil
			}
		}
		return 0
	}
	return -1
}

// IsReasonableMovieName — C: is_reasonable_movie_name
// (metadata_str.c:349-357). Counts bytes >= 0x30 (signed char in C —
// UTF-8 continuation bytes are negative and don't count).
func IsReasonableMovieName(s string) int {
	n := 0
	for i := range len(s) {
		if int8(s[i]) >= 0x30 {
			n++
		}
	}
	if n >= 3 {
		return 1
	}
	return 0
}

// MetadataRemovePostfixRstr — C: metadata_remove_postfix_rstr
// (metadata_str.c:366-378)
func MetadataRemovePostfixRstr(name *misc.Rstr, g *gconf.T) *misc.Rstr {
	if !fileaccesscore.ShowFilenameExtensions(g) {
		str := misc.RstrGet(name)
		l := len(str)
		if l > 4 && str[l-4] == '.' {
			sub := str[:l-4]
			return misc.RstrAllocl(&sub, l-4)
		}
		if l > 5 && strings.EqualFold(str[l-5:], ".m2ts") {
			sub := str[:l-5]
			return misc.RstrAllocl(&sub, l-5)
		}
	}
	return misc.RstrDup(name)
}

// MetadataRemovePostfix — C: metadata_remove_postfix
// (metadata_str.c:385-392)
func MetadataRemovePostfix(str string) *misc.Rstr {
	l := len(str)
	if l > 4 && str[l-4] == '.' {
		sub := str[:l-4]
		return misc.RstrAllocl(&sub, l-4)
	}
	return misc.RstrAllocl(&str, l)
}
