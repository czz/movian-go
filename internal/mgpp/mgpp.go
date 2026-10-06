package mgpp

// STPP wire-protocol constants — canonical port of src/api/stpp.h.
// The protocol implementation lives in pkg/api/mgpp (C: src/api/stpp.c).

const (
	MGPPVersion = 3
)

// STPP commands (from stpp.h - must match wire protocol)
const (
	MGPPCmdHello          = 0
	MGPPCmdSubscribe      = 1
	MGPPCmdUnsubscribe    = 2
	MGPPCmdSet            = 3
	MGPPCmdNotify         = 4
	MGPPCmdEvent          = 5
	MGPPCmdReqMove        = 6
	MGPPCmdWantMoreChilds = 7
	MGPPCmdSelect         = 8
	MGPPCmdImageLoad      = 9
	MGPPCmdImageReply     = 10
	MGPPCmdImageFail      = 11
	MGPPCmdImageCancel    = 12
)

// STPP notify types (from stpp.h - first byte in STPP_CMD_NOTIFY)
const (
	MGPPSetVoid           = 0
	MGPPSetInt            = 1
	MGPPSetFloat          = 2
	MGPPSetString         = 3
	MGPPSetURI            = 4
	MGPPSetDir            = 5
	MGPPAddChilds         = 6
	MGPPAddChildsBefore   = 7
	MGPPDelChild          = 8
	MGPPMoveChild         = 9
	MGPPSelectChild       = 10
	MGPPAddChildSelected  = 11
	MGPPValueProp         = 12
	MGPPToggleInt         = 13
	MGPPHaveMoreChildsYes = 14
	MGPPHaveMoreChildsNo  = 15
)
