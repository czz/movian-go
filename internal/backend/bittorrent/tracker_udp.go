package bittorrent

// Canonical port of src/backend/bittorrent/tracker_udp.c.

import (
	"encoding/binary"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/asyncio"
	netcore "github.com/czz/movian-go/internal/networking/core"
)

// C: static int txid_gen; static asyncio_fd_t *tracker_udp_fd —
// now BtGlobal fields.

// trackerUDPSendConnect — C: tracker_udp_send_connect (tracker_udp.c:39-61)
func (btg *BtGlobal) trackerUDPSendConnect(t *Tracker) {
	// C: static uint32_t idgen;
	btg.udpConnectIDGen++
	btg.udpConnectIDGen ^= uint32(arch.GetTS()) & 0xfffff000
	t.connTxid = btg.udpConnectIDGen | 0x80000000

	t.state = TrackerStateConnecting
	var hello [16]byte
	binary.BigEndian.PutUint64(hello[0:], 0x41727101980)
	binary.BigEndian.PutUint32(hello[8:], 0) // connect
	binary.BigEndian.PutUint32(hello[12:], t.connTxid)

	asyncio.UDPSend(btg.trackerUDPFd, hello[:], 16, &t.addr)

	timeout := 15 * (1 << t.connAttempt)
	t.timer.ArmDeltaSec(timeout)
	t.connAttempt++
	btg.trackerTrace(t,
		"Sending connect to %s (attempt:%d txid:0x%08x timeout: %ds)",
		t.url, t.connAttempt, t.connTxid, timeout)
}

// C: static uint32_t idgen — inside tracker_udp_send_connect;
// now a BtGlobal field.

// trackerUDPGotDNS — C: tracker_udp_got_dns (tracker_udp.c:68-94)
func (btg *BtGlobal) trackerUDPGotDNS(opaque any, status asyncio.DNSStatus, data any) {
	t := opaque.(*Tracker)

	t.adr = nil

	btg.mu.Lock()

	switch status {
	case asyncio.DNSStatusCompleted:
		t.addr = *data.(*netcore.NetAddr)
		t.addr.Port = t.port
		btg.trackerTrace(t, "DNS resolved to %s", netcore.NetAddrStr(&t.addr))
		btg.trackerUDPSendConnect(t)

	case asyncio.DNSStatusFailed:
		btg.trackerTrace(t, "Unable to resolve DNS: %s", data.(string))
		t.state = TrackerStateError

	default:
		panic("unreachable") // C: abort()
	}

	btg.mu.Unlock()
}

// trackerUDPTimerCb — C: tracker_udp_timer_cb (tracker_udp.c:101-113)
func (btg *BtGlobal) trackerUDPTimerCb(aux any) {
	t := aux.(*Tracker)
	btg.mu.Lock()
	defer btg.mu.Unlock()
	switch t.state {
	case TrackerStateConnecting:
		btg.trackerUDPSendConnect(t)
	}
}

// trackerUDPTorrentAnnounce — C: tracker_udp_torrent_announce
// (tracker_udp.c:120-152)
func (btg *BtGlobal) trackerUDPTorrentAnnounce(tt *TrackerTorrent, event int) {
	var out [98]byte
	t := tt.tracker
	to := tt.torrent

	if t.state != TrackerStateConnected {
		return
	}

	tt.tentative = 0

	btg.txidGen++
	tt.txid = uint32(btg.txidGen)

	btg.trackerTrace(t, "Sending annouce for \"%s\" event:%d txid:0x%x",
		to.title, event, tt.txid)

	binary.BigEndian.PutUint64(out[0:], t.connID)
	binary.BigEndian.PutUint32(out[8:], 1) // Announce
	binary.BigEndian.PutUint32(out[12:], tt.txid)
	copy(out[16:], to.infoHash[:])
	copy(out[36:], btg.peerID[:20])

	binary.BigEndian.PutUint64(out[56:], to.downloadedBytes)
	var left uint64 = 16384
	if to.totalLength != 0 {
		left = to.totalLength
	}
	binary.BigEndian.PutUint64(out[64:], left)
	binary.BigEndian.PutUint64(out[72:], to.uploadedBytes)
	binary.BigEndian.PutUint32(out[80:], uint32(event))

	binary.BigEndian.PutUint32(out[92:], 0xffffffff)
	binary.BigEndian.PutUint16(out[96:], 43213)
	asyncio.UDPSend(btg.trackerUDPFd, out[:], 98, &t.addr)
	tt.timer.ArmDeltaSec(int(tt.interval))
}

// trackerUDPAnnounceAll — C: tracker_udp_announce_all
// (tracker_udp.c:159-166)
func (btg *BtGlobal) trackerUDPAnnounceAll(t *Tracker) {
	for tt := t.torrents.lhFirst; tt != nil; tt = tt.trackerLinkNext {
		if tt.torrent != nil {
			btg.trackerUDPTorrentAnnounce(tt, 2)
		}
	}
}

// trackerUDPHandleConnectReply — C: tracker_udp_handle_connect_reply
// (tracker_udp.c:173-189)
func (btg *BtGlobal) trackerUDPHandleConnectReply(t *Tracker, data []byte) {
	if len(data) < 16 {
		return
	}

	txid := binary.BigEndian.Uint32(data[4:])

	if t.connTxid != txid {
		return
	}

	t.connAttempt = 0
	t.connID = binary.BigEndian.Uint64(data[8:])
	btg.trackerTrace(t, "Connected to tracker")
	t.timer.Disarm()
	t.state = TrackerStateConnected
	btg.trackerUDPAnnounceAll(t)
}

// trackerUDPHandleAnnounceReply — C: tracker_udp_handle_announce_reply
// (tracker_udp.c:195-246)
func (btg *BtGlobal) trackerUDPHandleAnnounceReply(tr *Tracker, data []byte) {
	if len(data) < 20 {
		return
	}

	txid := binary.BigEndian.Uint32(data[4:])

	var tt *TrackerTorrent
	for e := tr.torrents.lhFirst; e != nil; e = e.trackerLinkNext {
		if e.txid == txid {
			tt = e
			break
		}
	}

	if tt == nil {
		btg.trackerTrace(tr, "Got announce reply for unknown torrent, ignoring")
		return
	}

	tt.interval = binary.BigEndian.Uint32(data[8:])
	tt.leechers = binary.BigEndian.Uint32(data[12:])
	tt.seeders = binary.BigEndian.Uint32(data[16:])

	to := tt.torrent

	if to == nil {
		// We have successfully stopped our announcement, this is the end
		btg.trackerTorrentDestroy(tt)
		return
	}

	btg.trackerTrace(tr, "Got announce reply for \"%s\" (leechers:%d seeders:%d), "+
		"refresh in %d seconds",
		to.title, tt.leechers, tt.seeders, tt.interval)

	data = data[20:]

	var na netcore.NetAddr
	na.Family = 4

	for len(data) >= 6 {
		copy(na.Addr[:], data[:4])
		na.Port = binary.BigEndian.Uint16(data[4:])
		if na.Port > 0 {
			btg.peerAdd(to, &na)
		}
		data = data[6:]
	}
	tt.timer.ArmDeltaSec(int(tt.interval))
}

// trackerUDPHandleError — C: tracker_udp_handle_error
// (tracker_udp.c:253-283). Error, we need to reconnect
func (btg *BtGlobal) trackerUDPHandleError(tr *Tracker, data []byte) {
	if len(data) < 8 {
		return
	}

	txid := binary.BigEndian.Uint32(data[4:])

	var tt *TrackerTorrent
	for e := tr.torrents.lhFirst; e != nil; e = e.trackerLinkNext {
		if e.txid == txid {
			tt = e
			break
		}
	}

	if tt == nil {
		return // Error does not respond to our request
	}

	to := tt.torrent
	if to == nil {
		btg.trackerTorrentDestroy(tt)
		return
	}

	data = data[8:]
	// C: rstr_allocl + rstr_get + rstr_release — errmsg string
	errmsg := string(data)
	btg.trackerTrace(tr, "Got error for \"%s\" (%s) reconnecting",
		to.title, errmsg)

	btg.trackerUDPSendConnect(tr)
}

// trackerUDPHandleInput — C: tracker_udp_handle_input
// (tracker_udp.c:290-317)
func (btg *BtGlobal) trackerUDPHandleInput(data []byte, remoteAddr *netcore.NetAddr) {
	cmd := binary.BigEndian.Uint32(data)

	var tr *Tracker
	for e := btg.trackers.lhFirst; e != nil; e = e.tLinkNext {
		// C: !net_addr_cmp(&tr->t_addr, remote_addr) — Go NetAddrCmp
		// returns true when equal
		if netcore.NetAddrCmp(&e.addr, remoteAddr) {
			tr = e
			break
		}
	}
	if tr == nil {
		return
	}
	btg.trackerTrace(tr, "Got packet (command 0x%x)", cmd)

	switch cmd {
	case 0:
		btg.trackerUDPHandleConnectReply(tr, data)

	case 1:
		btg.trackerUDPHandleAnnounceReply(tr, data)

	case 3, 0x3000000: // Some trackers forgot to htonl() this command
		btg.trackerUDPHandleError(tr, data)
	}
}

// trackerUDPInput — C: tracker_udp_input (tracker_udp.c:324-332)
func (btg *BtGlobal) trackerUDPInput(opaque any, data []byte,
	remoteAddr *netcore.NetAddr) {
	if len(data) < 4 {
		return
	}
	btg.mu.Lock()
	defer btg.mu.Unlock()
	btg.trackerUDPHandleInput(data, remoteAddr)
}

// trackerUDPCreate — C: tracker_udp_create (tracker_udp.c:339-347)
func (btg *BtGlobal) trackerUDPCreate(hostname string, port int) *Tracker {
	t := &Tracker{}
	t.timer.Setup(btg.aio, btg.trackerUDPTimerCb, t)
	t.port = uint16(port)
	t.adr = btg.aio.DNSLookupHost(hostname, btg.trackerUDPGotDNS, t)
	t.announce = btg.trackerUDPTorrentAnnounce
	return t
}

// trackerUDPStart — C: tracker_udp_init (tracker_udp.c:354-358),
// INITME(INIT_GROUP_ASYNCIO, tracker_udp_init, NULL, 0).
// Runs on the asyncio thread (registered via torrentAsyncioStart).
func (btg *BtGlobal) trackerUDPStart() {
	btg.trackerUDPFd = btg.aio.UDPBind("bittorrent udp tracker",
		nil, btg.trackerUDPInput, nil, false, false)
}
