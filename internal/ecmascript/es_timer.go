// Canonical 1:1 port of src/ecmascript/es_timer.c
//
// A single background goroutine services a sorted list of sys.timer.list;
// firing invokes the rooted JS callback inside the owning es_context.
package ecmascript

import (
	"fmt"
	"slices"
	"time"

	"github.com/czz/movian-go/internal/gaftape"
)

// C: typedef struct es_timer (es_timer.c:24-28)
type esTimer struct {
	super      *ESResource
	etExpire   int64
	etInterval int // in ms
}

// es_timer.c file statics live on System.timer (see ecmascript.go).

// ---------------------------------------------------------------------------
// es_timer_destroy — C: es_timer.c:44-57
// ---------------------------------------------------------------------------

func esTimerDestroy(eres *ESResource) {
	et := eres.Data.(*esTimer)

	EsRootUnregister(eres.erCtx.ecGaf, eres)

	if et.etExpire != 0 {
		eres.erCtx.sys.timer.mu.Lock()
		for i, x := range eres.erCtx.sys.timer.list {
			if x == et {
				eres.erCtx.sys.timer.list = slices.Delete(eres.erCtx.sys.timer.list, i, i+1)
				break
			}
		}
		eres.erCtx.sys.timer.mu.Unlock()
	}

	EsResourceUnlink(et.super)
}

// ---------------------------------------------------------------------------
// es_timer_info — C: es_timer.c:62-68
// ---------------------------------------------------------------------------

func esTimerInfo(eres *ESResource) string {
	et := eres.Data.(*esTimer)
	delta := et.etExpire - archGetTS()
	return fmt.Sprintf("in %d ms repeat %d ms", int(delta/1000), et.etInterval)
}

// ---------------------------------------------------------------------------
// es_resource_timer — C: es_timer.c:73-78
// ---------------------------------------------------------------------------

var esResourceTimer = &ESResourceClass{
	ErcName:    "timer",
	ErcDestroy: esTimerDestroy,
	ErcInfo:    esTimerInfo,
}

// estimercmp — C: es_timer.c:85-92
func estimercmp(a, b *esTimer) int {
	if a.etExpire < b.etExpire {
		return -1
	} else if a.etExpire > b.etExpire {
		return 1
	}
	return 0
}

// timerInsertSorted — C: LIST_INSERT_SORTED
func (sys *System) timerInsertSorted(et *esTimer) {
	i := 0
	for i < len(sys.timer.list) && estimercmp(sys.timer.list[i], et) <= 0 {
		i++
	}
	sys.timer.list = append(sys.timer.list, nil)
	copy(sys.timer.list[i+1:], sys.timer.list[i:])
	sys.timer.list[i] = et
}

// ---------------------------------------------------------------------------
// timer_thread — C: es_timer.c:99-148
// ---------------------------------------------------------------------------

func (sys *System) timerThread() {
	destroy := 0
	var et *esTimer
	sys.timer.mu.Lock()
	for {
		if len(sys.timer.list) == 0 {
			break
		}
		et = sys.timer.list[0]

		now := archGetTS()
		delta := et.etExpire - now
		if delta > 0 {
			ms := (delta + 999) / 1000
			sys.timer.mu.Unlock()
			t := time.NewTimer(time.Duration(ms) * time.Millisecond)
			select {
			case <-t.C:
			case <-sys.timer.kick:
				t.Stop()
			}
			sys.timer.mu.Lock()
			continue
		}

		sys.timer.list = sys.timer.list[1:]
		if et.etInterval != 0 {
			et.etExpire = now + int64(et.etInterval)*1000
			sys.timerInsertSorted(et)
			destroy = 0
		} else {
			et.etExpire = 0
			destroy = 1
		}

		esResourceRetain(et.super)
		sys.timer.mu.Unlock()

		ec := et.super.erCtx

		ctx := EsContextBegin(ec)

		EsPushRoot(ctx, et)
		rc := ctx.PCall(0)
		if rc != 0 {
			EsDumpErr(ctx)
		}

		ctx.Pop()

		if destroy != 0 {
			EsResourceDestroy(et.super)
		}

		EsContextEnd(ec, 0, ctx)

		sys.timer.mu.Lock()
		EsResourceRelease(et.super)
	}
	sys.timer.running = false
	sys.timer.mu.Unlock()
}

// ---------------------------------------------------------------------------
// set_timer — C: es_timer.c:154-186
// ---------------------------------------------------------------------------

func setTimer(gaf *gaftape.Context, repeat int) int {
	ec := EsGet(gaf)

	et := &esTimer{}
	et.super = EsResourceAlloc(esResourceTimer, et)
	EsResourceLink(et.super, ec, 1)
	val := gaf.RequireInt(1)

	EsRootRegister(gaf, 0, et)

	et.etInterval = val * repeat

	now := archGetTS()
	et.etExpire = now + int64(val)*1000

	ec.sys.timer.mu.Lock()

	if !ec.sys.timer.running {
		ec.sys.timer.running = true
		go ec.sys.timerThread()
	} else {
		select {
		case ec.sys.timer.kick <- struct{}{}:
		default:
		}
	}

	ec.sys.timerInsertSorted(et)

	ec.sys.timer.mu.Unlock()

	EsResourcePush(gaf, et.super)
	return 1
}

// setTimeout — C: es_timer.c:205-209
func setTimeout(gaf *gaftape.Context) int {
	return setTimer(gaf, 0)
}

// setInterval — C: es_timer.c:215-219
func setInterval(gaf *gaftape.Context) int {
	return setTimer(gaf, 1)
}

// clearTimer — C: es_timer.c:225-230
func clearTimer(gaf *gaftape.Context) int {
	et := EsResourceGet(gaf, 0, esResourceTimer).(*ESResource)
	EsResourceDestroy(et)
	return 0
}

// es_fnlist_timer — C: es_timer.c:236-242
var esFnlistTimer = []gaftape.FunctionListEntry{
	{Key: "setTimeout", Value: setTimeout, Nargs: 2},
	{Key: "setInterval", Value: setInterval, Nargs: 2},
	{Key: "clearTimeout", Value: clearTimer, Nargs: 1},
	{Key: "clearInterval", Value: clearTimer, Nargs: 1},
}
