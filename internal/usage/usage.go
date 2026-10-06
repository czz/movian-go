// Package usage is the port of src/usage.c — the app's usage reporter.
// C keeps file-scope state (usageMutex, eventmap, usage_callout,
// start_sent, send_fails, session_starttime) driven by
// usage_start/usage_event/usage_page_open and POSTs Countly-style
// reports; the Go port puts the same state machine in *Reporter and
// ships the reports to a Matomo tracker (see sendmsg).
package usage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/czz/movian-go/internal/arch"
	"github.com/czz/movian-go/internal/callout"
	facore "github.com/czz/movian-go/internal/fileaccess"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/task"
	"github.com/czz/movian-go/internal/version"
)

// Analytics transport — C hardcodes the Countly SDK bulk endpoint
// (POST http://analytics1.movian.tv/i/bulk, requests=<json>). The Go
// port targets a Matomo tracker (HTTP Tracking API, matomo.php)
// instead; Config.TrackerURL/SiteID override these defaults.
const (
	DefaultTrackerURL = "https://analytics.czz78.com/matomo.php"
	DefaultSiteID     = "1"
)

// Config — the gconf.* fields C reads at send time (usage.c). Kept
// behind a pointer in Reporter so values filled later in init
// (device_id, lang) and runtime toggles (Disabled, from the
// settings:dev page) are read live, like C reads gconf.
type Config struct {
	DeviceID   string        // C: gconf.device_id
	OSInfo     string        // C: gconf.os_info → metrics._os_version
	DeviceType string        // C: gconf.device_type → metrics._device
	Disabled   int           // C: gconf.disable_analytics
	ShowEvents int           // C: gconf.show_usage_events
	Lang       func() string // C: gconf.lang (set by i18n)
	Track      func() string // C: upgrade_get_track()
	TrackerURL string        // empty → DefaultTrackerURL
	SiteID     string        // empty → DefaultSiteID
}

// Reporter — C: the usage.c file-scope globals. Methods are
// nil-receiver safe so call sites wired before Start (or not wired at
// all in tests) are harmless no-ops.
type Reporter struct {
	cfg *Config

	// usage.c file-scope state
	mu               sync.Mutex
	eventmap         *htsmsg.HTSMsg
	usageCallout     callout.Callout
	cs               *callout.CalloutSystem    // C: callout_* globals — injected
	tasks            *task.TaskSystem          // C: task_run globals — injected
	fam              *facore.FileAccessManager // C: implicit global fa context
	startSent        bool
	sendFails        int
	sessionStartTime int64
}

// New — C: usage_early_init (usage.c:34-38) registered
// INITME(INIT_GROUP_NET). The mutex is initialized implicitly in Go.
// SetCalloutSystem injects the callout system (C: callout_* globals).
func (r *Reporter) SetCalloutSystem(cs *callout.CalloutSystem) { r.cs = cs }

// SetTaskSystem injects the task system (C: task_run globals, task.c).
func (r *Reporter) SetTaskSystem(ts *task.TaskSystem) { r.tasks = ts }

func New(cfg *Config) *Reporter {
	if cfg == nil {
		cfg = &Config{}
	}
	return &Reporter{cfg: cfg}
}

// SetFileAccessManager injects the fa context (C: global fa state).
func (r *Reporter) SetFileAccessManager(fam *facore.FileAccessManager) {
	r.fam = fam
}

// Fini — C: usage_fini (usage.c:280-297), INITME(INIT_GROUP_API, ...,
// usage_fini, 0). Disarms the callout and sends a final end_session
// report if a session was started.
func (r *Reporter) Fini() {
	if r == nil {
		return
	}
	if cs := r.cs; cs != nil {
		cs.Disarm(&r.usageCallout)
	}
	if !r.startSent {
		return
	}

	// C: usage.c:290-296 — end_session report. device_id/app_key rode in
	// the map for Countly; the Matomo transport derives the visitor id
	// from cfg.DeviceID directly (analyticsCID).
	m := htsmsg.NewMap()
	m.AddU32("end_session", 1)
	r.addEvents(m)
	r.sendmsg(m)
	m.Release()
}

// Start — C: usage_start (usage.c:172-178).
func (r *Reporter) Start() {
	if r == nil || r.cfg.Disabled != 0 {
		return
	}
	r.tasks.Run(func(opaque any) { r.trySend() }, nil)
}

// analyticsCID — Matomo wants a 16-hex visitor id. device_id is already a
// MAC-MD5 hex string in practice, but md5 makes the mapping deterministic
// for any format.
func (r *Reporter) analyticsCID() string {
	sum := md5.Sum([]byte(r.cfg.DeviceID))
	return hex.EncodeToString(sum[:])[:16]
}

// sendmsg — C: sendmsg (usage.c:91-119). Translates the C report map into
// Matomo hits:
//
//	begin_session    → new_visit=1 + "Session start" action (metrics ride
//	                   in the UA/lang parameters)
//	session_duration → ping=1 (Matomo measures visit length between hits)
//	end_session      → "Session end" event
//	events[]         → e_c=Movian, e_a=<key>, e_n=<seg pairs>, e_v=<count>
//
// Returns the first request error, like C's single http_req result.
func (r *Reporter) sendmsg(m *htsmsg.HTSMsg) int {
	// Metrics ride in the User-Agent so Matomo's device/OS detection gets
	// them; _locale maps to the standard lang parameter.
	ua := "Movian Go/" + version.AppVersion()
	var lang string
	if metrics := m.GetMap("metrics"); metrics != nil {
		var extra []string
		for _, k := range []string{"_os", "_os_version", "_device", "_carrier"} {
			if v := metrics.GetStr(k); v != "" {
				extra = append(extra, v)
			}
		}
		if len(extra) > 0 {
			ua += " (" + strings.Join(extra, "; ") + ")"
		}
		lang = metrics.GetStr("_locale")
	}

	tracker := r.cfg.TrackerURL
	if tracker == "" {
		tracker = DefaultTrackerURL
	}
	site := r.cfg.SiteID
	if site == "" {
		site = DefaultSiteID
	}

	base := func() url.Values {
		v := url.Values{
			"idsite":     {site},
			"rec":        {"1"},
			"apiv":       {"1"},
			"send_image": {"0"},
			"cid":        {r.analyticsCID()},
			"ua":         {ua},
		}
		if lang != "" {
			v.Set("lang", lang)
		}
		return v
	}

	var hits []url.Values
	if m.GetU32OrDefault("begin_session", 0) != 0 {
		v := base()
		v.Set("new_visit", "1")
		v.Set("url", "movian-go://session/start")
		v.Set("action_name", "Session start")
		hits = append(hits, v)
	}
	if m.GetU32OrDefault("session_duration", 0) != 0 {
		v := base()
		v.Set("ping", "1")
		hits = append(hits, v)
	}
	if m.GetU32OrDefault("end_session", 0) != 0 {
		v := base()
		v.Set("e_c", "Movian Go")
		v.Set("e_a", "Session end")
		hits = append(hits, v)
	}
	if events := m.GetList("events"); events != nil {
		for _, f := range events.GetFields() {
			ev := f.GetChilds()
			if ev == nil {
				continue
			}
			v := base()
			v.Set("e_c", "Movian Go")
			v.Set("e_a", ev.GetStr("key"))
			if segs := ev.GetMap("segmentation"); segs != nil {
				var pairs []string
				for _, sf := range segs.GetFields() {
					if s, ok := sf.FieldGetString(); ok {
						pairs = append(pairs, sf.GetName()+"="+s)
					}
				}
				if len(pairs) > 0 {
					v.Set("e_n", strings.Join(pairs, " "))
				}
			}
			v.Set("e_v", fmt.Sprint(ev.GetU32OrDefault("count", 1)))
			hits = append(hits, v)
		}
	}

	rval := 0
	for _, h := range hits {
		err := r.fam.HTTPReq(tracker+"?"+h.Encode(),
			facore.HTTPTagFlags, facore.FaNoDebug)
		if err != 0 && rval == 0 {
			rval = err
		}
	}
	return rval
}

// addEvents — C: add_events (usage.c:125-134). Moves the pending
// eventmap into the report's "events" field.
func (r *Reporter) addEvents(m *htsmsg.HTSMsg) {
	r.mu.Lock()
	if r.eventmap != nil {
		m.AddMsg("events", r.eventmap)
		r.eventmap = nil
	}
	r.mu.Unlock()
}

// usagePeriodic — C: usage_periodic (usage.c:140-144).
func (r *Reporter) usagePeriodic(c *callout.Callout, opaque any) {
	r.tasks.Run(func(o any) { r.trySend() }, nil)
}

// trySend — C: try_send (usage.c:150-163). Builds the report: on the
// first send a begin_session with metrics, otherwise session_duration;
// attaches pending events and POSTs. Rearms the periodic callout.
func (r *Reporter) trySend() {
	// C: usage.c:113-115 — the C map also carries device_id + the Countly
	// app_key; the Matomo transport pulls the visitor id from
	// cfg.DeviceID (analyticsCID) and needs no app key.
	m := htsmsg.NewMap()

	if !r.startSent {
		m.AddU32("begin_session", 1)
		metrics := htsmsg.NewMap()

		// C: htsmsg_add_str(metrics, "_os", arch_get_system_type())
		metrics.AddStr("_os", arch.GetSystemType())
		if r.cfg.OSInfo != "" {
			metrics.AddStr("_os_version", r.cfg.OSInfo)
		}
		if r.cfg.DeviceType != "" {
			metrics.AddStr("_device", r.cfg.DeviceType)
		}
		// C: track = upgrade_get_track(); "_carrier" = track or "none"
		var track string
		if r.cfg.Track != nil {
			track = r.cfg.Track()
		}
		if track != "" {
			metrics.AddStr("_carrier", track)
		} else {
			metrics.AddStr("_carrier", "none")
		}
		metrics.AddStr("_app_version", version.AppVersion())
		var lang string
		if r.cfg.Lang != nil {
			lang = r.cfg.Lang()
		}
		metrics.AddStr("_locale", lang)
		m.AddMsg("metrics", metrics)
		r.sessionStartTime = arch.GetAvtime()
	} else {
		duration := (arch.GetAvtime() - r.sessionStartTime) / 1000000
		m.AddU32("session_duration", uint32(duration))
	}

	r.addEvents(m)

	err := r.sendmsg(m)
	m.Release()
	cs := r.cs
	if err == 0 {
		r.startSent = true
		if cs != nil {
			cs.Arm(&r.usageCallout, r.usagePeriodic, nil, 60*5)
		}
		r.sendFails = 0
	} else {
		r.sendFails++
		if r.sendFails == 10 {
			return // Give up
		}
		if cs != nil {
			cs.Arm(&r.usageCallout, r.usagePeriodic, nil, 10+r.sendFails*2)
		}
	}
}

// Event — C: usage_event (usage.c:183-268). Merges the event into
// the pending eventmap (keyed by key+segmentation) or appends a new
// entry; increments "count".
func (r *Reporter) Event(key string, count int, segmentation ...string) {
	if r == nil {
		return
	}
	if r.cfg.ShowEvents != 0 {
		fmt.Printf("event: %s %d ", key, count)
		for s := 0; s+1 < len(segmentation); s += 2 {
			fmt.Printf("%s=%s  ", segmentation[s], segmentation[s+1])
		}
		fmt.Printf("\n")
	}

	if r.cfg.Disabled != 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.eventmap == nil {
		r.eventmap = htsmsg.NewList()
	}

	// C: HTSMSG_FOREACH(f, eventmap) — find a matching key+segmentation
	for _, f := range r.eventmap.GetFields() {
		pm := f.GetChilds()
		if pm == nil {
			continue
		}
		k := pm.GetStr("key")
		if k != key {
			continue
		}
		segs := pm.GetMap("segmentation")

		if len(segmentation) > 0 {
			if segs == nil {
				continue
			}
			match := true
			for s := 0; s+1 < len(segmentation); s += 2 {
				v := segs.GetStr(segmentation[s])
				if v != segmentation[s+1] {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		} else if segs != nil {
			continue
		}

		// C: htsmsg_s32_inc(pm, "count", count)
		pm.S32Inc("count", int32(count))
		return
	}

	// C: new entry {key, count, [segmentation:{pairs}]}
	pm := htsmsg.NewMap()
	pm.AddStr("key", key)
	pm.S32Inc("count", int32(count))
	if len(segmentation) > 0 {
		segs := htsmsg.NewMap()
		for s := 0; s+1 < len(segmentation); s += 2 {
			segs.AddStr(segmentation[s], segmentation[s+1])
		}
		pm.AddMsg("segmentation", segs)
	}
	r.eventmap.AddMsg("", pm)
}

// PageOpen — C: usage_page_open (usage.c:271-276).
//
//	usage_event(sync ? "Open model" : "Open page", 1,
//	            USAGE_SEG("responder", responder))
func (r *Reporter) PageOpen(sync bool, responder string) {
	if sync {
		r.Event("Open model", 1, "responder", responder)
	} else {
		r.Event("Open page", 1, "responder", responder)
	}
}
