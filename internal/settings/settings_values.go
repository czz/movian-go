package settings

import (
	"fmt"
	"strings"

	"github.com/czz/movian-go/internal/db/kvstore"
	eventpkg "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	propcore "github.com/czz/movian-go/internal/prop"
)

// subscribe subscribes cb to prop, dispatching through courier if set.
// C: prop_subscribe(flags, PROP_TAG_CALLBACK, cb, opaque, PROP_TAG_ROOT, p,
//
//	PROP_TAG_COURIER, pc, PROP_TAG_MUTEX, mtx, PROP_TAG_LOCKMGR, lockmgr, NULL)
func (sm *SettingsManager) subscribe(prop *propcore.Prop, courier *propcore.Courier,
	cb func(any, propcore.EventType, ...any), opaque any,
	mtx any, lockmgr *propcore.Lockmgr,
	args ...any) *propcore.Subscription {
	// C: PROP_TAG_MUTEX + PROP_TAG_LOCKMGR are passed on every settings
	// subscription — they set hps_lock/hps_lockmgr regardless of mode.
	if mtx != nil {
		args = append(args, propcore.SubMutex{Ptr: mtx})
	}
	if lockmgr != nil {
		args = append(args, propcore.SubLockmgr{L: lockmgr})
	}
	if courier != nil {
		return sm.pm.SubscribeWithCourier(prop, courier, cb, opaque, args...)
	}
	return prop.Subscribe(cb, opaque, args...)
}

// settingsIntSetValue pushes an int/bool value through a setting:
// invokes the user callback, writes the external prop (WriteProp),
// persists to the store, and updates the origin prop.
// C: settings_int_set_value (settings.c:418-447)
func (sm *SettingsManager) settingsIntSetValue(s *Setting, v int) {
	s.mutex.Lock()
	cb := s.callback
	s.mutex.Unlock()

	s.valueSet.Store(true)

	if cb != nil {
		cb(s.opaque, v)
	}

	if s.extValue != nil {
		sm.pm.SetIntEx(s.extValue, nil, v)
	}

	if s.flags&SettingsDebug != 0 {
		sm.ts.Debug("SETTINGS", "Value set to %d\n", v)
	}

	if !s.enableWriteback {
		return
	}

	switch s.storeType {
	case SettingStoreTypeSimple:
		if st := s.storeRef(); st != nil {
			st.Set(s.storeName, s.id, htsmsg.HmfS64, int64(v))
		}
	case SettingStoreTypeKVStore:
		// C: kv_url_opt_set(s_store_name, KVSTORE_DOMAIN_SETTING, s_id,
		//                   KVSTORE_SET_INT, v)
		s.mgr.kvstore.UrlOptSet(s.storeName, kvstore.DomainSetting, s.id,
			kvstore.SetInt, v)
	}

	if s.currentOrigin != nil {
		sm.pm.SetStringEx(s.currentOrigin, nil, s.origin, propcore.StringUTF8)
	}
}

// settingsIntCallback is the prop-subscription callback for int/bool
// settings. C: settings_int_callback (settings.c:455-478)
func (sm *SettingsManager) settingsIntCallback(opaque any, eventType propcore.EventType, args ...any) {
	s := opaque.(*Setting)

	switch eventType {
	case propcore.EventSetInt:
		if len(args) > 0 {
			if v, ok := args[0].(int); ok {
				sm.settingsIntSetValue(s, v)
			}
		}
	case propcore.EventSetFloat:
		// C: settings_int_set_value(s, va_arg(ap, double)) — the Go
		// notification carries float32.
		if len(args) > 0 {
			switch v := args[0].(type) {
			case float32:
				sm.settingsIntSetValue(s, int(v))
			case float64:
				sm.settingsIntSetValue(s, int(v))
			}
		}
	case propcore.EventExtEvent:
		// C: event_is_action(e, ACTION_RESET) → setting_reset(s)
		// Ext events carry an *event.Event; check for reset action.
		if len(args) > 0 {
			if sm.isResetAction(args[0]) {
				s.settingReset()
			}
		}
	}
}

// settingsStringSetValue pushes a string value through a setting.
// C: settings_string_set_value (settings.c:523-554)
func (sm *SettingsManager) settingsStringSetValue(s *Setting, rstr string) {
	s.mutex.Lock()
	cb := s.callback
	s.mutex.Unlock()

	outval := rstr

	s.valueSet.Store(true)

	s.mutex.Lock()
	outvalDefault := s.defaultStr
	s.mutex.Unlock()

	if rstr == "" {
		outval = outvalDefault
	}

	if cb != nil {
		cb(s.opaque, outval)
	}

	if s.extValue != nil {
		sm.pm.SetStringEx(s.extValue, nil, outval, propcore.StringUTF8)
	}

	if !s.enableWriteback {
		return
	}

	switch s.storeType {
	case SettingStoreTypeSimple:
		if st := s.storeRef(); st != nil {
			st.Set(s.storeName, s.id, htsmsg.HmfStr, rstr)
		}
	case SettingStoreTypeKVStore:
		// C: kv_url_opt_set(..., rstr ? KVSTORE_SET_STRING : KVSTORE_SET_VOID, rstr)
		if rstr == "" {
			s.mgr.kvstore.UrlOptSet(s.storeName, kvstore.DomainSetting, s.id,
				kvstore.SetVoid, nil)
		} else {
			s.mgr.kvstore.UrlOptSet(s.storeName, kvstore.DomainSetting, s.id,
				kvstore.SetString, rstr)
		}
	}
}

// settingsStringCallback is the prop-subscription callback for string
// settings. C: settings_string_callback (settings.c:561-581)
func (sm *SettingsManager) settingsStringCallback(opaque any, eventType propcore.EventType, args ...any) {
	s := opaque.(*Setting)

	switch eventType {
	case propcore.EventSetRString:
		if len(args) > 0 {
			if v, ok := args[0].(string); ok {
				sm.settingsStringSetValue(s, v)
			}
		}
	case propcore.EventExtEvent:
		if len(args) > 0 {
			if sm.isResetAction(args[0]) {
				s.settingReset()
			}
		}
	}
}

// settingsIntInheritedValue is invoked when a parent/inherited value
// changes and this setting has no locally-set value yet.
// C: settings_int_inherited_value (settings.c:483-503)
func (sm *SettingsManager) settingsIntInheritedValue(opaque any, v int) {
	s := opaque.(*Setting)

	if s.valueSet.Load() {
		return
	}

	if s.flags&SettingsDebug != 0 {
		sm.ts.Debug("SETTINGS", "Value set to %d (inherited)\n", v)
	}

	s.mutex.Lock()
	cb := s.callback
	s.mutex.Unlock()

	if s.val != nil {
		sm.pm.SetIntEx(s.val, s.sub, v)
	}

	if cb != nil {
		cb(s.opaque, v)
	}

	if s.extValue != nil {
		sm.pm.SetIntEx(s.extValue, nil, v)
	}
}

// settingsIntInheritedOrigin updates the current_origin prop when the
// inherited origin changes and no value has been set locally.
// C: settings_int_inherited_origin (settings.c:508-516)
func (sm *SettingsManager) settingsIntInheritedOrigin(opaque any, origin string) {
	s := opaque.(*Setting)

	if !s.valueSet.Load() && s.currentOrigin != nil {
		sm.pm.SetStringEx(s.currentOrigin, nil, origin, propcore.StringUTF8)
	}
}

// settingsStrInheritedValue is invoked when a parent/inherited string
// value changes. C: settings_str_inherited_value (settings.c:586-611)
func (sm *SettingsManager) settingsStrInheritedValue(opaque any, v string) {
	s := opaque.(*Setting)

	s.mutex.Lock()
	s.defaultStr = v
	s.mutex.Unlock()
	if s.root != nil {
		sm.pm.SetStringEx(s.root, nil, v, propcore.StringUTF8) // "defaultValue" child set below
	}
	if s.root != nil {
		if dv := sm.pm.CreateEx(s.root, "defaultValue", nil, false, false); dv != nil {
			s.mutex.Lock()
			ds := s.defaultStr
			s.mutex.Unlock()
			sm.pm.SetStringEx(dv, nil, ds, propcore.StringUTF8)
		}
	}

	if s.valueSet.Load() {
		return
	}

	if s.flags&SettingsDebug != 0 {
		sm.ts.Debug("SETTINGS", "Value set to %s (inherited)\n", v)
	}

	if s.val != nil {
		s.pm.SetVoidEx(s.val, s.sub)
	}

	s.mutex.Lock()
	cb := s.callback
	ds := s.defaultStr
	s.mutex.Unlock()

	if cb != nil {
		cb(s.opaque, ds)
	}

	if s.extValue != nil {
		s.pm.SetStringEx(s.extValue, nil, ds, propcore.StringUTF8)
	}
}

// settingsMultioptCallback is the prop-subscription callback for
// multiopt settings. C: settings_multiopt_callback_ng (settings.c:616-678)
func (sm *SettingsManager) settingsMultioptCallback(opaque any, eventType propcore.EventType, args ...any) {
	s := opaque.(*Setting)

	switch eventType {
	case propcore.EventSelectChild:
		if s.pendingValue != "" {
			s.pendingValue = ""
		}

		var c *propcore.Prop
		if len(args) > 0 {
			c, _ = args[0].(*propcore.Prop)
		}

		if s.currentValue != nil {
			sm.pm.RefDec(s.currentValue)
		}
		if c != nil {
			s.currentValue = sm.pm.RefInc(c)
		} else {
			s.currentValue = nil
		}

		var name string
		if c != nil {
			name = sm.pm.GetName(c)
		}

		if s.extValue != nil {
			sm.pm.SetStringEx(s.extValue, nil, name, propcore.StringUTF8)
		}

		s.mutex.Lock()
		cb := s.callback
		s.mutex.Unlock()
		if cb != nil {
			cb(s.opaque, name)
		}

		if s.enableWriteback {
			switch s.storeType {
			case SettingStoreTypeSimple:
				if st := s.storeRef(); st != nil {
					st.Set(s.storeName, s.id, htsmsg.HmfStr, name)
				}
			case SettingStoreTypeKVStore:
				// C: kv_url_opt_set(..., name ? KVSTORE_SET_STRING : KVSTORE_SET_VOID, name)
				if name == "" {
					s.mgr.kvstore.UrlOptSet(s.storeName, kvstore.DomainSetting, s.id,
						kvstore.SetVoid, nil)
				} else {
					s.mgr.kvstore.UrlOptSet(s.storeName, kvstore.DomainSetting, s.id,
						kvstore.SetString, name)
				}
			}
		}

	case propcore.EventDelChild:
		if len(args) > 0 {
			if c, ok := args[0].(*propcore.Prop); ok && c == s.currentValue {
				if first := sm.pm.FirstChild(s.val); first != nil {
					sm.pm.SelectChildProp(first, nil)
					sm.pm.RefDec(first)
				}
			}
		}
	}
}

// isResetAction returns true if the ext-event arg carries an ACTION_RESET
// event. C: event_is_action(e, ACTION_RESET)
func (sm *SettingsManager) isResetAction(arg any) bool {
	// C: event_is_action(e, ACTION_RESET) — ext events carry
	// *event.Event.
	if e, ok := arg.(*eventpkg.Event); ok {
		return eventpkg.IsAction(e, eventpkg.ACTION_RESET)
	}
	return false
}

// storeRef returns the manager's store (nil when unwired — C treats an
// absent store as a no-op).
func (s *Setting) storeRef() *htsmsg.Store {
	if s.mgr == nil {
		return nil
	}
	return s.mgr.store
}

// settingReset resets a setting to its default value.
// C: setting_reset (settings.c:1225-1278)
func (s *Setting) settingReset() {
	s.valueSet.Store(false)

	switch s.storeType {
	case SettingStoreTypeSimple:
		if st := s.storeRef(); st != nil {
			// C: htsmsg_store_set(name, id, -1) — removes
			st.Set(s.storeName, s.id, -1, nil)
		}
	case SettingStoreTypeKVStore:
		// C: kv_url_opt_set(..., KVSTORE_SET_VOID)
		s.mgr.kvstore.UrlOptSet(s.storeName, kvstore.DomainSetting, s.id,
			kvstore.SetVoid, nil)
	}

	if s.parent == nil {
		switch s.settingType {
		case SettingString:
			if s.val != nil {
				s.pm.SetVoidEx(s.val, s.sub)
			}
			s.mutex.Lock()
			cb := s.callback
			ds := s.defaultStr
			s.mutex.Unlock()
			if cb != nil {
				cb(s.opaque, ds)
			}
			if s.extValue != nil {
				s.pm.SetStringEx(s.extValue, nil, ds, propcore.StringUTF8)
			}
		case SettingInt, SettingBool:
			if s.val != nil {
				s.pm.SetIntEx(s.val, s.sub, s.defaultInt)
			}
			s.mutex.Lock()
			cb := s.callback
			s.mutex.Unlock()
			if cb != nil {
				cb(s.opaque, s.defaultInt)
			}
			if s.extValue != nil {
				s.pm.SetIntEx(s.extValue, nil, s.defaultInt)
			}
		default:
			fmt.Printf("Can't reset type %d\n", s.settingType)
		}
	} else {
		// C: prop_sub_reemit(s->s_inherited_value_sub)
		if s.inheritedValueSub != nil {
			s.pm.SubReemit(s.inheritedValueSub)
		}
	}
}

// settingDetach removes the setting's root prop from its parent.
// C: setting_detach (settings.c:332-334)
func (sm *SettingsManager) settingDetach(s *Setting) {
	if s.root != nil {
		sm.pm.SetParentEx(s.root, nil, nil, "")
	}
}

// SettingCreate creates a setting with varargs.
// C: setting_create (settings.c:710-1148)
func (sm *SettingsManager) SettingCreate(settingType int, model any, flags int, args ...any) *Setting {
	s := &Setting{
		settingType: settingType,
		flags:       flags,
		origin:      "local",
		pm:          sm.pm,
		mgr:         sm,
	}

	var modelProp *propcore.Prop
	if model != nil {
		modelProp, _ = model.(*propcore.Prop)
	}

	// C: create s_root under model (or standalone if model == NULL)
	if modelProp == nil {
		s.root = sm.pm.CreateRootEx("", false)
	} else if flags&SettingsRawNodes != 0 {
		s.root = sm.pm.CreateEx(modelProp, "", nil, false, false)
	} else {
		nodes := sm.pm.CreateEx(modelProp, "nodes", nil, false, false)
		s.root = sm.pm.CreateEx(nodes, "", nil, false, false)
	}

	// C: create s_val child keyed by setting type
	switch settingType {
	case SettingInt, SettingBool, SettingString:
		s.val = sm.pm.CreateEx(s.root, "value", nil, false, false)
	case SettingMultiOpt:
		s.val = sm.pm.CreateEx(s.root, "options", nil, false, false)
	case SettingAction:
		s.val = sm.pm.CreateEx(s.root, "eventSink", nil, false, false)
	case SettingSeparator:
		// no s_val
	default:
		return nil
	}

	m := sm.pm.CreateEx(s.root, "metadata", nil, false, false)
	title := sm.pm.CreateEx(m, "title", nil, false, false)
	enabled := sm.pm.CreateEx(s.root, "enabled", nil, false, false)
	s.currentOrigin = sm.pm.CreateEx(s.root, "origin", nil, false, false)

	var pc *propcore.Courier
	var mtx any                   // C: void *mtx (SETTING_TAG_MUTEX)
	var lockmgr *propcore.Lockmgr // C: lockmgr_fn_t *lockmgr (SETTING_TAG_LOCKMGR)
	var initialValueProp *propcore.Prop
	var initialInt = 0
	var initialStr string
	min, max := 0, 100
	step := 1
	var optlist []string

	// C: va_arg tag loop
	for i := 0; i < len(args); i++ {
		tag, ok := args[i].(int)
		if !ok {
			break
		}
		if tag == 0 {
			break
		}

		switch tag {
		case SettingTagTitle:
			if i+1 < len(args) {
				if tp, ok := args[i+1].(*propcore.Prop); ok {
					sm.pm.Link(tp, title, nil, false, false)
				}
				i++
			}
		case SettingTagTitleCStr:
			if i+1 < len(args) {
				if str, ok := args[i+1].(string); ok {
					sm.pm.SetStringEx(title, nil, str, propcore.StringUTF8)
					s.title = str
				}
				i++
			}
		case SettingTagCallback:
			if i+2 < len(args) {
				// C: s->s_callback is stored unconditionally — its type is
				// setting_callback_t (void (*)(void *, const char *)). The Go
				// port's canonical callback shape is func(any,
				// any); some callers (i18n, upgrade) pass
				// func(any, string) — accept and wrap it.
				switch cb := args[i+1].(type) {
				case func(any, any):
					s.callback = cb
				case func(any, string):
					s.callback = func(o, v any) {
						if str, ok := v.(string); ok {
							cb(o, str)
						}
					}
				}
				s.opaque = args[i+2]
				i += 2
			}
		case SettingTagCourier:
			if i+1 < len(args) {
				pc, _ = args[i+1].(*propcore.Courier)
				i++
			}
		case SettingTagMutex:
			if i+1 < len(args) {
				mtx = args[i+1] // C: PROP_TAG_MUTEX — s->hps_lock
				i++
			}
		case SettingTagLockMgr:
			if i+1 < len(args) {
				lockmgr, _ = args[i+1].(*propcore.Lockmgr) // C: PROP_TAG_LOCKMGR
				i++
			}
		case SettingTagStore:
			if i+2 < len(args) {
				s.storeType = SettingStoreTypeSimple
				if str, ok := args[i+1].(string); ok {
					s.storeName = str
				}
				if str, ok := args[i+2].(string); ok {
					s.id = str
				}
				i += 2
			}
		case SettingTagValue:
			if i+1 < len(args) {
				switch settingType {
				case SettingInt, SettingBool:
					if v, ok := args[i+1].(int); ok {
						initialInt = v
					}
				case SettingString, SettingMultiOpt:
					if v, ok := args[i+1].(string); ok {
						initialStr = v
					}
				}
				i++
			}
		case SettingTagValueProp:
			if i+1 < len(args) {
				initialValueProp, _ = args[i+1].(*propcore.Prop)
				i++
			}
		case SettingTagRange:
			if i+2 < len(args) {
				if v, ok := args[i+1].(int); ok {
					min = v
				}
				if v, ok := args[i+2].(int); ok {
					max = v
				}
				s.minVal = min
				s.maxVal = max
				// C: prop_set_int_clipping_range(s_val, min, max)
				i += 2
			}
		case SettingTagStep:
			if i+1 < len(args) {
				if v, ok := args[i+1].(int); ok {
					step = v
					s.step = step
				}
				i++
			}
		case SettingTagUnitCStr:
			if i+1 < len(args) {
				if str, ok := args[i+1].(string); ok {
					s.unit = str
					if s.root != nil {
						if u := sm.pm.CreateEx(s.root, "unit", nil, false, false); u != nil {
							sm.pm.SetStringEx(u, nil, str, propcore.StringUTF8)
						}
					}
				}
				i++
			}
		case SettingTagOption:
			if i+2 < len(args) {
				if id, ok := args[i+1].(string); ok && id != "" {
					if tp, ok := args[i+2].(*propcore.Prop); ok {
						if s.val != nil {
							if o := sm.pm.CreateEx(s.val, id, nil, false, false); o != nil {
								sm.pm.Link(tp, sm.pm.CreateEx(o, "title", nil, false, false), nil, false, false)
							}
						}
					}
				}
				i += 2
			}
		case SettingTagOptionCStr:
			if i+2 < len(args) {
				if id, ok := args[i+1].(string); ok {
					if str, ok := args[i+2].(string); ok {
						if s.val != nil {
							if o := sm.pm.CreateEx(s.val, id, nil, false, false); o != nil {
								if t := sm.pm.CreateEx(o, "title", nil, false, false); t != nil {
									sm.pm.SetStringEx(t, nil, str, propcore.StringUTF8)
								}
							}
						}
					}
				}
				i += 2
			}
		case SettingTagOptionList:
			if i+1 < len(args) {
				if ol, ok := args[i+1].([]string); ok {
					optlist = ol
				}
				i++
			}
		case SettingTagWriteInt:
			if i+1 < len(args) {
				s.opaque = args[i+1]
				switch settingType {
				case SettingInt, SettingBool:
					s.callback = func(opaque any, value any) {
						if p, ok := opaque.(*int); ok {
							if v, ok := value.(int); ok {
								*p = v
							}
						}
					}
				case SettingString, SettingMultiOpt:
					s.callback = func(opaque any, value any) {
						if p, ok := opaque.(*int); ok {
							if str, ok := value.(string); ok {
								*p = misc.Atoi(str)
							}
						}
					}
				}
				i++
			}
		case SettingTagZeroText:
			if i+1 < len(args) {
				if zt, ok := args[i+1].(*propcore.Prop); ok {
					if s.root != nil {
						if z := sm.pm.CreateEx(s.root, "zerotext", nil, false, false); z != nil {
							sm.pm.Link(zt, z, nil, false, false)
						}
					}
				}
				i++
			}
		case SettingTagWriteProp:
			if i+1 < len(args) {
				if ep, ok := args[i+1].(*propcore.Prop); ok {
					s.extValue = sm.pm.RefInc(ep)
				}
				i++
			}
		case SettingTagKVStore:
			if i+2 < len(args) {
				s.storeType = SettingStoreTypeKVStore
				if str, ok := args[i+1].(string); ok {
					s.storeName = str
				}
				if str, ok := args[i+2].(string); ok {
					s.id = str
				}
				i += 2
			}
		case SettingTagPropEnabler:
			if i+1 < len(args) {
				if ep, ok := args[i+1].(*propcore.Prop); ok {
					sm.pm.Link(ep, enabled, nil, false, false)
					enabled = nil
				}
				i++
			}
		case SettingTagValueOrigin:
			if i+1 < len(args) {
				if str, ok := args[i+1].(string); ok {
					s.origin = str
				}
				i++
			}
		case SettingTagGroup:
			if i+1 < len(args) {
				s.onGroupList = true
				// C: SETTING_GROUP(&list) — LIST_INSERT_HEAD into the
				// caller-owned group list.
				if lp, ok := args[i+1].(*[]*Setting); ok && lp != nil {
					*lp = append([]*Setting{s}, *lp...)
				}
				i++
			}
		case SettingTagInherit:
			if i+1 < len(args) {
				if p, ok := args[i+1].(*Setting); ok {
					s.parent = p
					initialInt = -2147483648 // INT32_MIN
				}
				i++
			}
		}
	}

	// C: SETTING_TAG_OPTION_LIST — prop_setv(s_val, id, "title", NULL, STR, title)
	if optlist != nil {
		for j := 0; j+1 < len(optlist); j += 2 {
			if s.val == nil {
				break
			}
			if o := sm.pm.CreateEx(s.val, optlist[j], nil, false, false); o != nil {
				if t := sm.pm.CreateEx(o, "title", nil, false, false); t != nil {
					sm.pm.SetStringEx(t, nil, optlist[j+1], propcore.StringUTF8)
				}
			}
		}
	}

	// C: set "type" string on s_root
	typeStr := ""
	switch settingType {
	case SettingInt:
		typeStr = "integer"
	case SettingBool:
		typeStr = "bool"
	case SettingString:
		typeStr = "string"
	case SettingMultiOpt:
		typeStr = "multiopt"
	case SettingAction:
		typeStr = "action"
	case SettingSeparator:
		typeStr = "separator"
	}
	if s.root != nil && typeStr != "" {
		if t := sm.pm.CreateEx(s.root, "type", nil, false, false); t != nil {
			sm.pm.SetStringEx(t, nil, typeStr, propcore.StringUTF8)
		}
	}

	// C: per-type initial value load + subscription
	switch settingType {
	case SettingInt:
		if s.root != nil {
			if p := sm.pm.CreateEx(s.root, "min", nil, false, false); p != nil {
				sm.pm.SetIntEx(p, nil, min)
			}
			if p := sm.pm.CreateEx(s.root, "max", nil, false, false); p != nil {
				sm.pm.SetIntEx(p, nil, max)
			}
			if p := sm.pm.CreateEx(s.root, "step", nil, false, false); p != nil {
				sm.pm.SetIntEx(p, nil, step)
			}
		}
		fallthrough
	case SettingBool:
		s.defaultInt = initialInt

		i32 := -2147483648 // INT32_MIN

		switch s.storeType {
		case SettingStoreTypeSimple:
			if st := s.storeRef(); st != nil {
				i32 = st.GetInt(s.storeName, s.id, -2147483648)
			}
		case SettingStoreTypeKVStore:
			// C: kv_url_opt_get_int(s_store_name, KVSTORE_DOMAIN_SETTING,
			//                      s_id, INT32_MIN)
			i32 = s.mgr.kvstore.UrlOptGetInt(s.storeName, kvstore.DomainSetting,
				s.id, -2147483648)
		}

		if i32 == -2147483648 {
			i32 = initialInt
		}

		if i32 != -2147483648 {
			if settingType == SettingInt {
				if i32 > max {
					i32 = max
				}
				if i32 < min {
					i32 = min
				}
			} else if settingType == SettingBool {
				if i32 != 0 {
					i32 = 1
				}
			}
			s.valueSet.Store(true)
			if s.currentOrigin != nil {
				sm.pm.SetStringEx(s.currentOrigin, nil, s.origin, propcore.StringUTF8)
			}
			if s.val != nil {
				sm.pm.SetIntEx(s.val, nil, i32)
			}
			if flags&SettingsInitialUpdate != 0 {
				sm.settingsIntSetValue(s, i32)
			}
		}

		if s.val != nil {
			s.sub = sm.subscribe(s.val, pc, sm.settingsIntCallback, s,
				mtx, lockmgr,
				propcore.SubNoInitialUpdate, propcore.SubFlagIgnoreVoid)
		}

		if s.parent != nil {
			// C: s_inherited_value_sub on s_parent->s_val
			if s.parent.val != nil {
				s.inheritedValueSub = s.parent.val.Subscribe(
					func(o any, et propcore.EventType, a ...any) {
						if et == propcore.EventSetInt && len(a) > 0 {
							if v, ok := a[0].(int); ok {
								sm.settingsIntInheritedValue(o, v)
							}
						}
					}, s, propcore.SubFlagIgnoreVoid)
			}
			// C: s_inherited_origin_sub on s_parent->s_root "origin" child
			if s.parent.root != nil {
				if op := sm.pm.Find(s.parent.root, "origin"); op != nil {
					s.inheritedOriginSub = op.Subscribe(
						func(o any, et propcore.EventType, a ...any) {
							if et == propcore.EventSetRString && len(a) > 0 {
								if v, ok := a[0].(string); ok {
									sm.settingsIntInheritedOrigin(o, v)
								}
							}
						}, s, propcore.SubFlagIgnoreVoid)
				}
			}
		} else if initialValueProp != nil {
			s.inheritedValueSub = initialValueProp.Subscribe(
				func(o any, et propcore.EventType, a ...any) {
					if et == propcore.EventSetInt && len(a) > 0 {
						if v, ok := a[0].(int); ok {
							sm.settingsIntInheritedValue(o, v)
						}
					}
				}, s, propcore.SubFlagIgnoreVoid)
		}

	case SettingString:
		if flags&SettingsPassword != 0 && s.root != nil {
			if p := sm.pm.CreateEx(s.root, "password", nil, false, false); p != nil {
				sm.pm.SetIntEx(p, nil, 1)
			}
		}
		if flags&SettingsFile != 0 && s.root != nil {
			if p := sm.pm.CreateEx(s.root, "fileRequest", nil, false, false); p != nil {
				sm.pm.SetIntEx(p, nil, 1)
			}
		}
		if flags&SettingsDir != 0 && s.root != nil {
			if p := sm.pm.CreateEx(s.root, "dirRequest", nil, false, false); p != nil {
				sm.pm.SetIntEx(p, nil, 1)
			}
		}

		s.defaultStr = initialStr
		if s.root != nil {
			if p := sm.pm.CreateEx(s.root, "defaultValue", nil, false, false); p != nil {
				sm.pm.SetStringEx(p, nil, s.defaultStr, propcore.StringUTF8)
			}
		}

		var initial string
		hasInitial := false

		switch s.storeType {
		case SettingStoreTypeSimple:
			if st := s.storeRef(); st != nil {
				initial = st.GetStr(s.storeName, s.id)
			}
			hasInitial = initial != ""
		case SettingStoreTypeKVStore:
			// C: kv_url_opt_get_rstr(...) — NULL when unset
			initial, hasInitial = s.mgr.kvstore.UrlOptGetStringOK(
				s.storeName, kvstore.DomainSetting, s.id)
		}

		if initial != "" && s.val != nil {
			sm.pm.SetStringEx(s.val, nil, initial, propcore.StringUTF8)
		}

		if flags&SettingsInitialUpdate != 0 && initialValueProp == nil {
			if hasInitial || s.defaultStr != "" {
				v := initial
				if v == "" {
					v = s.defaultStr
				}
				sm.settingsStringSetValue(s, v)
			}
		}

		if s.val != nil {
			s.sub = sm.subscribe(s.val, pc, sm.settingsStringCallback, s,
				mtx, lockmgr,
				propcore.SubNoInitialUpdate, propcore.SubFlagIgnoreVoid)
		}

		if initialValueProp != nil {
			s.inheritedValueSub = initialValueProp.Subscribe(
				func(o any, et propcore.EventType, a ...any) {
					if et == propcore.EventSetRString && len(a) > 0 {
						if v, ok := a[0].(string); ok {
							sm.settingsStrInheritedValue(o, v)
						}
					}
				}, s, propcore.SubFlagIgnoreVoid)
		}

	case SettingMultiOpt:
		var curstr string
		switch s.storeType {
		case SettingStoreTypeSimple:
			if st := s.storeRef(); st != nil {
				curstr = st.GetStr(s.storeName, s.id)
			}
		case SettingStoreTypeKVStore:
			// C: kv_url_opt_get_rstr(...)
			curstr = s.mgr.kvstore.UrlOptGetString(s.storeName,
				kvstore.DomainSetting, s.id)
		}

		var o *propcore.Prop
		// C: o = prop_find(s->s_val, ...) — prop_find returns a ref'd prop
		if curstr != "" && s.val != nil {
			o = sm.pm.RefInc(sm.pm.Find(s.val, curstr))
		}
		if o == nil && initialStr != "" && s.val != nil {
			o = sm.pm.RefInc(sm.pm.Find(s.val, initialStr))
		}
		if o == nil {
			s.pendingValue = initialStr
			if s.val != nil {
				o = sm.pm.FirstChild(s.val)
			}
		}

		if o != nil {
			sm.pm.SelectChildProp(o, nil)
			if flags&SettingsInitialUpdate != 0 {
				name := sm.pm.GetName(o)
				sm.settingsStringSetValue(s, name)
			}
			s.currentValue = o
		}

		if s.val != nil && s.root != nil {
			sm.pm.LinkselectedCreate(s.val, s.root, "current", "value")
		}

		if s.val != nil {
			s.sub = sm.subscribe(s.val, pc, sm.settingsMultioptCallback, s,
				mtx, lockmgr,
				propcore.SubNoInitialUpdate)
		}

	case SettingAction:
		if s.val != nil {
			s.sub = sm.subscribe(s.val, pc,
				func(o any, et propcore.EventType, a ...any) {
					if et == propcore.EventExtEvent {
						s.mutex.Lock()
						cb := s.callback
						s.mutex.Unlock()
						if cb != nil && len(a) > 0 {
							cb(s.opaque, a[0])
						}
					}
				}, s, mtx, lockmgr, propcore.SubNoInitialUpdate)
		}

	case SettingSeparator:
	}

	if enabled != nil {
		sm.pm.SetIntEx(enabled, nil, 1)
	}
	// C: s->s_enable_writeback = 1; prop_ref_dec(title); prop_ref_dec(m)
	// Go: CreateEx results are not caller-refcounted — no RefDec needed.
	s.enableWriteback = true

	// C: LIST_INSERT_HEAD(&settings, s, s_link) — unprotected in C
	sm.settingsList = append(sm.settingsList, s)

	return s
}

// SettingPushToAncestor pushes setting value to ancestor
func (sm *SettingsManager) SettingPushToAncestor(s *Setting, ancestor string) {
	// C: setting_push_to_ancestor — for(a = s; a; a = a->s_parent)
	//   if(!strcmp(ancestor, a->s_origin)) break;
	//   if(a != NULL) prop_copy(a->s_val, s->s_val);  (settings.c:1302)
	current := s
	for current != nil {
		if current.origin == ancestor {
			// Found ancestor — copy s_val prop value (not the cached
			// s.value: prop_copy copies the prop's typed value).
			if current.val != nil && s.val != nil {
				sm.pm.PropCopy(current.val, s.val)
			}
			break
		}
		current = current.parent
	}
}

// SettingGroupPushToAncestor pushes group settings to ancestor
func (sm *SettingsManager) SettingGroupPushToAncestor(list []*Setting, ancestor string) {
	for _, s := range list {
		sm.SettingPushToAncestor(s, ancestor)
	}
}

func (sm *SettingsManager) SettingGetDir(key string) *propcore.Prop {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// Use a map for caching (simpler than C static variables)
	if sm.dirCache == nil {
		sm.dirCache = make(map[string]*propcore.Prop)
	}

	// Handle "settings:tv"
	if key == "settings:tv" {
		if sm.dirCache["tv"] == nil {
			sm.dirCache["tv"] = sm.AddDir(nil, sm.P("TV Control"), "display", "", sm.P("Configure communications with your TV"), "settings:tv")
		}
		return sm.dirCache["tv"]
	}

	// Handle "general:*" prefix
	if strings.HasPrefix(key, "general:") {
		k2 := key[8:] // Remove "general:" prefix

		// Initialize general directory and subdirectories on first use
		if sm.dirCache["general"] == nil {
			sm.dirCache["general"] = sm.AddDir(nil, sm.P("General"), "", "", sm.P("System related settings"), "settings:general")
			general := sm.dirCache["general"]

			// C: pc = prop_concat_create(prop_create(general, "nodes"))
			//   (settings.c:1716-1717)
			pm := sm.pm
			nodes := pm.CreateEx(general, "nodes", nil, false, false)
			pc := propcore.PropConcatCreate(pm, nodes)

			// Create subdirectories (C: addgroup calls, settings.c:1720-1725)
			sm.dirCache["misc"] = sm.addGroup(pc, nil)
			sm.dirCache["upgrade"] = sm.addGroup(pc, sm.P("Software upgrade"))
			sm.dirCache["filebrowse"] = sm.addGroup(pc, sm.P("File browsing"))
			sm.dirCache["runcontrol"] = sm.addGroup(pc, sm.P("Starting and stopping"))
			sm.dirCache["plugins"] = sm.addGroup(pc, sm.P("Plugin repositories"))
			sm.dirCache["resets"] = sm.addGroup(pc, sm.P("Reset"))
		}

		// Return the requested subdirectory
		switch k2 {
		case "resets":
			return sm.dirCache["resets"]
		case "runcontrol":
			return sm.dirCache["runcontrol"]
		case "upgrade":
			return sm.dirCache["upgrade"]
		case "filebrowse":
			return sm.dirCache["filebrowse"]
		case "plugins":
			return sm.dirCache["plugins"]
		case "misc":
			return sm.dirCache["misc"]
		}
	}

	return nil
}

// addGroup — C: addgroup (settings.c:1678-1684).
// Creates a standalone root prop whose "nodes" child is fed into the
// concat pc, preceded by a separator header made from title.
func (sm *SettingsManager) addGroup(pc *propcore.PropConcat, title *propcore.Prop) *propcore.Prop {
	p := sm.pm.CreateRootEx("", false)
	pc.AddSource(sm.pm.CreateEx(p, "nodes", nil, false, false),
		sm.MakeSep(title))
	return p
}

// MakeSep creates a separator
func (sm *SettingsManager) MakeSep(title *propcore.Prop) *propcore.Prop {
	p := sm.pm.CreateRootEx("", false)
	if title != nil {
		sm.setTitle2(p, title)
	}
	// Set type
	typeProp := sm.pm.CreateEx(p, "type", nil, false, false)
	sm.pm.SetStringEx(typeProp, nil, "separator", propcore.StringUTF8)
	return p
}

func (sm *SettingsManager) settingAdd(parent *propcore.Prop, title *propcore.Prop, settingType string, flags int) *propcore.Prop {
	p := sm.settingGet(parent, flags)
	if title != nil {
		sm.setTitle2(p, title)
	}
	// Set type
	typeProp := sm.pm.CreateEx(p, "type", nil, false, false)
	sm.pm.SetStringEx(typeProp, nil, settingType, propcore.StringUTF8)
	// C: prop_set(p, "enabled", PROP_SET_INT, 1)
	enabledProp := sm.pm.CreateEx(p, "enabled", nil, false, false)
	sm.pm.SetIntEx(enabledProp, nil, 1)
	return p
}

func (sm *SettingsManager) settingAddCStr(parent *propcore.Prop, title string, settingType string, flags int) *propcore.Prop {
	p := sm.settingGet(parent, flags)

	// Set metadata.title
	metadataProp := sm.pm.CreateEx(p, "metadata", nil, false, false)
	titleProp := sm.pm.CreateEx(metadataProp, "title", nil, false, false)
	sm.pm.SetStringEx(titleProp, nil, title, propcore.StringUTF8)

	// Set type
	typeProp := sm.pm.CreateEx(p, "type", nil, false, false)
	sm.pm.SetStringEx(typeProp, nil, settingType, propcore.StringUTF8)

	return p
}

func (sm *SettingsManager) settingGet(parent *propcore.Prop, flags int) *propcore.Prop {
	var p *propcore.Prop

	if flags&SettingsFirst != 0 {
		parentNode := parent
		if parentNode == nil {
			parentNode = sm.settingsNodes
		} else {
			parentNode = sm.pm.CreateEx(parent, "nodes", nil, false, false)
		}
		p = sm.pm.CreateRootEx("", false)
		before := sm.pm.FirstChild(parentNode)
		sm.pm.SetParentEx(p, parentNode, before, "")
		sm.pm.RefDec(before)
	} else if flags&SettingsRawNodes != 0 {
		p = sm.pm.CreateEx(parent, "", nil, false, false)
	} else {
		parentNode := parent
		if parentNode == nil {
			parentNode = sm.settingsNodes
		} else {
			parentNode = sm.pm.CreateEx(parent, "nodes", nil, false, false)
		}
		p = sm.pm.CreateEx(parentNode, "", nil, false, false)
	}

	return p
}

func (sm *SettingsManager) setTitle2(root *propcore.Prop, title *propcore.Prop) {
	// C: prop_setv(root, "metadata", "title", NULL, PROP_SET_LINK, title)
	metadataProp := sm.pm.CreateEx(root, "metadata", nil, false, false)
	titleProp := sm.pm.CreateEx(metadataProp, "title", nil, false, false)
	sm.pm.Link(title, titleProp, nil, false, false)
}

func (sm *SettingsManager) settingsAddDirSup(root *propcore.Prop, url string, icon string, subtype string) {
	// C: prop_set(root, "url", PROP_ADOPT_RSTRING,
	//            backend_prop_make(root, url))  (settings.c:177)
	urlProp := sm.pm.CreateEx(root, "url", nil, false, false)
	sm.pm.SetStringEx(urlProp, nil,
		sm.propPageManager.BackendPropMake(sm.pm, root, url),
		propcore.StringUTF8)

	// Set subtype
	subtypeProp := sm.pm.CreateEx(root, "subtype", nil, false, false)
	sm.pm.SetStringEx(subtypeProp, nil, subtype, propcore.StringUTF8)

	if icon != "" {
		// Set metadata.icon
		metadataProp := sm.pm.CreateEx(root, "metadata", nil, false, false)
		iconProp := sm.pm.CreateEx(metadataProp, "icon", nil, false, false)
		sm.pm.SetStringEx(iconProp, nil, icon, propcore.StringUTF8)
	}
}
