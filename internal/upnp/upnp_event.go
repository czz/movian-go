package upnp

import (
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/czz/movian-go/internal/api/soap"
	"github.com/czz/movian-go/internal/callout"
	httpnet "github.com/czz/movian-go/internal/networking/http"
)

// sendEvent is a pending NOTIFY delivery.
// C: send_event_t (upnp_event.c:37-42)
type sendEvent struct {
	hdrs httpnet.HTTPHeaders // C: hdrs
	url  string              // C: url
	out  strings.Builder     // C: out (htsbuf_queue_t)
}

// generateEvent builds one NOTIFY request for a subscription.
// C: upnp_event_generate_one (upnp_event.c:46-82)
func (uls *UPNPLocalService) generateEvent(us *UPNPSubscription) *sendEvent {
	set := &sendEvent{}

	set.out.WriteString("<?xml version=\"1.0\" encoding=\"utf-8\"?>" +
		"<e:propertyset xmlns:e=\"urn:schemas-upnp-org:event-1-0\">")

	if uls.generateProps != nil {
		p := uls.generateProps(uls, us.myhost, us.myport)

		for _, f := range p.GetFields() {
			set.out.WriteString("<e:property>")
			soap.SoapEncodeArg(&set.out, f)
			set.out.WriteString("</e:property>")
		}
		p.Release()
	}
	set.out.WriteString("</e:propertyset>")

	set.hdrs.HTTPHeaderAdd("NT", "upnp:event", false)
	set.hdrs.HTTPHeaderAdd("NTS", "upnp:propchange", false)
	set.hdrs.HTTPHeaderAdd("SID", us.uuid, false)

	set.hdrs.HTTPHeaderAdd("SEQ", strconv.Itoa(us.seq), false)
	us.seq++
	set.url = us.callback
	return set
}

// sendAndFree performs the NOTIFY request.
// C: upnp_event_send_and_free (upnp_event.c:88-100)
func (set *sendEvent) sendAndFree() {
	// C: http_req(url, HTTP_POSTDATA(...), HTTP_REQUEST_HEADERS(...),
	//             HTTP_METHOD("NOTIFY"), HTTP_READ_TIMEOUT(200),
	//             HTTP_CONNECT_TIMEOUT(700), NULL)
	dialer := &net.Dialer{Timeout: 700 * time.Millisecond}
	client := &http.Client{
		Timeout:   900 * time.Millisecond, // connect 700ms + read 200ms
		Transport: &http.Transport{DialContext: dialer.DialContext},
	}
	req, err := http.NewRequest("NOTIFY", set.url,
		strings.NewReader(set.out.String()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	for _, h := range set.hdrs {
		req.Header.Set(h.Key, h.Value)
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
	set.hdrs.HTTPHeadersFree()
}

// sendAllEvents generates and sends one NOTIFY per subscription.
// C: upnp_event_send_all (upnp_event.c:106-124)
func (uls *UPNPLocalService) sendAllEvents() {
	s := uls.sys
	var list []*sendEvent

	s.mu.Lock()

	for _, us := range uls.subscriptions {
		set := uls.generateEvent(us)
		list = slices.Insert(list, 0, set) // C: LIST_INSERT_HEAD
	}
	s.mu.Unlock()

	for _, set := range list {
		set.sendAndFree()
	}
}

// doNotify — C: do_notify (upnp_event.c:130-133)
func doNotify(c *callout.Callout, opaque any) {
	opaque.(*UPNPLocalService).sendAllEvents()
}

// scheduleNotify arms the 10ms notify timer.
// C: upnp_schedule_notify (upnp_event.c:140-143)
func (uls *UPNPLocalService) scheduleNotify() {
	s := uls.sys
	if s.calloutSystem != nil {
		s.calloutSystem.ArmHires(uls.notifyTimer, doNotify, uls, 10000)
	}
}

// doNotifyOne — C: do_notify_one (upnp_event.c:150-153)
func doNotifyOne(c *callout.Callout, opaque any) {
	opaque.(*sendEvent).sendAndFree()
}

// destroy removes and frees a subscription.
// C: subscription_destroy (upnp_event.c:164-175)
func (us *UPNPSubscription) destroy(reason string) {
	uls := us.service
	us.service.sys.upnpTrace("Deleted subscription for %s:%d callback: %s SID: %s -- %s",
		uls.name, uls.version, us.callback, us.uuid,
		reason)

	// C: LIST_REMOVE(us, us_link)
	for i, sub := range uls.subscriptions {
		if sub == us {
			uls.subscriptions = slices.Delete(uls.subscriptions, i, i+1)
			break
		}
	}
}

// findSubscription finds a subscription by SID.
// Returns with s.mu held on success, unlocked on failure.
// C: subscription_find (upnp_event.c:181-197)
func (uls *UPNPLocalService) findSubscription(str string) *UPNPSubscription {
	s := uls.sys
	if str == "" {
		return nil
	}

	s.mu.Lock()

	for _, us := range uls.subscriptions {
		if us.uuid == str {
			return us // lock still held, like C
		}
	}

	s.mu.Unlock()
	return nil
}

// subGenUuid generates a uuid:... string.
// C: sub_gen_uuid (upnp_event.c:203-220)
func subGenUuid() string {
	var d [20]byte
	rand.Read(d[:]) // C: arch_get_random_bytes(d, sizeof(d))

	return fmt.Sprintf(
		"uuid:%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-"+
			"%02x%02x%02x%02x%02x%02x",
		d[0x0], d[0x1], d[0x2], d[0x3],
		d[0x4], d[0x5], d[0x6], d[0x7],
		d[0x8], d[0x9], d[0xa], d[0xb],
		d[0xc], d[0xd], d[0xe], d[0xf])
}

// upnpSubscribe handles SUBSCRIBE/UNSUBSCRIBE on /upnp/<svc>/subscribe.
// C: upnp_subscribe (upnp_event.c:226-321)
func upnpSubscribe(hc *httpnet.HTTPConnection, remain string, opaque any,
	method httpnet.HTTPCmd) int {

	uls := opaque.(*UPNPLocalService)
	s := uls.sys
	callback := hc.HTTPArgGetHdr("callback")
	typ := hc.HTTPArgGetHdr("nt")
	sidstr := hc.HTTPArgGetHdr("sid")
	tstr := hc.HTTPArgGetHdr("timeout")
	var us *UPNPSubscription
	var timeout int
	var timeouttxt string

	switch method {
	default:
		return httpnet.HTTPStatusMethodNotAllowed

	case httpnet.HTTPCmdSubscribe:
		if tstr == "" {
			timeout = 1800
		} else {
			if strings.HasPrefix(strings.ToLower(tstr), "second-") {
				tstr = tstr[len("Second-"):]
				if strings.EqualFold(tstr, "infinite") {
					timeout = -1
				} else {
					// C: atoi(tstr) — parses leading digits only
					timeout = 0
					for len(tstr) > 0 && tstr[0] >= '0' && tstr[0] <= '9' {
						timeout = timeout*10 + int(tstr[0]-'0')
						tstr = tstr[1:]
					}
					if timeout == 0 {
						return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
							"Invalid timeout")
					}
				}
			} else {
				return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
					"Invalid timeout")
			}
		}

		if timeout == -1 {
			timeouttxt = "Second-infinite"
		} else {
			timeouttxt = fmt.Sprintf("Second-%d", timeout)
		}

		if sidstr == "" {
			// New subscription

			if typ == "" || typ != "upnp:event" {
				return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
					"Invalid or missing type")
			}

			if callback == "" {
				return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
					"No callback specified")
			}

			// C: extract <url> from callback header
			_, after, ok := strings.Cut(callback, "<")
			if !ok {
				return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
					"Misformated callback")
			}
			c := after
			d := strings.LastIndex(c, ">")
			if d < 0 {
				return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
					"Misformated callback")
			}
			c = c[:d]

			us = &UPNPSubscription{}
			us.service = uls
			us.callback = c

			us.myhost = hc.HTTPGetMyHost()
			us.myport = hc.HTTPGetMyPort()

			s.mu.Lock()
			uls.subscriptions = slices.Insert(uls.subscriptions, 0, us) // C: LIST_INSERT_HEAD

			us.uuid = subGenUuid()
			hc.HTTPSetResponseHdr("SID", us.uuid)

			s.upnpTrace("Created subscription for %s:%d callback: %s SID: %s "+
				"(timeout: %d seconds)",
				uls.name, uls.version, us.callback, us.uuid,
				timeout)

			if s.calloutSystem != nil {
				s.calloutSystem.ArmHires(nil, doNotifyOne,
					uls.generateEvent(us), 0)
			}

		} else {
			if callback != "" || typ != "" {
				return hc.HTTPError(httpnet.HTTPStatusBadRequest,
					"Callback or type sent in subscription renewal")
			}

			if us = uls.findSubscription(sidstr); us == nil {
				return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
					"Subscription not found")
			}
			// subscription_find returns with s.mu held
		}
		if timeout > 0 {
			us.expire = time.Now().Unix() + int64(timeout)
		} else {
			us.expire = -1
		}

		hc.HTTPSetResponseHdr("TIMEOUT", timeouttxt)

	case httpnet.HTTPCmdUnsubscribe:
		if callback != "" || typ != "" {
			return hc.HTTPError(httpnet.HTTPStatusBadRequest,
				"Callback or type sent in unsubscribe request")
		}
		if us = uls.findSubscription(sidstr); us == nil {
			return hc.HTTPError(httpnet.HTTPStatusPreconditionFailed,
				"Subscription not found")
		}
		// subscription_find returns with s.mu held
		us.destroy("Requested by subscriber")
	}
	s.mu.Unlock()

	uls.scheduleNotify()

	return hc.HTTPSendReply(0, "", "", "", 0, nil)
}

// purgeSubscriptions removes expired subscriptions.
// C: purge_subscriptions (upnp_event.c:330-338)
func (uls *UPNPLocalService) purgeSubscriptions(now int64) {
	// Iterate with removal like C's us/next loop
	i := 0
	for i < len(uls.subscriptions) {
		us := uls.subscriptions[i]
		if us.expire != -1 && us.expire+60 < now {
			us.destroy("Timed out")
			continue // do not i++ — list shrank
		}
		i++
	}
}

// upnpFlush periodically purges expired subscriptions on all local services.
// C: upnp_flush (upnp_event.c:344-359)
func (s *System) upnpFlush(c *callout.Callout, opaque any) {
	now := time.Now().Unix()

	if s.calloutSystem != nil {
		s.calloutSystem.Arm(s.flushTimer, s.upnpFlush, nil, 60)
	}

	s.mu.Lock()
	s.cm.purgeSubscriptions(now)
	s.rc.purgeSubscriptions(now)
	s.avt.purgeSubscriptions(now)
	s.mu.Unlock()
}

// upnpEventStart arms the periodic flush timer.
// C: upnp_event_init (upnp_event.c:366-369)
func (s *System) upnpEventStart() {
	if s.calloutSystem != nil {
		s.calloutSystem.Arm(s.flushTimer, s.upnpFlush, nil, 60)
	}
}

// eventEncodeStr emits <attrib val="str"/> with escaping.
// C: upnp_event_encode_str (upnp_event.c:376-384)
func eventEncodeStr(xml *strings.Builder, attrib, str string) {
	if str == "" {
		str = "NOT_IMPLEMENTED"
	}
	fmt.Fprintf(xml, "<%s val=\"", attrib)
	xml.WriteString(soap.EscapeXML(str))
	fmt.Fprintf(xml, "\"/>")
}

// eventEncodeInt emits <attrib val="v"/>.
// C: upnp_event_encode_int (upnp_event.c:390-393)
func eventEncodeInt(xml *strings.Builder, attrib string, v int) {
	fmt.Fprintf(xml, "<%s val=\"%d\"/>", attrib, v)
}
