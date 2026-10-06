package ssdp

// Package ssdp is a 1:1 Go port of src/networking/ssdp.c —
// SSDP discovery (M-SEARCH/NOTIFY/RESPONSE) + advertisement on all
// interfaces.
//
// In C the module is a set of file-statics (ssdp_uuid,
// http_server_port, ssdp_interfaces) plus ssdp_init and an INITME fini
// hook; ssdp_recv_notify/ssdp_response call back into upnp.c via
// upnp_add_device/upnp_del_device. Here the statics live on Server and
// the two cross-module calls are injected at construction.

import (
	"context"
	"fmt"
	"github.com/czz/movian-go/internal/trace"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/app"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/networking/ifaddr"
	"github.com/czz/movian-go/internal/version"
)

// This code executes on the asyncio thread / dispatch loop

const (
	ssdpCmdNotify   = 1 // C: SSDP_NOTIFY
	ssdpCmdSearch   = 2 // C: SSDP_SEARCH
	ssdpCmdResponse = 3 // C: SSDP_RESPONSE
)

// Server owns the SSDP subsystem state.
// C: static char *ssdp_uuid, static int http_server_port,
// static struct ssdp_interface_list ssdp_interfaces (ssdp.c:41-42,251).
// The mutex has no C counterpart — C runs everything on the asyncio
// thread; Go recv loops are goroutines and need the guard.
type Server struct {
	mu         sync.Mutex
	uuid       string             // C: static char *ssdp_uuid
	httpPort   int                // C: static int http_server_port
	ts         *trace.TraceSystem // C: trace() global — injected
	interfaces []*ssdpInterface

	// Cross-module hooks — C: upnp_add_device / upnp_del_device are
	// extern calls into upnp.c (declared in upnp.h); in Go they are
	// injected so this package does not depend back on pkg/upnp.
	addDevice func(url, typ string, maxage int)
	delDevice func(url string)
}

var mcastAddr = net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 1900}

// C: static net_addr_t ssdp_mcast_addr = {4, 1900, {239,255,255,250}}

// parse parses an SSDP datagram into a header list.
// Returns SSDP_NOTIFY/SSDP_SEARCH/SSDP_RESPONSE or 0.
// C: ssdp_parse (ssdp.c:45-72)
func parse(buf string, list *httpnet.HTTPHeaders) int {
	l := buf
	first := true
	r := 0

	for {
		e := strings.IndexByte(l, '\r')
		if e < 0 || e == 0 {
			break
		}
		line := l[:e]
		l = l[e+1:]
		if len(l) > 0 && l[0] == '\n' {
			l = l[1:]
		}

		if first {
			first = false
			if line == "HTTP/1.1 200 OK" {
				r = ssdpCmdResponse
			} else if line == "M-SEARCH * HTTP/1.1" {
				r = ssdpCmdSearch
			} else if line == "NOTIFY * HTTP/1.1" {
				r = ssdpCmdNotify
			} else {
				return 0
			}
		} else {
			before, after, ok := strings.Cut(line, ":")
			if !ok {
				return 0
			}
			name := before
			val := after
			val = strings.TrimLeft(val, " ")
			list.HTTPHeaderAdd(name, val, false)
		}
	}
	return r
}

// sendStatic sends str to the multicast group.
// C: ssdp_send_static (ssdp.c:81-84)
func (si *ssdpInterface) sendStatic(str string) {
	if si.fdUc != nil {
		si.fdUc.WriteTo([]byte(str), &mcastAddr)
	}
}

// maxage extracts max-age from cache-control.
// C: ssdp_maxage (ssdp.c:91-98)
func maxage(args *httpnet.HTTPHeaders) int {
	m := 1800
	cc := args.HTTPHeaderGet("cache-control")
	if cc != "" {
		if i := strings.Index(cc, "max-age"); i >= 0 {
			cc = cc[i:]
			if _, after, ok := strings.Cut(cc, "="); ok {
				m, _ = strconv.Atoi(strings.TrimSpace(after))
			}
		}
	}
	return m
}

// recvNotify handles a NOTIFY datagram.
// C: ssdp_recv_notify (ssdp.c:105-121)
func (si *ssdpInterface) recvNotify(args *httpnet.HTTPHeaders) {
	nts := args.HTTPHeaderGet("nts")
	url := args.HTTPHeaderGet("location")
	typ := args.HTTPHeaderGet("nt")

	if nts == "" || url == "" {
		return
	}

	ma := maxage(args)

	if strings.EqualFold(nts, "ssdp:alive") && typ != "" {
		si.srv.addDevice(url, typ, ma) // C: upnp_add_device
	}

	if strings.EqualFold(nts, "ssdp:byebye") {
		si.srv.delDevice(url) // C: upnp_del_device
	}
}

// response handles an M-SEARCH response.
// C: ssdp_response (ssdp.c:127-134)
func (si *ssdpInterface) response(args *httpnet.HTTPHeaders) {
	url := args.HTTPHeaderGet("location")
	typ := args.HTTPHeaderGet("st")

	if url != "" && typ != "" {
		si.srv.addDevice(url, typ, maxage(args)) // C: upnp_add_device
	}
}

// send sends one SSDP advertisement/response packet.
// C: ssdp_send (ssdp.c:140-193)
func (si *ssdpInterface) send(myaddr *net.UDPAddr, dst *net.UDPAddr,
	nt, nts, location string, inclHost int, usnPostfix string) {

	var date string
	if dst != nil {
		date = httpnet.HTTPAsctime(time.Now(), nil, 0)
	}

	firstLine := "NOTIFY * HTTP/1.1"
	if dst != nil {
		firstLine = "HTTP/1.1 200 OK"
	}
	hostHdr := ""
	if inclHost != 0 {
		hostHdr = "HOST: 239.255.255.250:1900\r\n"
	}
	dateHdr := ""
	if date != "" {
		dateHdr = "DATE: " + date + "\r\n"
	}
	extHdr := ""
	if dst != nil {
		extHdr = "EXT:\r\n"
	}
	ntKey := "NT"
	if dst != nil {
		ntKey = "ST"
	}
	ntsHdr := ""
	if nts != "" {
		ntsHdr = "NTS: " + nts + "\r\n"
	}

	ip4 := myaddr.IP.To4()
	if ip4 == nil {
		return
	}

	buf := fmt.Sprintf(
		"%s\r\n"+
			"USN: uuid:%s%s\r\n"+
			"%s"+
			"SERVER: "+app.AppNameUser+",%s,UPnP/1.0,"+app.AppNameUser+",%s\r\n"+
			"%s"+
			"%s"+
			"LOCATION: http://%d.%d.%d.%d:%d%s\r\n"+
			"CACHE-CONTROL: max-age=90\r\n"+
			"%s: %s\r\n"+
			"%s"+
			"\r\n",
		firstLine,
		si.srv.uuid, usnPostfix,
		hostHdr,
		version.AppVersion(), version.AppVersion(),
		dateHdr,
		extHdr,
		ip4[0], ip4[1], ip4[2], ip4[3],
		si.srv.httpPort,
		location,
		ntKey, nt,
		ntsHdr)

	if dst == nil {
		dst = &mcastAddr
	}

	if si.fdUc != nil {
		si.fdUc.WriteTo([]byte(buf), dst)
	}
}

// sendAll sends all 6 announcements to dst (nil = multicast).
// C: ssdp_send_all (ssdp.c:199-238)
func (si *ssdpInterface) sendAll(myaddr *net.UDPAddr,
	dst *net.UDPAddr, nts string) {

	nt := fmt.Sprintf("uuid:%s", si.srv.uuid)

	// Root device discovery
	si.send(myaddr, dst,
		"upnp:rootdevice",
		nts, "/upnp/description.xml", 1,
		"::upnp:rootdevice")

	si.send(myaddr, dst,
		nt,
		nts, "/upnp/description.xml", 1,
		"")

	si.send(myaddr, dst,
		"urn:schemas-upnp-org:device:MediaRenderer:2",
		nts, "/upnp/description.xml", 1,
		"::urn:schemas-upnp-org:device:MediaRenderer:2")

	// Service discovery

	si.send(myaddr, dst,
		"urn:schemas-upnp-org:service:ConnectionManager:2",
		nts, "/upnp/description.xml", 1,
		"::urn:schemas-upnp-org:service:ConnectionManager:2")

	si.send(myaddr, dst,
		"urn:schemas-upnp-org:service:AVTransport:2",
		nts, "/upnp/description.xml", 1,
		"::urn:schemas-upnp-org:service:AVTransport:2")

	si.send(myaddr, dst,
		"urn:schemas-upnp-org:service:RenderingControl:2",
		nts, "/upnp/description.xml", 1,
		"::urn:schemas-upnp-org:service:RenderingControl:2")
}

// ssdpInterface — C: ssdp_interface_t (ssdp.c:245-259)
type ssdpInterface struct {
	srv  *Server      // back-pointer — C reaches file statics directly
	fdMc *net.UDPConn // C: si_fd_mc
	fdUc *net.UDPConn // C: si_fd_uc

	ifname string // C: si_ifname[NET_IFNAME_SIZE]

	myaddr net.UDPAddr // C: si_myaddr

	mark bool // C: si_mark

	aliveTimer  *time.Timer // C: si_alive_timer
	searchTimer *time.Timer // C: si_search_timer
	iface       *net.Interface
}

// input handles a received datagram.
// C: ssdp_input (ssdp.c:281-304)
func (si *ssdpInterface) input(mc bool, input []byte,
	remoteAddr *net.UDPAddr) {

	var args httpnet.HTTPHeaders

	buf := string(input)
	cmd := parse(buf, &args)
	usn := args.HTTPHeaderGet("usn")

	self := usn != "" && strings.HasPrefix(usn, "uuid:") &&
		strings.HasPrefix(usn[5:], si.srv.uuid)

	if !self {
		if cmd == ssdpCmdNotify && mc {
			si.recvNotify(&args)
		}
		if cmd == ssdpCmdResponse && !mc {
			si.response(&args)
		}
		if cmd == ssdpCmdSearch && mc {
			si.sendAll(&si.myaddr, remoteAddr, "")
		}
	}
}

const searchReq = "M-SEARCH * HTTP/1.1\r\n" +
	"HOST: 239.255.255.250:1900\r\n" +
	"MAN: \"ssdp:discover\"\r\n" +
	"MX: 1\r\n" +
	"ST: urn:schemas-upnp-org:service:ContentDirectory:1\r\n\r\n"

// C: SEARCHREQ (ssdp.c:310-316)

// sendNotify — C: ssdp_send_notify_on_interface (ssdp.c:322-325)
func (si *ssdpInterface) sendNotify(nts string) {
	si.sendAll(&si.myaddr, nil, nts)
}

// recvLoop reads datagrams until the conn closes.
func recvLoop(conn *net.UDPConn, si *ssdpInterface, mc bool) {
	buf := make([]byte, 8192)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		si.srv.mu.Lock()
		si.input(mc, buf[:n], addr)
		si.srv.mu.Unlock()
	}
}

// sendAlive — C: ssdp_send_alive (ssdp.c:341-346)
func (si *ssdpInterface) sendAlive() {
	si.sendNotify("ssdp:alive")
	si.aliveTimer = time.AfterFunc(15*time.Second, func() { si.sendAlive() })
}

// sendSearch — C: ssdp_send_search (ssdp.c:352-355)
func (si *ssdpInterface) sendSearch() {
	si.sendStatic(searchReq)
	si.searchTimer = time.AfterFunc(60*time.Second, func() { si.sendSearch() })
}

// netIfUpdate adds/removes per-interface SSDP sockets.
// C: ssdp_netif_update (ssdp.c:362-455)
func (srv *Server) netIfUpdate(nis []netInterfaceInfo) {
	srv.mu.Lock()
	defer srv.mu.Unlock()

	singleInterfaceMode := 0
	for _, si := range srv.interfaces {
		si.mark = true
	}

	for _, ni := range nis {
		var si *ssdpInterface
		for _, s := range srv.interfaces {
			if s.myaddr.IP.Equal(ni.ipv4) {
				si = s
				break
			}
		}

		if si == nil {
			si = &ssdpInterface{
				srv:    srv,
				ifname: ni.name,
				iface:  ni.iface,
			}
			si.myaddr = net.UDPAddr{IP: ni.ipv4, Port: 1900}

			srv.ts.Debug("SSDP", "Trying to start on %s", ni.name)

			// Multicast listener bound to :1900, joined on this interface
			mc, err := net.ListenMulticastUDP("udp", si.iface,
				&net.UDPAddr{IP: mcastAddr.IP, Port: 1900})
			if err != nil {
				// Failed to join for a specific interface — try all
				mc, err = net.ListenMulticastUDP("udp", nil,
					&net.UDPAddr{IP: mcastAddr.IP, Port: 1900})
				if err != nil {
					srv.ts.Error("SSDP", "Failed to join multicast group %s on %s",
						mcastAddr.IP, ni.name)
					continue
				}
				singleInterfaceMode = 1
			}
			si.fdMc = mc

			// Unicast socket bound to the interface address, port 0
			uc, err := net.ListenUDP("udp",
				&net.UDPAddr{IP: ni.ipv4, Port: 0})
			if err != nil {
				srv.ts.Error("SSDP", "Failed to bind unicast to %s on %s",
					ni.ipv4, ni.name)
				si.fdMc.Close()
				continue
			}
			si.fdUc = uc

			srv.interfaces = slices.Insert(srv.interfaces, 0, si) // C: LIST_INSERT_HEAD

			go recvLoop(si.fdMc, si, true)
			go recvLoop(si.fdUc, si, false)

			si.sendStatic(searchReq)
			si.sendNotify("ssdp:alive")

			si.aliveTimer = time.AfterFunc(1*time.Second,
				func() { si.sendAlive() })
			si.searchTimer = time.AfterFunc(1*time.Second,
				func() { si.sendSearch() })

			srv.ts.Info("SSDP", "SSDP started on '%s'%s", si.ifname,
				map[bool]string{true: ", Single interface mode"}[singleInterfaceMode == 1])
			if singleInterfaceMode != 0 {
				break
			}
		} else {
			si.mark = false
		}
	}

	// Remove interfaces that went away
	keep := srv.interfaces[:0]
	for _, si := range srv.interfaces {
		if !si.mark {
			keep = append(keep, si)
			continue
		}

		srv.ts.Debug("SSDP", "SSDP stopped on %s", si.ifname)

		if si.fdMc != nil {
			si.fdMc.Close()
		}
		if si.fdUc != nil {
			si.fdUc.Close()
		}
		if si.aliveTimer != nil {
			si.aliveTimer.Stop()
		}
		if si.searchTimer != nil {
			si.searchTimer.Stop()
		}
	}
	srv.interfaces = keep
}

// netInterfaceInfo carries name + IPv4 addr for netIfUpdate.
// C: struct netif { ifname[], ipv4_addr[4] }
type netInterfaceInfo struct {
	name  string
	ipv4  net.IP
	iface *net.Interface
}

// enumerateInterfaces gathers the interface list.
// C: net_get_interfaces / the ni array walked by ssdp_netif_update
func enumerateInterfaces() []netInterfaceInfo {
	var out []netInterfaceInfo
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagUp == 0 ||
			ifaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifaces[i].Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil {
				continue
			}
			out = append(out, netInterfaceInfo{
				name:  ifaces[i].Name,
				ipv4:  ip4,
				iface: &ifaces[i],
			})
			break
		}
	}
	return out
}

// Shutdown sends byebye on all interfaces then tears them down.
// C: ssdp_shutdown (ssdp.c:462-473) + the INITME fini wrapper.
func (srv *Server) Shutdown() {
	srv.mu.Lock()
	for _, si := range srv.interfaces {
		srv.ts.Debug("SSDP", "Sending byebye on %s", si.ifname)
		for range 3 {
			si.sendNotify("ssdp:byebye")
		}
	}
	srv.mu.Unlock()
	srv.netIfUpdate(nil) // Turn off all
}

// NewServer starts the SSDP subsystem: stores uuid/httpPort and
// registers for network-change notifications (initial population runs
// immediately).
// C: ssdp_init (ssdp.c:499-505) — add/del map the extern
// upnp_add_device / upnp_del_device calls ssdp.c makes into upnp.c.
func NewServer(uuid string, httpPort int, nm *ifaddr.NetIfAddrManager,
	add func(url, typ string, maxage int),
	del func(url string), ts *trace.TraceSystem) *Server {

	srv := &Server{
		uuid:      uuid,     // C: ssdp_uuid = strdup(uuid)
		httpPort:  httpPort, // C: http_server_port = http_server_port0
		addDevice: add,
		delDevice: del,
		ts:        ts,
	}

	// C: asyncio_register_for_network_changes(ssdp_netif_update) —
	// asyncio fires the callback immediately at registration, which is
	// what populates the interface list. Go's RegisterNetworkChangeCallback
	// doesn't, so we invoke it explicitly. With no netif manager there is
	// no source of interface events at all → nothing binds (tests pass nil).
	if nm != nil {
		nm.RegisterNetworkChangeCallback(context.Background(), func() {
			srv.netIfUpdate(enumerateInterfaces())
		})
		srv.netIfUpdate(enumerateInterfaces())
	}
	return srv
}
