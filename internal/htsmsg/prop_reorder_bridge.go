package htsmsg

// prop_reorder bridge — prop/core can't import htsmsg (import cycle
// via fileaccess→arch→prop). These adapt htsmsg_store_save/load for
// C: prop_reorder.c's pr_order persistence; wired at init via
// PropManager.SetReorderBridge.

// PropReorderStoreSave — C: htsmsg_store_save of the reorder list.
func PropReorderStoreSave(storeDep any, id string, order []string) {
	store, _ := storeDep.(*Store)
	if store == nil {
		return
	}
	msg := NewList()
	for _, s := range order {
		msg.AddStr("", s)
	}
	store.Save(msg, id)
}

// PropReorderStoreLoad — C: htsmsg_store_load of the reorder list.
func PropReorderStoreLoad(storeDep any, id string) []string {
	store, _ := storeDep.(*Store)
	if store == nil {
		return nil // C: htsmsg_store_load returns NULL when store uninit/absent
	}
	msg, err := store.Load(id)
	if err != nil || msg == nil {
		return nil
	}
	var out []string
	for _, f := range msg.GetFields() {
		if f.GetType() == HmfStr {
			out = append(out, f.GetStrValue())
		}
	}
	msg.Release()
	return out
}
