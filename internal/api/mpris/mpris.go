// Package mpris implements the MPRIS2 D-Bus media player interface —
// a documented extension (upstream Movian has no D-Bus/MPRIS support).
//
// Exposes org.mpris.MediaPlayer2.movian on the session bus so the
// desktop environment routes global media keys to Movian and shows it
// in its player widget. Player methods dispatch the same ACTION_*
// events a physical media key produces (em.Dispatch →
// event_dispatch → media.eventSink, C: event.c:610); PlaybackStatus,
// Metadata, Position and CanSeek are read live from the media prop
// tree (media.current.*) like any other prop consumer.
package mpris

import (
	"math"
	"sync"

	eventpkg "github.com/czz/movian-go/internal/event"
	facore "github.com/czz/movian-go/internal/fileaccess"
	propcore "github.com/czz/movian-go/internal/prop"
	tracepkg "github.com/czz/movian-go/internal/trace"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	busName = "org.mpris.MediaPlayer2.moviango"
	objPath = "/org/mpris/MediaPlayer2"

	ifaceRoot   = "org.mpris.MediaPlayer2"
	ifacePlayer = "org.mpris.MediaPlayer2.Player"
	ifaceProps  = "org.freedesktop.DBus.Properties"
)

// Server is the MPRIS2 endpoint. Created by Start; nil methods are
// tolerated so callers need not check for a session bus.
type Server struct {
	conn *dbus.Conn
	em   *eventpkg.EventManager
	pm   *propcore.PropManager

	mu        sync.Mutex
	status    string // cached PlaybackStatus for change detection
	trackID   dbus.ObjectPath
	trackSeq  uint64
	pStatus   *propcore.Prop // media.current.playstatus
	pCurrent  *propcore.Prop // media.current.currenttime
	pSeek     *propcore.Prop // media.current.seektime
	pDuration *propcore.Prop // media.current.metadata.duration
	pTitle    *propcore.Prop // media.current.metadata.title
	pArtist   *propcore.Prop // media.current.metadata.artist
	pAlbum    *propcore.Prop // media.current.metadata.album
	pVolume   *propcore.Prop // global.audio.mastervolume (dB)
}

// Start connects to the session bus and exports the MPRIS object.
// Failure is non-fatal (headless/dbus-less sessions) — it logs and
// returns nil.
func NewServer(em *eventpkg.EventManager, pm *propcore.PropManager, ts *tracepkg.TraceSystem) *Server {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		ts.Trace(tracepkg.TRACE_INFO, "MPRIS",
			"No session bus, MPRIS disabled: %v", err)
		return nil
	}
	return newServer(conn, em, pm, ts)
}

// newServer claims the bus name and exports the MPRIS object on an
// already-connected bus (split out for tests using a private bus).
func newServer(conn *dbus.Conn, em *eventpkg.EventManager,
	pm *propcore.PropManager, ts *tracepkg.TraceSystem) *Server {
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		ts.Trace(tracepkg.TRACE_INFO, "MPRIS",
			"Cannot own %s (reply=%v err=%v)", busName, reply, err)
		conn.Close()
		return nil
	}

	s := &Server{conn: conn, em: em, pm: pm, status: "Stopped",
		trackID: "/org/mpris/MediaPlayer2/Track/NoTrack"}

	conn.Export(&mediaPlayer2{s}, dbus.ObjectPath(objPath), ifaceRoot)
	conn.ExportWithMap(&player{s}, map[string]string{
		// vet flags a method literally named Seek (io.Seeker
		// signature convention) — export under the MPRIS name.
		"SeekOffset": "Seek",
	}, dbus.ObjectPath(objPath), ifacePlayer)
	conn.Export(&props{s}, dbus.ObjectPath(objPath), ifaceProps)
	conn.Export(introspect.NewIntrospectable(introspectData()),
		dbus.ObjectPath(objPath), "org.freedesktop.DBus.Introspectable")

	s.watchProps()
	ts.Trace(tracepkg.TRACE_INFO, "MPRIS", "Listening as %s", busName)
	return s
}

// Close releases the bus name and disconnects.
func (s *Server) Close() {
	if s == nil || s.conn == nil {
		return
	}
	s.conn.ReleaseName(busName)
	s.conn.Close()
	s.conn = nil
}

// dispatch injects an action event — identical path to a media key.
func (s *Server) dispatch(a eventpkg.ActionType) {
	eav := s.em.CreateAction(a)
	s.em.Dispatch(&eav.Event)
}

// ---- org.mpris.MediaPlayer2 ----

type mediaPlayer2 struct{ s *Server }

// Raise — no-op (no reliable raise on Wayland; CanRaise=false).
func (m *mediaPlayer2) Raise() *dbus.Error { return nil }

// Quit — C: ACTION_QUIT reaches the global eventSink via
// event_dispatch.
func (m *mediaPlayer2) Quit() *dbus.Error {
	m.s.dispatch(eventpkg.ACTION_QUIT)
	return nil
}

// ---- org.mpris.MediaPlayer2.Player ----

type player struct{ s *Server }

func (p *player) Next() *dbus.Error {
	p.s.dispatch(eventpkg.ACTION_SKIP_FORWARD)
	return nil
}
func (p *player) Previous() *dbus.Error {
	p.s.dispatch(eventpkg.ACTION_SKIP_BACKWARD)
	return nil
}
func (p *player) Pause() *dbus.Error {
	p.s.dispatch(eventpkg.ACTION_PAUSE)
	return nil
}
func (p *player) PlayPause() *dbus.Error {
	p.s.dispatch(eventpkg.ACTION_PLAYPAUSE)
	return nil
}
func (p *player) Stop() *dbus.Error {
	p.s.dispatch(eventpkg.ACTION_STOP)
	return nil
}
func (p *player) Play() *dbus.Error {
	p.s.dispatch(eventpkg.ACTION_PLAY)
	return nil
}

// SeekOffset is exported on the bus as "Seek" (see ExportWithMap).
// Offset in microseconds, resolved through the canonical seektime
// prop (mp_seek_by_propchange) as an absolute target.
func (p *player) SeekOffset(off int64) *dbus.Error {
	target := float64(p.s.currentSec()) + float64(off)/1e6
	if p.s.setSeekTime(target) {
		p.s.conn.Emit(dbus.ObjectPath(objPath),
			ifacePlayer+".Seeked", target*1e6)
	}
	return nil
}

func (p *player) SetPosition(track dbus.ObjectPath, pos int64) *dbus.Error {
	if p.s.setSeekTime(float64(pos) / 1e6) {
		p.s.conn.Emit(dbus.ObjectPath(objPath),
			ifacePlayer+".Seeked", pos)
	}
	return nil
}

func (p *player) OpenUri(uri string) *dbus.Error {
	e := p.s.em.CreateOpenURLArgs(&eventpkg.EventOpenURLArgs{URL: uri})
	p.s.em.Dispatch(&e.Event)
	return nil
}

// ---- prop tree accessors ----

// leaf fetches a watched prop pointer under s.mu — the watchers
// publish leaves from prop-core notify goroutines, so every read
// must be synchronized.
func (s *Server) leaf(dst **propcore.Prop) *propcore.Prop {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *dst
}

// propStr reads a string leaf, following originators via GetString.
func (s *Server) propStr(dst **propcore.Prop, def string) string {
	if p := s.leaf(dst); p != nil {
		return s.pm.GetString(p, def)
	}
	return def
}

func (s *Server) propInt(dst **propcore.Prop, def int) int {
	if p := s.leaf(dst); p != nil {
		return s.pm.GetInt(p, def)
	}
	return def
}

// currentSec — Position in seconds (media.current.currenttime).
func (s *Server) currentSec() int {
	return s.propInt(&s.pCurrent, 0)
}

// setSeekTime — C: prop_set(mp_prop_root, "seektime", FLOAT) →
// mp_seek_by_propchange. Returns false when no seek prop exists.
func (s *Server) setSeekTime(sec float64) bool {
	if p := s.leaf(&s.pSeek); p != nil && sec >= 0 {
		s.pm.SetFloatEx(p, nil, float32(sec))
		return true
	}
	return false
}

// volumeLinear — MPRIS Volume (linear ≥0) from mastervolume dB.
// C: audio.c master_volume = pow(10, dB/20), range [-75,12].
func (s *Server) volumeLinear() float64 {
	if p := s.leaf(&s.pVolume); p != nil {
		return math.Pow(10, float64(s.pm.GetFloat(p, 0))/20)
	}
	return 1.0
}

// durationSec — media.current.metadata.duration (int seconds).
func (s *Server) durationSec() int {
	return s.propInt(&s.pDuration, 0)
}

func (s *Server) playbackStatus() string {
	switch s.propStr(&s.pStatus, "stop") {
	case "play":
		return "Playing"
	case "pause":
		return "Paused"
	default:
		return "Stopped"
	}
}

// metadata builds the MPRIS Metadata dict from the media prop tree.
func (s *Server) metadata() map[string]dbus.Variant {
	s.mu.Lock()
	trackID := s.trackID
	s.mu.Unlock()
	m := map[string]dbus.Variant{
		"mpris:trackid": dbus.MakeVariant(trackID),
	}
	if t := s.propStr(&s.pTitle, ""); t != "" {
		m["xesam:title"] = dbus.MakeVariant(t)
	}
	if a := s.propStr(&s.pArtist, ""); a != "" {
		m["xesam:artist"] = dbus.MakeVariant([]string{a})
	}
	if a := s.propStr(&s.pAlbum, ""); a != "" {
		m["xesam:album"] = dbus.MakeVariant(a)
	}
	if d := s.durationSec(); d > 0 {
		m["mpris:length"] = dbus.MakeVariant(int64(d) * 1e6)
	}
	return m
}

// ---- prop subscriptions → PropertiesChanged ----

// watchProps subscribes to the media prop leaves (created lazily by
// the playback pipeline, so WatchChildPath resolves them whenever
// they appear — the same pattern as clipboard watchers).
func (s *Server) watchProps() {
	global := s.pm.GetGlobal()
	if global == nil {
		return
	}

	facore.WatchChildPath(global, []string{"media", "current", "playstatus"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			s.mu.Lock()
			s.pStatus = leaf
			s.mu.Unlock()
			return leaf.Subscribe(s.onPlaystatus, nil)
		})

	facore.WatchChildPath(global, []string{"media", "current", "currenttime"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			s.mu.Lock()
			s.pCurrent = leaf
			s.mu.Unlock()
			return nil
		})
	facore.WatchChildPath(global, []string{"media", "current", "seektime"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			s.mu.Lock()
			s.pSeek = leaf
			s.mu.Unlock()
			return nil
		})

	for _, m := range []struct {
		name string
		dst  **propcore.Prop
	}{
		{"duration", &s.pDuration},
		{"title", &s.pTitle},
		{"artist", &s.pArtist},
		{"album", &s.pAlbum},
	} {
		facore.WatchChildPath(global,
			[]string{"media", "current", "metadata", m.name},
			func(leaf *propcore.Prop) *propcore.Subscription {
				s.mu.Lock()
				*m.dst = leaf
				s.mu.Unlock()
				return leaf.Subscribe(s.onMetadata, nil)
			})
	}

	// global.audio.mastervolume → Volume (created by audioMastervolSetup).
	facore.WatchChildPath(global, []string{"audio", "mastervolume"},
		func(leaf *propcore.Prop) *propcore.Subscription {
			s.mu.Lock()
			s.pVolume = leaf
			s.mu.Unlock()
			return leaf.Subscribe(s.onVolume, nil)
		})
}

func (s *Server) onPlaystatus(_ any, _ propcore.EventType, _ ...any) {
	now := s.playbackStatus()
	s.mu.Lock()
	changed := now != s.status
	s.status = now
	s.mu.Unlock()
	if changed {
		s.emit(ifacePlayer, map[string]dbus.Variant{
			"PlaybackStatus": dbus.MakeVariant(now),
			"Metadata":       dbus.MakeVariant(s.metadata()),
		})
	}
}

func (s *Server) onMetadata(_ any, _ propcore.EventType, _ ...any) {
	s.mu.Lock()
	s.trackSeq++
	s.trackID = dbus.ObjectPath(
		"/org/mpris/MediaPlayer2/Track/" + itoa(s.trackSeq))
	s.mu.Unlock()
	s.emit(ifacePlayer, map[string]dbus.Variant{
		"Metadata": dbus.MakeVariant(s.metadata()),
	})
}

func (s *Server) onVolume(_ any, _ propcore.EventType, _ ...any) {
	s.emit(ifacePlayer, map[string]dbus.Variant{
		"Volume": dbus.MakeVariant(s.volumeLinear()),
	})
}

// emit fires org.freedesktop.DBus.Properties.PropertiesChanged.
func (s *Server) emit(iface string, changed map[string]dbus.Variant) {
	if s.conn == nil {
		return
	}
	s.conn.Emit(dbus.ObjectPath(objPath),
		ifaceProps+".PropertiesChanged",
		iface, changed, []string{})
}

// ---- org.freedesktop.DBus.Properties ----

// props implements Get/GetAll/Set directly so values are always read
// live from the prop tree (Position/currenttime especially).
type props struct{ s *Server }

func (p *props) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	all, err := p.GetAll(iface)
	if err != nil {
		return dbus.Variant{}, err
	}
	if v, ok := all[name]; ok {
		return v, nil
	}
	return dbus.Variant{}, dbus.NewError(
		"org.freedesktop.DBus.Error.InvalidArgs",
		[]any{"unknown property " + name})
}

func (p *props) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	s := p.s
	switch iface {
	case ifaceRoot:
		return map[string]dbus.Variant{
			"CanQuit":          dbus.MakeVariant(true),
			"CanRaise":         dbus.MakeVariant(false),
			"Fullscreen":       dbus.MakeVariant(false),
			"CanSetFullscreen": dbus.MakeVariant(false),
			"HasTrackList":     dbus.MakeVariant(false),
			"Identity":         dbus.MakeVariant("Movian Go"),
			"DesktopEntry":     dbus.MakeVariant("movian-go"),
			"SupportedUriSchemes": dbus.MakeVariant([]string{
				"file", "http", "https", "ftp", "smb", "nfs",
				"rtsp", "rtmp", "dvd", "bluray",
			}),
			"SupportedMimeTypes": dbus.MakeVariant([]string{
				"video/mp4", "video/x-matroska", "video/mpeg",
				"audio/mpeg", "audio/flac", "audio/ogg",
			}),
		}, nil
	case ifacePlayer:
		dur := s.durationSec()
		return map[string]dbus.Variant{
			"PlaybackStatus": dbus.MakeVariant(s.playbackStatus()),
			"LoopStatus":     dbus.MakeVariant("None"),
			"Rate":           dbus.MakeVariant(1.0),
			"Shuffle":        dbus.MakeVariant(false),
			"Metadata":       dbus.MakeVariant(s.metadata()),
			"Volume":         dbus.MakeVariant(s.volumeLinear()),
			"Position":       dbus.MakeVariant(int64(s.currentSec()) * 1e6),
			"MinimumRate":    dbus.MakeVariant(1.0),
			"MaximumRate":    dbus.MakeVariant(1.0),
			"CanGoNext":      dbus.MakeVariant(true),
			"CanGoPrevious":  dbus.MakeVariant(true),
			"CanPlay":        dbus.MakeVariant(true),
			"CanPause":       dbus.MakeVariant(true),
			"CanSeek":        dbus.MakeVariant(dur > 0),
			"CanControl":     dbus.MakeVariant(true),
		}, nil
	}
	return nil, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs",
		[]any{"unknown interface " + iface})
}

// Set — Volume writes through to global.audio.mastervolume (dB,
// clipping enforced by the prop's own range). Movian has no
// loop/shuffle/rate, so those writes are accepted and ignored (a
// no-op set is legal per the spec).
func (p *props) Set(iface, name string, v dbus.Variant) *dbus.Error {
	switch iface {
	case ifacePlayer:
		switch name {
		case "Volume":
			if val, ok := v.Value().(float64); ok {
				s := p.s
				if pv := s.leaf(&s.pVolume); pv != nil {
					db := -75.0
					if val > 0 {
						db = 20 * math.Log10(val)
					}
					pv.SetFloat(float32(db))
				}
			}
			return nil
		case "LoopStatus", "Rate", "Shuffle":
			return nil
		}
	case ifaceRoot:
		if name == "Fullscreen" {
			return nil
		}
	}
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly",
		[]any{name})
}

// itoa — avoids importing strconv for the track counter.
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// introspectData — org.freedesktop.DBus.Introspectable payload for
// both MPRIS interfaces + Properties.
func introspectData() *introspect.Node {
	mkprop := func(name, typ, access string) introspect.Property {
		return introspect.Property{Name: name, Type: typ, Access: access}
	}
	mkmeth := func(name string, args ...introspect.Arg) introspect.Method {
		return introspect.Method{Name: name, Args: args}
	}
	ain := func(name, typ string) introspect.Arg {
		return introspect.Arg{Name: name, Type: typ, Direction: "in"}
	}
	return &introspect.Node{
		Name: objPath,
		Interfaces: []introspect.Interface{
			{
				Name: ifaceRoot,
				Methods: []introspect.Method{
					mkmeth("Raise"), mkmeth("Quit"),
				},
				Properties: []introspect.Property{
					mkprop("CanQuit", "b", "read"),
					mkprop("CanRaise", "b", "read"),
					mkprop("HasTrackList", "b", "read"),
					mkprop("Identity", "s", "read"),
					mkprop("DesktopEntry", "s", "read"),
					mkprop("SupportedUriSchemes", "as", "read"),
					mkprop("SupportedMimeTypes", "as", "read"),
				},
			},
			{
				Name: ifacePlayer,
				Methods: []introspect.Method{
					mkmeth("Next"), mkmeth("Previous"),
					mkmeth("Pause"), mkmeth("PlayPause"),
					mkmeth("Stop"), mkmeth("Play"),
					mkmeth("Seek", ain("Offset", "x")),
					mkmeth("SetPosition", ain("TrackId", "o"),
						ain("Position", "x")),
					mkmeth("OpenUri", ain("Uri", "s")),
				},
				Properties: []introspect.Property{
					mkprop("PlaybackStatus", "s", "read"),
					mkprop("LoopStatus", "s", "readwrite"),
					mkprop("Rate", "d", "readwrite"),
					mkprop("Shuffle", "b", "readwrite"),
					mkprop("Metadata", "a{sv}", "read"),
					mkprop("Volume", "d", "readwrite"),
					mkprop("Position", "x", "read"),
					mkprop("MinimumRate", "d", "read"),
					mkprop("MaximumRate", "d", "read"),
					mkprop("CanGoNext", "b", "read"),
					mkprop("CanGoPrevious", "b", "read"),
					mkprop("CanPlay", "b", "read"),
					mkprop("CanPause", "b", "read"),
					mkprop("CanSeek", "b", "read"),
					mkprop("CanControl", "b", "read"),
				},
			},
		},
	}
}
