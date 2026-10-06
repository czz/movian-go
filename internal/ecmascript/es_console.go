// Canonical 1:1 port of src/ecmascript/es_console.c
package ecmascript

import (
	"github.com/czz/movian-go/internal/gaftape"
	"github.com/czz/movian-go/internal/trace"
)

// makePrintable — C: make_printable (es_console.c:25-32)
func makePrintable(ctx *gaftape.Context) int {
	if ctx.IsObject(-1) {
		ctx.JsonEncode(-1)
	}
	return 1
}

// logConcat — C: log_concat (es_console.c:38-54)
func logConcat(ctx *gaftape.Context) string {
	argc := ctx.GetTop()
	if argc == 0 {
		return ""
	}

	ctx.PushString(" ")

	for i := range argc {
		ctx.Dup(i)
		ctx.SafeCall(makePrintable, 1, 1)
	}

	ctx.Join(argc)

	return ctx.GetString(-1)
}

// es_console_log — C: es_console.c:60-65
func esConsoleLog(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	esEnv.tracer.Trace(trace.TRACE_DEBUG, ec.ecID, "%s", logConcat(ctx))
	return 0
}

// es_console_error — C: es_console.c:71-76
func esConsoleError(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	esEnv.tracer.Trace(trace.TRACE_ERROR, ec.ecID, "%s", logConcat(ctx))
	return 0
}

// es_fnlist_console — C: es_console.c:82-87
var esFnlistConsole = []gaftape.FunctionListEntry{
	{Key: "log", Value: esConsoleLog, Nargs: -1},
	{Key: "error", Value: esConsoleError, Nargs: -1},
}
