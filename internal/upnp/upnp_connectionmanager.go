package upnp

import (
	"github.com/czz/movian-go/internal/htsmsg"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// cmGetProtocolInfo — C: cm_GetProtocolInfo (upnp_connectionmanager.c:30-37)
func (uls *UPNPLocalService) cmGetProtocolInfo(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	out := htsmsg.NewMap()
	out.AddStr("Source", "")
	out.AddStr("Sink", "http-get:*:*:*")
	return out
}

// cmGetCurrentConnectionInfo — C: cm_GetCurrentConnectionInfo
// (upnp_connectionmanager.c:44-58)
func (uls *UPNPLocalService) cmGetCurrentConnectionInfo(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	uri := args.GetStr("ConnectionID")
	if uri != "0" {
		return upnpControlInvalidArgs
	}

	out := htsmsg.NewMap()
	out.AddU32("RcsID", 0)
	out.AddU32("AVTransportID", 0)
	out.AddStr("ProtocolInfo", "")
	out.AddStr("PeerConnectionManager", "")
	out.AddS32("PeerConnectionID", -1)
	out.AddStr("Direction", "Input")
	out.AddStr("Status", "OK")
	return out
}

// cmGetCurrentConnectionIDs — C: cm_GetCurrentConnectionIDs
// (upnp_connectionmanager.c:65-74)
func (uls *UPNPLocalService) cmGetCurrentConnectionIDs(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	out := htsmsg.NewMap()
	out.AddStr("ConnectionIDs", "0")
	return out
}

// cmGenerateProps — C: cm_generate_props (upnp_connectionmanager.c:80-88)
func cmGenerateProps(uls *UPNPLocalService, myhost string, myport int) *htsmsg.HTSMsg {
	r := htsmsg.NewMap()
	r.AddStr("SourceProtocolInfo", "")
	r.AddStr("SinkProtocolInfo", "http-get:*:*:*")
	r.AddStr("CurrentConnectionIDs", "0")
	return r
}
