package bittorrent

// Mainline DHT (BEP-5) — EXTENSION, no C counterpart.
// The C implementation discovers peers exclusively via trackers
// (tracker_http.c/tracker_udp.c): a magnet link without tr= parameters
// ends up with an empty to_trackers list and stalls forever in
// torrent_load. This module implements the Mainline DHT so trackerless
// torrents can find peers:
//
//   - KRPC protocol over one UDP socket (asyncio thread)
//   - flat routing table of known nodes (cap 512)
//   - iterative get_peers lookups (α=3, K=8) per infohash
//   - discovered peers enter the canonical path via peer_add()
//   - incoming KRPC queries answered (ping/find_node/get_peers/
//     announce_peer) so this node stays in foreign routing tables
//   - bootstrap via the well-known DHT routers
//
// announce_peer is answered but not acted on: this codebase has no
// inbound BT listener, so peers announcing to us cannot connect anyway.
// Searches are driven from torrentPeriodic for torrents with an empty
// tracker list, keeping the extension strictly scoped to trackerless
// operation (canonical tracker path untouched).

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"slices"

	"github.com/czz/movian-go/internal/asyncio"
	"github.com/czz/movian-go/internal/htsmsg"
	netcore "github.com/czz/movian-go/internal/networking/core"
)

const (
	dhtK                = 8 // nodes returned per find_node/get_peers reply
	dhtAlpha            = 3 // concurrent search queries
	dhtTxTimeoutUs      = 5000000
	dhtNodeMaxFails     = 3
	dhtTableCap         = 512
	dhtSearchRefreshSec = 45 // re-run a completed trackerless search
	dhtSearchExpireSec  = 90 // drop a search not ensured for this long
	dhtMaxPeerStash     = 64
)

// dhtNode — a known DHT node.
type dhtNode struct {
	id       [20]byte
	addr     netcore.NetAddr
	lastSeen int64 // µs of last valid traffic
	lastTry  int64
	fails    int
	hasID    bool // id learned from a response (vs just the sender addr)
}

// dhtTx — an outstanding query waiting for its response.
type dhtTx struct {
	kind   string // "ping", "find_node", "get_peers"
	search *dhtSearch
	node   *dhtNode
	sent   int64
}

// dhtSearch — iterative get_peers lookup for one infohash.
type dhtSearch struct {
	btg         *BtGlobal // owning backend instance
	infoHash    [20]byte
	candidates  map[[20]byte]*dhtNode // id → node, grown from responses
	queried     map[[20]byte]bool
	outstanding int
	peersFound  int
	lastEnsure  int64 // µs; search dies if not re-ensured
	done        bool
	nextRun     int64 // µs; done searches re-arm for refresh
}

var (
	dhtBootstraps = []struct {
		host string
		port uint16
	}{
		{"dht.transmissionbt.com", 6881},
		{"dht.libtorrent.org", 25401},
		{"router.bittorrent.com", 6881},
		{"router.utorrent.com", 6881},
	}
	dhtBootstrapIdx  int
	dhtBootstrapPort uint16
	dhtLastBootstrap int64
)

// ------------------------------------------------------------------
// KRPC wire helpers
// ------------------------------------------------------------------

// dhtNewID generates a random node/transaction id.
func (btg *BtGlobal) dhtNewID(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// dhtSend serializes and sends a KRPC message.
func (btg *BtGlobal) dhtSend(addr *netcore.NetAddr, m *htsmsg.HTSMsg) {
	b := BencodeSerialize(m)
	m.Release()
	if b == nil || btg.dhtSock == nil {
		return
	}
	asyncio.UDPSend(btg.dhtSock, b.C8(), b.Len(), addr)
	b.Release()
}

// dhtSendQuery issues a KRPC query and records the transaction.
func (btg *BtGlobal) dhtSendQuery(n *dhtNode, q string, args *htsmsg.HTSMsg,
	search *dhtSearch) {
	args.AddBin("id", btg.dhtSelfID[:])

	var tid string
	for range 16 {
		tid = string(btg.dhtNewID(2))
		if _, ok := btg.dhtTxs[tid]; !ok {
			break
		}
	}

	// Keys must be bencode-sorted: a < q < t < y.
	m := htsmsg.NewMap()
	m.AddMsg("a", args)
	m.AddStr("q", q)
	m.AddStr("t", tid)
	m.AddStr("y", "q")

	btg.dhtTxs[tid] = &dhtTx{
		kind:   q,
		search: search,
		node:   n,
		sent:   btg.aio.CurrentTime(),
	}
	n.lastTry = btg.aio.CurrentTime()
	btg.dhtSend(&n.addr, m)
}

// dhtSendResponse answers a query: {r:{id,...}, t, y:"r"}.
func (btg *BtGlobal) dhtSendResponse(addr *netcore.NetAddr, tid string, r *htsmsg.HTSMsg) {
	r.AddBin("id", btg.dhtSelfID[:])
	m := htsmsg.NewMap()
	m.AddMsg("r", r)
	m.AddStr("t", tid)
	m.AddStr("y", "r")
	btg.dhtSend(addr, m)
}

// dhtSendError answers a query with a KRPC error.
func (btg *BtGlobal) dhtSendError(addr *netcore.NetAddr, tid string, code int64,
	msg string) {
	e := htsmsg.NewList()
	e.AddS64("", code)
	e.AddStr("", msg)
	m := htsmsg.NewMap()
	m.AddMsg("e", e)
	m.AddStr("t", tid)
	m.AddStr("y", "e")
	btg.dhtSend(addr, m)
}

// ------------------------------------------------------------------
// Routing table
// ------------------------------------------------------------------

// dhtDistance compares XOR distance of a/b to target.
func (btg *BtGlobal) dhtDistanceCmp(target, a, b *[20]byte) int {
	for i := range 20 {
		da := a[i] ^ target[i]
		db := b[i] ^ target[i]
		if da != db {
			if da < db {
				return -1
			}
			return 1
		}
	}
	return 0
}

// dhtAddNode inserts/updates a node in the routing table.
// id may be nil when only the address is known.
func (btg *BtGlobal) dhtAddNode(id []byte, addr *netcore.NetAddr, hasID bool) *dhtNode {
	var key [20]byte
	if hasID && len(id) == 20 {
		copy(key[:], id)
	} else {
		// Key on a hash of the address when the id isn't known.
		sum := sha1.Sum(addr.Addr[:4])
		copy(key[:], sum[:])
		key[0] ^= 0x80 // keep addr-keyed nodes out of real-id space
	}

	if n, ok := btg.dhtNodes[key]; ok {
		if hasID && len(id) == 20 && n.id != key {
			copy(n.id[:], id)
		}
		n.addr = *addr
		n.lastSeen = btg.aio.CurrentTime()
		n.fails = 0
		n.hasID = n.hasID || (hasID && len(id) == 20)
		return n
	}

	if len(btg.dhtNodes) >= dhtTableCap {
		// Evict the worst entry: most fails, then oldest.
		var worst *[20]byte
		var worstKey [20]byte
		var worstScore int64 = -1
		for k, n := range btg.dhtNodes {
			score := int64(n.fails)*1000000000 -
				n.lastSeen/1000000
			if score > worstScore {
				worstScore = score
				worst = &worstKey
				worstKey = k
			}
		}
		if worst != nil {
			delete(btg.dhtNodes, *worst)
		}
	}

	n := &dhtNode{addr: *addr, lastSeen: btg.aio.CurrentTime()}
	if hasID && len(id) == 20 {
		copy(n.id[:], id)
		n.hasID = true
	}
	btg.dhtNodes[key] = n
	return n
}

// dhtClosest returns the n closest known nodes to target.
func (btg *BtGlobal) dhtClosest(target *[20]byte, n int) []*dhtNode {
	nodes := make([]*dhtNode, 0, len(btg.dhtNodes))
	for _, nd := range btg.dhtNodes {
		nodes = append(nodes, nd)
	}
	slices.SortFunc(nodes, func(a, b *dhtNode) int { return btg.dhtDistanceCmp(target, &a.id, &b.id) })
	if len(nodes) > n {
		nodes = nodes[:n]
	}
	return nodes
}

// dhtCompactNodes encodes nodes as 26-byte entries (id+ipv4+port).
func (btg *BtGlobal) dhtCompactNodes(nodes []*dhtNode) []byte {
	var out []byte
	for _, n := range nodes {
		out = append(out, n.id[:]...)
		out = append(out, n.addr.Addr[:4]...)
		var p [2]byte
		binary.BigEndian.PutUint16(p[:], n.addr.Port)
		out = append(out, p[:]...)
	}
	return out
}

// dhtParseCompactNodes decodes a "nodes" blob.
func (btg *BtGlobal) dhtParseCompactNodes(blob []byte) []*dhtNode {
	var out []*dhtNode
	for len(blob) >= 26 {
		na := netcore.NetAddr{Family: 4}
		copy(na.Addr[:4], blob[20:24])
		na.Port = binary.BigEndian.Uint16(blob[24:26])
		if na.Port != 0 {
			out = append(out, btg.dhtAddNode(blob[:20], &na, true))
		}
		blob = blob[26:]
	}
	return out
}

// ------------------------------------------------------------------
// Token for announce_peer
// ------------------------------------------------------------------

// dhtToken computes the announce token for a remote address.
func (btg *BtGlobal) dhtToken(addr *netcore.NetAddr) []byte {
	h := sha1.New()
	h.Write(btg.dhtSecret[:])
	h.Write(addr.Addr[:4])
	var p [2]byte
	binary.BigEndian.PutUint16(p[:], addr.Port)
	h.Write(p[:])
	return h.Sum(nil)[:8]
}

// ------------------------------------------------------------------
// Searches
// ------------------------------------------------------------------

// dhtEnsureSearch — called from torrentPeriodic for every torrent with an
// empty tracker list. Creates (or refreshes) the get_peers search for the
// infohash. Safe: all DHT state lives on the asyncio thread.
func (btg *BtGlobal) dhtEnsureSearch(infoHash []byte) {
	if !btg.dhtStarted || len(infoHash) != 20 {
		return
	}
	var ih [20]byte
	copy(ih[:], infoHash)

	now := btg.aio.CurrentTime()
	s, ok := btg.dhtSearches[ih]
	if !ok {
		s = &dhtSearch{
			btg:        btg,
			infoHash:   ih,
			candidates: make(map[[20]byte]*dhtNode),
			queried:    make(map[[20]byte]bool),
		}
		btg.dhtSearches[ih] = s
		s.seedCandidates()
	}
	s.lastEnsure = now
	if s.done && now >= s.nextRun {
		// Refresh: start a new round with fresh closest nodes.
		s.done = false
		s.answeredReset()
	}
}

// seedCandidates fills the candidate set with the closest nodes that
// have a real (response-learned) node id — get_peers needs an id-keyed
// target so the candidate map stays consistent.
func (s *dhtSearch) seedCandidates() {
	for _, n := range s.btg.dhtClosest(&s.infoHash, dhtK) {
		if n.hasID {
			s.candidates[n.id] = n
		}
	}
}

func (s *dhtSearch) answeredReset() {
	s.candidates = make(map[[20]byte]*dhtNode)
	s.queried = make(map[[20]byte]bool)
	s.outstanding = 0
	s.peersFound = 0
	s.seedCandidates()
}

// dhtRunSearches drives all searches: fill up to α outstanding queries.
func (btg *BtGlobal) dhtRunSearches() {
	now := btg.aio.CurrentTime()
	for ih, s := range btg.dhtSearches {
		if now-s.lastEnsure > dhtSearchExpireSec*1000000 {
			delete(btg.dhtSearches, ih)
			continue
		}
		if s.done {
			continue
		}
		if len(btg.dhtTxs) > 64 {
			break
		}
		// Pick unqueried candidates closest to the target.
		var cands []*dhtNode
		for id, n := range s.candidates {
			if !s.queried[id] {
				cands = append(cands, n)
			}
		}
		if len(cands) == 0 {
			if s.outstanding == 0 {
				if len(s.queried) == 0 && len(s.candidates) == 0 {
					// Search created before bootstrap populated the
					// table — adopt newly-id'd nodes and retry.
					s.seedCandidates()
				} else {
					s.done = true
					s.nextRun = now + dhtSearchRefreshSec*1000000
				}
			}
			continue
		}
		slices.SortFunc(cands, func(a, b *dhtNode) int { return btg.dhtDistanceCmp(&s.infoHash, &a.id, &b.id) })
		for _, n := range cands {
			if s.outstanding >= dhtAlpha {
				break
			}
			s.queried[n.id] = true
			s.outstanding++

			args := htsmsg.NewMap()
			args.AddBin("info_hash", s.infoHash[:])
			btg.dhtSendQuery(n, "get_peers", args, s)
		}
	}
}

// dhtDeliverPeers feeds discovered peers into the canonical peer path.
func (btg *BtGlobal) dhtDeliverPeers(s *dhtSearch, peers []netcore.NetAddr) {
	if len(peers) == 0 {
		return
	}
	btg.mu.Lock()
	to := btg.torrentFindByHash(s.infoHash[:])
	if to != nil {
		for i := range peers {
			btg.peerAdd(to, &peers[i])
		}
	}
	btg.mu.Unlock()
	s.peersFound += len(peers)
}

// ------------------------------------------------------------------
// Incoming KRPC
// ------------------------------------------------------------------

// dhtHandleQuery answers incoming queries.
func (btg *BtGlobal) dhtHandleQuery(m *htsmsg.HTSMsg, tid string, remote *netcore.NetAddr) {
	a := m.GetMap("a")
	if a == nil {
		btg.dhtSendError(remote, tid, 203, "missing args")
		return
	}
	senderID, _ := a.GetBin("id")
	if len(senderID) == 20 {
		btg.dhtAddNode(senderID, remote, true)
	} else {
		btg.dhtSendError(remote, tid, 203, "bad node id")
		return
	}

	switch m.GetStr("q") {
	case "ping":
		btg.dhtSendResponse(remote, tid, htsmsg.NewMap())

	case "find_node":
		target, err := a.GetBin("target")
		if err != nil || len(target) != 20 {
			btg.dhtSendError(remote, tid, 203, "bad target")
			return
		}
		var t [20]byte
		copy(t[:], target)
		r := htsmsg.NewMap()
		r.AddBin("nodes", btg.dhtCompactNodes(btg.dhtClosest(&t, dhtK)))
		btg.dhtSendResponse(remote, tid, r)

	case "get_peers":
		ih, err := a.GetBin("info_hash")
		if err != nil || len(ih) != 20 {
			btg.dhtSendError(remote, tid, 203, "bad info_hash")
			return
		}
		var t [20]byte
		copy(t[:], ih)
		r := htsmsg.NewMap()
		r.AddBin("token", btg.dhtToken(remote))
		// We don't index peer swarms: reply with the closest nodes,
		// which is a spec-compliant "no values" answer.
		r.AddBin("nodes", btg.dhtCompactNodes(btg.dhtClosest(&t, dhtK)))
		btg.dhtSendResponse(remote, tid, r)

	case "announce_peer":
		// No inbound BT listener exists in this codebase, so the
		// announced peer data is of no use locally. Acknowledge to
		// remain protocol-correct and keep our node in their table.
		btg.dhtSendResponse(remote, tid, htsmsg.NewMap())

	default:
		btg.dhtSendError(remote, tid, 204, "unknown method")
	}
}

// dhtHandleResponse processes a response matching an outstanding tx.
func (btg *BtGlobal) dhtHandleResponse(m *htsmsg.HTSMsg, tid string, remote *netcore.NetAddr) {
	tx, ok := btg.dhtTxs[tid]
	if !ok {
		return
	}
	delete(btg.dhtTxs, tid)

	r := m.GetMap("r")
	if r == nil {
		return
	}

	nodeID, _ := r.GetBin("id")
	if len(nodeID) == 20 {
		n := btg.dhtAddNode(nodeID, remote, true)
		tx.node = n
	}
	if tx.node != nil {
		tx.node.lastSeen = btg.aio.CurrentTime()
		tx.node.fails = 0
	}

	if tx.search != nil {
		tx.search.outstanding--
	}

	// Harvest nodes.
	if blob, err := r.GetBin("nodes"); err == nil {
		newNodes := btg.dhtParseCompactNodes(blob)
		if tx.search != nil {
			for _, n := range newNodes {
				if !n.hasID {
					continue
				}
				if _, seen := tx.search.queried[n.id]; !seen {
					tx.search.candidates[n.id] = n
				}
			}
		}
	}

	// Harvest peers (get_peers only).
	if tx.kind == "get_peers" {
		if vals := r.GetList("values"); vals != nil {
			var peers []netcore.NetAddr
			for _, f := range vals.GetFields() {
				b := f.GetBinData()
				if b == nil {
					if s, ok := f.FieldGetString(); ok {
						b = []byte(s)
					}
				}
				if len(b) != 6 {
					continue
				}
				na := netcore.NetAddr{Family: 4}
				copy(na.Addr[:4], b[:4])
				na.Port = binary.BigEndian.Uint16(b[4:6])
				if na.Port != 0 {
					peers = append(peers, na)
				}
				if len(peers) >= dhtMaxPeerStash {
					break
				}
			}
			if tx.search != nil {
				btg.dhtDeliverPeers(tx.search, peers)
			}
		}
	}
}

// dhtHandleError marks the query node as failed.
func (btg *BtGlobal) dhtHandleError(tid string) {
	tx, ok := btg.dhtTxs[tid]
	if !ok {
		return
	}
	delete(btg.dhtTxs, tid)
	if tx.node != nil {
		tx.node.fails++
	}
	if tx.search != nil {
		tx.search.outstanding--
	}
}

// dhtInput — asyncio UDP callback.
func (btg *BtGlobal) dhtInput(opaque any, data []byte, remote *netcore.NetAddr) {
	if remote == nil || remote.Family != 4 {
		return // IPv4 DHT only (matches codebase's 4-byte addr model)
	}
	m, consumed, _ := BencodeDeserialize(data, nil, nil)
	if m == nil || consumed <= 0 {
		return
	}
	defer m.Release()

	y := m.GetStr("y")
	tidb, _ := m.GetBin("t")
	if len(tidb) == 0 {
		return
	}
	tid := string(tidb)

	switch y {
	case "q":
		btg.dhtHandleQuery(m, tid, remote)
	case "r":
		btg.dhtHandleResponse(m, tid, remote)
	case "e":
		btg.dhtHandleError(tid)
	}
}

// ------------------------------------------------------------------
// Bootstrap + maintenance
// ------------------------------------------------------------------

// dhtGotBootstrapDNS resolves a well-known router and pings it.
// The router's port rides along in dhtBootstrapPort — only one lookup is
// ever in flight (guarded by dhtBootstrapDNS != nil).
func (btg *BtGlobal) dhtGotBootstrapDNS(opaque any, status asyncio.DNSStatus, data any) {
	btg.dhtBootstrapDNS = nil
	if status != asyncio.DNSStatusCompleted {
		return
	}
	na := data.(*netcore.NetAddr)
	if na.Family != 4 {
		return
	}
	na.Port = dhtBootstrapPort
	n := btg.dhtAddNode(nil, na, false)
	// find_node on our own id seeds the table with close neighbours.
	args := htsmsg.NewMap()
	args.AddBin("target", btg.dhtSelfID[:])
	btg.dhtSendQuery(n, "find_node", args, nil)
}

// dhtBootstrap kicks off router resolution when the table is thin.
// Cycles deterministically through the router list, one lookup in
// flight at a time.
func (btg *BtGlobal) dhtBootstrap() {
	if btg.dhtBootstrapDNS != nil {
		return // resolution in flight
	}
	b := dhtBootstraps[dhtBootstrapIdx%len(dhtBootstraps)]
	dhtBootstrapIdx++
	// DNSLookupHost yields the address without a port — carry it via
	// dhtBootstrapPort into the callback.
	dhtBootstrapPort = b.port
	btg.dhtBootstrapDNS = btg.aio.DNSLookupHost(b.host,
		btg.dhtGotBootstrapDNS, nil)
}

// dhtTick — periodic maintenance on the asyncio thread (every 500ms).
func (btg *BtGlobal) dhtTick(opaque any) {
	now := btg.aio.CurrentTime()

	// Expire stale transactions.
	for tid, tx := range btg.dhtTxs {
		if now-tx.sent > dhtTxTimeoutUs {
			delete(btg.dhtTxs, tid)
			if tx.node != nil {
				tx.node.fails++
			}
			if tx.search != nil {
				tx.search.outstanding--
			}
		}
	}

	// Evict dead nodes.
	for k, n := range btg.dhtNodes {
		if n.fails >= dhtNodeMaxFails {
			delete(btg.dhtNodes, k)
		}
	}

	// Bootstrap: burst-try every router right away when the table is
	// empty, then keep retrying every 15s while we have < 8 live nodes.
	good := 0
	for _, n := range btg.dhtNodes {
		if n.fails == 0 {
			good++
		}
	}
	if good < 8 {
		interval := int64(15000000)
		if len(btg.dhtNodes) == 0 {
			interval = 1000000 // table empty: try the next router ~1s
		}
		if now-dhtLastBootstrap > interval {
			dhtLastBootstrap = now
			btg.dhtBootstrap()
		}
	}

	btg.dhtRunSearches()

	btg.dhtTimer.Arm(now + 500000)
}

// ------------------------------------------------------------------
// Start
// ------------------------------------------------------------------

// dhtStart — EXTENSION init, run on the asyncio thread alongside
// tracker_udp_init. Binds the KRPC socket and seeds bootstrap state.
func (btg *BtGlobal) dhtStart() {
	if btg.dhtStarted {
		return
	}
	copy(btg.dhtSelfID[:], btg.dhtNewID(20))
	rand.Read(btg.dhtSecret[:])

	btg.dhtSock = btg.aio.UDPBind("bittorrent dht", nil, btg.dhtInput, nil,
		true, false)
	if btg.dhtSock == nil {
		return
	}
	btg.dhtTimer.Setup(btg.aio, btg.dhtTick, nil)
	btg.dhtTimer.Arm(btg.aio.CurrentTime() + 1000000)
	dhtLastBootstrap = btg.aio.CurrentTime()
	btg.dhtStarted = true
}
