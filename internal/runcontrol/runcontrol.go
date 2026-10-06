// Package runcontrol is the canonical port of src/runcontrol.c — the
// $global.runcontrol prop tree, auto-standby/sleep-timer timers, and
// the global.eventSink action dispatch that drives app_shutdown.
package runcontrol

import (
	"sync"
	"time"

	"github.com/czz/movian-go/internal/callout"
	"github.com/czz/movian-go/internal/event"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// C: src/main.h exit codes
const (
	AppExitStandby  = 10 // APP_EXIT_STANDBY
	AppExitPoweroff = 11 // APP_EXIT_POWEROFF
	AppExitLogout   = 12 // APP_EXIT_LOGOUT
	AppExitRestart  = 13 // APP_EXIT_RESTART
	AppExitShell    = 14 // APP_EXIT_SHELL
	AppExitReboot   = 15 // APP_EXIT_REBOOT
)

// RunControl holds the static state of runcontrol.c.
type RunControl struct {
	mutex sync.Mutex

	pm          *propcore.PropManager
	settingsMgr *settingscore.SettingsManager
	callouts    *callout.CalloutSystem
	shutdownCb  func(int) // C: app_shutdown()
	shellFD     int       // C: gconf.shell_fd

	lastActivity      int64                  // C: static int64_t last_activity (µs)
	standbyDelay      int                    // C: static int standby_delay
	activeMedia       bool                   // C: static int active_media
	sleeptime         int                    // C: static int sleeptime
	sleeptimerEnabled bool                   // C: static int sleeptimer_enabled
	sleeptimeProp     *propcore.Prop         // C: static prop_t *sleeptime_prop
	sleeptimeSub      *propcore.Subscription // C: static prop_sub_t *sleeptime_sub

	autostandbyTimer callout.Callout // C: static callout_t autostandby_timer
	sleepTimer       callout.Callout // C: static callout_t sleep_timer

	pathSubs []*propcore.PathSub
	rc       *propcore.Prop // C: rc = prop_create(prop_get_global(), "runcontrol")

	canStandby   bool
	canPowerOff  bool
	canLogout    bool
	canOpenShell bool
	canRestart   bool
	canExit      bool
}

// NewRunControl — C: runcontrol_init (runcontrol.c:251-327). Creates
// $global.runcontrol, subscribes global.eventSink, exports can*
// capabilities, and installs settings actions + autostandby/sleeptimer.
func NewRunControl(pm *propcore.PropManager, sm *settingscore.SettingsManager,
	cs *callout.CalloutSystem,
	canStandby, canPowerOff, canLogout, canOpenShell, canRestart, canExit bool,
	shellFD int, shutdownCallback func(int)) *RunControl {

	rc := &RunControl{
		pm:           pm,
		settingsMgr:  sm,
		callouts:     cs,
		shutdownCb:   shutdownCallback,
		shellFD:      shellFD,
		canStandby:   canStandby,
		canPowerOff:  canPowerOff,
		canLogout:    canLogout,
		canOpenShell: canOpenShell,
		canRestart:   canRestart,
		canExit:      canExit,
		sleeptime:    60,
		lastActivity: time.Now().UnixMicro(),
	}

	// C: rc = prop_create(prop_get_global(), "runcontrol");
	global := pm.GetGlobal()
	rc.rc = pm.CreateEx(global, "runcontrol", nil, false, false)

	// C: prop_subscribe(0, PROP_TAG_NAME("global", "eventSink"),
	//   PROP_TAG_CALLBACK_EVENT, runcontrol_global_eventsink, NULL, NULL);
	rc.pathSubs = append(rc.pathSubs, propcore.SubscribePath(global,
		[]string{"eventSink"}, false,
		func(ev propcore.EventType, args ...any) {
			rc.globalEventsink(ev, args...)
		}))

	// C: prop_set(rc, "canStandby", PROP_SET_INT, !!gconf.can_standby); etc.
	setInt := func(name string, v bool) {
		if p := pm.CreateEx(rc.rc, name, nil, false, false); p != nil {
			b := 0
			if v {
				b = 1
			}
			pm.SetIntEx(p, nil, b)
		}
	}
	setInt("canStandby", canStandby)
	setInt("canPowerOff", canPowerOff)
	setInt("canLogout", canLogout)
	setInt("canOpenShell", canOpenShell)
	setInt("canRestart", canRestart)
	setInt("canExit", canExit)

	// C: if(!(gconf.can_standby || ... || !gconf.can_not_exit)) return;
	if !(canStandby || canPowerOff || canLogout || canOpenShell || canRestart || canExit) {
		return rc
	}

	if sm == nil {
		return rc
	}

	// C: prop_t *dir = setting_get_dir("general:runcontrol");
	dir := sm.SettingGetDir("general:runcontrol")

	if canStandby {
		// C: init_autostandby(); init_sleeptimer(rc);
		rc.setupAutostandby()
		rc.setupSleeptimer()
		// C: settings_create_action(dir, _p("Standby"), NULL, do_standby, NULL, 0, NULL);
		sm.CreateActionProp(dir, "Standby", "",
			func(opaque, value any) { rc.doStandby() }, nil, 0)
	}

	if canPowerOff {
		// C: settings_create_action(dir, _p("Power off system"), NULL, do_power_off, NULL, 0, NULL);
		sm.CreateActionProp(dir, "Power off system", "",
			func(opaque, value any) { rc.doPowerOff() }, nil, 0)
	}

	if canLogout {
		// C: settings_create_action(dir, _p("Logout"), NULL, do_logout, NULL, 0, NULL);
		sm.CreateActionProp(dir, "Logout", "",
			func(opaque, value any) { rc.doLogout() }, nil, 0)
	}

	if canOpenShell {
		// C: settings_create_action(dir, _p("Open shell"), NULL, do_open_shell, NULL, 0, NULL);
		sm.CreateActionProp(dir, "Open shell", "",
			func(opaque, value any) { rc.doOpenShell() }, nil, 0)
	}

	if canExit {
		// C: settings_create_action(dir, _p("Quit"), NULL, do_exit, NULL, 0, NULL);
		sm.CreateActionProp(dir, "Quit", "",
			func(opaque, value any) { rc.doExit() }, nil, 0)
	}

	// C: if(gconf.shell_fd > 0) { settings_create_separator(gconf.settings_network, _p("SSH server"));
	//   setting_create(SETTING_BOOL, gconf.settings_network, SETTINGS_INITIAL_UPDATE,
	//     SETTING_TITLE(_p("Enable SSH server")), SETTING_VALUE(0),
	//     SETTING_CALLBACK(set_ssh_server, NULL), SETTING_STORE("runcontrol", "sshserver"), NULL); }
	if shellFD > 0 {
		network := sm.Network()
		if network != nil {
			sm.CreateSeparatorProp(network, sm.P("SSH server"))
			sm.SettingCreate(settingscore.SettingBool, network,
				settingscore.SettingsInitialUpdate,
				settingscore.SettingTagTitle, sm.P("Enable SSH server"),
				settingscore.SettingTagValue, 0,
				settingscore.SettingTagCallback, func(opaque, value any) {
					if v, ok := value.(int); ok {
						rc.setSSHServer(v != 0)
					}
				}, nil,
				settingscore.SettingTagStore, "runcontrol", "sshserver",
				nil)
		}
	}

	return rc
}

// Activity — C: runcontrol_activity (runcontrol.c:49-53). Called from
// various places to indicate the user is active.
func (rc *RunControl) Activity() {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()
	if rc.standbyDelay != 0 {
		rc.lastActivity = time.Now().UnixMicro()
	}
}

// checkAutostandby — C: check_autostandby (runcontrol.c:60-77).
// Periodically checks if we should auto standby.
func (rc *RunControl) checkAutostandby(c *callout.Callout, opaque any) {
	rc.mutex.Lock()
	idle := time.Now().UnixMicro() - rc.lastActivity
	idle /= 1000000 * 60 // convert to minutes
	if rc.standbyDelay != 0 && idle >= int64(rc.standbyDelay) && !rc.activeMedia {
		rc.mutex.Unlock()
		rc.shutdownCb(AppExitStandby)
		return
	}
	rc.mutex.Unlock()
	rc.callouts.Arm(&rc.autostandbyTimer, rc.checkAutostandby, nil, 1)
}

// currentMediaPlaystatus — C: current_media_playstatus (runcontrol.c:83-91).
func (rc *RunControl) currentMediaPlaystatus(str string) {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()
	// Reset time to avoid risk of turning off as soon as track playback
	// has ended if UI has been idle
	rc.lastActivity = time.Now().UnixMicro()
	rc.activeMedia = str != "" // If str is something then we're playing, paused, etc
}

// setupAutostandby — C: init_autostandby (runcontrol.c:97-121).
func (rc *RunControl) setupAutostandby() {
	sm := rc.settingsMgr
	dir := sm.SettingGetDir("general:runcontrol")

	// C: setting_create(SETTING_INT, dir, SETTINGS_INITIAL_UPDATE,
	//   SETTING_TITLE(_p("Automatic standby")), SETTING_STORE("runcontrol", "autostandby"),
	//   SETTING_WRITE_INT(&standby_delay), SETTING_RANGE(0, 60), SETTING_STEP(5),
	//   SETTING_UNIT_CSTR("min"), SETTING_ZERO_TEXT(_p("Off")), NULL);
	sm.SettingCreate(settingscore.SettingInt, dir,
		settingscore.SettingsInitialUpdate,
		settingscore.SettingTagTitle, sm.P("Automatic standby"),
		settingscore.SettingTagStore, "runcontrol", "autostandby",
		settingscore.SettingTagWriteInt, &rc.standbyDelay,
		settingscore.SettingTagRange, 0, 60,
		settingscore.SettingTagStep, 5,
		settingscore.SettingTagUnitCStr, "min",
		settingscore.SettingTagZeroText, sm.P("Off"),
		nil)

	rc.lastActivity = time.Now().UnixMicro()

	// C: prop_subscribe(0, PROP_TAG_NAME("global", "media", "current", "playstatus"),
	//   PROP_TAG_CALLBACK_STRING, current_media_playstatus, NULL, NULL);
	global := rc.pm.GetGlobal()
	rc.pathSubs = append(rc.pathSubs, propcore.SubscribePath(global,
		[]string{"media", "current", "playstatus"}, false,
		func(ev propcore.EventType, args ...any) {
			switch ev {
			case propcore.EventSetRString, propcore.EventSetCString:
				if len(args) > 0 {
					if s, ok := args[0].(string); ok {
						rc.currentMediaPlaystatus(s)
					}
				}
			case propcore.EventSetVoid:
				rc.currentMediaPlaystatus("")
			}
		}))

	// C: callout_arm(&autostandby_timer, check_autostandby, NULL, 1);
	rc.callouts.Arm(&rc.autostandbyTimer, rc.checkAutostandby, nil, 1)
}

// updateSleeptime — C: update_sleeptime (runcontrol.c:123-128).
func (rc *RunControl) updateSleeptime(v int) {
	rc.mutex.Lock()
	rc.sleeptime = v
	rc.mutex.Unlock()
}

// decreaseSleeptimer — C: decrease_sleeptimer (runcontrol.c:130-146).
func (rc *RunControl) decreaseSleeptimer(c *callout.Callout, opaque any) {
	rc.mutex.Lock()
	if !rc.sleeptimerEnabled {
		rc.mutex.Unlock()
		return
	}
	rc.sleeptime--
	if rc.sleeptime < 0 {
		rc.mutex.Unlock()
		rc.shutdownCb(AppExitStandby)
		return
	}
	prop, sub := rc.sleeptimeProp, rc.sleeptimeSub
	rc.mutex.Unlock()
	// C: prop_set_int_ex(sleeptime_prop, sleeptime_sub, sleeptime);
	rc.pm.SetIntEx(prop, sub, rc.sleeptime)
	rc.callouts.Arm(&rc.sleepTimer, rc.decreaseSleeptimer, nil, 60)
}

// updateSleeptimer — C: update_sleeptimer (runcontrol.c:152-164).
func (rc *RunControl) updateSleeptimer(v int) {
	rc.mutex.Lock()
	rc.sleeptimerEnabled = v != 0
	prop := rc.sleeptimeProp
	rc.mutex.Unlock()
	if v != 0 {
		// C: prop_set_int(sleeptime_prop, 60);
		rc.pm.SetIntEx(prop, nil, 60)
		rc.callouts.Arm(&rc.sleepTimer, rc.decreaseSleeptimer, nil, 60)
	} else {
		rc.callouts.Disarm(&rc.sleepTimer)
	}
}

// setupSleeptimer — C: init_sleeptimer (runcontrol.c:170-191).
func (rc *RunControl) setupSleeptimer() {
	const maxtime = 180
	pm := rc.pm
	rc.sleeptimeProp = pm.CreateEx(rc.rc, "sleepTime", nil, false, false)
	pm.SetIntEx(rc.sleeptimeProp, nil, 60)
	rc.sleeptimeProp.SetClippedInt(0, maxtime)

	if p := pm.CreateEx(rc.rc, "sleepTimeMax", nil, false, false); p != nil {
		pm.SetIntEx(p, nil, maxtime)
	}
	if p := pm.CreateEx(rc.rc, "sleepTimeStep", nil, false, false); p != nil {
		pm.SetIntEx(p, nil, 5)
	}

	// C: sleeptime_sub = prop_subscribe(0, PROP_TAG_CALLBACK_INT,
	//   update_sleeptime, NULL, PROP_TAG_ROOT, sleeptime_prop, NULL);
	// C delivers through trampoline_int: SET_FLOAT/SET_STRING also convert.
	rc.sleeptimeSub = rc.sleeptimeProp.Subscribe(
		func(_ any, ev propcore.EventType, args ...any) {
			if v, deliver := propcore.TrampolineInt(ev, args, false); deliver {
				rc.updateSleeptime(v)
			}
		}, nil)

	// C: prop_subscribe(PROP_SUB_NO_INITIAL_UPDATE,
	//   PROP_TAG_NAME("global", "runcontrol", "sleepTimer"),
	//   PROP_TAG_CALLBACK_INT, update_sleeptimer, NULL, NULL);
	global := pm.GetGlobal()
	rc.pathSubs = append(rc.pathSubs, propcore.SubscribePath(global,
		[]string{"runcontrol", "sleepTimer"}, true,
		func(ev propcore.EventType, args ...any) {
			if v, deliver := propcore.TrampolineInt(ev, args, false); deliver {
				rc.updateSleeptimer(v)
			}
		}))
}

// doPowerOff — C: do_power_off (runcontrol.c:194-198).
func (rc *RunControl) doPowerOff() { rc.shutdownCb(AppExitPoweroff) }

// doLogout — C: do_logout (runcontrol.c:200-204).
func (rc *RunControl) doLogout() { rc.shutdownCb(AppExitLogout) }

// doOpenShell — C: do_open_shell (runcontrol.c:206-210).
func (rc *RunControl) doOpenShell() { rc.shutdownCb(AppExitShell) }

// doStandby — C: do_standby (runcontrol.c:212-216).
func (rc *RunControl) doStandby() { rc.shutdownCb(AppExitStandby) }

// doExit — C: do_exit (runcontrol.c:219-223).
func (rc *RunControl) doExit() { rc.shutdownCb(0) }

// setSSHServer — C: set_ssh_server (runcontrol.c:229-236).
func (rc *RunControl) setSSHServer(on bool) {
	cmd := byte(1)
	if !on {
		cmd = 2
	}
	if err := rcShellWrite(rc.shellFD, []byte{cmd}); err != nil {
		// C: TRACE(TRACE_ERROR, "SSHD", "Unable to send cmd -- %s", strerror(errno));
	}
}

// globalEventsink — C: runcontrol_global_eventsink (runcontrol.c:239-258).
func (rc *RunControl) globalEventsink(ev propcore.EventType, args ...any) {
	if ev != propcore.EventExtEvent || len(args) == 0 {
		return
	}
	e, ok := args[0].(*event.Event)
	if !ok || e == nil {
		return
	}
	switch {
	case e.IsAction(event.ACTION_QUIT):
		rc.shutdownCb(0)
	case e.IsAction(event.ACTION_STANDBY):
		rc.shutdownCb(AppExitStandby)
	case e.IsAction(event.ACTION_POWER_OFF):
		rc.shutdownCb(AppExitPoweroff)
	case e.IsAction(event.ACTION_RESTART):
		rc.shutdownCb(AppExitRestart)
	case e.IsAction(event.ACTION_REBOOT):
		rc.shutdownCb(AppExitReboot)
	}
}

// GetCapabilities returns the system capabilities (compat accessor —
// C exports them as the can* props on $global.runcontrol).
func (rc *RunControl) GetCapabilities() (canStandby, canPowerOff, canLogout, canOpenShell, canRestart, canExit bool) {
	rc.mutex.Lock()
	defer rc.mutex.Unlock()
	return rc.canStandby, rc.canPowerOff, rc.canLogout,
		rc.canOpenShell, rc.canRestart, rc.canExit
}

// Shutdown — compat wrapper; C callers invoke app_shutdown() directly.
func (rc *RunControl) Shutdown(exitCode int) {
	rc.shutdownCb(exitCode)
}
