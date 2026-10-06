package bittorrent

// C: src/backend/bittorrent/bittorrent.h — canonical 1:1 port.
// All structures, globals, constants and protocol definitions.
// LIST_ENTRY/TAILQ_ENTRY map to (Next *T, Prev **T) field pairs as in
// the rest of the canonical ports; list heads keep lh/tq naming.

import (
	"sync"

	"github.com/czz/movian-go/internal/gconf"

	"github.com/czz/movian-go/internal/asyncio"
	backendcore "github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/misc"
	netcore "github.com/czz/movian-go/internal/networking/core"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	propcore "github.com/czz/movian-go/internal/prop"
	settings "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// C: PIECE_* flags (bittorrent.h:20-22)
const (
	PieceHave     = 0x1 // C: PIECE_HAVE
	PieceNotified = 0x2 // C: PIECE_NOTIFIED
	PieceRejected = 0x4 // C: PIECE_REJECTED
)

// ---------------------------------------------------------------------------
// C: typedef struct bt_global (bittorrent.h:48-70)

type BtGlobal struct {
	// C: static hts_mutex_t bittorrent_mutex + the four pthread_cond_t
	// statics that signal work to the torrent threads. Protects most of
	// the bittorrent data structures.
	mu                sync.Mutex // C: hts_mutex_t bittorrent_mutex
	pieceHashNeeded   *sync.Cond // C: torrent_piece_hash_needed
	pieceIONeeded     *sync.Cond // C: torrent_piece_io_needed
	pieceVerified     *sync.Cond // C: torrent_piece_verified
	metainfoAvailable *sync.Cond // C: torrent_metainfo_available

	// Injected deps — the process singletons bittorrent needs (C's
	// implicit globals: gconf.backend, event manager, prop manager,
	// settings manager, http server). Wired at init via the Set* calls.
	eventManager    *event.EventManager
	backendSystem   *backendcore.BackendSystem
	propManager     *propcore.PropManager
	settingsManager *settings.SettingsManager
	httpServer      *httpnet.HTTPServer
	gconf           *gconf.T // C: gconf_t — injected

	// DHT session state — dht.go file statics folded into the backend
	// instance.
	dhtSock         *asyncio.AsyncIOFD
	dhtSelfID       [20]byte
	dhtSecret       [16]byte // announce-token secret
	dhtNodes        map[[20]byte]*dhtNode
	dhtTxs          map[string]*dhtTx
	dhtSearches     map[[20]byte]*dhtSearch
	dhtTimer        asyncio.Timer
	dhtStarted      bool
	dhtBootstrapDNS *asyncio.DNSReq

	maxPeersGlobal     int // C: btg_max_peers_global
	maxPeersTorrent    int
	inFlightRequests   int
	activePeers        int
	peerID             [21]byte // C: btg_peer_id
	enabled            int
	cachePath          *misc.Rstr
	fam                *facore.FileAccessManager // C: implicit global fa context
	ts                 *trace.TraceSystem        // C: trace() global — injected
	torrentStatus      *propcore.Prop
	diskStatus         *propcore.Prop
	freeSpacePercent   int // C: btg_free_space_percentage
	maxSendSpeed       int
	totalBytesInactive uint64 // C: btg_total_bytes_inactive
	totalBytesActive   uint64
	cacheLimit         uint64
	diskAvail          uint64

	// C: file-scope statics consolidated into the single backend
	// instance (bt_global_t btg — one bittorrent backend per process):
	// tracker_udp.c: static int txid_gen; static uint32_t idgen;
	// static asyncio_fd_t *tracker_udp_fd
	txidGen         int
	udpConnectIDGen uint32
	trackerUDPFd    *asyncio.AsyncIOFD
	// torrent.c:45-48, torrent_settings.c, tracker.c:32
	torrentPendingsSignal      int
	torrentBootPeriodicSignal  int
	torrentMetainfoSignal      int
	torrentHashThreadRunning   bool
	torrentWriteThreadRunning  bool
	torrentSettingsAllowUpdate int
	trackerNewTorrentSignal    int

	// C: extern struct torrent_list torrents; struct tracker_list
	// trackers; torrent.c periodic timer — file statics folded into
	// the process btg instance.
	torrents             torrentList
	trackers             trackerList
	torrentPeriodicTimer asyncio.Timer
	aio                  *asyncio.AsyncIO // C: global asyncio loop

}

// NewBtGlobal creates the bittorrent backend instance — C: the
// bt_global_t btg global plus the dht.c/bittorrent_mutex file statics
// (all folded into BtGlobal). Owned by main; wired via the Set* calls.
func NewBtGlobal(aio *asyncio.AsyncIO) *BtGlobal {
	btg := &BtGlobal{
		aio:         aio,
		dhtNodes:    make(map[[20]byte]*dhtNode),
		dhtTxs:      make(map[string]*dhtTx),
		dhtSearches: make(map[[20]byte]*dhtSearch),
	}
	btg.pieceHashNeeded = sync.NewCond(&btg.mu)
	btg.pieceIONeeded = sync.NewCond(&btg.mu)
	btg.pieceVerified = sync.NewCond(&btg.mu)
	btg.metainfoAvailable = sync.NewCond(&btg.mu)
	return btg
}

// SetGconf injects the process gconf (C: gconf_t — owned by main).
func (btg *BtGlobal) SetGconf(g *gconf.T) { btg.gconf = g }

// cfg — C: gconf_t reads; nil-safe for unwired/test paths.
func (btg *BtGlobal) cfg() *gconf.T {
	if btg.gconf == nil {
		btg.gconf = gconf.New()
	}
	return btg.gconf
}

// ---------------------------------------------------------------------------
// C: typedef struct tracker (bittorrent.h:76-103)

const (
	TrackerStatePendingDNSResolve = iota // C: TRACKER_STATE_PENDING_DNS_RESOLVE
	TrackerStateConnecting
	TrackerStateConnected
	TrackerStateError
)

type Tracker struct {
	tLinkNext *Tracker  // C: LIST_ENTRY t_link
	tLinkPrev **Tracker //

	timer asyncio.Timer // C: t_timer

	state int // C: t_state (enum above)

	url  string // C: t_url
	port uint16 // C: t_port

	addr netcore.NetAddr // C: t_addr

	torrents trackerTorrentList // C: t_torrents
	connTxid uint32             // C: t_conn_txid

	connAttempt int // C: t_conn_attempt

	connID uint64 // C: t_conn_id

	announce func(tt *TrackerTorrent, event int) // C: t_announce
	destroy  func(tt *TrackerTorrent)            // C: t_destroy

	adr *asyncio.DNSReq // C: t_adr — pending async DNS request
}

// ---------------------------------------------------------------------------
// C: typedef struct piece_peer (bittorrent.h:109-118)

type PiecePeer struct {
	ppPieceLinkNext *PiecePeer // C: LIST_ENTRY pp_piece_link
	ppPieceLinkPrev **PiecePeer

	tp *TorrentPiece // C: pp_tp

	ppPeerLinkNext *PiecePeer // C: LIST_ENTRY pp_peer_link
	ppPeerLinkPrev **PiecePeer

	peer *Peer // C: pp_peer

	bad int // C: pp_bad
}

// ---------------------------------------------------------------------------
// C: typedef struct metainfo_request (bittorrent.h:124-142)

const (
	MRPendingSend = iota // C: MR_PENDING_SEND
	MRSent
	MRReceived
	MRRejected
)

type MetainfoRequest struct {
	mrPeerLinkNext *MetainfoRequest // C: LIST_ENTRY mr_peer_link
	mrPeerLinkPrev **MetainfoRequest

	peer *Peer // C: mr_peer

	mrQueryLinkNext *MetainfoRequest // C: LIST_ENTRY mr_query_link
	mrQueryLinkPrev **MetainfoRequest

	data []byte // C: void *mr_data

	state     int // C: mr_state (enum above)
	size      int // C: mr_size
	piece     int // C: mr_piece
	totalSize int // C: mr_total_size
}

// ---------------------------------------------------------------------------
// C: typedef struct peer (bittorrent.h:148-248)

const (
	PeerStateInactive = iota // C: PEER_STATE_INACTIVE
	PeerStateConnecting
	PeerStateConnectFail
	PeerStateWaitHandshake
	PeerStateRunning
	PeerStateDisconnected
	PeerStateDestroyed
	PeerStateNum // C: PEER_STATE_num
)

type Peer struct {
	torrent *Torrent // C: p_torrent

	linkNext *Peer  // C: LIST_ENTRY p_link
	linkPrev **Peer //

	runningLinkNext *Peer // C: LIST_ENTRY p_running_link
	runningLinkPrev **Peer

	unchokedLinkNext *Peer // C: LIST_ENTRY p_unchoked_link
	unchokedLinkPrev **Peer

	haveSendreqLinkNext *Peer  // C: TAILQ_ENTRY p_have_sendreq_link
	haveSendreqLinkPrev **Peer //

	name string          // C: p_name
	addr netcore.NetAddr // C: p_addr

	timer      asyncio.Timer // C: p_timer
	chokedTime uint64        // C: p_choked_time

	connection *asyncio.AsyncIOFD // C: p_connection

	connectFail  int    // C: p_connect_fail
	disconnected int    // C: p_disconnected
	failTime     uint64 // C: p_fail_time

	queueLinkNext *Peer  // C: TAILQ_ENTRY p_queue_link
	queueLinkPrev **Peer //

	state        int    // C: p_state (enum above)
	stateChangeT uint64 // C: p_state_change_time

	amChoking      bool // C: p_am_choking : 1
	amInterested   bool // C: p_am_interested : 1
	peerChoking    bool // C: p_peer_choking : 1
	peerInterested bool // C: p_peer_interested : 1
	fastExt        bool // C: p_fast_ext : 1
	extProt        bool // C: p_ext_prot : 1
	pendingHaveAll bool // C: p_pending_have_all : 1

	id [21]byte // C: p_id

	numPiecesHave int    // C: p_num_pieces_have
	pieceFlags    []byte // C: p_piece_flags

	// C: initial bitfield kept until torrent size is known
	pendingBitfield     []byte // C: p_pending_bitfield
	pendingBitfieldSize int    // C: p_pending_bitfield_size

	downloadRequests torrentRequestList  // C: p_download_requests
	sendreqs         torrentSendreqQueue // C: p_sendreqs

	activeRequests int // C: p_active_requests

	lastSend uint64 // C: p_last_send

	bytesReceived uint64 // C: p_bytes_received
	bytesSent     uint64 // C: p_bytes_sent

	blockDelay int     // C: p_block_delay
	bd         [10]int // C: p_bd[10]

	kaSendTimer   asyncio.Timer // C: p_ka_send_timer
	dataRecvTimer asyncio.Timer // C: p_data_recv_timer

	maxq int // C: p_maxq

	numRequests int // C: p_num_requests
	numCancels  int // C: p_num_cancels
	numWaste    int // C: p_num_waste

	downloadRate misc.Average // C: p_download_rate

	pieces piecePeerList // C: p_pieces

	// C: extension mapping, 0 == no support
	extUtMetadata uint8 // C: p_ext_ut_metadata

	metainfoRequests metainfoRequestList // C: p_metainfo_requests
}

// ---------------------------------------------------------------------------
// C: typedef struct torrent_file (bittorrent.h:255-272)

type TorrentFile struct {
	torrentLinkNext *TorrentFile  // C: TAILQ_ENTRY tf_torrent_link
	torrentLinkPrev **TorrentFile //

	parentLinkNext *TorrentFile  // C: TAILQ_ENTRY tf_parent_link
	parentLinkPrev **TorrentFile //

	offset uint64 // C: tf_offset
	size   uint64 // C: tf_size

	fullpath string // C: tf_fullpath
	name     string // C: tf_name

	files torrentFileQueue // C: tf_files

	torrent *Torrent // C: tf_torrent

	fhs torrentFhList // C: tf_fhs
}

// ---------------------------------------------------------------------------
// C: typedef struct torrent_piece (bittorrent.h:280-312)

type TorrentPiece struct {
	linkNext *TorrentPiece  // C: TAILQ_ENTRY tp_link
	linkPrev **TorrentPiece //

	serveLinkNext *TorrentPiece  // C: LIST_ENTRY tp_serve_link
	serveLinkPrev **TorrentPiece //

	waitingBlocks torrentBlockList   // C: tp_waiting_blocks
	sentBlocks    torrentBlockList   // C: tp_sent_blocks
	sendreqs      torrentSendreqList // C: tp_sendreqs

	data []byte // C: tp_data

	peers piecePeerList // C: tp_peers — peers that have contributed

	pieceLength int // C: tp_piece_length
	refcount    int // C: tp_refcount
	index       int // C: tp_index

	complete     bool // C: tp_complete : 1
	hashComputed bool // C: tp_hash_computed : 1
	hashOk       bool // C: tp_hash_ok : 1
	onDisk       bool // C: tp_on_disk : 1
	diskFail     bool // C: tp_disk_fail : 1
	loadReq      bool // C: tp_load_req : 1
	loadfail     bool // C: tp_loadfail : 1

	activeFh torrentFhList // C: tp_active_fh

	deadline int64 // C: tp_deadline

	downloadRate misc.Average // C: tp_download_rate

	downloadedBytes int // C: tp_downloaded_bytes
}

// ---------------------------------------------------------------------------
// C: typedef struct torrent_block (bittorrent.h:318-329)

type TorrentBlock struct {
	pieceLinkNext *TorrentBlock  // C: LIST_ENTRY tb_piece_link
	pieceLinkPrev **TorrentBlock //

	piece *TorrentPiece // C: tb_piece

	requests torrentRequestList // C: tb_requests

	begin    uint32 // C: tb_begin
	length   uint32 // C: tb_length
	reqTally uint8  // C: tb_req_tally
}

// ---------------------------------------------------------------------------
// C: typedef struct torrent_request (bittorrent.h:335-351)

type TorrentRequest struct {
	peerLinkNext *TorrentRequest  // C: LIST_ENTRY tr_peer_link
	peerLinkPrev **TorrentRequest //

	peer *Peer // C: tr_peer

	blockLinkNext *TorrentRequest  // C: LIST_ENTRY tr_block_link
	blockLinkPrev **TorrentRequest //

	block *TorrentBlock // C: tr_block

	sendTime int64 // C: tr_send_time

	piece  uint32 // C: tr_piece
	begin  uint32 // C: tr_begin
	length uint32 // C: tr_length

	reqNum uint8 // C: tr_req_num
	qdepth uint8 // C: tr_qdepth
}

// ---------------------------------------------------------------------------
// C: typedef struct torrent_sendreq (bittorrent.h:359-369)

type TorrentSendreq struct {
	peerLinkNext *TorrentSendreq  // C: TAILQ_ENTRY ts_peer_link
	peerLinkPrev **TorrentSendreq //

	peer *Peer // C: ts_peer

	pieceLinkNext *TorrentSendreq  // C: LIST_ENTRY ts_piece_link
	pieceLinkPrev **TorrentSendreq //

	piece *TorrentPiece // C: ts_piece

	offset uint32 // C: ts_offset
	length uint32 // C: ts_length
}

// ---------------------------------------------------------------------------
// C: typedef struct torrent (bittorrent.h:375-434)

type Torrent struct {
	linkNext *Torrent  // C: LIST_ENTRY to_link
	linkPrev **Torrent //

	title string // C: to_title

	refcount int // C: to_refcount

	infoHash [20]byte // C: to_info_hash

	downloadedBytes uint64 // C: to_downloaded_bytes
	uploadedBytes   uint64 // C: to_uploaded_bytes
	wastedBytes     uint64 // C: to_wasted_bytes
	totalLength     uint64 // C: to_total_length

	trackers      trackerTorrentList // C: to_trackers
	peers         peerList           // C: to_peers
	runningPeers  peerList           // C: to_running_peers
	unchokedPeers peerList           // C: to_unchoked_peers

	activePeers             int // C: to_active_peers
	numPeers                int // C: to_num_peers
	peersWithOutstandingReq int // C: to_peers_with_outstanding_requests

	inactivePeers      peerQueue // C: to_inactive_peers
	disconnectedPeers  peerQueue // C: to_disconnected_peers
	connectFailedPeers peerQueue // C: to_connect_failed_peers
	haveSendreqPeers   peerQueue // C: to_have_sendreq_peers

	lastUnchokeCheck int // C: to_last_unchoke_check
	pieceLength      int // C: to_piece_length
	numPieces        int // C: to_num_pieces

	pieceHashes []byte // C: to_piece_hashes

	files torrentFileQueue // C: to_files
	root  torrentFileQueue // C: to_root

	numActivePieces uint // C: to_num_active_pieces
	activePiecesMem uint // C: to_active_pieces_mem

	activePieces torrentPieceQueue // C: to_active_pieces
	serveOrder   torrentPieceList  // C: to_serve_order

	fhs torrentFhList // C: to_fhs

	newValidPiece       bool // C: to_new_valid_piece
	needUpdatedInterest bool // C: to_need_updated_interest
	corruptPiece        bool // C: to_corrupt_piece
	loadfail            bool // C: to_loadfail
	loadingMetadata     bool // C: to_loading_metadata

	errbuf [256]byte // C: to_errbuf

	downloadRate misc.Average // C: to_download_rate

	metainfo *misc.Buf // C: to_metainfo

	cachefile *facore.Handle // C: to_cachefile

	cachefileMapOffset   int // C: to_cachefile_map_offset
	cachefileStoreOffset int // C: to_cachefile_store_offset

	cachefilePieceMap    []int32 // C: to_cachefile_piece_map
	cachefilePieceMapInv []int32 // C: to_cachefile_piece_map_inv
	nextDiskBlock        int     // C: to_next_disk_block
	totalDiskBlocks      int     // C: to_total_disk_blocks

	outputRateTimer      asyncio.Timer // C: to_output_rate_timer
	outputRateRefillTime int64         // C: to_output_rate_refill_time
	outputRateTokens     int           // C: to_output_rate_tokens
}

// ---------------------------------------------------------------------------
// C: typedef struct torrent_fh (bittorrent.h:440-465)

type TorrentFh struct {
	h facore.Handle // C: fa_handle_t h — embedded

	btg *BtGlobal // owning backend instance (Go: was file-global btg)

	torrentFileLinkNext *TorrentFh  // C: LIST_ENTRY tfh_torrent_file_link
	torrentFileLinkPrev **TorrentFh //

	torrentLinkNext *TorrentFh  // C: LIST_ENTRY tfh_torrent_link
	torrentLinkPrev **TorrentFh //

	pieceLinkNext *TorrentFh  // C: LIST_ENTRY tfh_piece_link
	pieceLinkPrev **TorrentFh // linked during access to an active piece

	fpos uint64 // C: tfh_fpos

	file *TorrentFile // C: tfh_file

	faStats         *propcore.Prop // C: tfh_fa_stats
	torrentSeeders  *propcore.Prop // C: tfh_torrent_seeders
	torrentLeechers *propcore.Prop // C: tfh_torrent_leechers
	knownPeers      *propcore.Prop // C: tfh_known_peers
	connectedPeers  *propcore.Prop // C: tfh_connected_peers
	recvPeers       *propcore.Prop // C: tfh_recv_peers

	deadline int64 // C: tfh_deadline

	cancellable *misc.Cancellable // C: tfh_cancellable
	cancelled   bool              // C: tfh_cancelled
}

// ---------------------------------------------------------------------------
// C: typedef struct tracker_torrent (bittorrent.h:471-493)

type TrackerTorrent struct {
	trackerLinkNext *TrackerTorrent  // C: LIST_ENTRY tt_tracker_link
	trackerLinkPrev **TrackerTorrent //

	torrentLinkNext *TrackerTorrent  // C: LIST_ENTRY tt_torrent_link
	torrentLinkPrev **TrackerTorrent //

	tracker *Tracker // C: tt_tracker
	torrent *Torrent // C: tt_torrent

	timer asyncio.Timer // C: tt_timer

	txid uint32 // C: tt_txid

	interval  uint32 // C: tt_interval
	leechers  uint32 // C: tt_leechers
	seeders   uint32 // C: tt_seeders
	attempt   uint8  // C: tt_attempt
	tentative uint8  // C: tt_tentative

	trackerid string                 // C: tt_trackerid
	httpReq   *facore.AsyncioHTTPReq // C: tt_http_req
}

// ---------------------------------------------------------------------------
// List heads — C: LIST_HEAD/TAILQ_HEAD (bittorrent.h:33-46)

type trackerTorrentList struct{ lhFirst *TrackerTorrent }
type torrentList struct{ lhFirst *Torrent }
type trackerList struct{ lhFirst *Tracker }
type peerList struct{ lhFirst *Peer }
type peerQueue struct {
	tqFirst *Peer
	tqLast  **Peer
}
type torrentFileQueue struct {
	tqFirst *TorrentFile
	tqLast  **TorrentFile
}
type torrentFhList struct{ lhFirst *TorrentFh }
type torrentRequestList struct{ lhFirst *TorrentRequest }
type torrentRequestQueue struct {
	tqFirst *TorrentRequest
	tqLast  **TorrentRequest
}
type torrentPieceQueue struct {
	tqFirst *TorrentPiece
	tqLast  **TorrentPiece
}
type torrentBlockList struct{ lhFirst *TorrentBlock }
type torrentPieceList struct{ lhFirst *TorrentPiece }
type torrentSendreqList struct{ lhFirst *TorrentSendreq }
type torrentSendreqQueue struct {
	tqFirst *TorrentSendreq
	tqLast  **TorrentSendreq
}
type piecePeerList struct{ lhFirst *PiecePeer }
type metainfoRequestList struct{ lhFirst *MetainfoRequest }
