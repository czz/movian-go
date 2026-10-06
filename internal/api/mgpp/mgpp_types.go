package mgpp

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/czz/movian-go/internal/backend/core"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/mgpp"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	"github.com/czz/movian-go/internal/networking/ifaddr"
	"github.com/czz/movian-go/internal/networking/udp"
	prop "github.com/czz/movian-go/internal/prop"
	"github.com/czz/movian-go/internal/service"
	settingscore "github.com/czz/movian-go/internal/settings"
	"github.com/czz/movian-go/internal/trace"
)

// Use STPP constants from pkg/mgpp
const (
	MGPPVersion = mgpp.MGPPVersion
)

// Use STPP command constants from pkg/mgpp
const (
	MGPPCmdHello          = mgpp.MGPPCmdHello
	MGPPCmdSubscribe      = mgpp.MGPPCmdSubscribe
	MGPPCmdUnsubscribe    = mgpp.MGPPCmdUnsubscribe
	MGPPCmdSet            = mgpp.MGPPCmdSet
	MGPPCmdNotify         = mgpp.MGPPCmdNotify
	MGPPCmdEvent          = mgpp.MGPPCmdEvent
	MGPPCmdReqMove        = mgpp.MGPPCmdReqMove
	MGPPCmdWantMoreChilds = mgpp.MGPPCmdWantMoreChilds
	MGPPCmdSelect         = mgpp.MGPPCmdSelect
	MGPPCmdImageLoad      = mgpp.MGPPCmdImageLoad
	MGPPCmdImageReply     = mgpp.MGPPCmdImageReply
	MGPPCmdImageFail      = mgpp.MGPPCmdImageFail
	MGPPCmdImageCancel    = mgpp.MGPPCmdImageCancel
)

// Use STPP notify type constants from pkg/mgpp
const (
	MGPPSetVoid           = mgpp.MGPPSetVoid
	MGPPSetInt            = mgpp.MGPPSetInt
	MGPPSetFloat          = mgpp.MGPPSetFloat
	MGPPSetString         = mgpp.MGPPSetString
	MGPPSetURI            = mgpp.MGPPSetURI
	MGPPSetDir            = mgpp.MGPPSetDir
	MGPPAddChilds         = mgpp.MGPPAddChilds
	MGPPAddChildsBefore   = mgpp.MGPPAddChildsBefore
	MGPPDelChild          = mgpp.MGPPDelChild
	MGPPMoveChild         = mgpp.MGPPMoveChild
	MGPPSelectChild       = mgpp.MGPPSelectChild
	MGPPAddChildSelected  = mgpp.MGPPAddChildSelected
	MGPPValueProp         = mgpp.MGPPValueProp
	MGPPToggleInt         = mgpp.MGPPToggleInt
	MGPPHaveMoreChildsYes = mgpp.MGPPHaveMoreChildsYes
	MGPPHaveMoreChildsNo  = mgpp.MGPPHaveMoreChildsNo
)

// STPPClient represents the STPP client state
// C: file-scope globals in stpp.c (stpp_controller, stpp_controllee,
// mgpp_id, stpp_system_name, stpp_interfaces, stpp_controllees).
type MGPPClient struct {
	controller            bool // C: stpp_controller
	controllee            bool // C: stpp_controllee
	instanceID            string
	netifUpdateCallbackID int
	systemName            string            // C: stpp_system_name
	multicastConn         *udp.UDPConn      // C: stpp_fd_mc
	multicastAddr         string            // C: stpp_mcast_addr
	interfaces            []*mgppInterface  // C: stpp_interfaces
	controllees           []*mgppControllee // C: stpp_controllees
	systemnameSub         *prop.Subscription
	mu                    sync.Mutex // C: stpp_controllees_mutex
	settingsMgr           *settingscore.SettingsManager
	serviceSystem         *service.ServiceSystem
	pm                    *prop.PropManager
	store                 *htsmsg.Store
	netIfMgr              *ifaddr.NetIfAddrManager
	httpServerPort        int
	finished              bool
}

// mgppInterface — C: stpp_interface_t (stpp.c:1391-1398). Per-interface
// unicast socket + periodic announce timer.
type mgppInterface struct {
	ifname  string       // C: si_ifname
	myAddr  net.IP       // C: si_myaddr
	mark    bool         // C: si_mark
	conn    *udp.UDPConn // C: si_af
	timer   *time.Timer  // C: si_periodic_timer
	stopped chan struct{}
}

// mgppControllee — C: stpp_controllee_t (stpp.c:1275-1283). A remote
// Movian instance discovered via STPP announcements.
type mgppControllee struct {
	id      [16]byte         // C: sc_id
	name    string           // C: sc_name
	typ     string           // C: sc_type
	addr    *net.UDPAddr     // C: sc_addr
	service *service.Service // C: sc_service
	timeout *time.Timer      // C: sc_timeout
}

// MgppProp represents an exported property
type MgppProp struct {
	ID   uint32
	Prop any // *prop.Prop - use any to avoid circular dependency
	Sub  *MgppSubscription
}

// MgppSubscription represents an STPP subscription
type MgppSubscription struct {
	ID         uint32
	Active     bool
	Sub        any // *prop.Subscription - use any to avoid circular dependency
	Mgpp       *Mgpp
	DirProps   []*MgppProp
	ValueProps []*MgppProp
}

// Mgpp represents an STPP connection
type Mgpp struct {
	Conn          *httpnet.HTTPConnection
	Subscriptions map[uint32]*MgppSubscription
	Props         map[uint32]*MgppProp
	PropTally     uint32
	HelloedOK     bool
	ImageReqs     map[uint32]*MgppImageReq
	mu            sync.Mutex
	ts            *trace.TraceSystem
	pm            *prop.PropManager
	backendSystem *core.BackendSystem
	// C: stpp subscriptions dispatch on asyncio_courier
	// (stpp.c:565 PROP_TAG_COURIER).
	courier *prop.Courier
}

// MgppImageReq represents an image request
type MgppImageReq struct {
	ID          uint32
	URL         string
	ReqWidth    uint32
	ReqHeight   uint32
	Flags       uint32
	Mgpp        *Mgpp
	ImageData   []byte
	Width       uint16
	Height      uint16
	ColorPlanes uint8
	ErrStr      string
	Cancel      context.CancelFunc
}
