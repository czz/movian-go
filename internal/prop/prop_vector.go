package prop

import (
	"sync/atomic"
)

/*
 * Port of src/prop/prop_vector.c
 *
 * C: typedef struct prop_vec {
 *      atomic_t pv_refcount;
 *      int pv_capacity;
 *      int pv_length;
 *      struct prop *pv_vec[0];   // flexible array member
 *    } prop_vec_t;
 *
 * C allocates one block: malloc(sizeof(prop_vec_t) + sizeof(prop_t*) * cap)
 * and grows it with realloc, which may move — callers must use the
 * returned pointer. The Go port mirrors that contract: PropVecAppend
 * returns the (possibly new) vector.
 */

// C: prop_vec_t — pv_vec is a GC-scanned pointer array
// keeps the pointees alive, like C's prop_t* owning a ref).
type PropVec struct {
	pvRefcount atomic.Int32 // C: atomic_t pv_refcount
	pvCapacity int          // C: pv_capacity
	pvLength   int          // C: pv_length
	pvVec      []*Prop      // C: pv_vec[0]
}

// C: prop_vec_t *prop_vec_create(int capacity)
func PropVecCreate(capacity int) *PropVec {
	// C: pv = malloc(sizeof(prop_vec_t) + sizeof(prop_t *) * capacity)
	pv := &PropVec{
		pvCapacity: capacity,
		pvLength:   0,
		pvVec:      make([]*Prop, capacity),
	}
	pv.pvRefcount.Store(1) // C: atomic_set(&pv->pv_refcount, 1)
	return pv
}

// C: prop_vec_t *prop_vec_append(prop_vec_t *pv, prop_t *p)
func PropVecAppend(pv *PropVec, p *Prop) *PropVec {
	// C: assert(atomic_get(&pv->pv_refcount) == 1)
	if pv.pvRefcount.Load() != 1 {
		panic("prop_vec_append: refcount != 1")
	}

	if pv.pvLength == pv.pvCapacity {
		pv.pvCapacity++
		// C: pv = realloc(pv, sizeof(prop_vec_t) + sizeof(prop_t *) * pv->pv_capacity)
		// realloc may move the block — replicate by allocating a new vector.
		npv := &PropVec{
			pvCapacity: pv.pvCapacity,
			pvLength:   pv.pvLength,
			pvVec:      make([]*Prop, pv.pvCapacity),
		}
		npv.pvRefcount.Store(1)
		copy(npv.pvVec, pv.pvVec)
		pv = npv
	}
	// C: assert(pv->pv_length < pv->pv_capacity)
	if !(pv.pvLength < pv.pvCapacity) {
		panic("prop_vec_append: length >= capacity")
	}
	// C: pv->pv_vec[pv->pv_length] = prop_ref_inc(p)
	if p != nil {
		atomic.AddInt32(&p.refCount, 1)
	}
	pv.pvVec[pv.pvLength] = p
	pv.pvLength++
	return pv
}

// C: prop_vec_t *prop_vec_addref(prop_vec_t *pv)
func PropVecAddref(pv *PropVec) *PropVec {
	pv.pvRefcount.Add(1) // C: atomic_inc(&pv->pv_refcount)
	return pv
}

// C: void prop_vec_release(prop_vec_t *pv)
func PropVecRelease(pv *PropVec) {
	// C: if(atomic_dec(&pv->pv_refcount)) return;
	if pv.pvRefcount.Add(-1) != 0 {
		return
	}

	for i := range pv.pvLength {
		// C: prop_ref_dec(pv->pv_vec[i])
		p := pv.pvVec[i]
		if p != nil && p.manager != nil {
			p.manager.RefDec(p)
		} else if p != nil {
			atomic.AddInt32(&p.refCount, -1)
		}
	}
	// C: free(pv) — Go GC reclaims the block
}

// C: void prop_vec_destroy_entries(prop_vec_t *pv)
func PropVecDestroyEntries(pv *PropVec) {
	for i := range pv.pvLength {
		// C: prop_destroy(pv->pv_vec[i])
		p := pv.pvVec[i]
		if p != nil && p.manager != nil {
			p.manager.Destroy(p)
		}
	}
}

// C: pv_length accessor (C reads the field directly)
func (pv *PropVec) Len() int { return pv.pvLength }

// C: pv_vec[i] accessor
func (pv *PropVec) Get(i int) *Prop {
	if i < 0 || i >= pv.pvLength {
		return nil
	}
	return pv.pvVec[i]
}

// VecAt returns the prop at index i (C: pv->pv_vec[i]).
func (pv *PropVec) VecAt(i int) *Prop {
	if i < 0 || i >= pv.pvLength {
		return nil
	}
	return pv.pvVec[i]
}

// RequestDelete — C: prop_request_delete (prop_core.c:5159-5187).
// Fires PROP_REQ_DELETE on the prop's own subscribers, then
// PROP_REQ_DELETE_VECTOR on the parent (when it's a directory).
func (p *Prop) RequestDelete() {
	if p == nil {
		return
	}
	if p.GetType() == PropTypeProxy { // C: hp_type == PROP_PROXY → return
		return
	}
	if p.GetType() != PropTypeZombie {
		parent := p.GetParent()
		p.FireEvent(EventReqDelete)
		if parent != nil && parent.GetType() == PropTypeDir {
			pv := PropVecCreate(1)
			pv = PropVecAppend(pv, p)
			parent.FireEvent(EventReqDeleteVector, pv)
			PropVecRelease(pv)
		}
	}
}

// RequestDeleteMulti — C: prop_request_delete_multi (prop_core.c:5190-5197).
// Fires PROP_REQ_DELETE_VECTOR on the parent of the first vector element.
func RequestDeleteMulti(pv *PropVec) {
	if pv == nil || pv.pvLength == 0 {
		return
	}
	first := pv.VecAt(0)
	if first == nil {
		return
	}
	parent := first.GetParent()
	if parent != nil {
		parent.FireEvent(EventReqDeleteVector, pv)
	}
}
