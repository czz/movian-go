package upnp

import (
	"strings"

	"github.com/czz/movian-go/internal/htsmsg"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// rcSetMute — C: rc_SetMute (upnp_renderingcontrol.c:33-38)
func (uls *UPNPLocalService) rcSetMute(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	return nil
}

// rcSetVolume — C: rc_SetVolume (upnp_renderingcontrol.c:44-49)
func (uls *UPNPLocalService) rcSetVolume(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	return nil
}

// rcSelectPreset — C: rc_SelectPreset (upnp_renderingcontrol.c:55-60)
func (uls *UPNPLocalService) rcSelectPreset(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	return nil
}

// rcListPresets — C: rc_ListPresets (upnp_renderingcontrol.c:66-72)
func (uls *UPNPLocalService) rcListPresets(hc *httpnet.HTTPConnection, args *htsmsg.HTSMsg,
	myhost string, myport int) *htsmsg.HTSMsg {
	out := htsmsg.NewMap()
	// Trailing space in the key is upstream's — upnp_renderingcontrol.c:77
	// has "CurrentPresetNameList " verbatim. Kept 1:1.
	out.AddStr("CurrentPresetNameList ", "")
	return out
}

// rcGenerateProps — C: rc_generate_props (upnp_renderingcontrol.c:93-112)
func rcGenerateProps(uls *UPNPLocalService, myhost string, myport int) *htsmsg.HTSMsg {
	var xml strings.Builder

	xml.WriteString("<Event xmlns=\"urn:schemas-upnp-org:metadata-1-0/RCS/\">" +
		"<InstanceID val=\"0\">")

	xml.WriteString("</InstanceID></Event>")

	r := htsmsg.NewMap()
	r.AddStr("LastChange", xml.String())
	return r
}
