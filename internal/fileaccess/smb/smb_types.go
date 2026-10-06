// Canonical port of src/fileaccess/smb/fa_nativesmb.c — native SMBv1
// (CIFS) client. Architecture: a global pool of cifs_connection_t, each
// owning a tcpcon and a dispatch goroutine that demultiplexes replies by
// MID onto a pending-request list guarded by smb_global_mutex; per-share
// cifs_tree_t with cond-signaled connect; DCE/RPC share enumeration;
// pipelined READ_ANDX; 30s SMB_ECHO keepalive with auto-disconnect.
package smb

import (
	"sync"

	"github.com/czz/movian-go/internal/callout"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/gconf"
	"github.com/czz/movian-go/internal/networking/tcpcon"
	"github.com/czz/movian-go/internal/trace"
)

// C: SAMBA_NEED_AUTH ((void *)-1) — sentinel returned by
// cifs_get_connection / smb_tree_connect_andX when a non-interactive
// context needs credentials.
var sambaNeedAuthConn = &cifsConnection{}

var sambaNeedAuthTree = &cifsTree{}

const smbEchoInterval = 30 // C: SMB_ECHO_INTERVAL (seconds)

const nbtTimeoutMs = 30000 // C: NBT_TIMEOUT

// SMB_t (32 bytes) field offsets
const (
	smbProtoOff     = 0  // u32
	smbCmdOff       = 4  // u8
	smbErrorcodeOff = 5  // u32
	smbFlagsOff     = 9  // u8
	smbFlags2Off    = 10 // u16
	// extra[12] @ 12
	smbTidOff = 24 // u16
	smbPidOff = 26 // u16
	smbUidOff = 28 // u16
	smbMidOff = 30 // u16
	smbHdrLen = 32
)

// C: smbv1.h flag/command constants
const (
	smbFlagsCaselessPathnames  = 0x08
	smbFlagsCanonicalPathnames = 0x10
	smbFlags2KnowsLongNames    = 0x0001
	smbFlags232BitStatus       = 0x4000
	smbFlags2UnicodeString     = 0x8000

	serverCapUnicode = 0x00000004
	serverCapNtSmbs  = 0x00000010

	securityUserLevel = 0x01

	clientCapLargeReadx = 0x00004000
	clientCapStatus32   = 0x00000040
	clientCapNtSmbs     = 0x00000010
	clientCapLargeFiles = 0x00000008
	clientCapUnicode    = 0x00000004

	nbtSessionMsg = 0x00
	smbProto      = 0x424d53ff

	smbDeleteDir    = 0x01
	smbClose        = 0x04
	smbDeleteFile   = 0x06
	smbTransaction  = 0x25
	smbEcho         = 0x2b
	smbReadAndx     = 0x2e
	smbNegProtocol  = 0x72
	smbSetupAndx    = 0x73
	smbTreecAndx    = 0x75
	smbTrans2       = 0x32
	smbNtCreateAndx = 0xa2

	trans2FindFirst2           = 1
	trans2FindNext2            = 2
	trans2QueryPathInformation = 5
	trans2SetPathInformation   = 6

	attrReadonly  = 0x01
	attrDirectory = 0x10
	attrArchive   = 0x20
)

// Packed-struct sizes (C sizeof of the smbv1.h structs)
const (
	smbNegProtoReqLen        = 39  // SMB_NEG_PROTOCOL_req_t
	smbNegProtoReplyLen      = 69  // SMB_NEG_PROTOCOL_reply_t
	smbSetupAndXReqLen       = 65  // SMB_SETUP_ANDX_req_t
	smbSetupAndXReplyLen     = 41  // SMB_SETUP_ANDX_reply_t
	smbTreeConnectAndXReqLen = 47  // SMB_TREE_CONNECT_ANDX_req_t
	transReqLen              = 65  // TRANS_req_t
	dcerpcReqLen             = 104 // DCERPC_req_t
	dcerpcBindReqLen         = 116 // DCERPC_bind_req_t
	dcerpcEnumSharesReqLen   = 112 // DCERPC_enum_shares_req_t
	dcerpcEnumSharesReplyLen = 24  // DCERPC_enum_shares_reply_t
	smbEnumServersReqLen     = 122 // SMB_enum_servers_req_t
	transReplyLen            = 55  // TRANS_reply_t
	trans2ReqLen             = 69  // TRANS2_req_t
	trans2ReplyLen           = 55  // TRANS2_reply_t
	smbTrans2FindReqLen      = 84  // SMB_TRANS2_FIND_req_t
	smbFindDataLen           = 94  // SMB_FIND_DATA_t
	smbNTCreateAndXReqLen    = 87  // SMB_NTCREATE_ANDX_req_t
	smbNTCreateAndXRespLen   = 103 // SMB_NTCREATE_ANDX_resp_t
	smbCloseReqLen           = 45  // SMB_CLOSE_req_t
	smbReadAndXReqLen        = 63  // SMB_READ_ANDX_req_t
	smbTrans2PathQueryReqLen = 78  // SMB_TRANS2_PATH_QUERY_req_t
	echoRequestLen           = 41  // EchoRequest_t
	smbDeleteFileReqLen      = 42  // SMB_DELETE_FILE_req_t
	smbDeleteDirReqLen       = 40  // SMB_DELETE_DIR_req_t
	eaHdrLen                 = 8   // eahdr_t
	getEaHdrLen              = 5   // get_eahdr_t
)

// C: cc_flags bits
const (
	ccFAsGuest        = 0x1
	ccFNonInteractive = 0x2
	ccFAnonymous      = 0x4
)

// C: cc_status values
const (
	ccConnecting = iota
	ccRunning
	ccError
	ccZombie
)

// C: ct_status values
const (
	ctConnecting = iota
	ctRunning
	ctError
)

// C: cifs_resolve result codes
const (
	cifsResolveNeedAuth   = -1
	cifsResolveError      = 0
	cifsResolveTree       = 1
	cifsResolveConnection = 2
)

// System — C: smb_global_mutex + cifs connection list — grouped
// process state, owned by ctx.
type System struct {
	mu          sync.Mutex
	connections []*cifsConnection
	cs          *callout.CalloutSystem // C: callout_* globals — injected
	ts          *trace.TraceSystem     // C: trace() global — injected
	kr          func(id string, username, password, domain *string,
		rememberMe *int, source, reason string, flags int) int // C: keyring_lookup direct call
	gettextFn  func(string) string         // C: _() nls
	usageEvent func(key string, count int) // C: usage_event direct call
	gconf      *gconf.T                    // C: gconf_t — injected
}

// cifsConnection — C: cifs_connection_t (fa_nativesmb.c:88-139)
type cifsConnection struct {
	sys      *System // C: backpointer to smb_global_mutex/conn-list
	refcount int
	tc       *tcpcon.TCPCon

	hostname string
	port     int
	flags    int

	midGenerator uint16 // cc_mid_generator

	status int // CC_CONNECTING / CC_RUNNING / CC_ERROR / CC_ZOMBIE

	threadDone chan struct{} // C: cc_thread (joinable dispatch thread)

	cond *sync.Cond // C: cc_cond (bound to sys.mu)

	pendingNBT []*nbtReq // C: cc_pending_nbt_requests

	broken      bool
	waitForPing bool
	uid         uint16
	maxBufSize  uint16
	maxMpxCount uint16

	sessionKey uint32

	unicode      bool // cc_unicode
	bpc          int  // cc_bpc
	ntsmb        bool // cc_ntsmb
	securityMode uint8

	challengeKey [8]byte
	domain       [64]byte

	err error

	trees []*cifsTree // C: cc_trees

	timer     callout.Callout // C: cc_timer
	autoClose int             // C: cc_auto_close

	nativeOS      string
	nativeLanman  string
	primaryDomain string
}

// cifsTree — C: cifs_tree_t (fa_nativesmb.c:146-166)
type cifsTree struct {
	sys      *System         // C: backpointer to smb_global_mutex/conn-list
	cc       *cifsConnection // C: ct_cc (holds a ref on the connection)
	tid      int
	share    string
	refcount int
	cond     *sync.Cond // C: ct_cond (bound to sys.mu)
	status   int
	err      error
}

// smbFile — C: smb_file_t (fa_nativesmb.c:2234-2240)
type smbFile struct {
	sys      *System // C: backpointer to smb_global_mutex/conn-list
	ct       *cifsTree
	fid      uint16
	pos      uint64
	fileSize uint64
}

// smbFAP implements facore.SMBOps over the canonical smb machinery —
// C: fa_protocol_smb's fap_* vtable.
type smbFAP struct{ sys *System }

func (f smbFAP) NoParking(fh *facore.Handle) bool { return smbNoParking(fh) }
