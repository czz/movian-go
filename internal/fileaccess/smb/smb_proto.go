// Canonical port of src/fileaccess/smb/fa_nativesmb.c — native SMBv1
// (CIFS) client. Architecture: a global pool of cifs_connection_t, each
// owning a tcpcon and a dispatch goroutine that demultiplexes replies by
// MID onto a pending-request list guarded by smb_global_mutex; per-share
// cifs_tree_t with cond-signaled connect; DCE/RPC share enumeration;
// pipelined READ_ANDX; 30s SMB_ECHO keepalive with auto-disconnect.
package smb

import (
	"crypto/des"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/trace"
)

// condWaitTimeout — C: hts_cond_wait_timeout. Caller must hold
// sys.mu (== c.L). Returns true on timeout.
func (sys *System) condWaitTimeout(c *sync.Cond, ms int) bool {
	timedOut := false
	timer := time.AfterFunc(time.Duration(ms)*time.Millisecond, func() {
		sys.mu.Lock()
		timedOut = true
		c.Broadcast()
		sys.mu.Unlock()
	})
	c.Wait()
	timer.Stop()
	return timedOut
}

// nbtReq — C: nbt_req_t (fa_nativesmb.c:69-83)
type nbtReq struct {
	mid       uint16 // nr_mid
	response  []byte // nr_response
	result    int    // nr_result: -1 pending, 0 ok, 1 error
	offset    int    // nr_offset (smb_read multi-request list)
	cnt       int    // nr_cnt
	last      bool   // nr_last
	isTrans2  bool   // nr_is_trans2
	dataCount int    // nr_data_count
}

// cstrSet — C: snprintf(buf, len, ...)
func cstrSet(buf []byte, format string, args ...any) {
	if len(buf) == 0 {
		return
	}
	s := fmt.Sprintf(format, args...)
	if len(s) > len(buf)-1 {
		s = s[:len(buf)-1]
	}
	n := copy(buf, s)
	if n < len(buf) {
		buf[n] = 0
	}
}

// smbError — C: smberr_write (fa_nativesmb.c:183-210)
func (sys *System) smbError(code uint32) error {
	var r string
	switch code {
	case 0xc00000cc:
		r = sys.gettext("Bad network share name")
	case 0xc000006d:
		r = sys.gettext("Logon failure")
	case 0xc000006e:
		r = sys.gettext("Account restricted")
	case 0xc0000022:
		r = sys.gettext("Access denied")
	case 0xc0000034:
		r = sys.gettext("Object name not found")
	default:
		return fmt.Errorf("NTStatus: 0x%08x", code)
	}
	return errors.New(r)
}

// readstring — C: readstring (fa_nativesmb.c:216-251). Reads a UCS2-LE
// NUL-terminated string out of buf at *pp, bounded by *lp bytes;
// advances *pp past the terminator and updates *lp. Returns ok=false
// for C's NULL returns.
func readstring(buf []byte, pp *int, lp *int, unicode bool) (string, bool) {
	if !unicode {
		return "", false
	}
	remain := *lp
	p := *pp
	c := 0
	if remain < 2 {
		return "", false
	}
	var out []byte
	for remain >= 2 {
		if p+1 >= len(buf) {
			break
		}
		if buf[p] == 0 && buf[p+1] == 0 {
			remain -= 2
			break
		}
		var tmp [8]byte
		n := misc.Utf8Put(tmp[:], int(buf[p])|int(buf[p+1])<<8)
		out = append(out, tmp[:n]...)
		p += 2
		remain -= 2
		c++
	}
	*lp = remain
	*pp = p + 2
	return string(out), true
}

// parsetime — C: parsetime (fa_nativesmb.c:261). NT time → unix time.
func parsetime(v int64) time.Time {
	return time.Unix(v/10000000-11644473600, 0)
}

// ntlmHash — C: NTLM_hash (fa_nativesmb.c:268) — MD4 of UCS2 password.
func ntlmHash(password string) [16]byte {
	d := make([]byte, 0, len(password)*2)
	for i := range len(password) {
		d = append(d, password[i], 0)
	}
	return md4Sum(d)
}

// desKeySpread — C: des_key_spread (fa_nativesmb.c:293)
func desKeySpread(normal []byte) [8]byte {
	var spread [8]byte
	spread[0] = normal[0] & 0xfe
	spread[1] = (normal[0]<<7 | normal[1]>>1) & 0xfe
	spread[2] = (normal[1]<<6 | normal[2]>>2) & 0xfe
	spread[3] = (normal[2]<<5 | normal[3]>>3) & 0xfe
	spread[4] = (normal[3]<<4 | normal[4]>>4) & 0xfe
	spread[5] = (normal[4]<<3 | normal[5]>>5) & 0xfe
	spread[6] = (normal[5]<<2 | normal[6]>>6) & 0xfe
	spread[7] = normal[6] << 1
	return spread
}

// lmresponseRound — C: lmresponse_round (fa_nativesmb.c:310) — DES-ECB
// encrypt the 8-byte challenge under the spread 7-byte key.
func lmresponseRound(out []byte, challenge, hash []byte) {
	spread := desKeySpread(hash)
	block, err := des.NewCipher(spread[:])
	if err != nil {
		return
	}
	block.Encrypt(out, challenge)
}

// lmresponse — C: lmresponse (fa_nativesmb.c:341)
func lmresponse(out []byte, hash, challenge []byte) {
	tmp := [7]byte{hash[14], hash[15]}
	lmresponseRound(out[0:], challenge, hash)
	lmresponseRound(out[8:], challenge, hash[7:])
	lmresponseRound(out[16:], challenge, tmp[:])
}

// smbSetupHeader — C: smbv1_init_header (fa_nativesmb.c:355). h points at
// the SMB_t inside a packet (after the 4-byte NBT header for requests).
func smbSetupHeader(cc *cifsConnection, h []byte, cmd int, flags, flags2 int, tid uint16, uc bool) {
	binary.LittleEndian.PutUint32(h[smbProtoOff:], smbProto)
	h[smbCmdOff] = byte(cmd)
	h[smbFlagsOff] = byte(flags)

	flags2 |= smbFlags2KnowsLongNames
	if cc.unicode && uc {
		flags2 |= smbFlags2UnicodeString
	}
	binary.LittleEndian.PutUint16(h[smbFlags2Off:], uint16(flags2))
	binary.LittleEndian.PutUint16(h[smbPidOff:], 1)
	binary.LittleEndian.PutUint16(h[smbMidOff:], 0)
	binary.LittleEndian.PutUint16(h[smbUidOff:], cc.uid)
	binary.LittleEndian.PutUint16(h[smbTidOff:], tid)
}

// smbSetupT2Header — C: smbv1_init_t2_header (fa_nativesmb.c:379). t2
// points at the TRANS2_req_t including its NBT header.
func smbSetupT2Header(cc *cifsConnection, t2 []byte, cmd int, paramCount, dataCount int, tid uint16) {
	smbSetupHeader(cc, t2[4:], smbTrans2, smbFlagsCaselessPathnames, 0, tid, true)

	t2[36] = 15                                                            // wordcount
	binary.LittleEndian.PutUint16(t2[41:], 256)                            // max_param_count
	binary.LittleEndian.PutUint16(t2[43:], cc.maxBufSize)                  // max_data_count
	t2[63] = 1                                                             // setup_count
	binary.LittleEndian.PutUint16(t2[65:], uint16(cmd))                    // sub_cmd
	binary.LittleEndian.PutUint16(t2[57:], 68)                             // param_offset
	binary.LittleEndian.PutUint16(t2[37:], uint16(paramCount))             // total_param_count
	binary.LittleEndian.PutUint16(t2[55:], uint16(paramCount))             // param_count
	binary.LittleEndian.PutUint16(t2[61:], uint16(68+paramCount))          // data_offset
	binary.LittleEndian.PutUint16(t2[59:], uint16(dataCount))              // data_count
	binary.LittleEndian.PutUint16(t2[39:], uint16(dataCount))              // total_data_count
	binary.LittleEndian.PutUint16(t2[67:], uint16(3+paramCount+dataCount)) // byte_count
}

// nbtRead — C: nbt_read (fa_nativesmb.c:407). Reads one NBT session
// message (skipping 0x85 keepalives and zero-length frames), returns the
// payload (no NBT header).
func (cc *cifsConnection) nbtRead() ([]byte, error) {
	var data [4]byte
	length := 0
	for {
		if cc.tc.TCPReadData(data[:], nil, nil) != 0 {
			return nil, errors.New("read error")
		}
		if data[0] == 0x85 {
			continue // keep alive
		}
		if data[0] != 0 {
			return nil, errors.New("bad NBT message type")
		}
		length = int(data[1])<<16 | int(data[2])<<8 | int(data[3])
		if length != 0 {
			break
		}
	}
	buf := make([]byte, length)
	if cc.tc.TCPReadData(buf, nil, nil) != 0 {
		return nil, errors.New("read error")
	}
	return buf, nil
}

// nbtWrite — C: nbt_write (fa_nativesmb.c:442). Fills the 4-byte NBT
// header (msg=0, flags=0, u16be length) and sends.
func (cc *cifsConnection) nbtWrite(buf []byte) int {
	buf[0] = nbtSessionMsg
	buf[1] = 0 // flags
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(buf)-4))
	return cc.tc.TCPWriteData(buf)
}

// utf8ToSMB — C: utf8_to_smb (fa_nativesmb.c:458). Encodes to UCS2-LE
// when unicode, else ASCII. Returns byte count incl. terminator.
func (cc *cifsConnection) utf8ToSMB(dst []byte, src string) int {
	if cc.unicode {
		return misc.Utf8ToUcs2(dst, src, 1)
	}
	return misc.Utf8ToAscii(dst, src)
}

// nbtAsyncReq — C: nbt_async_req (fa_nativesmb.c:1146). Caller holds
// sys.mu.
func nbtAsyncReq(cc *cifsConnection, request []byte, isTrans2 bool, info string) *nbtReq {
	nr := &nbtReq{result: -1}
	nr.mid = cc.midGenerator
	cc.midGenerator++
	nr.isTrans2 = isTrans2
	h := request[4:]
	binary.LittleEndian.PutUint16(h[smbPidOff:], 2)
	binary.LittleEndian.PutUint16(h[smbMidOff:], nr.mid)
	cc.nbtWrite(request)

	cc.pendingNBT = slices.Insert(cc.pendingNBT, 0, nr)
	cc.sys.smbTrace("%s:%d %s sent mid=%d", cc.hostname, cc.port, info, nr.mid)
	return nr
}

// nbtAsyncReqReply — C: nbt_async_req_reply_ex (fa_nativesmb.c:1169).
// Caller holds cc.sys.mu; returns with it held.
func nbtAsyncReqReply(cc *cifsConnection, request []byte,
	isTrans2 bool, info string) ([]byte, int) {

	nr := nbtAsyncReq(cc, request, isTrans2, info)

	for nr.result == -1 {
		if cc.sys.condWaitTimeout(cc.cond, nbtTimeoutMs) {
			cc.sys.ts.Trace(trace.TRACE_ERROR, "SMB",
				"%s:%d request timeout (%d) on %p",
				cc.hostname, cc.port, nr.mid, cc)
			dumpRequestList(cc)
			cc.broken = true
			break
		}
	}
	// C: LIST_REMOVE(nr, nr_link)
	for i, r := range cc.pendingNBT {
		if r == nr {
			cc.pendingNBT = slices.Delete(cc.pendingNBT, i, i+1)
			break
		}
	}
	return nr.response, nr.result
}

// checkSMBError — C: check_smb_error (fa_nativesmb.c:1564). On error
// the tree is released (mutex unlocked) and the error returned.
func checkSMBError(ct *cifsTree, rbuf []byte, runtLim int) error {
	errcode := binary.LittleEndian.Uint32(rbuf[smbErrorcodeOff:])
	if errcode != 0 {
		ct.sys.smbTrace("Error: 0x%08x", errcode)
		cifsReleaseTree(ct, false)
		return fmt.Errorf("SMB Error 0x%08x", errcode)
	}
	if len(rbuf) < runtLim {
		cifsReleaseTree(ct, true)
		return errors.New("Short packet")
	}
	return nil
}

// backslashify — C: backslashify (fa_nativesmb.c:1593)
func backslashify(s string) string {
	return strings.ReplaceAll(s, "/", "\\")
}
