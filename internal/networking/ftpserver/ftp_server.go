// Package ftpserver — canonical port of src/networking/ftp_server.c.
//
// A minimal FTP server exposing the VFS root. Runs its sessions on
// detached goroutines (C: hts_thread_create_detached "FTP-session").
package ftpserver

import (
	"cmp"
	"errors"
	"fmt"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/misc"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/asyncio"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/metadata"
	netcore "github.com/czz/movian-go/internal/networking/core"
	"github.com/czz/movian-go/internal/networking/tcpcon"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
	"github.com/czz/movian-go/internal/version"
)

// ftpConnection — C: ftp_connection_t (ftp_server.c:40-57)
type ftpConnection struct {
	tc           *tcpcon.TCPCon // fc_tc
	authorized   bool           // fc_authorized
	wd           string         // fc_wd
	typ          byte           // fc_type
	eol          string         // fc_eol[3] (write-only in C)
	acceptSocket net.Listener   // fc_accept_socket (nil ⇔ -1)
	localAddr    *netcore.NetAddr
	remoteAddr   *netcore.NetAddr
	username     *string // fc_username
	pendingRNFR  *string // fc_pending_RNFR
}

// pre — C: #define PRE(code) (-(code))
func pre(code int) int { return -code }

// Server carries the externs C reads directly: fa_protocol_vfs (via the
// fam), the asyncio instance, and the settings manager.
type Server struct {
	fam *facore.FileAccessManager
	aio *asyncio.AsyncIO

	usageEvent func(key string, count int) // C: usage_event direct call

	mu     sync.Mutex
	fd     *asyncio.AsyncIOFD // C: static asyncio_fd_t *ftp_server_fd
	port   int                // C: static int ftp_server_port
	enable int                // C: static int ftp_server_enable

	// C: static char *ftp_username / *ftp_password — "" is the unset
	// (anonymous-allowed) value.
	username string
	password string

	// gconf — C: gconf_t fields (system_name, enable_ftp_server_debug).
	gconf *gconf.T
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (s *Server) SetGconf(g *gconf.T) { s.gconf = g }

// gcfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (s *Server) gcfg() *gconf.T {
	if s.gconf == nil {
		s.gconf = gconf.New()
	}
	return s.gconf
}

// ---------------------------------------------------------------------------
// vfs helpers — C: ftp_server_{stat,open,makedirs,unlink,rmdir,rename,scandir}
// call fa_protocol_vfs.fap_* directly (ftp_server.c:70-150).
// ---------------------------------------------------------------------------

func (s *Server) vfs() *facore.VFSProtocol {
	return s.fam.VFSProto()
}

// ftpServerStat — C: ftp_server_stat. 0 = ok. The fap boundary takes
// the post-scheme path ("/test/x"), not "vfs:///test/x".
func (s *Server) stat(url string) (*facore.FileStat, error) {
	return s.vfs().FapStat(url, facore.FaNonInteractive)
}

// ftpServerOpen — C: ftp_server_open.
func (s *Server) open(url string, flags int) (*facore.Handle, error) {
	return s.vfs().FapOpen(url, &facore.OpenExtra{Flags: flags})
}

// ftpServerMakedirs — C: ftp_server_makedirs. Returns fa_err_code_t.
func (s *Server) makedirs(url string) int {
	if err := s.vfs().FapMakedir(url); err != nil {
		return facore.FAP_ERROR
	}
	return facore.FAP_OK
}

// ftpServerUnlink — C: ftp_server_unlink. 0 = ok.
func (s *Server) unlink(url string) error {
	return s.vfs().FapUnlink(url)
}

// ftpServerRmdir — C: ftp_server_rmdir. 0 = ok.
func (s *Server) rmdir(url string) error {
	return s.vfs().FapRmdir(url)
}

// ftpServerRename — C: ftp_server_rename. 0 = ok.
func (s *Server) rename(old, new string) error {
	return s.vfs().FapRename(old, new)
}

// ftpServerScandir — C: ftp_server_scandir.
func (s *Server) scandir(url string) (*facore.Dir, error) {
	return s.vfs().FapScandir(
		facore.FAFlagsCtx(facore.FaNonInteractive), url)
}

// ---------------------------------------------------------------------------
// connection plumbing
// ---------------------------------------------------------------------------

// setWd — C: set_wd (mystrset(&fc->fc_wd, path))
func setWd(fc *ftpConnection, path string) { fc.wd = path }

// setType — C: set_type (ftp_server.c:164-173)
func setType(fc *ftpConnection, typ byte) {
	fc.typ = typ
	if typ == 'I' {
		fc.eol = "\n'"
	}
	if typ == 'A' {
		fc.eol = "\r\n"
	}
}

// ftpWrite — C: ftp_write (ftp_server.c:179-199). Replies are assembled
// in a 2048-byte buffer; each snprintf truncates at what remains.
func (s *Server) ftpWrite(fc *ftpConnection, code int,
	format string, args ...any) {
	buf := make([]byte, 0, 2048)

	if code != 0 {
		c := code
		if c < 0 {
			c = -c
		}
		sep := " "
		if code < 0 {
			sep = "-"
		}
		buf = append(buf, fmt.Sprintf("%d%s", c, sep)...)
	} else {
		buf = append(buf, ' ')
	}
	// C: vsnprintf(buf + len, sizeof(buf) - len, fmt, ap)
	msg := fmt.Sprintf(format, args...)
	if n := 2048 - len(buf); len(msg) > n {
		msg = msg[:n]
	}
	buf = append(buf, msg...)

	if s.gcfg().EnableFTPServerDebug.Load() {
		level := trace.TRACE_DEBUG
		if code >= 400 {
			level = trace.TRACE_ERROR
		}
		s.fam.TraceSystem().Trace(level, "FTP-SERVER", "SEND: %s", buf)
	}

	if n := 2048 - len(buf); n > 2 {
		buf = append(buf, "\r\n"...)
	} else if n > 0 {
		buf = append(buf, "\r\n"[:n]...)
	}
	fc.tc.TCPWriteData(buf)
}

// ---------------------------------------------------------------------------
// commands — C: cmd_* (ftp_server.c:205-783)
// ---------------------------------------------------------------------------

// cmdQUIT — C: cmd_QUIT
func (s *Server) cmdQUIT(fc *ftpConnection, args *string) int {
	s.ftpWrite(fc, 221,
		"Thank you for using the "+app.AppNameUser+" FTP service")
	return 1
}

// cmdUSER — C: cmd_USER
func (s *Server) cmdUSER(fc *ftpConnection, args *string) int {
	fc.authorized = false
	fc.username = args // C: mystrset(&fc->fc_username, args)
	s.ftpWrite(fc, 331, "Password required")
	return 0
}

// cmdPASS — C: cmd_PASS
func (s *Server) cmdPASS(fc *ftpConnection, args *string) int {
	if fc.username == nil {
		s.ftpWrite(fc, 503, "Login with USER first.")
		return 0
	}

	if s.username != "" &&
		(*fc.username != s.username || *args != s.password) {
		s.ftpWrite(fc, 530, "Login incorrect.")
		fc.username = nil // C: mystrset(&fc->fc_username, NULL)
	} else {
		fc.authorized = true
		s.ftpWrite(fc, 230, "User logged in")
	}
	return 0
}

// cmdSYST — C: cmd_SYST
func (s *Server) cmdSYST(fc *ftpConnection, args *string) int {
	s.ftpWrite(fc, 215, "UNIX Type: L8 Version: %s %s",
		version.AppVersion(), arch.GetSystemType())
	return 0
}

// cmdFEAT — C: cmd_FEAT
func (s *Server) cmdFEAT(fc *ftpConnection, args *string) int {
	s.ftpWrite(fc, pre(211), "Features supported")
	s.ftpWrite(fc, 0, "UTF8")
	s.ftpWrite(fc, 0, "SIZE")
	s.ftpWrite(fc, 211, "End")
	return 0
}

// cmdTYPE — C: cmd_TYPE
func (s *Server) cmdTYPE(fc *ftpConnection, args *string) int {
	a := *args
	if len(a) > 0 && (a[0] == 'A' || a[0] == 'I') {
		setType(fc, a[0])
		s.ftpWrite(fc, 200, "Type set to %c.", fc.typ)
	} else {
		c := byte(0)
		if len(a) > 0 {
			c = a[0]
		}
		s.ftpWrite(fc, 500, "Type '%c' not understood", c)
	}
	return 0
}

// cmdPWD — C: cmd_PWD
func (s *Server) cmdPWD(fc *ftpConnection, args *string) int {
	s.ftpWrite(fc, 257, "\"%s\" is the current directory.", fc.wd)
	return 0
}

// constructPath — C: construct_path (ftp_server.c:285-342). Writes the
// resolved path into a 1024-byte buffer (Go: the returned string,
// truncated at 1023). Returns 1 after emitting the error reply itself.
func (s *Server) constructPath(fc *ftpConnection, path string) (string, int) {
	atRoot := fc.wd == "/"

	var dst string
	if path == "." {
		dst = fc.wd
	} else if path == ".." {
		if atRoot {
			s.ftpWrite(fc, 550, "%s: Can't go further up", path)
			return "", 1
		}
		dst = fc.wd
		if len(dst) > 1023 {
			dst = dst[:1023]
		}
		r := strings.LastIndexByte(dst, '/')
		// C: assert(r != NULL)
		dst = dst[:r]
		if dst == "" {
			// did chdir(..) to root, restore root path
			dst = "/"
		}
	} else {
		r := strings.IndexAny(path, "\\:?*|<>")
		if r >= 0 {
			s.ftpWrite(fc, 550,
				"%s: Path contains invalid character: '%c'",
				path, path[r])
			return "", 1
		}
		if strings.Contains(path, "/..") {
			s.ftpWrite(fc, 550,
				"%s: Path contains invalid component: '/..'", path)
			return "", 1
		}
		if strings.HasPrefix(path, "/") {
			dst = path
		} else {
			sep := "/"
			if atRoot {
				sep = ""
			}
			dst = fc.wd + sep + path
		}
	}
	if len(dst) > 1023 {
		dst = dst[:1023]
	}

	// C: while(l > 0 && dst[l] == '/') dst[l--] = 0
	for len(dst) > 1 && strings.HasSuffix(dst, "/") {
		dst = dst[:len(dst)-1]
	}
	return dst, 0
}

// cmdCWD — C: cmd_CWD
func (s *Server) cmdCWD(fc *ftpConnection, args *string) int {
	var newpath string

	if args == nil {
		newpath = "/"
	} else {
		var cerr int
		newpath, cerr = s.constructPath(fc, *args)
		if cerr != 0 {
			return 0
		}
	}

	fs, err := s.stat(newpath)

	if err == nil && !metadata.ContentDirish(metadata.ContentType(fs.Type)) {
		err = errors.New("Not a directory")
	}

	if s.gcfg().EnableFTPServerDebug.Load() {
		if err == nil {
			s.fam.TraceSystem().Trace(trace.TRACE_DEBUG, "FTP-SERVER",
				"CHDIR: '%s' OK", newpath)
		} else {
			s.fam.TraceSystem().Trace(trace.TRACE_DEBUG, "FTP-SERVER",
				"CHDIR: '%s' Failed -- %v", newpath, err)
		}
	}

	if err != nil {
		s.ftpWrite(fc, 550, "%s: %v", newpath, err)
		return 0
	}

	setWd(fc, newpath)

	s.ftpWrite(fc, 250, "CWD command successful.")
	return 0
}

// cmdPASV — C: cmd_PASV. AF_INET socket bound to port 0, listen(1).
func (s *Server) cmdPASV(fc *ftpConnection, args *string) int {
	if fc.acceptSocket != nil {
		fc.acceptSocket.Close()
		fc.acceptSocket = nil
	}

	// XXX: We should bind on same interface as connection arrives (C)
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		s.fam.TraceSystem().Trace(trace.TRACE_ERROR, "FTP-SERVER",
			"Unable to bind -- %s", err)
		return 1
	}

	fc.acceptSocket = ln
	port := ln.Addr().(*net.TCPAddr).Port

	s.ftpWrite(fc, 227, "Entering Passive Mode (%d,%d,%d,%d,%d,%d)",
		fc.localAddr.Addr[0], fc.localAddr.Addr[1],
		fc.localAddr.Addr[2], fc.localAddr.Addr[3],
		(port>>8)&0xff, port&0xff)
	return 0
}

// getDataChannel — C: get_data_channel. Passive-mode accept().
func (fc *ftpConnection) getDataChannel() *tcpcon.TCPCon {
	if fc.acceptSocket != nil {
		conn, err := fc.acceptSocket.Accept()
		if err != nil {
			return nil
		}
		fc.acceptSocket.Close()
		fc.acceptSocket = nil
		return tcpcon.TCPFromConn(conn)
	}
	return nil
}

// cmdLIST — C: cmd_LIST
func (s *Server) cmdLIST(fc *ftpConnection, args *string) int {
	tc := fc.getDataChannel()

	if tc == nil {
		s.ftpWrite(fc, 425, "Can't build data connection")
		return 0
	}

	var a string
	if args != nil {
		// Clean up arguments that some clients wanna pass to "/bin/ls"
		a = *args
		a = strings.TrimLeft(a, " ")
		for strings.HasPrefix(a, "-") {
			// while(*args != ' ' && *args) args++;
			i := strings.IndexByte(a, ' ')
			if i < 0 {
				a = ""
				break
			}
			// while(*args == ' ') args++;
			a = strings.TrimLeft(a[i:], " ")
		}
	}

	var path string
	if a != "" {
		p, _ := s.constructPath(fc, a) // C ignores the return value
		path = p
	} else {
		path = fc.wd
	}

	if s.gcfg().EnableFTPServerDebug.Load() {
		s.fam.TraceSystem().Trace(trace.TRACE_DEBUG, "FTP-SERVER", "Listing '%s'", path)
	}

	fd, derr := s.scandir(path)
	if fd == nil {
		_ = derr
		s.ftpWrite(fc, 400, "No such directory")
		tc.TCPClose()
		return 0
	}

	s.ftpWrite(fc, 130, "Opening ASCII mode data connection for 'ls'")

	// C: RB_FOREACH over fd_entries — ordered by fde_url (fa_dir_cmp2).
	entries := make([]*facore.DirEntry, 0, len(fd.Entries))
	for _, e := range fd.Entries {
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b *facore.DirEntry) int { return cmp.Compare(a.URL, b.URL) })

	for _, fde := range entries {
		if !fde.StatDone {
			// C: fa_stat_ex(url, &fde->fde_stat, NULL, 0,
			//   FA_NON_INTERACTIVE) — statdone is NOT set.
			if st, err := facore.StatEx(s.fam, fde.URL,
				facore.FaNonInteractive); err == nil {
				fde.Stat = *st
			}
		}

		c := byte('-')
		if metadata.ContentDirish(metadata.ContentType(fde.Type)) {
			c = 'd'
		}
		tc.TCPPrintf(
			"%crwx------  1 nobody nobody %10d May  5 11:20 %s\r\n",
			c, fde.Stat.Size, fde.Filename)
	}

	facore.DirFree(fd)
	tc.TCPClose()

	s.ftpWrite(fc, 226, "Transfer complete")
	return 0
}

// cmdSIZE — C: cmd_SIZE
func (s *Server) cmdSIZE(fc *ftpConnection, args *string) int {
	pathbuf, _ := s.constructPath(fc, *args)

	fs, err := s.stat(pathbuf)
	if err != nil {
		s.ftpWrite(fc, 550, "%s: %v", *args, err)
	} else if fs.Type == int(metadata.ContentFile) {
		s.ftpWrite(fc, 213, "%d", fs.Size)
	} else {
		s.ftpWrite(fc, 550, "%s: not a plain file.", *args)
	}
	return 0
}

// cmdRETR — C: cmd_RETR
func (s *Server) cmdRETR(fc *ftpConnection, args *string) int {
	pathbuf, _ := s.constructPath(fc, *args)

	fh, oerr := s.open(pathbuf, 0)
	if fh == nil {
		s.ftpWrite(fc, 550, "%s: %v", *args, oerr)
		return 0
	}

	s.ftpWrite(fc, 150,
		"Opening BINARY mode data connetion for '%s'", *args)

	tc := fc.getDataChannel()
	if tc == nil {
		s.ftpWrite(fc, 425, "Can't build data connection")
		return 0
	}

	const bufsize = 65536
	readbuf := make([]byte, bufsize)

	error := 0
	for {
		r, _ := facore.FARead(fh, readbuf)
		if r <= 0 {
			break
		}
		if tc.TCPWriteData(readbuf[:r]) != 0 {
			error = 1
			break
		}
	}

	tc.TCPClose()
	fh.Close()

	if error != 0 {
		s.ftpWrite(fc, 400, "Write error") // XXX errorcode
	} else {
		s.ftpWrite(fc, 226, "Transfer complete")
	}
	return 0
}

// cmdSTOR — C: cmd_STOR
func (s *Server) cmdSTOR(fc *ftpConnection, args *string) int {

	pathbuf, _ := s.constructPath(fc, *args)

	fh, oerr := s.open(pathbuf, facore.FaWrite)
	if fh == nil {
		s.ftpWrite(fc, 550, "%s: %v", *args, oerr)
		return 0
	}

	s.ftpWrite(fc, 150,
		"Opening BINARY mode data connetion for '%s'", *args)

	tc := fc.getDataChannel()
	if tc == nil {
		s.ftpWrite(fc, 425, "Can't build data connection")
		return 0
	}

	const bufsize = 65536
	writebuf := make([]byte, bufsize)
	error := 0

	for {
		r := tc.TCPReadDataNowait(writebuf)
		if r <= 0 {
			break
		}
		if n, _ := facore.FAWrite(fh, writebuf[:r]); n != r {
			error = 1
			break
		}
	}

	tc.TCPClose()
	fh.Close()

	if error != 0 {
		s.ftpWrite(fc, 400, "Write error") // XXX errorcode
	} else {
		s.ftpWrite(fc, 226, "Transfer complete")
	}
	return 0
}

// cmdMKD — C: cmd_MKD
func (s *Server) cmdMKD(fc *ftpConnection, args *string) int {
	pathbuf, _ := s.constructPath(fc, *args)

	err := s.makedirs(pathbuf)
	if err != 0 {
		s.ftpWrite(fc, 550, "%s: error %d", *args, err)
		return 0
	}
	s.ftpWrite(fc, 257, "\"%s\" directory created.", *args)
	return 0
}

// cmdDELE — C: cmd_DELE
func (s *Server) cmdDELE(fc *ftpConnection, args *string) int {

	pathbuf, _ := s.constructPath(fc, *args)

	if uerr := s.unlink(pathbuf); uerr != nil {
		s.ftpWrite(fc, 550, "%s: %v", *args, uerr)
		return 0
	}
	s.ftpWrite(fc, 250, "DELE command successful.")
	return 0
}

// cmdRMD — C: cmd_RMD
func (s *Server) cmdRMD(fc *ftpConnection, args *string) int {

	pathbuf, _ := s.constructPath(fc, *args)

	if rerr := s.rmdir(pathbuf); rerr != nil {
		s.ftpWrite(fc, 550, "%s: %v", *args, rerr)
		return 0
	}
	s.ftpWrite(fc, 250, "RMD command successful.")
	return 0
}

// cmdRNFR — C: cmd_RNFR
func (s *Server) cmdRNFR(fc *ftpConnection, args *string) int {

	pathbuf, _ := s.constructPath(fc, *args)

	_, err := s.stat(pathbuf)
	if err != nil {
		s.ftpWrite(fc, 550, "%s: %v", *args, err)
		return 0
	}

	fc.pendingRNFR = &pathbuf // C: mystrset(&fc->fc_pending_RNFR, pathbuf)
	s.ftpWrite(fc, 350, "File exists, ready for destination name")
	return 0
}

// cmdRNTO — C: cmd_RNTO
func (s *Server) cmdRNTO(fc *ftpConnection, args *string) int {

	if fc.pendingRNFR == nil {
		s.ftpWrite(fc, 503, "Bad sequence of commands")
		return 0
	}

	pathbuf, _ := s.constructPath(fc, *args)

	r := s.rename(*fc.pendingRNFR, pathbuf)

	fc.pendingRNFR = nil // C: mystrset(&fc->fc_pending_RNFR, NULL)

	if r != nil {
		s.ftpWrite(fc, 550, "%s: %v", *args, r)
	} else {
		s.ftpWrite(fc, 250, "RNTO command successful.")
	}
	return 0
}

// cmdOPTS — C: cmd_OPTS
func (s *Server) cmdOPTS(fc *ftpConnection, args *string) int {
	if strings.EqualFold(*args, "UTF8 ON") {
		s.ftpWrite(fc, 200, "UTF8 set to on")
	} else if strings.EqualFold(*args, "UTF8 OFF") {
		s.ftpWrite(fc, 200, "UTF8 set to off")
	} else {
		s.ftpWrite(fc, 500, "%s: Not understood", *args)
	}
	return 0
}

// ---------------------------------------------------------------------------
// command table + session
// ---------------------------------------------------------------------------

// C: FTPCMD_NEED_ARGS / FTPCMD_AUTH_REQ (ftp_server.c:785-786)
const (
	ftpCmdNeedArgs = 0x1
	ftpCmdAuthReq  = 0x2
)

// C: ftpcmds[] (ftp_server.c:794-820)
var ftpcmds = []struct {
	cmd   string
	fn    func(s *Server, fc *ftpConnection, args *string) int
	flags int
}{
	{"QUIT", (*Server).cmdQUIT, 0},
	{"USER", (*Server).cmdUSER, ftpCmdNeedArgs},
	{"PASS", (*Server).cmdPASS, ftpCmdNeedArgs},

	{"SYST", (*Server).cmdSYST, ftpCmdAuthReq},
	{"FEAT", (*Server).cmdFEAT, ftpCmdAuthReq},
	{"PWD", (*Server).cmdPWD, ftpCmdAuthReq},
	{"XPWD", (*Server).cmdPWD, ftpCmdAuthReq},
	{"CWD", (*Server).cmdCWD, ftpCmdAuthReq},
	{"XCWD", (*Server).cmdCWD, ftpCmdAuthReq},
	{"PASV", (*Server).cmdPASV, ftpCmdAuthReq},
	{"LIST", (*Server).cmdLIST, ftpCmdAuthReq},
	{"SIZE", (*Server).cmdSIZE, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"TYPE", (*Server).cmdTYPE, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"RETR", (*Server).cmdRETR, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"STOR", (*Server).cmdSTOR, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"MKD", (*Server).cmdMKD, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"XMKD", (*Server).cmdMKD, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"DELE", (*Server).cmdDELE, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"RMD", (*Server).cmdRMD, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"XRMD", (*Server).cmdRMD, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"RNFR", (*Server).cmdRNFR, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"RNTO", (*Server).cmdRNTO, ftpCmdAuthReq | ftpCmdNeedArgs},
	{"OPTS", (*Server).cmdOPTS, ftpCmdAuthReq | ftpCmdNeedArgs},
}

// ftpSession — C: ftp_session (ftp_server.c:827-885). Detached thread.
func (s *Server) ftpSession(fc *ftpConnection) {
	var buf [1024]byte

	host := netcore.NetFmtHost(fc.localAddr)

	s.ftpWrite(fc, 220, "%s FTP server (%s  Version %s) ready.",
		host, s.gcfg().SystemName, version.AppVersion())

	for {
		if fc.tc.TCPReadLine(buf[:]) != 0 {
			break
		}
		line := misc.CStr(buf[:])

		if s.gcfg().EnableFTPServerDebug.Load() {
			s.fam.TraceSystem().Trace(trace.TRACE_DEBUG, "FTP-SERVER",
				"RECV: %s", line)
		}

		var args *string
		cmd := line
		if before, after, ok := strings.Cut(line, " "); ok {
			cmd = before
			a := strings.TrimLeft(after, " ")
			args = &a
		}

		r := -1
		for i := range ftpcmds {
			if strings.EqualFold(ftpcmds[i].cmd, cmd) {

				if ftpcmds[i].flags&ftpCmdAuthReq != 0 &&
					!fc.authorized {
					s.ftpWrite(fc, 530, "Please login first")
					r = 0
				} else if ftpcmds[i].flags&ftpCmdNeedArgs != 0 &&
					args == nil {
					s.ftpWrite(fc, 500,
						"'%s': Arguments required", cmd)
					r = 0
				} else {
					r = ftpcmds[i].fn(s, fc, args)
				}
				break
			}
		}

		if r == -1 {
			s.ftpWrite(fc, 500, "'%s': Command not understood", cmd)
			continue
		}

		if r != 0 {
			break
		}
	}

	fc.tc.TCPClose()
	// C: free(fc->fc_username / fc_pending_RNFR / fc_wd) — GC
	if fc.acceptSocket != nil {
		fc.acceptSocket.Close()
	}
}

// netAddrFromAddr — C: net_addr_from_sockaddr_in (asyncio_posix.c).
func netAddrFromAddr(a net.Addr) *netcore.NetAddr {
	na := &netcore.NetAddr{}
	if ta, ok := a.(*net.TCPAddr); ok {
		na.Port = uint16(ta.Port)
		if ip4 := ta.IP.To4(); ip4 != nil {
			na.Family = 4
			copy(na.Addr[:], ip4)
		} else {
			na.Family = 6
			copy(na.Addr[:], ta.IP.To16())
		}
	}
	return na
}

// ftpAccept — C: ftp_accept (ftp_server.c:891-907). The canonical
// callback receives the raw fd; Go wraps it in a net.Conn for tcpcon.
func (s *Server) ftpAccept(opaque any, conn net.Conn,
	localAddr, remoteAddr *netcore.NetAddr) {
	s.ftpAcceptConn(conn, localAddr, remoteAddr)
}

// ftpAcceptConn — connection-level half of ftp_accept (fd wrapping is
// done by the asyncio callback above).
func (s *Server) ftpAcceptConn(conn net.Conn,
	localAddr, remoteAddr *netcore.NetAddr) {
	fc := &ftpConnection{}
	fc.tc = tcpcon.TCPFromConn(conn)
	fc.localAddr = localAddr
	fc.remoteAddr = remoteAddr
	fc.acceptSocket = nil
	setWd(fc, "/")
	setType(fc, 'A')

	if s.usageEvent != nil {
		s.usageEvent("FTP Server", 1)
	}

	// C: hts_thread_create_detached("FTP-session", ftp_session, fc,
	//   THREAD_PRIO_MODEL)
	go s.ftpSession(fc)
}

// ---------------------------------------------------------------------------
// settings + init — C: enable_disable / set_* / ftp_server_init
// ---------------------------------------------------------------------------

// enableDisable — C: enable_disable (ftp_server.c:918-937)
func (s *Server) enableDisable() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.port != 0 && s.enable != 0 {

		if s.fd != nil && s.fd.GetPort() == s.port {
			return
		}

		if s.fd != nil {
			s.fd.Close()
		}

		// C: asyncio_listen("ftp-server", port, ftp_accept, NULL, 0)
		// C: asyncio_listen returns NULL on failure
		s.fd = s.aio.Listen("ftp-server", s.port, s.ftpAccept, nil, false)
	} else {
		if s.fd == nil {
			return
		}
		s.fd.Close()
		s.fd = nil
	}
}

// setEnable — C: set_enable
func (s *Server) setEnable(v int) {
	s.enable = v
	s.enableDisable()
}

// setPort — C: set_port
func (s *Server) setPort(str string) {
	p, _ := strconv.Atoi(str)
	s.port = p
	s.enableDisable()
}

// setUsername — C: set_username (mystrset(&ftp_username, str))
func (s *Server) setUsername(str string) { s.username = str }

// setPassword — C: set_password (mystrset(&ftp_password, str))
func (s *Server) setPassword(str string) { s.password = str }

// FTPServerStart — C: ftp_server_init (ftp_server.c:980-1024),
// INITME(INIT_GROUP_ASYNCIO, ftp_server_init, NULL, 0).
// SetUsageEvent wires usage_event (C: usage.c direct call on auth'd
// connects — ftp_server.c).
func (s *Server) SetUsageEvent(fn func(key string, count int)) { s.usageEvent = fn }

func (s *Server) FTPServerStart(sm *settingscore.SettingsManager,
	aio *asyncio.AsyncIO, fam *facore.FileAccessManager) {
	s.fam = fam
	s.aio = aio

	if sm == nil {
		return
	}

	// C: settings_create_separator(gconf.settings_network, ...)
	sm.CreateSeparatorProp(sm.Network(), sm.P("FTP server"))

	courier := aio.Courier()

	// C: setting_create(SETTING_BOOL, gconf.settings_network, ...)
	sm.SettingCreate(settingscore.SettingBool, sm.Network(),
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Enable FTP server"),
		settingscore.SettingTagValue, 0,
		settingscore.SettingTagCallback,
		func(opaque any, value any) {
			if v, ok := value.(int); ok {
				s.setEnable(v)
			}
		}, nil,
		settingscore.SettingTagStore, "ftpserver", "enable",
		settingscore.SettingTagCourier, courier,
	)

	// C: setting_create(SETTING_STRING, ..., "Server TCP port", "2121")
	sm.SettingCreate(settingscore.SettingString, sm.Network(),
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Server TCP port"),
		settingscore.SettingTagValue, "2121",
		settingscore.SettingTagCallback,
		func(opaque any, value any) {
			if str, ok := value.(string); ok {
				s.setPort(str)
			}
		}, nil,
		settingscore.SettingTagStore, "ftpserver", "port",
		settingscore.SettingTagCourier, courier,
	)

	// C: SETTING_STRING "Username" default ""
	sm.SettingCreate(settingscore.SettingString, sm.Network(),
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Username"),
		settingscore.SettingTagValue, "",
		settingscore.SettingTagCallback,
		func(opaque any, value any) {
			if str, ok := value.(string); ok {
				s.setUsername(str)
			}
		}, nil,
		settingscore.SettingTagStore, "ftpserver", "username",
		settingscore.SettingTagCourier, courier,
	)

	// C: SETTING_STRING | SETTINGS_PASSWORD "Password" default ""
	sm.SettingCreate(settingscore.SettingString, sm.Network(),
		settingscore.SettingsInitialUpdate|settingscore.SettingsPassword,
		settingscore.SettingTagTitle, sm.P("Password"),
		settingscore.SettingTagValue, "",
		settingscore.SettingTagCallback,
		func(opaque any, value any) {
			if str, ok := value.(string); ok {
				s.setPassword(str)
			}
		}, nil,
		settingscore.SettingTagStore, "ftpserver", "password",
		settingscore.SettingTagCourier, courier,
	)
}
