package backend

import (
	"sync"

	propcore "github.com/czz/movian-go/internal/prop"
)

// BackendDef represents a backend definition
type BackendDef struct {
	Name  string
	Prio  int
	Start func(pm *propcore.PropManager) error
	Fini  func(pm *propcore.PropManager) error
}

// BackendRegistry manages backend registration without global state
type BackendRegistry struct {
	backendMutex       sync.RWMutex
	registeredBackends []*BackendDef
}

// NewBackendRegistry creates a new backend registry
func NewBackendRegistry() *BackendRegistry {
	return &BackendRegistry{
		registeredBackends: make([]*BackendDef, 0),
	}
}

// RegisterBackend registers a backend
func (br *BackendRegistry) RegisterBackend(be *BackendDef) {
	br.backendMutex.Lock()
	defer br.backendMutex.Unlock()
	br.registeredBackends = append(br.registeredBackends, be)
}

// BackendStart initializes all registered backends
func (br *BackendRegistry) BackendStart(pm *propcore.PropManager) error {
	br.backendMutex.RLock()
	defer br.backendMutex.RUnlock()

	for _, be := range br.registeredBackends {
		if be.Start != nil {
			if err := be.Start(pm); err != nil {
				return err
			}
		}
	}
	return nil
}

// BackendFini finalizes all registered backends
func (br *BackendRegistry) BackendFini(pm *propcore.PropManager) error {
	br.backendMutex.RLock()
	defer br.backendMutex.RUnlock()

	for _, be := range br.registeredBackends {
		if be.Fini != nil {
			if err := be.Fini(pm); err != nil {
				return err
			}
		}
	}
	return nil
}
