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
	"io"
	"slices"
	"time"

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
)

// cifsDelete — C: cifs_delete (fa_nativesmb.c:1961)
func (sys *System) cifsDelete(url string, dir bool) error {
	var filename [512]byte
	var ct *cifsTree
	var cc *cifsConnection

	r, rerr := sys.cifsResolve(url, filename[:], 0, &ct, &cc, true)
	if r == cifsResolveNeedAuth {
		return facore.ErrAuthRequired
	}
	if r != cifsResolveTree {
		return rerr
	}
	fname := backslashify(misc.CStr(filename[:]))
	cc = ct.cc

	plen := cc.utf8ToSMB(nil, fname)
	var req []byte
	if dir {
		tlen := smbDeleteDirReqLen + plen
		req = make([]byte, tlen)
		smbSetupHeader(cc, req[4:], smbDeleteDir,
			smbFlagsCanonicalPathnames, 0, uint16(ct.tid), true)
		cc.utf8ToSMB(req[40:], fname)
		binary.LittleEndian.PutUint16(req[37:], uint16(plen)) // byte_count
	} else {
		tlen := smbDeleteFileReqLen + plen
		req = make([]byte, tlen)
		smbSetupHeader(cc, req[4:], smbDeleteFile,
			smbFlagsCanonicalPathnames, 0, uint16(ct.tid), true)
		req[36] = 1 // word_count
		req[41] = 4 // buffer_format
		cc.utf8ToSMB(req[42:], fname)
		binary.LittleEndian.PutUint16(req[39:], uint16(plen)) // byte_count
	}

	rbuf, result := nbtAsyncReqReply(cc, req, false, "delete")
	if result != 0 {
		cifsReleaseTree(ct, true)
		return errors.New("I/O error")
	}
	if err := checkSMBError(ct, rbuf, smbHdrLen); err != nil {
		return err
	}
	cifsReleaseTree(ct, false)
	return nil
}

// cifsStat — C: cifs_stat (fa_nativesmb.c:2025). Caller holds
// sys.mu; releases the tree on error only.
func cifsStat(ct *cifsTree, filename string, fs *facore.FileStat) error {
	fname := backslashify(filename)
	plen := ct.cc.utf8ToSMB(nil, fname)
	tlen := smbTrans2PathQueryReqLen + plen

	for i := range 2 {
		req := make([]byte, tlen)
		smbSetupT2Header(ct.cc, req, trans2QueryPathInformation,
			6+plen, 0, uint16(ct.tid))
		binary.LittleEndian.PutUint16(req[72:], uint16(0x101+i))
		ct.cc.utf8ToSMB(req[78:], fname)

		rbuf, result := nbtAsyncReqReply(ct.cc, req, true, "stat")
		if result != 0 {
			return releaseTreeIOError(ct)
		}
		if err := checkSMBError(ct, rbuf, trans2ReplyLen); err != nil {
			return err
		}
		dataOff := int(binary.LittleEndian.Uint16(rbuf[47:]))
		if i == 0 {
			if dataOff+36 > len(rbuf) {
				cifsReleaseTree(ct, true)
				return errors.New("Short packet")
			}
			bfi := rbuf[dataOff:]
			fa := binary.LittleEndian.Uint32(bfi[32:])
			fs.MTime = parsetime(int64(binary.LittleEndian.Uint64(bfi[24:])))
			if fa&0x10 != 0 {
				fs.Type = facore.ContentDir
				fs.Size = 0
				i = 1 // skip StandardFileInfo query
			} else {
				fs.Type = facore.ContentFile
			}
		} else {
			if dataOff+22 > len(rbuf) {
				cifsReleaseTree(ct, true)
				return errors.New("Short packet")
			}
			fs.Size = int64(binary.LittleEndian.Uint64(rbuf[dataOff+8:]))
		}
	}
	return nil
}

// cifsScandir — C: cifs_scandir (fa_nativesmb.c:2081). Caller holds
// ct.sys.mu; releases the tree on error only.
func cifsScandir(ct *cifsTree, path string, fd *facore.Dir) error {
	searchID := -1
	searchCount := 100

	fname := fmt.Sprintf("%s/*", path)
	fname = backslashify(fname)
	plen := ct.cc.utf8ToSMB(nil, fname)
	tlen := smbTrans2FindReqLen + plen

	url := fmt.Sprintf("smb://%s", ct.cc.hostname)
	if ct.cc.port != 445 {
		url += fmt.Sprintf(":%d", ct.cc.port)
	}
	suffix := "/"
	if path == "" {
		suffix = ""
	}
	url += fmt.Sprintf("/%s/%s%s", ct.share, path, suffix)
	urlbase := url

	for {
		var req []byte
		if searchID == -1 {
			req = make([]byte, tlen)
			smbSetupT2Header(ct.cc, req, trans2FindFirst2, 12+plen, 0,
				uint16(ct.tid))
			binary.LittleEndian.PutUint16(req[72:], attrReadonly|attrDirectory|attrArchive)
			binary.LittleEndian.PutUint16(req[74:], uint16(searchCount))
			binary.LittleEndian.PutUint16(req[76:], 6)
			binary.LittleEndian.PutUint16(req[78:], 260)
			ct.cc.utf8ToSMB(req[84:], fname)
		} else {
			req = make([]byte, smbTrans2FindReqLen+1)
			smbSetupT2Header(ct.cc, req, trans2FindNext2, 12+1, 0,
				uint16(ct.tid))
			binary.LittleEndian.PutUint16(req[72:], uint16(searchID))
			binary.LittleEndian.PutUint16(req[74:], uint16(searchCount))
			binary.LittleEndian.PutUint16(req[76:], 260) // level_of_interest
			binary.LittleEndian.PutUint16(req[82:], 0xe) // flags
			req[84] = 0
		}

		rbuf, result := nbtAsyncReqReply(ct.cc, req, true, "scandir")
		if result != 0 {
			return releaseTreeIOError(ct)
		}
		if err := checkSMBError(ct, rbuf, trans2ReplyLen); err != nil {
			return err
		}

		poff := int(binary.LittleEndian.Uint16(rbuf[41:]))
		var respparam []byte
		if searchID == -1 {
			if poff+6 > len(rbuf) {
				cifsReleaseTree(ct, true)
				return errors.New("Short packet")
			}
			respparam = rbuf[poff:]
			searchID = int(binary.LittleEndian.Uint16(respparam[0:]))
		} else {
			if poff < 2 {
				// C: free(rbuf); return -1 — no tree release
				return errors.New("Short packet")
			}
			if poff+4 > len(rbuf) {
				cifsReleaseTree(ct, true)
				return errors.New("Short packet")
			}
			respparam = rbuf[poff-2:]
		}
		eos := binary.LittleEndian.Uint16(respparam[4:])

		for off := int(binary.LittleEndian.Uint16(rbuf[47:])); off+smbFindDataLen < len(rbuf); {
			data := rbuf[off:]
			var fnameb [512]byte
			misc.Ucs2ToUtf8(fnameb[:], len(fnameb), data[94:],
				int(binary.LittleEndian.Uint32(data[60:])), 1)
			name := misc.CStr(fnameb[:])

			isdir := binary.LittleEndian.Uint32(data[56:]) & 0x10
			typ := facore.ContentFile
			if isdir != 0 {
				typ = facore.ContentDir
			}
			fde := facore.DirAdd(fd, urlbase+name, name, typ)
			if fde != nil {
				fde.Stat.Size = int64(binary.LittleEndian.Uint64(data[40:]))
				fde.Stat.MTime = parsetime(int64(binary.LittleEndian.Uint64(data[32:])))
				fde.StatDone = true
			}
			neo := int(binary.LittleEndian.Uint32(data[0:]))
			if neo == 0 {
				break
			}
			off += neo
		}

		if eos != 0 {
			break
		}
	}
	return nil
}

// SMBScandir — C: smb_scandir (fa_nativesmb.c:2205)
func (sys *System) SMBScandir(fd *facore.Dir, url string, flags int) error {
	var filename [512]byte
	var ct *cifsTree
	var cc *cifsConnection

	r, rerr := sys.cifsResolve(url, filename[:], flags, &ct, &cc, false)
	switch r {
	case cifsResolveNeedAuth:
		return facore.ErrAuthRequired
	case cifsResolveTree:
		if err := cifsScandir(ct, misc.CStr(filename[:]), fd); err != nil {
			return err
		}
		cifsReleaseTree(ct, false)
		return nil
	case cifsResolveConnection:
		return cifsEnumShares(cc, fd)
	default:
		return rerr
	}
}

// smbOpen — C: smb_open (fa_nativesmb.c:2247)
func (sys *System) smbOpen(url string, flags int) (*smbFile, error) {
	var filename [512]byte
	var ct *cifsTree

	r, rerr := sys.cifsResolve(url, filename[:], flags, &ct, nil, true)
	if r == cifsResolveNeedAuth {
		return nil, facore.ErrAuthRequired
	}
	if r != cifsResolveTree {
		return nil, rerr
	}
	cc := ct.cc
	fname := backslashify(misc.CStr(filename[:]))

	plen := cc.utf8ToSMB(nil, fname)
	uc := 0
	if cc.unicode {
		uc = 1
	}
	tlen := smbNTCreateAndXReqLen + plen + uc

	req := make([]byte, tlen)
	smbSetupHeader(cc, req[4:], smbNtCreateAndx,
		smbFlagsCanonicalPathnames, 0, uint16(ct.tid), true)
	req[36] = 24                                     // wordcount
	req[37] = 0xff                                   // andx_command
	binary.LittleEndian.PutUint32(req[52:], 0x20089) // access_mask
	binary.LittleEndian.PutUint32(req[64:], 1)       // file_attributes
	binary.LittleEndian.PutUint32(req[72:], 1)       // create_disposition
	binary.LittleEndian.PutUint32(req[68:], 1)       // share_access
	binary.LittleEndian.PutUint32(req[80:], 2)       // impersonation_level
	req[84] = 3                                      // security_flags
	cc.utf8ToSMB(req[87+uc:], fname)
	binary.LittleEndian.PutUint16(req[42:], uint16(plen-uc-1))
	binary.LittleEndian.PutUint16(req[85:], uint16(plen+uc))

	rbuf, result := nbtAsyncReqReply(cc, req, false, "open")
	if result != 0 {
		cifsReleaseTree(ct, true)
		return nil, errors.New("I/O error")
	}
	if err := checkSMBError(ct, rbuf, smbNTCreateAndXRespLen); err != nil {
		return nil, err
	}

	sf := &smbFile{
		sys:      sys,
		ct:       ct, // transfer of tree reference to smb_file_t
		fid:      binary.LittleEndian.Uint16(rbuf[38:]),
		fileSize: binary.LittleEndian.Uint64(rbuf[88:]),
	}
	sys.mu.Unlock()
	return sf, nil
}

// Close — C: smb_close (fa_nativesmb.c:2318). Sends SMB_CLOSE and
// releases the tree.
func (sf *smbFile) Close() error {
	ct := sf.ct
	sf.sys.mu.Lock()

	req := make([]byte, smbCloseReqLen)
	smbSetupHeader(ct.cc, req[4:], smbClose, smbFlagsCanonicalPathnames, 0,
		uint16(ct.tid), true)
	binary.LittleEndian.PutUint16(req[37:], sf.fid)
	req[36] = 3 // wordcount
	ct.cc.nbtWrite(req)
	cifsReleaseTree(ct, false)
	return nil
}

// Read — C: smb_read (fa_nativesmb.c:2344). Issues pipelined READ_ANDX
// requests (57344-byte chunks) and reassembles in order.
func (sf *smbFile) Read(buf []byte) (int, error) {
	ct := sf.ct
	size := len(buf)

	if sf.pos >= sf.fileSize {
		return 0, io.EOF
	}
	if sf.pos+uint64(size) > sf.fileSize {
		size = int(sf.fileSize - sf.pos)
	}
	if size == 0 {
		return 0, io.EOF
	}

	var reqs []*nbtReq // C: struct nbt_req_list reqs (nr_multi_link)

	req := make([]byte, smbReadAndXReqLen)

	sf.sys.mu.Lock()

	total := 0
	remain := size
	var nr *nbtReq
	for remain > 0 {
		cnt := min(remain,
			// 14 * 4096 is max according to spec
			57344)
		smbSetupHeader(ct.cc, req[4:], smbReadAndx,
			smbFlagsCanonicalPathnames, 0, uint16(ct.tid), true)
		binary.LittleEndian.PutUint16(req[41:], sf.fid)
		pos := sf.pos + uint64(total)
		binary.LittleEndian.PutUint32(req[43:], uint32(pos))
		binary.LittleEndian.PutUint16(req[47:], uint16(cnt&0xffff))
		binary.LittleEndian.PutUint32(req[51:], uint32(cnt>>16))
		req[36] = 12   // wordcount
		req[37] = 0xff // andx_command
		binary.LittleEndian.PutUint32(req[57:], uint32(pos>>32))

		nr = nbtAsyncReq(ct.cc, req, false, "read")
		reqs = slices.Insert(reqs, 0, nr) // LIST_INSERT_HEAD(multi)
		nr.offset = total
		nr.cnt = cnt
		total += cnt
		remain -= cnt
		nr.last = false
	}
	nr.last = true

	// Wait for all requests to complete (C: the nr_result==-1 scan loop)
	for {
		nr = nil
		for _, r := range reqs {
			if r.result == -1 {
				nr = r
				break
			}
		}
		if nr == nil {
			break
		}
		if sf.sys.condWaitTimeout(ct.cc.cond, nbtTimeoutMs) {
			break
		}
	}

	total = 0
	fail := false
	for len(reqs) > 0 {
		nr := reqs[0]
		if nr.result != 0 {
			fail = true
			break
		}
		if nr.response == nil || len(nr.response) < 59 {
			fail = true
			break
		}
		errcode := binary.LittleEndian.Uint32(nr.response[smbErrorcodeOff:])
		if errcode != 0 {
			fail = true
			break
		}
		// drop from pendingNBT too (C: LIST_REMOVE(nr, nr_link))
		for i, r := range ct.cc.pendingNBT {
			if r == nr {
				ct.cc.pendingNBT = slices.Delete(ct.cc.pendingNBT, i, i+1)
				break
			}
		}
		reqs = reqs[1:]

		rcnt := int(binary.LittleEndian.Uint16(nr.response[43:])) +
			int(binary.LittleEndian.Uint32(nr.response[47:]))<<16
		doff := int(binary.LittleEndian.Uint16(nr.response[45:]))
		if doff+rcnt > len(nr.response) {
			rcnt = len(nr.response) - doff
		}
		if nr.offset+rcnt > len(buf) {
			rcnt = len(buf) - nr.offset
		}
		if rcnt > 0 {
			copy(buf[nr.offset:], nr.response[doff:doff+rcnt])
		}
		sf.pos += uint64(rcnt)
		total += rcnt

		if nr.last && rcnt < nr.cnt {
			break
		}
	}
	if fail {
		// C: fail: — unlink and free all remaining reqs
		for _, nr := range reqs {
			for i, r := range ct.cc.pendingNBT {
				if r == nr {
					ct.cc.pendingNBT = slices.Delete(ct.cc.pendingNBT, i, i+1)
					break
				}
			}
		}
		sf.sys.mu.Unlock()
		return 0, errors.New("smb: read error")
	}
	sf.sys.mu.Unlock()
	if total == 0 {
		return 0, io.EOF
	}
	return total, nil
}

// Seek — C: smb_seek (fa_nativesmb.c:2461). Only moves sf_pos.
func (sf *smbFile) Seek(pos int64, whence int) (int64, error) {
	var np int64
	switch whence {
	case io.SeekStart:
		np = pos
	case io.SeekCurrent:
		np = int64(sf.pos) + pos
	case io.SeekEnd:
		np = int64(sf.fileSize) + pos
	default:
		return -1, errors.New("smb: invalid whence")
	}
	if np < 0 {
		return -1, errors.New("smb: negative seek")
	}
	sf.pos = uint64(np)
	return np, nil
}

// Size — C: smb_fsize (fa_nativesmb.c:2491)
func (sf *smbFile) Size() int64 { return int64(sf.fileSize) }

// smbStatFAP — C: smb_stat (fa_nativesmb.c:2501)
func (sys *System) smbStatFAP(fap *facore.FAProtocol, url string,
	flags int) (*facore.FileStat, error) {
	var filename [512]byte
	var ct *cifsTree
	var cc *cifsConnection
	st := &facore.FileStat{}

	r, rerr := sys.cifsResolve(url, filename[:], flags, &ct, &cc, false)
	switch r {
	case cifsResolveNeedAuth:
		return nil, facore.ErrAuthRequired
	case cifsResolveTree:
		if err := cifsStat(ct, misc.CStr(filename[:]), st); err != nil {
			return nil, err
		}
		cifsReleaseTree(ct, false)
		return st, nil
	case cifsResolveConnection:
		st.Type = facore.ContentShare
		st.Size = 0
		st.MTime = time.Unix(0, 0)
		cc.refcount--
		sys.mu.Unlock()
		return st, nil
	default:
		return nil, rerr
	}
}

// smbUnlinkFAP — C: smb_unlink (fa_nativesmb.c:2540)
func (sys *System) smbUnlinkFAP(fap *facore.FAProtocol, url string) error {
	return sys.cifsDelete(url, false)
}

// smbRmdirFAP — C: smb_rmdir (fa_nativesmb.c:2550)
func (sys *System) smbRmdirFAP(fap *facore.FAProtocol, url string) error {
	return sys.cifsDelete(url, true)
}

// smbSetXattrFAP — C: smb_set_xattr (fa_nativesmb.c:2572)
func (sys *System) smbSetXattrFAP(fap *facore.FAProtocol, url, name string, data []byte) int {
	var filename [512]byte
	var ct *cifsTree
	nameLen := len(name)

	dataLen := len(data)

	r, _ := sys.cifsResolve(url, filename[:], facore.FaNonInteractive, &ct, nil, true)
	if r != cifsResolveTree {
		return -1
	}
	fname := backslashify(misc.CStr(filename[:]))

	plen := ct.cc.utf8ToSMB(nil, fname)
	dlen := eaHdrLen + nameLen + 1 + dataLen
	tlen := smbTrans2PathQueryReqLen + plen + dlen

	req := make([]byte, tlen)
	smbSetupT2Header(ct.cc, req, trans2SetPathInformation,
		6+plen, dlen, uint16(ct.tid))
	binary.LittleEndian.PutUint16(req[72:], 2) // SMB_SET_FILE_EA
	ct.cc.utf8ToSMB(req[78:], fname)

	ea := 78 + plen
	binary.LittleEndian.PutUint32(req[ea:], uint32(dlen)) // list_len
	req[ea+5] = byte(nameLen)
	binary.LittleEndian.PutUint16(req[ea+6:], uint16(dataLen))
	copy(req[ea+8:], name)
	if data != nil {
		copy(req[ea+8+nameLen+1:], data)
	}

	rbuf, result := nbtAsyncReqReply(ct.cc, req, true, "setxattr")
	if result != 0 {
		releaseTreeIOError(ct)
		return -1
	}
	errcode := binary.LittleEndian.Uint32(rbuf[smbErrorcodeOff:])
	cifsReleaseTree(ct, false)
	switch errcode {
	case 0:
		return facore.FAP_OK
	case 0xC0000022:
		return facore.FAP_PERMISSION_DENIED
	case 0xC000004F, 0x03E20001:
		return facore.FAP_NOT_SUPPORTED
	default:
		return facore.FAP_ERROR
	}
}

// smbGetXattrFAP — C: smb_get_xattr (fa_nativesmb.c:2649)
func (sys *System) smbGetXattrFAP(fap *facore.FAProtocol, url, name string) ([]byte, int) {
	var filename [512]byte
	var ct *cifsTree
	nameLen := len(name)

	r, _ := sys.cifsResolve(url, filename[:], facore.FaNonInteractive, &ct, nil, true)
	if r != cifsResolveTree {
		return nil, -1
	}
	fname := backslashify(misc.CStr(filename[:]))

	plen := ct.cc.utf8ToSMB(nil, fname)
	dlen := getEaHdrLen + nameLen + 1
	tlen := smbTrans2PathQueryReqLen + plen + dlen

	req := make([]byte, tlen)
	smbSetupT2Header(ct.cc, req, trans2QueryPathInformation,
		6+plen, dlen, uint16(ct.tid))
	binary.LittleEndian.PutUint16(req[72:], 3) // SMB_INFO_QUERY_EAS_FROM_LIST
	ct.cc.utf8ToSMB(req[78:], fname)

	ea := 78 + plen
	binary.LittleEndian.PutUint32(req[ea:], uint32(dlen)) // list_len
	req[ea+4] = byte(nameLen)
	copy(req[ea+5:], name)

	rbuf, result := nbtAsyncReqReply(ct.cc, req, true, "getxattr")
	if result != 0 {
		releaseTreeIOError(ct)
		return nil, -1
	}
	errcode := binary.LittleEndian.Uint32(rbuf[smbErrorcodeOff:])
	var out []byte
	retcode := facore.FAP_OK
	if errcode != 0 {
		retcode = facore.FAP_ERROR
	} else {
		offset := int(binary.LittleEndian.Uint16(rbuf[47:])) // data_offset
		dlen2 := int(binary.LittleEndian.Uint16(rbuf[45:]))  // data_count
		if dlen2 < eaHdrLen || offset+eaHdrLen > len(rbuf) {
			retcode = facore.FAP_ERROR
		} else {
			d := rbuf[offset:]
			vlen := int(binary.LittleEndian.Uint16(d[6:]))
			voff := 8 + int(d[5]) + 1
			if vlen > 0 {
				if offset+voff+vlen > len(rbuf) {
					vlen = len(rbuf) - offset - voff
				}
				if vlen > 0 {
					out = make([]byte, vlen)
					copy(out, d[voff:voff+vlen])
				}
			}
		}
	}
	cifsReleaseTree(ct, false)
	return out, retcode
}

// smbNoParking — C: smb_no_parking (fa_nativesmb.c:2915)
func smbNoParking(fh *facore.Handle) bool { return true }

// smbOpenGo — C: fap_open = smb_open adapted to the Protocol shape;
// returns the raw smb_file_t for core to wrap in a Handle.
func (sys *System) smbOpenGo(url string, extra *facore.OpenExtra) (facore.SMBFile, error) {
	flags := 0
	if extra != nil {
		flags = extra.Flags
	}
	return sys.smbOpen(url, flags)
}

func (sys *System) smbScandirGo(url string, flags int) (*facore.Dir, error) {
	fd := facore.DirAlloc()
	if err := sys.SMBScandir(fd, url, flags); err != nil {
		return nil, err
	}
	return fd, nil
}

func (sys *System) smbStatGo(url string) (*facore.FileStat, error) {
	return sys.smbStatFAP(nil, url, 0)
}

func (f smbFAP) Open(url string, extra *facore.OpenExtra) (facore.SMBFile, error) {
	return f.sys.smbOpenGo(url, extra)
}

func (f smbFAP) Scandir(url string, flags int) (*facore.Dir, error) {
	return f.sys.smbScandirGo(url, flags)
}

func (f smbFAP) Stat(url string) (*facore.FileStat, error) { return f.sys.smbStatGo(url) }

func (f smbFAP) StatFAP(fap *facore.FAProtocol, url string, flags int) (*facore.FileStat, error) {
	return f.sys.smbStatFAP(fap, url, flags)
}

func (f smbFAP) Unlink(fap *facore.FAProtocol, url string) error {
	return f.sys.smbUnlinkFAP(fap, url)
}

func (f smbFAP) Rmdir(fap *facore.FAProtocol, url string) error {
	return f.sys.smbRmdirFAP(fap, url)
}

func (f smbFAP) SetXattr(fap *facore.FAProtocol, url, name string, data []byte) int {
	return f.sys.smbSetXattrFAP(fap, url, name, data)
}

func (f smbFAP) GetXattr(fap *facore.FAProtocol, url, name string) ([]byte, int) {
	return f.sys.smbGetXattrFAP(fap, url, name)
}
