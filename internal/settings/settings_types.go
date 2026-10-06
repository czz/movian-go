package settings

import (
	"sync"
	"sync/atomic"

	propcore "github.com/czz/movian-go/internal/prop"
)

// Settings flags
const (
	SettingsInitialUpdate = 0x1
	SettingsPassword      = 0x2
	SettingsRawNodes      = 0x4
	SettingsFirst         = 0x8
	SettingsDebug         = 0x10
	SettingsFile          = 0x20
	SettingsDir           = 0x40
)

// Setting types
const (
	SettingInt = iota
	SettingString
	SettingBool
	SettingMultiOpt
	SettingAction
	SettingSeparator
	SettingInfo
)

// Setting tags
const (
	SettingTagTitle = iota + 1
	SettingTagTitleCStr
	SettingTagValue
	SettingTagValueProp
	SettingTagCallback
	SettingTagCourier
	SettingTagStore
	SettingTagRange
	SettingTagStep
	SettingTagUnitCStr
	SettingTagOption
	SettingTagOptionCStr
	SettingTagOptionList
	SettingTagWriteInt
	SettingTagZeroText
	SettingTagMutex
	SettingTagLockMgr
	SettingTagWriteProp
	SettingTagPropEnabler
	SettingTagKVStore
	SettingTagValueOrigin
	SettingTagGroup
	SettingTagInherit
)

// Setting store types (C: SETTING_STORETYPE_*)
const (
	SettingStoreTypeNone    = 0 // C: SETTING_STORETYPE_NONE
	SettingStoreTypeSimple  = 1 // C: SETTING_STORETYPE_SIMPLE
	SettingStoreTypeKVStore = 2 // C: SETTING_STORETYPE_KVSTORE
)

// Setting represents a configuration setting.
// C: struct setting (settings.c:55-97)
type Setting struct {
	settingType int      // C: s_type
	flags       int      // C: s_flags
	parent      *Setting // C: s_parent
	title       string
	value       any
	origin      string                      // C: s_origin[10]
	callback    func(opaque any, value any) // C: s_callback
	opaque      any                         // C: s_opaque

	// C: s_root / s_val / s_current_origin / s_ext_value
	root          *propcore.Prop // s_root: the setting's prop node
	val           *propcore.Prop // s_val: the "value"/"options"/"eventSink" child
	currentOrigin *propcore.Prop // s_current_origin
	extValue      *propcore.Prop // s_ext_value (SETTING_TAG_WRITE_PROP)

	// C: s_sub / s_inherited_value_sub / s_inherited_origin_sub
	sub                *propcore.Subscription
	inheritedValueSub  *propcore.Subscription
	inheritedOriginSub *propcore.Subscription

	// C: s_store_type / s_store_name / s_id
	storeType int    // s_store_type
	storeName string // s_store_name
	id        string // s_id

	pendingValue string         // s_pending_value
	currentValue *propcore.Prop // s_current_value

	defaultStr string // s_default_str (rstr_t)
	defaultInt int    // s_default_int

	// C: s_enable_writeback / s_on_group_list / s_value_set
	enableWriteback bool
	onGroupList     bool
	valueSet        atomic.Bool

	// For int settings
	minVal int
	maxVal int
	step   int

	// For string settings
	unit string

	// pm is the prop manager (for reading s_val in Get)
	pm *propcore.PropManager

	// mgr — owning manager; carries the store dep (C: htsmsg_store global)
	mgr *SettingsManager

	// Mutex for thread safety
	mutex sync.Mutex
}

// SettingOption represents an option for multi-opt settings
// SettingSaver is a callback for saving settings
type SettingSaver func(opaque any, value any)
