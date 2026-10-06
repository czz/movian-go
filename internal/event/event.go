package event

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	propcore "github.com/czz/movian-go/internal/prop"
)

// ActionType represents user actions
type ActionType int

const (
	ActionNone = iota

	ACTION_mappable_begin

	ACTION_UP
	ACTION_DOWN
	ACTION_LEFT
	ACTION_RIGHT
	ACTION_CLICK
	ACTION_ACTIVATE
	ACTION_ENTER
	ACTION_SUBMIT
	ACTION_OK
	ACTION_CANCEL
	ACTION_BS
	ACTION_DELETE

	ACTION_FOCUS_NEXT
	ACTION_FOCUS_PREV

	ACTION_MOVE_UP
	ACTION_MOVE_DOWN
	ACTION_MOVE_LEFT
	ACTION_MOVE_RIGHT

	ACTION_NAV_FWD
	ACTION_NAV_BACK

	ACTION_PAGE_UP
	ACTION_PAGE_DOWN

	ACTION_TOP
	ACTION_BOTTOM

	ACTION_INCR
	ACTION_DECR

	ACTION_STOP
	ACTION_PLAYPAUSE
	ACTION_PLAY
	ACTION_PAUSE
	ACTION_EJECT
	ACTION_RECORD

	ACTION_SKIP_FORWARD
	ACTION_SKIP_BACKWARD

	ACTION_SEEK_FORWARD
	ACTION_SEEK_BACKWARD

	ACTION_VOLUME_UP
	ACTION_VOLUME_DOWN
	ACTION_VOLUME_MUTE_TOGGLE

	ACTION_MENU
	ACTION_ITEMMENU
	ACTION_COPY
	ACTION_PASTE
	ACTION_LOGWINDOW
	ACTION_SELECT
	ACTION_SHOW_MEDIA_STATS
	ACTION_HOME
	ACTION_SYSTEM_HOME
	ACTION_RESET

	ACTION_SWITCH_VIEW
	ACTION_FULLSCREEN_TOGGLE

	ACTION_NEXT_CHANNEL
	ACTION_PREV_CHANNEL

	ACTION_ZOOM_UI_INCR
	ACTION_ZOOM_UI_DECR
	ACTION_ZOOM_UI_RESET
	ACTION_RELOAD_UI

	ACTION_QUIT
	ACTION_STANDBY
	ACTION_POWER_OFF
	ACTION_RESTART
	ACTION_REBOOT

	ACTION_SHUFFLE
	ACTION_REPEAT

	ACTION_ENABLE_SCREENSAVER

	ACTION_CYCLE_AUDIO
	ACTION_CYCLE_SUBTITLE

	ACTION_RELOAD_DATA

	ACTION_PLAYQUEUE

	ACTION_SYSINFO

	ACTION_SWITCH_UI

	ACTION_RECORD_UI

	ACTION_mappable_end

	ACTION_invalid = -1
)

// EventType represents different event types
type EventType int

// C: event_type_t (event.h:143-177) — explicit numbering; "These numbers
// are sent over the wire (STPP) so don't change them".
const (
	EVENT_ACTION_VECTOR            EventType = 1
	EVENT_UNICODE                  EventType = 2
	EVENT_KEYDESC                  EventType = 3
	EVENT_PLAYQUEUE_JUMP           EventType = 4
	EVENT_PLAYQUEUE_JUMP_AND_PAUSE EventType = 5
	EVENT_SEEK                     EventType = 6
	EVENT_DELTA_SEEK_REL           EventType = 7
	EVENT_EOF                      EventType = 8
	EVENT_PLAY_URL                 EventType = 9
	EVENT_EXIT                     EventType = 10
	EVENT_DVD_PCI                  EventType = 11
	EVENT_DVD_SELECT_BUTTON        EventType = 12
	EVENT_DVD_ACTIVATE_BUTTON      EventType = 13
	EVENT_OPENURL                  EventType = 14
	EVENT_PLAYTRACK                EventType = 15
	EVENT_INTERNAL_PAUSE           EventType = 16
	EVENT_CURRENT_TIME             EventType = 17
	EVENT_SELECT_AUDIO_TRACK       EventType = 18
	EVENT_SELECT_SUBTITLE_TRACK    EventType = 19
	EVENT_PLAYBACK_PRIORITY        EventType = 20
	EVENT_STOP_UI                  EventType = 21
	EVENT_HOLD                     EventType = 22
	EVENT_REPAINT_UI               EventType = 23
	EVENT_REOPEN                   EventType = 24
	EVENT_REDIRECT                 EventType = 25
	EVENT_PROPREF                  EventType = 26
	EVENT_DYNAMIC_ACTION           EventType = 27
	EVENT_MAKE_SCREENSHOT          EventType = 28
	EVENT_PROP_ACTION              EventType = 29
	EVENT_SCROLL                   EventType = 30
	EVENT_INSERT_STRING            EventType = 31
)

// EVENT_RATE — Go extension (C's airplay_rate is an empty stub returning
// 200; no canonical event type exists). Kept outside the C wire range so
// it can never collide with a canonical event_type_t.
const EVENT_RATE EventType = 1000

// Event flags
const (
	EventKeypress       = 0x1
	EventMouse          = 0x2
	EventScreenPosition = 0x4
)

// Event represents a system event
type Event struct {
	Nav       *propcore.Prop
	Dtor      func(*Event)
	CloneFunc func(*Event) *Event
	Timestamp int64
	ScreenX   float32
	ScreenY   float32
	RefCount  int32
	Flags     int
	Type      EventType
	Actions   []ActionType // For EVENT_ACTION_VECTOR

	// OpenURL carries the 6 context fields for EVENT_OPENURL events.
	// This is set by CreateOpenURLArgs so the fields survive channel
	// round-trips (where the outer *EventOpenURL type is lost and only
	// *Event is sent/received). Equivalent to C's event_openurl_t being
	// castable from event_t via pointer arithmetic.
	OpenURL *EventOpenURLArgs

	// PlayTrack carries the track/source/mode fields for
	// EVENT_PLAYTRACK events. Same round-trip rationale as OpenURL:
	// receivers get the flattened *Event (C casts event_t* back to
	// event_playtrack_t*).
	PlayTrack *EventPlayTrackArgs

	// Payload carries the string for event_payload_t events
	// (EVENT_DYNAMIC_ACTION and every event_create_str type). Same
	// round-trip rationale as OpenURL: once the event travels as *Event
	// (C's event_t*), the outer *EventPayload type is lost — the base
	// field keeps the payload readable exactly like C's
	// ((event_payload_t *)e)->payload.
	Payload string

	// concrete points back to the subtype allocation this base event
	// was extracted from via AsEvent(). In C an event_t* IS the
	// subtype allocation (event_* structs embed event_t first), so a
	// receiver can always cast back. Go cannot recover the outer
	// struct from &inner safely, so creators set this to let receivers
	// recover the full object (e.g. *EventSelectTrack) after the event
	// travelled through channels/property couriers as *Event.
	concrete any

	pm *propcore.PropManager
}

// Concrete returns the original subtype object this event was
// extracted from via AsEvent(), or the base event itself when it is a
// plain *Event (or one created without a subtype). C: a cast of
// event_t* back to event_subtype_t*.
func (e *Event) Concrete() any {
	if e != nil && e.concrete != nil {
		return e.concrete
	}
	return e
}

// ConcreteOf unwraps v to the original event object: when v is an
// *Event carrying a concrete back-pointer it returns that subtype,
// otherwise v is returned unchanged. Non-event values pass through.
func ConcreteOf(v any) any {
	if e, ok := v.(*Event); ok && e != nil {
		return e.Concrete()
	}
	return v
}

// SetConcrete links a base event to the subtype allocation it was
// extracted from. Needed for subtype literals created outside this
// package (creators inside pkg/event set it themselves).
func (e *Event) SetConcrete(c any) {
	if e != nil {
		e.concrete = c
	}
}

// EventInt represents an event with an integer value
type EventInt struct {
	Event
	Val int
}

// EventPayload represents an event with string payload (C:
// event_payload_t). The payload lives on the embedded base Event so it
// survives *Event flattening — the Go equivalent of C casting event_t*
// back to event_payload_t*.
type EventPayload struct {
	Event
}

// EventInt3 represents an event with three integer values
type EventInt3 struct {
	Event
	Val1 int
	Val2 int
	Val3 int
}

// EventTs — C: event_ts_t (media.h:86-90) — carries a timestamp
// (seek target / current time) and the media epoch it applies to.
type EventTs struct {
	Event
	Ts    int64
	Epoch int
}

// EventOpenURL represents an open URL event
type EventOpenURL struct {
	Event
	URL         string
	View        string
	ItemModel   any // *propcore.Prop
	ParentModel any // *propcore.Prop
	How         string
	ParentURL   string
}

// EventOpenURLArgs represents arguments for creating an open URL event
type EventOpenURLArgs struct {
	URL         string
	View        string
	ItemModel   any // *propcore.Prop
	ParentModel any // *propcore.Prop
	How         string
	ParentURL   string
}

// EventPlayURL represents a play URL event
type EventPlayURL struct {
	Event
	URL         string
	Primary     bool
	Priority    int
	NoAudio     bool
	ItemModel   any // *propcore.Prop
	ParentModel any // *propcore.Prop
	How         string
	ParentURL   string
}

// EventPlayURLArgs represents arguments for creating a play URL event
type EventPlayURLArgs struct {
	URL         string
	Primary     bool
	Priority    int
	NoAudio     bool
	ItemModel   any // *propcore.Prop
	ParentModel any // *propcore.Prop
	How         string
	ParentURL   string
}

// EventSelectTrack represents a track selection event
type EventSelectTrack struct {
	Event
	Manual bool
	ID     string
}

// EventActionVector represents a vector of actions
type EventActionVector struct {
	Event
	Actions []ActionType
}

// EventPlayTrack represents a play track event
type EventPlayTrack struct {
	Event
	Track  any // *propcore.Prop
	Source any // *propcore.Prop
	Mode   int
}

// EventPlayTrackArgs carries the EventPlayTrack fields on the base
// Event so they survive flattening to *Event.
type EventPlayTrackArgs struct {
	Track  any
	Source any
	Mode   int
}

// EventProp represents an event with a property
type EventProp struct {
	Event
	P any // *propcore.Prop
}

// EventPropAction represents a property action event
type EventPropAction struct {
	Event
	P      any // *propcore.Prop
	Action string
}

// EventScroll represents a scroll event
type EventScroll struct {
	Event
	DX   float32
	DY   float32
	Mode int
}

// Scroll modes
const (
	EventScrollModePixels = 0
	EventScrollModeLines  = 1
	EventScrollModePages  = 2
)

// EventManager manages event creation and action mappings
type EventManager struct {
	actionNames        map[string]ActionType
	reverseActionNames map[ActionType]string
	uiEventSink        chan *Event
	eventSink          chan *Event
	pm                 *propcore.PropManager

	// navEventSinkProp is the prop to send NAV events to via SendExtEvent.
	// C: prop_send_ext_event(nav.eventSink, e) in event_dispatch / glw_inject_event
	navEventSinkProp *propcore.Prop

	// eventSinkTestHook is an optional test-only hook called from Dispatch.
	// It receives every event dispatched via Dispatch(). Used by E9 runtime
	// tests to verify event propagation without modifying production code paths.
	eventSinkTestHook func(e *Event)
}

// NewEventManager creates a new event manager with default action mappings
func NewEventManager(pm *propcore.PropManager) *EventManager {
	return &EventManager{
		actionNames: map[string]ActionType{
			"Up":                ACTION_UP,
			"Down":              ACTION_DOWN,
			"Left":              ACTION_LEFT,
			"Right":             ACTION_RIGHT,
			"Activate":          ACTION_ACTIVATE,
			"Click":             ACTION_CLICK,
			"Enter":             ACTION_ENTER,
			"Submit":            ACTION_SUBMIT,
			"Ok":                ACTION_OK,
			"Cancel":            ACTION_CANCEL,
			"Backspace":         ACTION_BS,
			"Delete":            ACTION_DELETE,
			"MoveUp":            ACTION_MOVE_UP,
			"MoveDown":          ACTION_MOVE_DOWN,
			"MoveLeft":          ACTION_MOVE_LEFT,
			"MoveRight":         ACTION_MOVE_RIGHT,
			"Forward":           ACTION_NAV_FWD,
			"Back":              ACTION_NAV_BACK,
			"FocusNext":         ACTION_FOCUS_NEXT,
			"FocusPrev":         ACTION_FOCUS_PREV,
			"PageUp":            ACTION_PAGE_UP,
			"PageDown":          ACTION_PAGE_DOWN,
			"Top":               ACTION_TOP,
			"Bottom":            ACTION_BOTTOM,
			"Increase":          ACTION_INCR,
			"Decrease":          ACTION_DECR,
			"Stop":              ACTION_STOP,
			"PlayPause":         ACTION_PLAYPAUSE,
			"Play":              ACTION_PLAY,
			"Pause":             ACTION_PAUSE,
			"Eject":             ACTION_EJECT,
			"Record":            ACTION_RECORD,
			"PreviousTrack":     ACTION_SKIP_BACKWARD,
			"NextTrack":         ACTION_SKIP_FORWARD,
			"SeekForward":       ACTION_SEEK_FORWARD,
			"SeekReverse":       ACTION_SEEK_BACKWARD,
			"VolumeUp":          ACTION_VOLUME_UP,
			"VolumeDown":        ACTION_VOLUME_DOWN,
			"VolumeMuteToggle":  ACTION_VOLUME_MUTE_TOGGLE,
			"Menu":              ACTION_MENU,
			"ItemMenu":          ACTION_ITEMMENU,
			"LogWindow":         ACTION_LOGWINDOW,
			"Select":            ACTION_SELECT,
			"MediaStats":        ACTION_SHOW_MEDIA_STATS,
			"Home":              ACTION_HOME,
			"SysHome":           ACTION_SYSTEM_HOME,
			"Reset":             ACTION_RESET,
			"Copy":              ACTION_COPY,
			"Paste":             ACTION_PASTE,
			"ChangeView":        ACTION_SWITCH_VIEW,
			"FullscreenToggle":  ACTION_FULLSCREEN_TOGGLE,
			"Channel+":          ACTION_NEXT_CHANNEL,
			"Channel-":          ACTION_PREV_CHANNEL,
			"ZoomUI+":           ACTION_ZOOM_UI_INCR,
			"ZoomUI-":           ACTION_ZOOM_UI_DECR,
			"ZoomUIReset":       ACTION_ZOOM_UI_RESET,
			"ReloadUI":          ACTION_RELOAD_UI,
			"Restart":           ACTION_RESTART,
			"Quit":              ACTION_QUIT,
			"Standby":           ACTION_STANDBY,
			"PowerOff":          ACTION_POWER_OFF,
			"Reboot":            ACTION_REBOOT,
			"Shuffle":           ACTION_SHUFFLE,
			"Repeat":            ACTION_REPEAT,
			"EnableScreenSaver": ACTION_ENABLE_SCREENSAVER,
			"AudioTrack":        ACTION_CYCLE_AUDIO,
			"SubtitleTrack":     ACTION_CYCLE_SUBTITLE,
			"ReloadData":        ACTION_RELOAD_DATA,
			"Playqueue":         ACTION_PLAYQUEUE,
			"Sysinfo":           ACTION_SYSINFO,
			"SwitchUI":          ACTION_SWITCH_UI,
		},
		reverseActionNames: map[ActionType]string{
			ACTION_UP:                 "Up",
			ACTION_DOWN:               "Down",
			ACTION_LEFT:               "Left",
			ACTION_RIGHT:              "Right",
			ACTION_ACTIVATE:           "Activate",
			ACTION_CLICK:              "Click",
			ACTION_ENTER:              "Enter",
			ACTION_SUBMIT:             "Submit",
			ACTION_OK:                 "Ok",
			ACTION_CANCEL:             "Cancel",
			ACTION_BS:                 "Backspace",
			ACTION_DELETE:             "Delete",
			ACTION_MOVE_UP:            "MoveUp",
			ACTION_MOVE_DOWN:          "MoveDown",
			ACTION_MOVE_LEFT:          "MoveLeft",
			ACTION_MOVE_RIGHT:         "MoveRight",
			ACTION_NAV_FWD:            "Forward",
			ACTION_NAV_BACK:           "Back",
			ACTION_FOCUS_NEXT:         "FocusNext",
			ACTION_FOCUS_PREV:         "FocusPrev",
			ACTION_PAGE_UP:            "PageUp",
			ACTION_PAGE_DOWN:          "PageDown",
			ACTION_TOP:                "Top",
			ACTION_BOTTOM:             "Bottom",
			ACTION_INCR:               "Increase",
			ACTION_DECR:               "Decrease",
			ACTION_STOP:               "Stop",
			ACTION_PLAYPAUSE:          "PlayPause",
			ACTION_PLAY:               "Play",
			ACTION_PAUSE:              "Pause",
			ACTION_EJECT:              "Eject",
			ACTION_RECORD:             "Record",
			ACTION_SKIP_BACKWARD:      "PreviousTrack",
			ACTION_SKIP_FORWARD:       "NextTrack",
			ACTION_SEEK_FORWARD:       "SeekForward",
			ACTION_SEEK_BACKWARD:      "SeekReverse",
			ACTION_VOLUME_UP:          "VolumeUp",
			ACTION_VOLUME_DOWN:        "VolumeDown",
			ACTION_VOLUME_MUTE_TOGGLE: "VolumeMuteToggle",
			ACTION_MENU:               "Menu",
			ACTION_ITEMMENU:           "ItemMenu",
			ACTION_COPY:               "Copy",
			ACTION_PASTE:              "Paste",
			ACTION_LOGWINDOW:          "LogWindow",
			ACTION_SELECT:             "Select",
			ACTION_SHOW_MEDIA_STATS:   "MediaStats",
			ACTION_HOME:               "Home",
			ACTION_SYSTEM_HOME:        "SysHome",
			ACTION_RESET:              "Reset",
			ACTION_SWITCH_VIEW:        "ChangeView",
			ACTION_FULLSCREEN_TOGGLE:  "FullscreenToggle",
			ACTION_NEXT_CHANNEL:       "Channel+",
			ACTION_PREV_CHANNEL:       "Channel-",
			ACTION_ZOOM_UI_INCR:       "ZoomUI+",
			ACTION_ZOOM_UI_DECR:       "ZoomUI-",
			ACTION_ZOOM_UI_RESET:      "ZoomUIReset",
			ACTION_RELOAD_UI:          "ReloadUI",
			ACTION_RESTART:            "Restart",
			ACTION_QUIT:               "Quit",
			ACTION_STANDBY:            "Standby",
			ACTION_POWER_OFF:          "PowerOff",
			ACTION_REBOOT:             "Reboot",
			ACTION_SHUFFLE:            "Shuffle",
			ACTION_REPEAT:             "Repeat",
			ACTION_ENABLE_SCREENSAVER: "EnableScreenSaver",
			ACTION_CYCLE_AUDIO:        "AudioTrack",
			ACTION_CYCLE_SUBTITLE:     "SubtitleTrack",
			ACTION_RELOAD_DATA:        "ReloadData",
			ACTION_PLAYQUEUE:          "Playqueue",
			ACTION_SYSINFO:            "Sysinfo",
			ACTION_SWITCH_UI:          "SwitchUI",
		},
		uiEventSink: make(chan *Event, 100),
		eventSink:   make(chan *Event, 100),
		pm:          pm,
	}
}

// Create creates a new event
func (em *EventManager) Create(eventType EventType, size int) *Event {
	e := &Event{
		Timestamp: time.Now().UnixNano() / 1000000,
		Type:      eventType,
		RefCount:  1,
		pm:        em.pm,
	}
	return e
}

// CreateType creates an event with a specific type
func (em *EventManager) CreateType(eventType EventType) *Event {
	return em.Create(eventType, 0)
}

// CreateInt creates an event with an integer value
func (em *EventManager) CreateInt(eventType EventType, val int) *EventInt {
	e := &EventInt{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      eventType,
			RefCount:  1,
			pm:        em.pm,
		},
		Val: val,
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// CreateInt3 creates an event with three integer values
func (em *EventManager) CreateInt3(eventType EventType, v1, v2, v3 int) *EventInt3 {
	e := &EventInt3{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      eventType,
			RefCount:  1,
			pm:        em.pm,
		},
		Val1: v1,
		Val2: v2,
		Val3: v3,
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// AddRef increments the reference count.
// C: atomic_inc(&e->e_refcount)
func (e *Event) AddRef() {
	if e != nil {
		atomic.AddInt32(&e.RefCount, 1)
	}
}

// Release decrements the reference count and frees the event if zero
func (e *Event) Release() {
	if e == nil {
		return
	}
	if atomic.AddInt32(&e.RefCount, -1) > 0 {
		return
	}
	if e.Dtor != nil {
		e.Dtor(e)
	}
	if e.Nav != nil {
		e.pm.RefDec(e.Nav)
		e.Nav = nil
	}
}

// ActionCode2Str converts an action code to string
func (em *EventManager) ActionCode2Str(code ActionType) string {
	if str, ok := em.reverseActionNames[code]; ok {
		return str
	}
	return fmt.Sprintf("unknown(%d)", code)
}

// ActionStr2Code converts a string to action code.
// C: action_str2code → str2val → str2val0 uses strcasecmp
// (misc/strtab.h:35-43) — the lookup is CASE-INSENSITIVE.
func (em *EventManager) ActionStr2Code(str string) ActionType {
	if code, ok := em.actionNames[str]; ok {
		return code
	}
	for name, code := range em.actionNames {
		if strings.EqualFold(name, str) {
			return code
		}
	}
	return ACTION_invalid
}

// ApplyMetadata copies metadata from source to destination event
func (dst *Event) ApplyMetadata(src *Event) {
	if src == nil {
		return
	}
	dst.Timestamp = src.Timestamp
	if src.Flags&EventScreenPosition != 0 {
		dst.Flags |= EventScreenPosition
		dst.ScreenX = src.ScreenX
		dst.ScreenY = src.ScreenY
	}
}

// Clone creates a copy of the event
func (e *Event) Clone() *Event {
	if e.CloneFunc == nil {
		return nil
	}
	return e.CloneFunc(e)
}

// AttachNav sets the navigator property reference for the event.
// Equivalent to C's `e->e_nav = prop_ref_inc(gr_prop_nav)`.
// The refcount increment keeps the nav prop alive for the event's lifetime.
// On event release, RefDec is called (see Event.Release).
func (e *Event) AttachNav(nav *propcore.Prop) {
	if e == nil || nav == nil {
		return
	}
	if e.pm != nil {
		e.pm.RefInc(nav)
		if e.Nav != nil {
			e.pm.RefDec(e.Nav)
		}
	}
	e.Nav = nav
}

// CreateStr creates an event with string payload
func (em *EventManager) CreateStr(eventType EventType, str string) *EventPayload {
	e := &EventPayload{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      eventType,
			RefCount:  1,
			Payload:   str,
			pm:        em.pm,
		},
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// CreatePlayURLArgs creates a play URL event from arguments
func (em *EventManager) CreatePlayURLArgs(args *EventPlayURLArgs) *EventPlayURL {
	e := &EventPlayURL{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      EVENT_PLAY_URL,
			RefCount:  1,
			pm:        em.pm,
		},
		URL:         args.URL,
		How:         args.How,
		Primary:     args.Primary,
		Priority:    args.Priority,
		NoAudio:     args.NoAudio,
		ParentURL:   args.ParentURL,
		ItemModel:   args.ItemModel,
		ParentModel: args.ParentModel,
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// CreateOpenURLArgs creates an open URL event from arguments.
//
// C-canonical: event_create_openurl_args (event.c:312-325) calls
// prop_ref_inc on item_model and parent_model, and event_openurl_dtor
// (event.c:296-306) calls prop_ref_dec. This keeps the props alive
// for the event's lifetime. Go: we do the same via pm.RefInc/RefDec
// when the model values are *propcore.Prop.
func (em *EventManager) CreateOpenURLArgs(args *EventOpenURLArgs) *EventOpenURL {
	// C: e->item_model = prop_ref_inc(args->item_model)
	// C: e->parent_model = prop_ref_inc(args->parent_model)
	itemProp := em.refIncPropIfProp(args.ItemModel)
	parentProp := em.refIncPropIfProp(args.ParentModel)

	e := &EventOpenURL{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      EVENT_OPENURL,
			RefCount:  1,
			OpenURL:   args, // preserve 6 fields through channel round-trips
			pm:        em.pm,
			// C: e->h.e_dtor = event_openurl_dtor
			// The dtor releases the prop references captured above.
			Dtor: func(_ *Event) {
				// C: prop_ref_dec(ou->item_model); prop_ref_dec(ou->parent_model)
				if itemProp != nil {
					em.pm.RefDec(itemProp)
				}
				if parentProp != nil {
					em.pm.RefDec(parentProp)
				}
			},
		},
		URL:         args.URL,
		View:        args.View,
		How:         args.How,
		ParentURL:   args.ParentURL,
		ItemModel:   args.ItemModel,
		ParentModel: args.ParentModel,
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// refIncPropIfProp calls pm.RefInc if v is a *propcore.Prop and returns it.
// Equivalent to C's prop_ref_inc (which is safe on NULL and returns NULL).
func (em *EventManager) refIncPropIfProp(v any) *propcore.Prop {
	if p, ok := v.(*propcore.Prop); ok && p != nil {
		em.pm.RefInc(p)
		return p
	}
	return nil
}

// CreatePlayTrack creates a play track event
func (em *EventManager) CreatePlayTrack(track, source any, mode int) *EventPlayTrack {
	e := &EventPlayTrack{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      EVENT_PLAYTRACK,
			RefCount:  1,
			pm:        em.pm,
		},
		Track:  track,
		Source: source,
		Mode:   mode,
	}
	e.Event.PlayTrack = &EventPlayTrackArgs{Track: track, Source: source, Mode: mode}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// CreateSelectTrack creates a select track event
func (em *EventManager) CreateSelectTrack(id string, eventType EventType, manual bool) *EventSelectTrack {
	e := &EventSelectTrack{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      eventType,
			RefCount:  1,
			pm:        em.pm,
		},
		ID:     id,
		Manual: manual,
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// AsEvent returns the embedded base event. On subtypes the promoted
// method returns &subtype.Event — equivalent to C where every event_*
// embeds event_t as its first member.
func (e *Event) AsEvent() *Event { return e }

// BaseEvent returns the base event_t* view of any event value: either
// an already-flattened *Event or any *event_* subtype (whose promoted
// AsEvent yields its embedded base). C: every subsystem just casts
// event_* to event_t* — same thing here.
func BaseEvent(e any) *Event {
	if b, ok := e.(interface{ AsEvent() *Event }); ok {
		return b.AsEvent()
	}
	return nil
}

// GetType returns the event type (C: e->e_type). Defined on *Event so all
// embedded subtypes satisfy `interface{ GetType() EventType }` uniformly.
func (e *Event) GetType() EventType { return e.Type }

// ActionUpdateHoldByEvent updates hold state based on event
func (em *EventManager) ActionUpdateHoldByEvent(hold int, e *Event) int {
	if e.IsAction(ACTION_PLAYPAUSE) {
		return 1 - hold
	}
	if e.IsAction(ACTION_PAUSE) {
		return 1
	}
	if e.IsAction(ACTION_PLAY) {
		return 0
	}
	return 0
}

// CreateActionMulti creates an event with multiple actions
func (em *EventManager) CreateActionMulti(actions []ActionType) *EventActionVector {
	e := &EventActionVector{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      EVENT_ACTION_VECTOR,
			RefCount:  1,
			pm:        em.pm,
			Actions:   make([]ActionType, len(actions)),
		},
		Actions: make([]ActionType, len(actions)),
	}
	copy(e.Actions, actions)
	copy(e.Event.Actions, actions)
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// CreateAction creates an event with a single action
func (em *EventManager) CreateAction(action ActionType) *EventActionVector {
	return em.CreateActionMulti([]ActionType{action})
}

// CreateActionStr creates an action event from string
func (em *EventManager) CreateActionStr(str string) *Event {
	a := em.ActionStr2Code(str)
	if a == ACTION_invalid {
		return &em.CreateStr(EVENT_DYNAMIC_ACTION, str).Event
	}
	return em.CreateAction(a).AsEvent()
}

// AsEvent converts EventActionVector to Event
func (e *EventActionVector) AsEvent() *Event {
	return &e.Event
}

// IsAction checks if event contains the specified action
func IsAction(e *Event, at ActionType) bool {
	if e.Type != EVENT_ACTION_VECTOR {
		return false
	}
	return slices.Contains(e.Actions, at)
}

// IsAction checks if event contains the specified action (method)
func (e *Event) IsAction(at ActionType) bool {
	return IsAction(e, at)
}

// CreateProp creates an event with a property.
// C: event_create_prop (event.c:479-485) calls prop_ref_inc(p) to keep
// the prop alive for the event's lifetime.
func (em *EventManager) CreateProp(eventType EventType, p any) *EventProp {
	// C: e->p = prop_ref_inc(p)
	em.refIncPropIfProp(p)
	e := &EventProp{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      eventType,
			RefCount:  1,
			pm:        em.pm,
		},
		P: p,
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// CreatePropAction creates a property action event
func (em *EventManager) CreatePropAction(p any, action string) *EventPropAction {
	e := &EventPropAction{
		Event: Event{
			Timestamp: time.Now().UnixNano() / 1000000,
			Type:      EVENT_PROP_ACTION,
			RefCount:  1,
			pm:        em.pm,
			CloneFunc: func(src *Event) *Event {
				s, ok := src.Concrete().(*EventPropAction)
				if !ok {
					return src
				}
				return em.CreatePropAction(s.P, s.Action).AsEvent()
			},
		},
		P:      p,
		Action: action,
	}
	e.concrete = e // C: event_t* is the subtype allocation
	return e
}

// AsEvent converts EventPropAction to Event
func (e *EventPropAction) AsEvent() *Event {
	return &e.Event
}

// ToUI sends an event to the UI event sink
func (em *EventManager) ToUI(e *Event) {
	if e == nil {
		return
	}
	select {
	case em.uiEventSink <- e:
	default:
		// Channel full, drop event
		e.Release()
	}
}

// DispatchAction is a convenience method that creates an action event from
// a string name and dispatches it. This mirrors C's event_dispatch(event_create_action(...)).
func (em *EventManager) DispatchAction(action string) {
	if em == nil {
		return
	}
	evt := em.CreateActionStr(action)
	if evt != nil {
		em.Dispatch(evt)
	}
}

// Dispatch dispatches an event to the appropriate handlers.
//
// C-canonical implementation of event_dispatch (event.c:554-619).
//
// C flow:
//  1. Convert space (EVENT_UNICODE val=32) to ACTION_PLAYPAUSE
//  2. event_to_prop(global.eventSink, e) — always sends to global eventSink prop
//  3. Route based on event type:
//     - NAV_BACK/FWD/HOME/PLAYQUEUE/RELOAD_DATA/EVENT_OPENURL → global.navigators.current.eventSink
//     - VOLUME_UP/DOWN → adjust global.audio.mastervolume by ±1
//     - VOLUME_MUTE_TOGGLE → toggle global.audio.mastermute
//     - Media actions → global.media.eventSink
//     - EVENT_PLAYTRACK → global.playqueue.eventSink
//  4. event_release(e)
func (em *EventManager) Dispatch(e *Event) {
	if e == nil {
		return
	}

	// C: event.c:559-563 — convert space to playpause
	if e.Type == EVENT_UNICODE {
		if ei, ok := any(e).(*EventInt); ok && ei.Val == 32 {
			e.Release()
			e = em.CreateAction(ACTION_PLAYPAUSE).AsEvent()
		}
	}

	// Test hook: receive event for verification (test-only, no production side effects)
	if em.eventSinkTestHook != nil {
		em.eventSinkTestHook(e)
	}

	// C: event.c:565-566 — event_to_prop(global.eventSink, e)
	// Always send to the global eventSink prop (fires runcontrol subscription).
	// Also send to the eventSink channel for Go-internal consumers.
	if gp := em.pm.GetGlobal(); gp != nil {
		if es := em.getOrCreatePropByPath(gp, "eventSink"); es != nil {
			em.pm.SendExtEvent(es, e)
		}
	}

	// C: event.c:568-616 — route based on event type
	isNavEvent := e.IsAction(ACTION_NAV_BACK) ||
		e.IsAction(ACTION_NAV_FWD) ||
		e.IsAction(ACTION_HOME) ||
		e.IsAction(ACTION_PLAYQUEUE) ||
		e.IsAction(ACTION_RELOAD_DATA) ||
		e.Type == EVENT_OPENURL

	if isNavEvent {
		// C: event.c:574-575 — event_to_prop(global.navigators.current.eventSink, e)
		// "current" is a linkselected prop: resolve it per dispatch so
		// events reach the navigator spawned by the active UI, not a
		// fixed one captured at init.
		if p := em.resolveNavEventSink(); p != nil {
			em.pm.SendExtEvent(p, e)
		}
	} else if e.IsAction(ACTION_VOLUME_UP) || e.IsAction(ACTION_VOLUME_DOWN) {
		// C: event.c:577-582 — adjust global.audio.mastervolume by ±1
		if gp := em.pm.GetGlobal(); gp != nil {
			if p := em.getOrCreatePropByPath(gp, "audio", "mastervolume"); p != nil {
				delta := float32(1)
				if e.IsAction(ACTION_VOLUME_DOWN) {
					delta = -1
				}
				p.AddFloat(delta)
			}
		}
	} else if e.IsAction(ACTION_VOLUME_MUTE_TOGGLE) {
		// C: event.c:584-588 — toggle global.audio.mastermute
		if gp := em.pm.GetGlobal(); gp != nil {
			if p := em.getOrCreatePropByPath(gp, "audio", "mastermute"); p != nil {
				v := em.pm.GetInt(p, 0)
				if v != 0 {
					p.SetInt(0)
				} else {
					p.SetInt(1)
				}
			}
		}
	} else if e.IsAction(ACTION_SEEK_BACKWARD) ||
		e.IsAction(ACTION_SEEK_FORWARD) ||
		e.IsAction(ACTION_PLAYPAUSE) ||
		e.IsAction(ACTION_PLAY) ||
		e.IsAction(ACTION_PAUSE) ||
		e.IsAction(ACTION_STOP) ||
		e.IsAction(ACTION_EJECT) ||
		e.IsAction(ACTION_SKIP_BACKWARD) ||
		e.IsAction(ACTION_SKIP_FORWARD) ||
		e.IsAction(ACTION_SHOW_MEDIA_STATS) ||
		e.IsAction(ACTION_SHUFFLE) ||
		e.IsAction(ACTION_REPEAT) ||
		e.IsAction(ACTION_NEXT_CHANNEL) ||
		e.IsAction(ACTION_PREV_CHANNEL) ||
		e.IsAction(ACTION_CYCLE_AUDIO) ||
		e.IsAction(ACTION_CYCLE_SUBTITLE) ||
		e.Type == EVENT_DELTA_SEEK_REL ||
		e.Type == EVENT_SELECT_AUDIO_TRACK ||
		e.Type == EVENT_SELECT_SUBTITLE_TRACK {
		// C: event.c:610-611 — event_to_prop(global.media.eventSink, e)
		if gp := em.pm.GetGlobal(); gp != nil {
			if p := em.getOrCreatePropByPath(gp, "media", "eventSink"); p != nil {
				em.pm.SendExtEvent(p, e)
			}
		}
	} else if e.Type == EVENT_PLAYTRACK {
		// C: event.c:613-614 — event_to_prop(global.playqueue.eventSink, e)
		if gp := em.pm.GetGlobal(); gp != nil {
			if p := em.getOrCreatePropByPath(gp, "playqueue", "eventSink"); p != nil {
				em.pm.SendExtEvent(p, e)
			}
		}
	}

	// C: event.c:618 — event_release(e)
	// Also send to the eventSink channel for Go-internal consumers (tests, etc).
	// The producer's ref is consumed here: either sent to eventSink channel
	// (consumer will Release) or dropped (Release here).
	select {
	case em.eventSink <- e:
	default:
		// Channel full, drop event from eventSink channel.
		e.Release()
	}
}

// getOrCreatePropByPath resolves a prop by path components from a parent,
// auto-creating children as needed. This matches C's prop_get_by_name with
// create=1, which auto-creates missing props in the path.
func (em *EventManager) getOrCreatePropByPath(parent *propcore.Prop, names ...string) *propcore.Prop {
	if parent == nil || em.pm == nil {
		return nil
	}
	current := parent
	for _, name := range names {
		if name == "" {
			continue
		}
		child := current.GetChild(name)
		if child == nil {
			child = em.pm.CreateEx(current, name, nil, false, false)
		}
		if child == nil {
			return nil
		}
		current = child
	}
	return current
}

// EventToUI sends an event to global.userinterfaces.ui.eventSink.
// C: event_to_ui (event.c:541-547):
//
//	event_to_prop(prop_get_by_name(PNVEC("global", "userinterfaces",
//		"ui", "eventSink"), 1, NULL), e);
//	event_release(e);
//
// where event_to_prop (event.c:530-535) is:
//
//	prop_send_ext_event(p, e);
//	prop_ref_dec(p);
func (em *EventManager) EventToUI(e *Event) {
	if em == nil || e == nil {
		return
	}
	p := em.pm.GetByName([]string{"global", "userinterfaces", "ui", "eventSink"}, 1, nil, nil)
	em.pm.SendExtEvent(p, e)
	if p != nil {
		p.Release()
	}
	e.Release()
}

// GetUISink returns the UI event sink channel
func (em *EventManager) GetUISink() <-chan *Event {
	return em.uiEventSink
}

// SendNavEvent sends an event directly to the navigator's eventSink prop
// via SendExtEvent. Equivalent to C's glw_root_event_handler
// (glw.c:1890-1894): prop_send_ext_event(nav.eventSink, e).
// This does NOT re-route through event_dispatch (which would double-deliver
// to global.eventSink). The caller retains ownership of the event.
func (em *EventManager) SendNavEvent(e *Event) {
	if em == nil || e == nil {
		return
	}
	if p := em.resolveNavEventSink(); p != nil {
		em.pm.SendExtEvent(p, e)
	}
}

// resolveNavEventSink resolves global.navigators.current.eventSink —
// the eventSink of the navigator currently selected via prop_select
// (i.e. the one the active UI spawned). Falls back to the fixed
// navEventSinkProp when no navigator is selected yet (pre-UI init,
// headless tests). ResolvePath does not create missing nodes, matching
// C's prop_get_by_name for this path.
func (em *EventManager) resolveNavEventSink() *propcore.Prop {
	if gp := em.pm.GetGlobal(); gp != nil {
		if p := gp.ResolvePath([]string{"navigators", "current", "eventSink"}, true); p != nil {
			return p
		}
	}
	return em.navEventSinkProp
}

// GetEventSink returns the global event sink channel
func (em *EventManager) GetEventSink() <-chan *Event {
	return em.eventSink
}

// SetNavEventSinkProp sets the prop to route NAV events to via SendExtEvent.
// C: prop_send_ext_event(nav.eventSink, e) in event_dispatch / glw_inject_event
func (em *EventManager) SetNavEventSinkProp(p *propcore.Prop) {
	em.navEventSinkProp = p
}

// SetEventSinkTestHook sets a test-only hook that receives every event
// dispatched via Dispatch(). This is used by E9 runtime tests to verify
// event propagation. The hook is called before the event is sent to the
// channel sinks, so it sees the event regardless of whether the channel
// is full. The hook must NOT call Release() or modify the event.
func (em *EventManager) SetEventSinkTestHook(fn func(e *Event)) {
	em.eventSinkTestHook = fn
}

// FromFkey creates an event from function key
func (em *EventManager) FromFkey(keynum, mod uint) *Event {
	if keynum < 1 || keynum > 12 || mod > 1 {
		return nil
	}

	// F-key mapping table [keynum][mod]
	actionFromFkey := [13][2]ActionType{
		{0, 0},
		{ACTION_MENU, ACTION_PLAYQUEUE},
		{ACTION_SHOW_MEDIA_STATS, ACTION_SKIP_BACKWARD},
		{ACTION_ITEMMENU, ACTION_SKIP_FORWARD},
		{ACTION_LOGWINDOW, ACTION_ENABLE_SCREENSAVER},
		{ACTION_RELOAD_UI, ACTION_RELOAD_DATA},
		{ACTION_SYSINFO, ACTION_CYCLE_SUBTITLE},
		{0, ACTION_SEEK_BACKWARD},
		{ACTION_STOP, ACTION_PLAYPAUSE},
		{ACTION_SWITCH_VIEW, ACTION_SEEK_FORWARD},
		{0, ACTION_VOLUME_MUTE_TOGGLE},
		{ACTION_FULLSCREEN_TOGGLE, ACTION_VOLUME_DOWN},
		{ACTION_SWITCH_UI, ACTION_VOLUME_UP},
	}

	a := actionFromFkey[keynum][mod]
	if a == 0 {
		return nil
	}
	return em.CreateAction(a).AsEvent()
}

// EventTs returns the timestamp of an event
func (em *EventManager) EventTs(e *Event) int64 {
	if e == nil {
		return 0
	}
	return e.Timestamp
}

// Sprint returns a string representation of the event
func (em *EventManager) Sprint(e *Event) string {
	if e == nil {
		return "(null)"
	}
	switch e.Type {
	case EVENT_OPENURL:
		if eo, ok := any(e).(*EventOpenURL); ok {
			return fmt.Sprintf("openurl(%s)", eo.URL)
		}
	case EVENT_DYNAMIC_ACTION:
		return e.Payload
	case EVENT_ACTION_VECTOR:
		var result strings.Builder
		for i, action := range e.Actions {
			if i > 0 {
				result.WriteString(", ")
			}
			result.WriteString(em.ActionCode2Str(action))
		}
		return result.String()
	case EVENT_PROP_ACTION:
		if epa, ok := any(e).(*EventPropAction); ok {
			return epa.Action
		}
	default:
		return fmt.Sprintf("event<%d>", e.Type)
	}
	return fmt.Sprintf("event<%d>", e.Type)
}

// GconfEnableInputEventDebug — C: gconf.enable_input_event_debug

// SetEnableInputEventDebug — C: gconf field written by settings.c dev bool.
