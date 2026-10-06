package misc

import (
	"strings"
	"sync/atomic"
)

/*
 * Port of src/misc/rstr.h + src/misc/rstr.c
 * USE_RSTR_REFCOUNTING is defined (C: !ENABLE_BUGHUNT).
 *
 * C: typedef struct rstr { atomic_t refcnt; char str[0]; } rstr_t;
 */

// C: rstr_t — refcounted immutable string. str is the C flexible
// array member char str[0], held as a Go string.
type Rstr struct {
	refcnt atomic.Int32 // C: atomic_t refcnt
	str    string       // C: char str[0]
}

// C: rstr_t *rstr_alloc(const char *in)
func RstrAlloc(in *string) *Rstr {
	if in == nil {
		return nil
	}
	rs := &Rstr{str: *in}
	rs.refcnt.Store(1) // C: atomic_set(&rs->refcnt, 1)
	return rs
}

// RstrAllocStr is rstr_alloc for a Go string (C takes const char*).
func RstrAllocStr(in string) *Rstr {
	rs := &Rstr{str: in}
	rs.refcnt.Store(1)
	return rs
}

// C: rstr_t *rstr_allocl(const char *in, size_t len)
func RstrAllocl(in *string, len_ int) *Rstr {
	rs := &Rstr{}
	rs.refcnt.Store(1)
	if in != nil {
		s := *in
		if len_ < len(s) {
			s = s[:len_]
		}
		rs.str = s
	} else {
		rs.str = string(make([]byte, len_))
	}
	return rs
}

// C: static __inline const char *rstr_get(const rstr_t *rs)
func RstrGet(rs *Rstr) string {
	if rs == nil {
		return "" // C: NULL — Go callers use "" or check rs == nil
	}
	return rs.str
}

// C: static __inline const char *rstr_get_always(const rstr_t *rs)
func RstrGetAlways(rs *Rstr) string {
	return rs.str
}

// C: static __inline char *rstr_data(rstr_t *rs)
func RstrData(rs *Rstr) string {
	return rs.str
}

// C: static __inline rstr_t *rstr_dup(rstr_t *rs)
func RstrDup(rs *Rstr) *Rstr {
	if rs != nil {
		rs.refcnt.Add(1) // C: atomic_inc(&rs->refcnt)
	}
	return rs
}

// C: static __inline void rstr_release(rstr_t *rs)
func RstrRelease(rs *Rstr) {
	if rs != nil && rs.refcnt.Add(-1) == 0 {
		// C: free(rs)
	}
}

// C: static __inline void rstr_set(rstr_t **p, rstr_t *r)
func RstrSet(p **Rstr, r *Rstr) {
	RstrRelease(*p)
	if r != nil {
		*p = RstrDup(r)
	} else {
		*p = nil
	}
}

// C: rstr_t *rstr_spn(rstr_t *s, const char *set, int offset)
// l = strcspn(rstr_get(s) + offset, set) + offset
func RstrSpn(s *Rstr, set string, offset int) *Rstr {
	str := RstrGet(s)
	if offset >= len(str) {
		return RstrDup(s)
	}
	l := strcspn(str[offset:], set) + offset
	if l == len(str) {
		return RstrDup(s)
	}
	sub := str[:l]
	return RstrAllocl(&sub, l)
}

// C: strcspn — length of initial segment not containing any char of set
func strcspn(s, set string) int {
	if i := strings.IndexAny(s, set); i >= 0 {
		return i
	}
	return len(s)
}

// C: static __inline int rstr_eq(const rstr_t *a, const rstr_t *b)
func RstrEq(a, b *Rstr) int {
	if a == nil && b == nil {
		return 1
	}
	if a == nil || b == nil {
		return 0
	}
	if RstrGet(a) == RstrGet(b) {
		return 1
	}
	return 0
}

// C: typedef struct rstr_vec { int size; int capacity; rstr_t *v[0]; }
// v is the flexible array member.
type RstrVec struct {
	size     int
	capacity int
	v        []*Rstr
}

// C: void rstr_vec_append(rstr_vec_t **rvp, rstr_t *str)
func RstrVecAppend(rvp **RstrVec, str *Rstr) {
	rv := *rvp

	if rv == nil {
		// C: malloc(sizeof(rstr_vec_t) + sizeof(rstr_t *) * 16)
		rv = &RstrVec{capacity: 16, size: 0, v: make([]*Rstr, 16)}
		*rvp = rv
	} else if rv.size == rv.capacity {
		rv.capacity = rv.capacity * 2
		// C: rv = realloc(...)
		nv := make([]*Rstr, rv.capacity)
		copy(nv, rv.v)
		rv.v = nv
		*rvp = rv
	}
	rv.v[rv.size] = RstrDup(str)
	rv.size++
}

// C: void rstr_vec_free(rstr_vec_t *rv)
func RstrVecFree(rv *RstrVec) {
	if rv == nil {
		return
	}
	for i := range rv.size {
		RstrRelease(rv.v[i])
	}
	// C: free(rv)
}
