package settings

import propcore "github.com/czz/movian-go/internal/prop"

// SettingsProvider defines the settings manager interface used by consumers
// (keyring, upgrade, text, app). SettingsManager implements this interface.
type SettingsProvider interface {
	Start()
	Fini()
	GetModel() *propcore.Prop
	GetNodes() *propcore.Prop
	GetSettingsList() []*Setting
	FindSettingByTitle(title string) *Setting
	Create(settingType int, parent *Setting, flags int, title string) *Setting
	Destroy(s *Setting)
	CreateAction(parent *Setting, title string, callback func(opaque any, value any), opaque any, flags int) *Setting
	CreateActionProp(parent *propcore.Prop, title, subtype string, callback func(opaque any, value any), opaque any, flags int)
	CreateSeparator(parent *Setting, caption string) *Setting
	CreateInt(parent *Setting, title string, value int, flags int) *Setting
	CreateIntProp(parent *propcore.Prop, title string, value int, flags int)
	CreateString(parent *Setting, title string, value string, flags int) *Setting
	CreateBool(parent *Setting, title string, value bool, flags int) *Setting
	CreateBoolProp(parent *propcore.Prop, title string, value bool, flags int)
	CreateMultiOpt(parent *Setting, title string, flags int) *Setting
	CreateMultiOptProp(parent *propcore.Prop, title string, flags int)
	CreateInfo(parent *propcore.Prop, image string, description *propcore.Prop)
	CreateSeparatorProp(parent *propcore.Prop, caption *propcore.Prop)
	CreateBoundString(parent *propcore.Prop, title *propcore.Prop, value *propcore.Prop)
	GroupDestroy(list *[]*Setting)
	AddDir(parent *propcore.Prop, title *propcore.Prop, subtype string, icon string, shortdesc *propcore.Prop, url string) *propcore.Prop
	AddDirCStr(parent *propcore.Prop, title string, subtype string, icon string, shortdesc string, url string) *propcore.Prop
	AddUrl(parent *propcore.Prop, title *propcore.Prop, subtype string, icon string, shortdesc *propcore.Prop, url string, flags int)
	AddInt(s *Setting, delta int)
	SettingCreate(settingType int, model any, flags int, args ...any) *Setting
	SettingPushToAncestor(s *Setting, ancestor string)
	SettingGroupPushToAncestor(list []*Setting, ancestor string)
	SettingGetDir(key string) *propcore.Prop
	MakeSep(title *propcore.Prop) *propcore.Prop
	P(s string) *propcore.Prop
}
