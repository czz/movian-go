// Canonical port of src/fileaccess/smb/fa_nativesmb.c — native SMBv1
// (CIFS) client. Architecture: a global pool of cifs_connection_t, each
// owning a tcpcon and a dispatch goroutine that demultiplexes replies by
// MID onto a pending-request list guarded by smb_global_mutex; per-share
// cifs_tree_t with cond-signaled connect; DCE/RPC share enumeration;
// pipelined READ_ANDX; 30s SMB_ECHO keepalive with auto-disconnect.
package smb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/czz/movian-go/internal/app"
	"github.com/czz/movian-go/internal/callout"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/networking/tcpcon"
	"github.com/czz/movian-go/internal/trace"
)

// cifsMaybeDestroy — C: cifs_maybe_destroy (fa_nativesmb.c:472-505).
// Caller holds cc.sys.mu; always returns with it unlocked.
func cifsMaybeDestroy(cc *cifsConnection) {
	if cc.refcount > 0 {
		cc.sys.mu.Unlock()
		return
	}
	for i, c := range cc.sys.connections {
		if c == cc {
			cc.sys.connections = slices.Delete(cc.sys.connections, i, i+1)
			break
		}
	}
	// As we are unlinked noone can find us anymore, so it's safe to unlock
	cc.sys.mu.Unlock()

	if cc.tc != nil {
		cc.sys.smbTrace("Disconnecting from %s:%d", cc.hostname, cc.port)
		cc.tc.TCPShutdown()
	}
	if cc.threadDone != nil {
		<-cc.threadDone // C: hts_thread_join
	}
	if cc.tc != nil {
		cc.tc.TCPClose()
	}
	if cs := cc.sys.cs; cs != nil {
		cs.Disarm(&cc.timer)
	}
}

// cifsReleaseConnection — C: cifs_release_connection (fa_nativesmb.c:511)
func cifsReleaseConnection(cc *cifsConnection) {
	cc.refcount--
	cifsMaybeDestroy(cc)
}

// smbNegProto — C: smb_neg_proto (fa_nativesmb.c:523). Caller holds
// cc.sys.mu (blocks on socket I/O while holding it, as in C).
func smbNegProto(cc *cifsConnection) error {
	dialect := "NT LM 0.12"
	dlen := len(dialect) + 1
	tlen := smbNegProtoReqLen + 1 + dlen

	req := make([]byte, tlen)
	smbSetupHeader(cc, req[4:], smbNegProtocol,
		smbFlagsCaselessPathnames, smbFlags232BitStatus, 0, true)
	req[36] = 0                                             // wordcount
	binary.LittleEndian.PutUint16(req[37:], uint16(dlen+1)) // bytecount
	req[39] = 2                                             // protos[0]
	copy(req[40:], dialect)

	cc.nbtWrite(req)

	rbuf, err := cc.nbtRead()
	if err != nil {
		return errors.New("Socket read error during negotiation")
	}
	if len(rbuf) < smbNegProtoReplyLen || rbuf[32] != 17 {
		return fmt.Errorf("Malformed response %d bytes during negotiation", len(rbuf))
	}
	if e := binary.LittleEndian.Uint32(rbuf[smbErrorcodeOff:]); e != 0 {
		return fmt.Errorf("Negotiation error 0x%08x", e)
	}

	caps := binary.LittleEndian.Uint32(rbuf[52:])
	cc.unicode = caps&serverCapUnicode != 0
	if cc.unicode {
		cc.bpc = 2
	} else {
		cc.bpc = 1
	}
	if caps&serverCapNtSmbs != 0 {
		cc.ntsmb = true
	} else {
		return errors.New("Server does not support NTSMB")
	}

	cc.sessionKey = binary.LittleEndian.Uint32(rbuf[48:])
	cc.securityMode = rbuf[35]
	cc.maxBufSize = min(uint16(binary.LittleEndian.Uint32(rbuf[40:])), 65000)
	cc.maxMpxCount = binary.LittleEndian.Uint16(rbuf[36:])

	remain := len(rbuf) - smbNegProtoReplyLen
	if remain >= 8 {
		copy(cc.challengeKey[:], rbuf[69:77])
		remain -= 8
		if remain > 0 {
			misc.Ucs2ToUtf8(cc.domain[:], len(cc.domain), rbuf[77:], remain, 1)
		}
	} else if remain > 0 {
		// C reads 8 bytes unconditionally; truncated keys get the tail.
		copy(cc.challengeKey[:], rbuf[69:])
	}
	return nil
}

// krUnset marks a keyring out-param the handler left untouched — models
// C's NULL out-param results from keyring_lookup.
const krUnset = "\x00keyring-unset\x00"

// smbSetupAndX — C: smb_setup_andX (fa_nativesmb.c:603). Returns 0 ok,
// -1 error, -2 → SAMBA_NEED_AUTH (non-interactive retry bail).
// Caller holds cc.sys.mu.
func smbSetupAndX(cc *cifsConnection, flags int) (int, error) {
	os := "Unix"
	lanmgr := app.AppNameUser

	olen := cc.utf8ToSMB(nil, os)
	llen := cc.utf8ToSMB(nil, lanmgr)

	var retryReason string

	for {
		password := make([]byte, 24)
		passwordLen := 1
		// C: domain = strdup(cc_domain[0] ? cc_domain : "WORKGROUP")
		dom := "WORKGROUP"
		if cc.domain[0] != 0 {
			dom = misc.CStr(cc.domain[:])
		}
		var domainP *string
		domainP = &dom
		var usernameP, passwordP *string

		if cc.securityMode&securityUserLevel != 0 && flags&ccFAsGuest == 0 {
			if retryReason != "" && flags&ccFNonInteractive != 0 {
				return -2, nil
			}
			var id, name [256]byte
			cstrSet(id[:], "smb:connection:%s:%d", cc.hostname, cc.port)
			cstrSet(name[:], "Samba server '%s'", cc.hostname)

			u, p, d := krUnset, krUnset, *domainP
			r := 1
			if cc.sys.kr != nil {
				kf := 0x2 | 0x4 // SHOW_REMEMBER_ME | REMEMBER_ME_SET
				if retryReason != "" {
					kf |= 0x1 // QUERY_USER
				}
				r = cc.sys.kr(misc.CStr(id[:]),
					&u, &p, &d, nil,
					misc.CStr(name[:]), retryReason, kf)
			}
			if r == 1 {
				retryReason = "Login required"
				continue
			}
			if r == -1 {
				return -1, errors.New("Authentication rejected by user")
			}
			if u != krUnset {
				usernameP = &u
			}
			if p != krUnset {
				passwordP = &p
			}
			// C: keyring cleared domain → reset to server default
			if d == krUnset {
				d = "WORKGROUP"
				if cc.domain[0] != 0 {
					d = misc.CStr(cc.domain[:])
				}
			}
			domainP = &d
		} else if flags&ccFAnonymous != 0 {
			// Anonymous — NULL username/password/domain
			domainP = nil
		} else {
			u, p := "guest", ""
			usernameP, passwordP = &u, &p
		}

		if passwordP != nil {
			digest := ntlmHash(*passwordP)
			lmresponse(password, digest[:], cc.challengeKey[:])
			passwordLen = 24
			u := "<unset>"
			if usernameP != nil {
				u = *usernameP
			}
			pw := "<unset>"
			if *passwordP != "" {
				pw = "<hidden>"
			}
			dom := "<unset>"
			if domainP != nil {
				dom = *domainP
			}
			cc.sys.smbTrace("SETUP %s:%s:%s", u, pw, dom)
		} else {
			cc.sys.smbTrace("SETUP anonymous")
			passwordLen = 0
		}
		passwordPad := 0
		if cc.unicode && passwordLen&1 == 0 {
			passwordPad = 1
		}

		ulen := 2
		if usernameP != nil {
			ulen = cc.utf8ToSMB(nil, *usernameP)
		}
		dlen := 2
		if domainP != nil {
			dlen = cc.utf8ToSMB(nil, *domainP)
		}
		bytecount := passwordLen + passwordPad + ulen + dlen + olen + llen
		tlen := bytecount + smbSetupAndXReqLen

		req := make([]byte, tlen)
		smbSetupHeader(cc, req[4:], smbSetupAndx,
			smbFlagsCaselessPathnames, smbFlags232BitStatus, 0, true)
		req[36] = 13   // wordcount
		req[37] = 0xff // andx_command
		binary.LittleEndian.PutUint16(req[41:], cc.maxBufSize)
		binary.LittleEndian.PutUint16(req[43:], cc.maxMpxCount)
		binary.LittleEndian.PutUint16(req[45:], 1) // vc_number
		binary.LittleEndian.PutUint32(req[47:], cc.sessionKey)
		binary.LittleEndian.PutUint32(req[59:], clientCapLargeReadx|
			clientCapUnicode|clientCapLargeFiles|clientCapNtSmbs|
			clientCapStatus32)
		binary.LittleEndian.PutUint16(req[53:], uint16(passwordLen))
		binary.LittleEndian.PutUint16(req[63:], uint16(bytecount))

		ptr := smbSetupAndXReqLen
		copy(req[ptr:], password[:passwordLen])
		ptr += passwordLen + passwordPad

		if usernameP != nil {
			ptr += cc.utf8ToSMB(req[ptr:], *usernameP)
		} else {
			req[ptr] = 0
			req[ptr+1] = 0
			ptr += 2
		}
		if domainP != nil {
			ptr += cc.utf8ToSMB(req[ptr:], *domainP)
		} else {
			req[ptr] = 0
			req[ptr+1] = 0
			ptr += 2
		}
		ptr += cc.utf8ToSMB(req[ptr:], os)
		ptr += cc.utf8ToSMB(req[ptr:], lanmgr)

		cc.nbtWrite(req)

		rbuf, err := cc.nbtRead()
		if err != nil {
			return -1, errors.New("Socket read error during setup")
		}
		errcode := binary.LittleEndian.Uint32(rbuf[smbErrorcodeOff:])
		cc.sys.smbTrace("SETUP errorcode=0x%08x", errcode)

		if errcode != 0 {
			retryReason = cc.sys.smbError(errcode).Error()
			if flags&ccFAsGuest != 0 {
				return -1, errors.New("Guest login failed")
			}
			continue
		}
		if len(rbuf) < smbSetupAndXReplyLen {
			return -1, fmt.Errorf("Malformed response %d bytes during setup", len(rbuf))
		}
		guest := binary.LittleEndian.Uint16(rbuf[37:]) & 1 // action
		cc.uid = binary.LittleEndian.Uint16(rbuf[smbUidOff:])

		bc := int(binary.LittleEndian.Uint16(rbuf[39:])) - 1 // bytecount - pad
		dataOff := 41 + 1                                    // 1 byte pad
		if s, ok := readstring(rbuf, &dataOff, &bc, cc.unicode); ok {
			cc.nativeOS = s
			if s, ok := readstring(rbuf, &dataOff, &bc, cc.unicode); ok {
				cc.nativeLanman = s
				if s, ok := readstring(rbuf, &dataOff, &bc, cc.unicode); ok {
					cc.primaryDomain = s
				}
			}
		}

		cc.sys.smbTrace("Logged in as UID:%d guest=%v os='%s' lanman='%s' PD='%s'",
			cc.uid, guest == 1, cc.nativeOS, cc.nativeLanman, cc.primaryDomain)

		if guest == 1 && flags&ccFAsGuest == 0 &&
			cc.securityMode&securityUserLevel != 0 {
			retryReason = "Login attempt failed"
			continue
		}
		if flags&ccFAnonymous == 0 && cc.sys.usageEvent != nil {
			cc.sys.usageEvent("SMB connect", 1)
		}
		return 0, nil
	}
}

// dumpRequestList — C: dump_request_list (fa_nativesmb.c:834)
func dumpRequestList(cc *cifsConnection) {
	cc.sys.smbTrace("List of pending reuqests")
	for _, nr := range cc.pendingNBT {
		cc.sys.smbTrace("  Pending request %d", nr.mid)
	}
}

// smbDispatch — C: smb_dispatch (fa_nativesmb.c:848). Read loop
// demultiplexing replies by MID; TRANS2 reassembly happens here.
func smbDispatch(cc *cifsConnection) {
	defer close(cc.threadDone)
	cc.sys.smbTrace("%s:%d Read thread running", cc.hostname, cc.port)

	for {
		buf, err := cc.nbtRead()
		if err != nil {
			break
		}
		if len(buf) < smbHdrLen {
			cc.sys.ts.Trace(trace.TRACE_ERROR, "SMB",
				"%s:%d malformed packet smbhdrlen %d",
				cc.hostname, cc.port, len(buf))
			break
		}
		mid := binary.LittleEndian.Uint16(buf[smbMidOff:])
		pid := binary.LittleEndian.Uint16(buf[smbPidOff:])

		if pid == 3 {
			// SMB_ECHO is always transferred on PID 3
			cc.sys.smbTrace("%s:%d got echo reply", cc.hostname, cc.port)
			cc.sys.mu.Lock()
			cc.waitForPing = false
			cc.sys.mu.Unlock()
		}

		// We run all requests on PID 2 — drop anything else
		if pid != 2 {
			continue
		}

		cc.sys.mu.Lock()

		var nr *nbtReq
		for _, r := range cc.pendingNBT {
			if r.mid == mid {
				nr = r
				break
			}
		}

		if nr != nil {
			cc.sys.smbTrace("%s:%d Got response for mid=%d (err:0x%08x len:%d)",
				cc.hostname, cc.port, mid,
				binary.LittleEndian.Uint32(buf[smbErrorcodeOff:]), len(buf))

			badTrans2 := false
			if nr.isTrans2 &&
				binary.LittleEndian.Uint32(buf[smbErrorcodeOff:]) == 0 &&
				len(buf) >= trans2ReplyLen {

				// TRANS2 reassembly (C: reassembly in smb_dispatch)
				totalCount := int(binary.LittleEndian.Uint16(buf[35:]))
				segCount := int(binary.LittleEndian.Uint16(buf[39:])) +
					int(binary.LittleEndian.Uint16(buf[45:]))

				if segCount > len(buf)-trans2ReplyLen {
					cc.sys.ts.Trace(trace.TRACE_ERROR, "SMB",
						"%s:%d malformed trans2, %d > %d",
						cc.hostname, cc.port, segCount,
						len(buf)-trans2ReplyLen)
					badTrans2 = true
				} else {
					nr.dataCount += int(binary.LittleEndian.Uint16(buf[45:]))

					if nr.response == nil {
						// Params must all arrive in the first packet
						if binary.LittleEndian.Uint16(buf[33:]) !=
							binary.LittleEndian.Uint16(buf[39:]) {
							cc.sys.ts.Trace(trace.TRACE_ERROR, "SMB",
								"%s:%d Unable to reassemble trans2, param count err:%d,%d",
								cc.hostname, cc.port,
								binary.LittleEndian.Uint16(buf[33:]),
								binary.LittleEndian.Uint16(buf[39:]))
							badTrans2 = true
						} else {
							nr.response = buf
						}
					} else {
						// C: payload = buf + tr->param_offset; memcpy
						// seg_count — unbounded in C; bounds-checked
						// here, over-long offsets → bad_trans2.
						poff := int(binary.LittleEndian.Uint16(buf[41:]))
						if poff > len(buf) || segCount > len(buf)-poff {
							badTrans2 = true
						} else {
							nr.response = append(nr.response,
								buf[poff:poff+segCount]...)
						}
					}

					if !badTrans2 && nr.dataCount < totalCount {
						cc.sys.mu.Unlock()
						continue // not complete yet
					}
				}
			} else {
				nr.response = buf
			}

			if badTrans2 {
				nr.result = 1
				nr.response = nil
			} else {
				nr.result = 0
			}
			cc.cond.Broadcast()
		} else {
			cc.sys.smbTrace("%s:%d unexpected response pid=%d mid=%d on %p",
				cc.hostname, cc.port,
				binary.LittleEndian.Uint16(buf[smbPidOff:]), mid, cc)
			dumpRequestList(cc)
		}
		cc.sys.mu.Unlock()
	}

	// Connection died — fail all pending requests
	cc.sys.mu.Lock()
	for _, nr := range cc.pendingNBT {
		nr.result = 1
		nr.response = nil
	}
	cc.cond.Broadcast()
	cc.sys.mu.Unlock()
}

// getTreeNoCreate — C: get_tree_no_create (fa_nativesmb.c:1009).
// Returns the tree with mutex held and ct refcount++ on success;
// returns nil with mutex unlocked when no connection/tree exists.
func (sys *System) getTreeNoCreate(hostname string, port int, share string) *cifsTree {
	sys.mu.Lock()

	var cc *cifsConnection
	for _, c := range sys.connections {
		if c.hostname == hostname && c.port == port &&
			!c.broken && c.status < ccError {
			cc = c
			break
		}
	}
	if cc == nil {
		sys.mu.Unlock()
		return nil
	}
	var ct *cifsTree
	for _, t := range cc.trees {
		if t.share == share {
			ct = t
			break
		}
	}
	if ct != nil {
		ct.refcount++
	} else {
		sys.mu.Unlock()
	}
	return ct
}

// cifsGetConnection — C: cifs_get_connection (fa_nativesmb.c:1044).
// Returns the connection with sys.mu held and refcount held;
// sambaNeedAuthConn on the -2 path, nil on error.
func (sys *System) cifsGetConnection(hostname string, port int, flags int) (*cifsConnection, error) {
	sys.mu.Lock()

	var cc *cifsConnection
	for _, c := range sys.connections {
		if c.hostname == hostname && c.port == port &&
			c.flags == flags && !c.broken && c.status < ccError {
			cc = c
			break
		}
	}

	if cc == nil {
		cc = &cifsConnection{
			sys:      sys,
			uid:      1,
			refcount: 1,
			status:   ccConnecting,
			port:     port,
			hostname: hostname,
			flags:    flags,
		}
		cc.cond = sync.NewCond(&sys.mu)

		sys.connections = slices.Insert(sys.connections, 0, cc)
		sys.mu.Unlock()

		tc, cerr := tcpcon.TCPConnect(hostname, port, 3000, 0, nil)
		cc.err = cerr
		cc.tc = tc

		sys.mu.Lock()

		if cc.tc == nil {
			sys.smbTrace("Unable to connect to %s:%d - %s",
				hostname, port, cc.err)
			cc.status = ccError
		} else {
			sys.smbTrace("Connected to %s:%d", hostname, port)

			if cerr := smbNegProto(cc); cerr != nil {
				cc.err = cerr
				cc.status = ccError
			} else {
				sys.smbTrace("%s:%d Protocol negotiated", hostname, port)

				r, cerr := smbSetupAndX(cc, flags)
				if cerr != nil {
					cc.err = cerr
				}
				if r != 0 {
					if r == -2 {
						cifsReleaseConnection(cc)
						return sambaNeedAuthConn, nil
					}
					cc.status = ccError
				} else {
					sys.smbTrace("%s:%d Session setup", hostname, port)
					cc.status = ccRunning
					cc.threadDone = make(chan struct{})
					go smbDispatch(cc)
					if cs := sys.cs; cs != nil {
						cs.Arm(&cc.timer, sys.cifsPeriodic, cc, smbEchoInterval)
					}
				}
			}
		}
		cc.cond.Broadcast()
	} else {
		cc.refcount++
		for cc.status == ccConnecting {
			cc.cond.Wait()
		}
	}

	if cc.status == ccError {
		err := cc.err
		if err == nil {
			err = errors.New("Connection failed")
		}
		cifsReleaseConnection(cc)
		return nil, err
	}
	cc.autoClose = 0
	return cc, nil
}

// cifsReleaseTree — C: cifs_release_tree (fa_nativesmb.c:1206).
// Caller holds cc.sys.mu; returns with it unlocked.
func cifsReleaseTree(ct *cifsTree, full bool) {
	if ct.cc.flags&ccFNonInteractive != 0 {
		full = true
	}
	ct.cc.autoClose = 0
	ct.refcount--
	if ct.refcount > 0 || !full {
		ct.sys.mu.Unlock()
		return
	}
	for i, t := range ct.cc.trees {
		if t == ct {
			ct.cc.trees = slices.Delete(ct.cc.trees, i, i+1)
			break
		}
	}
	cifsReleaseConnection(ct.cc)
}

// cifsDisconnect — C: cifs_disconnect (fa_nativesmb.c:1228).
// Caller holds ct.sys.mu; returns with it unlocked.
func cifsDisconnect(cc *cifsConnection) {
	for len(cc.trees) > 0 {
		ct := cc.trees[0]
		if ct.refcount != 0 {
			cc.sys.mu.Unlock()
			return
		}
		cc.trees = cc.trees[1:]
		cc.refcount--
	}
	cifsMaybeDestroy(cc)
}

// smbTreeConnectAndX — C: smb_tree_connect_andX (fa_nativesmb.c:1252).
// Caller holds cc.sys.mu; returns ct with mutex held, or nil /
// sambaNeedAuthTree.
func smbTreeConnectAndX(cc *cifsConnection, share string) (*cifsTree, error) {
	service := "?????"
	var password [24]byte

	var retryReason string
	cc.autoClose = 0

	nonInteractive := cc.flags&ccFNonInteractive != 0

	var ct *cifsTree
	if !nonInteractive {
		for _, t := range cc.trees {
			if t.share == share {
				ct = t
				ct.refcount++
				cc.refcount--
				break
			}
		}
	}

	if ct == nil {
		var resource [256]byte
		cstrSet(resource[:], "\\\\%s\\%s", "127.0.0.1", share)
		resStr := misc.CStr(resource[:])

	again:
		for {
			password[0] = 0
			passwordLen := 1

			if cc.securityMode&securityUserLevel == 0 {
				var id, name [256]byte
				cstrSet(id[:], "smb:share:%s:%d:share", cc.hostname, cc.port)
				cstrSet(name[:], "Samba share '\\%s' on '%s'", share, cc.hostname)

				if nonInteractive && retryReason != "" {
					if ct != nil {
						cifsReleaseTree(ct, true)
					}
					return sambaNeedAuthTree, nil
				}
				var pw string = krUnset
				r := 1
				if cc.sys.kr != nil {
					kf := 0x2 | 0x4 // SHOW_REMEMBER_ME | REMEMBER_ME_SET
					if retryReason != "" {
						kf |= 0x1 // QUERY_USER
					}
					r = cc.sys.kr(misc.CStr(id[:]),
						nil, &pw, nil, nil,
						misc.CStr(name[:]), retryReason, kf)
				}
				if r == -1 {
					if ct != nil {
						ct.err = errors.New(
							"Authentication rejected by user")
						ct.status = ctError
						ct.cond.Broadcast()
						break again // C: goto out
					}
					// C NULL-derefs ct here (latent bug); deliver
					// the error directly instead.
					return nil, errors.New("Authentication rejected by user")
				}
				if r == 0 && pw != krUnset {
					digest := ntlmHash(pw)
					lmresponse(password[:], digest[:], cc.challengeKey[:])
					passwordLen = 24
				}
			}

			passwordPad := 0
			if cc.unicode && passwordLen&1 == 0 {
				passwordPad = 1
			}

			resourceLen := cc.utf8ToSMB(nil, resStr)
			serviceLen := len(service) + 1
			bytecount := passwordLen + passwordPad + resourceLen + serviceLen
			tlen := smbTreeConnectAndXReqLen + bytecount

			req := make([]byte, tlen)
			smbSetupHeader(cc, req[4:], smbTreecAndx,
				smbFlagsCaselessPathnames, smbFlags232BitStatus, 0, true)
			req[36] = 4    // wordcount
			req[37] = 0xff // andx_command
			binary.LittleEndian.PutUint16(req[43:], uint16(passwordLen))
			binary.LittleEndian.PutUint16(req[45:], uint16(bytecount))

			ptr := smbTreeConnectAndXReqLen
			copy(req[ptr:], password[:passwordLen])
			ptr += passwordLen + passwordPad
			ptr += cc.utf8ToSMB(req[ptr:], resStr)
			copy(req[ptr:], service)
			ptr += serviceLen

			ct = &cifsTree{sys: cc.sys, cc: cc, share: share, refcount: 1, status: ctConnecting}
			ct.cond = sync.NewCond(&cc.sys.mu)
			cc.trees = slices.Insert(cc.trees, 0, ct)

			rbuf, result := nbtAsyncReqReply(cc, req, false, "treeconnect")
			if result != 0 {
				ct.status = ctError
				ct.err = errors.New("Connection lost")
			} else {
				err := binary.LittleEndian.Uint32(rbuf[smbErrorcodeOff:])
				cc.sys.smbTrace("Tree connect errorcode:0x%08x (%s)", err, share)
				if err != 0 {
					if cc.securityMode&securityUserLevel == 0 && retryReason == "" {
						// C: goto again — the failed CONNECTING
						// tree stays in cc_trees (C leak).
						retryReason = "Authentication failed"
						continue
					}
					ct.status = ctError
					ct.err = cc.sys.smbError(err)
				} else {
					ct.tid = int(binary.LittleEndian.Uint16(rbuf[smbTidOff:]))
					ct.status = ctRunning
				}
			}
			ct.cond.Broadcast()
			break
		}
	}

	if ct == nil {
		// non-interactive bail already returned; unreachable
		return nil, nil
	}
	for ct.status == ctConnecting {
		ct.cond.Wait()
	}
	// C: out:
	if ct.status == ctError {
		err := ct.err
		if err == nil {
			err = errors.New("Tree connect failed")
		}
		cifsReleaseTree(ct, true)
		return nil, err
	}
	return ct, nil
}

// cifsResolve — C: cifs_resolve (fa_nativesmb.c:1420). Splits the URL
// into hostname/share/filename, finds-or-creates the connection and
// tree. Returns with cc.sys.mu held on TREE/CONNECTION results.
func (sys *System) cifsResolve(url string, filename []byte, faFlags int,
	pCT **cifsTree, pCC **cifsConnection, needFile bool) (int, error) {

	var hostname [128]byte
	var path [512]byte
	port := -1

	flags := 0
	if faFlags&facore.FaNonInteractive != 0 {
		flags = ccFNonInteractive
	}

	misc.UrlSplit(nil, 0, nil, 0, hostname[:], len(hostname), &port,
		path[:], len(path), url)

	p := misc.CStr(path[:])
	if strings.HasPrefix(p, "/") {
		p = p[1:]
	}
	fn := ""
	if i := strings.IndexByte(p, '/'); i >= 0 {
		fn = p[i+1:]
		p = p[:i]
	}
	if port < 0 {
		port = 445
	}
	host := misc.CStr(hostname[:])

	if p == "" {
		if pCC == nil {
			return cifsResolveError, errors.New("Invalid URL for operation")
		}
		if cc, _ := sys.cifsGetConnection(host, port, flags|ccFAsGuest); cc != nil &&
			cc != sambaNeedAuthConn {
			*pCC = cc
			return cifsResolveConnection, nil
		}
		cc, err := sys.cifsGetConnection(host, port, flags)
		if cc == sambaNeedAuthConn {
			return cifsResolveNeedAuth, nil
		}
		if cc != nil {
			*pCC = cc
			return cifsResolveConnection, nil
		}
		return cifsResolveError, err
	}

	cstrSet(filename, "%s", fn)

	if needFile && len(fn) == 0 {
		return cifsResolveError, errors.New("Invalid URL for operation")
	}

	ct := sys.getTreeNoCreate(host, port, p)
	if ct != nil {
		for ct.status == ctConnecting {
			ct.cond.Wait()
		}
		if ct.status == ctRunning {
			*pCT = ct
			return cifsResolveTree, nil
		}
		cifsReleaseTree(ct, false)
	}

	var err error
	cc, err := sys.cifsGetConnection(host, port, flags|ccFAsGuest)
	if cc != nil && cc != sambaNeedAuthConn {
		securityMode := cc.securityMode
		ct, err = smbTreeConnectAndX(cc, p)
		if securityMode&securityUserLevel == 0 || ct != nil {
			if ct == sambaNeedAuthTree {
				return cifsResolveNeedAuth, nil
			}
			if ct == nil {
				return cifsResolveError, err
			}
			*pCT = ct
			return cifsResolveTree, nil
		}
	}

	cc, err = sys.cifsGetConnection(host, port, flags)
	if cc == nil {
		return cifsResolveError, err
	}
	if cc == sambaNeedAuthConn {
		return cifsResolveNeedAuth, nil
	}
	ct, err = smbTreeConnectAndX(cc, p)
	if ct == sambaNeedAuthTree {
		// C stores the sentinel in *p_ct and returns TREE here (a
		// latent bug); map it to NEED_AUTH like the guest block.
		return cifsResolveNeedAuth, nil
	}
	if ct == nil {
		return cifsResolveError, err
	}
	*pCT = ct
	return cifsResolveTree, nil
}

// releaseTreeIOError — C: release_tree_io_error (fa_nativesmb.c:1542)
func releaseTreeIOError(ct *cifsTree) error {
	cifsReleaseTree(ct, true)
	return errors.New("I/O error")
}

// cifsPeriodic — C: cifs_periodic (fa_nativesmb.c:2727). SMB_ECHO
// keepalive: detects a dead connection (no echo reply in one interval)
// and auto-closes after 5 idle ticks.
func (sys *System) cifsPeriodic(c *callout.Callout, opaque any) {
	cc := opaque.(*cifsConnection)

	req := make([]byte, echoRequestLen+2)

	sys.mu.Lock()

	smbSetupHeader(cc, req[4:], smbEcho, 0, 0, 0, true)
	req[36] = 1                                // wordcount
	binary.LittleEndian.PutUint16(req[37:], 1) // echo_count
	binary.LittleEndian.PutUint16(req[39:], 2) // byte_count
	req[41] = 0x13
	req[42] = 0x37

	binary.LittleEndian.PutUint16(req[4+smbPidOff:], 3) // PING
	cc.nbtWrite(req)

	if cc.waitForPing {
		cc.broken = true
		sys.smbTrace("%s:%d no ping response", cc.hostname, cc.port)
	}
	cc.waitForPing = true

	if cs := sys.cs; cs != nil {
		cs.Arm(&cc.timer, sys.cifsPeriodic, cc, smbEchoInterval)
	}
	cc.autoClose++
	if cc.autoClose > 5 {
		cifsDisconnect(cc)
	} else {
		sys.mu.Unlock()
	}
}
