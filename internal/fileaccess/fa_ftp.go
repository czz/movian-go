// Canonical port of src/fileaccess/fa_ftp.c — pooled FTP control
// connections (10 s expiry, NOOP-verified reuse), lazy per-file
// ftp_file_t, PASV data transfers, LIST+ftpparse scandir, MLST stat
// with parent-dir LIST fallback, keyring auth.
package fileaccess

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

// C: keyring_lookup return codes (keyring.h)
const (
	keyringUserRejected = -1
	keyringOK           = 0
	keyringNotFound     = 1
)

// C: keyring_lookup flags (keyring.h)
const (
	keyringQueryUser      = 0x1
	keyringShowRememberMe = 0x2
	keyringRememberMeSet  = 0x4
)

// ftpConnection — C: ftp_connection_t (fa_ftp.c:45)
type ftpConnection struct {
	fam      *FileAccessManager // C: implicit global fa context
	id       int
	conn     net.Conn
	reader   *bufio.Reader
	hostname string
	port     int
	url      string // C: fc_url — "ftp://host[:port]"

	expire int64 // C: fc_expire — arch_get_ts() µs

	noMLST bool // C: fc_no_mlst
}

// ftpFile — C: ftp_file_t (fa_ftp.c:65)
type ftpFile struct {
	fam   *FileAccessManager // C: implicit global fa context
	fc    *ftpConnection
	pathx string // C: ff_pathx — path without leading '/'
	port  int

	fpos int64 // C: ff_fpos
	size int64 // C: ff_size (-1 unknown)

	xfer  net.Conn      // C: ff_xfer — data channel
	xferR *bufio.Reader // line reader over xfer

	usage usageEventer // C: usage_event (usage.c) — injected dep

	hostname string // C: ff_hostname[128]
	pathbuf  string // C: ff_pathbuf[1024]
}

// ftpTrace — C: FTP_TRACE (fa_ftp.c:81-85, gated on
// gconf.enable_ftp_client_debug)
func (fam *FileAccessManager) ftpTrace(format string, args ...any) {
	if fam.gconf != nil && fam.gconf.EnableFTPClientDebug.Load() {
		fam.tracer.Trace(trace.TRACE_DEBUG, "FTP", format, args...)
	}
}

// archGetTs — C: arch_get_ts() — µs monotonic-ish timestamp.
func archGetTs() int64 { return time.Now().UnixMicro() }

// tcpReadLine — C: tcp_read_line; returns the line sans trailing \r\n.
func tcpReadLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ftpReadLine — C: ftp_read_line (fa_ftp.c:90)
func ftpReadLine(fc *ftpConnection) (string, error) {
	line, err := tcpReadLine(fc.reader)
	if err != nil {
		fc.fam.ftpTrace("[%d]: Read error", fc.id)
		return "", err
	}
	fc.fam.ftpTrace("[%d]: Recv: %s", fc.id, line)
	return line, nil
}

// fcReadResultFull — C: fc_read_result (fa_ftp.c:105). Reads multi-line
// replies (NNN-...NNN ), returns the numeric code and the text after
// the code's space; 412 → -1.
func fcReadResultFull(fc *ftpConnection) (int, string) {
	var rc int
	var resp string
	for {
		var err error
		resp, err = ftpReadLine(fc)
		if err != nil {
			return -1, ""
		}
		i := 0
		for i < len(resp) && resp[i] == ' ' {
			i++
		}
		j := i
		for j < len(resp) && resp[j] >= '0' && resp[j] <= '9' {
			j++
		}
		rc, _ = strconv.Atoi(resp[i:j])
		if j >= len(resp) || resp[j] != '-' {
			break
		}
	}
	msg := ""
	if _, after, ok := strings.Cut(resp, " "); ok {
		msg = after
	}
	if rc == 412 {
		fmt.Printf("FTP: Disconnected: %s\n", msg)
		return -1, msg
	}
	return rc, msg
}

// fcWrite — C: fc_write (fa_ftp.c:142)
func fcWrite(fc *ftpConnection, format string, args ...any) {
	buf := fmt.Sprintf(format, args...)
	fc.fam.ftpTrace("[%d]: Send: %s", fc.id, buf)
	fc.conn.Write([]byte(buf))
}

// fcDisconnect — C: fc_disconnect (fa_ftp.c:160)
func fcDisconnect(fc *ftpConnection) {
	if fc.conn != nil {
		fc.conn.Close()
	}
}

// fcConnect — C: fc_connect (fa_ftp.c:172). Reuses a pooled connection
// (NOOP-verified past expiry), else connects + welcome(220) + keyring
// USER/PASS loop + TYPE I.
func (fam *FileAccessManager) fcConnect(hostname string, port int, nonInteractive bool, u usageEventer) (*ftpConnection, error) {
	now := archGetTs()

	for {
		fam.ftp.mu.Lock()
		var fc *ftpConnection
		for _, c := range fam.ftp.conns {
			if c.hostname == hostname && c.port == port {
				fc = c
				break
			}
		}
		if fc != nil {
			for i, c := range fam.ftp.conns {
				if c == fc {
					fam.ftp.conns = slices.Delete(fam.ftp.conns, i, i+1)
					break
				}
			}
			fam.ftp.mu.Unlock()

			// Verify connection still works
			if now > fc.expire {
				fcWrite(fc, "NOOP\n")
				r, _ := fcReadResultFull(fc)
				if r == 200 {
					return fc, nil
				}
				fcDisconnect(fc)
				continue // C: goto again
			}
			return fc, nil
		}
		fam.ftp.mu.Unlock()
		break
	}

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(hostname,
		strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		return nil, err
	}

	fc := &ftpConnection{
		fam:      fam,
		conn:     conn,
		reader:   bufio.NewReader(conn),
		hostname: hostname,
		port:     port,
	}
	fc.id = int(atomic.AddInt32(&fam.ftp.idTally, 1))

	// Check welcome message
	r, _ := fcReadResultFull(fc)
	if r != 220 {
		fcDisconnect(fc)
		return nil, fmt.Errorf("Invalid welcome, expected 220 got %d", r)
	}

	attempt := 0
	reason := "Login"
	buf1 := fmt.Sprintf("ftp://%s:%d", hostname, port)

	for {
		if attempt > 0 && nonInteractive {
			fcDisconnect(fc)
			return nil, fmt.Errorf("FTP auth required")
		}
		var username, password string
		krflags := keyringShowRememberMe | keyringRememberMeSet
		if attempt > 0 {
			krflags |= keyringQueryUser
		}
		// No keyring wired → cannot obtain credentials (C always has
		// keyring_lookup; a nil hook can't answer → fail the open).
		if fam.keyringLookup == nil {
			fcDisconnect(fc)
			return nil, errors.New("No keyring available")
		}
		kr := fam.keyringLookup(buf1, &username, &password, nil, nil,
			"FTP Client", reason, krflags)
		attempt++
		switch kr {
		case keyringNotFound:
			continue
		case keyringUserRejected:
			fcDisconnect(fc)
			return nil, errors.New("Rejected by user")
		}
		// KEYRING_OK
		fcWrite(fc, "USER %s\n", username)
		fcReadResultFull(fc)
		fcWrite(fc, "PASS %s\n", password)
		r, msg := fcReadResultFull(fc)
		reason = msg
		if r == 230 {
			goto authed
		}
	}

authed:
	fcWrite(fc, "TYPE I\n")
	r, reason = fcReadResultFull(fc)
	if r != 200 {
		fcDisconnect(fc)
		return nil, fmt.Errorf("Unable to set binary mode -- %s", reason)
	}

	if port != 21 {
		fc.url = fmt.Sprintf("ftp://%s:%d", hostname, port)
	} else {
		fc.url = fmt.Sprintf("ftp://%s", hostname)
	}

	// C: usage_event("FTP Client", 1, NULL) (fa_ftp.c:281)
	if u != nil {
		u.Event("FTP Client", 1)
	}
	return fc, nil
}

// ftpFileRelease — C: ftp_file_release (fa_ftp.c:295). Parks the
// control connection (10 s expiry) or drops it.
func ftpFileRelease(ff *ftpFile, drop bool) {
	fam := ff.fam
	fc := ff.fc
	if fc != nil {
		if drop {
			fcDisconnect(fc)
		} else {
			fc.expire = archGetTs() + 10000000
			fam.ftp.mu.Lock()
			fam.ftp.conns = slices.Insert(fam.ftp.conns, 0, fc)
			fam.ftp.mu.Unlock()
		}
	}
}

// ftpOpenDataTransfer — C: ftp_open_data_transfer (fa_ftp.c:323).
// PASV → "227 Entering Passive Mode (a,b,c,d,p1,p2)" → tcp connect.
func ftpOpenDataTransfer(fc *ftpConnection) net.Conn {
	fcWrite(fc, "PASV\n")
	r, msg := fcReadResultFull(fc)
	if r != 227 {
		return nil
	}

	// C: sscanf(buf, "Entering Passive Mode (%d,%d,%d,%d,%d,%d)")
	open := strings.IndexByte(msg, '(')
	if open < 0 {
		// msg may already be past "Entering Passive Mode "
		open = strings.IndexByte(msg, '(')
		if open < 0 {
			return nil
		}
	}
	inside := msg[open+1:]
	if c := strings.IndexByte(inside, ')'); c >= 0 {
		inside = inside[:c]
	}
	var d [6]int
	parts := strings.Split(inside, ",")
	if len(parts) != 6 {
		return nil
	}
	for i, s := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil
		}
		d[i] = v
	}

	host := fmt.Sprintf("%d.%d.%d.%d", d[0], d[1], d[2], d[3])
	dport := d[4]*256 + d[5]
	tc, err := net.DialTimeout("tcp", net.JoinHostPort(host,
		strconv.Itoa(dport)), 5*time.Second)
	if err != nil {
		fmt.Printf("FTP: Data channel connection failed to %s:%d -- %s\n",
			host, dport, err)
		return nil
	}
	fc.fam.ftpTrace("[%d]: Data channel connected to %s:%d", fc.id, host, dport)
	return tc
}

// ffReconnect — C: ff_reconnect (fa_ftp.c:357)
func ffReconnect(ff *ftpFile, nonInteractive bool) error {
	if ff.fc != nil {
		return nil
	}
	fc, err := ff.fam.fcConnect(ff.hostname, ff.port, nonInteractive, ff.usage)
	if err != nil {
		return err
	}
	ff.fc = fc
	return nil
}

// newFTPFile — C: ftp_file_init (fa_ftp.c:373). Splits the URL and
// establishes the control connection.
func (fam *FileAccessManager) newFTPFile(url string, nonInteractive bool, u usageEventer) (*ftpFile, error) {
	if fam == nil { // unwired protocol (C: globals existed unconditionally)
		return nil, errors.New("no fa context")
	}
	ff := &ftpFile{size: -1, fam: fam}

	var hostname [128]byte
	var pathbuf [1024]byte
	var proto [16]byte
	misc.UrlSplit(proto[:], 16, nil, 0, hostname[:], 128,
		&ff.port, pathbuf[:], 1024, url)
	ff.hostname = misc.CStr(hostname[:])
	ff.pathbuf = misc.CStr(pathbuf[:])

	if ff.port < 0 {
		ff.port = 21
	}
	ff.pathx = ff.pathbuf
	if strings.HasPrefix(ff.pathx, "/") {
		ff.pathx = ff.pathx[1:]
	}

	ff.usage = u
	if err := ffReconnect(ff, nonInteractive); err != nil {
		return nil, err
	}
	return ff, nil
}

// ftpFileSize — C: ftp_file_size (fa_ftp.c:409). SIZE → 213.
func ftpFileSize(ff *ftpFile) int64 {
	if ff.size != -1 {
		return ff.size
	}
	fcWrite(ff.fc, "SIZE %s\n", ff.pathx)
	r, msg := fcReadResultFull(ff.fc)
	if r == 213 {
		ff.size = strtoll(msg)
	}
	return ff.size
}

// strtoll — C's strtoll(s, NULL, 10): leading whitespace, optional
// sign, then digits; stops at the first non-digit.
func strtoll(s string) int64 {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	var v int64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		v = v*10 + int64(s[i]-'0')
		i++
	}
	if neg {
		v = -v
	}
	return v
}

// ftpAddDirEntry — C: ftp_add_dir_entry (fa_ftp.c:443)
func ftpAddDirEntry(fc *ftpConnection, path string, fd *Dir, n string) int {
	var fp FTPParse
	if !fc.fam.FTPParseLine(&fp, []byte(n)) {
		return 1
	}
	if fp.Name == "" {
		return 0
	}

	fileType := ContentFile
	if fp.FlagTryCWD {
		fileType = ContentDir
	}

	fname := fp.Name
	sep := ""
	if path != "" {
		sep = "/"
	}
	url := fmt.Sprintf("%s/%s%s%s", fc.url, path, sep, fname)

	fde := DirAdd(fd, url, fname, fileType)
	if fde != nil {
		fde.StatDone = true
		fde.Stat.MTime = time.Unix(fp.Mtime, 0)
		fde.Stat.Size = fp.Size
		fde.Stat.Type = fileType
	}
	return 0
}

// ftpListDirLIST — C: ftp_list_dir_LIST (fa_ftp.c:482)
func ftpListDirLIST(fc *ftpConnection, path string, fd *Dir) int {
	tc := ftpOpenDataTransfer(fc)
	if tc == nil {
		return -1
	}
	tr := bufio.NewReader(tc)

	listPath := path
	if listPath == "" {
		listPath = "."
	}
	fcWrite(fc, "LIST %s\n", listPath)

	for {
		resp, err := tcpReadLine(tr)
		if err != nil {
			break
		}
		fc.fam.ftpTrace("[%dd]: Recv: %s", fc.id, resp)
		ftpAddDirEntry(fc, path, fd, resp)
	}
	fc.fam.ftpTrace("[%dd]: Closed", fc.id)
	tc.Close()

	for {
		r, _ := fcReadResultFull(fc)
		if r == -1 {
			return -1
		}
		if r < 200 {
			continue
		}
		if r >= 400 {
			return -1
		}
		return 0
	}
}

// ftpListDir — C: ftp_list_dir (fa_ftp.c:550). The STAT variant is
// dead code (if(0)) in C.
func ftpListDir(fc *ftpConnection, path string, fd *Dir) int {
	return ftpListDirLIST(fc, path, fd)
}

// ftpScandir — C: ftp_scandir (fa_ftp.c:561)
func (fam *FileAccessManager) ftpScandir(url string, fd *Dir, u usageEventer) error {
	ff, err := fam.newFTPFile(url, false, u)
	if err != nil {
		return err
	}
	r := ftpListDir(ff.fc, ff.pathx, fd)
	ftpFileRelease(ff, false)
	if r != 0 {
		return errors.New("ftp scandir failed")
	}
	return nil
}

// ftpOpen — C: ftp_open (fa_ftp.c:578)
func (fam *FileAccessManager) ftpOpen(url string, u usageEventer) (*ftpFile, error) {
	ff, err := fam.newFTPFile(url, false, u)
	if err != nil {
		return nil, err
	}
	ftpFileSize(ff)
	return ff, nil
}

// Close — C: ftp_close (fa_ftp.c:597)
func (ff *ftpFile) Close() error {
	drop := false
	ff.fc.fam.ftpTrace("[%dd]: Close, xfer:%v", ff.fc.id, ff.xfer != nil)
	if ff.xfer != nil {
		ff.xfer.Close()
		ff.xfer = nil
		ff.xferR = nil
		fcReadResultFull(ff.fc)
		drop = true
	}
	ftpFileRelease(ff, drop)
	return nil
}

// Read — C: ftp_read (fa_ftp.c:618). Clamps to ff_size; lazily opens
// the data channel with REST+RETR on first read.
func (ff *ftpFile) Read(buf []byte) (int, error) {
	size := int64(len(buf))

	if err := ffReconnect(ff, false); err != nil {
		return -1, err
	}

	if ff.fpos+size > ff.size {
		size = ff.size - ff.fpos
	}
	if size <= 0 {
		return 0, io.EOF
	}

	if ff.xfer == nil {
		ff.xfer = ftpOpenDataTransfer(ff.fc)
		if ff.xfer == nil {
			return -1, errors.New("ftp: data channel failed")
		}
		ff.xferR = bufio.NewReader(ff.xfer)

		fcWrite(ff.fc, "REST %d\n", ff.fpos)
		if r, _ := fcReadResultFull(ff.fc); r != 350 {
			return -1, fmt.Errorf("ftp: REST failed (%d)", r)
		}
		fcWrite(ff.fc, "RETR %s\n", ff.pathx)
		if r, _ := fcReadResultFull(ff.fc); r != 150 {
			ff.xfer.Close()
			ff.xfer = nil
			ff.xferR = nil
			return -1, fmt.Errorf("ftp: RETR failed (%d)", r)
		}
	}

	rval := 0
	for size > 0 {
		n, err := ff.xfer.Read(buf[rval : int64(rval)+size])
		ff.fc.fam.ftpTrace("[%dd]: Recv data: %d", ff.fc.id, n)
		if n <= 0 {
			if err != nil && !errors.Is(err, io.EOF) {
				ff.xfer.Close()
				ff.xfer = nil
				ff.xferR = nil
				fcReadResultFull(ff.fc)
				return -1, err
			}
			break
		}
		size -= int64(n)
		rval += n
	}
	ff.fpos += int64(rval)
	return rval, nil
}

// Seek — C: ftp_seek (fa_ftp.c:678). Closes the data channel on a
// real position change; -1/>=500 handling canonical.
func (ff *ftpFile) Seek(pos int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = pos
	case io.SeekCurrent:
		np = ff.fpos + pos
	case io.SeekEnd:
		if ff.size == -1 {
			return -1, errors.New("ftp: SEEK_END with unknown size")
		}
		np = ff.size + pos
	default:
		return -1, errors.New("ftp: invalid whence")
	}
	if np < 0 {
		return -1, errors.New("ftp: negative position")
	}

	ff.fam.ftpTrace("After seek pos = %d np = %d", ff.fpos, np)
	if ff.fpos == np {
		return ff.fpos, nil
	}

	if ff.xfer != nil {
		ff.fc.fam.ftpTrace("[%dd]: Closing due to seek", ff.fc.id)
		ff.xfer.Close()
		ff.xfer = nil
		ff.xferR = nil
		r, _ := fcReadResultFull(ff.fc)
		ff.fc.fam.ftpTrace("[%d]: Read terminated with %d", ff.fc.id, r)
		if r == -1 {
			fcDisconnect(ff.fc)
			ff.fc = nil
		}
		if r >= 500 {
			return -1, errors.New("ftp: seek rejected")
		}
	}
	ff.fpos = np
	return ff.fpos, nil
}

// Size — C: ftp_fsize (fa_ftp.c:737)
func (ff *ftpFile) Size() int64 {
	if ffReconnect(ff, false) != nil {
		return -1
	}
	return ftpFileSize(ff)
}

// mkint — C: mkint (fa_ftp.c:749)
func mkint(s string, off, ln int) int {
	r := 0
	for i := range ln {
		r = r*10 + int(s[off+i]-'0')
	}
	return r
}

// mktimeUTC — C: mktime_utc. mon is 0-based in C callers.
func mktimeUTC(year, mon0, day, hour, min, sec int) (time.Time, bool) {
	return time.Date(year, time.Month(mon0+1), day, hour, min, sec, 0,
		time.UTC), true
}

// ftpStat — C: ftp_stat (fa_ftp.c:765). MLST first; falls back to
// listing the parent directory when MLST is unsupported.
func (fam *FileAccessManager) ftpStat(url string, flags int, u usageEventer) (*FileStat, error) {
	nonInteractive := flags&FaNonInteractive != 0
	ff, err := fam.newFTPFile(url, nonInteractive, u)
	if err != nil {
		return nil, err
	}

	st := &FileStat{}
	fc := ff.fc
	errFail := func() (*FileStat, error) {
		ftpFileRelease(ff, false)
		return nil, errors.New("ftp stat failed")
	}

	if !fc.noMLST {
		fcWrite(fc, "MLST %s\n", ff.pathx)
		resp, err := ftpReadLine(fc)
		if err != nil {
			return errFail()
		}
		if strings.HasPrefix(resp, "550") {
			return errFail()
		}

		if strings.HasPrefix(resp, "250-") {
			ok := false
			resp, err = ftpReadLine(fc)
			if err != nil {
				return errFail()
			}
			t := strings.Index(resp, "type=")
			m := strings.Index(resp, "modify=")
			s := strings.Index(resp, "size=")

			if t >= 0 && m >= 0 {
				t += len("type=")
				m += len("modify=")
				if s >= 0 {
					s += len("size=")
				}

				if strings.HasPrefix(resp[t:], "cdir") ||
					strings.HasPrefix(resp[t:], "dir") {
					st.Type = ContentDir
				} else if strings.HasPrefix(resp[t:], "file") && s >= 0 {
					st.Type = ContentFile
					st.Size = strtoll(resp[s:])
				} else {
					return errFail()
				}

				if len(resp)-m >= 14 {
					st.MTime, _ = mktimeUTC(
						mkint(resp, m, 4),
						mkint(resp, m+4, 2)-1,
						mkint(resp, m+6, 2),
						mkint(resp, m+8, 2),
						mkint(resp, m+10, 2),
						mkint(resp, m+12, 2))
					ok = true
				}
			}

			if _, err := ftpReadLine(fc); err != nil {
				return errFail()
			}
			ftpFileRelease(ff, false)
			if !ok {
				return nil, errors.New("ftp stat failed")
			}
			return st, nil
		}
		// Server does not understand MLST
		fc.fam.ftpTrace("[%d]: Server does not understand MLST, not using that anymore", fc.id)
		fc.noMLST = true
	}

	if ff.pathx == "" {
		st.Type = ContentDir
	} else {
		// Get parent dir — C mutates ff->ff_pathx: find last '/';
		// if it's the trailing char, truncate and search again.
		pathx := ff.pathx
		r := strings.LastIndexByte(pathx, '/')
		if r >= 0 {
			if r == len(pathx)-1 {
				pathx = pathx[:r]
			}
			r = strings.LastIndexByte(pathx, '/')
		}
		var dir, fname string
		if r < 0 {
			fname = pathx
			dir = ""
		} else {
			fname = pathx[r+1:]
			dir = pathx[:r]
		}

		fd := DirAlloc()
		if ftpListDir(ff.fc, dir, fd) != 0 {
			DirFree(fd)
			return errFail()
		}
		var fde *DirEntry
		for _, e := range fd.Entries {
			if e.Filename == fname {
				fde = e
				break
			}
		}
		if fde == nil {
			DirFree(fd)
			return errFail()
		}
		*st = fde.Stat
		DirFree(fd)
	}
	ftpFileRelease(ff, false)
	return st, nil
}
