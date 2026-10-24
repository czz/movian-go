// SMB2/3 client path backed by github.com/hirochachacha/go-smb2/v2.
// The native SMBv1 machinery (smb_conn.go/smb_ops.go) stays as fallback
// for servers that do not speak SMB2: every public op tries SMB2 first
// and degrades to the legacy path when negotiation fails.
package smb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	smb2 "github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/auth"
	"github.com/hirochachacha/go-smb2/v2/x/protocol"
	"github.com/hirochachacha/go-smb2/v2/x/wire"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/networking/tcpcon"
)

// errSMB2Unsupported is returned when the server does not speak SMB2/3
// (negotiate/transport failure); callers degrade to the native SMBv1
// path.
var errSMB2Unsupported = errors.New("server does not speak SMB2/3")

// errSMB2ConnDead marks a cached connection whose session died.
var errSMB2ConnDead = errors.New("SMB2 session lost")

// NTSTATUS codes matched against protocol.ResponseError.Code.
const (
	ntStatusLogonFailure           = 0xC000006D
	ntStatusPasswordExpired        = 0xC0000071
	ntStatusAccountDisabled        = 0xC0000072
	ntStatusPasswordMustChange     = 0xC0000224
	ntStatusNetworkAccessDenied    = 0xC00000CA
	ntStatusNetworkNameDeleted     = 0xC00000C9
	ntStatusUserSessionDeleted     = 0xC0000203
	ntStatusConnectionDisconnected = 0xC000020C
	ntStatusConnectionReset        = 0xC000020D
	ntStatusPipeDisconnected       = 0xC00000B0
)

// smb2Conn — pooled SMB2/3 session with its mounted shares. Replaces
// cifsConnection on the modern path: one TCP session per host:port,
// one tree connect per share.
type smb2Conn struct {
	sess   *smb2.Session
	shares map[string]*smb2.Share
	dead   bool
}

// smb2File wraps *smb2.File as facore.SMBFile (C: smb_file_t).
type smb2File struct {
	f    *smb2.File
	size int64
}

func (sf *smb2File) Read(buf []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(),
		60*time.Second)
	defer cancel()
	return sf.f.Read(ctx, buf)
}

func (sf *smb2File) Seek(offset int64, whence int) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(),
		30*time.Second)
	defer cancel()
	return sf.f.Seek(ctx, offset, whence)
}

func (sf *smb2File) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(),
		15*time.Second)
	defer cancel()
	return sf.f.Close(ctx)
}

func (sf *smb2File) Size() int64 { return sf.size }

// smb2TCPDialer connects the SMB2 transport through tcpcon's resolver
// so dot-less NetBIOS names keep working (net.Dialer alone is DNS-only).
type smb2TCPDialer struct{}

func (smb2TCPDialer) Dial(ctx context.Context, serverName string) (smb2.Transport, error) {
	host, port, err := net.SplitHostPort(serverName)
	if err != nil {
		host, port = serverName, "445"
	}
	ip, err := tcpcon.ResolveHostname(host)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return nil, err
	}
	return smb2.NewTransport(conn), nil
}

// smb2Dial — session setup with keyring credential loop, mirroring
// smbSetupAndX: stored creds first (no QUERY_USER), then interactive
// re-prompt on failure. Same keyring ids so saved logins carry over.
func (sys *System) smb2Dial(host string, port int, interactive bool) (*smb2.Session, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var retryReason string
	for i := 0; i < 8; i++ {
		if retryReason != "" && !interactive {
			return nil, facore.ErrAuthRequired
		}
		var id, name [256]byte
		cstrSet(id[:], "smb:connection:%s:%d", host, port)
		cstrSet(name[:], "Samba server '%s'", host)

		ku, kp, kd := krUnset, krUnset, krUnset
		r := 0
		if sys.kr != nil {
			kf := 0x2 | 0x4 // SHOW_REMEMBER_ME | REMEMBER_ME_SET
			if retryReason != "" {
				kf |= 0x1 // QUERY_USER
			}
			r = sys.kr(misc.CStr(id[:]), &ku, &kp, &kd, nil,
				misc.CStr(name[:]), retryReason, kf)
		}
		if r == 1 {
			retryReason = "Login required"
			continue
		}
		if r == -1 {
			return nil, errors.New("Authentication rejected by user")
		}
		cred := smb2Credential(host, ku, kp, kd)
		d := &smb2.Dialer{
			Credentials:     cred,
			TransportDialer: smb2TCPDialer{},
		}
		ctx, cancel := context.WithTimeout(context.Background(),
			30*time.Second)
		sess, err := d.Dial(ctx, addr)
		cancel()
		if err == nil {
			sys.smbTrace("%s:%d SMB2/3 session established", host, port)
			return sess, nil
		}
		if smb2IsAuthError(err) {
			sys.smbTrace("%s:%d SMB2 auth failed: %s", host, port, err)
			if sys.kr == nil {
				return nil, err
			}
			retryReason = "Authentication failed"
			continue
		}
		return nil, err
	}
	return nil, errors.New("Authentication failed")
}

// smb2Credential maps the keyring result to go-smb2's NTLM credential.
// An unset or empty domain stays nil so go-smb2 authenticates against the
// server's challenge TargetName; a pointer to "" would force an empty
// domain instead.
//
// TargetSPN is set to cifs/<host> without the port. go-smb2 otherwise
// derives it from the dial address ("host:port"), which differs from the
// SPN Windows clients and smbclient send and can be rejected by servers
// that harden SPN target-name checking.
func smb2Credential(host, user, password, domain string) auth.NTLMCredential {
	spn := "cifs/" + host
	cred := auth.NTLMCredential{TargetSPN: &spn}
	if user != krUnset {
		cred.User = user
	}
	if password != krUnset {
		cred.Password = password
	}
	if domain != krUnset && domain != "" {
		cred.Domain = &domain
	}
	return cred
}

func smb2IsAuthError(err error) bool {
	var re *protocol.ResponseError
	if !errors.As(err, &re) {
		return false
	}
	switch re.Code {
	case ntStatusLogonFailure, ntStatusPasswordExpired,
		ntStatusAccountDisabled, ntStatusPasswordMustChange,
		ntStatusNetworkAccessDenied:
		return true
	}
	return false
}

// smb2SessionDead — the transport/session broke; the caller drops the
// cached conn and retries once on a fresh session.
func smb2SessionDead(err error) bool {
	if errors.Is(err, errSMB2ConnDead) || errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}
	var re *protocol.ResponseError
	if errors.As(err, &re) {
		switch re.Code {
		case ntStatusNetworkNameDeleted, ntStatusUserSessionDeleted,
			ntStatusConnectionDisconnected, ntStatusConnectionReset,
			ntStatusPipeDisconnected:
			return true
		}
	}
	return false
}

// smb2SplitURL — smb://host[:port]/share/path → parts, same split as
// cifsResolve.
func smb2SplitURL(url string) (host string, port int, share, path string) {
	var hostname [128]byte
	var pathb [512]byte
	port = -1
	misc.UrlSplit(nil, 0, nil, 0, hostname[:], len(hostname), &port,
		pathb[:], len(pathb), url)
	p := misc.CStr(pathb[:])
	p = strings.TrimPrefix(p, "/")
	share = p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		path = p[i+1:]
		share = p[:i]
	}
	if port < 0 {
		port = 445
	}
	host = misc.CStr(hostname[:])
	return
}

// smb2GetConn — find-or-dial the pooled session for host:port.
func (sys *System) smb2GetConn(host string, port int, interactive bool) (*smb2Conn, error) {
	key := net.JoinHostPort(host, strconv.Itoa(port))
	sys.s2mu.Lock()
	if sys.s2down[key] {
		sys.s2mu.Unlock()
		return nil, errSMB2Unsupported
	}
	if cc := sys.s2conns[key]; cc != nil && !cc.dead {
		sys.s2mu.Unlock()
		return cc, nil
	}
	sys.s2mu.Unlock()

	sess, err := sys.smb2Dial(host, port, interactive)
	if err != nil {
		if errors.Is(err, facore.ErrAuthRequired) {
			return nil, err
		}
		var re *protocol.ResponseError
		if errors.As(err, &re) {
			// The server spoke SMB2 — surface its error verbatim.
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", errSMB2Unsupported, err)
	}
	cc := &smb2Conn{sess: sess, shares: map[string]*smb2.Share{}}
	sys.s2mu.Lock()
	if sys.s2conns == nil {
		sys.s2conns = map[string]*smb2Conn{}
	}
	if old := sys.s2conns[key]; old != nil {
		if !old.dead {
			// A concurrent dial won while we were dialing and its
			// session may already serve other callers: keep it.
			sys.s2mu.Unlock()
			sess.Abort()
			return old, nil
		}
		old.sess.Abort()
	}
	sys.s2conns[key] = cc
	sys.s2mu.Unlock()
	return cc, nil
}

// smb2Drop discards a dead session so the next resolve redials.
func (sys *System) smb2Drop(cc *smb2Conn) {
	sys.s2mu.Lock()
	cc.dead = true
	for key, c := range sys.s2conns {
		if c == cc {
			delete(sys.s2conns, key)
			break
		}
	}
	sys.s2mu.Unlock()
	cc.sess.Abort()
}

// smb2MarkDown records host:port as SMBv1-only once a legacy fallback
// actually succeeded — subsequent ops skip the SMB2 attempt.
func (sys *System) smb2MarkDown(url string) {
	host, port, _, _ := smb2SplitURL(url)
	if host == "" {
		return
	}
	sys.s2mu.Lock()
	if sys.s2down == nil {
		sys.s2down = map[string]bool{}
	}
	sys.s2down[net.JoinHostPort(host, strconv.Itoa(port))] = true
	sys.s2mu.Unlock()
}

// smb2Resolve — url → pooled conn + share + relative path.
func (sys *System) smb2Resolve(url string, interactive bool) (*smb2Conn, string, string, error) {
	host, port, share, path := smb2SplitURL(url)
	if host == "" {
		return nil, "", "", errSMB2Unsupported
	}
	cc, err := sys.smb2GetConn(host, port, interactive)
	if err != nil {
		return nil, "", "", err
	}
	return cc, share, path, nil
}

// smb2Attempt runs fn on a resolved conn, transparently retrying once
// on a fresh session when the cached one died mid-operation.
func (sys *System) smb2Attempt(url string, interactive bool,
	fn func(cc *smb2Conn, share, path string) error) error {
	var err error
	for i := 0; i < 2; i++ {
		var cc *smb2Conn
		var share, path string
		cc, share, path, err = sys.smb2Resolve(url, interactive)
		if err != nil {
			return err
		}
		err = fn(cc, share, path)
		if err == nil {
			return nil
		}
		if !smb2SessionDead(err) {
			return err
		}
		sys.smb2Drop(cc)
	}
	return err
}

// smb2Mount — find-or-mount the share on the session.
func (sys *System) smb2Mount(cc *smb2Conn, share string) (*smb2.Share, error) {
	sys.s2mu.Lock()
	if cc.dead {
		sys.s2mu.Unlock()
		return nil, errSMB2ConnDead
	}
	sh := cc.shares[share]
	sys.s2mu.Unlock()
	if sh != nil {
		return sh, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		30*time.Second)
	defer cancel()
	nsh, err := cc.sess.Mount(ctx, share)
	if err != nil {
		return nil, err
	}
	sys.s2mu.Lock()
	cc.shares[share] = nsh
	sys.s2mu.Unlock()
	return nsh, nil
}

// smb2Open — SMB2 equivalent of smb_open.
func (sys *System) smb2Open(url string, flags int) (facore.SMBFile, error) {
	var out facore.SMBFile
	err := sys.smb2Attempt(url, flags&facore.FaNonInteractive == 0,
		func(cc *smb2Conn, share, path string) error {
			if share == "" || path == "" {
				return errors.New("Invalid URL for operation")
			}
			sh, err := sys.smb2Mount(cc, share)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(),
				30*time.Second)
			defer cancel()
			f, err := sh.Open(ctx, path)
			if err != nil {
				return err
			}
			fi, err := f.Stat(ctx)
			if err != nil {
				f.Close(ctx)
				return err
			}
			out = &smb2File{f: f, size: fi.Size()}
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// smb2Scandir — SMB2 equivalent of smb_scandir: server → share list
// (SRVS enum via IPC$), share → directory listing.
func (sys *System) smb2Scandir(fd *facore.Dir, url string, flags int) error {
	host, port, _, _ := smb2SplitURL(url)
	urlbase := fmt.Sprintf("smb://%s", host)
	if port != 445 {
		urlbase += fmt.Sprintf(":%d", port)
	}
	urlbase += "/"
	return sys.smb2Attempt(url, flags&facore.FaNonInteractive == 0,
		func(cc *smb2Conn, share, path string) error {
			ctx, cancel := context.WithTimeout(context.Background(),
				30*time.Second)
			defer cancel()
			if share == "" {
				names, err := cc.sess.ListShareNames(ctx)
				if err != nil {
					return err
				}
				for _, n := range names {
					facore.DirAdd(fd, urlbase+n, n, facore.ContentShare)
				}
				return nil
			}
			sh, err := sys.smb2Mount(cc, share)
			if err != nil {
				return err
			}
			dir := path
			if dir == "" {
				dir = "."
			}
			fis, err := sh.ReadDir(ctx, dir)
			if err != nil {
				return err
			}
			base := urlbase + share
			if path != "" {
				base += "/" + path
			}
			base += "/"
			for _, fi := range fis {
				name := fi.Name()
				if name == "." || name == ".." {
					continue
				}
				typ := facore.ContentFile
				if fi.IsDir() {
					typ = facore.ContentDir
				}
				fde := facore.DirAdd(fd, base+name, name, typ)
				if fde != nil {
					fde.Stat.Size = fi.Size()
					fde.Stat.MTime = fi.ModTime()
					fde.StatDone = true
				}
			}
			return nil
		})
}

// smb2Stat — SMB2 equivalent of smb_stat.
func (sys *System) smb2Stat(url string, flags int) (*facore.FileStat, error) {
	var out *facore.FileStat
	err := sys.smb2Attempt(url, flags&facore.FaNonInteractive == 0,
		func(cc *smb2Conn, share, path string) error {
			st := &facore.FileStat{MTime: time.Unix(0, 0)}
			if share == "" {
				st.Type = facore.ContentShare
				out = st
				return nil
			}
			if path == "" {
				st.Type = facore.ContentDir
				out = st
				return nil
			}
			sh, err := sys.smb2Mount(cc, share)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(),
				30*time.Second)
			defer cancel()
			fi, err := sh.Stat(ctx, path)
			if err != nil {
				return err
			}
			st.Type = facore.ContentFile
			if fi.IsDir() {
				st.Type = facore.ContentDir
			}
			st.Size = fi.Size()
			st.MTime = fi.ModTime()
			out = st
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// smb2Unlink — SMB2 equivalent of smb_unlink (delete file).
func (sys *System) smb2Unlink(url string) error {
	return sys.smb2Attempt(url, true,
		func(cc *smb2Conn, share, path string) error {
			if share == "" || path == "" {
				return errors.New("Invalid URL for operation")
			}
			sh, err := sys.smb2Mount(cc, share)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(),
				30*time.Second)
			defer cancel()
			return sh.Remove(ctx, path)
		})
}

// smb2Rmdir — SMB2 equivalent of smb_rmdir. Share.Remove opens files
// only; deleting a directory needs FILE_DIRECTORY_FILE on the CREATE.
func (sys *System) smb2Rmdir(url string) error {
	return sys.smb2Attempt(url, true,
		func(cc *smb2Conn, share, path string) error {
			if share == "" || path == "" {
				return errors.New("Invalid URL for operation")
			}
			sh, err := sys.smb2Mount(cc, share)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(),
				30*time.Second)
			defer cancel()
			req := sh.Request().WithFollowSymlinks(true).
				Create(path, wire.DELETE, wire.FILE_OPEN,
					wire.FILE_OPEN_REPARSE_POINT|wire.FILE_DIRECTORY_FILE,
					wire.FILE_ATTRIBUTE_NORMAL).
				SetInfo(wire.SMB2_0_INFO_FILE,
					wire.FileDispositionInformation, 0,
					&wire.FileDispositionInformationEncoder{
						DeletePending: 1,
					}).
				Close()
			res, err := req.Do(ctx)
			if err != nil {
				return err
			}
			res.Close()
			return nil
		})
}
