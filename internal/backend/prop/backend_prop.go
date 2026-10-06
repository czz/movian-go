package prop

import (
	"fmt"
	"slices"
	"sync"

	propcore "github.com/czz/movian-go/internal/prop"
)

// PropPageManager manages prop pages without global state
type PropPageManager struct {
	PropPages     []*PropPage
	PropPagesMu   sync.RWMutex
	PropPageTally int
}

// NewPropPageManager creates a new prop page manager
func NewPropPageManager() *PropPageManager {
	return &PropPageManager{
		PropPages:     make([]*PropPage, 0),
		PropPageTally: 0,
	}
}

// PropPage represents a property page (proppage_t in C)
type PropPage struct {
	Pm       *propcore.PropManager
	URL      string
	Model    *propcore.Prop
	ModelSub *propcore.Subscription
	Pages    []*OpenPage
	Mu       sync.Mutex
	Manager  *PropPageManager
}

// OpenPage represents an open page referencing a PropPage (openpage_t in C)
type OpenPage struct {
	Root    *propcore.Prop
	PageSub *propcore.Subscription
	Pp      *PropPage
	Mu      sync.Mutex
}

// BackendPropMake creates a new prop page with the given model and optional URL suggestion
// Corresponds to backend_prop_make() in C
func (mgr *PropPageManager) BackendPropMake(pm *propcore.PropManager, model *propcore.Prop, suggest string) string {
	mgr.PropPagesMu.Lock()
	defer mgr.PropPagesMu.Unlock()

	pp := &PropPage{
		Pm:      pm,
		Model:   pm.RefInc(model), // C: pp->pp_model = prop_ref_inc(model)
		Manager: mgr,
	}

	if suggest == "" {
		mgr.PropPageTally++
		pp.URL = fmt.Sprintf("prop:%d", mgr.PropPageTally)
	} else {
		pp.URL = suggest
	}

	// Subscribe to model destruction
	pp.ModelSub = model.Subscribe(pp.modelCallback, pp, propcore.SubFlagTrackDestroy)

	mgr.PropPages = append(mgr.PropPages, pp)
	return pp.URL
}

// modelCallback handles model property destruction
func (pp *PropPage) modelCallback(opaque any, event propcore.EventType, args ...any) {
	if event != propcore.EventDestroyed {
		return
	}

	pp.Mu.Lock()
	defer pp.Mu.Unlock()

	// Close all open pages
	for _, op := range pp.Pages {
		op.Mu.Lock()
		op.Pp = nil
		op.Mu.Unlock()

		closeProp := op.Root.FindChild("close")
		if closeProp == nil {
			closeProp = pp.Pm.Create("close")
			op.Root.AddChild(closeProp)
		}
		closeProp.SetInt(1)
	}

	// Remove from manager list
	if pp.Manager != nil {
		pp.Manager.PropPagesMu.Lock()
		for i, p := range pp.Manager.PropPages {
			if p == pp {
				pp.Manager.PropPages = slices.Delete(pp.Manager.PropPages, i, i+1)
				break
			}
		}
		pp.Manager.PropPagesMu.Unlock()
	}

	// Cleanup
	// C: prop_ref_dec(pp->pp_model)
	if pp.Model != nil {
		pp.Model.Release()
		pp.Model = nil
	}
	if pp.ModelSub != nil {
		pp.ModelSub.Unsubscribe()
		pp.ModelSub = nil
	}
	pp.URL = ""
	pp.Pages = nil
}

// PageCallback handles page property destruction
func (op *OpenPage) PageCallback(opaque any, event propcore.EventType, args ...any) {
	if event != propcore.EventDestroyed {
		return
	}

	op.Mu.Lock()
	defer op.Mu.Unlock()

	if op.Pp != nil {
		op.Pp.Mu.Lock()
		// Remove from pp.Pages
		for i, p := range op.Pp.Pages {
			if p == op {
				op.Pp.Pages = slices.Delete(op.Pp.Pages, i, i+1)
				break
			}
		}
		op.Pp.Mu.Unlock()
		op.Pp = nil
	}

	if op.PageSub != nil {
		op.PageSub.Unsubscribe()
		op.PageSub = nil
	}
	if op.Root != nil {
		op.Root.Release()
		op.Root = nil
	}
}
