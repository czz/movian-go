// Package htsp is a 1:1 port of src/backend/htsp/htsp.c.
//
// Every C function, struct and global maps to the Go counterpart below,
// in the same order. C references are given per function.
package htsp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	mediacore "github.com/czz/movian-go/internal/media/core"
	propcore "github.com/czz/movian-go/internal/prop"
)

// snprintf — C: snprintf(buf, size, fmt, ...) — truncates to size-1 chars.
func snprintf(size int, format string, args ...any) string {
	s := fmt.Sprintf(format, args...)
	if size > 0 && len(s) > size-1 {
		s = s[:size-1]
	}
	return s
}

// htsmsgGetStr — C: htsmsg_get_str (htsmsg.c:477-484) — NULL when the
// field is missing or of a non-convertible type; HMF_S64 converts to
// string like C's htsmsg_field_get_string.
func htsmsgGetStr(m *htsmsg.HTSMsg, name string) (string, bool) {
	f := m.FieldFind(name)
	if f == nil {
		return "", false
	}
	switch f.GetType() {
	case htsmsg.HmfStr:
		return f.GetStrValue(), true
	case htsmsg.HmfS64:
		return strconv.FormatInt(f.GetS64Value(), 10), true
	default:
		return "", false
	}
}

// propSetStr — C: prop_set(p, name, PROP_SET_STRING, str)
// NULL str → prop_set_void (prop_seti, prop_core.c:5544-5549).
func propSetStr(pm *propcore.PropManager, p *propcore.Prop, name string,
	s string, ok bool) {
	if ok {
		pm.SetVEx(nil, p, name, s)
	} else {
		pm.SetVEx(nil, p, name, nil)
	}
}

// propSetStringOrVoid — C: prop_set_string(p, str)
// NULL str → prop_set_void (prop_set_string_ex, prop_core.c:3583-3597).
func propSetStringOrVoid(pm *propcore.PropManager, p *propcore.Prop,
	s string, ok bool) {
	if ok {
		pm.SetStringEx(p, nil, s, propcore.StringUTF8)
	} else {
		pm.SetVoidEx(p, nil)
	}
}

// htspMsgRemove — C: TAILQ_REMOVE(&hc->hc_rpc_queue, hm, hm_link)
func htspMsgRemove(q *[]*htspMsg, hm *htspMsg) {
	for i, x := range *q {
		if x == hm {
			*q = slices.Delete((*q), i, i+1)
			return
		}
	}
}

// meIsType — C: event_is_type.
func meIsType(e *mediacore.MediaEvent, t event.EventType) bool {
	return e != nil && e.Type == int(t)
}

// meIsAction — C: event_is_action.
func meIsAction(e *mediacore.MediaEvent, at event.ActionType) bool {
	if e == nil {
		return false
	}
	if c, ok := e.Data.(interface{ IsAction(event.ActionType) bool }); ok {
		return c.IsAction(at)
	}
	return false
}

// mystrbegins — C: mystrbegins (misc/str.h) — returns remainder or NULL.
func mystrbegins(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// C: prio_to_weight (htsp.c:1705-1717)
func prioToWeight(p int) int {
	if p == 0 {
		return 150
	}

	w := max(140-p, 110)
	return w
}

// toMediaEvent wraps a raw return value in a MediaEvent when needed.
// C: be_play_video returns event_t* — the backend system's caller
// normalizes via toMediaEvent; the DVR path delegates to
// be_file_playvideo_fh whose result arrives already wrapped.
func toMediaEvent(v any) *mediacore.MediaEvent {
	switch ev := v.(type) {
	case nil:
		return nil
	case *mediacore.MediaEvent:
		return ev
	case *event.Event:
		if ev == nil {
			return nil
		}
		return &mediacore.MediaEvent{Type: int(ev.Type), Data: ev.Concrete()}
	case interface{ AsEvent() *event.Event }:
		be := ev.AsEvent()
		if be == nil {
			return nil
		}
		return &mediacore.MediaEvent{Type: int(be.Type), Data: ev}
	}
	return nil
}
