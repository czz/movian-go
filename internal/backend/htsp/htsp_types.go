// Package htsp is a 1:1 port of src/backend/htsp/htsp.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package htsp

import (
	"sync"
	"sync/atomic"

	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/keyring"
	mediacore "github.com/czz/movian-go/internal/media/core"
	"github.com/czz/movian-go/internal/networking/tcpcon"
	propcore "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/trace"
)

// C: #define EPG_TAIL 20 (htsp.c:47) — how many EPG entries to keep per channel
const epgTail = 20

// C: #define HTSP_PROTO_VERSION 1 (htsp.c:49)
const htspProtoVersion = 1

// C: #define URL_MAX 2048 / #define HOSTNAME_MAX 256 (main.h:77-78)
const urlMax = 2048

const hostnameMax = 256

// System — C: static hts_mutex_t htsp_global_mutex (htsp.c:52) +
// static struct htsp_connection_list htsp_connections (htsp.c:60).
// Owned by ctx (created in RegisterHTSPBackend).
type System struct {
	mu          sync.Mutex
	connections []*htspConnection
	kr          *keyring.Keyring      // C: implicit global keyring — injected
	ts          *trace.TraceSystem    // C: trace() global — injected from bs
	pm          *propcore.PropManager // C: implicit global prop mgr — injected from bs
}

// htspSystem — C: htsp_global_mutex + htsp_connections statics.
type htspSystem = System

// C: typedef struct htsp_tag (htsp.c:65-72)
// ht_link is implicit in the hc.tags slice.
type htspTag struct {
	id       string         // C: ht_id
	title    string         // C: ht_title
	root     *propcore.Prop // C: ht_root
	nodes    *propcore.Prop // C: ht_nodes — sorted output nodes
	channels *propcore.Prop // C: ht_channels — source nodes
}

// C: typedef struct htsp_channel (htsp.c:78-89)
// ch_link is implicit in the hc.channels slice.
type htspChannel struct {
	id    int            // C: ch_id
	title string         // C: ch_title
	root  *propcore.Prop // C: ch_root

	propIcon          *propcore.Prop // C: ch_prop_icon
	propTitle         *propcore.Prop // C: ch_prop_title
	propChannelNumber *propcore.Prop // C: ch_prop_channelNumber
	propEvents        *propcore.Prop // C: ch_prop_events
}

// C: typedef struct htsp_connection (htsp.c:95-141)
// hc_global_link is implicit in the sys.connections slice.
type htspConnection struct {
	sys *System          // C: backpointer to htsp_global_mutex/htsp_connections
	tc  *tcpcon.TCPCon   // C: hc_tc
	kr  *keyring.Keyring // C: implicit global keyring

	challenge [32]byte // C: hc_challenge

	isAsync bool // C: hc_is_async

	refcount int // C: hc_refcount

	hostname string // C: hc_hostname
	port     int    // C: hc_port

	seqGenerator atomic.Int32 // C: hc_seq_generator
	sidGenerator atomic.Int32 // C: hc_sid_generator — subscription ID

	serverName *propcore.Prop // C: hc_server_name
	rootModel  *propcore.Prop // C: hc_root_model

	channelsModel  *propcore.Prop // C: hc_channels_model
	channelsSorted *propcore.Prop // C: hc_channels_sorted
	channelsNodes  *propcore.Prop // C: hc_channels_nodes

	tagsModel *propcore.Prop // C: hc_tags_model
	tagsNodes *propcore.Prop // C: hc_tags_nodes

	dvrModel  *propcore.Prop // C: hc_dvr_model
	dvrSorted *propcore.Prop // C: hc_dvr_sorted
	dvrNodes  *propcore.Prop // C: hc_dvr_nodes

	rpcMutex sync.Mutex // C: hc_rpc_mutex
	rpcCond  *sync.Cond // C: hc_rpc_cond
	rpcQueue []*htspMsg // C: struct htsp_msg_queue hc_rpc_queue

	workerMutex sync.Mutex // C: hc_worker_mutex
	workerCond  *sync.Cond // C: hc_worker_cond
	workerQueue []*htspMsg // C: struct htsp_msg_queue hc_worker_queue

	subscriptionMutex sync.Mutex          // C: hc_subscription_mutex
	subscriptions     []*htspSubscription // C: struct htsp_subscription_list hc_subscriptions

	metaMutex sync.Mutex     // C: hc_meta_mutex
	tags      []*htspTag     // C: struct htsp_tag_list hc_tags
	channels  []*htspChannel // C: struct htsp_channel_list hc_channels
}

// C: typedef struct htsp_msg (htsp.c:147-152)
// hm_link is implicit in the queue slices.
type htspMsg struct {
	msg   *htsmsg.HTSMsg // C: hm_msg
	error bool           // C: hm_error
	seq   uint32         // C: hm_seq
}

// C: typedef struct htsp_subscription (htsp.c:158-168)
// hs_link is implicit in the hc.subscriptions slice.
type htspSubscription struct {
	sid uint32               // C: hs_sid
	mp  *mediacore.MediaPipe // C: hs_mp

	streams []*htspSubscriptionStream // C: hs_streams

	origin *propcore.Prop // C: hs_origin
}

// C: typedef struct htsp_subscription_stream (htsp.c:174-182)
// Defines a component (audio, video, etc) inside a tv stream.
// hss_link is implicit in the hs.streams slice.
type htspSubscriptionStream struct {
	index    int                   // C: hss_index
	cw       *mediacore.MediaCodec // C: hss_cw
	mq       *mediacore.MediaQueue // C: hss_mq
	dataType int                   // C: hss_data_type
}

// C: typedef struct htsp_file (htsp.c:1924-1931)
//
// In Go the fileaccess Handle wraps an io.ReadCloser + io.Seeker + sizer;
// htspFile implements those (C: fa_handle_t h embedded with fh_proto =
// &fa_protocol_htsp).
type htspFile struct {
	hc       *htspConnection // C: hf_hc
	id       int             // C: hf_id
	pos      int64           // C: hf_pos
	fileSize int64           // C: hf_file_size
	mtime    int             // C: hf_mtime
}
