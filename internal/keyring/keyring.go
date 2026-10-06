// Package keyring — C-canonical 1:1 port of src/keyring.c.
// Credential management with persistent/temporary stores and popup UI.
// C: src/keyring.c (243 lines).
package keyring

import (
	"sync"

	"github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/htsmsg"
	"github.com/czz/movian-go/internal/misc"
	"github.com/czz/movian-go/internal/notifications"
	propcore "github.com/czz/movian-go/internal/prop"
	settingscore "github.com/czz/movian-go/internal/settings"
)

// Flags for keyring_lookup, matching C: keyring.h.
//
//	#define KEYRING_QUERY_USER       0x1
//	#define KEYRING_SHOW_REMEMBER_ME 0x2
//	#define KEYRING_REMEMBER_ME_SET  0x4
//	#define KEYRING_ONE_SHOT         0x8
const (
	FlagQueryUser      = 0x1
	FlagShowRememberMe = 0x2
	FlagRememberMeSet  = 0x4
	FlagOneShot        = 0x8
)

// Return codes, matching C: keyring.h.
//
//	#define KEYRING_USER_REJECTED -1
//	#define KEYRING_OK             0
//	#define KEYRING_NOT_FOUND      1
const (
	UserRejected = -1
	OK           = 0
	NotFound     = 1
)

// Keyring holds the credential stores and dependencies.
// C: static htsmsg_t *persistent_keyring, *temporary_keyring; (keyring.c:33)
// C: static hts_mutex_t keyring_mutex; (keyring.c:34)
type Keyring struct {
	persistentKeyring *htsmsg.HTSMsg
	temporaryKeyring  *htsmsg.HTSMsg
	mutex             sync.Mutex
	propManager       *propcore.PropManager
	notifyMgr         *notifications.NotificationManager
	settingsMgr       *settingscore.SettingsManager
	store             *htsmsg.Store // C: global htsmsg_store — injected
}

// New matches C: keyring_init (keyring.c:56-68) — loads
// htsmsg_store("keyring") from the global store. The instance is
// returned so the app can inject it (C: file-scope statics).
func New(pm *propcore.PropManager,
	nm *notifications.NotificationManager, sm *settingscore.SettingsManager,
	store *htsmsg.Store) *Keyring {
	k := &Keyring{
		propManager: pm,
		notifyMgr:   nm,
		settingsMgr: sm,
		store:       store,
	}
	if store != nil {
		if msg, err := store.Load("keyring"); err == nil && msg != nil {
			k.persistentKeyring = msg
		}
	}
	if k.persistentKeyring == nil {
		k.persistentKeyring = htsmsg.NewMap()
	}
	k.temporaryKeyring = htsmsg.NewMap()

	// C: prop_t *dir = setting_get_dir("general:resets")
	// C: settings_create_action(dir, _p("Forget remembered passwords"),
	//   keyring_clear, NULL, 0, NULL)
	if sm != nil {
		dir := sm.SettingGetDir("general:resets")
		if dir != nil {
			sm.CreateActionProp(dir, "Forget remembered passwords", "",
				func(opaque any, value any) {
					k.clear()
				}, nil, 0)
		}
	}

	return k
}

// clear matches C: keyring_clear (keyring.c:36-50).
func (k *Keyring) clear() {
	k.mutex.Lock()
	k.persistentKeyring = htsmsg.NewMap()
	k.temporaryKeyring = htsmsg.NewMap()
	if k.store != nil {
		k.store.Save(k.persistentKeyring, "keyring")
	}
	k.mutex.Unlock()
	// C: notify_add(NULL, NOTIFY_WARNING, NULL, 3, _("Rembered passwords erased"))
	if k.notifyMgr != nil {
		k.notifyMgr.NotifyAdd(nil, notifications.NotifyWarning, "", 3,
			"Remembered passwords erased")
	}
}

// store_ matches C: keyring_store (keyring.c:74-78).
func (k *Keyring) store_() {
	if k.store != nil {
		k.store.Save(k.persistentKeyring, "keyring")
	}
}

// setstr matches C: setstr (keyring.c:84-96).
//
//	static void
//	setstr(char **p, htsmsg_t *m, const char *fname)
//	{
//	  const char *s;
//	  if(p == NULL) return;
//	  if((s = htsmsg_get_str(m, fname)) != NULL)
//	    *p = strdup(s);
//	  else
//	    *p = NULL;
//	}
func setstr(p *string, m *htsmsg.HTSMsg, fname string) {
	if p == nil {
		return
	}
	s := m.GetStr(fname)
	*p = s
}

// setRemember matches C: set_remember (keyring.c:101-106).
//
//	static void
//	set_remember(void *opaque, int v)
//	{
//	  int *rp = (int *)opaque;
//	  *rp = v;
//	}
func setRemember(rp *int, v int) {
	*rp = v
}

// Lookup matches C: keyring_lookup (keyring.c:112-243).
func (k *Keyring) Lookup(id string, username, password, domain *string,
	rememberMe *int, source, reason string, flags int) int {
	// C: int remember = !!(flags & KEYRING_REMEMBER_ME_SET)
	remember := 0
	if flags&FlagRememberMeSet != 0 {
		remember = 1
	}

	k.mutex.Lock()

	// C: if(flags & KEYRING_QUERY_USER) { ... popup UI ... }
	if flags&FlagQueryUser != 0 {
		// C: prop_t *p = prop_ref_inc(prop_create_root(NULL))
		p := k.propManager.CreateRoot("")
		k.propManager.RefInc(p)

		// C: prop_set_string(prop_create(p, "type"), "auth")
		typeProp := k.propManager.CreateEx(p, "type", nil, false, false)
		k.propManager.SetStringEx(typeProp, nil, "auth", propcore.StringUTF8)
		// C: prop_set_string(prop_create(p, "id"), id)
		idProp := k.propManager.CreateEx(p, "id", nil, false, false)
		k.propManager.SetStringEx(idProp, nil, id, propcore.StringUTF8)
		// C: prop_set_string(prop_create(p, "source"), source)
		srcProp := k.propManager.CreateEx(p, "source", nil, false, false)
		k.propManager.SetStringEx(srcProp, nil, source, propcore.StringUTF8)
		// C: prop_set_string(prop_create(p, "reason"), reason)
		reasonProp := k.propManager.CreateEx(p, "reason", nil, false, false)
		k.propManager.SetStringEx(reasonProp, nil, reason, propcore.StringUTF8)
		// C: prop_set_int(prop_create(p, "disableUsername"), username == NULL)
		disableUserProp := k.propManager.CreateEx(p, "disableUsername", nil, false, false)
		k.propManager.SetIntEx(disableUserProp, nil, misc.BoolToInt(username == nil))
		// C: prop_set_int(prop_create(p, "disablePassword"), password == NULL)
		disablePassProp := k.propManager.CreateEx(p, "disablePassword", nil, false, false)
		k.propManager.SetIntEx(disablePassProp, nil, misc.BoolToInt(password == nil))
		// C: prop_set_int(prop_create(p, "disableDomain"), domain == NULL)
		disableDomProp := k.propManager.CreateEx(p, "disableDomain", nil, false, false)
		k.propManager.SetIntEx(disableDomProp, nil, misc.BoolToInt(domain == nil))

		// C: prop_set_int(prop_create(p, "canRemember"),
		//   !!(flags & KEYRING_SHOW_REMEMBER_ME))
		canRememberProp := k.propManager.CreateEx(p, "canRemember", nil, false, false)
		k.propManager.SetIntEx(canRememberProp, nil, misc.BoolToInt(flags&FlagShowRememberMe != 0))

		// C: prop_t *rememberMe = prop_create_r(p, "rememberMe")
		// C: prop_set_int(rememberMe, remember)
		rememberMeProp := k.propManager.RefInc(k.propManager.CreateEx(p, "rememberMe", nil, false, false))
		k.propManager.SetIntEx(rememberMeProp, nil, remember)

		// C: prop_sub_t *remember_sub = prop_subscribe(0,
		//   PROP_TAG_CALLBACK_INT, set_remember, &remember,
		//   PROP_TAG_ROOT, rememberMe, NULL)
		rememberSub := k.propManager.Subscribe(rememberMeProp,
			// C: PROP_TAG_CALLBACK_INT → trampoline_int — SET_FLOAT/
			// SET_STRING convert to int, void delivers 0.
			func(opaque any, evType propcore.EventType, args ...any) {
				if v, deliver := propcore.TrampolineInt(evType, args, false); deliver {
					setRemember(&remember, v)
				}
			}, &remember)

		// C: prop_t *user = prop_create_r(p, "username")
		userProp := k.propManager.RefInc(k.propManager.CreateEx(p, "username", nil, false, false))
		// C: prop_t *pass = prop_create_r(p, "password")
		passProp := k.propManager.RefInc(k.propManager.CreateEx(p, "password", nil, false, false))
		// C: prop_t *dom = prop_create_r(p, "domain")
		domProp := k.propManager.RefInc(k.propManager.CreateEx(p, "domain", nil, false, false))
		// C: if(domain != NULL) prop_set_string(dom, *domain)
		if domain != nil {
			k.propManager.SetStringEx(domProp, nil, *domain, propcore.StringUTF8)
		}

		// C: event_t *e = popup_display(p)
		e := k.notifyMgr.PopupDisplay(p)

		// C: prop_unsubscribe(remember_sub)
		rememberSub.Unsubscribe()

		// C: if(flags & KEYRING_ONE_SHOT) parent = NULL
		// C: else if(remember) parent = persistent_keyring
		// C: else parent = temporary_keyring
		var parent *htsmsg.HTSMsg
		if flags&FlagOneShot != 0 {
			parent = nil
		} else if remember != 0 {
			parent = k.persistentKeyring
		} else {
			parent = k.temporaryKeyring
		}

		// C: if(parent != NULL) htsmsg_delete_field(parent, id)
		if parent != nil {
			parent.DeleteField(id)
		}

		// C: if(event_is_action(e, ACTION_OK)) { ... OK ... }
		if e != nil && e.IsAction(event.ACTION_OK) {
			m := htsmsg.NewMap()

			// C: if(username != NULL) { r = prop_get_string(user, NULL);
			//   htsmsg_add_str(m, "username", r ? rstr_get(r) : "");
			//   *username = strdup(r ? rstr_get(r) : ""); rstr_release(r) }
			if username != nil {
				r := k.propManager.GetString(userProp, "")
				m.AddStr("username", r)
				*username = r
			}

			// C: if(domain != NULL) { r = prop_get_string(dom, NULL);
			//   htsmsg_add_str(m, "domain", r ? rstr_get(r) : "");
			//   *domain = strdup(r ? rstr_get(r) : ""); rstr_release(r) }
			if domain != nil {
				r := k.propManager.GetString(domProp, "")
				m.AddStr("domain", r)
				*domain = r
			}

			// C: if(password != NULL) { r = prop_get_string(pass, NULL);
			//   htsmsg_add_str(m, "password", r ? rstr_get(r) : "");
			//   *password = strdup(r ? rstr_get(r) : ""); rstr_release(r) }
			if password != nil {
				r := k.propManager.GetString(passProp, "")
				m.AddStr("password", r)
				*password = r
			}

			// C: if(parent != NULL) { htsmsg_add_msg(parent, id, m);
			//   if(parent == persistent_keyring) keyring_store() }
			if parent != nil {
				parent.AddMsg(id, m)
				if parent == k.persistentKeyring {
					k.store_()
				}
			}
		} else {
			// C: else { CANCEL } if(parent == persistent_keyring) keyring_store()
			if parent == k.persistentKeyring {
				k.store_()
			}
		}

		// C: if(remember_me != NULL) *remember_me = remember
		if rememberMe != nil {
			*rememberMe = remember
		}

		// C: prop_destroy(p); prop_ref_dec(p); prop_ref_dec(user);
		//   prop_ref_dec(pass); prop_ref_dec(dom); prop_ref_dec(rememberMe)
		k.propManager.Destroy(p)
		k.propManager.RefDec(p)
		k.propManager.RefDec(userProp)
		k.propManager.RefDec(passProp)
		k.propManager.RefDec(domProp)
		k.propManager.RefDec(rememberMeProp)

		// C: if(event_is_action(e, ACTION_CANCEL)) { unlock; event_release(e); return -1 }
		if e != nil && e.IsAction(event.ACTION_CANCEL) {
			k.mutex.Unlock()
			e.Release()
			return UserRejected
		}
		// C: event_release(e)
		if e != nil {
			e.Release()
		}
	} else {
		// C: else { if((m = htsmsg_get_map(temporary_keyring, id)) == NULL &&
		//   (m = htsmsg_get_map(persistent_keyring, id)) == NULL) {
		//   unlock; return 1 }
		m := k.temporaryKeyring.GetMap(id)
		if m == nil {
			m = k.persistentKeyring.GetMap(id)
		}
		if m == nil {
			k.mutex.Unlock()
			return NotFound
		}

		// C: setstr(username, m, "username")
		setstr(username, m, "username")
		// C: setstr(password, m, "password")
		setstr(password, m, "password")
		// C: setstr(domain, m, "domain")
		setstr(domain, m, "domain")
	}

	// C: hts_mutex_unlock(&keyring_mutex); return 0
	k.mutex.Unlock()
	return OK
}
