package prop

import "sync"

// SubOpts is the option-typed form of Subscribe's variadic tag args —
// the C PROP_TAG_* list collapsed into one self-documenting struct.
// New code should use SubscribeOpts; the variadic Subscribe remains
// for compatibility (the two are wire-equivalent).
type SubOpts struct {
	// Flags — the PROP_SUB_* bitmask (SubFlag* constants).
	Flags int

	// Courier — dispatch the callback on this courier instead of the
	// global dispatch thread (PROP_TAG_COURIER).
	Courier *Courier

	// ValueProp — the prop carrying the subscription's value
	// (PROP_SUB_VALUE_PROP) — the link-followed canonical target.
	ValueProp *Prop

	// Origins — the link origin chain for retarget-follow
	// (PROP_SUB_ORIGIN_CHAIN) — collected by ResolveForSubscription.
	Origins []*Prop

	// UserInt — opaque int delivered to the callback (PROP_TAG_USER_INT).
	// Sent only when HasUserInt is true — a 0 can be a real value.
	UserInt    int
	HasUserInt bool

	// Mutex — C: PROP_SUB_MUTEX — lock held while the callback runs.
	Mutex *sync.Mutex
}

// toArgs decomposes the struct into the variadic tag list Subscribe
// consumes — keeps one canonical parse path.
func (o SubOpts) toArgs() []any {
	args := []any{o.Flags}
	if o.Courier != nil {
		args = append(args, SubCourier{C: o.Courier})
	}
	if o.Mutex != nil {
		args = append(args, SubMutex{Ptr: o.Mutex})
	}
	if o.HasUserInt {
		args = append(args, SubUserInt(o.UserInt))
	}
	if o.ValueProp != nil {
		args = append(args, SubValueProp{P: o.ValueProp})
	}
	if len(o.Origins) > 0 {
		args = append(args, SubOrigins{Props: o.Origins})
	}
	return args
}

// SubscribeOpts — Subscribe with the typed option struct.
func (p *Prop) SubscribeOpts(callback func(opaque any, eventType EventType, args ...any), opaque any, o SubOpts) *Subscription {
	return p.Subscribe(callback, opaque, o.toArgs()...)
}
