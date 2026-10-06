// Canonical port of src/fileaccess/ftpparse.c — D. J. Bernstein's FTP LIST
// line parser (EPLF, UNIX ls, Microsoft FTP Service, Windows NT, VMS,
// WFTPD, NetPresenz, NetWare, MSDOS).
package fileaccess

import (
	"time"
)

// C: FTPPARSE_* constants (ftpparse.h)
const (
	FTPParseSizeUnknown = 0
	FTPParseSizeBinary  = 1 // size in octets, TYPE I
	FTPParseSizeASCII   = 2 // size in octets, TYPE A

	FTPParseMTimeUnknown      = 0
	FTPParseMTimeLocal        = 1 // time is correct
	FTPParseMTimeRemoteMinute = 2 // time zone and secs unknown
	FTPParseMTimeRemoteDay    = 3 // time zone and time of day unknown

	FTPParseIDUnknown = 0
	FTPParseIDFull    = 1
)

// FTPParse — C: struct ftpparse. Name/ID are substrings of the parsed
// line (C stores pointer+length into buf).
type FTPParse struct {
	Name        string // C: name (not NUL-terminated) + namelen
	FlagTryCWD  bool   // C: flagtrycwd — 0 if cwd is definitely pointless
	FlagTryRETR bool   // C: flagtryretr
	Sizetype    int    // C: sizetype
	Size        int64  // C: size — octets
	Mtimetype   int    // C: mtimetype
	Mtime       int64  // C: mtime — time_t
	Idtype      int    // C: idtype
	ID          string // C: id + idlen
}

// totai — C: totai (ftpparse.c:39-54). Days-based date math.
func totai(year, month, mday int64) int64 {
	if month >= 2 {
		month -= 2
	} else {
		month += 10
		year--
	}
	result := (mday-1)*10 + 5 + 306*month
	result /= 10
	if result == 365 {
		year -= 3
		result = 1460
	} else {
		result += 365 * (year % 4)
	}
	year /= 4
	result += 1461 * (year % 25)
	year /= 25
	if result == 36524 {
		year -= 3
		result = 146096
	} else {
		result += 36524 * (year % 4)
	}
	year /= 4
	result += 146097 * (year - 5)
	result += 11017
	return result * 86400
}

// setupBase — C: setupBase (ftpparse.c:60-67)
// totai(1970-01-01) == 0 so base converges to 0, but setupBase is
// idempotent by construction (gmtime(&base) of 0 is the epoch).
func (fam *FileAccessManager) setupBase() {
	t := time.Unix(fam.ftpTimebase.base, 0).UTC()
	fam.ftpTimebase.base = -(totai(int64(t.Year()), int64(t.Month())-1, int64(t.Day())) +
		int64(t.Hour())*3600 + int64(t.Minute())*60 + int64(t.Second()))
}

// FTPParseStart — C: ftpparse_init (ftpparse.c:69-94). Computes `now` and
// the current-year approximation used by guesstai.
func (fam *FileAccessManager) FTPParseStart() {
	fam.setupBase()
	fam.ftpTimebase.now = time.Now().Unix() - fam.ftpTimebase.base

	day := fam.ftpTimebase.now / 86400
	if fam.ftpTimebase.now%86400 < 0 {
		day--
	}
	day -= 11017
	year := 5 + day/146097
	day = day % 146097
	if day < 0 {
		day += 146097
		year--
	}
	year *= 4
	if day == 146096 {
		year += 3
		day = 36524
	} else {
		year += day / 36524
		day %= 36524
	}
	year *= 25
	year += day / 1461
	day %= 1461
	year *= 4
	if day == 1460 {
		year += 3
		day = 365
	} else {
		year += day / 365
		day %= 365
	}
	day *= 10
	if (day+5)/306 >= 10 {
		year++
	}
	fam.ftpTimebase.currentYear = year
}

// guesstai — C: guesstai (ftpparse.c:101-112). UNIX ls omits the year for
// recent dates; guess the nearest year within the last ~350 days.
func (fam *FileAccessManager) guesstai(month, mday int64) int64 {
	for year := fam.ftpTimebase.currentYear - 1; year < fam.ftpTimebase.currentYear+100; year++ {
		t := totai(year, month, mday)
		if fam.ftpTimebase.now-t < 350*86400 {
			return t
		}
	}
	return 0
}

// check — C: check (ftpparse.c:114-120) — case-insensitive 3-char match.
func check(buf []byte, monthname string) bool {
	if buf[0] != monthname[0] && buf[0] != monthname[0]-32 {
		return false
	}
	if buf[1] != monthname[1] && buf[1] != monthname[1]-32 {
		return false
	}
	if buf[2] != monthname[2] && buf[2] != monthname[2]-32 {
		return false
	}
	return true
}

var ftpMonths = [12]string{
	"jan", "feb", "mar", "apr", "may", "jun",
	"jul", "aug", "sep", "oct", "nov", "dec",
}

// getmonth — C: getmonth (ftpparse.c:127-134)
func getmonth(buf []byte) int {
	if len(buf) == 3 {
		for i := range 12 {
			if check(buf, ftpMonths[i]) {
				return i
			}
		}
	}
	return -1
}

// getlong — C: getlong (ftpparse.c:136-142) — unchecked digit parse.
func getlong(buf []byte) int64 {
	var u int64
	for _, c := range buf {
		u = u*10 + int64(c-'0')
	}
	return u
}

// FTPParseLine — C: ftpparse (ftpparse.c:155-457). Parses one LIST line
// (without CR LF) into fp; returns false if no filename was found.
// FTPParseStart must have run (or call it lazily — canonical code calls
// setupBase() inline and ftpparse_init() at ftp_init).
func (fam *FileAccessManager) FTPParseLine(fp *FTPParse, buf []byte) bool {
	var month, mday, year, hour, minute int64
	var size int64

	*fp = FTPParse{}

	if len(buf) < 2 {
		return false
	}

	switch buf[0] {
	// EPLF — http://pobox.com/~djb/proto/eplf.txt
	case '+':
		i := 1
		for j := 1; j < len(buf); j++ {
			if buf[j] == 9 {
				fp.Name = string(buf[j+1:])
				return true
			}
			if buf[j] == ',' {
				switch buf[i] {
				case '/':
					fp.FlagTryCWD = true
				case 'r':
					fp.FlagTryRETR = true
				case 's':
					fp.Sizetype = FTPParseSizeBinary
					fp.Size = getlong(buf[i+1 : j])
				case 'm':
					fp.Mtimetype = FTPParseMTimeLocal
					fam.setupBase()
					fp.Mtime = fam.ftpTimebase.base + getlong(buf[i+1:j])
				case 'i':
					fp.Idtype = FTPParseIDFull
					fp.ID = string(buf[i+1 : j])
				}
				i = j + 1
			}
		}
		return false

	// UNIX-style listing (also Microsoft FTP, WFTPD, NetWare, NetPresenz)
	case 'b', 'c', 'd', 'l', 'p', 's', '-':
		if buf[0] == 'd' {
			fp.FlagTryCWD = true
		}
		if buf[0] == '-' {
			fp.FlagTryRETR = true
		}
		if buf[0] == 'l' {
			fp.FlagTryCWD = true
			fp.FlagTryRETR = true
		}

		state := 1
		i := 0
		for j := 1; j < len(buf); j++ {
			if buf[j] == ' ' && buf[j-1] != ' ' {
				switch state {
				case 1: // skipping perm
					state = 2
				case 2: // skipping nlink
					state = 3
					if j-i == 6 && buf[i] == 'f' { // NetPresenz
						state = 4
					}
				case 3: // skipping uid
					state = 4
				case 4: // tentative size
					size = getlong(buf[i:j])
					state = 5
				case 5: // month, else tentative size
					m := getmonth(buf[i:j])
					if m >= 0 {
						month = int64(m)
						state = 6
					} else {
						size = getlong(buf[i:j])
					}
				case 6: // have size and month
					mday = getlong(buf[i:j])
					state = 7
				case 7: // have size, month, mday
					if j-i == 4 && buf[i+1] == ':' {
						hour = getlong(buf[i : i+1])
						minute = getlong(buf[i+2 : i+4])
						fp.Mtimetype = FTPParseMTimeRemoteMinute
						fam.setupBase()
						fp.Mtime = fam.ftpTimebase.base + fam.guesstai(month, mday) + hour*3600 + minute*60
					} else if j-i == 5 && buf[i+2] == ':' {
						hour = getlong(buf[i : i+2])
						minute = getlong(buf[i+3 : i+5])
						fp.Mtimetype = FTPParseMTimeRemoteMinute
						fam.setupBase()
						fp.Mtime = fam.ftpTimebase.base + fam.guesstai(month, mday) + hour*3600 + minute*60
					} else if j-i >= 4 {
						year = getlong(buf[i:j])
						fp.Mtimetype = FTPParseMTimeRemoteDay
						fam.setupBase()
						fp.Mtime = fam.ftpTimebase.base + totai(year, month, mday)
					} else {
						return false
					}
					fp.Name = string(buf[j+1:])
					state = 8
				case 8:
				}
				i = j + 1
				for i < len(buf) && buf[i] == ' ' {
					i++
				}
			}
		}
		if state != 8 {
			return false
		}

		fp.Size = size
		fp.Sizetype = FTPParseSizeBinary

		// symlink: truncate at " -> "
		if buf[0] == 'l' {
			for i := 0; i+3 < len(fp.Name); i++ {
				if fp.Name[i] == ' ' && fp.Name[i+1] == '-' &&
					fp.Name[i+2] == '>' && fp.Name[i+3] == ' ' {
					fp.Name = fp.Name[:i]
					break
				}
			}
		}

		// eliminate extra NetWare spaces
		if buf[1] == ' ' || buf[1] == '[' {
			if len(fp.Name) > 3 && fp.Name[0] == ' ' &&
				fp.Name[1] == ' ' && fp.Name[2] == ' ' {
				fp.Name = fp.Name[3:]
			}
		}
		return true
	}

	// MultiNet / VMS:
	// "00README.TXT;1      2 30-DEC-1996 17:44 [SYSTEM] (RWED,RWED,RE,RE)"
	i := 0
	for i = 0; i < len(buf); i++ {
		if buf[i] == ';' {
			break
		}
	}
	if i < len(buf) {
		fp.Name = string(buf[:i])
		if i > 4 && buf[i-4] == '.' && buf[i-3] == 'D' &&
			buf[i-2] == 'I' && buf[i-1] == 'R' {
			fp.Name = fp.Name[:len(fp.Name)-4]
			fp.FlagTryCWD = true
		}
		if !fp.FlagTryCWD {
			fp.FlagTryRETR = true
		}
		for buf[i] != ' ' {
			if i++; i == len(buf) {
				return false
			}
		}
		for buf[i] == ' ' {
			if i++; i == len(buf) {
				return false
			}
		}
		for buf[i] != ' ' {
			if i++; i == len(buf) {
				return false
			}
		}
		for buf[i] == ' ' {
			if i++; i == len(buf) {
				return false
			}
		}
		j := i
		for buf[j] != '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		mday = getlong(buf[i:j])
		for buf[j] == '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		m := getmonth(buf[i:j])
		if m < 0 {
			return false
		}
		month = int64(m)
		for buf[j] == '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != ' ' {
			if j++; j == len(buf) {
				return false
			}
		}
		year = getlong(buf[i:j])
		for buf[j] == ' ' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != ':' {
			if j++; j == len(buf) {
				return false
			}
		}
		hour = getlong(buf[i:j])
		for buf[j] == ':' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != ':' && buf[j] != ' ' {
			if j++; j == len(buf) {
				return false
			}
		}
		minute = getlong(buf[i:j])

		fp.Mtimetype = FTPParseMTimeRemoteMinute
		fam.setupBase()
		fp.Mtime = fam.ftpTimebase.base + totai(year, month, mday) + hour*3600 + minute*60
		return true
	}

	// MSDOS format:
	// "04-27-00  09:09PM       <DIR>          licensed"
	// "04-14-00  03:47PM                  589 readme.htm"
	if buf[0] >= '0' && buf[0] <= '9' {
		i = 0
		j := 0
		for buf[j] != '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		month = getlong(buf[i:j]) - 1
		for buf[j] == '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		mday = getlong(buf[i:j])
		for buf[j] == '-' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != ' ' {
			if j++; j == len(buf) {
				return false
			}
		}
		year = getlong(buf[i:j])
		if year < 50 {
			year += 2000
		}
		if year < 1000 {
			year += 1900
		}
		for buf[j] == ' ' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != ':' {
			if j++; j == len(buf) {
				return false
			}
		}
		hour = getlong(buf[i:j])
		for buf[j] == ':' {
			if j++; j == len(buf) {
				return false
			}
		}
		i = j
		for buf[j] != 'A' && buf[j] != 'P' {
			if j++; j == len(buf) {
				return false
			}
		}
		minute = getlong(buf[i:j])
		if hour == 12 {
			hour = 0
		}
		if buf[j] == 'A' {
			if j++; j == len(buf) {
				return false
			}
		}
		if buf[j] == 'P' {
			hour += 12
			if j++; j == len(buf) {
				return false
			}
		}
		if buf[j] == 'M' {
			if j++; j == len(buf) {
				return false
			}
		}
		for buf[j] == ' ' {
			if j++; j == len(buf) {
				return false
			}
		}
		if buf[j] == '<' {
			fp.FlagTryCWD = true
			for buf[j] != ' ' {
				if j++; j == len(buf) {
					return false
				}
			}
		} else {
			i = j
			for buf[j] != ' ' {
				if j++; j == len(buf) {
					return false
				}
			}
			fp.Size = getlong(buf[i:j])
			fp.Sizetype = FTPParseSizeBinary
			fp.FlagTryRETR = true
		}
		for buf[j] == ' ' {
			if j++; j == len(buf) {
				return false
			}
		}
		fp.Name = string(buf[j:])

		fp.Mtimetype = FTPParseMTimeRemoteMinute
		fam.setupBase()
		fp.Mtime = fam.ftpTimebase.base + totai(year, month, mday) + hour*3600 + minute*60
		return true
	}

	// Useless lines (VMS totals, "total NNNNN", VMS dirs) — ignored.
	return false
}
