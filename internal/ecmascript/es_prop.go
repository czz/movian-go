// Canonical 1:1 port of src/ecmascript/es_prop.c — property bindings.
package ecmascript

import (
	"fmt"
	"math"
	"strings"

	event "github.com/czz/movian-go/internal/event"
	"github.com/czz/movian-go/internal/gaftape"
	propcore "github.com/czz/movian-go/internal/prop"
)

// ---------------------------------------------------------------------------
// es_prop_sub_t — C: es_prop.c:32-36
// ---------------------------------------------------------------------------

// C: es_prop_sub_t
type esPropSub struct {
	epsSuper         *ESResource
	epsSub           *propcore.Subscription
	epsAutodestry    bool
	epsActionAsArray bool
}

// ---------------------------------------------------------------------------
// Native classes — C: es_prop.c:38-44
// ---------------------------------------------------------------------------

// C: es_prop_ref_dec (es_prop.c:39)
func esPropRefDec(p any) {
	if prop, ok := p.(*propcore.Prop); ok && prop != nil {
		esEnv.propPM.RefDec(prop)
	}
}

// C: ES_NATIVE_CLASS(prop, es_prop_ref_dec) (es_prop.c:43)
var esNativeProp = &EcmascriptNativeClass{
	Name:    "prop",
	Release: esPropRefDec,
}

// C: ES_NATIVE_CLASS(propnf, prop_nf_release) (es_prop.c:44)
var esNativePropnf = &EcmascriptNativeClass{
	Name: "propnf",
	Release: func(p any) {
		if nf, ok := p.(*propcore.PropNF); ok && nf != nil {
			nf.Release()
		}
	},
}

func registerEsPropa() {
	ecmascriptRegisterNativeClass(esNativeProp)
	ecmascriptRegisterNativeClass(esNativePropnf)
}

// EsSetPropDeps wires the prop/event/backend seams (called from init).
func EsSetPropDeps(pm *propcore.PropManager, em *event.EventManager,
	ppm interface {
		BackendPropMake(*propcore.PropManager, *propcore.Prop, string) string
	}) {
	esEnv.propPM = pm
	esEnv.eventManager = em
	esEnv.propPageManager = ppm
}

// EsSetPropManager wires only the PropManager seam — for callers (plugin
// manager) that own the pm but not the event/backend seams.
func EsSetPropManager(pm *propcore.PropManager) { esEnv.propPM = pm }

// ---------------------------------------------------------------------------
// es_prop_sub_destroy — C: es_prop.c:51-63
// ---------------------------------------------------------------------------

func esPropSubDestroy(eres *ESResource) {
	eps := eres.Data.(*esPropSub)
	if eps.epsSub == nil {
		return
	}

	EsRootUnregister(eres.erCtx.ecGaf, eps)
	eps.epsSub.Unsubscribe()
	eps.epsSub = nil
	EsResourceUnlink(eres)
}

// C: es_resource_prop_sub (es_prop.c:70-74)
var esResourcePropSub = &ESResourceClass{
	ErcName:    "propsub",
	ErcDestroy: esPropSubDestroy,
}

// ---------------------------------------------------------------------------
// es_stprop_get / es_stprop_push — C: es_prop.c:78-92
// ---------------------------------------------------------------------------

// C: es_stprop_get (es_prop.c:78)
func esStpropGet(ctx *gaftape.Context, valIndex int) *propcore.Prop {
	v := esGetNativeObj(ctx, valIndex, esNativeProp)
	if p, ok := v.(*propcore.Prop); ok {
		return p
	}
	return nil
}

// C: es_stprop_push (es_prop.c:88)
func esStpropPush(ctx *gaftape.Context, p *propcore.Prop) {
	esPushNativeObj(ctx, esNativeProp, esEnv.propPM.RefInc(p))
}

// ---------------------------------------------------------------------------
// es_prop_release_duk — C: es_prop.c:97-114
// ---------------------------------------------------------------------------

func esPropReleaseGaf(ctx *gaftape.Context) int {
	p := ctx.RequirePointer(0).(*propcore.Prop)

	// C: hts_mutex_lock(&prop_mutex) — Go props self-lock; the C critical
	// section covers parent-check + destroy0 + ref_dec atomically.
	if p.GetParent() == nil {
		esEnv.propPM.Destroy(p)
	}

	esEnv.propPM.RefDec(p)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_print_duk — C: es_prop.c:117-124
// ---------------------------------------------------------------------------

func esPropPrintGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	propcore.PrintPropTree(p)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_create_duk — C: es_prop.c:129-136
// ---------------------------------------------------------------------------

func esPropCreateGaf(ctx *gaftape.Context) int {
	str := ctx.GetString(0)
	esStpropPush(ctx, esEnv.propPM.CreateRootEx(str, false))
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_get_global — C: es_prop.c:141-147
// ---------------------------------------------------------------------------

func esPropGetGlobal(ctx *gaftape.Context) int {
	esStpropPush(ctx, esEnv.propPM.GetGlobal())
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_get_name_duk — C: es_prop.c:152-162
// ---------------------------------------------------------------------------

func esPropGetNameGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)

	// C: rstr_t *r = prop_get_name(p); push rstr_get(r); rstr_release(r)
	ctx.PushString(esEnv.propPM.GetName(p))
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_get_value_duk — C: es_prop.c:167-248
// ---------------------------------------------------------------------------

func esPropGetValueGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)

	pt := p.GetPropType()

	if pt == propcore.PropTypeZombie {
		ctx.Error(ST_ERROR_PROP_ZOMBIE, "")
	}

	switch pt {
	case propcore.PropTypeString:
		// C: PROP_CSTRING / PROP_RSTRING — push raw string value
		ctx.PushString(p.GetString())

	case propcore.PropTypeURI:
		// C: PROP_URI — pushes hp_uri_title (the title part)
		if uv, ok := esEnv.propPM.GetValue(p).(propcore.URIValue); ok {
			ctx.PushString(uv.Title)
		} else {
			ctx.PushString("")
		}

	case propcore.PropTypeFloat:
		ctx.PushNumber(float64(p.GetFloat()))

	case propcore.PropTypeInt:
		ctx.PushInt(p.GetInt())

	case propcore.PropTypeVoid:
		ctx.PushNull()

	case propcore.PropTypeDir:
		// C: htsbuf_qprintf("[prop directory {", children names, "}]")
		var sb strings.Builder
		sb.WriteString("[prop directory {")
		delim := ""
		for _, c := range p.GetChildren() {
			sb.WriteString(fmt.Sprintf("%s\"%s\"", delim, c.GetName()))
			delim = ", "
		}
		sb.WriteString("}]")
		ctx.PushString(sb.String())

	default:
		ctx.PushString(fmt.Sprintf("[prop internal type %d]", int(pt)))
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_get_child_duk — C: es_prop.c:253-294
// ---------------------------------------------------------------------------

func esPropGetChildGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	var str string
	hasStr := false
	idx := 0
	isSym := false
	if ctx.IsNumber(1) {
		idx = ctx.ToInt(1)
	} else if ctx.IsSymbol(1) {
		isSym = true
	} else {
		str = ctx.RequireString(1)
		hasStr = true
	}

	if p.GetPropType() == propcore.PropTypeZombie {
		ctx.Error(ST_ERROR_PROP_ZOMBIE, "")
	}

	if isSym {
		// goja performs a @@toPrimitive lookup when coercing a prop
		// proxy (e.g. `page.entries++`), which reaches propHandler's
		// generic get trap and lands here. The vendored Duktape 1.5
		// has no Symbol type and no @@toPrimitive: its coercion is
		// ordinary ToPrimitive, i.e. the trap's 'valueOf' branch
		// returning np.getValue(obj). Push a callable doing the same
		// via this.__rawprop__ so makeProp() wraps a callable Proxy
		// and the coercion resolves exactly like in C.
		ctx.PushCFunction(func(c *gaftape.Context) int {
			c.PushThis()
			c.GetPropString(-1, "__rawprop__")
			c.SwapTop(0)
			return esPropGetValueGaf(c)
		}, 0)
		return 1
	}

	var c *propcore.Prop
	if hasStr {
		// C: p = prop_create0(p, str, NULL, 0)
		c = esEnv.propPM.CreateEx(p, str, nil, false, false)
	} else {
		// C: TAILQ_FOREACH(c, &p->hp_childs) { if(idx == 0) break; idx--; }
		children := p.GetChildren()
		if idx >= 0 && idx < len(children) {
			c = children[idx]
		}
	}

	if c != nil {
		c = esEnv.propPM.RefInc(c)
		esPushNativeObj(ctx, esNativeProp, c)
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_enum_duk — C: es_prop.c:299-339
// ---------------------------------------------------------------------------

func esPropEnumGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)

	ctx.PushArray()

	if p.GetPropType() != propcore.PropTypeDir {
		return 1
	}

	children := p.GetChildren()
	for i, c := range children {
		if name := c.GetName(); name != "" {
			ctx.PushString(name)
		} else {
			ctx.PushInt(i)
		}
		ctx.PutPropIndex(-2, i)
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_has_duk — C: es_prop.c:344-366
// ---------------------------------------------------------------------------

func esPropHasGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	name := ctx.GetString(1)
	yes := false

	if p.GetPropType() == propcore.PropTypeDir {
		for _, c := range p.GetChildren() {
			if c.GetName() == name {
				yes = true
				break
			}
		}
	}
	ctx.PushBoolean(yes)
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_delete_child_duk — C: es_prop.c:371-380
// ---------------------------------------------------------------------------

func esPropDeleteChildGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	name := ctx.RequireString(1)
	esEnv.propPM.DestroyByName(p, name)
	ctx.PushBoolean(true)
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_delete_childs_duk — C: es_prop.c:385-392
// ---------------------------------------------------------------------------

func esPropDeleteChildsGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	esEnv.propPM.DestroyChilds(p)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_destroy_duk — C: es_prop.c:397-407
// ---------------------------------------------------------------------------

func esPropDestroyGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	esEnv.propPM.Destroy(p)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_set_value_duk — C: es_prop.c:412-443
// ---------------------------------------------------------------------------

func esPropSetValueGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	str := ctx.RequireString(1)

	if ctx.IsBoolean(2) {
		v := 0
		if ctx.GetBoolean(2) {
			v = 1
		}
		esEnv.propPM.SetVEx(nil, p, str, v)
	} else if ctx.IsNumber(2) {
		dbl := ctx.GetNumber(2)

		if math.Ceil(dbl) == dbl && dbl <= math.MaxInt32 && dbl >= math.MinInt32 {
			esEnv.propPM.SetVEx(nil, p, str, int(dbl))
		} else {
			esEnv.propPM.SetVEx(nil, p, str, float32(dbl))
		}
	} else if ctx.IsString(2) {
		esEnv.propPM.SetVEx(nil, p, str, ctx.GetString(2))
	} else {
		esEnv.propPM.SetVEx(nil, p, str, nil)
	}
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_set_rich_str_duk — C: es_prop.c:448-460
// ---------------------------------------------------------------------------

func esPropSetRichStrGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	key := ctx.RequireString(1)
	richstr := ctx.RequireString(2)

	// C: prop_create_r(p, key) — prop_create_ex with incref=1
	c := esEnv.propPM.RefInc(esEnv.propPM.CreateEx(p, key, nil, false, false))
	esEnv.propPM.SetStringEx(c, nil, richstr, propcore.StringRich)
	esEnv.propPM.RefDec(c)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_set_parent_duk — C: es_prop.c:465-476
// ---------------------------------------------------------------------------

func esPropSetParentGaf(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	parent := esStpropGet(ctx, 1)

	if esEnv.propPM.SetParentEx(p, parent, nil, "") != 0 {
		esEnv.propPM.Destroy(p)
	}
	return 0
}

// ---------------------------------------------------------------------------
// es_sub_cb — C: es_prop.c:481-714
// ---------------------------------------------------------------------------

// esSubCb is the property-event → JS callback bridge.
// C: es_sub_cb — invoked by the group dispatch under ec_mutex (lockmgr).
// Go: dispatched from the context's group-courier drain goroutine.
func esSubCb(opaque any, eventType propcore.EventType, args ...any) {
	eps := opaque.(*esPropSub)
	ec := eps.epsSuper.erCtx
	ctx := ec.ecGaf

	EsPushRoot(ctx, eps)

	nargs := 0
	destroy := false

	// args trailing element is hps_user_int (unused by es_sub_cb) — strip it.
	if len(args) > 0 {
		args = args[:len(args)-1]
	}

	switch eventType {
	case propcore.EventSetDir:
		ctx.PushString("dir")
		nargs = 1

	case propcore.EventSetVoid:
		ctx.PushString("set")
		ctx.PushNull()
		nargs = 2

	case propcore.EventSetRString:
		// C: r = va_arg(ap, rstr_t *) — args[0]
		ctx.PushString("set")
		ctx.PushString(esArgString(args, 0))
		nargs = 2

	case propcore.EventSetCString:
		ctx.PushString("set")
		ctx.PushString(esArgString(args, 0))
		nargs = 2

	case propcore.EventSetURI:
		// C: r = title, r2 = url
		ctx.PushString("uri")
		ctx.PushString(esArgString(args, 0))
		ctx.PushString(esArgString(args, 1))
		nargs = 3

	case propcore.EventSetInt:
		ctx.PushString("set")
		ctx.PushInt(esArgInt(args, 0))
		nargs = 2

	case propcore.EventSetFloat:
		ctx.PushString("set")
		ctx.PushNumber(esArgFloat(args, 0))
		nargs = 2

	case propcore.EventWantMoreChilds:
		ctx.PushString("wantmorechilds")
		nargs = 1

	case propcore.EventDestroyed:
		// C: (void)va_arg(ap, prop_sub_t *)
		ctx.PushString("destroyed")
		nargs = 1
		if eps.epsAutodestry {
			destroy = true
		}

	case propcore.EventReqMoveChild:
		// Go: notifySub(sub, EventReqMoveChild, child, parent, before)
		ctx.PushString("reqmove")
		p1 := esArgProp(args, 0)
		p2 := esArgProp(args, 2)
		nargs = 3
		esStpropPush(ctx, p1)
		if p2 != nil {
			esStpropPush(ctx, p2)
		} else {
			ctx.PushNull()
		}

	case propcore.EventAddChild:
		// Go: notifySub(sub, EventAddChild, child, parent)
		ctx.PushString("addchild")
		nargs = 2
		esStpropPush(ctx, esArgProp(args, 0))

	case propcore.EventAddChildBefore:
		// Go: notifySub(sub, EventAddChildBefore, child, parent, before)
		ctx.PushString("addchildbefore")
		p1 := esArgProp(args, 0)
		p2 := esArgProp(args, 2)
		nargs = 3
		esStpropPush(ctx, p1)
		esStpropPush(ctx, p2)

	case propcore.EventAddChildVector, propcore.EventAddChildVectorDirect:
		ctx.PushString("addchilds")
		pv := esArgPropVec(args, 0)
		ctx.PushArray()
		for i := range pv.Len() {
			esStpropPush(ctx, pv.Get(i))
			ctx.PutPropIndex(-2, i)
		}
		nargs = 2

	case propcore.EventAddChildVectorBefore:
		ctx.PushString("addchildsbefore")
		pv := esArgPropVec(args, 0)
		p2 := esArgProp(args, 1)
		ctx.PushArray()
		for i := range pv.Len() {
			esStpropPush(ctx, pv.Get(i))
			ctx.PutPropIndex(-2, i)
		}
		esStpropPush(ctx, p2)
		nargs = 3

	case propcore.EventDelChild:
		// Go: notifySub(sub, EventDelChild, child, parent)
		ctx.PushString("delchild")
		p1 := esArgProp(args, 0)
		esStpropPush(ctx, p1)
		nargs = 2

	case propcore.EventMoveChild:
		// Go: notifySub(sub, EventMoveChild, child, parent, before)
		ctx.PushString("movechild")
		p1 := esArgProp(args, 0)
		p2 := esArgProp(args, 2)
		esStpropPush(ctx, p1)
		esStpropPush(ctx, p2)
		nargs = 3

	case propcore.EventExtEvent:
		e := event.ConcreteOf(esArgExtEvent(args, 0))

		var nav *propcore.Prop
		switch ev := e.(type) {
		case *event.EventPayload:
			if ev.Type == event.EVENT_DYNAMIC_ACTION {
				nargs = 2
				ctx.PushString("action")
				if eps.epsActionAsArray {
					ctx.PushArray()
					ctx.PushString(ev.Payload)
					ctx.PutPropIndex(-2, 0)
				} else {
					ctx.PushString(ev.Payload)
				}
				nav = ev.Nav
			}

		case *event.EventActionVector:
			if ev.Type == event.EVENT_ACTION_VECTOR {
				// C: assert(eav->num > 0)
				nargs = 2
				ctx.PushString("action")
				if eps.epsActionAsArray {
					ctx.PushArray()
					for i, a := range ev.Actions {
						ctx.PushString(esEnv.eventManager.ActionCode2Str(a))
						ctx.PutPropIndex(-2, i)
					}
				} else {
					ctx.PushString(esEnv.eventManager.ActionCode2Str(ev.Actions[0]))
				}
				nav = ev.Nav
			}
		case *event.EventInt:
			if ev.Type == event.EVENT_UNICODE {
				nargs = 2
				ctx.PushString("unicode")
				ctx.PushInt(ev.Val)
				nav = ev.Nav
			}
		case *event.EventProp:
			if ev.Type == event.EVENT_PROPREF {
				nargs = 2
				ctx.PushString("propref")
				if p, ok := ev.P.(*propcore.Prop); ok {
					esStpropPush(ctx, p)
				} else {
					ctx.PushNull()
				}
				nav = ev.Nav
			}
		case *event.Event:
			// Fallback for flattened *Event (subtypes lose their outer
			// type on round-trip — C casts event_t* back). Payload and
			// Actions live on the base Event.
			if ev.Type == event.EVENT_DYNAMIC_ACTION {
				nargs = 2
				ctx.PushString("action")
				if eps.epsActionAsArray {
					ctx.PushArray()
					ctx.PushString(ev.Payload)
					ctx.PutPropIndex(-2, 0)
				} else {
					ctx.PushString(ev.Payload)
				}
				nav = ev.Nav
			} else if ev.Type == event.EVENT_ACTION_VECTOR {
				nargs = 2
				ctx.PushString("action")
				if eps.epsActionAsArray {
					ctx.PushArray()
					for i, a := range ev.Actions {
						ctx.PushString(esEnv.eventManager.ActionCode2Str(a))
						ctx.PutPropIndex(-2, i)
					}
				} else {
					ctx.PushString(esEnv.eventManager.ActionCode2Str(ev.Actions[0]))
				}
				nav = ev.Nav
			}
		}
		if nargs > 0 && nav != nil {
			esStpropPush(ctx, nav)
			nargs++
		}

	case propcore.EventSelectChild:
		// Go: notifySub(sub, EventSelectChild, child, parent [, extra])
		ctx.PushString("selectchild")
		esStpropPush(ctx, esArgProp(args, 0))
		nargs = 2

	default:
		nargs = 0
	}

	if nargs > 0 {
		rc := ctx.PCall(nargs)
		if rc != 0 {
			EsDumpErr(ctx)
		}
	}
	ctx.Pop()

	if destroy {
		EsResourceDestroy(eps.epsSuper)
	}
}

// ---------------------------------------------------------------------------
// es_prop_subscribe — C: es_prop.c:721-761
// ---------------------------------------------------------------------------

func esPropSubscribe(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	p := esStpropGet(ctx, 0)
	eps := &esPropSub{}
	eps.epsSuper = EsResourceAlloc(esResourcePropSub, eps)
	EsResourceLink(eps.epsSuper, ec, 1)

	EsRootRegister(ctx, 1, eps)

	eps.epsAutodestry = EsPropIsTrue(ctx, 2, "autoDestroy") != 0

	var subArgs []any
	subArgs = append(subArgs, propcore.SubFlagTrackDestroy)

	if EsPropIsTrue(ctx, 2, "ignoreVoid") != 0 {
		subArgs = append(subArgs, propcore.SubFlagIgnoreVoid)
	}

	if EsPropIsTrue(ctx, 2, "debug") != 0 {
		subArgs = append(subArgs, propcore.SubFlagDebug)
	}

	if EsPropIsTrue(ctx, 2, "noInitialUpdate") != 0 {
		subArgs = append(subArgs, propcore.SubNoInitialUpdate)
	}

	if EsPropIsTrue(ctx, 2, "earlyChildDelete") != 0 {
		subArgs = append(subArgs, propcore.SubFlagEarlyDelChild)
	}

	if EsPropIsTrue(ctx, 2, "actionAsArray") != 0 {
		eps.epsActionAsArray = true
	}

	// C: prop_subscribe(flags, PROP_TAG_ROOT p, PROP_TAG_LOCKMGR es_lockmgr,
	//   PROP_TAG_MUTEX ec, PROP_TAG_CALLBACK es_sub_cb eps,
	//   PROP_TAG_DISPATCH_GROUP ec->ec_prop_dispatch_group, NULL)
	// Group-mode notifications are dispatched by the global workers,
	// which hold ec->ec_mutex via ecmascript_context_lockmgr.
	subArgs = append(subArgs,
		propcore.SubMutex{Ptr: ec},
		propcore.SubLockmgr{L: ecPropLockmgr},
		propcore.SubDispatchGroup{G: ec.ecPropDispatchGroup})

	eps.epsSub = p.Subscribe(esSubCb, eps, subArgs...)

	EsResourcePush(ctx, eps.epsSuper)
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_have_more — C: es_prop.c:766-774
// ---------------------------------------------------------------------------

func esPropHaveMore(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	yes := ctx.RequireBoolean(1)
	esEnv.propPM.HaveMoreChilds(p, yes)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_make_url — C: es_prop.c:779-788
// ---------------------------------------------------------------------------

func esPropMakeURL(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	// C: rstr_t *r = backend_prop_make(p, NULL)
	var r string
	if esEnv.propPageManager != nil {
		r = esEnv.propPageManager.BackendPropMake(esEnv.propPM, p, "")
	}
	ctx.PushString(r)
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_select / link / unlink — C: es_prop.c:793-821
// ---------------------------------------------------------------------------

func esPropSelect(ctx *gaftape.Context) int {
	// C: prop_select(p) = prop_select_ex(p, NULL, NULL)
	esEnv.propPM.SelectChildPropEx(esStpropGet(ctx, 0), nil, nil)
	return 0
}

func esPropLink(ctx *gaftape.Context) int {
	// C: prop_link(src, dst) = prop_link_ex(src, dst, NULL, PROP_LINK_NORMAL, 0)
	esEnv.propPM.Link(esStpropGet(ctx, 0), esStpropGet(ctx, 1), nil, false, false)
	return 0
}

func esPropUnlink(ctx *gaftape.Context) int {
	esEnv.propPM.Unlink(esStpropGet(ctx, 0))
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_send_event — C: es_prop.c:826-864
// ---------------------------------------------------------------------------

func esPropSendEvent(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	typ := ctx.RequireString(1)
	var e propcore.ExtEvent

	if typ == "redirect" {
		e = esEnv.eventManager.CreateStr(event.EVENT_REDIRECT, ctx.RequireString(2))
	} else if typ == "openurl" {
		url := EsPropToRstr(ctx, 2, "url")
		view := EsPropToRstr(ctx, 2, "view")
		how := EsPropToRstr(ctx, 2, "how")
		parentURL := EsPropToRstr(ctx, 2, "parenturl")

		e = esEnv.eventManager.CreateOpenURLArgs(&event.EventOpenURLArgs{
			URL:       esRstrGet(url),
			View:      esRstrGet(view),
			How:       esRstrGet(how),
			ParentURL: esRstrGet(parentURL),
		})
	} else {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Event type %s not understood", typ)
	}

	esEnv.propPM.SendExtEvent(p, e)
	e.Release()
	return 0
}

// esRstrGet dereferences the *string rstr stand-in (C: rstr_get).
func esRstrGet(r *string) string {
	if r == nil {
		return ""
	}
	return *r
}

// ---------------------------------------------------------------------------
// es_prop_is_value — C: es_prop.c:869-899
// ---------------------------------------------------------------------------

func esPropIsValue(ctx *gaftape.Context) int {
	v := esGetNativeObjNothrow(ctx, 0, esNativeProp)
	p, _ := v.(*propcore.Prop)
	if p == nil {
		ctx.PushBoolean(false)
	} else {
		yes := false
		switch p.GetPropType() {
		case propcore.PropTypeString, propcore.PropTypeURI,
			propcore.PropTypeFloat, propcore.PropTypeInt,
			propcore.PropTypeVoid:
			yes = true
		}
		ctx.PushBoolean(yes)
	}
	return 1
}

// ---------------------------------------------------------------------------
// es_prop_atomic_add — C: es_prop.c:904-913
// ---------------------------------------------------------------------------

func esPropAtomicAdd(ctx *gaftape.Context) int {
	p := esStpropGet(ctx, 0)
	num := int(ctx.RequireNumber(1))

	// C: prop_add_int(p, num)
	p.AddInt(num)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_is_same / move_before — C: es_prop.c:918-939
// ---------------------------------------------------------------------------

func esPropIsSame(ctx *gaftape.Context) int {
	a := esStpropGet(ctx, 0)
	b := esStpropGet(ctx, 1)
	ctx.PushBoolean(a == b)
	return 1
}

func esPropMoveBefore(ctx *gaftape.Context) int {
	a := esStpropGet(ctx, 0)
	v := esGetNativeObjNothrow(ctx, 1, esNativeProp)
	b, _ := v.(*propcore.Prop)
	esEnv.propPM.Move(a, b)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_unload_destroy — C: es_prop.c:944-952
// ---------------------------------------------------------------------------

func esPropUnloadDestroy(ctx *gaftape.Context) int {
	ec := EsGet(ctx)
	a := esStpropGet(ctx, 0)
	ec.ecPropUnloadDestroy = propcore.PropVecAppend(ec.ecPropUnloadDestroy, a)
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_is_zombie / set_clip_range — C: es_prop.c:957-976
// ---------------------------------------------------------------------------

func esPropIsZombie(ctx *gaftape.Context) int {
	a := esStpropGet(ctx, 0)
	ctx.PushBoolean(a.GetPropType() == propcore.PropTypeZombie)
	return 1
}

func esPropSetClipRange(ctx *gaftape.Context) int {
	a := esStpropGet(ctx, 0)
	// C: prop_set_int_clipping_range(a, min, max)
	a.SetClippedInt(ctx.ToInt(1), ctx.ToInt(2))
	return 0
}

// ---------------------------------------------------------------------------
// es_prop_tag_set / clear / get — C: es_prop.c:981-1021
// ---------------------------------------------------------------------------

func esPropTagSet(ctx *gaftape.Context) int {
	eps := EsResourceGet(ctx, 0, esResourcePropSub).(*ESResource).Data.(*esPropSub)
	a := esStpropGet(ctx, 1)
	// C: void *v = malloc(1) — a unique token identity for the tag/root key
	v := &struct{}{}
	esEnv.propPM.TagSet(a, eps, v)
	EsRootRegister(ctx, 2, v)
	return 0
}

func esPropTagClear(ctx *gaftape.Context) int {
	eps := EsResourceGet(ctx, 0, esResourcePropSub).(*ESResource).Data.(*esPropSub)
	a := esStpropGet(ctx, 1)
	v := esEnv.propPM.TagClear(a, eps)
	EsPushRoot(ctx, v)
	EsRootUnregister(ctx, v)
	return 1
}

func esPropTagGet(ctx *gaftape.Context) int {
	eps := EsResourceGet(ctx, 0, esResourcePropSub).(*ESResource).Data.(*esPropSub)
	a := esStpropGet(ctx, 1)
	v := esEnv.propPM.TagGet(a, eps)
	EsPushRoot(ctx, v)
	return 1
}

// ---------------------------------------------------------------------------
// node filters — C: es_prop.c:1024-1083
// ---------------------------------------------------------------------------

func esPropNodeFilterCreate(ctx *gaftape.Context) int {
	esPushNativeObj(ctx, esNativePropnf,
		propcore.PropNFCreate(esStpropGet(ctx, 0), esStpropGet(ctx, 1), nil, 0))
	return 1
}

func esPropNodeFilterAddPred(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativePropnf)
	pnf, _ := v.(*propcore.PropNF)
	path := ctx.RequireString(1)
	ev := esGetNativeObjNothrow(ctx, 4, esNativeProp)
	enable, _ := ev.(*propcore.Prop)

	cfstr := ctx.RequireString(2)
	var cf propcore.PropNFCmp
	if cfstr == "eq" {
		cf = propcore.PropNFCmpEq
	} else if cfstr == "neq" {
		cf = propcore.PropNFCmpNeq
	} else {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Bad comparison function")
	}

	modestr := ctx.RequireString(5)
	var mode propcore.PropNFMode
	if modestr == "include" {
		mode = propcore.PropNFModeInclude
	} else if modestr == "exclude" {
		mode = propcore.PropNFModeExclude
	} else {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Bad filter mode")
	}

	var r int
	if ctx.IsString(3) {
		r = propcore.PropNFPredStrAdd(pnf, path, cf, ctx.ToString(3), enable, mode)
	} else if ctx.IsNumber(3) {
		r = propcore.PropNFPredIntAdd(pnf, path, cf, int(ctx.ToNumber(3)), enable, mode)
	} else {
		ctx.Error(gaftape.GAF_ERR_ERROR, "Predicate is not a string or number")
	}
	ctx.PushInt(r)
	return 1
}

func esPropNodeFilterDelPred(ctx *gaftape.Context) int {
	v := esGetNativeObj(ctx, 0, esNativePropnf)
	pnf, _ := v.(*propcore.PropNF)
	predid := int(ctx.RequireNumber(1))
	propcore.PropNFPredRemove(pnf, predid)
	return 0
}

// ecPropLockmgr wraps ecmascript_context_lockmgr for the prop dispatch
// machinery — global/group workers hold ec->ec_mutex around each callback
// (C: PROP_TAG_LOCKMGR, ecmascript_context_lockmgr + PROP_TAG_MUTEX, ec).
var ecPropLockmgr = &propcore.Lockmgr{Fn: func(ptr any, op propcore.LockmgrOp) int {
	return EcmascriptContextLockmgr(ptr.(*ESContext), int(op))
}}

// ---------------------------------------------------------------------------
// arg helpers for esSubCb — Go notify args are typed any slices.
// ---------------------------------------------------------------------------

func esArgString(args []any, i int) string {
	if i >= len(args) {
		return ""
	}
	switch v := args[i].(type) {
	case string:
		return v
	case *string:
		return esRstrGet(v)
	}
	return ""
}

func esArgInt(args []any, i int) int {
	if i >= len(args) {
		return 0
	}
	switch v := args[i].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	}
	return 0
}

func esArgFloat(args []any, i int) float64 {
	if i >= len(args) {
		return 0
	}
	switch v := args[i].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func esArgProp(args []any, i int) *propcore.Prop {
	if i >= len(args) {
		return nil
	}
	p, _ := args[i].(*propcore.Prop)
	return p
}

func esArgPropVec(args []any, i int) *propcore.PropVec {
	if i >= len(args) {
		return nil
	}
	if pv, ok := args[i].(*propcore.PropVec); ok {
		return pv
	}
	if pv, ok := args[i].([]*propcore.Prop); ok {
		v := propcore.PropVecCreate(len(pv))
		for _, p := range pv {
			v = propcore.PropVecAppend(v, p)
		}
		return v
	}
	return nil
}

func esArgExtEvent(args []any, i int) any {
	if i >= len(args) {
		return nil
	}
	return args[i]
}

// ---------------------------------------------------------------------------
// fnlist_prop — C: es_prop.c:1090-1128
// ---------------------------------------------------------------------------

var esFnlistProp = []gaftape.FunctionListEntry{
	{Key: "print", Value: esPropPrintGaf, Nargs: 1},
	{Key: "release", Value: esPropReleaseGaf, Nargs: 1},
	{Key: "create", Value: esPropCreateGaf, Nargs: 1},
	{Key: "getValue", Value: esPropGetValueGaf, Nargs: 1},
	{Key: "getName", Value: esPropGetNameGaf, Nargs: 1},
	{Key: "getChild", Value: esPropGetChildGaf, Nargs: 2},
	{Key: "set", Value: esPropSetValueGaf, Nargs: 3},
	{Key: "setRichStr", Value: esPropSetRichStrGaf, Nargs: 3},
	{Key: "setParent", Value: esPropSetParentGaf, Nargs: 2},
	{Key: "subscribe", Value: esPropSubscribe, Nargs: 3},
	{Key: "haveMore", Value: esPropHaveMore, Nargs: 2},
	{Key: "makeUrl", Value: esPropMakeURL, Nargs: 1},
	{Key: "global", Value: esPropGetGlobal, Nargs: 0},
	{Key: "enumerate", Value: esPropEnumGaf, Nargs: 1},
	{Key: "has", Value: esPropHasGaf, Nargs: 2},
	{Key: "deleteChild", Value: esPropDeleteChildGaf, Nargs: 2},
	{Key: "deleteChilds", Value: esPropDeleteChildsGaf, Nargs: 1},
	{Key: "destroy", Value: esPropDestroyGaf, Nargs: 1},
	{Key: "select", Value: esPropSelect, Nargs: 1},
	{Key: "link", Value: esPropLink, Nargs: 2},
	{Key: "unlink", Value: esPropUnlink, Nargs: 1},
	{Key: "sendEvent", Value: esPropSendEvent, Nargs: 3},
	{Key: "isValue", Value: esPropIsValue, Nargs: 1},
	{Key: "atomicAdd", Value: esPropAtomicAdd, Nargs: 2},
	{Key: "isSame", Value: esPropIsSame, Nargs: 2},
	{Key: "moveBefore", Value: esPropMoveBefore, Nargs: 2},
	{Key: "unloadDestroy", Value: esPropUnloadDestroy, Nargs: 1},
	{Key: "isZombie", Value: esPropIsZombie, Nargs: 1},
	{Key: "setClipRange", Value: esPropSetClipRange, Nargs: 3},
	{Key: "tagSet", Value: esPropTagSet, Nargs: 3},
	{Key: "tagClear", Value: esPropTagClear, Nargs: 2},
	{Key: "tagGet", Value: esPropTagGet, Nargs: 2},
	{Key: "nodeFilterCreate", Value: esPropNodeFilterCreate, Nargs: 2},
	{Key: "nodeFilterAddPred", Value: esPropNodeFilterAddPred, Nargs: 6},
	{Key: "nodeFilterDelPred", Value: esPropNodeFilterDelPred, Nargs: 2},
}

// C: ES_MODULE("prop", fnlist_prop) (es_prop.c:1131)
func registerEsPropb() {
	EcmascriptRegisterModule(&EcmascriptModule{
		Name:      "prop",
		Functions: esFnlistProp,
	})
}
