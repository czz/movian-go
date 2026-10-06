package ecmascript

// RegisterBuiltinModules registers all built-in ECMAScript modules and
// native classes. C-parity: ES module/native-class constructors ran
// pre-main; Go calls this explicitly from the composition root.
// Order = previous per-file init() order (alphabetical file order):
// esModules prepends (list inverts), nativeClasses IDs are slot order.
func RegisterBuiltinModules() {
	if esBuiltinRegistered {
		return
	}
	esBuiltinRegistered = true
	registerEsCryptoa()
	registerEsCryptob()
	registerEsFaprovidera()
	registerEsFaproviderb()
	registerEsFs()
	registerEsJambalayaa()
	registerEsJambalayab()
	registerEsHook()
	registerEsHtsmsga()
	registerEsHtsmsgb()
	registerEsIoa()
	registerEsIob()
	registerEsKvstore()
	registerEsMetadata()
	registerEsMisc()
	registerEsNativeObj()
	registerEsPropa()
	registerEsPropb()
	registerEsRoute()
	registerEsService()
	registerEsSqlite()
	registerEsString()
	registerEsSubtitles()
	registerEsWebsocket()
}

var esBuiltinRegistered bool
