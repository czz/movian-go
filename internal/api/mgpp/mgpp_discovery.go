package mgpp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/htsmsg"
	netcore "github.com/czz/movian-go/internal/networking/core"
	"github.com/czz/movian-go/internal/networking/ifaddr"
	"github.com/czz/movian-go/internal/networking/udp"
	prop "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// NewSTPPClient creates a new STPP client
func NewMGPPClient(pm *prop.PropManager, settingsMgr *settingscore.SettingsManager,
	ss *service.ServiceSystem, store *htsmsg.Store,
	netIfMgr *ifaddr.NetIfAddrManager) *MGPPClient {
	// Generate instance ID
	b := make([]byte, 16)
	instanceID := "00000000000000000000000000000000"
	if _, err := rand.Read(b); err == nil {
		instanceID = hex.EncodeToString(b)
	}

	return &MGPPClient{
		controller:    true,
		controllee:    true,
		settingsMgr:   settingsMgr,
		serviceSystem: ss,
		instanceID:    instanceID,
		multicastAddr: "239.255.255.250:42000",
		pm:            pm,
		store:         store,
		netIfMgr:      netIfMgr,
	}
}

// SetHTTPServerPort sets the HTTP server port for STPP discovery announcements.
func (c *MGPPClient) SetHTTPServerPort(port int) {
	c.httpServerPort = port
}

// NewMgpp initializes an STPP connection (C: stpp_init / ws connection
// setup in stpp.c:1213-1231 — allocates stpp_t, initializes maps).
func NewMgpp(pm *prop.PropManager, ts *trace.TraceSystem) *Mgpp {
	return &Mgpp{
		Subscriptions: make(map[uint32]*MgppSubscription),
		Props:         make(map[uint32]*MgppProp),
		ImageReqs:     make(map[uint32]*MgppImageReq),
		ts:            ts,
		pm:            pm,
	}
}

// MgppFini finalizes an STPP connection
// C: stpp_fini (stpp.c:1233-1256) — ss_destroy on every subscription
// (which unexports all props), then clears image requests.
func MgppFini(mgpp *Mgpp) {
	mgpp.mu.Lock()
	subs := make([]*MgppSubscription, 0, len(mgpp.Subscriptions))
	for _, sub := range mgpp.Subscriptions {
		subs = append(subs, sub)
	}
	mgpp.Subscriptions = make(map[uint32]*MgppSubscription)
	mgpp.mu.Unlock()

	// C: while(stpp->stpp_subscriptions.root != NULL) ss_destroy(...)
	for _, sub := range subs {
		ssDestroy(mgpp, sub)
	}

	// Clean up image requests (C: sir->sir_stpp = NULL)
	mgpp.mu.Lock()
	for _, req := range mgpp.ImageReqs {
		req.Mgpp = nil
	}
	mgpp.ImageReqs = nil
	// C: free(stpp) — the whole stpp_t is released; all maps are gone.
	mgpp.Subscriptions = nil
	mgpp.Props = nil
	mgpp.mu.Unlock()
}

// StartDiscovery initializes STPP discovery
func (c *MGPPClient) StartDiscovery() {
	// Load or generate instance ID from storage
	if c.store != nil {
		if storedID := c.store.GetStr("stpp", "id"); storedID != "" {
			c.mu.Lock()
			c.instanceID = storedID
			c.mu.Unlock()
		} else {
			c.mu.Lock()
			c.store.Set("stpp", "id", htsmsg.HmfStr, c.instanceID)
			c.mu.Unlock()
		}
	}

	// Subscribe to systemname changes
	global := c.pm.GetGlobal()
	if global != nil {
		app := c.pm.CreateMulti(global, "app")
		if app != nil {
			systemname := app.CreateString("systemname", "")
			if systemname != nil {
				c.systemnameSub = systemname.Subscribe(c.setSystemname, nil, 0)
			}
		}
	}

	// Register for network changes
	if c.netIfMgr != nil {
		ctx := context.Background()
		c.netifUpdateCallbackID, _ = c.netIfMgr.RegisterNetworkChangeCallback(ctx, c.netifUpdate)
	}
}

// CreateSettings creates STPP remote control settings
// C: stpp_discover_init (stpp.c:1746-1754) — separator "Remote control"
// + bool "Allow remote control" in gconf.settings_network, callback
// stpp_set_controllee, store "stpp"/"enablecontrollee".
func (c *MGPPClient) CreateSettings(sm *settingscore.SettingsManager) {
	if sm == nil || sm.Network() == nil {
		return
	}

	// C: settings_create_separator(gconf.settings_network, _p("Remote control"))
	sm.CreateSeparatorProp(sm.Network(), sm.P("Remote control"))

	// C: setting_create(SETTING_BOOL, gconf.settings_network,
	//   SETTINGS_INITIAL_UPDATE, SETTING_TITLE(_p("Allow remote control")),
	//   SETTING_VALUE(1), SETTING_CALLBACK(stpp_set_controllee, NULL),
	//   SETTING_COURIER(asyncio_courier), SETTING_STORE("stpp", "enablecontrollee"))
	sm.SettingCreate(settingscore.SettingBool, sm.Network(), settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Allow remote control"),
		settingscore.SettingTagValue, 1,
		settingscore.SettingTagCallback, func(opaque any, value any) {
			switch v := value.(type) {
			case int:
				c.SetControllee(v != 0)
			case bool:
				c.SetControllee(v)
			}
		}, nil,
		settingscore.SettingTagStore, "stpp", "enablecontrollee",
	)
}

// setSystemname callback for systemname changes
func (c *MGPPClient) setSystemname(opaque any, event prop.EventType, args ...any) {
	// Update system name
	if len(args) > 0 {
		if name, ok := args[0].(string); ok {
			c.mu.Lock()
			c.systemName = name
			c.mu.Unlock()
		}
	}

	// Broadcast systemname change
	c.Broadcast(1)
	_ = opaque
	_ = event
}

// Broadcast broadcasts STPP discovery message
// C: stpp_broadcast (stpp.c:1477-1489) — sends the announcement on every
// per-interface socket, count times.
func (c *MGPPClient) Broadcast(count int) {
	msg := c.buildMessage()
	if msg == nil {
		return
	}

	addr, err := net.ResolveUDPAddr("udp", c.multicastAddr)
	if err != nil {
		return
	}

	c.mu.Lock()
	ifaces := make([]*mgppInterface, len(c.interfaces))
	copy(ifaces, c.interfaces)
	c.mu.Unlock()

	for range count {
		for _, si := range ifaces {
			si.conn.Write(msg, addr)
		}
	}
}

// buildMessage builds STPP discovery message
// C: build_msg (stpp.c:1426-1441) — packed stppmsg_t:
// magic(4) "STPP" | deviceid(16) | version(1) | role(1) | port(2 BE) |
// name(32) | type(32). Returns nil while stpp_system_name is unset.
func (c *MGPPClient) buildMessage() []byte {
	c.mu.Lock()
	name := c.systemName
	c.mu.Unlock()
	if name == "" {
		return nil // C: if(stpp_system_name == NULL) return -1
	}

	buf := make([]byte, 4+16+1+1+2+32+32)

	copy(buf[0:4], "STPP")
	if idBytes, err := hex.DecodeString(c.instanceID); err == nil {
		copy(buf[4:20], idBytes)
	}
	buf[20] = MGPPVersion
	var role byte
	if c.controller {
		role |= 0x1 // C: STPP_ROLE_CONTROLLER
	}
	if c.controllee {
		role |= 0x2 // C: STPP_ROLE_CONTROLLEE
	}
	buf[21] = role
	port := uint16(c.httpServerPort)
	if port == 0 {
		port = 42000
	}
	binary.BigEndian.PutUint16(buf[22:24], port) // C: wr16_be(msg->port, ...)
	if len(name) > 32 {
		name = name[:32]
	}
	copy(buf[24:56], name)
	typ := arch.GetSystemType()
	if len(typ) > 32 {
		typ = typ[:32]
	}
	copy(buf[56:88], typ)
	return buf
}

// mgppSend — C: stpp_send (stpp.c:1450-1457). Sends the announcement on
// conn to dst (multicast when nil).
func (c *MGPPClient) mgppSend(conn *udp.UDPConn, dst *net.UDPAddr) {
	msg := c.buildMessage()
	if msg == nil {
		return
	}
	if dst == nil {
		dst, _ = net.ResolveUDPAddr("udp", c.multicastAddr)
	}
	if dst != nil {
		conn.Write(msg, dst)
	}
}

// mgppVerifyPacket — C: stpp_verify_packet (stpp.c:1496-1502).
func (c *MGPPClient) mgppVerifyPacket(data []byte) bool {
	idBytes, _ := hex.DecodeString(c.instanceID)
	var myID [16]byte
	copy(myID[:], idBytes)
	// C: size < sizeof(stppmsg_t) || magic != "STPP" ||
	//    version != STPP_VERSION || deviceid == stpp_id
	return len(data) < 88 ||
		string(data[0:4]) != "STPP" ||
		data[20] != MGPPVersion ||
		bytes.Equal(data[4:20], myID[:])
}

// mgppMulticastInput — C: stpp_multicast_input (stpp.c:1587-1597).
// Replies to controllers as controllee, then tracks the sender.
func (c *MGPPClient) mgppMulticastInput(data []byte, remoteAddr *net.UDPAddr) {
	if c.mgppVerifyPacket(data) {
		return
	}
	if data[21]&0x1 != 0 && c.controllee { // C: STPP_ROLE_CONTROLLER
		c.mgppSend(c.multicastConn, remoteAddr)
	}
	c.mgppUDPInput(data, remoteAddr)
}

// mgppUnicastInput — C: stpp_unicast_input (stpp.c:1604-1611).
func (c *MGPPClient) mgppUnicastInput(data []byte, remoteAddr *net.UDPAddr) {
	if c.mgppVerifyPacket(data) {
		return
	}
	c.mgppUDPInput(data, remoteAddr)
}

// mgppUDPInput — C: stpp_udp_input (stpp.c:1534-1580). Tracks controllee
// announcements, creating managed services for discovered Movians.
func (c *MGPPClient) mgppUDPInput(data []byte, remoteAddr *net.UDPAddr) {
	var id [16]byte
	copy(id[:], data[4:20])
	role := data[21]

	c.mu.Lock()
	defer c.mu.Unlock()

	var sc *mgppControllee
	for _, x := range c.controllees {
		if x.id == id {
			sc = x
			break
		}
	}

	if role&0x2 == 0 { // C: !(msg->role & STPP_ROLE_CONTROLLEE)
		if sc != nil {
			c.mgppControlleeDestroyLocked(sc)
		}
		return
	}

	if sc == nil {
		sc = &mgppControllee{id: id}
		c.controllees = slices.Insert(c.controllees, 0, sc)
	}
	sc.name = strings.TrimRight(string(data[24:56]), "\x00")
	sc.typ = strings.TrimRight(string(data[56:88]), "\x00")
	sc.addr = &net.UDPAddr{IP: remoteAddr.IP,
		Port: int(binary.BigEndian.Uint16(data[22:24]))}

	if sc.service == nil && c.serviceSystem != nil {
		// C: snprintf(url, "stpp:id:%s", hex(sc_id));
		//    service_create_managed(url, sc_name, url, "movian", NULL,0,0,
		//                           SVC_ORIGIN_DISCOVERED)
		url := "stpp:id:" + hex.EncodeToString(id[:])
		sc.service = c.serviceSystem.ServiceCreateManaged(
			url, sc.name, url, "movian", "", false, false,
			service.SvcOriginDiscovered)
	} else if sc.service != nil {
		service.ServiceSetTitle(sc.service, sc.name)
	}

	// C: asyncio_timer_arm_delta_sec(&sc->sc_timeout, 60)
	if sc.timeout != nil {
		sc.timeout.Stop()
	}
	sc.timeout = time.AfterFunc(60*time.Second, func() {
		c.mu.Lock()
		c.mgppControlleeDestroyLocked(sc)
		c.mu.Unlock()
	})
}

// mgppControlleeDestroyLocked — C: stpp_controllee_destroy (stpp.c:1509-1515).
// Caller must hold c.mu.
func (c *MGPPClient) mgppControlleeDestroyLocked(sc *mgppControllee) {
	if sc.service != nil && c.serviceSystem != nil {
		c.serviceSystem.ServiceDestroy(sc.service)
	}
	for i, x := range c.controllees {
		if x == sc {
			c.controllees = slices.Delete(c.controllees, i, i+1)
			break
		}
	}
	if sc.timeout != nil {
		sc.timeout.Stop()
	}
}

// udpReadLoop reads datagrams on conn and feeds them to fn — the Go
// equivalent of C's asyncio fd input callback.
func udpReadLoop(conn *udp.UDPConn, stop <-chan struct{},
	fn func(data []byte, remoteAddr *net.UDPAddr)) {
	buf := make([]byte, 2048)
	for {
		select {
		case <-stop:
			return
		default:
		}
		conn.SetReadTimeout(500 * time.Millisecond)
		n, remote, err := conn.Read(buf)
		if err != nil || n == 0 {
			if conn.IsClosed() {
				return
			}
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])
		fn(data, remote)
	}
}

// mgppPeriodic — C: stpp_periodic (stpp.c:1464-1470). Re-announces on the
// interface every 20s while controllee is enabled.
func (c *MGPPClient) mgppPeriodic(si *mgppInterface) {
	c.mgppSend(si.conn, nil)
	if c.controllee { // C: keep announcing
		si.timer = time.AfterFunc(20*time.Second, func() { c.mgppPeriodic(si) })
	}
}

// netifUpdate callback for network interface changes
// C: stpp_netif_update (stpp.c:1626-1715) — binds the multicast socket
// once, then creates/removes per-interface unicast sockets with a
// periodic announce timer.
func (c *MGPPClient) netifUpdate() {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}

	// C: if(stpp_fd_mc == NULL) bind multicast + join group
	if c.multicastConn == nil {
		conn, err := udp.NewUDPMulticast("239.255.255.250", 42000, nil)
		if err != nil {
			c.mu.Unlock()
			return
		}
		c.multicastConn = conn
		go udpReadLoop(conn, nil, c.mgppMulticastInput)
	}
	c.mu.Unlock()

	nis, err := netcore.NetGetInterfaces()
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// C: mark all interfaces, unmark those still present
	for _, si := range c.interfaces {
		si.mark = true
	}

	for _, ni := range nis {
		var si *mgppInterface
		for _, x := range c.interfaces {
			if bytes.Equal(x.myAddr, ni.IPv4Addr[:]) {
				si = x
				break
			}
		}
		if si != nil {
			si.mark = false
			continue
		}

		si = &mgppInterface{ifname: ni.Name,
			myAddr:  net.IP(ni.IPv4Addr[:]),
			stopped: make(chan struct{})}

		// C: si_af = asyncio_udp_bind(name, &si->si_myaddr (port 0),
		//                             stpp_unicast_input, si, 0, 0)
		conn, err := udp.NewUDPConn(net.IP(ni.IPv4Addr[:]).String(), 0, false)
		if err != nil {
			continue
		}
		si.conn = conn
		c.interfaces = slices.Insert(c.interfaces, 0, si)

		// C: stpp_send(si->si_af, NULL); timer armed at 1s then 20s
		c.mgppSend(conn, nil)
		go udpReadLoop(conn, si.stopped, c.mgppUnicastInput)
		si.timer = time.AfterFunc(time.Second, func() { c.mgppPeriodic(si) })
	}

	// C: remove marked (gone) interfaces
	kept := c.interfaces[:0]
	for _, si := range c.interfaces {
		if si.mark {
			close(si.stopped)
			si.conn.Close()
			if si.timer != nil {
				si.timer.Stop()
			}
			continue
		}
		kept = append(kept, si)
	}
	c.interfaces = kept
}

// DiscoverFini finalizes STPP discovery
// C: stpp_discover_fini (stpp.c:1762-1766) — disables both roles and
// broadcasts twice so peers see the shutdown.
func (c *MGPPClient) DiscoverFini() {
	c.mu.Lock()
	c.finished = true
	c.controllee = false
	c.controller = false
	c.mu.Unlock()

	// C: stpp_broadcast(2)
	c.Broadcast(2)

	// Unregister from network changes
	if c.netIfMgr != nil {
		ctx := context.Background()
		c.netIfMgr.UnregisterNetworkChangeCallback(ctx, c.netifUpdateCallbackID)
	}

	c.mu.Lock()
	// Tear down per-interface sockets and timers
	for _, si := range c.interfaces {
		close(si.stopped)
		si.conn.Close()
		if si.timer != nil {
			si.timer.Stop()
		}
	}
	c.interfaces = nil

	// Destroy tracked controllees
	for len(c.controllees) > 0 {
		c.mgppControlleeDestroyLocked(c.controllees[0])
	}

	// Close multicast connection
	if c.multicastConn != nil {
		c.multicastConn.Close()
		c.multicastConn = nil
	}
	c.mu.Unlock()

	// Unsubscribe from properties
	if c.systemnameSub != nil {
		c.pm.Unsubscribe(c.systemnameSub)
		c.systemnameSub = nil
	}
}

// SetControllee — C: stpp_set_controllee (stpp.c:1719-1723).
// Sets the flag and announces the new role once.
func (c *MGPPClient) SetControllee(on bool) {
	c.mu.Lock()
	c.controllee = on
	c.mu.Unlock()
	c.Broadcast(1)
}

// NetRefreshNetworkStatus refreshes the network status and updates property system
// This function is here because pkg/api can import both pkg/networking and pkg/prop
// without creating circular dependencies
func NetRefreshNetworkStatus(pm *prop.PropManager) error {
	interfaces, err := netcore.NetGetInterfaces()
	if err != nil {
		return err
	}

	// Get global prop
	globalProp := pm.GetGlobal()
	if globalProp == nil {
		return nil
	}

	// Create net prop
	netProp := pm.CreateEx(globalProp, "net", nil, false, true)
	if netProp == nil {
		return nil
	}

	// Create interfaces prop
	interfacesProp := pm.CreateEx(netProp, "interfaces", nil, false, true)
	if interfacesProp == nil {
		return nil
	}

	// Check if we have any interfaces
	if len(interfaces) == 0 || interfaces[0].Name == "" {
		// C: prop_set(np, "connectivity", PROP_SET_INT, 0) — a CHILD prop
		pm.SetIntEx(pm.CreateEx(netProp, "connectivity", nil, false, false),
			nil, 0)
		pm.DestroyChilds(interfacesProp)
		return nil
	}

	// C: prop_set(np, "connectivity", PROP_SET_INT, 1)
	pm.SetIntEx(pm.CreateEx(netProp, "connectivity", nil, false, false),
		nil, 1)

	// Mark all existing children for cleanup
	pm.MarkChilds(interfacesProp)

	// Update each interface
	for _, ni := range interfaces {
		ifaceProp := pm.CreateEx(interfacesProp, ni.Name, nil, false, true)
		if ifaceProp == nil {
			continue
		}

		pm.Unmark(ifaceProp)

		// C: prop_set(iface, "name", PROP_SET_STRING, ni[i].ifname)
		pm.SetStringEx(pm.CreateEx(ifaceProp, "name", nil, false, false),
			nil, ni.Name, 0)

		ipv4Addr := fmt.Sprintf("%d.%d.%d.%d",
			ni.IPv4Addr[0], ni.IPv4Addr[1], ni.IPv4Addr[2], ni.IPv4Addr[3])
		pm.SetStringEx(pm.CreateEx(ifaceProp, "ipv4_addr", nil, false, false),
			nil, ipv4Addr, 0)

		ipv4Mask := fmt.Sprintf("%d.%d.%d.%d",
			ni.IPv4Mask[0], ni.IPv4Mask[1], ni.IPv4Mask[2], ni.IPv4Mask[3])
		pm.SetStringEx(pm.CreateEx(ifaceProp, "ipv4_mask", nil, false, false),
			nil, ipv4Mask, 0)

		// C: prop_ref_dec(iface) — Go CreateEx children are
		// parent-owned; no dec needed.
	}

	// Remove interfaces that are no longer present
	pm.DestroyMarkedChilds(interfacesProp)

	return nil
}

// StartNetworking initializes networking integration with property system
func StartNetworking(netIfMgr *ifaddr.NetIfAddrManager, pm *prop.PropManager) {
	if netIfMgr == nil {
		return
	}
	ctx := context.Background()
	netIfMgr.RegisterNetworkChangeCallback(ctx, func() {
		NetRefreshNetworkStatus(pm)
	})
}

// SetCourier sets the notification courier — C: subscriptions are created
// with PROP_TAG_COURIER, asyncio_courier (stpp.c:565).
func (mgpp *Mgpp) SetCourier(c *prop.Courier) {
	mgpp.courier = c
}
