package mgpp

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/czz/movian-go/internal/event"
	prop "github.com/czz/movian-go/internal/prop"
)

// mgppHandleBinarySub handles subscribe command in binary mode
func mgppHandleBinarySub(mgpp *Mgpp, data []byte) error {
	if len(data) < 10 {
		return fmt.Errorf("subscribe data too short")
	}

	id := binary.LittleEndian.Uint32(data[0:4])
	propRef := int(binary.LittleEndian.Uint32(data[4:8]))
	flags := binary.LittleEndian.Uint16(data[8:10])

	// Parse name vector from data[10:]
	nameVec := decodeStringVector(data[10:])

	return mgppCmdSub(mgpp, id, propRef, "", flags, nameVec, mgppSubBinary)
}

// mgppHandleBinaryUnsub handles unsubscribe command in binary mode
func mgppHandleBinaryUnsub(mgpp *Mgpp, data []byte) error {
	if len(data) != 4 {
		return fmt.Errorf("unsubscribe data must be 4 bytes")
	}

	id := binary.LittleEndian.Uint32(data[0:4])
	return mgppCmdUnsub(mgpp, id)
}

// mgppHandleBinarySet handles set command in binary mode
func mgppHandleBinarySet(mgpp *Mgpp, data []byte) error {
	if len(data) < 6 {
		return fmt.Errorf("set data too short")
	}

	propRef := int(binary.LittleEndian.Uint32(data[0:4]))
	setType := data[4]
	payload := data[5:]

	p := mgppResolvePropRef(mgpp, propRef)
	if p == nil {
		return fmt.Errorf("property reference %d not found", propRef)
	}

	propPtr, ok := p.(*prop.Prop)
	if !ok {
		return fmt.Errorf("invalid property type")
	}

	switch setType {
	case MGPPSetInt:
		if len(payload) != 4 {
			return fmt.Errorf("set int requires 4 bytes")
		}
		val := int32(binary.LittleEndian.Uint32(payload))
		mgpp.pm.SetIntEx(propPtr, nil, int(val))

	case MGPPSetFloat:
		if len(payload) != 4 {
			return fmt.Errorf("set float requires 4 bytes")
		}
		val := binary.LittleEndian.Uint32(payload)
		mgpp.pm.SetFloatEx(propPtr, nil, math.Float32frombits(val))

	case MGPPSetString:
		if len(payload) < 1 {
			return fmt.Errorf("set string requires at least 1 byte for strtype")
		}
		strType := int(payload[0])
		strVal := string(payload[1:])
		mgpp.pm.SetStringEx(propPtr, nil, strVal, prop.StringType(strType))

	case MGPPSetVoid:
		mgpp.pm.SetVoidEx(propPtr, nil)

	case MGPPSetDir:
		// Set dir - mark as directory type
		mgpp.pm.MakeDir(propPtr)
		return nil

	case MGPPSetURI:
		if len(payload) == 0 {
			return fmt.Errorf("set URI requires data")
		}
		uri := string(payload)
		// C stpp_cmd_set does NOT handle URI sets from client (only int/string/float).
		// This is a Go-only extension. Pass uri as both title and url for
		// backward compatibility with existing Go STPP clients.
		mgpp.pm.SetURIEx(propPtr, nil, uri, uri)

	default:
		return fmt.Errorf("unknown set type: %d", setType)
	}

	return nil
}

// mgppHandleBinaryEvent handles event command in binary mode
func mgppHandleBinaryEvent(mgpp *Mgpp, data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("event data too short")
	}

	// Parse: [propRef(4), eventType(4), eventData...]
	propRef := int(binary.LittleEndian.Uint32(data[0:4]))
	eventType := int(binary.LittleEndian.Uint32(data[4:8]))
	eventData := data[8:]

	p := mgppResolvePropRef(mgpp, propRef)
	if p == nil {
		return fmt.Errorf("property reference %d not found", propRef)
	}

	em := event.NewEventManager(mgpp.pm)

	switch eventType {
	case int(event.EVENT_ACTION_VECTOR):
		// Decode string vector and convert to actions
		actions := mgppDecodeStringVector(eventData, mgpp.pm)
		if len(actions) > 0 {
			e := em.CreateActionMulti(actions)
			em.Dispatch(&e.Event)
		}

	case int(event.EVENT_OPENURL):
		if len(eventData) < 1 {
			return fmt.Errorf("openurl data too short")
		}
		flags := eventData[0]
		eventData = eventData[1:]

		args := &event.EventOpenURLArgs{}

		if flags&0x01 != 0 {
			args.URL, eventData = mgppDecodeString(eventData)
		}
		if flags&0x02 != 0 {
			args.View, eventData = mgppDecodeString(eventData)
		}
		if flags&0x04 != 0 {
			itemRef := int(binary.LittleEndian.Uint32(eventData[0:4]))
			if len(eventData) >= 4 {
				ip := mgppResolvePropRef(mgpp, itemRef)
				if ip != nil {
					args.ItemModel = ip
				}
				eventData = eventData[4:]
			}
		}
		if flags&0x08 != 0 {
			parentRef := int(binary.LittleEndian.Uint32(eventData[0:4]))
			if len(eventData) >= 4 {
				pp := mgppResolvePropRef(mgpp, parentRef)
				if pp != nil {
					args.ParentModel = pp
				}
				eventData = eventData[4:]
			}
		}
		if flags&0x10 != 0 {
			args.How, eventData = mgppDecodeString(eventData)
		}
		if flags&0x20 != 0 {
			args.ParentURL, eventData = mgppDecodeString(eventData)
		}

		e := em.CreateOpenURLArgs(args)
		em.Dispatch(&e.Event)

	default:
		return fmt.Errorf("unsupported event type: %d", eventType)
	}

	return nil
}

// mgppDecodeStringVector decodes a string vector from binary data
func mgppDecodeStringVector(data []byte, pm *prop.PropManager) []event.ActionType {
	var actions []event.ActionType
	offset := 0

	for offset < len(data) {
		if offset+4 > len(data) {
			break
		}
		strLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		offset += 4

		if offset+strLen > len(data) {
			break
		}
		str := string(data[offset : offset+strLen])
		offset += strLen

		em := event.NewEventManager(pm)
		action := em.ActionStr2Code(str)
		if action != event.ACTION_invalid {
			actions = append(actions, action)
		}
	}

	return actions
}

// mgppDecodeString decodes a string from binary data
func mgppDecodeString(data []byte) (string, []byte) {
	if len(data) < 4 {
		return "", data
	}
	strLen := int(binary.LittleEndian.Uint32(data[0:4]))
	if len(data) < 4+strLen {
		return "", data
	}
	str := string(data[4 : 4+strLen])
	return str, data[4+strLen:]
}

// mgppHandleBinaryReqMove handles req_move command in binary mode
func mgppHandleBinaryReqMove(mgpp *Mgpp, data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("req_move data too short")
	}

	propRef := int(binary.LittleEndian.Uint32(data[0:4]))
	p := mgppResolvePropRef(mgpp, propRef)
	if p == nil {
		return fmt.Errorf("property reference %d not found", propRef)
	}

	propPtr, ok := p.(*prop.Prop)
	if !ok {
		return fmt.Errorf("invalid property type")
	}

	var before *prop.Prop
	if len(data) >= 8 {
		beforeRef := int(binary.LittleEndian.Uint32(data[4:8]))
		if beforeRef != 0 {
			bp := mgppResolvePropRef(mgpp, beforeRef)
			if bp != nil {
				before, _ = bp.(*prop.Prop)
			}
		}
	}

	// Call prop_req_move
	mgpp.pm.ReqMove(propPtr, before)
	_ = propPtr
	_ = before
	return nil
}

// mgppHandleBinaryWantMoreChilds handles want_more_childs command in binary mode
func mgppHandleBinaryWantMoreChilds(mgpp *Mgpp, data []byte) error {
	if len(data) != 4 {
		return fmt.Errorf("want_more_childs data must be 4 bytes")
	}

	id := binary.LittleEndian.Uint32(data[0:4])
	mgpp.mu.Lock()
	sub, exists := mgpp.Subscriptions[id]
	mgpp.mu.Unlock()

	if !exists {
		return nil
	}

	// Call prop_want_more_childs if we have a subscription
	if sub.Sub != nil {
		if propSub, ok := sub.Sub.(*prop.Subscription); ok {
			mgpp.pm.WantMoreChilds(propSub)
		}
	}

	return nil
}

// mgppHandleBinarySelect handles select command in binary mode
func mgppHandleBinarySelect(mgpp *Mgpp, data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("select data too short")
	}

	propRef := int(binary.LittleEndian.Uint32(data[0:4]))
	p := mgppResolvePropRef(mgpp, propRef)
	if p == nil {
		return fmt.Errorf("property reference %d not found", propRef)
	}

	propPtr, ok := p.(*prop.Prop)
	if !ok {
		return fmt.Errorf("invalid property type")
	}

	// Select property
	mgpp.pm.SelectChild(propPtr, "")
	return nil
}

// mgppHandleBinaryImageLoad handles image load command
func mgppHandleBinaryImageLoad(mgpp *Mgpp, data []byte) error {
	if len(data) < 16 {
		return fmt.Errorf("image load data too short")
	}

	id := binary.LittleEndian.Uint32(data[0:4])
	flags := binary.LittleEndian.Uint32(data[4:8])
	reqWidth := binary.LittleEndian.Uint32(data[8:12])
	reqHeight := binary.LittleEndian.Uint32(data[12:16])
	url := string(data[16:])

	mgpp.mu.Lock()
	defer mgpp.mu.Unlock()

	req := &MgppImageReq{
		ID:        id,
		URL:       url,
		ReqWidth:  reqWidth,
		ReqHeight: reqHeight,
		Flags:     flags,
		Mgpp:      mgpp,
	}
	mgpp.ImageReqs[id] = req

	// Load image asynchronously
	go mgppImageReqDo(req)

	return nil
}

// mgppHandleBinaryImageCancel handles image cancel command
func mgppHandleBinaryImageCancel(mgpp *Mgpp, data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("image cancel data too short")
	}

	id := binary.LittleEndian.Uint32(data[0:4])

	mgpp.mu.Lock()
	defer mgpp.mu.Unlock()

	if req, exists := mgpp.ImageReqs[id]; exists {
		// Cancel the request using context cancellation
		if req.Cancel != nil {
			req.Cancel()
		}
		delete(mgpp.ImageReqs, id)
	}

	return nil
}

// mgppImageReqDo loads an image and sends the response
func mgppImageReqDo(req *MgppImageReq) {
	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	req.Cancel = cancel

	// Load image using backend imageloader
	var cacheControl int = 0

	if req.Mgpp.backendSystem == nil {
		req.ErrStr = "backend system not configured"
		mgppImageReqSendFail(req)
		return
	}
	result, _ := req.Mgpp.backendSystem.Imageloader(req.URL, ctx, &cacheControl, nil, nil)

	req.Mgpp.mu.Lock()
	defer req.Mgpp.mu.Unlock()

	if result == nil {
		req.ErrStr = "Failed to load image"
		mgppImageReqSendFail(req)
		return
	}

	// Try to extract image data from result
	// The result type depends on backend implementation
	if imageData, ok := result.([]byte); ok {
		req.ImageData = imageData
		req.Width = uint16(req.ReqWidth)
		req.Height = uint16(req.ReqHeight)
		req.ColorPlanes = 4 // RGBA
		mgppImageReqSendReply(req)
	} else {
		req.ErrStr = "Unsupported image format"
		mgppImageReqSendFail(req)
	}
}

// mgppImageReqSendReply sends a successful image reply
func mgppImageReqSendReply(req *MgppImageReq) {
	if req.Mgpp == nil || req.Mgpp.Conn == nil {
		return
	}

	// Build reply: [STPPCmdImageReply, id(4), width(2), height(2), flags(2), colorPlanes(1), imageData...]
	buf := make([]byte, 12+len(req.ImageData))
	buf[0] = MGPPCmdImageReply
	binary.LittleEndian.PutUint32(buf[1:5], req.ID)
	binary.LittleEndian.PutUint16(buf[5:7], req.Width)
	binary.LittleEndian.PutUint16(buf[7:9], req.Height)
	binary.LittleEndian.PutUint16(buf[9:11], uint16(req.Flags))
	buf[11] = req.ColorPlanes
	copy(buf[12:], req.ImageData)

	req.Mgpp.Conn.WebSocketSend(2, buf, len(buf))
}

// mgppImageReqSendFail sends a failed image reply
func mgppImageReqSendFail(req *MgppImageReq) {
	if req.Mgpp == nil || req.Mgpp.Conn == nil {
		return
	}

	// Build fail: [STPPCmdImage_FAIL, id(4), errStr...]
	buf := make([]byte, 5+len(req.ErrStr))
	buf[0] = MGPPCmdImageFail
	binary.LittleEndian.PutUint32(buf[1:5], req.ID)
	copy(buf[5:], req.ErrStr)

	req.Mgpp.Conn.WebSocketSend(2, buf, len(buf))
}

// mgppSubBinary is the binary subscription callback
// mgppSubBinary is the binary subscription callback
// C: stpp_sub_binary (stpp.c:357-541) — binary notify layout:
// buf[0] = STPP_CMD_NOTIFY, buf[1] = notify type, wr32_le(buf+2, ss_id),
// payload at buf+6. buflen base = 1+1+4. Sent with websocket opcode 2.
func mgppSubBinary(sub *MgppSubscription, event any, args ...any) {
	if sub.Mgpp == nil || sub.Mgpp.Conn == nil {
		return
	}
	hc := sub.Mgpp.Conn

	var notifyType int
	switch ev := event.(type) {
	case int:
		notifyType = ev
	case prop.EventType:
		notifyType = int(ev)
	default:
		return
	}

	buflen := 1 + 1 + 4
	var buf []byte

	switch prop.EventType(notifyType) {
	case prop.EventSetInt:
		if len(args) == 0 {
			return
		}
		v, _ := args[0].(int)
		buflen += 4
		buf = make([]byte, buflen)
		buf[1] = MGPPSetInt
		binary.LittleEndian.PutUint32(buf[6:], uint32(v))
		ssClearProps(sub, true)
	case prop.EventSetFloat:
		if len(args) == 0 {
			return
		}
		var f float32
		switch v := args[0].(type) {
		case float32:
			f = v
		case float64:
			f = float32(v)
		}
		buflen += 4
		buf = make([]byte, buflen)
		buf[1] = MGPPSetFloat
		binary.LittleEndian.PutUint32(buf[6:], math.Float32bits(f))
		ssClearProps(sub, true)
	case prop.EventSetVoid:
		buf = make([]byte, buflen)
		buf[1] = MGPPSetVoid
		ssClearProps(sub, true)
	case prop.EventSetDir:
		buf = make([]byte, buflen)
		buf[1] = MGPPSetDir
		ssClearProps(sub, true)
	case prop.EventSetRString, prop.EventSetCString:
		if len(args) == 0 {
			return
		}
		str, _ := args[0].(string)
		buflen += len(str) + 1
		buf = make([]byte, buflen)
		buf[1] = MGPPSetString
		// C: buf[6] = event == PROP_SET_RSTRING ? va_arg(ap, int) : 0
		if prop.EventType(notifyType) == prop.EventSetRString && len(args) > 1 {
			if st, ok := args[1].(int); ok {
				buf[6] = byte(st)
			}
		}
		copy(buf[7:], str)
		ssClearProps(sub, true)

	case prop.EventAddChild:
		// C: wr32_le(buf+6, stpp_property_export_from_sub(...)->sp_id)
		// flags & PROP_ADD_SELECTED → STPP_ADD_CHILD_SELECTED else
		// STPP_ADD_CHILDS. Go EventAddChild args are (child, parent);
		// the C flag is gen_add_flags = (child == parent->hp_selected).
		if len(args) == 0 {
			return
		}
		p, _ := args[0].(*prop.Prop)
		var parent *prop.Prop
		if len(args) > 1 {
			parent, _ = args[1].(*prop.Prop)
		}
		buflen += 4
		buf = make([]byte, buflen)
		binary.LittleEndian.PutUint32(buf[6:], mgppPropertyExport(sub, p, true).ID)
		if parent != nil && parent.GetSelectedChild() == p {
			buf[1] = MGPPAddChildSelected
		} else {
			buf[1] = MGPPAddChilds
		}
	case prop.EventAddChildBefore:
		// Go args are (child, parent, before); C passes (p, before).
		if len(args) == 0 {
			return
		}
		p, _ := args[0].(*prop.Prop)
		var before *prop.Prop
		if len(args) > 2 {
			before, _ = args[2].(*prop.Prop)
		}
		buflen += 8
		buf = make([]byte, buflen)
		binary.LittleEndian.PutUint32(buf[6:], spGet(before, sub).ID)
		binary.LittleEndian.PutUint32(buf[10:], mgppPropertyExport(sub, p, true).ID)
		buf[1] = MGPPAddChildsBefore
	case prop.EventAddChildVector, prop.EventAddChildVectorDirect:
		if len(args) == 0 {
			return
		}
		pv, _ := args[0].([]*prop.Prop)
		buflen += len(pv) * 4
		buf = make([]byte, buflen)
		for i, p := range pv {
			binary.LittleEndian.PutUint32(buf[6+i*4:], mgppPropertyExport(sub, p, true).ID)
		}
		buf[1] = MGPPAddChilds
	case prop.EventAddChildVectorBefore:
		// Go args are (children, before); C passes (pv, before).
		if len(args) == 0 {
			return
		}
		pv, _ := args[0].([]*prop.Prop)
		var before *prop.Prop
		if len(args) > 1 {
			before, _ = args[1].(*prop.Prop)
		}
		buflen += len(pv)*4 + 4
		buf = make([]byte, buflen)
		binary.LittleEndian.PutUint32(buf[6:], spGet(before, sub).ID)
		for i, p := range pv {
			binary.LittleEndian.PutUint32(buf[10+i*4:], mgppPropertyExport(sub, p, true).ID)
		}
		buf[1] = MGPPAddChildsBefore
	case prop.EventDelChild:
		if len(args) == 0 {
			return
		}
		p, _ := args[0].(*prop.Prop)
		spv := sub.Mgpp.pm.TagClear(p, sub)
		sp, _ := spv.(*MgppProp)
		if sp == nil {
			return
		}
		buflen += 4
		buf = make([]byte, buflen)
		binary.LittleEndian.PutUint32(buf[6:], sp.ID)
		buf[1] = MGPPDelChild
		mgppPropertyUnexportFromSub(sub, sp)
	case prop.EventMoveChild:
		// Go args are (child, parent, before); C passes (p, before).
		if len(args) == 0 {
			return
		}
		p, _ := args[0].(*prop.Prop)
		var before *prop.Prop
		if len(args) > 2 {
			before, _ = args[2].(*prop.Prop)
		}
		if before != nil {
			buflen += 8
		} else {
			buflen += 4
		}
		buf = make([]byte, buflen)
		binary.LittleEndian.PutUint32(buf[6:], spGet(p, sub).ID)
		if before != nil {
			binary.LittleEndian.PutUint32(buf[10:], spGet(before, sub).ID)
		}
		buf[1] = MGPPMoveChild
	case prop.EventSelectChild:
		if len(args) == 0 {
			return
		}
		p, _ := args[0].(*prop.Prop)
		buflen += 4
		buf = make([]byte, buflen)
		binary.LittleEndian.PutUint32(buf[6:], spGet(p, sub).ID)
		buf[1] = MGPPSelectChild
	case prop.EventValueProp:
		if len(args) == 0 {
			return
		}
		p, _ := args[0].(*prop.Prop)
		// C: skip if the head of ss_value_props already wraps p
		if len(sub.ValueProps) > 0 && sub.ValueProps[0].Prop == p {
			return
		}
		ssClearProps(sub, false)
		buflen += 4
		buf = make([]byte, buflen)
		binary.LittleEndian.PutUint32(buf[6:], mgppPropertyExport(sub, p, false).ID)
		buf[1] = MGPPValueProp
	case prop.EventWantMoreChilds:
		return
	case prop.EventHaveMoreChildsYes:
		buf = make([]byte, buflen)
		buf[1] = MGPPHaveMoreChildsYes
	case prop.EventHaveMoreChildsNo:
		buf = make([]byte, buflen)
		buf[1] = MGPPHaveMoreChildsNo
	default:
		// C: printf("STPP SUB BINARY cant handle event %d\n", event)
		return
	}

	if buf == nil {
		return
	}
	buf[0] = MGPPCmdNotify
	binary.LittleEndian.PutUint32(buf[2:], sub.ID)
	hc.WebSocketSend(2, buf, len(buf))
}

// decodeStringVector decodes a string vector from binary data
func decodeStringVector(data []byte) []string {
	var result []string
	offset := 0

	for offset < len(data) {
		length := int(data[offset])
		offset++
		if length == 0 {
			break
		}
		if offset+length > len(data) {
			break
		}
		result = append(result, string(data[offset:offset+length]))
		offset += length
	}

	return result
}
