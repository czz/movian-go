package mgpp

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/czz/movian-go/internal/htsmsg"
	navcore "github.com/czz/movian-go/internal/navigator"
	prop "github.com/czz/movian-go/internal/prop"
)

// BeMgppCanHandle — C: be_stpp_canhandle (stpp.c:1369-1372).
// "stpp:" stays for backwards compatibility; "mgpp:" is an alias.
func (c *MGPPClient) BeMgppCanHandle(url string) int {
	if strings.HasPrefix(url, "stpp:") || strings.HasPrefix(url, "mgpp:") {
		return 1
	}
	return 0
}

// BeMgppOpen — C: be_stpp_open (stpp.c:1297-1362).
// stpp://host:port sets model.remoteurl directly; stpp:id:<hex>
// resolves a discovered controllee to its announced address.
func (c *MGPPClient) BeMgppOpen(page any, url string, sync bool) error {
	pp, _ := page.(*prop.Prop)
	if pp == nil {
		return nil
	}

	var model *prop.Prop

	if strings.HasPrefix(url, "stpp://") || strings.HasPrefix(url, "mgpp://") {
		model = c.pm.CreateEx(pp, "model", nil, false, false)
		c.pm.SetStringEx(c.pm.CreateEx(model, "remoteurl", nil, false, false),
			nil, url, prop.StringUTF8)
	}

	// C: if(!strncmp(url + 5, "id:", 3)) — a second, non-else branch
	if len(url) > 5 && strings.HasPrefix(url[5:], "id:") {
		idBytes, err := hex.DecodeString(url[8:])
		if err != nil || len(idBytes) != 16 {
			// C: nav_open_error(page, "Bad URL")
			navcore.OpenError(c.pm, pp, "Bad URL")
			return nil
		}
		var id [16]byte
		copy(id[:], idBytes)

		c.mu.Lock()
		var sc *mgppControllee
		for _, x := range c.controllees {
			if x.id == id {
				sc = x
				break
			}
		}

		if sc == nil {
			// C: nav_open_errorf(page, "Device not available")
			c.mu.Unlock()
			navcore.OpenError(c.pm, pp, "Device not available")
			return nil
		}
		newurl := fmt.Sprintf("stpp://%s:%d", sc.addr.IP.String(), sc.addr.Port)
		c.mu.Unlock()

		model = c.pm.CreateEx(pp, "model", nil, false, false)
		c.pm.SetStringEx(c.pm.CreateEx(model, "remoteurl", nil, false, false),
			nil, newurl, prop.StringUTF8)
	}

	if model != nil {
		// C: prop_set(model, "type", PROP_SET_STRING, "stpp")
		c.pm.SetStringEx(c.pm.CreateEx(model, "type", nil, false, false),
			nil, "stpp", prop.StringUTF8)
	}
	return nil
}

// mgppPropertyExport exports a property and assigns it an ID
// C: stpp_property_export_from_sub (stpp.c:114) — prop_ref_inc(p),
// insert into the per-subscription list, register in stpp_props and
// prop_tag_set(p, ss, sp).
func mgppPropertyExport(sub *MgppSubscription, p any, dirMode bool) *MgppProp {
	mgpp := sub.Mgpp
	mgpp.mu.Lock()
	defer mgpp.mu.Unlock()

	mgpp.PropTally++
	sp := &MgppProp{
		ID:   mgpp.PropTally,
		Prop: p,
		Sub:  sub,
	}

	mgpp.Props[sp.ID] = sp

	if pp, ok := p.(*prop.Prop); ok && pp != nil {
		mgpp.pm.RefInc(pp)          // C: prop_ref_inc(p)
		mgpp.pm.TagSet(pp, sub, sp) // C: prop_tag_set(p, ss, sp)
	}

	if dirMode {
		sub.DirProps = append(sub.DirProps, sp)
	} else {
		sub.ValueProps = append(sub.ValueProps, sp)
	}

	return sp
}

// mgppPropertyUnexportFromSub — C: stpp_property_unexport_from_sub
// (stpp.c:134). prop_ref_dec + remove from sub list + remove from
// stpp_props. Callers clear the prop tag first (prop_tag_clear).
func mgppPropertyUnexportFromSub(sub *MgppSubscription, sp *MgppProp) {
	mgpp := sub.Mgpp
	mgpp.mu.Lock()
	defer mgpp.mu.Unlock()

	if pp, ok := sp.Prop.(*prop.Prop); ok && pp != nil {
		mgpp.pm.RefDec(pp)
	}
	delete(mgpp.Props, sp.ID)
	sub.DirProps = removeMgppProp(sub.DirProps, sp)
	sub.ValueProps = removeMgppProp(sub.ValueProps, sp)
}

// removeMgppProp removes sp from a stpp_prop_list slice
// (C: LIST_REMOVE(sp, sp_sub_link)).
func removeMgppProp(list []*MgppProp, sp *MgppProp) []*MgppProp {
	for i, x := range list {
		if x == sp {
			return slices.Delete(list, i, i+1)
		}
	}
	return list
}

// spGet returns the exported prop for p under this subscription.
// C: sp_get (prop_tag_get + abort on NULL).
func spGet(p *prop.Prop, sub *MgppSubscription) *MgppProp {
	if p == nil {
		return nil
	}
	sub.Mgpp.mu.Lock()
	defer sub.Mgpp.mu.Unlock()
	sp, _ := sub.Mgpp.pm.TagGet(p, sub).(*MgppProp)
	return sp
}

// ssClearProps unexports all props in a per-subscription list.
// C: ss_clear_props (stpp.c:239) — prop_tag_clear + unexport each.
func ssClearProps(sub *MgppSubscription, dirMode bool) {
	var list []*MgppProp
	if dirMode {
		list = sub.DirProps
		sub.DirProps = nil
	} else {
		list = sub.ValueProps
		sub.ValueProps = nil
	}
	for _, sp := range list {
		if pp, ok := sp.Prop.(*prop.Prop); ok && pp != nil {
			sub.Mgpp.pm.TagClear(pp, sub) // C: prop_tag_clear(sp->sp_prop, ss)
		}
		mgppPropertyUnexportFromSub(sub, sp)
	}
}

// MgppHandleJSON handles STPP JSON messages
func MgppHandleJSON(mgpp *Mgpp, msg *htsmsg.HTSMsg) error {
	if msg == nil {
		return fmt.Errorf("nil message")
	}

	// Get command from array index 0
	fields := msg.GetFields()
	if len(fields) == 0 {
		return fmt.Errorf("empty message")
	}

	cmd := fields[0].GetS64Value()

	switch int(cmd) {
	case MGPPCmdSubscribe:
		if len(fields) < 4 {
			return fmt.Errorf("subscribe requires 4 fields")
		}
		id := uint32(fields[1].GetS64Value())
		propRef := int(fields[2].GetS64Value())
		path := fields[3].GetStrValue()
		return mgppCmdSub(mgpp, id, propRef, path, 0, nil, mgppSubJSON)

	case MGPPCmdUnsubscribe:
		if len(fields) < 2 {
			return fmt.Errorf("unsubscribe requires 2 fields")
		}
		id := uint32(fields[1].GetS64Value())
		return mgppCmdUnsub(mgpp, id)

	case MGPPCmdSet:
		if len(fields) < 4 {
			return fmt.Errorf("set requires 4 fields")
		}
		propRef := int(fields[1].GetS64Value())
		path := fields[2].GetStrValue()
		field := fields[3]
		return mgppCmdSet(mgpp, propRef, path, field)

	default:
		return fmt.Errorf("unknown JSON command: %d", cmd)
	}
}

// MgppHandleBinary handles STPP binary messages
func MgppHandleBinary(mgpp *Mgpp, client *MGPPClient, data []byte) error {
	if len(data) < 1 {
		return fmt.Errorf("data too short")
	}

	cmd := data[0]
	data = data[1:]

	switch cmd {
	case MGPPCmdHello:
		return mgppHandleHello(mgpp, client, data)

	case MGPPCmdSubscribe:
		return mgppHandleBinarySub(mgpp, data)

	case MGPPCmdUnsubscribe:
		return mgppHandleBinaryUnsub(mgpp, data)

	case MGPPCmdSet:
		return mgppHandleBinarySet(mgpp, data)

	case MGPPCmdEvent:
		return mgppHandleBinaryEvent(mgpp, data)

	case MGPPCmdReqMove:
		return mgppHandleBinaryReqMove(mgpp, data)

	case MGPPCmdWantMoreChilds:
		return mgppHandleBinaryWantMoreChilds(mgpp, data)

	case MGPPCmdSelect:
		return mgppHandleBinarySelect(mgpp, data)

	case MGPPCmdImageLoad:
		return mgppHandleBinaryImageLoad(mgpp, data)

	case MGPPCmdImageCancel:
		return mgppHandleBinaryImageCancel(mgpp, data)

	default:
		return fmt.Errorf("unknown binary command: %d", cmd)
	}
}

// MgppSendHello sends a hello message
func MgppSendHello(mgpp *Mgpp, client *MGPPClient) error {
	if mgpp.Conn == nil {
		return fmt.Errorf("no connection")
	}

	// Build hello message: [STPPCmdHello, STPPVersion, instanceID(16 bytes), flags(1 byte)]
	buf := make([]byte, 1+1+16+1)
	buf[0] = MGPPCmdHello
	buf[1] = MGPPVersion
	// Add instance ID (hex decoded to bytes)
	instanceBytes, _ := hex.DecodeString(client.instanceID)
	copy(buf[2:18], instanceBytes)
	buf[18] = 0 // flags

	// Send via websocket
	mgpp.Conn.WebSocketSend(2, buf, len(buf))
	return nil
}

// MgppSet sets a property value
func MgppSet(mgpp *Mgpp, propRef int, path string, value any) error {
	mgpp.mu.Lock()
	defer mgpp.mu.Unlock()

	// Resolve propRef to actual property (mutex already held)
	p := mgppResolvePropRefLocked(mgpp, propRef)
	if p == nil {
		return fmt.Errorf("property reference %d not found", propRef)
	}

	// Convert any to prop.Prop for type assertion
	propPtr, ok := p.(*prop.Prop)
	if !ok {
		return fmt.Errorf("invalid property type")
	}

	// C: stpp_cmd_set → prop_setdn(NULL, p, path, PROP_SET_*, v) —
	// walks the dotted path, creating intermediate children.
	switch v := value.(type) {
	case int:
		mgpp.pm.Setdn(nil, propPtr, path, prop.EventSetInt, v)
	case int64:
		mgpp.pm.Setdn(nil, propPtr, path, prop.EventSetInt, int(v))
	case string:
		mgpp.pm.Setdn(nil, propPtr, path, prop.EventSetRString, v)
	case float64:
		mgpp.pm.Setdn(nil, propPtr, path, prop.EventSetFloat, float32(v))
	case float32:
		mgpp.pm.Setdn(nil, propPtr, path, prop.EventSetFloat, v)
	default:
		return fmt.Errorf("unsupported value type: %T", value)
	}

	return nil
}

// mgppResolvePropRef resolves a property reference to a property
func mgppResolvePropRef(mgpp *Mgpp, propRef int) any {
	if propRef == 0 {
		// Global property reference
		return mgpp.pm.GetGlobal()
	}

	mgpp.mu.Lock()
	defer mgpp.mu.Unlock()
	return mgppResolvePropRefLocked(mgpp, propRef)
}

// mgppResolvePropRefLocked resolves a property reference assuming the mutex is already held
func mgppResolvePropRefLocked(mgpp *Mgpp, propRef int) any {
	if propRef == 0 {
		// Global property reference
		return mgpp.pm.GetGlobal()
	}

	for _, sp := range mgpp.Props {
		if int(sp.ID) == propRef {
			return sp.Prop
		}
	}

	return nil
}

// mgppCmdSub handles subscribe command
func mgppCmdSub(mgpp *Mgpp, id uint32, propRef int, path string, flags uint16, nameVec []string, notifyFunc func(*MgppSubscription, any, ...any)) error {
	mgpp.mu.Lock()
	defer mgpp.mu.Unlock()

	if _, exists := mgpp.Subscriptions[id]; exists {
		return fmt.Errorf("subscription ID %d already exists", id)
	}

	// Resolve property reference (mutex already held)
	// C: stpp_cmd_sub → resolve_propref(stpp, propref) → prop_t *p
	p := mgppResolvePropRefLocked(mgpp, propRef)
	if p == nil {
		return fmt.Errorf("property reference %d not found", propRef)
	}

	propPtr, ok := p.(*prop.Prop)
	if !ok {
		return fmt.Errorf("invalid property type")
	}

	// C: prop_subscribe with PROP_SUB_ALT_PATH resolves the path from the
	// root prop (PROP_TAG_ROOT) using prop_subfind (prop_core.c:2682-2740).
	// The path components come from PROP_TAG_NAMESTR (dotted path) or
	// PROP_TAG_NAME_VECTOR (string array).
	//
	// C JSON: path="metadata.title" → name=["metadata","title"] → traverse
	// C binary: nameVec=["metadata","title"] → traverse
	//
	// Go: Pre-resolve the target prop from the root using ResolvePath,
	// then subscribe to the resolved prop. This is equivalent to C's
	// ALT_PATH resolution as long as ResolvePath produces the same target.
	var components []string
	if len(nameVec) > 0 {
		// Binary protocol: nameVec is the path components
		components = nameVec
	} else if path != "" {
		// JSON protocol: path is a dotted path
		components = strings.Split(path, ".")
	}

	targetProp := propPtr
	if len(components) > 0 {
		// C: prop_subfind with follow_symlinks=1 for value resolution
		targetProp = propPtr.ResolvePath(components, true)
		if targetProp == nil {
			return fmt.Errorf("path resolution failed for propRef %d, path %v", propRef, components)
		}
	}

	// Create STPP subscription
	sub := &MgppSubscription{
		ID:     id,
		Active: true,
		Mgpp:   mgpp,
	}
	mgpp.Subscriptions[id] = sub

	// Subscribe to the RESOLVED target prop (not the root)
	// C: prop_subscribe subscribes to the resolved value prop on
	// asyncio_courier (stpp.c:565 PROP_TAG_COURIER).
	subArgs := []any{int(flags)}
	if mgpp.courier != nil {
		subArgs = append(subArgs, prop.SubCourier{C: mgpp.courier})
	}
	propSub := targetProp.Subscribe(func(opaque any, event prop.EventType, args ...any) {
		notifyFunc(sub, int(event), args...)
	}, sub, subArgs...)
	sub.Sub = propSub

	return nil
}

// ssDestroy destroys a subscription.
// C: ss_destroy (stpp.c:578-585) — ss_clear_props on both lists,
// prop_unsubscribe, remove from stpp_subscriptions.
// Must be called WITHOUT stpp.mu held (unexport takes the lock).
func ssDestroy(mgpp *Mgpp, sub *MgppSubscription) {
	ssClearProps(sub, true)  // C: ss_clear_props(ss, &ss->ss_dir_props)
	ssClearProps(sub, false) // C: ss_clear_props(ss, &ss->ss_value_props)
	if sub.Sub != nil {
		if propSub, ok := sub.Sub.(*prop.Subscription); ok {
			mgpp.pm.Unsubscribe(propSub) // C: prop_unsubscribe(ss->ss_sub)
		}
	}
	sub.Active = false
}

// mgppCmdUnsub handles unsubscribe command
// C: stpp_cmd_unsub (stpp.c:592-600) — RB_FIND + ss_destroy.
func mgppCmdUnsub(mgpp *Mgpp, id uint32) error {
	mgpp.mu.Lock()
	sub, exists := mgpp.Subscriptions[id]
	if exists {
		delete(mgpp.Subscriptions, id)
	}
	mgpp.mu.Unlock()

	if !exists {
		return nil // C: silently ignore non-existent subscriptions
	}
	ssDestroy(mgpp, sub)
	return nil
}

// mgppCmdSet handles set command
func mgppCmdSet(mgpp *Mgpp, propRef int, path string, field *htsmsg.HTSMsgField) error {
	if path == "" || field == nil {
		return nil
	}

	switch field.GetType() {
	case htsmsg.HmfS64:
		return MgppSet(mgpp, propRef, path, field.GetS64Value())
	case htsmsg.HmfStr:
		return MgppSet(mgpp, propRef, path, field.GetStrValue())
	case htsmsg.HmfDbl:
		return MgppSet(mgpp, propRef, path, field.GetDblValue())
	}
	return nil
}

// mgppHandleHello handles hello command in binary mode
func mgppHandleHello(mgpp *Mgpp, client *MGPPClient, data []byte) error {
	if len(data) < 2 {
		return fmt.Errorf("hello data too short")
	}

	version := data[0]
	if version != MGPPVersion {
		return fmt.Errorf("unsupported MGPP version: %d (expected %d)", version, MGPPVersion)
	}

	// flags := data[1]

	if err := MgppSendHello(mgpp, client); err != nil {
		return err
	}
	mgpp.HelloedOK = true
	return nil
}
