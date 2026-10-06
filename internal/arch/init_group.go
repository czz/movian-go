package arch

import (
	"cmp"
	"slices"
	"sync"
)

// Start group constants
const (
	InitGroupNet      = 1
	InitGroupGraphics = 2
	InitGroupIPC      = 3
	InitGroupAPI      = 4
	InitGroupAsyncIO  = 5 // C: INIT_GROUP_ASYNCIO (main.h:355)
)

// InitHelper represents an initialization helper
type InitHelper struct {
	Group int
	Prio  int
	Start func()
	Fini  func()
}

// InitGroupSystem manages initialization helpers without global state
type InitGroupSystem struct {
	initHelpers []*InitHelper
	setupMutex  sync.Mutex
}

// NewInitGroupSystem creates a new init group system
func NewInitGroupSystem() *InitGroupSystem {
	return &InitGroupSystem{
		initHelpers: make([]*InitHelper, 0),
	}
}

// RegisterInitHelper registers an initialization helper
func (igs *InitGroupSystem) RegisterInitHelper(ih *InitHelper) {
	igs.setupMutex.Lock()
	defer igs.setupMutex.Unlock()

	igs.initHelpers = append(igs.initHelpers, ih)
}

// InitGroup initializes all helpers in a specific group
func (igs *InitGroupSystem) InitGroup(group int) {
	igs.setupMutex.Lock()
	defer igs.setupMutex.Unlock()

	// Sort helpers by priority (lower priority = earlier initialization)
	slices.SortFunc(igs.initHelpers, func(a, b *InitHelper) int { return cmp.Compare(a.Prio, b.Prio) })

	for _, ih := range igs.initHelpers {
		if ih.Group == group && ih.Start != nil {
			ih.Start()
		}
	}
}

// FiniGroup finalizes all helpers in a specific group
func (igs *InitGroupSystem) FiniGroup(group int) {
	igs.setupMutex.Lock()
	defer igs.setupMutex.Unlock()

	// Sort helpers by priority in reverse order (higher priority = later finalization)
	slices.SortFunc(igs.initHelpers, func(a, b *InitHelper) int { return cmp.Compare(b.Prio, a.Prio) })

	for _, ih := range igs.initHelpers {
		if ih.Group == group && ih.Fini != nil {
			ih.Fini()
		}
	}
}
