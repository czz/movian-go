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

	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
)

// closeSrvsvc — C: close_srvsvc (fa_nativesmb.c:1604). Caller holds
// sys.mu.
func closeSrvsvc(ct *cifsTree, fid int) {
	req := make([]byte, smbCloseReqLen)
	smbSetupHeader(ct.cc, req[4:], smbClose, smbFlagsCanonicalPathnames, 0,
		uint16(ct.tid), true)
	binary.LittleEndian.PutUint16(req[37:], uint16(fid))
	req[36] = 3 // wordcount
	ct.cc.nbtWrite(req)
}

// openSrvsvc — C: open_srvsvc (fa_nativesmb.c:1621). Caller holds
// ct.sys.mu; returns with it held on success.
func openSrvsvc(ct *cifsTree) (int, error) {
	cc := ct.cc
	filename := "\\srvsvc"

	plen := cc.utf8ToSMB(nil, filename)
	uc := 0
	if cc.unicode {
		uc = 1
	}
	tlen := smbNTCreateAndXReqLen + plen + uc

	req := make([]byte, tlen)
	smbSetupHeader(cc, req[4:], smbNtCreateAndx,
		smbFlagsCanonicalPathnames|smbFlagsCaselessPathnames, 0,
		uint16(ct.tid), true)
	req[36] = 24                                     // wordcount
	req[37] = 0xff                                   // andx_command
	binary.LittleEndian.PutUint32(req[52:], 0x2019f) // access_mask
	binary.LittleEndian.PutUint32(req[64:], 0)       // file_attributes
	binary.LittleEndian.PutUint32(req[72:], 1)       // create_disposition
	binary.LittleEndian.PutUint32(req[68:], 3)       // share_access
	binary.LittleEndian.PutUint32(req[80:], 2)       // impersonation_level
	req[84] = 0                                      // security_flags
	cc.utf8ToSMB(req[87+uc:], filename)
	binary.LittleEndian.PutUint16(req[42:], uint16(plen-uc-1)) // name_len
	binary.LittleEndian.PutUint16(req[85:], uint16(plen+uc))   // byte_count

	rbuf, result := nbtAsyncReqReply(cc, req, false, "opensrvsvc")
	if result != 0 {
		return -1, errors.New("I/O error")
	}
	if err := checkSMBError(ct, rbuf, smbNTCreateAndXRespLen); err != nil {
		return -1, err
	}
	return int(binary.LittleEndian.Uint16(rbuf[38:])), nil // fid
}

// bindArgs — C: bind_args (fa_nativesmb.c:1668)
var bindArgs = []byte{
	0x00, 0x00, 0x01, 0x00, 0xc8, 0x4f, 0x32, 0x4b,
	0x70, 0x16, 0xd3, 0x01, 0x12, 0x78, 0x5a, 0x47,
	0xbf, 0x6e, 0xe1, 0x88, 0x03, 0x00, 0x00, 0x00,
	0x04, 0x5d, 0x88, 0x8a, 0xeb, 0x1c, 0xc9, 0x11,
	0x9f, 0xe8, 0x08, 0x00, 0x2b, 0x10, 0x48, 0x60,
	0x02, 0x00, 0x00, 0x00,
}

// dcerpcBind — C: dcerpc_bind (fa_nativesmb.c:1681). Caller holds
// ct.sys.mu.
func dcerpcBind(ct *cifsTree, fid int) error {
	cc := ct.cc
	tlen := dcerpcBindReqLen + len(bindArgs)
	req := make([]byte, tlen)

	smbSetupHeader(cc, req[4:], smbTransaction,
		smbFlagsCanonicalPathnames|smbFlagsCaselessPathnames, 0,
		uint16(ct.tid), true)

	// TRANS_req_t fields
	req[36] = 16                                           // wordcount
	binary.LittleEndian.PutUint16(req[39:], 72)            // total_data_count
	binary.LittleEndian.PutUint16(req[43:], cc.maxBufSize) // max_data_count
	binary.LittleEndian.PutUint16(req[57:], 84)            // param_offset
	binary.LittleEndian.PutUint16(req[59:], 72)            // data_count
	binary.LittleEndian.PutUint16(req[61:], 84)            // data_offset
	req[63] = 2                                            // setup_count

	// DCERPC_req_t tail
	binary.LittleEndian.PutUint16(req[65:], 0x26)            // function = TransactNmPipe
	binary.LittleEndian.PutUint16(req[67:], uint16(fid))     // fid
	binary.LittleEndian.PutUint16(req[69:], 89)              // byte_count
	copy(req[72:86], "\\\x00P\x00I\x00P\x00E\x00\\\x00\x00") // name[14]

	// DCERPC_hdr_t @ 88
	req[88] = 5                                   // major_version
	req[90] = 0xb                                 // type = Bind
	req[91] = 0x3                                 // flags
	binary.LittleEndian.PutUint32(req[92:], 0x10) // data_representation
	binary.LittleEndian.PutUint16(req[96:], 72)   // frag_length
	binary.LittleEndian.PutUint32(req[100:], 1)   // callid

	// bind tail @ 104
	binary.LittleEndian.PutUint16(req[104:], cc.maxBufSize) // max_xmit_frag
	binary.LittleEndian.PutUint16(req[106:], cc.maxBufSize) // max_recv_frag
	req[112] = 1                                            // num_ctx_items
	copy(req[116:], bindArgs)

	_, result := nbtAsyncReqReply(cc, req, false, "dcerpc_bind")
	if result != 0 {
		return errors.New("I/O error")
	}
	return nil
}

// enumArgs — C: enumargs (fa_nativesmb.c:1735)
var enumArgs = []byte{
	0x01, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00,
	0x04, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff,
	0x08, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00,
}

// parseEnumShares — C: parse_enum_shares (fa_nativesmb.c:1740)
func parseEnumShares(data []byte, cc *cifsConnection, fd *facore.Dir) int {
	if len(data) < 24 {
		return -1
	}
	data = data[20:]
	numShares := int(binary.LittleEndian.Uint32(data))
	data = data[4:]

	if numShares > 256 {
		return -1
	}

	var url [512]byte
	base := fmt.Sprintf("smb://%s", cc.hostname)
	copy(url[:], base)
	n := len(base)
	if cc.port != 445 {
		n += copy(url[n:], fmt.Sprintf(":%d", cc.port))
	}
	urlbase := n

	typeArray := make([]uint32, numShares)
	for i := range numShares {
		if len(data) < 12 {
			return -1
		}
		typeArray[i] = binary.LittleEndian.Uint32(data[4:])
		data = data[12:]
	}

	for i := range numShares {
		if len(data) < 12 {
			return -1
		}
		count := int(binary.LittleEndian.Uint32(data[8:]))
		if count < 1 || count > 64 {
			return -1
		}
		data = data[12:]
		if (count-1)*2 > len(data) {
			return -1
		}
		var sharename [310]byte
		misc.Ucs2ToUtf8(sharename[:], len(sharename), data, (count-1)*2, 1)
		count = (count + 1) &^ 1
		if count*2 > len(data) {
			return -1
		}
		data = data[count*2:]

		if len(data) < 12 {
			return -1
		}
		count = int(binary.LittleEndian.Uint32(data[8:]))
		if count < 1 || count > 100 {
			return -1
		}
		data = data[12:]
		if (count-1)*2 > len(data) {
			return -1
		}
		var comment [310]byte
		misc.Ucs2ToUtf8(comment[:], len(comment), data, (count-1)*2, 1)
		count = (count + 1) &^ 1
		if count*2 > len(data) {
			return -1
		}
		data = data[count*2:]

		name := misc.CStr(sharename[:])
		copy(url[urlbase:], "/"+name)
		full := string(url[:urlbase+1+len(name)])

		cc.sys.smbTrace("Enumerated share %s (%s) -> %s type=%x",
			name, misc.CStr(comment[:]), full, typeArray[i])

		if typeArray[i] == 0 {
			// 0 for DISKTREE shares
			facore.DirAdd(fd, full, name, facore.ContentShare)
		}
	}
	return 0
}

// dcerpcEnumShares — C: dcerpc_enum_shares (fa_nativesmb.c:1830).
// Caller holds sys.mu; releases the tree before returning.
func dcerpcEnumShares(ct *cifsTree, fid int, fd *facore.Dir) error {
	cc := ct.cc
	servername := cc.hostname

	servernamechars := len(servername) + 1
	snlen := cc.utf8ToSMB(nil, servername)
	snlen = (snlen + 3) &^ 3
	arglen := 16 + snlen + 32
	tlen := dcerpcEnumSharesReqLen + arglen
	req := make([]byte, tlen)

	smbSetupHeader(cc, req[4:], smbTransaction,
		smbFlagsCanonicalPathnames|smbFlagsCaselessPathnames, 0,
		uint16(ct.tid), true)

	fragLen := arglen + 24

	req[36] = 16                                             // wordcount
	binary.LittleEndian.PutUint16(req[39:], uint16(fragLen)) // total_data_count
	binary.LittleEndian.PutUint16(req[43:], cc.maxBufSize)   // max_data_count
	binary.LittleEndian.PutUint16(req[57:], 84)              // param_offset
	binary.LittleEndian.PutUint16(req[59:], uint16(fragLen)) // data_count
	binary.LittleEndian.PutUint16(req[61:], 84)              // data_offset
	req[63] = 2                                              // setup_count

	binary.LittleEndian.PutUint16(req[65:], 0x26)               // TransactNmPipe
	binary.LittleEndian.PutUint16(req[67:], uint16(fid))        // fid
	binary.LittleEndian.PutUint16(req[69:], uint16(fragLen+17)) // byte_count
	copy(req[72:86], "\\\x00P\x00I\x00P\x00E\x00\\\x00\x00")

	req[88] = 5                                              // major_version
	req[90] = 0x0                                            // type = Request
	req[91] = 0x3                                            // flags
	binary.LittleEndian.PutUint32(req[92:], 0x10)            // data_representation
	binary.LittleEndian.PutUint16(req[96:], uint16(fragLen)) // frag_length
	binary.LittleEndian.PutUint32(req[100:], 2)              // callid

	// DCERPC_enum_shares_req_t tail @ 104
	binary.LittleEndian.PutUint32(req[104:], 68) // alloc_hint
	// context_id @ 108 = 0
	binary.LittleEndian.PutUint16(req[110:], 15) // opnum = NetShareEnumAll

	p := 112
	binary.LittleEndian.PutUint32(req[p:], 0x20000)
	binary.LittleEndian.PutUint32(req[p+4:], uint32(servernamechars))
	binary.LittleEndian.PutUint32(req[p+12:], uint32(servernamechars))
	p += 16
	cc.utf8ToSMB(req[p:], servername)
	p += snlen
	copy(req[p:], enumArgs)

	bad := func(err error) error {
		closeSrvsvc(ct, fid)
		cifsReleaseTree(ct, false)
		return err
	}

	rbuf, result := nbtAsyncReqReply(cc, req, false, "enumshares")
	if result != 0 {
		return errors.New("I/O error")
	}

	if len(rbuf) < transReplyLen {
		return bad(errors.New("Short packet"))
	}

	dataOffset := int(binary.LittleEndian.Uint16(rbuf[47:]))
	dataLen := len(rbuf) - dataOffset
	if dataOffset > len(rbuf) || dataLen < dcerpcEnumSharesReplyLen {
		return bad(errors.New("Short enumshare reply"))
	}

	if rbuf[dataOffset+2] != 3 { // reply->hdr.flags
		return bad(errors.New("Fragmented enumshare replies not supported"))
	}

	parseEnumShares(rbuf[dataOffset+dcerpcEnumSharesReplyLen:], cc, fd)

	closeSrvsvc(ct, fid)
	cifsReleaseTree(ct, false)
	return nil
}

// cifsEnumShares — C: cifs_enum_shares (fa_nativesmb.c:1937). Caller
// holds ct.sys.mu.
func cifsEnumShares(cc *cifsConnection, fd *facore.Dir) error {
	ct, err := smbTreeConnectAndX(cc, "IPC$")
	if ct == sambaNeedAuthTree {
		return facore.ErrAuthRequired
	}
	if ct == nil {
		return err
	}
	fid, err := openSrvsvc(ct)
	if err != nil {
		return err
	}
	if err := dcerpcBind(ct, fid); err != nil {
		return err
	}
	return dcerpcEnumShares(ct, fid, fd)
}

// smbNetServerEnum2 — C: smb_NetServerEnum2 (fa_nativesmb.c:2769).
// Caller holds sys.mu; releases the tree on error only.
func smbNetServerEnum2(ct *cifsTree) ([]string, error) {
	cc := ct.cc
	domain := cc.primaryDomain
	if domain == "" {
		domain = "WORKGROUP"
	}
	dlen := len(domain) + 1

	tlen := smbEnumServersReqLen + dlen
	req := make([]byte, tlen)

	smbSetupHeader(cc, req[4:], smbTransaction,
		smbFlagsCanonicalPathnames|smbFlagsCaselessPathnames, 0,
		uint16(ct.tid), true)

	req[36] = 14                                             // wordcount
	binary.LittleEndian.PutUint16(req[37:], uint16(26+dlen)) // total_param_count
	binary.LittleEndian.PutUint16(req[41:], 8)               // max_param_count
	binary.LittleEndian.PutUint16(req[43:], 65535)           // max_data_count
	binary.LittleEndian.PutUint16(req[55:], uint16(26+dlen)) // param_count
	binary.LittleEndian.PutUint16(req[57:], 92)              // param_offset
	binary.LittleEndian.PutUint16(req[65:], uint16(55+dlen)) // bytecount
	copy(req[68:94],
		"\\\x00P\x00I\x00P\x00E\x00\\\x00L\x00A\x00N\x00M\x00A\x00N\x00\x00\x00")

	binary.LittleEndian.PutUint16(req[96:], 104)         // NetServerEnum2
	copy(req[98:106], "WrLehDz")                         // parameter_desc
	copy(req[106:114], "B16BBDz")                        // return_desc
	binary.LittleEndian.PutUint16(req[114:], 1)          // detail_level
	binary.LittleEndian.PutUint16(req[116:], 65535)      // receive_buffer_length
	binary.LittleEndian.PutUint32(req[118:], 0xffffffff) // server_type = -1
	copy(req[122:], domain)

	rbuf, result := nbtAsyncReqReply(cc, req, false, "NetServerEnum2")
	if result != 0 {
		return nil, errors.New("I/O error")
	}
	if err := checkSMBError(ct, rbuf, transReplyLen); err != nil {
		return nil, err
	}

	paramOffset := int(binary.LittleEndian.Uint16(rbuf[41:]))
	paramCount := int(binary.LittleEndian.Uint16(rbuf[39:]))

	if paramCount < 8 || paramOffset+8 > len(rbuf) {
		cifsReleaseTree(ct, false)
		return nil, fmt.Errorf("Bad params %d %d", paramOffset, paramCount)
	}
	entries := int(binary.LittleEndian.Uint16(rbuf[paramOffset+6:]))
	if entries > 256 {
		cifsReleaseTree(ct, false)
		return nil, errors.New("Too many servers")
	}

	dataOffset := int(binary.LittleEndian.Uint16(rbuf[47:]))
	items := rbuf[dataOffset:]
	remain := len(rbuf) - dataOffset

	var rvec []string
	var servername [17]byte
	for range entries {
		if remain < 26 {
			break
		}
		copy(servername[:16], items)
		ct.sys.smbTrace("Found server %s", misc.CStr(servername[:]))

		remain -= 26
		items = items[26:]
		rvec = append(rvec, misc.CStr(servername[:16]), domain)
	}
	return rvec, nil
}

// SMBEnumServers — C: smb_enum_servers (fa_nativesmb.c:2865). Returns
// flat name/workgroup string pairs for nmb's master-browser enumeration.
func (sys *System) SMBEnumServers(hostname string) []string {
	port := 445

	cc, err := sys.cifsGetConnection(hostname, port,
		ccFAsGuest|ccFNonInteractive|ccFAnonymous)
	if cc == sambaNeedAuthConn {
		return nil
	}
	if cc == nil {
		sys.smbTrace("Unable to connect to %s:%d for network listings -- %s",
			hostname, port, err)
		return nil
	}

	ct, err := smbTreeConnectAndX(cc, "IPC$")
	if ct == sambaNeedAuthTree || ct == nil {
		return nil
	}
	servers, err := smbNetServerEnum2(ct)
	if servers == nil {
		sys.smbTrace("Failed to enumerate network servers at %s:%d -- %s",
			hostname, port, err)
		return nil
	}
	cifsReleaseTree(ct, true)
	return servers
}
