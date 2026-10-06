package mgpp

import (
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	httpnet "github.com/czz/movian-go/internal/networking/http"
	prop "github.com/czz/movian-go/internal/prop"
)

// mgppSubJSONAddChild — C: stpp_sub_json_add_child (stpp.c:167-175)
// Exports p into ss_dir_props and sends [5,ss_id,before_id,[sp_id]].
func mgppSubJSONAddChild(sub *MgppSubscription, hc *httpnet.HTTPConnection,
	p *prop.Prop, before *prop.Prop) {
	var b uint32
	if before != nil {
		b = spGet(before, sub).ID
	}
	sp := mgppPropertyExport(sub, p, true)
	msg := fmt.Sprintf("[5,%d,%d,[%d]]", sub.ID, b, sp.ID)
	hc.WebSocketSend(1, []byte(msg), len(msg))
}

// mgppSubJSONAddChilds — C: stpp_sub_json_add_childs (stpp.c:182-198)
// Exports each prop in pv and sends [5,ss_id,before_id,[ids...]].
func mgppSubJSONAddChilds(sub *MgppSubscription, hc *httpnet.HTTPConnection,
	pv []*prop.Prop, before *prop.Prop) {
	var b uint32
	if before != nil {
		b = spGet(before, sub).ID
	}
	var out strings.Builder
	fmt.Fprintf(&out, "[5,%d,%d,[", sub.ID, b)
	for i, p := range pv {
		sp := mgppPropertyExport(sub, p, true)
		if i > 0 {
			out.WriteByte(',')
		}
		fmt.Fprintf(&out, "%d", sp.ID)
	}
	out.WriteString("]]")
	msg := out.String()
	hc.WebSocketSend(1, []byte(msg), len(msg))
}

// mgppSubJSONDelChild — C: stpp_sub_json_del_child (stpp.c:207-215)
// prop_tag_clear + send [6,ss_id,[sp_id]] + unexport.
func mgppSubJSONDelChild(sub *MgppSubscription, hc *httpnet.HTTPConnection,
	p *prop.Prop) {
	spv := sub.Mgpp.pm.TagClear(p, sub)
	sp, _ := spv.(*MgppProp)
	if sp == nil {
		return
	}
	msg := fmt.Sprintf("[6,%d,[%d]]", sub.ID, sp.ID)
	hc.WebSocketSend(1, []byte(msg), len(msg))
	mgppPropertyUnexportFromSub(sub, sp)
}

// mgppSubJSONMoveChild — C: stpp_sub_json_move_child (stpp.c:223-232)
// Sends [7,ss_id,sp_id,before_id].
func mgppSubJSONMoveChild(sub *MgppSubscription, hc *httpnet.HTTPConnection,
	p *prop.Prop, before *prop.Prop) {
	sp := spGet(p, sub)
	var b *MgppProp
	if before != nil {
		b = spGet(before, sub)
	}
	var bid uint32
	if b != nil {
		bid = b.ID
	}
	msg := fmt.Sprintf("[7,%d,%d,%d]", sub.ID, sp.ID, bid)
	hc.WebSocketSend(1, []byte(msg), len(msg))
}

// mgppSubJSON is the JSON subscription callback
// C: stpp_sub_json (stpp.c:253-351) — hardwired JSON output. The first
// array element is the JSON notify code (4 = value set, 5 = add childs,
// 6 = del child, 7 = move child), NOT STPP_CMD_NOTIFY.
func mgppSubJSON(sub *MgppSubscription, event any, args ...any) {
	if sub.Mgpp == nil || sub.Mgpp.Conn == nil {
		return
	}
	hc := sub.Mgpp.Conn

	ev, ok := event.(int)
	if !ok {
		if e2, ok2 := event.(prop.EventType); ok2 {
			ev, ok = int(e2), true
		}
		if !ok {
			return
		}
	}

	var out strings.Builder

	switch prop.EventType(ev) {
	case prop.EventSetFloat:
		// C: my_double2str(buf, va_arg(ap, double))
		var f float64
		switch v := args[0].(type) {
		case float32:
			f = float64(v)
		case float64:
			f = v
		}
		fmt.Fprintf(&out, "[4,%d,%s]", sub.ID, misc.MyDouble2str(64, f))
		hc.WebSocketSend(1, []byte(out.String()), out.Len())
		ssClearProps(sub, true)
	case prop.EventSetInt:
		fmt.Fprintf(&out, "[4,%d,%d]", sub.ID, args[0])
		hc.WebSocketSend(1, []byte(out.String()), out.Len())
		ssClearProps(sub, true)
	case prop.EventSetRString, prop.EventSetCString:
		str, _ := args[0].(string)
		fmt.Fprintf(&out, "[4,%d,", sub.ID)
		htsmsg.AppendAndEscapeJSONString(&out, str)
		out.WriteByte(']')
		hc.WebSocketSend(1, []byte(out.String()), out.Len())
		ssClearProps(sub, true)
	case prop.EventSetVoid:
		fmt.Fprintf(&out, "[4,%d,null]", sub.ID)
		hc.WebSocketSend(1, []byte(out.String()), out.Len())
	case prop.EventSetURI:
		// C stpp.c:300-311: ["uri", title, url] — TWO strings
		var title, url string
		if len(args) >= 2 {
			title, _ = args[0].(string)
			url, _ = args[1].(string)
		}
		fmt.Fprintf(&out, "[4,%d,[\"uri\",", sub.ID)
		htsmsg.AppendAndEscapeJSONString(&out, title)
		out.WriteByte(',')
		htsmsg.AppendAndEscapeJSONString(&out, url)
		out.WriteString("]]")
		hc.WebSocketSend(1, []byte(out.String()), out.Len())
		ssClearProps(sub, true)
	case prop.EventSetDir:
		fmt.Fprintf(&out, "[4,%d,[\"dir\"]]", sub.ID)
		hc.WebSocketSend(1, []byte(out.String()), out.Len())
		ssClearProps(sub, true)

	case prop.EventAddChild:
		// Go EventAddChild args are (child, parent); C passes (p, flags).
		p, _ := args[0].(*prop.Prop)
		mgppSubJSONAddChild(sub, hc, p, nil)
	case prop.EventAddChildBefore:
		// Go args are (child, parent, before); C passes (p, before).
		p, _ := args[0].(*prop.Prop)
		var before *prop.Prop
		if len(args) > 2 {
			before, _ = args[2].(*prop.Prop)
		}
		mgppSubJSONAddChild(sub, hc, p, before)
	case prop.EventAddChildVector, prop.EventAddChildVectorDirect:
		pv, _ := args[0].([]*prop.Prop)
		mgppSubJSONAddChilds(sub, hc, pv, nil)
	case prop.EventAddChildVectorBefore:
		// Go args are (children, before); C passes (pv, before).
		pv, _ := args[0].([]*prop.Prop)
		var before *prop.Prop
		if len(args) > 1 {
			before, _ = args[1].(*prop.Prop)
		}
		mgppSubJSONAddChilds(sub, hc, pv, before)
	case prop.EventDelChild:
		p, _ := args[0].(*prop.Prop)
		mgppSubJSONDelChild(sub, hc, p)
	case prop.EventMoveChild:
		// Go args are (child, parent, before); C passes (p, before).
		p, _ := args[0].(*prop.Prop)
		var before *prop.Prop
		if len(args) > 2 {
			before, _ = args[2].(*prop.Prop)
		}
		mgppSubJSONMoveChild(sub, hc, p, before)

	default:
		// C: printf("stpp_sub_json() can't deal with event %d\n", event)
	}
}
