package settings

import (
	"slices"

	propcore "github.com/czz/movian-go/internal/prop"
)

// Destroy destroys a setting.
// C: setting_destroy (settings.c):
//
//	if(s->s_on_group_list) LIST_REMOVE(s, s_group_link);
//	s->s_callback = NULL;
//	prop_unsubscribe(s->s_sub);
//	prop_unsubscribe(s->s_inherited_value_sub);
//	prop_unsubscribe(s->s_inherited_origin_sub);
//	prop_destroy(s->s_root);
//	setting_release(s);
//
// (The group list is caller-owned in Go; GroupDestroy covers that removal.)
func (sm *SettingsManager) Destroy(s *Setting) {
	if s == nil {
		return
	}

	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	// C: s->s_callback = NULL — pending notifications can't reach the
	// callback after destroy.
	s.mutex.Lock()
	s.callback = nil
	s.mutex.Unlock()

	// C: prop_unsubscribe(s->s_sub / s_inherited_value_sub / s_inherited_origin_sub)
	if s.sub != nil {
		s.sub.Unsubscribe()
		s.sub = nil
	}
	if s.inheritedValueSub != nil {
		s.inheritedValueSub.Unsubscribe()
		s.inheritedValueSub = nil
	}
	if s.inheritedOriginSub != nil {
		s.inheritedOriginSub.Unsubscribe()
		s.inheritedOriginSub = nil
	}

	// C: prop_destroy(s->s_root)
	if s.root != nil {
		sm.pm.Destroy(s.root)
		s.root = nil
	}

	// C: setting_release(s) — Go: drop the manager's reference.
	for i, setting := range sm.settingsList {
		if setting == s {
			sm.settingsList = slices.Delete(sm.settingsList, i, i+1)
			break
		}
	}
}

// Set sets the value of a setting
func (s *Setting) Set(value any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.value = value

	if s.callback != nil {
		s.callback(s.opaque, value)
	}
}

// SettingSet — C: setting_set (settings.c:1191-1215).
// if(s->s_type != type) return; then per type:
//
//	SETTING_INT/BOOL  → prop_set_int(s->s_val, i32)
//	SETTING_STRING    → abort()
//	SETTING_MULTIOPT  → prop_select_by_value(s->s_val, str)
//
// For MULTIOPT the selection notification on s_val reaches s_callback
// through the value subscription (synchronously or via the setting's
// courier, matching C's dispatch).
func (sm *SettingsManager) SettingSet(s *Setting, settingType int, value any) {
	if s == nil || s.settingType != settingType {
		return
	}
	switch settingType {
	case SettingInt, SettingBool:
		if v, ok := value.(int); ok && s.val != nil {
			s.pm.SetIntEx(s.val, nil, v)
		}
	case SettingString:
		panic("setting_set: SETTING_STRING") // C: abort()
	case SettingMultiOpt:
		if str, ok := value.(string); ok && s.val != nil {
			s.pm.SelectChild(s.val, str) // C: prop_select_by_value
		}
	}
}

// Get gets the value of a setting.
// C: reads the s_val prop; falls back to the cached s.value field.
func (s *Setting) Get() any {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.val != nil && s.pm != nil {
		switch s.settingType {
		case SettingInt, SettingBool:
			return s.pm.GetInt(s.val, s.defaultInt)
		case SettingString:
			return s.pm.GetString(s.val, s.defaultStr)
		}
	}
	return s.value
}

// AddInt adds a delta to an int setting
func (s *Setting) AddInt(delta int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.settingType != SettingInt {
		return
	}

	if val, ok := s.value.(int); ok {
		newVal := val + delta
		if s.minVal != 0 && newVal < s.minVal {
			newVal = s.minVal
		}
		if s.maxVal != 0 && newVal > s.maxVal {
			newVal = s.maxVal
		}
		s.value = newVal

		if s.callback != nil {
			s.callback(s.opaque, newVal)
		}
	}
}

// SetRange sets the min/max range for an int setting
func (s *Setting) SetRange(min, max int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.minVal = min
	s.maxVal = max
}

// SetStep sets the step value for an int setting
func (s *Setting) SetStep(step int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.step = step
}

// SetUnit sets the unit string for a setting
func (s *Setting) SetUnit(unit string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.unit = unit
}

// AddOption — C: setting_add_option (settings.c:1173-1187). Creates the
// option child prop under s_val with a "title" child, and selects it
// when it matches s_pending_value or sel is set. Returns the option prop
// (C: prop_t *).
func (s *Setting) AddOption(id string, title string, sel bool) *propcore.Prop {
	opt := s.pm.CreateEx(s.val, id, nil, false, false)
	if tp := s.pm.CreateEx(opt, "title", nil, false, false); tp != nil {
		s.pm.SetStringEx(tp, nil, title, propcore.StringUTF8)
	}

	if (s.pendingValue != "" && s.pendingValue == id) || sel {
		s.pendingValue = ""
		s.pm.SelectChildProp(opt, nil)
	}
	return opt
}

// SetCallback sets the callback for a setting
func (s *Setting) SetCallback(callback func(opaque any, value any), opaque any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.callback = callback
	s.opaque = opaque
}

// GetType returns the setting type
func (s *Setting) GetType() int {
	return s.settingType
}

// GetTitle returns the setting title
func (s *Setting) GetTitle() string {
	return s.title
}

// Detach detaches a setting from its parent
func (s *Setting) Detach() {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.parent = nil
}

// CreateAction creates an action setting
func (sm *SettingsManager) CreateAction(parent *Setting, title string, callback func(opaque any, value any), opaque any, flags int) *Setting {
	s := sm.Create(SettingAction, parent, flags, title)
	s.SetCallback(callback, opaque)
	return s
}

// CreateActionProp creates an action setting with prop parent.
// C: settings_create_action (settings.c:300-315):
//
//	s = setting_create_leaf(parent, title, "action", "action", flags)
//	prop_set(s->s_root, "subtype", PROP_SET_STRING, subtype)
//	s->s_sub = prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE, cb on
//	    s_root/eventSink, opaque, courier)
//
// Go: SettingCreate(SettingAction) builds s_root under model/"nodes"
// (or as a raw child with SettingsRawNodes), creates the "eventSink"
// child and subscribes it — EventExtEvent dispatches s.callback.
func (sm *SettingsManager) CreateActionProp(parent *propcore.Prop, title, subtype string,
	callback func(opaque any, value any), opaque any, flags int) {
	args := make([]any, 0, 6)
	if title != "" {
		// C callers pass _p("...") — a live-translated nls prop
		args = append(args, SettingTagTitle, sm.P(title))
	}
	args = append(args, SettingTagCallback, callback, opaque, 0)
	s := sm.SettingCreate(SettingAction, parent, flags, args...)
	if s != nil && s.root != nil && subtype != "" {
		// C: prop_set(s->s_root, "subtype", PROP_SET_STRING, subtype)
		if st := sm.pm.CreateEx(s.root, "subtype", nil, false, false); st != nil {
			sm.pm.SetStringEx(st, nil, subtype, propcore.StringUTF8)
		}
	}
}

// CreateSeparator creates a separator setting
func (sm *SettingsManager) CreateSeparator(parent *Setting, caption string) *Setting {
	s := sm.Create(SettingSeparator, parent, 0, caption)
	return s
}

// CreateInt creates an integer setting
func (sm *SettingsManager) CreateInt(parent *Setting, title string, value int, flags int) *Setting {
	s := sm.Create(SettingInt, parent, flags, title)
	s.value = value
	return s
}

// CreateIntProp creates an integer setting with prop parent
func (sm *SettingsManager) CreateIntProp(parent *propcore.Prop, title string, value int, flags int) {
	s := sm.createLeaf(parent, nil, "int", "value", flags)
	s.value = value
}

// CreateString creates a string setting
func (sm *SettingsManager) CreateString(parent *Setting, title string, value string, flags int) *Setting {
	s := sm.Create(SettingString, parent, flags, title)
	s.value = value
	return s
}

// CreateBool creates a boolean setting
func (sm *SettingsManager) CreateBool(parent *Setting, title string, value bool, flags int) *Setting {
	s := sm.Create(SettingBool, parent, flags, title)
	s.value = value
	return s
}

// CreateBoolProp creates a boolean setting with prop parent
func (sm *SettingsManager) CreateBoolProp(parent *propcore.Prop, title string, value bool, flags int) {
	s := sm.createLeaf(parent, nil, "bool", "value", flags)
	s.value = value
}

// CreateMultiOpt creates a multi-option setting
func (sm *SettingsManager) CreateMultiOpt(parent *Setting, title string, flags int) *Setting {
	s := sm.Create(SettingMultiOpt, parent, flags, title)
	return s
}

// CreateMultiOptProp creates a multi-option setting with prop parent
func (sm *SettingsManager) CreateMultiOptProp(parent *propcore.Prop, title string, flags int) {
	sm.createLeaf(parent, nil, "multiopt", "value", flags)
}

// GroupDestroy destroys a group of settings.
// C: setting_group_destroy(LIST_HEAD*) — destroys every member and
// leaves the list empty. Takes a pointer so the caller's list is cleared.
func (sm *SettingsManager) GroupDestroy(list *[]*Setting) {
	if list == nil {
		return
	}
	for _, s := range *list {
		sm.Destroy(s)
	}
	*list = nil
}

// GroupReset resets a group of settings
// C: setting_group_reset (settings.c:1284-1290).
func GroupReset(list []*Setting) {
	for _, s := range list {
		s.settingReset()
	}
}

// GetSettingsList returns the list of all settings
func (sm *SettingsManager) GetSettingsList() []*Setting {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	result := make([]*Setting, len(sm.settingsList))
	copy(result, sm.settingsList)
	return result
}

// FindSettingByTitle finds a setting by its title
func (sm *SettingsManager) FindSettingByTitle(title string) *Setting {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()

	for _, s := range sm.settingsList {
		if s.title == title {
			return s
		}
	}
	return nil
}
